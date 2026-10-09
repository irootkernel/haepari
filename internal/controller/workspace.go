package controller

import (
	"os"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"golang.org/x/sys/unix"
)

func localWorkspaceRoot(path string) (managed.RetainedPath, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return managed.RetainedPath{}, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return managed.RetainedPath{}, err
	}
	if st.Uid != uint32(os.Geteuid()) {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	return managed.RetainedPath{Path: "workspace", Device: uint64(st.Dev), Inode: st.Ino, Directory: true}, nil
}
func (s *runtimeServer) verifyLocalWorkspace(id managed.LocalIdentity, path string) (managed.RetainedPath, error) {
	expected, err := s.ledger.LocalWorkspace(id)
	if err != nil {
		return expected, err
	}
	actual, err := localWorkspaceRoot(path)
	if err != nil {
		return expected, err
	}
	if actual != expected {
		return expected, managed.ErrStale
	}
	return expected, nil
}
func (s *runtimeServer) cleanupLocalWorkspace(id managed.LocalIdentity, job managed.JobSpec, started bool, path string, deadline time.Time) error {
	if !managed.AbsenceAware(job) || !started {
		return removeTreeUntil(path, deadline)
	}
	root, err := s.verifyLocalWorkspace(id, path)
	if err != nil {
		return err
	}
	return removeTreeUntilBound(path, deadline, &root)
}
