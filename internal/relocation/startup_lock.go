package relocation

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/state"
	"golang.org/x/sys/unix"
)

// AcquireController preserves ordinary new-home acquisition, but a relocation
// journal makes the original existing lock mandatory. Never create a new inode
// that could be locked independently of a still-held original.
func AcquireController(r state.Root) (*os.File, error) {
	root, err := openDirectory(r.Path)
	if err != nil {
		return nil, fail("startup_root", err)
	}
	defer root.close()
	j, _, _, err := readJournal(root)
	if os.IsNotExist(err) {
		return r.AcquireController()
	}
	if err != nil {
		return nil, fail("startup_journal", err)
	}
	if root.record != j.Plan.SourceRootIdentity || r.Path != j.Plan.SourcePlan.Root && r.Path != j.Plan.TargetPlan.Root {
		return nil, fail("startup_identity", managed.ErrConflict)
	}
	fd, err := unix.Openat(root.fd(), "controller.lock", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EISDIR) {
			err = managed.ErrConflict
		}
		return nil, fail("startup_lock", err)
	}
	controller := os.NewFile(uintptr(fd), filepath.Join(r.Path, "controller.lock"))
	if err = bindOriginalController(root, r.Path, j, controller); err != nil {
		controller.Close()
		return nil, fail("startup_lock", err)
	}
	return controller, nil
}

// Only identity and private-file ownership are stable here. The original lock's
// socket-ownership record may legitimately change during later normal startups.
func bindOriginalController(root *held, rootPath string, j Journal, controller *os.File) error {
	if controller == nil {
		return managed.ErrInvalid
	}
	var planned File
	for _, f := range j.Plan.Files {
		if f.Path == "controller.lock" {
			planned = f
			break
		}
	}
	if planned.Kind != "file" || !planned.Identity.Valid() || planned.OwnerUID != uint32(os.Geteuid()) || planned.Mode&0077 != 0 {
		return managed.ErrConflict
	}
	check := func() error {
		var supplied, named unix.Stat_t
		fd := int(controller.Fd())
		if err := unix.Fstat(fd, &supplied); err != nil {
			return err
		}
		if supplied.Mode&unix.S_IFMT != unix.S_IFREG || supplied.Uid != planned.OwnerUID || supplied.Nlink != 1 || uint32(supplied.Mode&07777) != planned.Mode {
			return managed.ErrConflict
		}
		identity, err := platform.PersistentFileIdentity(fd)
		if err != nil {
			return err
		}
		if identity != planned.Identity || root.matches(unix.AT_FDCWD, rootPath) != nil {
			return managed.ErrConflict
		}
		if err = unix.Fstatat(root.fd(), "controller.lock", &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return managed.ErrConflict
		}
		if supplied.Dev != named.Dev || supplied.Ino != named.Ino || supplied.Btim != named.Btim || supplied.Mode != named.Mode || supplied.Uid != named.Uid || supplied.Nlink != named.Nlink {
			return managed.ErrConflict
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	if err := unix.Flock(int(controller.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return state.ErrControllerOwned
		}
		return err
	}
	return check()
}
