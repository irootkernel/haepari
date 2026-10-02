package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ObjectIdentity is an incarnation fence for an already verified sealed
// object. The caller owns durable reference checking and deletion authority.
func (s *Store) ObjectIdentity(id string) (uint64, uint64, error) {
	if !validDigest(id) {
		return 0, 0, ErrInvalid
	}
	fd, err := unix.Open(s.root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, 0, err
	}
	defer unix.Close(fd)
	next, err := unix.Openat(fd, strings.TrimPrefix(id, "sha256:"), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, 0, err
	}
	defer unix.Close(next)
	var st unix.Stat_t
	if err = unix.Fstat(next, &st); err != nil {
		return 0, 0, err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		return 0, 0, ErrInvalid
	}
	return uint64(st.Dev), st.Ino, nil
}

// PurgeOwned is serialized with sealing. It may finish a partially deleted
// object only under its original durable inode fence; it refuses replacements.
func (s *Store) PlanPurgeOwned(ctx context.Context, id string, device, inode uint64, inventory []byte) ([]PurgePath, error) {
	return s.purgeOwned(ctx, id, device, inode, inventory, nil, false)
}

func (s *Store) PurgeOwned(ctx context.Context, id string, device, inode uint64, inventory []byte, fences []PurgePath) error {
	_, err := s.purgeOwned(ctx, id, device, inode, inventory, fences, true)
	return err
}

func (s *Store) purgeOwned(ctx context.Context, id string, device, inode uint64, inventory []byte, fences []PurgePath, remove bool) ([]PurgePath, error) {
	if !validDigest(id) || inode == 0 {
		return nil, ErrInvalid
	}
	lock, err := openPrivateFile(path.Join(s.root, ".seal.lock"), unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	dev, ino, err := s.ObjectIdentity(id)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if dev != device || ino != inode {
		return nil, ErrChanged
	}
	m, err := DecodeInventory(inventory, id)
	if err != nil {
		return nil, err
	}
	allowed := map[string]Entry{"manifest.json": {Path: "manifest.json", Size: int64(len(inventory)), SHA256: "sha256:" + digest(inventory)}}
	dirs := map[string]bool{"files": true}
	for _, entry := range m.Entries {
		name := "files/" + entry.Path
		allowed[name] = entry
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			dirs[parent] = true
		}
	}
	rootFD, err := unix.Open(s.root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	objectName := strings.TrimPrefix(id, "sha256:")
	objectFD, err := unix.Openat(rootFD, objectName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(objectFD)
	var current unix.Stat_t
	if err = unix.Fstat(objectFD, &current); err != nil {
		return nil, err
	}
	if uint64(current.Dev) != device || current.Ino != inode {
		return nil, ErrChanged
	}
	entries := []PurgePath{}
	if err = planObjectPurge(ctx, objectFD, "", allowed, dirs, &entries); err != nil {
		return nil, err
	}
	if !remove {
		return entries, nil
	}
	fenceMap := map[string]PurgePath{}
	for _, fence := range fences {
		fenceMap[fence.Path] = fence
	}
	if len(fenceMap) != len(fences) || len(fences) == 0 {
		return nil, ErrInvalid
	}
	for _, entry := range entries {
		prior, ok := fenceMap[entry.Path]
		if !ok || prior != entry {
			return nil, ErrChanged
		}
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if err = removeObjectEntry(objectFD, entry, fenceMap); err != nil {
			return nil, err
		}
	}
	if err = unix.Fstatat(rootFD, objectName, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if uint64(current.Dev) != device || current.Ino != inode {
		return nil, ErrChanged
	}
	if err = unix.Unlinkat(rootFD, objectName, unix.AT_REMOVEDIR); err != nil {
		return nil, err
	}
	return nil, syncDir(s.root)
}

type PurgePath struct {
	Path      string `json:"path"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	Directory bool   `json:"directory"`
}

func planObjectPurge(ctx context.Context, fd int, prefix string, allowed map[string]Entry, dirs map[string]bool, out *[]PurgePath) error {
	dup, err := unix.Dup(fd)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(dup), prefix)
	defer f.Close()
	children, err := f.ReadDir(100001)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(children) > 100000 {
		return ErrInvalid
	}
	for _, child := range children {
		if err = ctx.Err(); err != nil {
			return err
		}
		if len(*out) >= 100000 {
			return ErrInvalid
		}
		name := path.Join(prefix, child.Name())
		var st unix.Stat_t
		if err = unix.Fstatat(fd, child.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		directory := st.Mode&unix.S_IFMT == unix.S_IFDIR
		if st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 || !directory && (st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1) {
			return ErrCorrupt
		}
		if directory {
			if !dirs[name] {
				return ErrCorrupt
			}
			next, err := unix.Openat(fd, child.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			if err = unix.Fstat(next, &opened); err != nil || opened.Dev != st.Dev || opened.Ino != st.Ino || opened.Mode != st.Mode || opened.Uid != st.Uid {
				unix.Close(next)
				return ErrChanged
			}
			err = planObjectPurge(ctx, next, name, allowed, dirs, out)
			unix.Close(next)
			if err != nil {
				return err
			}
		} else {
			expected, ok := allowed[name]
			if !ok || st.Size != expected.Size {
				return ErrCorrupt
			}
			next, err := unix.Openat(fd, child.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			if err = unix.Fstat(next, &opened); err != nil || opened.Dev != st.Dev || opened.Ino != st.Ino || opened.Mode != st.Mode || opened.Size != st.Size || opened.Nlink != 1 || opened.Uid != st.Uid {
				unix.Close(next)
				return ErrChanged
			}
			if name != "manifest.json" && (st.Mode&0111 != 0) != expected.Executable {
				unix.Close(next)
				return ErrCorrupt
			}
			file := os.NewFile(uintptr(next), name)
			h := sha256.New()
			_, err = io.CopyBuffer(h, contextReader{ctx, io.LimitReader(file, expected.Size+1)}, make([]byte, 64<<10))
			var after unix.Stat_t
			statErr := unix.Fstat(next, &after)
			file.Close()
			if statErr != nil || after.Dev != opened.Dev || after.Ino != opened.Ino || after.Size != opened.Size || after.Mtim != opened.Mtim || after.Mode != opened.Mode {
				return ErrChanged
			}
			if err != nil {
				return err
			}
			if "sha256:"+hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
				return ErrCorrupt
			}
		}
		if len(*out) >= 100000 {
			return ErrInvalid
		}
		*out = append(*out, PurgePath{name, uint64(st.Dev), st.Ino, directory})
	}
	return nil
}

func removeObjectEntry(root int, entry PurgePath, fences map[string]PurgePath) error {
	parts := strings.Split(entry.Path, "/")
	fd, err := unix.Dup(root)
	if err != nil {
		return err
	}
	defer func() { unix.Close(fd) }()
	prefix := ""
	for _, name := range parts[:len(parts)-1] {
		if prefix != "" {
			prefix += "/"
		}
		prefix += name
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		var current unix.Stat_t
		fence, ok := fences[prefix]
		if err = unix.Fstat(next, &current); err != nil || !ok || !fence.Directory || uint64(current.Dev) != fence.Device || current.Ino != fence.Inode {
			unix.Close(next)
			return ErrChanged
		}
		unix.Close(fd)
		fd = next
	}
	var st unix.Stat_t
	name := parts[len(parts)-1]
	if err = unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if uint64(st.Dev) != entry.Device || st.Ino != entry.Inode || entry.Directory != (st.Mode&unix.S_IFMT == unix.S_IFDIR) {
		return ErrChanged
	}
	flag := 0
	if entry.Directory {
		flag = unix.AT_REMOVEDIR
	}
	return unix.Unlinkat(fd, name, flag)
}
