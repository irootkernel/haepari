//go:build darwin

package platform

import (
	"encoding/binary"
	"encoding/hex"
	"golang.org/x/sys/unix"
	"unsafe"
)

func descriptorAttributes(fd int, common, volume uint32, size int) ([]byte, error) {
	a := unix.Attrlist{Bitmapcount: 5, Commonattr: common, Volattr: volume}
	b := make([]byte, size)
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, uintptr(fd), uintptr(unsafe.Pointer(&a)), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, 0)
	if errno != 0 {
		return nil, errno
	}
	if int(binary.NativeEndian.Uint32(b)) != size {
		return nil, unix.ENOTSUP
	}
	return b, nil
}

// PersistentFileIdentity requires a local filesystem reporting a volume UUID,
// 64-bit file ID and creation time. Missing attributes fail closed.
func PersistentFileIdentity(fd int) (FileIdentity, error) {
	var fs unix.Statfs_t
	var before, after unix.Stat_t
	if err := unix.Fstatfs(fd, &fs); err != nil {
		return FileIdentity{}, err
	}
	if fs.Flags&unix.MNT_LOCAL == 0 {
		return FileIdentity{}, unix.ENOTSUP
	}
	if err := unix.Fstat(fd, &before); err != nil {
		return FileIdentity{}, err
	}
	c, err := descriptorAttributes(fd, unix.ATTR_CMN_CRTIME|unix.ATTR_CMN_FILEID, 0, 28)
	if err != nil {
		return FileIdentity{}, err
	}
	caps, err := descriptorAttributes(fd, 0, unix.ATTR_VOL_INFO|unix.ATTR_VOL_CAPABILITIES, 36)
	if err != nil {
		return FileIdentity{}, err
	}
	// sys/attr.h: VOL_CAP_FMT_PERSISTENTOBJECTIDS must be both valid and set.
	const persistentObjectIDs = uint32(0x00000001)
	if binary.NativeEndian.Uint32(caps[4:8])&binary.NativeEndian.Uint32(caps[20:24])&persistentObjectIDs == 0 {
		return FileIdentity{}, unix.ENOTSUP
	}
	v, err := descriptorAttributes(fd, 0, unix.ATTR_VOL_INFO|unix.ATTR_VOL_UUID, 20)
	if err != nil {
		return FileIdentity{}, err
	}
	i := FileIdentity{VolumeUUID: hex.EncodeToString(v[4:20]), FileID: binary.NativeEndian.Uint64(c[20:28]), CreatedSeconds: int64(binary.NativeEndian.Uint64(c[4:12])), CreatedNanos: int64(binary.NativeEndian.Uint64(c[12:20]))}
	if unix.Fstat(fd, &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Btim != after.Btim || i.FileID != before.Ino || i.CreatedSeconds != before.Btim.Sec || i.CreatedNanos != before.Btim.Nsec || !i.Valid() {
		return FileIdentity{}, unix.ENOTSUP
	}
	return i, nil
}
