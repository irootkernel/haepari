//go:build darwin

package platform

import (
	"encoding/binary"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// PersistentSocketIdentityAt uses getattrlistat because Darwin rejects opening
// Unix socket vnodes with O_EVTONLY. It requests the same native FILEID/CRTIME
// attributes as PersistentFileIdentity, under a held no-follow parent, and uses
// that parent's verified local persistent-object capability and volume UUID.
func PersistentSocketIdentityAt(parent int, name string) (FileIdentity, error) {
	if name == "" || strings.Contains(name, "/") {
		return FileIdentity{}, unix.EINVAL
	}
	volume, err := PersistentFileIdentity(parent)
	if err != nil {
		return FileIdentity{}, err
	}
	var directory, before, after unix.Stat_t
	if err = unix.Fstat(parent, &directory); err != nil {
		return FileIdentity{}, err
	}
	if err = unix.Fstatat(parent, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return FileIdentity{}, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFSOCK || before.Dev != directory.Dev || before.Nlink != 1 || before.Uid != uint32(os.Geteuid()) || before.Mode&0077 != 0 {
		return FileIdentity{}, unix.ENOTSUP
	}
	filename, err := unix.BytePtrFromString(name)
	if err != nil {
		return FileIdentity{}, err
	}
	attributes := unix.Attrlist{Bitmapcount: 5, Commonattr: unix.ATTR_CMN_CRTIME | unix.ATTR_CMN_FILEID}
	buffer := make([]byte, 28)
	// sys/unistd.h: getattrlistat(int,const char*,void*,void*,size_t,unsigned long).
	// sys/attr.h: FSOPT_NOFOLLOW = 1. A single basename has no intermediate links.
	_, _, errno := unix.Syscall6(unix.SYS_GETATTRLISTAT, uintptr(parent), uintptr(unsafe.Pointer(filename)), uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 1)
	runtime.KeepAlive(filename)
	runtime.KeepAlive(&attributes)
	runtime.KeepAlive(buffer)
	if errno != 0 {
		return FileIdentity{}, errno
	}
	if binary.NativeEndian.Uint32(buffer) != uint32(len(buffer)) {
		return FileIdentity{}, unix.ENOTSUP
	}
	identity := FileIdentity{VolumeUUID: volume.VolumeUUID, FileID: binary.NativeEndian.Uint64(buffer[20:28]), CreatedSeconds: int64(binary.NativeEndian.Uint64(buffer[4:12])), CreatedNanos: int64(binary.NativeEndian.Uint64(buffer[12:20]))}
	if err = unix.Fstatat(parent, name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return FileIdentity{}, err
	}
	if !identity.Valid() || identity.FileID != before.Ino || identity.CreatedSeconds != before.Btim.Sec || identity.CreatedNanos != before.Btim.Nsec || before.Dev != after.Dev || before.Ino != after.Ino || before.Btim != after.Btim || before.Uid != after.Uid || before.Mode != after.Mode || before.Nlink != after.Nlink {
		return FileIdentity{}, unix.ENOTSUP
	}
	return identity, nil
}
