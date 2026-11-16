package relocation

import (
	"bytes"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"golang.org/x/sys/unix"
)

type backupEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type backupManifest struct {
	Schema       string        `json:"schema"`
	PlanSHA256   string        `json:"plan_sha256"`
	Directory    Directory     `json:"directory"`
	SourceFiles  []File        `json:"source_files"`
	Entries      []backupEntry `json:"entries"`
	LedgerSHA256 string        `json:"ledger_sha256"`
}

const backupManifestName = "manifest.json"

func backupDirectory(p Plan, j Journal) (*held, error) {
	if j.BackupIdentity == nil {
		return nil, managed.ErrConflict
	}
	h, err := openDirectory(p.BackupDir)
	if err != nil {
		return nil, err
	}
	if h.record != *j.BackupIdentity || h.record.Mode&0077 != 0 {
		h.close()
		return nil, managed.ErrConflict
	}
	return h, nil
}
func bindBackup(root *held, current string, j *Journal, e Environment) error {
	p := j.Plan
	parent, err := openDirectory(filepath.Dir(p.BackupDir))
	if err != nil {
		return err
	}
	defer parent.close()
	if parent.record != p.BackupParentIdentity {
		return managed.ErrConflict
	}
	if j.BackupIdentity == nil {
		if err = absent(parent, filepath.Base(p.BackupDir)); err != nil {
			return managed.ErrConflict
		}
		if err = unix.Mkdirat(parent.fd(), filepath.Base(p.BackupDir), 0700); err != nil {
			return err
		}
		e.boundary("backup_mkdir")
		created, err := openAt(parent.fd(), filepath.Base(p.BackupDir), true)
		if err != nil {
			return err
		}
		defer created.close()
		if created.record.Mode&0077 != 0 || created.record.Identity.VolumeUUID != p.SourceRootIdentity.Identity.VolumeUUID {
			return managed.ErrConflict
		}
		if err = created.sync(); err != nil {
			return err
		}
		if err = parent.sync(); err != nil {
			return err
		}
		j.BackupIdentity = &created.record
		j.BackupState = "bound"
		if err = updateJournal(root, current, j, e); err != nil {
			return err
		}
		e.boundary("backup_bound")
	}
	return nil
}
func ensureBackupParents(root *held, relative string, base string, e Environment) (*held, func(), error) {
	current := root
	currentPath := base
	var opened []*held
	close := func() {
		for _, h := range opened {
			h.close()
		}
	}
	for _, part := range strings.Split(path.Dir(relative), "/") {
		if part == "." {
			continue
		}
		if err := unix.Mkdirat(current.fd(), part, 0700); err != nil && !os.IsExist(err) {
			close()
			return nil, nil, err
		}
		if err := syncDirectory(current, currentPath, e); err != nil {
			close()
			return nil, nil, err
		}
		h, err := openAt(current.fd(), part, true)
		if err != nil {
			close()
			return nil, nil, err
		}
		opened = append(opened, h)
		if h.record.Mode&0077 != 0 {
			close()
			return nil, nil, managed.ErrConflict
		}
		current = h
		currentPath = filepath.Join(currentPath, part)
	}
	return current, close, nil
}
func createBackup(root *held, current string, j *Journal, o *observation, e Environment) error {
	if err := bindBackup(root, current, j, e); err != nil {
		return err
	}
	backup, err := backupDirectory(j.Plan, *j)
	if err != nil {
		return err
	}
	defer backup.close()
	parent, err := openDirectory(filepath.Dir(j.Plan.BackupDir))
	if err != nil {
		return err
	}
	defer parent.close()
	if parent.record != j.Plan.BackupParentIdentity {
		return managed.ErrConflict
	}
	type payload struct {
		raw            []byte
		source, digest string
		absolute       bool
	}
	data := map[string]payload{"source-plan.json": {raw: canonical(j.Plan.SourcePlan)}, "target-plan.json": {raw: canonical(j.Plan.TargetPlan)}}
	for _, f := range j.Plan.Files {
		if f.Kind == "directory" {
			continue
		}
		if f.Kind != "file" {
			continue
		}
		source := f.Path
		if f.Path == JournalName && j.PredecessorArchive != nil {
			source = j.PredecessorArchive.Path
		}
		data[path.Join("payload", f.Path)] = payload{source: source, digest: f.SHA256}
	}
	data["source-binary"] = payload{source: j.Plan.SourcePlan.Binary, digest: j.Plan.SourcePlan.BinarySHA256, absolute: true}
	data["target-binary"] = payload{source: j.Plan.TargetPlan.Binary, digest: j.Plan.TargetPlan.BinarySHA256, absolute: true}
	if err = validatePartialBackup(backup, j.Plan); err != nil {
		return err
	}
	if raw, _, readErr := readRegular(backup.fd(), backupManifestName); readErr == nil {
		probe := *j
		probe.BackupState = "verified"
		probe.BackupManifestSHA256 = Digest(raw)
		if err = verifyBackup(j.Plan, probe); err != nil {
			return err
		}
		if err = syncBackupTree(backup, j.Plan, e); err != nil {
			return err
		}
		j.BackupState = probe.BackupState
		j.BackupManifestSHA256 = probe.BackupManifestSHA256
		j.Phase = "backup_verified"
		return updateJournal(root, current, j, e)
	} else if !os.IsNotExist(readErr) {
		return readErr
	}

	for _, f := range j.Plan.Files {
		if f.Kind == "directory" {
			_, close, err := ensureBackupParents(backup, path.Join("payload", f.Path, ".entry"), j.Plan.BackupDir, e)
			if err != nil {
				return err
			}
			close()
		}
	}
	// An existing engine backup is usable only inside this exact durably bound
	// directory and only after SQLite proves the original logical inventory.
	database, _, err := readRegular(backup.fd(), "database.sqlite")
	if os.IsNotExist(err) {
		database, err = o.db.BackupBytes()
	} else if err != nil {
		return err
	}
	if err != nil {
		return err
	}
	check, err := sqlite.Observe(database, nil)
	if err != nil {
		return err
	}
	digest, err := sqlite.LogicalDigest(check.Tx)
	end := check.Close()
	if err != nil || end != nil || digest != j.Plan.LedgerSHA256 {
		return managed.ErrConflict
	}
	data["database.sqlite"] = payload{raw: database}
	names := make([]string, 0, len(data))
	for name := range data {
		names = append(names, name)
	}
	sort.Strings(names)
	manifest := backupManifest{"haepari.service-relocation-backup/v1", j.PlanSHA256, *j.BackupIdentity, j.Plan.Files, nil, j.Plan.LedgerSHA256}
	for _, name := range names {
		dest, close, err := ensureBackupParents(backup, name, j.Plan.BackupDir, e)
		if err != nil {
			return err
		}
		item := data[name]
		raw := item.raw
		if item.source != "" {
			if item.absolute {
				raw, _, err = readRegular(unix.AT_FDCWD, item.source)
			} else {
				raw, _, err = readRelative(root, item.source)
			}
			if err != nil || Digest(raw) != item.digest {
				close()
				return managed.ErrConflict
			}
		}
		old, f, err := readRegular(dest.fd(), path.Base(name))
		if os.IsNotExist(err) {
			e.boundary("before_backup_payload:" + name)
			err = publish(parent, filepath.Dir(j.Plan.BackupDir), dest, filepath.Join(j.Plan.BackupDir, filepath.FromSlash(path.Dir(name))), path.Base(name), raw, nil, nil, e)
		} else if err == nil && (!bytes.Equal(old, raw) || f.Mode&0077 != 0) {
			err = managed.ErrConflict
		}
		close()
		if err != nil {
			return err
		}
		manifest.Entries = append(manifest.Entries, backupEntry{name, Digest(raw), int64(len(raw))})
		e.boundary("backup_payload")
	}
	if err = syncBackupTree(backup, j.Plan, e); err != nil {
		return err
	}
	expected := canonical(manifest)
	raw, file, err := readRegular(backup.fd(), backupManifestName)
	if os.IsNotExist(err) {
		e.boundary("before_backup_manifest")
		err = publish(parent, filepath.Dir(j.Plan.BackupDir), backup, j.Plan.BackupDir, backupManifestName, expected, nil, nil, e)
	} else if err == nil && (!bytes.Equal(raw, expected) || file.Mode&0077 != 0) {
		err = managed.ErrConflict
	}
	if err != nil {
		return err
	}
	e.boundary("backup_manifest")
	j.BackupManifestSHA256 = Digest(expected)
	j.BackupState = "verified"
	if err = verifyBackup(j.Plan, *j); err != nil {
		err = fail("backup_verification", err)
		return err
	}
	if err = syncBackupTree(backup, j.Plan, e); err != nil {
		return err
	}
	j.Phase = "backup_verified"
	return updateJournal(root, current, j, e)
}
func verifyBackup(p Plan, j Journal) error {
	backup, err := backupDirectory(p, j)
	if err != nil {
		return err
	}
	defer backup.close()
	raw, file, err := readRegular(backup.fd(), backupManifestName)
	if err != nil || file.Mode&0077 != 0 || Digest(raw) != j.BackupManifestSHA256 {
		return managed.ErrConflict
	}
	var m backupManifest
	if Decode(raw, &m) != nil || m.Schema != "haepari.service-relocation-backup/v1" || m.PlanSHA256 != p.PlanSHA256 || m.Directory != *j.BackupIdentity || !equalFiles(m.SourceFiles, p.Files) || m.LedgerSHA256 != p.LedgerSHA256 {
		return managed.ErrConflict
	}
	expected := map[string]bool{backupManifestName: true}
	expectedDirs := map[string]bool{}
	required := map[string]string{"database.sqlite": "", "source-plan.json": Digest(canonical(p.SourcePlan)), "target-plan.json": Digest(canonical(p.TargetPlan)), "source-binary": p.SourcePlan.BinarySHA256, "target-binary": p.TargetPlan.BinarySHA256}
	for _, f := range p.Files {
		if f.Kind == "file" {
			required[path.Join("payload", f.Path)] = f.SHA256
		}
	}
	if len(m.Entries) != len(required) {
		return managed.ErrConflict
	}
	for _, entry := range m.Entries {
		if !filepath.IsLocal(entry.Path) || path.Clean(entry.Path) != entry.Path || expected[entry.Path] || !digestPattern.MatchString(entry.SHA256) {
			return managed.ErrConflict
		}
		bound, exists := required[entry.Path]
		if !exists || bound != "" && bound != entry.SHA256 {
			return managed.ErrConflict
		}
		expected[entry.Path] = true
		raw, f, err := readRelative(backup, entry.Path)
		if err != nil || f.Mode&0077 != 0 || int64(len(raw)) != entry.Size || Digest(raw) != entry.SHA256 {
			return managed.ErrConflict
		}
		for dir := path.Dir(entry.Path); dir != "."; dir = path.Dir(dir) {
			expectedDirs[dir] = true
		}
	}
	for _, source := range p.Files {
		if source.Kind == "directory" {
			for dir := path.Join("payload", source.Path); dir != "."; dir = path.Dir(dir) {
				expectedDirs[dir] = true
			}
		}
	}
	files, err := inventory(backup, p.BackupDir)
	if err != nil {
		return err
	}
	if len(files) != len(expected)+len(expectedDirs) {
		return managed.ErrConflict
	}
	for _, f := range files {
		if f.Kind == "directory" {
			if !expectedDirs[f.Path] || f.Mode&0077 != 0 {
				return managed.ErrConflict
			}
		} else if f.Kind != "file" || !expected[f.Path] {
			return managed.ErrConflict
		}
	}
	for _, pair := range []struct{ name, digest string }{{"source-plan.json", Digest(canonical(p.SourcePlan))}, {"target-plan.json", Digest(canonical(p.TargetPlan))}, {"source-binary", p.SourcePlan.BinarySHA256}, {"target-binary", p.TargetPlan.BinarySHA256}} {
		raw, _, err := readRelative(backup, pair.name)
		if err != nil || Digest(raw) != pair.digest {
			return managed.ErrConflict
		}
	}
	db, _, err := readRelative(backup, "database.sqlite")
	if err != nil {
		return err
	}
	o, err := sqlite.Observe(db, nil)
	if err != nil {
		return err
	}
	logical, err := sqlite.LogicalDigest(o.Tx)
	end := o.Close()
	if err != nil || end != nil || logical != p.LedgerSHA256 {
		return managed.ErrConflict
	}
	return nil
}
func backupState(p Plan, j Journal) string {
	if j.BackupIdentity == nil {
		if _, err := os.Lstat(p.BackupDir); !errors.Is(err, os.ErrNotExist) {
			return "unproven"
		}
		return "not_created"
	}
	return j.BackupState
}

func validatePartialBackup(backup *held, p Plan) error {
	files := map[string]bool{"source-plan.json": true, "target-plan.json": true, "source-binary": true, "target-binary": true, "database.sqlite": true, backupManifestName: true}
	dirs := map[string]bool{}
	for _, f := range p.Files {
		if f.Kind == "socket" {
			continue
		}
		name := path.Join("payload", f.Path)
		if f.Kind == "file" {
			files[name] = true
		} else {
			dirs[name] = true
		}
		for d := path.Dir(name); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	entries, err := inventory(backup, p.BackupDir)
	if err != nil {
		return err
	}
	for _, f := range entries {
		if f.Mode&0077 != 0 {
			return managed.ErrConflict
		}
		if f.Kind == "directory" {
			if !dirs[f.Path] {
				return managed.ErrConflict
			}
		} else if f.Kind != "file" || !files[f.Path] {
			return managed.ErrConflict
		}
	}
	return nil
}

// Child-first sync completes interrupted payload/manifest publications. Unknown
// entries or changed bound directories still refuse; no staging is adopted.
func syncBackupTree(backup *held, p Plan, e Environment) error {
	if backup.matches(unix.AT_FDCWD, p.BackupDir) != nil {
		return managed.ErrConflict
	}
	if err := validatePartialBackup(backup, p); err != nil {
		return err
	}
	files, err := inventory(backup, p.BackupDir)
	if err != nil {
		return err
	}
	// Descendants sort after their parent, so reverse order syncs children first.
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		full := filepath.Join(p.BackupDir, filepath.FromSlash(f.Path))
		if f.Kind == "directory" {
			h, err := openDirectory(full)
			if err != nil {
				return err
			}
			if h.record != (Directory{f.Identity, f.OwnerUID, f.Mode}) {
				h.close()
				return managed.ErrConflict
			}
			err = syncDirectory(h, full, e)
			h.close()
			if err != nil {
				return err
			}
		} else if f.Kind == "file" {
			parent, err := openDirectory(filepath.Dir(full))
			if err != nil {
				return err
			}
			err = syncRegular(parent, filepath.Base(full), f)
			parent.close()
			if err != nil {
				return err
			}
		} else {
			return managed.ErrConflict
		}
	}
	if err = syncDirectory(backup, p.BackupDir, e); err != nil {
		return err
	}
	parent, err := openDirectory(filepath.Dir(p.BackupDir))
	if err != nil {
		return err
	}
	defer parent.close()
	if parent.record != p.BackupParentIdentity {
		return managed.ErrConflict
	}
	return syncDirectory(parent, filepath.Dir(p.BackupDir), e)
}
