package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

// GuestControl is a guest-native filesystem-worker receipt, separate from the
// workload receipts. A lost SSH observer never establishes worker termination.
type GuestControl struct {
	Schema        string                   `json:"schema"`
	Request       GuestRequest             `json:"request"`
	NativeRequest *Request                 `json:"native_request,omitempty"`
	Root          *managed.RetainedPath    `json:"root,omitempty"`
	Owner         platform.ProcessIdentity `json:"owner"`
	Phase         string                   `json:"phase"`
	Retired       bool                     `json:"retired"`
	Stopped       bool                     `json:"worker_stopped"`
	Complete      bool                     `json:"complete"`
	Error         string                   `json:"error,omitempty"`
	Observation   *Observation             `json:"observation,omitempty"`
}
type GuestFile struct {
	Path            string `json:"path"`
	Device          uint64 `json:"device"`
	Inode           uint64 `json:"inode"`
	Size            int64  `json:"size"`
	SHA256          string `json:"sha256"`
	ModifiedSeconds int64  `json:"modified_seconds"`
	ModifiedNanos   int64  `json:"modified_nanos"`
}
type guestControlFile struct {
	file  *os.File
	value GuestControl
}

func (c *guestControlFile) save() error {
	data, err := json.Marshal(c.value)
	if err != nil || len(data) > receiptLimit {
		return ErrInvalid
	}
	if err = c.file.Truncate(0); err != nil {
		return err
	}
	if _, err = c.file.WriteAt(data, 0); err != nil {
		return err
	}
	return c.file.Sync()
}
func (c *guestControlFile) bindRoot(d *directory) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(d.file.Fd()), &st); err != nil {
		return err
	}
	c.value.Root = &managed.RetainedPath{Path: "", Device: uint64(st.Dev), Inode: uint64(st.Ino), Directory: true}
	return c.save()
}
func sameGuestOwner(a, b GuestRequest) bool {
	x, y := a.Request, b.Request
	// Prepare's pre-publication request has no guest output inode yet.
	x.LogDevice, x.LogInode = 0, 0
	y.LogDevice, y.LogInode = 0, 0
	return a.VMUUID == b.VMUUID && a.MAC == b.MAC && a.BootID == b.BootID &&
		a.JobRoot == b.JobRoot && a.RunnerDigest == b.RunnerDigest && a.ObjectID == b.ObjectID &&
		a.InputDigest == b.InputDigest && a.ProfileDigest == b.ProfileDigest && reflect.DeepEqual(x, y)
}
func guestOwnerStopped(p platform.ProcessIdentity) bool {
	current, err := platform.ObserveProcess(p.PID)
	return errors.Is(err, unix.ESRCH) || err == nil && current.Identity != p
}

// All effects on one guest Job serialize through a private, single-link inode.
// The control receipt remains outside the Job directory until cleanup and a
// fresh health observation prove reuse. It is never a permanent guest service.
func Guest(q GuestRequest) (GuestReply, error) {
	reply, err := guestIdentity(q)
	if err != nil {
		return reply, err
	}
	if q.Deadline.IsZero() || !time.Now().Before(q.Deadline) {
		return reply, ErrInvalid
	}
	if q.Operation == "identity" {
		return reply, nil
	}
	switch q.Operation {
	case "prepare", "start", "inspect", "watch", "cancel", "output", "root", "file", "cleanup", "retire", "state", "health":
	default:
		return reply, ErrInvalid
	}
	if q.Offset < 0 {
		return reply, ErrInvalid
	}
	if !managed.ValidRequestKey(q.Request.Identity.JobID) || q.Request.BootID != q.BootID || q.BootID == "" ||
		!guestDigest(q.InputDigest) || !guestDigest(q.ProfileDigest) || !guestDigest(q.ObjectID) {
		return reply, ErrInvalid
	}
	probe := q.Request
	if probe.LogInode == 0 {
		probe.LogInode = 1
	}
	if probe.Validate(filepath.Join(q.JobRoot, q.Request.Identity.JobID)) != nil {
		return reply, ErrInvalid
	}
	parent, err := openDirectory(q.JobRoot)
	if err != nil {
		return reply, err
	}
	defer parent.close()
	name := ".haepari-control-" + q.Request.Identity.JobID + ".json"
	fresh := false
	f, err := parent.open(name, unix.O_RDWR)
	if errors.Is(err, unix.ENOENT) && q.Operation == "prepare" {
		f, err = parent.open(name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
		fresh = err == nil
	}
	if err != nil {

		return reply, err
	}
	defer f.Close()
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return reply, ErrUnproven
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	data, err := io.ReadAll(io.NewSectionReader(f, 0, receiptLimit+1))
	if err != nil || len(data) > receiptLimit {
		return reply, ErrInvalid
	}
	c := guestControlFile{file: f}
	if len(data) == 0 {
		if !fresh {
			return reply, ErrConflict
		}
		owner := q
		owner.Files = nil
		owner.Inventory = nil
		owner.Challenge = ""
		owner.Root = nil
		c.value = GuestControl{Schema: "haepari.guest-control/v1", Request: owner}
		if err = parent.file.Sync(); err != nil {
			return reply, err
		}
	} else {
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if managed.ValidateUniqueJSONKeys(data) != nil || d.Decode(&c.value) != nil || d.Decode(new(any)) != io.EOF ||
			c.value.Schema != "haepari.guest-control/v1" || !sameGuestOwner(c.value.Request, q) {
			return reply, ErrConflict
		}
		if q.Root != nil && c.value.Root != nil && !reflect.DeepEqual(q.Root, c.value.Root) {
			return reply, ErrConflict
		}
		if !c.value.Complete && !guestOwnerStopped(c.value.Owner) {
			return reply, ErrUnproven
		}
	}
	if !fresh && c.value.Root == nil && (q.Operation == "state" || q.Operation == "retire") {
		if err = c.observeInitialRoot(parent, q); err != nil {
			return reply, err
		}
	}
	if q.Root != nil && !reflect.DeepEqual(q.Root, c.value.Root) {
		return reply, ErrConflict
	}
	reply.Control = &c.value
	reply.Root = c.value.Root
	reply.Request = c.value.NativeRequest
	if q.Operation == "state" {
		if c.value.Root != nil {
			var st unix.Stat_t
			e := unix.Fstatat(int(parent.file.Fd()), q.Request.Identity.JobID, &st, unix.AT_SYMLINK_NOFOLLOW)
			if e != nil && !(errors.Is(e, unix.ENOENT) && c.value.Phase == "cleanup") || e == nil && (uint64(st.Dev) != c.value.Root.Device || uint64(st.Ino) != c.value.Root.Inode || uint32(st.Mode)&unix.S_IFMT != unix.S_IFDIR) {
				return reply, ErrConflict
			}
		}
		c.value.Stopped = c.value.Complete || guestOwnerStopped(c.value.Owner)
		reply.Observation = c.value.Observation
		return reply, nil
	}
	if q.Operation == "health" {
		var st unix.Stat_t
		absent := errors.Is(unix.Fstatat(int(parent.file.Fd()), q.Request.Identity.JobID, &st, unix.AT_SYMLINK_NOFOLLOW), unix.ENOENT)
		stopped := c.value.Complete || guestOwnerStopped(c.value.Owner)
		nativeStopped := c.value.Observation != nil && (c.value.Observation.Exit != nil && c.value.Observation.Exit.TerminationProven || c.value.Retired && c.value.Observation.State == "prepared" && c.value.Observation.Intent == nil) || c.value.Retired && c.value.NativeRequest == nil
		c.value.Stopped = stopped
		if !absent || !stopped || c.value.Root == nil || !nativeStopped {
			return reply, nil
		}

		reply.Reusable = true
		reply.Observation = c.value.Observation
		return reply, nil
	}
	owner, err := platform.ObserveProcess(os.Getpid())
	if err != nil || owner.Zombie {
		return reply, ErrUnproven
	}
	if c.value.Retired && (q.Operation == "prepare" || q.Operation == "start") {
		return reply, ErrConflict
	}
	if !fresh && q.Operation == "prepare" {
		if !c.value.Complete || c.value.NativeRequest == nil || c.value.Root == nil {
			return reply, ErrAmbiguous
		}
		var st unix.Stat_t
		if unix.Fstatat(int(parent.file.Fd()), q.Request.Identity.JobID, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || uint64(st.Dev) != c.value.Root.Device || uint64(st.Ino) != c.value.Root.Inode {
			return reply, ErrConflict
		}
	}
	if c.value.NativeRequest == nil && q.Operation != "prepare" && q.Operation != "retire" && q.Operation != "cleanup" {
		return reply, ErrAmbiguous
	}
	partialCleanup := q.Operation == "cleanup" && c.value.NativeRequest == nil && c.value.Retired
	c.value.Owner = owner.Identity
	c.value.Phase = q.Operation
	c.value.Complete = false
	c.value.Stopped = false
	c.value.Error = ""
	if err = c.save(); err != nil {
		return reply, err
	}
	if q.Operation == "retire" {
		reply, err = retireGuest(parent, q, reply, &c)
	} else if partialCleanup {
		if !c.value.Retired {
			return reply, ErrUnproven
		}
		err = cleanupGuestPreparation(parent, q, c.value.Root)
	} else {
		reply, err = guestEffect(q, &c)
	}
	c.value.Complete = true
	c.value.Stopped = true
	if err != nil {
		c.value.Error = err.Error()
	} else {
		if reply.Request != nil {
			c.value.NativeRequest = reply.Request
		}
		if reply.Observation != nil {
			c.value.Observation = reply.Observation
		}
	}
	if e := c.save(); e != nil {
		return reply, e
	}
	reply.Control = &c.value
	reply.Root = c.value.Root
	return reply, err
}

// The durable prepare intent claims the exact private Job name before mkdir.
// Root binding precedes every payload write, so only an empty directory can be
// reconciled after the preparer stops in that window. Never adopt partial files.
func (c *guestControlFile) observeInitialRoot(parent *directory, q GuestRequest) error {
	probe := q
	probe.Root = nil
	if c.value.Phase != "prepare" || c.value.NativeRequest != nil || c.value.Retired ||
		ValidateGuestControl(c.value, probe) != nil || !c.value.Complete && !guestOwnerStopped(c.value.Owner) {
		return ErrUnproven
	}
	d, err := openDirectory(filepath.Join(q.JobRoot, q.Request.Identity.JobID))
	if err != nil {
		return err
	}
	defer d.close()
	entries, err := d.file.ReadDir(1)
	if len(entries) != 0 || !errors.Is(err, io.EOF) {
		return ErrConflict
	}
	var opened, current unix.Stat_t
	if unix.Fstat(int(d.file.Fd()), &opened) != nil ||
		unix.Fstatat(int(parent.file.Fd()), q.Request.Identity.JobID, &current, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		opened.Dev != current.Dev || opened.Ino != current.Ino {
		return ErrConflict
	}
	// State is read-only. The retirement effect syncs this observed binding
	// with its worker intent before any barrier or cleanup can be authorized.
	c.value.Root = &managed.RetainedPath{Path: "", Device: uint64(opened.Dev), Inode: uint64(opened.Ino), Directory: true}
	return nil
}

func guestFiles(parent, d *directory, q GuestRequest, reply GuestReply, c *guestControlFile) (GuestReply, error) {
	var st unix.Stat_t
	if unix.Fstat(int(d.file.Fd()), &st) != nil || c.value.Root == nil ||
		c.value.Root.Device != uint64(st.Dev) || c.value.Root.Inode != uint64(st.Ino) {
		return reply, ErrConflict
	}
	reply.Root = c.value.Root
	if q.Operation == "root" {
		return reply, nil
	}
	view := reply.Observation
	if view == nil || !(view.Exit != nil && view.Exit.TerminationProven || q.Operation == "cleanup" && c.value.Retired && view.State == "prepared" && view.Intent == nil) {
		return reply, ErrUnproven
	}
	if q.Operation == "cleanup" {
		// Keep the last native termination observation outside the deleted subtree.
		c.value.Observation = view
		c.value.NativeRequest = reply.Request
		if err := c.save(); err != nil {
			return reply, err
		}
		boundary("guest_cleanup_bound_root")
		if err := guestRemoveBound(int(parent.file.Fd()), q.Request.Identity.JobID, q.Deadline, c.value.Root); err != nil {
			return reply, err
		}
		return reply, nil
	}
	f, err := guestOutputFile(d, q.Path, q.Area)
	if err != nil {
		return reply, err
	}
	defer f.Close()
	return readGuestFile(f, q, reply)
}

func readGuestFile(f *os.File, q GuestRequest, reply GuestReply) (GuestReply, error) {
	var err error
	var before, after unix.Stat_t
	if unix.Fstat(int(f.Fd()), &before) != nil || before.Size < 0 || before.Size > 1<<30 || q.Offset < 0 || q.Offset > before.Size {
		return reply, ErrInvalid
	}
	metadata := GuestFile{Path: q.Path, Device: uint64(before.Dev), Inode: uint64(before.Ino), Size: before.Size, ModifiedSeconds: before.Mtim.Sec, ModifiedNanos: before.Mtim.Nsec}
	if q.ExpectedFile != nil {
		expected := *q.ExpectedFile
		metadata.SHA256 = expected.SHA256
		if expected != metadata || len(expected.SHA256) != 64 {
			return reply, ErrConflict
		}
	} else {
		h := sha256.New()
		if _, err = guestCopyUntil(h, f, q.Deadline); err != nil {
			return reply, err
		}
		metadata.SHA256 = hex.EncodeToString(h.Sum(nil))
	}
	data := make([]byte, 64<<10)
	n, e := f.ReadAt(data, q.Offset)
	if e != nil && e != io.EOF {
		return reply, e
	}
	if unix.Fstat(int(f.Fd()), &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim {
		return reply, ErrConflict
	}
	reply.File = &metadata
	reply.Output = data[:n]
	reply.NextOffset = q.Offset + int64(n)
	return reply, nil
}
func guestOutputFile(d *directory, name, area string) (*os.File, error) {
	if area == "log" {
		if name != "output.log" {
			return nil, ErrInvalid
		}
		return d.open(name, unix.O_RDONLY)
	}
	if area != "" && area != "workspace" {
		return nil, ErrInvalid
	}
	if name == "" || filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || strings.HasPrefix(name, "../") || name == ".." {
		return nil, ErrInvalid
	}
	fd, err := unix.Openat(int(d.file.Fd()), "workspace", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { unix.Close(fd) }()
	parts := strings.Split(name, "/")
	for _, p := range parts[:len(parts)-1] {
		next, e := unix.Openat(fd, p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return nil, e
		}
		unix.Close(fd)
		fd = next
	}
	out, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(out, &st) != nil || uint32(st.Mode)&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) {
		unix.Close(out)
		return nil, ErrInvalid
	}
	return os.NewFile(uintptr(out), name), nil
}
func guestCopyUntil(dst io.Writer, src io.Reader, deadline time.Time) (int64, error) {
	buf := make([]byte, 64<<10)
	var total int64
	for time.Now().Before(deadline) {
		n, err := src.Read(buf)
		if n > 0 {
			m, e := dst.Write(buf[:n])
			total += int64(m)
			if e != nil {
				return total, e
			}
			if m != n {
				return total, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
	return total, ErrUnproven
}
func guestRemove(parent int, name string, deadline time.Time) error {
	return guestRemoveBound(parent, name, deadline, nil)
}

func guestRemoveBound(parent int, name string, deadline time.Time, expected *managed.RetainedPath) error {
	if !time.Now().Before(deadline) {
		return ErrUnproven
	}
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if expected != nil && (!expected.Directory || uint32(st.Mode)&unix.S_IFMT != unix.S_IFDIR || uint64(st.Dev) != expected.Device || st.Ino != expected.Inode) {
		return ErrConflict
	}
	if uint32(st.Mode)&unix.S_IFMT != unix.S_IFDIR {
		boundary("guest_cleanup_unlink_file")
		if !time.Now().Before(deadline) {
			return ErrUnproven
		}
		return unix.Unlinkat(parent, name, 0)
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var opened unix.Stat_t
	if unix.Fstat(fd, &opened) != nil || opened.Dev != st.Dev || opened.Ino != st.Ino {
		return ErrConflict
	}
	for {
		entries, e := f.ReadDir(128)
		if e != nil && e != io.EOF {
			return e
		}
		for _, entry := range entries {
			if err = guestRemove(fd, entry.Name(), deadline); err != nil {
				return err
			}
		}
		if e == io.EOF {
			break
		}
	}
	var current unix.Stat_t
	if unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Dev != st.Dev || current.Ino != st.Ino {
		return ErrConflict
	}
	boundary("guest_cleanup_unlink_directory")
	if !time.Now().Before(deadline) {
		return ErrUnproven
	}
	if err = unix.Unlinkat(parent, name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	if expected != nil {
		boundary("guest_cleanup_root_unlinked")
	}
	return unix.Fsync(parent)
}

// ValidateGuestControl binds filesystem-worker observations to the original
// prepared identity, independently of workload process evidence.
func ValidateGuestControl(c GuestControl, q GuestRequest) error {
	if c.Schema != "haepari.guest-control/v1" || !sameGuestOwner(c.Request, q) ||
		c.Owner.PID <= 0 || c.Owner.BootID != q.BootID || c.Owner.StartedSeconds <= 0 ||
		!strings.Contains("|prepare|start|inspect|watch|cancel|output|root|file|cleanup|retire|", "|"+c.Phase+"|") {
		return ErrConflict
	}
	if q.Root != nil && !reflect.DeepEqual(c.Root, q.Root) {
		return ErrConflict
	}
	if c.Root != nil && (c.Root.Path != "" || !c.Root.Directory || c.Root.Inode == 0) {
		return ErrConflict
	}
	return nil
}
func cleanupGuestPreparation(parent *directory, q GuestRequest, root *managed.RetainedPath) error {
	if root == nil {
		return ErrUnproven
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(parent.file.Fd()), q.Request.Identity.JobID, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if root.Device != uint64(st.Dev) || root.Inode != uint64(st.Ino) || uint32(st.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return ErrConflict
	}
	boundary("guest_cleanup_bound_preparation")
	return guestRemoveBound(int(parent.file.Fd()), q.Request.Identity.JobID, q.Deadline, root)
}

// Retirement serializes with launch through the same durable worker inode. A
// delayed original SSH start cannot launch after a proven no-start recovery.
func retireGuest(parent *directory, q GuestRequest, reply GuestReply, c *guestControlFile) (GuestReply, error) {
	if c.value.Retired {
		reply.Request = c.value.NativeRequest
		reply.Observation = c.value.Observation
		return reply, nil
	}
	if c.value.Root == nil {
		return reply, ErrUnproven
	}
	d, err := openDirectory(filepath.Join(q.JobRoot, q.Request.Identity.JobID))
	if err != nil {
		return reply, err
	}
	defer d.close()
	var st unix.Stat_t
	if unix.Fstat(int(d.file.Fd()), &st) != nil || uint64(st.Dev) != c.value.Root.Device || uint64(st.Ino) != c.value.Root.Inode {
		return reply, ErrConflict
	}
	probe := q
	probe.Operation = "inspect"
	if c.value.NativeRequest != nil {
		reply, err = guestEffect(probe, c)
		if err != nil {
			return reply, err
		}
		if reply.Observation == nil || reply.Observation.State != "prepared" || reply.Observation.Intent != nil {
			return reply, ErrUnproven
		}
	} else {
		var claim guestLaunch
		if e := d.read("launch.json", &claim); !errors.Is(e, unix.ENOENT) {
			return reply, ErrUnproven
		}
	}
	c.value.Retired = true
	if err = c.save(); err != nil {
		return reply, err
	}
	reply.Request = c.value.NativeRequest
	return reply, nil
}
