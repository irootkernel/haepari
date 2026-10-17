package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

type Sources struct {
	Path           string
	ConfigExplicit bool
	GlobalLimit    int
	GlobalExplicit bool
	HostLimit      int
	HostExplicit   bool
}

// Load reads the selected configuration before the caller mutates a root.
// An omitted file uses validated defaults overlaid with the legacy CLI limits
// and reports fromFile false. An explicit path may be relative to the process
// working directory or an absolute private file outside the root. It never
// selects a different root. A missing or unsafe explicit file is refused
// without falling back to defaults.
func Load(rootPath string, sources Sources) (Document, bool, error) {
	path, explicit, err := filePath(rootPath, sources)
	if err != nil {
		return Document{}, false, err
	}
	data, present, err := readFile(path, explicit)
	if err != nil {
		return Document{}, false, err
	}
	if !present {
		doc := Defaults()
		doc.GlobalLimit = int64(sources.GlobalLimit)
		doc.HostMACOSGuestLimit = int64(sources.HostLimit)
		if err := doc.Validate(); err != nil {
			return Document{}, false, err
		}
		return doc, false, nil
	}
	parsed, presence, err := Parse(data)
	if err != nil {
		return Document{}, true, err
	}
	if sources.GlobalExplicit && presence.GlobalLimit && int64(sources.GlobalLimit) != parsed.GlobalLimit {
		return Document{}, true, fmt.Errorf("%w: explicit --global-limit %d differs from config global_limit %d", ErrRefused, sources.GlobalLimit, parsed.GlobalLimit)
	}
	if sources.GlobalExplicit && !presence.GlobalLimit {
		parsed.GlobalLimit = int64(sources.GlobalLimit)
	}
	if sources.HostExplicit && presence.HostLimit && int64(sources.HostLimit) != parsed.HostMACOSGuestLimit {
		return Document{}, true, fmt.Errorf("%w: explicit --host-macos-guest-limit %d differs from config host_macos_guest_limit %d", ErrRefused, sources.HostLimit, parsed.HostMACOSGuestLimit)
	}
	if sources.HostExplicit && !presence.HostLimit {
		parsed.HostMACOSGuestLimit = int64(sources.HostLimit)
	}
	if err := parsed.Validate(); err != nil {
		return Document{}, true, err
	}
	return parsed, true, nil
}

func filePath(rootPath string, sources Sources) (string, bool, error) {
	if sources.ConfigExplicit && sources.Path == "" {
		return "", true, fmt.Errorf("%w: explicit --config is empty", ErrRefused)
	}
	if !sources.ConfigExplicit {
		return filepath.Join(rootPath, "config.json"), false, nil
	}
	candidate, err := canonicalConfigPath(sources.Path)
	if err != nil {
		return "", true, err
	}
	return candidate, true, nil
}

func canonicalConfigPath(path string) (string, error) {
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("%w: --config path is unsafe", ErrRefused)
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil || !filepath.IsAbs(cwd) {
			return "", fmt.Errorf("%w: --config path is unsafe", ErrRefused)
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	for _, systemLink := range []string{"/tmp", "/var"} {
		if path != systemLink && !strings.HasPrefix(path, systemLink+"/") {
			continue
		}
		canonical, err := filepath.EvalSymlinks(systemLink)
		if err != nil {
			return "", fmt.Errorf("%w: configuration path is unsafe: %s", ErrRefused, path)
		}
		if path == systemLink {
			path = canonical
		} else {
			path = filepath.Join(canonical, strings.TrimPrefix(path, systemLink+"/"))
		}
		break
	}
	if err := rejectSymlinkAncestors(path); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("%w: configuration path is unsafe: %s", ErrRefused, path)
	}
	if resolved != path {
		return "", fmt.Errorf("%w: configuration path must not contain a symlink: %s", ErrRefused, path)
	}
	return path, nil
}

func rejectSymlinkAncestors(path string) error {
	for ancestor := filepath.Dir(path); ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if os.IsNotExist(err) {
			if ancestor == string(filepath.Separator) {
				break
			}
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: configuration path must not contain a symlink: %s", ErrRefused, path)
		}
		if ancestor == string(filepath.Separator) {
			break
		}
	}
	return nil
}

func readFile(path string, explicit bool) ([]byte, bool, error) {
	// O_NONBLOCK keeps a named pipe from blocking before the regular-file check.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if os.IsNotExist(err) {
			if explicit {
				return nil, false, fmt.Errorf("%w: explicit config file is missing: %s", ErrRefused, path)
			}
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%w: configuration file is unsafe: %s", ErrRefused, path)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return nil, false, fmt.Errorf("%w: inspect configuration file: %s", ErrRefused, path)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0077 != 0 || int(stat.Uid) != os.Getuid() {
		return nil, false, fmt.Errorf("%w: configuration file must be a private regular file: %s", ErrRefused, path)
	}
	local, err := platform.LocalFilesystem(path)
	if err != nil || !local {
		return nil, false, fmt.Errorf("%w: configuration file must be on a local filesystem: %s", ErrRefused, path)
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return nil, false, fmt.Errorf("%w: read configuration file: %s", ErrRefused, path)
	}
	if len(data) == 0 || len(data) > 1<<20 {
		return nil, false, fmt.Errorf("%w: configuration document is empty or too large", ErrRefused)
	}
	return data, true, nil
}
