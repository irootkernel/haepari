package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/irootkernel/haepari/internal/platform"
)

const socketName = "control.sock"

var ErrInvalidRoot = errors.New("invalid state root")

type Root struct {
	Path       string
	SocketPath string
	DBPath     string
}

// ResolvePreferred selects the CLI root when it is set, then the environment
// root, then the default root. Both paths use Resolve and refuse an unsafe
// root before Prepare.
func ResolvePreferred(cliRoot, environment string) (Root, error) {
	if cliRoot != "" {
		return Resolve(cliRoot)
	}
	return Resolve(environment)
}

func Resolve(override string) (Root, error) {
	path := override
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Root{}, fmt.Errorf("find home: %w", err)
		}
		path = filepath.Join(home, ".haepari")
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return Root{}, fmt.Errorf("%w: path must be absolute and contain no NUL byte", ErrInvalidRoot)
	}
	path = filepath.Clean(path)
	for _, systemLink := range []string{"/tmp", "/var"} {
		if !strings.HasPrefix(path, systemLink+"/") {
			continue
		}
		canonicalRoot, err := filepath.EvalSymlinks(systemLink)
		if err != nil {
			return Root{}, fmt.Errorf("resolve system root %s: %w", systemLink, err)
		}
		path = filepath.Join(canonicalRoot, strings.TrimPrefix(path, systemLink+"/"))
		break
	}
	if err := checkAncestors(path); err != nil {
		return Root{}, err
	}
	socketPath := filepath.Join(path, socketName)
	if len([]byte(socketPath)) >= 104 {
		return Root{}, fmt.Errorf("%w: socket path exceeds macOS 104-byte sun_path limit", ErrInvalidRoot)
	}
	return Root{Path: path, SocketPath: socketPath, DBPath: filepath.Join(path, "state.sqlite")}, nil
}

func checkAncestors(path string) error {
	checkedFilesystem := false
	for ancestor := path; ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect state root ancestor %s: %w", ancestor, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: %s is a symlink or non-directory", ErrInvalidRoot, ancestor)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != 0 && int(stat.Uid) != os.Getuid() {
			return fmt.Errorf("%w: %s is owned by another user", ErrInvalidRoot, ancestor)
		}
		if !checkedFilesystem {
			local, err := platform.LocalFilesystem(ancestor)
			if err != nil {
				return fmt.Errorf("inspect state root filesystem: %w", err)
			}
			if !local {
				return fmt.Errorf("%w: filesystem is not local", ErrInvalidRoot)
			}
			checkedFilesystem = true
		}
		if info.Mode().Perm()&0022 != 0 {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || info.Mode()&os.ModeSticky == 0 || stat.Uid != 0 {
				return fmt.Errorf("%w: %s is shared or writable by another user", ErrInvalidRoot, ancestor)
			}
		}
		if ancestor == string(filepath.Separator) {
			break
		}
	}
	return nil
}

func (root Root) Prepare() error {
	if err := checkAncestors(root.Path); err != nil {
		return err
	}
	if info, err := os.Lstat(root.Path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: not a real directory", ErrInvalidRoot)
		}
		if info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("%w: permissions must exclude group and other", ErrInvalidRoot)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
			return fmt.Errorf("%w: owner differs from current user", ErrInvalidRoot)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect state root: %w", err)
	}
	// Create only the final component. MkdirAll could follow a newly planted
	// symlink in a missing ancestor before the validation pass runs again.
	if err := os.Mkdir(root.Path, 0700); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: parent directory must exist: %w", ErrInvalidRoot, err)
		}
		return fmt.Errorf("create state root: %w", err)
	}
	return root.Prepare()
}
