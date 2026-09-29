//go:build darwin || linux

package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type sourceRoot struct {
	file      *os.File
	canonical string
}

type sourceVersion struct {
	dev, ino, nlink uint64
	size            int64
	mode            uint32
	mtime, ctime    timestamp
	executable      bool
}

type timestamp struct{ seconds, nanoseconds int64 }

func openSourceRoot(name string) (*sourceRoot, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name || name == "/" {
		return nil, fmt.Errorf("%w: source root must be an explicit directory", ErrInvalid)
	}
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: source root is not a real directory", ErrInvalid)
	}
	// macOS aliases /var to /private/var. Resolve ancestors of the selected
	// root, then open every actual component without following a symlink.
	canonical, err := filepath.EvalSymlinks(name)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(canonical, "/"), "/") {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	opened := os.NewFile(uintptr(fd), canonical)
	openedInfo, err := opened.Stat()
	if err == nil && !os.SameFile(info, openedInfo) {
		err = ErrChanged
	}
	if err != nil {
		opened.Close()
		return nil, err
	}
	return &sourceRoot{file: opened, canonical: canonical}, nil
}

func (r *sourceRoot) Close() error { return r.file.Close() }

func (r *sourceRoot) openRegular(name string) (*os.File, sourceVersion, error) {
	parts := strings.Split(name, "/")
	dir := int(r.file.Fd())
	owned := false
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(dir, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if owned {
			unix.Close(dir)
		}
		if err != nil {
			return nil, sourceVersion{}, err
		}
		dir, owned = next, true
	}
	fd, err := unix.Openat(dir, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if owned {
		unix.Close(dir)
	}
	if err != nil {
		return nil, sourceVersion{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	version, err := sourceVersionOf(file)
	if err != nil {
		file.Close()
		return nil, sourceVersion{}, err
	}
	return file, version, nil
}

func sourceVersionOf(file *os.File) (sourceVersion, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return sourceVersion{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return sourceVersion{}, fmt.Errorf("%w: selected input is not a single-link regular file", ErrInvalid)
	}
	mtime, ctime := statTimes(&stat)
	return sourceVersion{dev: uint64(stat.Dev), ino: stat.Ino, nlink: uint64(stat.Nlink), size: stat.Size,
		mode: uint32(stat.Mode), mtime: mtime, ctime: ctime, executable: stat.Mode&0111 != 0}, nil
}

func copySelected(ctx context.Context, source *sourceRoot, name, files string) (Entry, sourceVersion, error) {
	input, before, err := source.openRegular(name)
	if err != nil {
		return Entry{}, sourceVersion{}, err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(files, filepath.FromSlash(name))), 0700); err != nil {
		return Entry{}, sourceVersion{}, err
	}
	output, err := os.OpenFile(filepath.Join(files, filepath.FromSlash(name)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Entry{}, sourceVersion{}, err
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(output, hash), contextReader{ctx: ctx, reader: input})
	if err == nil {
		var after sourceVersion
		after, err = sourceVersionOf(input)
		if err != nil {
			err = fmt.Errorf("%w: copied input changed: %v", ErrChanged, err)
		} else if before != after || size != before.size {
			err = ErrChanged
		}
	}
	if err == nil {
		_, err = input.Seek(0, io.SeekStart)
	}
	if err == nil {
		second := sha256.New()
		var checked int64
		checked, err = io.Copy(second, contextReader{ctx: ctx, reader: input})
		if err == nil && (checked != size || !equalDigest(hash.Sum(nil), second.Sum(nil))) {
			err = ErrChanged
		}
	}
	if err == nil {
		var after sourceVersion
		after, err = sourceVersionOf(input)
		if err != nil {
			err = fmt.Errorf("%w: copied input changed: %v", ErrChanged, err)
		} else if before != after {
			err = ErrChanged
		}
	}
	if err == nil {
		var again *os.File
		var current sourceVersion
		again, current, err = source.openRegular(name)
		if err == nil {
			err = again.Close()
			if err == nil && before != current {
				err = ErrChanged
			}
		} else {
			err = fmt.Errorf("%w: selected path disappeared or changed: %v", ErrChanged, err)
		}
	}
	if err == nil {
		mode := os.FileMode(0400)
		if before.executable {
			mode = 0500
		}
		err = output.Chmod(mode)
	}
	if err == nil {
		err = output.Sync()
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Entry{}, sourceVersion{}, err
	}
	return Entry{Path: name, Size: size, Executable: before.executable, SHA256: "sha256:" + hex.EncodeToString(hash.Sum(nil))}, before, nil
}

func recheckSelected(ctx context.Context, source *sourceRoot, entry Entry, expected sourceVersion) error {
	file, before, err := source.openRegular(entry.Path)
	if err != nil {
		return fmt.Errorf("%w: selected path unavailable: %v", ErrChanged, err)
	}
	defer file.Close()
	if before != expected {
		return ErrChanged
	}
	hash := sha256.New()
	size, err := io.Copy(hash, contextReader{ctx: ctx, reader: file})
	if err != nil {
		return err
	}
	after, err := sourceVersionOf(file)
	if err != nil || before != after || size != entry.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return ErrChanged
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func equalDigest(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var difference byte
	for i := range a {
		difference |= a[i] ^ b[i]
	}
	return difference == 0
}

func acquireStoreLock(root string) (*os.File, error) {
	file, err := openPrivateFile(filepath.Join(root, ".seal.lock"), unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func openPrivateAppend(name string) (*os.File, error) {
	return openPrivateFile(name, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT)
}

func openPrivateReadWrite(name string) (*os.File, error) {
	return openPrivateFile(name, unix.O_RDWR)
}

func openPrivateFile(name string, flags int) (*os.File, error) {
	fd, err := unix.Open(name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0077 != 0 {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: store control file is not private and regular", ErrCorrupt)
	}
	return file, nil
}
