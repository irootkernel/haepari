package runner

import (
	"os"
	"path/filepath"
	"reflect"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

// GuestPreparation binds the outside control worker and original runner to the
// same preparation as the root, workspace and log in the native request.
type GuestPreparation struct {
	Schema    string                `json:"schema"`
	Control   managed.RetainedPath  `json:"control"`
	Runner    managed.RetainedPath  `json:"runner"`
	Workspace *managed.RetainedPath `json:"workspace,omitempty"`
}

const GuestPreparationSchema = "haepari.guest-preparation/v1"

func validPreparationVersion(p *GuestPreparation) bool {
	return p == nil || p.Schema == GuestPreparationSchema
}

func guestRetainedDescriptor(f *os.File, name string, directory, persistent bool) (managed.RetainedPath, error) {
	if persistent {
		return retainedDescriptor(f, name, directory)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return managed.RetainedPath{}, err
	}
	return managed.RetainedPath{Path: name, Directory: directory, Device: uint64(st.Dev), Inode: st.Ino}, nil
}

func retainedDescriptor(f *os.File, name string, directory bool) (managed.RetainedPath, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return managed.RetainedPath{}, err
	}
	identity, err := platform.PersistentFileIdentity(int(f.Fd()))
	if err != nil {
		return managed.RetainedPath{}, err
	}
	boot, err := platform.BootIdentity()
	if err != nil {
		return managed.RetainedPath{}, err
	}
	return managed.RetainedPath{Path: name, Directory: directory, Device: uint64(st.Dev), Inode: st.Ino, Persistent: identity, ObservedBoot: boot}, nil
}

// Same-boot device/inode checks remain mandatory. Across boots both the volume
// and the file incarnation must match; missing/unsupported evidence is refused.
func retainedDescriptorMatches(fd int, p managed.RetainedPath) bool {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || uint32(st.Mode)&0077 != 0 ||
		(p.Directory && uint32(st.Mode)&unix.S_IFMT != unix.S_IFDIR) ||
		(!p.Directory && (uint32(st.Mode)&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1)) {
		return false
	}
	boot, err := platform.BootIdentity()
	if err != nil {
		return false
	}
	if p.ObservedBoot == "" || p.ObservedBoot == boot {
		if uint64(st.Dev) != p.Device || st.Ino != p.Inode {
			return false
		}
		if p.Persistent == (platform.FileIdentity{}) {
			return true
		}
	}
	identity, err := platform.PersistentFileIdentity(fd)
	return err == nil && p.Persistent.Valid() && identity == p.Persistent
}

func retainedAtMatches(parent int, name string, p managed.RetainedPath) bool {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if p.Directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(parent, name, flags, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	return retainedDescriptorMatches(fd, p)
}

func (c *guestControlFile) bindPreparation(q GuestRequest) error {
	d, err := openDirectory(filepath.Join(q.JobRoot, q.Request.Identity.JobID))
	if err != nil {
		return err
	}
	defer d.close()
	control, err := retainedDescriptor(c.file, "", false)
	if err != nil {
		return err
	}
	f, err := d.open("haepari", unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	runner, err := retainedDescriptor(f, "haepari", false)
	if err != nil {
		return err
	}
	w, err := openDirectory(filepath.Join(q.JobRoot, q.Request.Identity.JobID, "workspace"))
	if err != nil {
		return err
	}
	defer w.close()
	workspace, err := retainedDescriptor(w.file, "workspace", true)
	if err != nil {
		return err
	}
	c.value.Preparation = &GuestPreparation{Schema: GuestPreparationSchema, Control: control, Runner: runner, Workspace: &workspace}
	return nil
}

func validatePersistentPreparation(c *guestControlFile, guest GuestRequest, crossBoot bool) error {
	p := c.value.Preparation
	if !validPreparationVersion(p) || !validPreparationVersion(guest.Preparation) {
		return ErrUnproven
	}
	if p == nil {
		if crossBoot || guest.Preparation != nil || GuestUsesPersistentIdentity(guest) && c.value.NativeRequest != nil {
			return ErrUnproven
		}
		return nil // Historical same-boot receipts retain their existing guards.
	}
	if guest.Preparation != nil && !reflect.DeepEqual(p, guest.Preparation) {
		return ErrConflict
	}
	if crossBoot && (guest.Preparation == nil || guest.Root == nil || !guest.Root.Persistent.Valid() || !guest.Request.LogIdentity.Valid()) {
		return ErrUnproven
	}
	if !p.Control.Persistent.Valid() || !p.Runner.Persistent.Valid() || c.value.Root == nil || !c.value.Root.Persistent.Valid() || (p.Workspace == nil || !p.Workspace.Persistent.Valid() || c.value.WorkspaceRoot != nil && !reflect.DeepEqual(p.Workspace, c.value.WorkspaceRoot)) {
		return ErrUnproven
	}
	// For a proven new boot, the original control descriptor may have a new
	// device number. It must still be the prepared object, not a copied receipt.
	control := p.Control
	if crossBoot {
		control.ObservedBoot = guest.BootID
	}
	if !retainedDescriptorMatches(int(c.file.Fd()), control) {
		return ErrConflict
	}
	return nil
}

func validatePreparedPayloads(d *directory, c GuestControl, guest GuestRequest) error {
	if c.Preparation == nil {
		return nil
	}
	if !retainedAtMatches(int(d.file.Fd()), "haepari", c.Preparation.Runner) {
		return ErrConflict
	}
	f, err := d.open("output.log", unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	p := managed.RetainedPath{Device: guest.Request.LogDevice, Inode: guest.Request.LogInode, Persistent: guest.Request.LogIdentity, ObservedBoot: guest.BootID}
	if !retainedDescriptorMatches(int(f.Fd()), p) {
		return ErrConflict
	}
	return nil
}

func preparedWorkspace(c GuestControl) *managed.RetainedPath {
	if c.WorkspaceRoot != nil {
		return c.WorkspaceRoot
	}
	if c.Preparation != nil {
		return c.Preparation.Workspace
	}
	return nil
}
