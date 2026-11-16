package relocation

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/store/sqlite"
)

type observation struct {
	db                   *sqlite.Observation
	files                []File
	installation, digest string
}

func observe(root *held, current string, service ServicePlan) (*observation, error) {
	before, err := inventory(root, current)
	if err != nil {
		return nil, err
	}
	database, _, err := readRegular(root.fd(), "state.sqlite")
	if err != nil {
		return nil, err
	}
	wal, _, err := readRegular(root.fd(), "state.sqlite-wal")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if errors.Is(err, os.ErrNotExist) {
		wal = nil
	}
	db, err := sqlite.Observe(database, wal)
	if err != nil {
		return nil, err
	}
	o := &observation{db: db, files: before}
	ok := false
	defer func() {
		if !ok {
			_ = db.Close()
		}
	}()
	o.installation, err = managed.RelocationObservation(db.Tx, current)
	if err != nil {
		return nil, err
	}
	o.digest, err = sqlite.LogicalDigest(db.Tx)
	if err != nil {
		return nil, err
	}
	if err = verifyEvidence(root, db.Tx, before); err != nil {
		return nil, err
	}
	loaded, _, err := config.Load(current, config.Sources{GlobalLimit: service.GlobalLimit, HostLimit: service.HostMACOSGuestLimit})
	if err != nil {
		return nil, err
	}
	var stored []byte
	err = db.Tx.QueryRow("SELECT document_json FROM managed_effective_config WHERE singleton=1").Scan(&stored)
	if err == nil {
		var doc config.Document
		if json.Unmarshal(stored, &doc) != nil {
			return nil, managed.ErrInvalid
		}
		_, _, values, e := doc.Digests()
		if e != nil {
			return nil, e
		}
		_, _, selected, e := loaded.Digests()
		if e != nil || values != selected {
			return nil, managed.ErrBlocked
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		var version int
		if e := db.Tx.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version >= 11 {
			return nil, err
		}
	}
	after, err := inventory(root, current)
	if err != nil || !equalFiles(before, after) {
		return nil, managed.ErrConflict
	}
	ok = true
	return o, nil
}
func verifyEvidence(root *held, tx *sql.Tx, files []File) error {
	indexed := map[string]File{}
	for _, f := range files {
		indexed[f.Path] = f
		if strings.HasPrefix(f.Path, "objects/.staging-") {
			return managed.ErrBlocked
		}
	}
	if f, ok := indexed["objects/staging-cleanup.jsonl"]; ok {
		raw, _, e := readRelative(root, f.Path)
		if e != nil {
			return e
		}
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			return managed.ErrBlocked
		}
		pending := map[string]bool{}
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var r struct {
				Schema     string `json:"schema"`
				Name       string `json:"name"`
				Phase      string `json:"phase"`
				RecordedAt string `json:"recorded_at"`
			}
			if Decode(line, &r) != nil || r.Schema != "haepari.staging-cleanup/v1" || r.Phase != "intent" && r.Phase != "complete" {
				return managed.ErrInvalid
			}
			pending[r.Name] = r.Phase == "intent"
		}
		for _, v := range pending {
			if v {
				return managed.ErrBlocked
			}
		}
	}
	rows, err := tx.Query("SELECT job_id,object_id,inventory_json,retention_state FROM managed_admissions ORDER BY job_id")
	if err != nil {
		return err
	}
	type admission struct {
		job, object, retention string
		raw                    []byte
	}
	var admissions []admission
	for rows.Next() {
		var a admission
		if err = rows.Scan(&a.job, &a.object, &a.raw, &a.retention); err != nil {
			rows.Close()
			return err
		}
		admissions = append(admissions, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, a := range admissions {
		if len(a.raw) == 0 && a.retention != "unavailable" {
			var readErr error
			a.raw, _, readErr = readRelative(root, "objects/"+strings.TrimPrefix(a.object, "sha256:")+"/manifest.json")
			if readErr != nil {
				return readErr
			}
		}
		manifest, e := snapshot.DecodeInventory(a.raw, a.object)
		if e != nil {
			return e
		}
		// Purge removes payloads, not their permanent admission manifest binding.
		if a.retention == "unavailable" {
			continue
		}

		prefix := "objects/" + strings.TrimPrefix(a.object, "sha256:")
		for _, entry := range manifest.Entries {
			f, ok := indexed[prefix+"/files/"+entry.Path]
			if !ok || f.Kind != "file" || f.Size == nil || *f.Size != entry.Size || f.SHA256 != entry.SHA256 {
				return managed.ErrConflict
			}
		}
		var raw []byte
		if e = tx.QueryRow("SELECT result_json FROM managed_jobs WHERE id=?", a.job).Scan(&raw); errors.Is(e, sql.ErrNoRows) {
			continue
		} else if e != nil {
			return e
		}
		var result managed.Result
		if Decode(raw, &result) != nil {
			return managed.ErrInvalid
		}
		for _, entry := range result.Artifacts {
			f, ok := indexed[path.Join("jobs", a.job, "artifacts", entry.Path)]
			if !ok || f.Size == nil || *f.Size != entry.SizeBytes || f.SHA256 != "sha256:"+entry.SHA256 {
				return managed.ErrConflict
			}
		}
		for _, entry := range result.Logs {
			f, ok := indexed[path.Join("jobs", a.job, entry.Path)]
			if !ok || f.Size == nil || *f.Size != entry.SizeBytes || f.SHA256 != "sha256:"+entry.SHA256 {
				return managed.ErrConflict
			}
		}
	}
	return nil
}
func readRelative(root *held, name string) ([]byte, File, error) {
	if !filepath.IsLocal(name) || filepath.ToSlash(filepath.Clean(name)) != name {
		return nil, File{}, managed.ErrConflict
	}
	parent := root
	var opened []*held
	defer func() {
		for _, h := range opened {
			h.close()
		}
	}()
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		h, err := openAt(parent.fd(), part, true)
		if err != nil {
			return nil, File{}, err
		}
		opened = append(opened, h)
		parent = h
	}
	raw, f, err := readRegular(parent.fd(), parts[len(parts)-1])
	f.Path = name
	return raw, f, err
}
func validatePairShape(p Plan) error {
	a, b := p.SourcePlan, p.TargetPlan
	if a.Schema != "haepari.release-service-plan/v1" && a.Schema != "haepari.release-service-plan/v2" || b.Schema != "haepari.release-service-plan/v2" || a.Instance != b.Instance || a.Label != b.Label || a.Domain != b.Domain || a.OwnerUID != b.OwnerUID || a.PlistPath != b.PlistPath || a.GlobalLimit != b.GlobalLimit || a.HostMACOSGuestLimit != b.HostMACOSGuestLimit || a.GlobalLimit < 1 || a.HostMACOSGuestLimit < 1 || a.HostMACOSGuestLimit > 2 || a.Root == b.Root {
		return managed.ErrInvalid
	}
	for _, s := range []string{a.Root, b.Root, p.BackupDir} {
		if !filepath.IsAbs(s) || filepath.Clean(s) != s {
			return managed.ErrInvalid
		}
	}
	for i, x := range []string{a.Root, b.Root, p.BackupDir} {
		for j, y := range []string{a.Root, b.Root, p.BackupDir} {
			if i != j && contained(x, y) {
				return managed.ErrInvalid
			}
		}
		for _, binary := range []string{a.Binary, b.Binary} {
			if contained(x, binary) {
				return managed.ErrInvalid
			}
		}
	}
	return nil
}
func validatePairs(p Plan, e Environment) error {
	if err := validatePairShape(p); err != nil {
		return err
	}
	a, b := p.SourcePlan, p.TargetPlan
	if e.ValidateService == nil || e.ServicesStopped == nil {
		return managed.ErrInvalid
	}
	if err := e.ValidateService(a); err != nil {
		if os.IsNotExist(err) || errors.Is(err, managed.ErrInvalid) {
			return managed.ErrConflict
		}
		return err
	}
	if err := e.ValidateService(b); err != nil {
		if os.IsNotExist(err) || errors.Is(err, managed.ErrInvalid) {
			return managed.ErrConflict
		}
		return err
	}
	if e.MoverBinary != b.Binary {
		return managed.ErrConflict
	}
	for _, s := range []ServicePlan{a, b} {
		physical, err := filepath.EvalSymlinks(s.Binary)
		if err != nil || physical != s.Binary {
			return managed.ErrConflict
		}
		parent, err := openDirectory(filepath.Dir(s.Binary))
		if err != nil {
			return err
		}
		raw, executable, err := readRegular(parent.fd(), filepath.Base(s.Binary))
		parent.close()
		if err != nil || executable.Mode&0111 == 0 || Digest(raw) != s.BinarySHA256 {
			return managed.ErrConflict
		}
		if err := e.ServicesStopped(s); err != nil {
			return err
		}
		if _, err := os.Lstat(s.PlistPath); !os.IsNotExist(err) {
			if err == nil {
				return managed.ErrBlocked
			}
			return err
		}
	}
	return nil
}
func acquire(root *held) (func(), error) {
	controller, err := lockExisting(root, "controller.lock")
	if err != nil {
		return nil, err
	}
	objects, err := openAt(root.fd(), "objects", true)
	if err != nil {
		controller.close()
		return nil, err
	}
	store, err := lockExisting(objects, ".seal.lock")
	if err != nil {
		objects.close()
		controller.close()
		return nil, err
	}
	return func() { store.close(); objects.close(); controller.close() }, nil
}
func locate(p Plan) (*held, string, error) {
	var found *held
	var current string
	for _, candidate := range []string{p.SourcePlan.Root, p.TargetPlan.Root} {
		h, err := openDirectory(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if found != nil {
				found.close()
			}
			return nil, "", err
		}
		if h.record != p.SourceRootIdentity {
			h.close()
			continue
		}
		if found != nil {
			h.close()
			found.close()
			return nil, "", managed.ErrConflict
		}
		found, current = h, candidate
	}
	if found == nil {
		return nil, "", managed.ErrConflict
	}
	return found, current, nil
}
func checkParents(p Plan) error {
	volume := p.SourceRootIdentity.Identity.VolumeUUID
	for _, pair := range []struct {
		path   string
		record Directory
	}{{filepath.Dir(p.SourcePlan.Root), p.SourceParentIdentity}, {filepath.Dir(p.TargetPlan.Root), p.TargetParentIdentity}, {filepath.Dir(p.BackupDir), p.BackupParentIdentity}} {
		h, err := openDirectory(pair.path)
		if err != nil {
			return err
		}
		valid := h.record == pair.record && h.record.Identity.VolumeUUID == volume
		h.close()
		if !valid {
			return managed.ErrConflict
		}
	}
	return nil
}
