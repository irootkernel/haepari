// Package snapshot seals explicitly selected regular files into an owned,
// content-addressed input object. It never reads Git state or walks a source
// tree to discover inputs.
package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	manifestSchema = "haepari.snapshot/v1"
	treeAlgorithm  = "haepari-tree-sha256/v1"
	maxEntries     = 1000
	maxPathBytes   = 512
)

var ErrInvalid = errors.New("invalid snapshot input")
var ErrChanged = errors.New("source changed during snapshot")
var ErrCorrupt = errors.New("sealed snapshot is corrupt")
var ErrSourceUnavailable = errors.New("snapshot source unavailable")

type Entry struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
	SHA256     string `json:"sha256"`
}

// Provenance is caller supplied. The base commit is an observation about the
// checkout, never a substitute for the digest of selected bytes.
type Provenance struct {
	EvidenceClass        string `json:"evidence_class"`
	BaseCommit           string `json:"base_commit,omitempty"`
	ReleaseReference     string `json:"release_reference,omitempty"`
	ReleaseArchiveSHA256 string `json:"release_archive_sha256,omitempty"`
}

type Manifest struct {
	Schema     string     `json:"schema"`
	Algorithm  string     `json:"algorithm"`
	TreeSHA256 string     `json:"tree_sha256"`
	Provenance Provenance `json:"provenance"`
	Entries    []Entry    `json:"entries"`
}

// ObjectID binds the complete manifest, including the separately recorded
// provenance. TreeSHA256 binds only selected paths, modes and bytes.
type Snapshot struct {
	ObjectID string
	Root     string
	Manifest Manifest
}

type Store struct{ root string }

// OpenStore requires an already prepared, private state directory. Only this
// directory and its objects child are written by the snapshot package.
func OpenStore(stateRoot string) (*Store, error) {
	if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot {
		return nil, fmt.Errorf("%w: state root must be absolute and clean", ErrInvalid)
	}
	info, err := os.Lstat(stateRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%w: state root must be a private real directory", ErrInvalid)
	}
	objects := filepath.Join(stateRoot, "objects")
	if err := os.Mkdir(objects, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err = os.Lstat(objects)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%w: snapshot store is not a private real directory", ErrInvalid)
	}
	// The object parent and the state-root entry must be durable before a
	// later Seal can return an object ID that admission will reference.
	if err := syncDir(stateRoot); err != nil {
		return nil, err
	}
	if err := syncDir(filepath.Dir(stateRoot)); err != nil {
		return nil, err
	}
	store := &Store{root: objects}
	lock, err := acquireStoreLock(objects)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := store.recoverStaging(); err != nil {
		return nil, err
	}
	return store, nil
}

// Seal copies exactly selected into a private temporary directory, checks the
// source after copying, and publishes a complete object with one rename.
func (s *Store) Seal(ctx context.Context, sourceRoot string, selected []string, provenance Provenance) (Snapshot, error) {
	if err := validateSelection(selected); err != nil {
		return Snapshot{}, err
	}
	if sensitivePath(filepath.ToSlash(sourceRoot)) {
		return Snapshot{}, fmt.Errorf("%w: source root contains a credential path", ErrInvalid)
	}
	if err := validateProvenance(provenance); err != nil {
		return Snapshot{}, err
	}
	source, err := openSourceRoot(sourceRoot)
	if err != nil {
		return Snapshot{}, err
	}
	defer source.Close()
	if sensitivePath(filepath.ToSlash(source.canonical)) {
		return Snapshot{}, fmt.Errorf("%w: resolved source root contains a credential path", ErrInvalid)
	}
	lock, err := acquireStoreLock(s.root)
	if err != nil {
		return Snapshot{}, err
	}
	defer lock.Close()
	stage, err := os.MkdirTemp(s.root, ".staging-")
	if err != nil {
		return Snapshot{}, err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0700); err != nil {
		return Snapshot{}, err
	}
	files := filepath.Join(stage, "files")
	if err := os.Mkdir(files, 0700); err != nil {
		return Snapshot{}, err
	}
	entries := make([]Entry, 0, len(selected))
	versions := make(map[string]sourceVersion, len(selected))
	for _, name := range selected {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		entry, version, err := copySelected(ctx, source, name, files)
		if err != nil {
			return Snapshot{}, fmt.Errorf("copy %s: %w", name, err)
		}
		entries = append(entries, entry)
		versions[name] = version
	}
	for _, entry := range entries {
		if err := recheckSelected(ctx, source, entry, versions[entry.Path]); err != nil {
			return Snapshot{}, fmt.Errorf("recheck %s: %w", entry.Path, err)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	manifest := Manifest{Schema: manifestSchema, Algorithm: treeAlgorithm, TreeSHA256: treeDigest(entries), Provenance: provenance, Entries: entries}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return Snapshot{}, err
	}
	id := "sha256:" + digest(encoded)
	manifestFile, err := os.OpenFile(filepath.Join(stage, "manifest.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err = manifestFile.Write(encoded); err == nil {
		err = manifestFile.Sync()
	}
	if closeErr := manifestFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Snapshot{}, err
	}
	if err := syncTree(stage, entries); err != nil {
		return Snapshot{}, err
	}
	final := filepath.Join(s.root, strings.TrimPrefix(id, "sha256:"))
	if err := os.Rename(stage, final); err != nil {
		if _, statErr := os.Lstat(final); statErr != nil {
			return Snapshot{}, err
		}
		// An identical concurrent seal may already have published the object.
		return s.Read(id)
	}
	if err := syncDir(s.root); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{ObjectID: id, Root: filepath.Join(final, "files"), Manifest: manifest}, nil
}

// Read verifies the manifest identity and every selected file before returning
// the owned root. Consumers should reverify immediately before execution.
func (s *Store) Read(id string) (Snapshot, error) {
	if !validDigest(id) {
		return Snapshot{}, fmt.Errorf("%w: object ID", ErrInvalid)
	}
	object := filepath.Join(s.root, strings.TrimPrefix(id, "sha256:"))
	info, err := os.Lstat(object)
	if err != nil {
		return Snapshot{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Snapshot{}, ErrCorrupt
	}
	objectRoot, err := openSourceRoot(object)
	if err != nil {
		return Snapshot{}, ErrCorrupt
	}
	manifestFile, version, err := objectRoot.openRegular("manifest.json")
	if err != nil {
		objectRoot.Close()
		return Snapshot{}, ErrCorrupt
	}
	encoded, err := io.ReadAll(io.LimitReader(manifestFile, 1<<20+1))
	after, statErr := sourceVersionOf(manifestFile)
	closeErr := manifestFile.Close()
	objectRoot.Close()
	if err != nil || statErr != nil || closeErr != nil || version != after || len(encoded) > 1<<20 || "sha256:"+digest(encoded) != id {
		return Snapshot{}, ErrCorrupt
	}
	manifest, err := DecodeInventory(encoded, id)
	if err != nil {
		return Snapshot{}, err
	}
	files := filepath.Join(object, "files")
	root, err := openSourceRoot(files)
	if err != nil {
		return Snapshot{}, ErrCorrupt
	}
	defer root.Close()
	for _, entry := range manifest.Entries {
		file, before, err := root.openRegular(entry.Path)
		if err != nil {
			return Snapshot{}, ErrCorrupt
		}
		h := sha256.New()
		size, copyErr := io.Copy(h, file)
		after, statErr := sourceVersionOf(file)
		closeErr := file.Close()
		if copyErr != nil || statErr != nil || closeErr != nil || before != after || size != entry.Size ||
			"sha256:"+hex.EncodeToString(h.Sum(nil)) != entry.SHA256 || before.executable != entry.Executable {
			return Snapshot{}, ErrCorrupt
		}
	}
	if err := verifyInventory(files, manifest.Entries); err != nil {
		return Snapshot{}, ErrCorrupt
	}
	return Snapshot{ObjectID: id, Root: files, Manifest: manifest}, nil
}

// DecodeInventory verifies retained manifest bytes against the original object
// identity without requiring payload files, which retention may have removed.
func DecodeInventory(encoded []byte, objectID string) (Manifest, error) {
	if len(encoded) > 1<<20 || !validDigest(objectID) || "sha256:"+digest(encoded) != objectID {
		return Manifest{}, ErrCorrupt
	}
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return Manifest{}, ErrCorrupt
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || string(canonical) != string(encoded) || manifest.Schema != manifestSchema || manifest.Algorithm != treeAlgorithm || validateProvenance(manifest.Provenance) != nil || validateEntries(manifest.Entries) != nil || treeDigest(manifest.Entries) != manifest.TreeSHA256 {
		return Manifest{}, ErrCorrupt
	}
	return manifest, nil
}

func verifyInventory(files string, entries []Entry) error {
	wantedFiles := make(map[string]bool, len(entries))
	wantedDirs := map[string]bool{".": true}
	for _, entry := range entries {
		wantedFiles[entry.Path] = true
		for dir := path.Dir(entry.Path); dir != "."; dir = path.Dir(dir) {
			wantedDirs[dir] = true
		}
	}
	return filepath.WalkDir(files, func(name string, item os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(files, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if item.IsDir() {
			if !wantedDirs[rel] {
				return ErrCorrupt
			}
		} else if !wantedFiles[rel] || !item.Type().IsRegular() {
			return ErrCorrupt
		}
		return nil
	})
}

func validateProvenance(p Provenance) error {
	if p.BaseCommit != "" && !validCommit(p.BaseCommit) {
		return ErrInvalid
	}
	switch p.EvidenceClass {
	case "development":
		if p.ReleaseReference != "" || p.ReleaseArchiveSHA256 != "" {
			return ErrInvalid
		}
	case "candidate-integration":
		if p.BaseCommit == "" || p.ReleaseReference != "" || p.ReleaseArchiveSHA256 != "" {
			return ErrInvalid
		}
	case "release-distribution":
		if p.BaseCommit == "" || p.ReleaseReference == "" || !validDigest(p.ReleaseArchiveSHA256) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validateSelection(names []string) error {
	if len(names) == 0 || len(names) > maxEntries {
		return ErrInvalid
	}
	for i, name := range names {
		if !validPath(name) || sensitivePath(name) {
			return fmt.Errorf("%w: selected path %q", ErrInvalid, name)
		}
		for _, earlier := range names[:i] {
			if collides(name, earlier) {
				return fmt.Errorf("%w: selected paths collide", ErrInvalid)
			}
		}
	}
	return nil
}

func validateEntries(entries []Entry) error {
	if len(entries) == 0 || len(entries) > maxEntries {
		return ErrInvalid
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		if entry.Size < 0 || !validDigest(entry.SHA256) || i > 0 && entries[i-1].Path >= entry.Path {
			return ErrInvalid
		}
		names[i] = entry.Path
	}
	return validateSelection(names)
}

func validPath(name string) bool {
	if name == "" || len(name) > maxPathBytes || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00") ||
		path.IsAbs(name) || path.Clean(name) != name || name == "." {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func collides(a, b string) bool {
	partsA, partsB := strings.Split(a, "/"), strings.Split(b, "/")
	n := min(len(partsA), len(partsB))
	for i := 0; i < n; i++ {
		if !strings.EqualFold(partsA[i], partsB[i]) {
			return false
		}
		if partsA[i] != partsB[i] {
			return true
		}
	}
	return len(partsA) == n || len(partsB) == n
}

func sensitivePath(name string) bool {
	for _, part := range strings.Split(strings.ToLower(name), "/") {
		switch part {
		case ".ssh", ".aws", ".gnupg", ".kube", ".codex", ".azure", ".docker", "gcloud", ".npmrc", ".pypirc", ".netrc", ".env", "credentials", "credentials.json", "application_default_credentials.json", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa":
			return true
		}
		if strings.HasPrefix(part, ".env.") || strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") || strings.HasSuffix(part, ".p12") || strings.HasSuffix(part, ".pfx") {
			return true
		}
	}
	return false
}

func (s *Store) recoverStaging() error {
	pending, err := s.pendingCleanups()
	if err != nil {
		return err
	}
	items, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, item := range items {
		if !strings.HasPrefix(item.Name(), ".staging-") {
			continue
		}
		seen[item.Name()] = true
		if !item.IsDir() || item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: unexpected staging entry %q", ErrCorrupt, item.Name())
		}
		if !pending[item.Name()] {
			if err := s.recordCleanup(item.Name(), "intent", time.Now().UTC()); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(filepath.Join(s.root, item.Name())); err != nil {
			return fmt.Errorf("remove orphan staging %q: %w", item.Name(), err)
		}
		if err := s.recordCleanup(item.Name(), "complete", time.Now().UTC()); err != nil {
			return err
		}
	}
	for name := range pending {
		if !seen[name] {
			// A previous recovery removed the directory but stopped before its
			// completion record. Preserve the durable intent and finish it.
			if err := s.recordCleanup(name, "complete", time.Now().UTC()); err != nil {
				return err
			}
		}
	}
	return syncDir(s.root)
}

type cleanupRecord struct {
	Schema     string `json:"schema"`
	Name       string `json:"name"`
	Phase      string `json:"phase"`
	RecordedAt string `json:"recorded_at"`
}

func (s *Store) recordCleanup(name, phase string, at time.Time) error {
	entry, err := json.Marshal(cleanupRecord{"haepari.staging-cleanup/v1", name, phase, at.Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	file, err := openPrivateAppend(filepath.Join(s.root, "staging-cleanup.jsonl"))
	if err != nil {
		return err
	}
	if _, err = file.Write(append(entry, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return syncDir(s.root)
}

func (s *Store) pendingCleanups() (map[string]bool, error) {
	pending := make(map[string]bool)
	name := filepath.Join(s.root, "staging-cleanup.jsonl")
	if _, err := os.Lstat(name); os.IsNotExist(err) {
		return pending, nil
	} else if err != nil {
		return nil, err
	}
	file, err := openPrivateReadWrite(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 16<<20+1))
	if err != nil || len(data) > 16<<20 {
		return nil, ErrCorrupt
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		last := bytes.LastIndexByte(data, '\n') + 1
		if err := file.Truncate(int64(last)); err != nil {
			return nil, err
		}
		if err := file.Sync(); err != nil {
			return nil, err
		}
		data = data[:last]
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var record cleanupRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, ErrCorrupt
		}
		canonical, _ := json.Marshal(record)
		if !bytes.Equal(line, canonical) || record.Schema != "haepari.staging-cleanup/v1" ||
			!strings.HasPrefix(record.Name, ".staging-") || filepath.Base(record.Name) != record.Name ||
			(record.Phase != "intent" && record.Phase != "complete") {
			return nil, ErrCorrupt
		}
		if _, err := time.Parse(time.RFC3339Nano, record.RecordedAt); err != nil {
			return nil, ErrCorrupt
		}
		pending[record.Name] = record.Phase == "intent"
	}
	for name, active := range pending {
		if !active {
			delete(pending, name)
		}
	}
	return pending, nil
}

func validDigest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	for _, c := range s[7:] {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func validCommit(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func treeDigest(entries []Entry) string {
	h := sha256.New()
	h.Write([]byte(treeAlgorithm + "\x00"))
	var size [8]byte
	for _, entry := range entries {
		name := []byte(entry.Path)
		binary.BigEndian.PutUint64(size[:], uint64(len(name)))
		h.Write(size[:])
		h.Write(name)
		if entry.Executable {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
		binary.BigEndian.PutUint64(size[:], uint64(entry.Size))
		h.Write(size[:])
		content, _ := hex.DecodeString(strings.TrimPrefix(entry.SHA256, "sha256:"))
		h.Write(content)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func syncDir(name string) error {
	dir, err := os.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func syncTree(stage string, entries []Entry) error {
	dirs := map[string]bool{stage: true, filepath.Join(stage, "files"): true}
	for _, entry := range entries {
		for dir := filepath.Dir(filepath.Join(stage, "files", filepath.FromSlash(entry.Path))); dir != stage && dir != "."; dir = filepath.Dir(dir) {
			dirs[dir] = true
		}
	}
	ordered := make([]string, 0, len(dirs))
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, dir := range ordered {
		if err := syncDir(dir); err != nil {
			return err
		}
	}
	return nil
}
