package relocation

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
	"golang.org/x/text/unicode/norm"
)

type held struct {
	file   *os.File
	record Directory
	stat   unix.Stat_t
}

func openAt(parent int, name string, directory bool) (*held, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(parent, name, flags, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	var s unix.Stat_t
	if err = unix.Fstat(fd, &s); err != nil {
		f.Close()
		return nil, err
	}
	id, err := platform.PersistentFileIdentity(fd)
	if err != nil {
		f.Close()
		return nil, err
	}
	if (!directory && s.Uid != uint32(os.Geteuid()) || directory && s.Uid != uint32(os.Geteuid()) && s.Uid != 0) || s.Mode&0022 != 0 && !(directory && s.Uid == 0 && s.Mode&unix.S_ISVTX != 0) || !directory && s.Nlink != 1 {
		f.Close()
		return nil, managed.ErrConflict
	}
	return &held{f, Directory{id, s.Uid, uint32(s.Mode & 07777)}, s}, nil
}
func openDirectory(p string) (*held, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsRune(p, 0) {
		return nil, managed.ErrInvalid
	}
	for ancestor := p; ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if err != nil {
			return nil, err
		}
		var st unix.Stat_t
		if err = unix.Lstat(ancestor, &st); err != nil {
			return nil, err
		}
		s := &st
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || s.Uid != 0 && s.Uid != uint32(os.Geteuid()) || s.Mode&0022 != 0 && !(s.Mode&unix.S_ISVTX != 0 && s.Uid == 0) {
			return nil, managed.ErrConflict
		}
		if ancestor == "/" {
			break
		}
	}
	return openAt(unix.AT_FDCWD, p, true)
}
func (h *held) close()  { _ = h.file.Close() }
func (h *held) fd() int { return int(h.file.Fd()) }
func (h *held) matches(parent int, name string) error {
	var s unix.Stat_t
	if err := unix.Fstatat(parent, name, &s, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if s.Dev != h.stat.Dev || s.Ino != h.stat.Ino || s.Mode != h.stat.Mode || s.Uid != h.stat.Uid {
		return managed.ErrConflict
	}
	id, err := platform.PersistentFileIdentity(h.fd())
	if err != nil || id != h.record.Identity {
		return managed.ErrConflict
	}
	return nil
}
func (h *held) sync() error { return h.file.Sync() }
func readRegular(parent int, name string) ([]byte, File, error) {
	h, err := openAt(parent, name, false)
	if err != nil {
		return nil, File{}, err
	}
	defer h.close()
	if h.stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, File{}, managed.ErrConflict
	}
	raw, err := io.ReadAll(h.file)
	if err != nil {
		return nil, File{}, err
	}
	var after unix.Stat_t
	if unix.Fstat(h.fd(), &after) != nil || after.Size != h.stat.Size || after.Mtim != h.stat.Mtim || int64(len(raw)) != h.stat.Size || h.matches(parent, name) != nil {
		return nil, File{}, managed.ErrConflict
	}
	size := h.stat.Size
	return raw, File{Path: name, Kind: "file", Identity: h.record.Identity, OwnerUID: h.record.OwnerUID, Mode: h.record.Mode, Size: &size, SHA256: Digest(raw)}, nil
}
func directoryEntry(name string, h *held) File {
	return File{Path: name, Kind: "directory", Identity: h.record.Identity, OwnerUID: h.record.OwnerUID, Mode: h.record.Mode}
}
func inventory(root *held, rootPath string) ([]File, error) {
	entries := []File{}
	seen := map[string]bool{}
	var walk func(*held, string) error
	walk = func(dir *held, prefix string) error {
		names, err := dir.file.Readdirnames(-1)
		if err != nil {
			return err
		}
		sort.Strings(names)
		for _, name := range names {
			relative := path.Join(prefix, name)
			fold := strings.ToLower(norm.NFC.String(relative))
			if !filepath.IsLocal(relative) || path.Clean(relative) != relative || seen[fold] {
				return managed.ErrConflict
			}
			seen[fold] = true
			var s unix.Stat_t
			if err = unix.Fstatat(dir.fd(), name, &s, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			switch s.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				h, e := openAt(dir.fd(), name, true)
				if e != nil {
					return e
				}
				entries = append(entries, directoryEntry(relative, h))
				e = walk(h, relative)
				if e == nil {
					e = h.matches(dir.fd(), name)
				}
				h.close()
				if e != nil {
					return e
				}
			case unix.S_IFREG:
				_, entry, e := readRegular(dir.fd(), name)
				if e != nil {
					return e
				}
				entry.Path = relative
				entries = append(entries, entry)
			case unix.S_IFSOCK:
				if relative != "control.sock" || s.Uid != uint32(os.Geteuid()) || s.Mode&0077 != 0 {
					return managed.ErrConflict
				}
				conn, e := net.DialTimeout("unix", filepath.Join(rootPath, relative), 100*time.Millisecond)
				if e == nil {
					conn.Close()
					return managed.ErrBlocked
				}
				if !errors.Is(e, unix.ECONNREFUSED) && !errors.Is(e, os.ErrNotExist) {
					return e
				}
				id, e := platform.PersistentSocketIdentityAt(dir.fd(), name)
				if e != nil {
					return fail("socket_attributes", e)
				}
				if id.FileID != s.Ino || id.CreatedSeconds != s.Btim.Sec || id.CreatedNanos != s.Btim.Nsec {
					return managed.ErrConflict
				}
				entries = append(entries, File{Path: relative, Kind: "socket", Identity: id, OwnerUID: s.Uid, Mode: uint32(s.Mode & 07777)})
			default:
				return managed.ErrConflict
			}
		}
		return nil
	}
	// Readdir has descriptor-local offset; each inventory starts from zero.
	if _, err := root.file.Seek(0, 0); err != nil {
		return nil, err
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}
func equalFiles(a, b []File) bool { return bytes.Equal(canonical(a), canonical(b)) }
func contained(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
func absent(parent *held, name string) error {
	var s unix.Stat_t
	err := unix.Fstatat(parent.fd(), name, &s, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err == nil {
		return managed.ErrConflict
	}
	return err
}
func lockExisting(root *held, name string) (*held, error) {
	h, err := openAt(root.fd(), name, false)
	if err != nil {
		return nil, err
	}
	if h.stat.Mode&unix.S_IFMT != unix.S_IFREG || h.stat.Mode&0077 != 0 {
		h.close()
		return nil, managed.ErrConflict
	}
	if err = unix.Flock(h.fd(), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		h.close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, managed.ErrBlocked
		}
		return nil, err
	}
	if err = h.matches(root.fd(), name); err != nil {
		h.close()
		return nil, err
	}
	return h, nil
}
