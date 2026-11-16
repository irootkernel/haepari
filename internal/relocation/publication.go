package relocation

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

// publish never stages inside the installation/history/backup. Crash leftovers
// remain outside the inventory; only this live descriptor owner cleans them.
func publish(parent *held, parentPath string, destination *held, destinationPath, name string, raw []byte, prior *File, priorBytes []byte, e Environment) error {
	guardDestination := func() error {
		named, err := openDirectory(destinationPath)
		if err != nil {
			return err
		}
		defer named.close()
		if named.record != destination.record || destination.matches(unix.AT_FDCWD, destinationPath) != nil {
			return managed.ErrConflict
		}
		return nil
	}
	if err := guardDestination(); err != nil {
		return err
	}
	if parent.matches(unix.AT_FDCWD, parentPath) != nil || parent.record.Identity.VolumeUUID != destination.record.Identity.VolumeUUID {
		return managed.ErrConflict
	}
	id, err := newID()
	if err != nil {
		return err
	}
	attempt := ".haepari-publish-" + id
	if err = unix.Mkdirat(parent.fd(), attempt, 0700); err != nil {
		return err
	}
	stage, err := openAt(parent.fd(), attempt, true)
	if err != nil {
		return err
	}
	defer stage.close()
	if stage.record.Identity.VolumeUUID != parent.record.Identity.VolumeUUID || stage.record.OwnerUID != uint32(os.Geteuid()) || stage.record.Mode&0077 != 0 {
		return managed.ErrConflict
	}
	fd, err := unix.Openat(stage.fd(), "record", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "record")
	defer f.Close()
	idFile, err := platform.PersistentFileIdentity(fd)
	if err != nil {
		return err
	}
	// No defer removes a lost/unknown object. The successful live cleanup below
	// requires the original descriptors and unchanged path identities.
	e.boundary("publication_created")
	var staged, named unix.Stat_t
	if unix.Fstat(fd, &staged) != nil || unix.Fstatat(stage.fd(), "record", &named, unix.AT_SYMLINK_NOFOLLOW) != nil || staged.Mode&unix.S_IFMT != unix.S_IFREG || staged.Uid != uint32(os.Geteuid()) || staged.Mode&0077 != 0 || staged.Nlink != 1 || staged.Dev != named.Dev || staged.Ino != named.Ino || stage.matches(parent.fd(), attempt) != nil {
		return managed.ErrConflict
	}
	if _, err = f.Write(raw[:len(raw)/2]); err != nil {
		return err
	}
	e.boundary("publication_partial")
	if _, err = f.Write(raw[len(raw)/2:]); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	got, file, err := readRegular(stage.fd(), "record")
	if err != nil || !bytes.Equal(got, raw) || file.Identity != idFile || file.Mode&0077 != 0 || stage.matches(parent.fd(), attempt) != nil {
		return managed.ErrConflict
	}
	e.boundary("publication_synced")
	if err = guardDestination(); err != nil {
		return err
	}
	if prior == nil {
		if err = absent(destination, name); err != nil {
			return err
		}
	} else {
		current, entry, err := readRegular(destination.fd(), name)
		if err != nil || entry.Identity != prior.Identity || !bytes.Equal(current, priorBytes) {
			return managed.ErrConflict
		}
	}
	if parent.matches(unix.AT_FDCWD, parentPath) != nil || stage.matches(parent.fd(), attempt) != nil {
		return managed.ErrConflict
	}
	verified, entry, guardErr := readRegular(stage.fd(), "record")
	if guardErr != nil || entry.Identity != idFile || entry.Mode&0077 != 0 || !bytes.Equal(verified, raw) {
		return managed.ErrConflict
	}
	if err = guardDestination(); err != nil {
		return err
	}
	if prior == nil {
		err = platform.RenameExclusive(stage.fd(), "record", destination.fd(), name)
	} else {
		err = unix.Renameat(stage.fd(), "record", destination.fd(), name)
	}
	if err != nil {
		return err
	}
	e.boundary("publication_renamed")
	if err = syncDirectory(destination, destinationPath, e); err != nil {
		return err
	}
	if err = syncDirectory(stage, filepath.Join(parentPath, attempt), e); err != nil {
		return err
	}
	e.boundary("publication_durable")
	if stage.matches(parent.fd(), attempt) != nil {
		return managed.ErrConflict
	}
	if err = unix.Unlinkat(parent.fd(), attempt, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return parent.sync()
}
func parentFor(p Plan, current string) (*held, string, error) {
	parentPath := filepath.Dir(current)
	expected := p.SourceParentIdentity
	if current == p.TargetPlan.Root {
		expected = p.TargetParentIdentity
	} else if current != p.SourcePlan.Root {
		return nil, "", managed.ErrConflict
	}
	h, err := openDirectory(parentPath)
	if err != nil {
		return nil, "", err
	}
	if h.record != expected {
		h.close()
		return nil, "", managed.ErrConflict
	}
	return h, parentPath, nil
}
func readJournal(root *held) (Journal, File, []byte, error) {
	raw, f, err := readRegular(root.fd(), JournalName)
	if err != nil {
		return Journal{}, File{}, nil, err
	}
	var j Journal
	if Decode(raw, &j) != nil || j.Schema != JournalSchema || j.Sequence < 1 || j.PlanSHA256 != j.Plan.PlanSHA256 {
		return Journal{}, f, raw, managed.ErrConflict
	}
	if _, err = DecodePlan(canonical(j.Plan)); err != nil {
		return Journal{}, f, raw, managed.ErrConflict
	}
	validPhase := false
	for _, phase := range []string{"prepared", "backup_verified", "rename_pending", "moved", "owner_pending", "complete", "rollback_pending", "rolled_back"} {
		validPhase = validPhase || j.Phase == phase
	}
	if !validPhase {
		return Journal{}, f, raw, managed.ErrConflict
	}
	if len(j.OriginalOwner) == 0 || len(j.TargetOwner) == 0 || j.BackupState != "not_created" && j.BackupState != "bound" && j.BackupState != "verified" || j.BackupIdentity == nil && j.BackupState != "not_created" || j.BackupIdentity != nil && !j.BackupIdentity.Identity.Valid() || j.BackupState == "verified" && !digestPattern.MatchString(j.BackupManifestSHA256) || j.Phase == "complete" && j.CompletionReceipt == nil || j.Phase == "rolled_back" && j.RollbackReceipt == nil {
		return Journal{}, f, raw, managed.ErrConflict
	}
	if validateJournal(j) != nil {
		return Journal{}, f, raw, managed.ErrConflict
	}
	if f.Mode&0077 != 0 {
		return Journal{}, f, raw, managed.ErrConflict
	}
	return j, f, raw, nil
}
func updateJournal(root *held, current string, j *Journal, e Environment) error {
	previous, prior, raw, err := readJournal(root)
	if err != nil {
		return err
	}
	if previous.PlanSHA256 != j.PlanSHA256 || previous.Sequence != j.Sequence || j.Sequence == int64(^uint64(0)>>1) {
		return managed.ErrConflict
	}
	parent, p, err := parentFor(j.Plan, current)
	if err != nil {
		return err
	}
	defer parent.close()
	e.boundary("before_journal_" + j.Phase)
	j.Sequence++
	err = publish(parent, p, root, current, JournalName, canonical(j), &prior, raw, e)
	if err == nil {
		e.boundary("journal_" + j.Phase)
	}
	return err
}
func ownerBytes(root *held, p ServicePlan) ([]byte, error) {
	raw, f, err := readRegular(root.fd(), OwnerName)
	if err != nil {
		return nil, err
	}
	var owner struct {
		Schema   string `json:"schema"`
		Instance string `json:"instance"`
		OwnerUID int    `json:"owner_uid"`
		Root     string `json:"root"`
	}
	if f.Mode&0077 != 0 || Decode(raw, &owner) != nil || owner.Schema != "haepari.release-service-owner/v1" || owner.Instance != p.Instance || owner.OwnerUID != p.OwnerUID || owner.Root != p.Root {
		return nil, managed.ErrConflict
	}
	return raw, nil
}
func replaceOwner(root *held, current string, j Journal, intended []byte, e Environment) error {
	got, prior, err := readRegular(root.fd(), OwnerName)
	if err != nil {
		return err
	}
	if bytes.Equal(got, intended) {
		return nil
	}
	if !bytes.Equal(got, j.OriginalOwner) && !bytes.Equal(got, j.TargetOwner) {
		return managed.ErrConflict
	}
	parent, p, err := parentFor(j.Plan, current)
	if err != nil {
		return err
	}
	defer parent.close()
	e.boundary("before_owner_update")
	return publish(parent, p, root, current, OwnerName, intended, &prior, got, e)
}
func journalAbsent(err error) bool { return errors.Is(err, os.ErrNotExist) }

func syncDirectory(dir *held, name string, e Environment) error {
	if dir.matches(unix.AT_FDCWD, name) != nil {
		return managed.ErrConflict
	}
	if e.DirectorySync != nil {
		if err := e.DirectorySync(name); err != nil {
			return err
		}
	}
	if dir.matches(unix.AT_FDCWD, name) != nil {
		return managed.ErrConflict
	}
	if err := dir.sync(); err != nil {
		return err
	}
	return dir.matches(unix.AT_FDCWD, name)
}

// Reconcile an already visible immutable publication through its exact identity.
func syncRegular(dir *held, name string, expected File) error {
	h, err := openAt(dir.fd(), name, false)
	if err != nil {
		return err
	}
	defer h.close()
	if h.record.Identity != expected.Identity || h.stat.Mode&unix.S_IFMT != unix.S_IFREG || h.record.Mode&0077 != 0 || h.matches(dir.fd(), name) != nil {
		return managed.ErrConflict
	}
	if err = h.sync(); err != nil {
		return err
	}
	return h.matches(dir.fd(), name)
}
