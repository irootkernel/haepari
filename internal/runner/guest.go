package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/snapshot"
	"golang.org/x/sys/unix"
)

const GuestWireLimit = 96 << 20

var guestUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// GuestRequest is stdin data, never a remote shell command. Prepare supplies
// the sealed inventory and bytes; later operations carry the returned request.
type GuestRequest struct {
	Schema        string                `json:"schema"`
	Operation     string                `json:"operation"`
	VMUUID        string                `json:"vm_uuid"`
	MAC           string                `json:"mac"`
	Challenge     string                `json:"challenge"`
	BootID        string                `json:"boot_id"`
	JobRoot       string                `json:"job_root"`
	RunnerDigest  string                `json:"runner_digest"`
	Request       Request               `json:"request"`
	ObjectID      string                `json:"object_id"`
	InputDigest   string                `json:"input_digest"`
	ProfileDigest string                `json:"profile_digest"`
	Deadline      time.Time             `json:"deadline"`
	Inventory     []byte                `json:"inventory"`
	Files         [][]byte              `json:"files"`
	Offset        int64                 `json:"offset"`
	Area          string                `json:"area"`
	ExpectedFile  *GuestFile            `json:"expected_file,omitempty"`
	Path          string                `json:"path"`
	Root          *managed.RetainedPath `json:"root,omitempty"`
}

type GuestReply struct {
	Schema          string                `json:"schema"`
	ProtocolDigest  string                `json:"protocol_digest"`
	VMUUID          string                `json:"vm_uuid"`
	MAC             string                `json:"mac"`
	Challenge       string                `json:"challenge"`
	BootID          string                `json:"boot_id"`
	OS              string                `json:"os"`
	Arch            string                `json:"arch"`
	Request         *Request              `json:"request,omitempty"`
	Observation     *Observation          `json:"observation,omitempty"`
	Output          []byte                `json:"output,omitempty"`
	NextOffset      int64                 `json:"next_offset"`
	Root            *managed.RetainedPath `json:"root,omitempty"`
	File            *GuestFile            `json:"file,omitempty"`
	Control         *GuestControl         `json:"control,omitempty"`
	SupervisorAlive *bool                 `json:"supervisor_alive,omitempty"`
	OwnersLive      *bool                 `json:"owners_live,omitempty"`
	Reusable        bool                  `json:"reusable"`
}

func guestIdentity(q GuestRequest) (GuestReply, error) {
	mac, err := net.ParseMAC(q.MAC)
	if err != nil || len(mac) != 6 || !guestUUID.MatchString(q.VMUUID) || q.Challenge == "" || len(q.Challenge) > 256 || q.Schema != "haepari.guest-request/v1" {
		return GuestReply{}, ErrInvalid
	}
	// UUID is the host's registered alias. The pinned SSH key plus this actual
	// guest MAC and boot observation establishes the mapping, not an echoed UUID.
	interfaces, err := net.Interfaces()
	if err != nil {
		return GuestReply{}, err
	}
	found := false
	for _, iface := range interfaces {
		if bytes.Equal(iface.HardwareAddr, mac) {
			found = true
		}
	}
	boot, err := platform.BootIdentity()
	if err != nil || !found || runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return GuestReply{}, ErrConflict
	}
	if q.BootID != "" && q.BootID != boot {
		return GuestReply{}, ErrConflict
	}
	return GuestReply{Schema: "haepari.guest-reply/v1", ProtocolDigest: GuestProtocolDigest(q), VMUUID: q.VMUUID, MAC: mac.String(), Challenge: q.Challenge, BootID: boot, OS: runtime.GOOS, Arch: runtime.GOARCH}, nil
}

func GuestProtocolDigest(q GuestRequest) string {
	data, _ := json.Marshal(q)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func DecodeGuestReply(data []byte) (GuestReply, error) {
	var reply GuestReply
	if len(data) > 1<<20 || managed.ValidateUniqueJSONKeys(data) != nil {
		return reply, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&reply) != nil || d.Decode(new(any)) != io.EOF {
		return reply, ErrInvalid
	}
	canonical, _ := json.Marshal(reply)
	if !canonicalKeys(data, canonical) {
		return reply, ErrInvalid
	}
	return reply, nil
}

// GuestCommand is the transferred executable's fixed structured entrypoint.
func GuestCommand(stdin io.Reader, stdout, stderr io.Writer) int {
	data, err := io.ReadAll(io.LimitReader(stdin, GuestWireLimit+1))
	var q GuestRequest
	if err != nil || len(data) > GuestWireLimit || managed.ValidateUniqueJSONKeys(data) != nil {
		return 2
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF {
		return 2
	}
	canonical, _ := json.Marshal(q)
	if !canonicalKeys(data, canonical) {
		return 2
	}
	reply, err := Guest(q)
	response := managed.Envelope{Schema: "haepari.envelope/v1", Operation: "runner.guest", OK: err == nil}
	if err != nil {
		response.Error = runnerError(err)
		_, _ = io.WriteString(stderr, err.Error()+"\n")
	} else {
		response.Data, _ = json.Marshal(reply)
	}
	if json.NewEncoder(stdout).Encode(response) != nil {
		return 6
	}
	if err != nil {
		code, _ := managed.ExitCode(response.Error.Code)
		return code
	}
	return 0
}

func guestEffect(q GuestRequest, control *guestControlFile) (GuestReply, error) {
	if q.Deadline.IsZero() || !time.Now().Before(q.Deadline) {
		return GuestReply{}, ErrInvalid
	}
	reply, err := guestIdentity(q)
	if err != nil {
		return reply, err
	}
	if q.Operation == "identity" {
		return reply, nil
	}
	if q.Operation != "prepare" && q.Operation != "start" && q.Operation != "inspect" && q.Operation != "watch" && q.Operation != "cancel" && q.Operation != "output" && q.Operation != "root" && q.Operation != "file" && q.Operation != "cleanup" {
		return reply, ErrInvalid
	}
	if q.BootID == "" || q.Request.BootID != reply.BootID || !managed.ValidRequestKey(q.Request.Identity.JobID) || q.Request.Identity.Nonce == "" || q.Offset < 0 {
		return reply, ErrInvalid
	}
	if !guestDigest(q.InputDigest) || !guestDigest(q.ProfileDigest) || !guestDigest(q.ObjectID) {
		return reply, ErrInvalid
	}
	parent, err := openDirectory(q.JobRoot)
	if err != nil {
		return reply, err
	}
	defer parent.close()
	root := filepath.Join(q.JobRoot, q.Request.Identity.JobID)
	validationRequest := q.Request
	if validationRequest.LogDevice == 0 && validationRequest.LogInode == 0 {
		validationRequest.LogInode = 1
	}
	if validationRequest.Validate(root) != nil {
		return reply, ErrInvalid
	}
	if q.Operation == "prepare" {
		request, err := prepareGuest(parent, root, q, control)
		if err != nil {
			return reply, err
		}
		reply.Request = &request
		return reply, nil
	}
	d, request, err := readRequest(filepath.Join(root, "request.json"))
	if err != nil {
		return reply, err
	}
	defer d.close()
	var owner GuestRequest
	if d.read("guest.json", &owner) != nil || owner.VMUUID != q.VMUUID || owner.MAC != q.MAC || owner.BootID != q.BootID || owner.RunnerDigest != q.RunnerDigest || owner.JobRoot != q.JobRoot || owner.ObjectID != q.ObjectID || owner.InputDigest != q.InputDigest || owner.ProfileDigest != q.ProfileDigest {
		return reply, ErrConflict
	}
	expected := q.Request
	if expected.LogDevice == 0 && expected.LogInode == 0 && reflect.DeepEqual(expected, owner.Request) {
		expected.LogDevice, expected.LogInode = request.LogDevice, request.LogInode
	}
	if !reflect.DeepEqual(request, expected) {
		return reply, ErrConflict
	}
	path := filepath.Join(root, "request.json")
	if q.Operation == "start" {
		err = launchGuest(d, root, request, q.RunnerDigest, q.Deadline)
	}
	if q.Operation == "cancel" {
		err = Cancel(path)
	}
	if err != nil {
		return reply, err
	}
	view, err := inspect(d, request)
	if err != nil {
		return reply, err
	}
	if view.Intent == nil {
		var claim guestLaunch
		if err := d.read("launch.json", &claim); err == nil {
			if claim.Binding != request.Binding() || claim.RunnerDigest != q.RunnerDigest {
				return reply, ErrConflict
			}
			view.State = "launching"
		} else if !errors.Is(err, unix.ENOENT) {
			return reply, err
		}
	}
	if q.Operation == "watch" || q.Operation == "cancel" {
		budget := 2 * time.Second
		if q.Operation == "cancel" {
			budget = min(30*time.Second, time.Duration(request.TerminationSeconds)*time.Second+time.Second)
		}
		until := minTime(time.Now().Add(budget), q.Deadline)
		for view.Exit == nil && time.Now().Before(until) {
			time.Sleep(25 * time.Millisecond)
			view, err = inspect(d, request)
			if err != nil {
				return reply, err
			}
		}
		if q.Operation == "cancel" && (view.Exit == nil || !view.Exit.TerminationProven) {
			return reply, ErrUnproven
		}
	}
	reply.Request, reply.Observation = &request, &view
	guestNativeOwners(&reply, view)
	if q.Operation == "root" || q.Operation == "file" || q.Operation == "cleanup" {
		return guestFiles(parent, d, q, reply, control)
	}
	if q.Operation == "output" {
		f, err := d.open("output.log", unix.O_RDONLY)
		if err != nil {
			return reply, err
		}
		defer f.Close()
		var st unix.Stat_t
		if unix.Fstat(int(f.Fd()), &st) != nil || uint64(st.Dev) != request.LogDevice || uint64(st.Ino) != request.LogInode || q.Offset > st.Size {
			return reply, ErrConflict
		}
		data := make([]byte, 64<<10)
		n, e := f.ReadAt(data, q.Offset)
		if e != nil && e != io.EOF {
			return reply, e
		}
		reply.Output = data[:n]
		reply.NextOffset = q.Offset + int64(n)
	}
	return reply, nil
}

func prepareGuest(parent *directory, root string, q GuestRequest, control *guestControlFile) (Request, error) {
	manifest, err := snapshot.DecodeInventory(q.Inventory, q.ObjectID)
	if err != nil || manifest.TreeSHA256 != q.InputDigest || len(manifest.Entries) != len(q.Files) || q.Request.LogDevice != 0 || q.Request.LogInode != 0 {
		return Request{}, ErrInvalid
	}
	probe := q.Request
	probe.LogInode = 1
	if probe.Validate(root) != nil {
		return Request{}, ErrInvalid
	}
	for i, entry := range manifest.Entries {
		sum := sha256.Sum256(q.Files[i])
		if int64(len(q.Files[i])) != entry.Size || "sha256:"+hex.EncodeToString(sum[:]) != entry.SHA256 {
			return Request{}, ErrConflict
		}
	}
	self, err := os.Executable()
	if err != nil {
		return Request{}, err
	}
	binary, err := os.ReadFile(self)
	if err != nil || len(binary) > 64<<20 {
		return Request{}, ErrInvalid
	}
	sum := sha256.Sum256(binary)
	if "sha256:"+hex.EncodeToString(sum[:]) != q.RunnerDigest {
		return Request{}, ErrConflict
	}
	// Exclusive creation is the preparation claim. Partial preparation retains
	// its files and cannot be retried or adopted without TASK-015 recovery.
	owner := q
	owner.Files, owner.Challenge = nil, ""
	if !time.Now().Before(q.Deadline) {
		return Request{}, ErrInvalid
	}
	if err := unix.Mkdirat(int(parent.file.Fd()), q.Request.Identity.JobID, 0700); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return Request{}, err
		}
		d, err := openDirectory(root)
		if err != nil {
			return Request{}, err
		}
		defer d.close()
		var prior GuestRequest
		if d.read("guest.json", &prior) != nil || !reflect.DeepEqual(prior, owner) {
			return Request{}, ErrConflict
		}
		requestDir, request, err := readRequest(filepath.Join(root, "request.json"))
		if err != nil {
			return Request{}, ErrAmbiguous
		}
		requestDir.close()
		expected := q.Request
		expected.LogDevice, expected.LogInode = request.LogDevice, request.LogInode
		if !reflect.DeepEqual(expected, request) {
			return Request{}, ErrConflict
		}
		// A complete durable preparation may be observed again. Never fill in
		// partial files or replace an existing request after transport loss.
		return request, nil
	}
	if err := parent.file.Sync(); err != nil {
		return Request{}, err
	}
	d, err := openDirectory(root)
	if err != nil {
		return Request{}, err
	}
	defer d.close()
	if err := control.bindRoot(d); err != nil {
		return Request{}, err
	}
	if err := d.write("guest.json", owner); err != nil {
		return Request{}, err
	}
	if err := unix.Mkdirat(int(d.file.Fd()), "workspace", 0700); err != nil {
		return Request{}, err
	}
	workspace, err := openDirectory(filepath.Join(root, "workspace"))
	if err != nil {
		return Request{}, err
	}
	defer workspace.close()
	for i, entry := range manifest.Entries {
		if err := guestPayload(workspace, entry.Path, q.Files[i], entry.Executable); err != nil {
			return Request{}, err
		}
		if !time.Now().Before(q.Deadline) {
			return Request{}, ErrInvalid
		}
	}
	if err := guestPayload(d, "haepari", binary, true); err != nil {
		return Request{}, err
	}
	log, err := d.open("output.log", unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY)
	if err != nil {
		return Request{}, err
	}
	var st unix.Stat_t
	err = unix.Fstat(int(log.Fd()), &st)
	err = errors.Join(err, log.Sync(), log.Close())
	if err != nil {
		return Request{}, err
	}
	q.Request.LogDevice, q.Request.LogInode = uint64(st.Dev), uint64(st.Ino)
	if err := d.write("request.json", q.Request); err != nil {
		return Request{}, err
	}
	return q.Request, d.file.Sync()
}

func guestPayload(root *directory, name string, data []byte, executable bool) error {
	parts := strings.Split(name, "/")
	fd, err := unix.Dup(int(root.file.Fd()))
	if err != nil {
		return err
	}
	defer func() { unix.Close(fd) }()
	for _, part := range parts[:len(parts)-1] {
		if err := unix.Mkdirat(fd, part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		if err := unix.Fsync(fd); err != nil {
			return err
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		unix.Close(fd)
		fd = next
		if err := privateFile(fd, true); err != nil {
			return err
		}
	}
	mode := uint32(0600)
	if executable {
		mode = 0700
	}
	file, err := unix.Openat(fd, parts[len(parts)-1], unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(file), name)
	_, e := f.Write(data)
	e = errors.Join(e, f.Sync(), f.Close())
	if e != nil {
		return e
	}
	return unix.Fsync(fd)
}

func launchGuest(d *directory, root string, q Request, digest string, deadline time.Time) error {
	deadline = minTime(deadline, q.Deadline).UTC()
	lock, err := d.open("launch.lock", unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return ErrAmbiguous
	}
	var claim guestLaunch
	if err := d.read("launch.json", &claim); err == nil {
		if claim.Binding != q.Binding() || claim.RunnerDigest != digest || claim.Deadline != nil && !claim.Deadline.Equal(deadline) {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	f, err := d.open("haepari", unix.O_RDONLY)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(h, io.LimitReader(f, 64<<20+1))
	f.Close()
	if err != nil || "sha256:"+hex.EncodeToString(h.Sum(nil)) != digest {
		return ErrConflict
	}
	claim.Binding, claim.RunnerDigest, claim.Deadline = q.Binding(), digest, &deadline
	if !time.Now().Before(deadline) {
		return ErrInvalid
	}
	if err := d.write("launch.json", claim); err != nil {
		return err
	}
	cmd := exec.Command(filepath.Join(root, "haepari"), "runner", "start", "--request", filepath.Join(root, "request.json"))
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	if !time.Now().Before(deadline) {
		return ErrAmbiguous
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// The supervisor owns its durable claim and output descriptors. Reap when
	// this short-lived protocol process survives; exit never kills the child.
	go func() { _ = cmd.Wait() }()
	return nil
}

func guestDigest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	data, err := hex.DecodeString(s[7:])
	return err == nil && len(data) == 32 && s == strings.ToLower(s)
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

type guestLaunch struct {
	Binding      Binding    `json:"binding"`
	RunnerDigest string     `json:"runner_digest"`
	Deadline     *time.Time `json:"deadline,omitempty"`
}

// ValidateGuestObservation verifies receipt bindings at the host without
// interpreting guest PIDs as host PIDs. Native liveness remains guest-owned.
func ValidateGuestObservation(view Observation, q Request) error {
	binding := q.Binding()
	state := "prepared"
	uid := uint32(0)
	if view.Intent != nil {
		uid = view.Intent.Supervisor.UID
	}
	validProcess := func(p platform.ProcessIdentity) bool {
		return p.BootID == q.BootID && p.PID > 0 && p.PGID == p.PID && p.SID == p.PID && p.StartedSeconds > 0 && p.StartedMicros >= 0 && p.StartedMicros < 1000000 && p.UID == uid
	}
	if view.Intent != nil {
		i := view.Intent
		if i.Schema != managed.RunnerSchema("intent") || i.Binding != binding || i.RecordedAt.IsZero() || !validProcess(i.Supervisor) {
			return ErrConflict
		}
		state = "spawning"
	}
	if view.Start != nil {
		s := view.Start
		if view.Intent == nil || s.Schema != managed.RunnerSchema("start") || s.Binding != binding || s.StartedAt.IsZero() || !validProcess(s.Process) {
			return ErrConflict
		}
		state = "running"
	}
	if view.Outcome != nil {
		o := view.Outcome
		if view.Intent == nil || o.Schema != managed.RunnerSchema("outcome") || o.Binding != binding || o.ObservedAt.IsZero() || !validOutcome(o.Outcome) || o.Outcome.Started && view.Start == nil {
			return ErrConflict
		}
		state = "terminating"
	}
	if view.Exit != nil {
		e := view.Exit
		if view.Outcome == nil || e.Schema != managed.RunnerSchema("exit") || e.Binding != binding || e.FinishedAt.IsZero() || !reflect.DeepEqual(e.Outcome, view.Outcome.Outcome) || !e.TerminationProven && e.Reason == "" || e.Log != nil && !validLogManifest(*e.Log, q) {
			return ErrConflict
		}
		state = "exited"
	}
	if state == "prepared" && view.State == "launching" {
		return nil
	}
	if view.State != state {
		return ErrConflict
	}
	return nil
}

// These are fresh guest kernel observations, never host PID probes. Missing
// census evidence remains unknown; immutable receipts alone are not liveness.
func guestNativeOwners(reply *GuestReply, view Observation) {
	supervisor, owners := false, false
	if view.Exit != nil && view.Exit.TerminationProven {
		reply.SupervisorAlive = &supervisor
		reply.OwnersLive = &owners
		return
	}
	if view.Intent == nil {
		if view.State == "prepared" {
			reply.SupervisorAlive = &supervisor
			reply.OwnersLive = &owners
		}
		return
	}
	processes, err := platform.ProcessCensus()
	if err != nil {
		return
	}
	for _, p := range processes {
		if p.Zombie {
			continue
		}
		if p.Identity == view.Intent.Supervisor {
			supervisor = true
			owners = true
		}
		if p.Identity.BootID == view.Intent.Supervisor.BootID && (p.Identity.PGID == view.Intent.Supervisor.PGID || p.Identity.SID == view.Intent.Supervisor.SID) {
			owners = true
		}
		if view.Start != nil && p.Identity.BootID == view.Start.Process.BootID && (p.Identity == view.Start.Process || p.Identity.PGID == view.Start.Process.PGID || p.Identity.SID == view.Start.Process.SID) {
			owners = true
		}
	}
	reply.SupervisorAlive = &supervisor
	reply.OwnersLive = &owners
}
