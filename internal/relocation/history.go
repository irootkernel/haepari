package relocation

import (
	"bytes"
	"fmt"
	"golang.org/x/text/unicode/norm"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"golang.org/x/sys/unix"
)

func archiveName(p Predecessor) string {
	return p.RelocationID + "-" + strings.TrimPrefix(p.JournalSHA256, "sha256:") + ".json"
}
func predecessorMatches(p Plan, j Journal, file File, raw []byte, current string) bool {
	b := p.Predecessor
	return b != nil && terminal(j) && j.Plan.InstallationID == p.InstallationID && j.Plan.RelocationID == b.RelocationID && j.PlanSHA256 == b.PlanSHA256 && j.Sequence == b.Sequence && file.Identity == b.JournalIdentity && Digest(raw) == b.JournalSHA256 && current == b.CurrentRoot
}
func archivePredecessor(root *held, current string, p Plan, prior Journal, file File, raw []byte, e Environment) (*Archive, error) {
	if !predecessorMatches(p, prior, file, raw, current) {
		return nil, managed.ErrConflict
	}
	if err := unix.Mkdirat(root.fd(), HistoryName, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if err := root.sync(); err != nil {
		return nil, err
	}
	e.boundary("history_directory")
	history, err := openAt(root.fd(), HistoryName, true)
	if err != nil {
		return nil, err
	}
	defer history.close()
	if history.record.Mode&0077 != 0 {
		return nil, managed.ErrConflict
	}
	name := archiveName(*p.Predecessor)
	existing, entry, err := readRegular(history.fd(), name)
	if os.IsNotExist(err) {
		var parent *held
		var pp string
		parent, pp, err = parentFor(p, current)
		if err != nil {
			return nil, err
		}
		e.boundary("before_snapshot_publication")
		err = publish(parent, pp, history, filepath.Join(current, HistoryName), name, raw, nil, nil, e)
		parent.close()
		if err != nil {
			return nil, err
		}
		existing, entry, err = readRegular(history.fd(), name)
	}
	if err != nil || !bytes.Equal(existing, raw) || entry.Mode&0077 != 0 {
		return nil, managed.ErrConflict
	}
	if err = syncRegular(history, name, entry); err != nil {
		return nil, err
	}
	if err = syncDirectory(history, filepath.Join(current, HistoryName), e); err != nil {
		return nil, err
	}
	e.boundary("predecessor_archived")
	return &Archive{path.Join(HistoryName, name), entry.Identity, Digest(raw), history.record.Identity, history.record.OwnerUID, history.record.Mode}, nil
}
func archived(root *held, j Journal) (Journal, error) {
	a := j.PredecessorArchive
	if a == nil || j.Plan.Predecessor == nil || a.Path != path.Join(HistoryName, archiveName(*j.Plan.Predecessor)) {
		return Journal{}, managed.ErrConflict
	}
	history, err := openAt(root.fd(), HistoryName, true)
	if err != nil {
		return Journal{}, err
	}
	defer history.close()
	if history.record != (Directory{a.DirectoryIdentity, a.OwnerUID, a.Mode}) || a.Mode&0077 != 0 {
		return Journal{}, managed.ErrConflict
	}
	raw, f, err := readRegular(history.fd(), path.Base(a.Path))
	if err != nil || f.Identity != a.Identity || f.SHA256 != a.SHA256 || f.Mode&0077 != 0 {
		return Journal{}, managed.ErrConflict
	}
	var old Journal
	if Decode(raw, &old) != nil || old.Schema != JournalSchema || !terminal(old) || old.Plan.InstallationID != j.Plan.InstallationID || old.PlanSHA256 != j.Plan.Predecessor.PlanSHA256 || old.Plan.RelocationID != j.Plan.Predecessor.RelocationID || Digest(raw) != j.Plan.Predecessor.JournalSHA256 {
		return Journal{}, managed.ErrConflict
	}
	if validateJournal(old) != nil {
		return Journal{}, managed.ErrConflict
	}
	if _, err = DecodePlan(canonical(old.Plan)); err != nil {
		return Journal{}, err
	}
	return old, nil
}
func archivedReceipt(root *held, j Journal, p Plan) (Receipt, error) {
	seen := map[string]bool{}
	for {
		if seen[j.PlanSHA256] {
			return Receipt{}, managed.ErrConflict
		}
		seen[j.PlanSHA256] = true
		if j.PlanSHA256 == p.PlanSHA256 && j.Plan.RelocationID == p.RelocationID {
			if j.Phase == "rolled_back" && j.RollbackReceipt != nil {
				return *j.RollbackReceipt, nil
			}
			if j.Phase == "complete" && j.CompletionReceipt != nil {
				return *j.CompletionReceipt, nil
			}
			return Receipt{}, managed.ErrConflict
		}
		old, err := archived(root, j)
		if err != nil {
			return Receipt{}, err
		}
		j = old
	}
}

// The same namespace semantics apply even when no current journal exists.
func verifyHistoryNamespace(root *held, installation string) (bool, error) {
	history, err := openAt(root.fd(), HistoryName, true)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	defer history.close()
	if history.record.Mode&0077 != 0 {
		return true, managed.ErrConflict
	}
	names, err := history.file.Readdirnames(-1)
	if err != nil {
		return true, err
	}
	for _, name := range names {
		raw, file, err := readRegular(history.fd(), name)
		if err != nil || file.Mode&0077 != 0 {
			return true, managed.ErrConflict
		}
		var j Journal
		if Decode(raw, &j) != nil || j.Schema != JournalSchema || !terminal(j) || j.Plan.InstallationID != installation || name != j.Plan.RelocationID+"-"+strings.TrimPrefix(Digest(raw), "sha256:")+".json" {
			return true, managed.ErrConflict
		}
		if validateJournal(j) != nil {
			return true, managed.ErrConflict
		}
		if _, err = DecodePlan(canonical(j.Plan)); err != nil {
			return true, err
		}
	}
	return true, nil
}
func verifyHistory(root *held, current Journal) error {
	present, err := verifyHistoryNamespace(root, current.Plan.InstallationID)
	if err != nil {
		return err
	}
	if !present && current.PredecessorArchive != nil {
		return managed.ErrConflict
	}
	seen := map[string]bool{}
	j := current
	for j.PredecessorArchive != nil {
		if seen[j.PlanSHA256] {
			return managed.ErrConflict
		}
		seen[j.PlanSHA256] = true
		j, err = archived(root, j)
		if err != nil {
			return err
		}
	}
	return nil
}
func inventoryMatches(root *held, current string, p Plan, j *Journal, files []File) error {
	if j == nil && p.Predecessor == nil {
		if _, err := verifyHistoryNamespace(root, p.InstallationID); err != nil {
			return err
		}
	}
	expected := map[string]File{}
	for _, f := range p.Files {
		expected[f.Path] = f
	}
	actual := map[string]File{}
	for _, f := range files {
		actual[f.Path] = f
	}
	if j != nil {
		raw, f, err := readRegular(root.fd(), JournalName)
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, canonical(*j)) {
			return managed.ErrConflict
		}
		expected[JournalName] = f
		raw, owner, err := readRegular(root.fd(), OwnerName)
		if err != nil {
			return err
		}
		original := bytes.Equal(raw, j.OriginalOwner)
		target := bytes.Equal(raw, j.TargetOwner)
		if !original && !target {
			return managed.ErrConflict
		}
		if target || j.Phase == "rollback_pending" || j.Phase == "rolled_back" {
			expected[OwnerName] = owner
		}
		if j.PredecessorArchive != nil {
			a := j.PredecessorArchive
			history, err := openAt(root.fd(), HistoryName, true)
			if err != nil {
				return err
			}
			if history.record != (Directory{a.DirectoryIdentity, a.OwnerUID, a.Mode}) {
				history.close()
				return managed.ErrConflict
			}
			expected[HistoryName] = directoryEntry(HistoryName, history)
			history.close()
			raw, f, err = readRelative(root, a.Path)
			if err != nil || f.Identity != a.Identity || Digest(raw) != a.SHA256 {
				return managed.ErrConflict
			}
			expected[a.Path] = f
		}
		if err = verifyHistory(root, *j); err != nil {
			return err
		}
	} else if p.Predecessor != nil {
		// The still-current predecessor may have an exact snapshot/empty history dir
		// from this plan's interrupted succession before new journal publication.
		old, file, raw, err := readJournal(root)
		if err != nil || !predecessorMatches(p, old, file, raw, current) {
			return managed.ErrConflict
		}
		if _, ok := actual[HistoryName]; ok {
			history, err := openAt(root.fd(), HistoryName, true)
			if err != nil {
				return err
			}
			if history.record.Mode&0077 != 0 {
				history.close()
				return managed.ErrConflict
			}
			if _, wasPlanned := expected[HistoryName]; !wasPlanned {
				expected[HistoryName] = directoryEntry(HistoryName, history)
			}
			name := archiveName(*p.Predecessor)
			copyRaw, copyFile, err := readRegular(history.fd(), name)
			history.close()
			if err == nil {
				if !bytes.Equal(copyRaw, raw) || copyFile.Mode&0077 != 0 {
					return managed.ErrConflict
				}
				copyFile.Path = path.Join(HistoryName, name)
				expected[copyFile.Path] = copyFile
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	if len(actual) != len(expected) {
		return managed.ErrConflict
	}
	for name, want := range expected {
		got, ok := actual[name]
		if !ok || !bytes.Equal(canonical(got), canonical(want)) {
			return managed.ErrConflict
		}
	}
	return nil
}
func validateFilePlan(p Plan) error {
	if !p.SourceRootIdentity.Identity.Valid() || p.SourceRootIdentity.Mode&0077 != 0 {
		return managed.ErrInvalid
	}
	if !sort.SliceIsSorted(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path }) {
		return managed.ErrInvalid
	}
	seen := map[string]bool{}
	for _, record := range []Directory{p.SourceRootIdentity, p.SourceParentIdentity, p.TargetParentIdentity, p.BackupParentIdentity} {
		if !record.Identity.Valid() || record.Mode&^uint32(07777) != 0 || record.Identity.VolumeUUID != p.SourceRootIdentity.Identity.VolumeUUID {
			return managed.ErrInvalid
		}
	}
	if p.SourceRootIdentity.OwnerUID != uint32(p.SourcePlan.OwnerUID) {
		return managed.ErrInvalid
	}
	for _, f := range p.Files {
		if !filepath.IsLocal(f.Path) || path.Clean(f.Path) != f.Path || seen[strings.ToLower(norm.NFC.String(f.Path))] || !f.Identity.Valid() || f.OwnerUID != uint32(p.SourcePlan.OwnerUID) || f.Identity.VolumeUUID != p.SourceRootIdentity.Identity.VolumeUUID {
			return managed.ErrInvalid
		}
		seen[strings.ToLower(norm.NFC.String(f.Path))] = true
		if f.Kind == "file" {
			if f.Size == nil || *f.Size < 0 || !digestPattern.MatchString(f.SHA256) {
				return managed.ErrInvalid
			}
		} else if f.Kind != "directory" && !(f.Kind == "socket" && f.Path == "control.sock") || f.Size != nil || f.SHA256 != "" {
			return managed.ErrInvalid
		}
	}
	for _, required := range []string{OwnerName, "state.sqlite", "controller.lock", "objects", "objects/.seal.lock"} {
		if !seen[required] {
			return fmt.Errorf("%w: absent required inventory", managed.ErrInvalid)
		}
	}
	return nil
}
