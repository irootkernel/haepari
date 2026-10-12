package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"golang.org/x/sys/unix"
)

// GuestTransportBinding is an immutable preparation observation. It does not
// replace the original native request, phase deadlines or control receipt.
type GuestTransportBinding struct {
	Schema string               `json:"schema"`
	Guest  GuestRequest         `json:"guest"`
	Root   managed.RetainedPath `json:"root"`
	Runner managed.RetainedPath `json:"runner"`
}
type GuestTransportRequest struct {
	Recovery       *GuestRecoveryRequest  `json:"recovery,omitempty"`
	Schema         string                 `json:"schema"`
	Guest          GuestRequest           `json:"guest"`
	ExecutorDigest string                 `json:"executor_digest"`
	Binding        *GuestTransportBinding `json:"binding,omitempty"`
}
type GuestTransportReply struct {
	Recovery       *GuestRecoveryReply    `json:"recovery,omitempty"`
	Schema         string                 `json:"schema"`
	ProtocolDigest string                 `json:"protocol_digest"`
	Guest          GuestReply             `json:"guest"`
	Binding        *GuestTransportBinding `json:"binding,omitempty"`
}

func GuestTransportDigest(q GuestTransportRequest) string { return managed.RecoveryDigest(q) }

func ValidateGuestTransportBinding(b GuestTransportBinding, q GuestRequest) error {
	if b.Schema != "haepari.guest-transport-binding/v1" || !sameGuestOwner(b.Guest, q) || b.Guest.Schema != q.Schema || b.Guest.Operation != "prepare" || b.Guest.Challenge != "" || b.Guest.Root == nil || !reflect.DeepEqual(*b.Guest.Root, b.Root) ||
		!managed.ValidRequestKey(q.Request.Identity.JobID) || !filepath.IsAbs(q.JobRoot) || filepath.Clean(q.JobRoot) != q.JobRoot || q.Request.Validate(filepath.Join(q.JobRoot, q.Request.Identity.JobID)) != nil || !guestDigest(q.RunnerDigest) ||
		!reflect.DeepEqual(b.Guest.Request, q.Request) || b.Root.Path != "" || !b.Root.Directory || b.Root.Inode == 0 ||
		b.Runner.Path != "haepari" || b.Runner.Directory || b.Runner.Inode == 0 ||
		q.Root == nil || !reflect.DeepEqual(*q.Root, b.Root) || len(b.Guest.Files) != 0 || len(b.Guest.Inventory) != 0 {
		return ErrConflict
	}
	return nil
}

func sameTransportStat(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim && a.Mode == b.Mode && a.Uid == b.Uid && a.Nlink == b.Nlink
}

func transportRunner(q GuestRequest, root managed.RetainedPath) (managed.RetainedPath, error) {
	d, err := openDirectory(filepath.Join(q.JobRoot, q.Request.Identity.JobID))
	if err != nil {
		return managed.RetainedPath{}, err
	}
	defer d.close()
	var st unix.Stat_t
	if unix.Fstat(int(d.file.Fd()), &st) != nil || uint64(st.Dev) != root.Device || st.Ino != root.Inode {
		return managed.RetainedPath{}, ErrConflict
	}
	f, err := d.open("haepari", unix.O_RDONLY)
	if err != nil {
		return managed.RetainedPath{}, err
	}
	defer f.Close()
	if unix.Fstat(int(f.Fd()), &st) != nil || st.Size < 1 || st.Size > 64<<20 {
		return managed.RetainedPath{}, ErrInvalid
	}
	before := st
	h := sha256.New()
	n, err := guestCopyUntil(h, io.LimitReader(f, (64<<20)+1), q.Deadline)
	if err != nil || n != before.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != q.RunnerDigest {
		return managed.RetainedPath{}, ErrConflict
	}
	var path unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) != nil || !sameTransportStat(st, before) || unix.Fstatat(int(d.file.Fd()), "haepari", &path, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameTransportStat(path, before) {
		return managed.RetainedPath{}, ErrConflict
	}
	// Reopen the pathname to reject a renamed/replaced root as well as its file.
	current, err := openDirectory(filepath.Join(q.JobRoot, q.Request.Identity.JobID))
	if err != nil {
		return managed.RetainedPath{}, err
	}
	defer current.close()
	if unix.Fstat(int(current.file.Fd()), &st) != nil || uint64(st.Dev) != root.Device || st.Ino != root.Inode {
		return managed.RetainedPath{}, ErrConflict
	}
	return managed.RetainedPath{Path: "haepari", Device: uint64(before.Dev), Inode: before.Ino}, nil
}

func GuestTransport(q GuestTransportRequest) (GuestTransportReply, error) {
	r := GuestTransportReply{Schema: "haepari.guest-transport-reply/v1", ProtocolDigest: GuestTransportDigest(q)}
	deadline := q.Guest.Deadline
	if q.Recovery != nil {
		deadline = q.Recovery.Deadline
	}
	if q.Schema != "haepari.guest-transport-request/v1" || !guestDigest(q.ExecutorDigest) || deadline.IsZero() || !time.Now().Before(deadline) {
		return r, ErrInvalid
	}
	self, err := os.Executable()
	if err != nil {
		return r, err
	}
	f, err := os.OpenFile(self, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return r, err
	}
	h := sha256.New()
	n, hashErr := guestCopyUntil(h, io.LimitReader(f, (64<<20)+1), deadline)
	closeErr := f.Close()
	if hashErr != nil || closeErr != nil || n < 1 || n > 64<<20 || "sha256:"+hex.EncodeToString(h.Sum(nil)) != q.ExecutorDigest {
		return r, ErrConflict
	}
	if q.Recovery != nil {
		if q.Binding != nil || q.Recovery.Operation != "health" || !reflect.DeepEqual(q.Recovery.Guest, q.Guest) {
			return r, ErrInvalid
		}
		original, e := json.Marshal(q.Recovery)
		if e != nil {
			return r, e
		}
		if _, e = DecodeGuestRecoveryRequest(original); e != nil {
			return r, e
		}
		recovery, e := guestRecoveryWithExecutor(*q.Recovery, q.ExecutorDigest, true)
		r.Recovery, r.Guest = &recovery, recovery.Guest
		return r, e
	}
	// A new verifier may observe original completed cleanup only. It cannot
	// launch, collect or change any original guest filesystem-worker state.
	if q.ExecutorDigest != q.Guest.RunnerDigest && (q.Guest.Operation != "health" || q.Binding != nil) {
		return r, ErrConflict
	}
	if q.Binding != nil {
		if err := ValidateGuestTransportBinding(*q.Binding, q.Guest); err != nil {
			return r, err
		}
		observed, err := transportRunner(q.Guest, q.Binding.Root)
		if err != nil || observed != q.Binding.Runner {
			return r, ErrConflict
		}
	}
	r.Guest, err = Guest(q.Guest)
	if err != nil {
		return r, err
	}
	if q.ExecutorDigest != q.Guest.RunnerDigest {
		c := r.Guest.Control
		if !r.Guest.Reusable || c == nil || c.Phase != "cleanup" || c.Error != "" || !c.Complete || !c.Stopped || c.NativeRequest != nil && !reflect.DeepEqual(*c.NativeRequest, q.Guest.Request) || c.NativeRequest == nil && (q.Guest.Request.LogInode != 0 || !c.Retired) {
			return r, ErrUnproven
		}
	}
	if q.Guest.Operation == "prepare" {
		if r.Guest.Root == nil || r.Guest.Request == nil || r.Guest.Control == nil || !r.Guest.Control.Complete || !r.Guest.Control.Stopped || r.Guest.Control.Error != "" {
			return r, ErrUnproven
		}
		owner := q.Guest
		owner.Request = *r.Guest.Request
		owner.Files, owner.Inventory, owner.Challenge = nil, nil, ""
		owner.Root = r.Guest.Root
		file, e := transportRunner(owner, *r.Guest.Root)
		if e != nil {
			return r, e
		}
		r.Binding = &GuestTransportBinding{Schema: "haepari.guest-transport-binding/v1", Guest: owner, Root: *r.Guest.Root, Runner: file}
	} else {
		r.Binding = q.Binding
	}
	return r, nil
}

func DecodeGuestTransportReply(raw []byte) (GuestTransportReply, error) {
	var r GuestTransportReply
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if managed.ValidateUniqueJSONKeys(raw) != nil || d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return r, ErrInvalid
	}
	canonical, err := json.Marshal(r)
	if err != nil || !canonicalKeys(raw, canonical) || r.Schema != "haepari.guest-transport-reply/v1" {
		return r, ErrInvalid
	}
	guest, err := json.Marshal(r.Guest)
	if err != nil {
		return r, err
	}
	_, err = DecodeGuestReply(guest)
	if err == nil && r.Recovery != nil {
		raw, e := json.Marshal(r.Recovery)
		if e != nil {
			return r, e
		}
		_, err = DecodeGuestRecoveryReply(raw)
		if r.Binding != nil || !reflect.DeepEqual(r.Recovery.Guest, r.Guest) {
			return r, ErrInvalid
		}
	}
	return r, err
}

func GuestTransportCommand(in io.Reader, out, stderr io.Writer) int {
	raw, err := io.ReadAll(io.LimitReader(in, GuestWireLimit+1))
	var q GuestTransportRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err != nil || len(raw) > GuestWireLimit || managed.ValidateUniqueJSONKeys(raw) != nil || d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF {
		return 2
	}
	canonical, _ := json.Marshal(q)
	if !canonicalKeys(raw, canonical) {
		return 2
	}
	r, err := GuestTransport(q)
	var encoded []byte
	if err == nil {
		if q.Guest.Schema == "haepari.guest-request/v2" && q.Guest.Operation == "file" && q.Recovery == nil {
			encoded, err = marshalGuestFileReply("runner.guest-transport", &r, &r.Guest)
		} else {
			encoded, _ = json.Marshal(r)
		}
	}
	e := managed.Envelope{Schema: "haepari.envelope/v1", Operation: "runner.guest-transport", OK: err == nil}
	code := 0
	if err != nil {
		e.Error = runnerError(err)
		e.Error.Message = DiagnosticCause(err)
		code, _ = managed.ExitCode(e.Error.Code)
		_, _ = io.WriteString(stderr, DiagnosticCause(err)+"\n")
	} else {
		e.Data = encoded
	}
	if json.NewEncoder(out).Encode(e) != nil {
		return 6
	}
	return code
}
