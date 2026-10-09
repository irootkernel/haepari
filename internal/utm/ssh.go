package utm

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"golang.org/x/sys/unix"
)

type SSHMapping struct {
	Schema       string `json:"schema"`
	User         string `json:"user"`
	Port         int    `json:"port"`
	IdentityFile string `json:"identity_file"`
}

type SSHRunner func(context.Context, string, []string, []byte) (CommandResult, error)

var ErrGuestRefused = errors.New("guest completed the operation with a structured refusal")

var ErrGuestUncertain = errors.New("guest operation may have taken effect; preserve the exact run and reconcile")

// SSH transfers a pinned artifact into a transient bootstrap and then into
// each owned Job. It never installs a service or enables public VM dispatch.
type SSH struct {
	Executable   string
	Control      Control
	Run          SSHRunner
	Runner       []byte
	RunnerDigest string
}

var userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,63}$`)

func readMapping(reference string) (SSHMapping, error) {
	var mapping SSHMapping
	parts := strings.SplitN(reference, ":", 3)
	if len(parts) != 3 || parts[0] != "sha256" || len(parts[1]) != 64 {
		return mapping, ErrUnavailable
	}
	name := parts[2]
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return mapping, ErrUnavailable
	}
	f, err := os.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return mapping, ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return mapping, ErrUnavailable
	}
	var mappingStat unix.Stat_t
	if unix.Fstat(int(f.Fd()), &mappingStat) != nil || mappingStat.Uid != uint32(os.Geteuid()) || mappingStat.Nlink != 1 {
		return mapping, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	sum := sha256.Sum256(data)
	if err != nil || len(data) > 16384 || hex.EncodeToString(sum[:]) != parts[1] || managed.ValidateUniqueJSONKeys(data) != nil {
		return mapping, ErrUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&mapping) != nil || d.Decode(new(any)) != io.EOF {
		return mapping, ErrUnavailable
	}
	canonical, _ := json.Marshal(mapping)
	if !bytes.Equal(canonical, data) {
		return mapping, ErrUnavailable
	}
	if mapping.Schema != "haepari.ssh-mapping/v1" || !userPattern.MatchString(mapping.User) || mapping.Port < 1 || mapping.Port > 65535 || !filepath.IsAbs(mapping.IdentityFile) || filepath.Clean(mapping.IdentityFile) != mapping.IdentityFile || strings.ContainsAny(mapping.IdentityFile, "\x00\r\n%") {
		return mapping, ErrUnavailable
	}
	var st unix.Stat_t
	key, err := unix.Open(mapping.IdentityFile, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return mapping, ErrUnavailable
	}
	defer unix.Close(key)
	if unix.Fstat(key, &st) != nil || uint32(st.Mode)&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return mapping, ErrUnavailable
	}
	return mapping, nil
}

// The remote command is constant. All variable values are stdin data. The
// bootstrap creates only a unique private temporary directory, verifies the
// runner digest, and invokes its structured entrypoint; no project shell text
// is evaluated here. A transport loss may leave this temporary directory for
// diagnosis, never authorize a retry of a guest effect.
const guestBootstrap = `/bin/sh -c 'set -eu
umask 077
scratch=$(/usr/bin/mktemp -d /private/tmp/haepari-ssh.XXXXXXXX)
trap '\''/bin/rm -f "$scratch/haepari" "$scratch/request.json" "$scratch/digest"; /bin/rmdir "$scratch"'\'' EXIT
/usr/bin/tar -xf - -C "$scratch" haepari request.json digest
digest=$(/bin/cat "$scratch/digest")
case "$digest" in *[!0-9a-f]*|"") exit 2;; esac
test "${#digest}" -eq 64
actual=$(/usr/bin/shasum -a 256 "$scratch/haepari")
test "${actual%% *}" = "$digest"
/bin/chmod 700 "$scratch/haepari"
"$scratch/haepari" runner guest --stdio < "$scratch/request.json"
'`

func (s SSH) exchangeWire(ctx context.Context, spec managed.ResourceSpec, lease Lease, q any, recovery bool) ([]byte, error) {
	var reply []byte
	if !filepath.IsAbs(s.Executable) || filepath.Clean(s.Executable) != s.Executable || len(s.Runner) == 0 || len(s.Runner) > 64<<20 {
		return reply, ErrUnavailable
	}
	sum := sha256.Sum256(s.Runner)
	if "sha256:"+hex.EncodeToString(sum[:]) != s.RunnerDigest {
		return reply, ErrUnavailable
	}
	key, err := ReadPin(spec.Guest.KnownHostsReference, spec.VM.UTMVMUUID)
	if err != nil {
		return reply, err
	}
	mapping, err := readMapping(spec.Guest.ConnectionReference)
	if err != nil {
		return reply, err
	}
	// Snapshot the verified key so OpenSSH cannot read changed pin-file bytes.
	pinDirectory, err := os.MkdirTemp("/private/tmp", "haepari-pin-*")
	if err != nil {
		return reply, err
	}
	defer os.Remove(pinDirectory)
	pin, err := os.CreateTemp(pinDirectory, "known_hosts-*")
	if err != nil {
		return reply, err
	}
	defer os.Remove(pin.Name())
	_, err = io.WriteString(pin, HostAlias(spec.VM.UTMVMUUID)+" "+key+"\n")
	err = errors.Join(err, pin.Close())
	if err != nil {
		return reply, err
	}
	bootstrap := guestBootstrap
	if recovery {
		bootstrap = guestRecoveryBootstrap
	}
	args := []string{"-F", "/dev/null", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + pin.Name(), "-o", "GlobalKnownHostsFile=/dev/null", "-o", "HostKeyAlias=" + HostAlias(spec.VM.UTMVMUUID), "-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "UpdateHostKeys=no", "-o", "VerifyHostKeyDNS=no", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "IdentityAgent=none", "-o", "IdentitiesOnly=yes", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "NumberOfPasswordPrompts=0", "-o", "ConnectTimeout=10", "-o", "ConnectionAttempts=1", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=2", "-i", mapping.IdentityFile, "-p", strconv.Itoa(mapping.Port), "-l", mapping.User, "--", lease.Address.String(), bootstrap}
	data, err := json.Marshal(q)
	if err != nil || len(data) > runner.GuestWireLimit {
		return reply, ErrUnavailable
	}
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	for _, item := range []struct {
		name string
		data []byte
	}{{"haepari", s.Runner}, {"request.json", data}, {"digest", []byte(hex.EncodeToString(sum[:]))}} {
		if err := w.WriteHeader(&tar.Header{Name: item.name, Mode: 0600, Size: int64(len(item.data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			return reply, err
		}
		if _, err := w.Write(item.data); err != nil {
			return reply, err
		}
	}
	if err := w.Close(); err != nil {
		return reply, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	run := s.Run
	if run == nil {
		run = runSSH
	}
	r, err := run(ctx, s.Executable, args, archive.Bytes())
	if len(r.Stdout) > commandLimit || len(r.Stderr) > commandLimit {
		return reply, ErrUnavailable
	}
	if err != nil || r.ExitCode != 0 {
		// A structured guest refusal is a completed observation even when SSH
		// reports the guest's nonzero exit. Transport stderr is never evidence.
		envelope, decodeErr := managed.DecodeEnvelope([]byte(r.Stdout))
		operation := "runner.guest"
		if recovery {
			operation = "runner.guest-recovery"
		}
		if decodeErr != nil || envelope.OK || envelope.Operation != operation {
			return reply, ErrUnavailable
		}
	}
	return []byte(r.Stdout), nil
}

var guestRecoveryBootstrap = strings.Replace(guestBootstrap, "runner guest --stdio", "runner guest-recovery --stdio", 1)

func (s SSH) exchange(ctx context.Context, spec managed.ResourceSpec, lease Lease, q runner.GuestRequest) (runner.GuestReply, error) {
	var reply runner.GuestReply
	raw, err := s.exchangeWire(ctx, spec, lease, q, false)
	if err != nil {
		return reply, err
	}
	envelope, err := managed.DecodeEnvelope(raw)
	if err != nil || envelope.Operation != "runner.guest" {
		return reply, ErrUnavailable
	}
	if !envelope.OK {
		return reply, guestRefusalError(envelope.Error, q.Schema == "haepari.guest-request/v2")
	}
	reply, err = runner.DecodeGuestReply(envelope.Data)
	if err != nil || reply.ProtocolDigest != runner.GuestProtocolDigest(q) || reply.Schema != runner.GuestReplySchema(q) || reply.VMUUID != q.VMUUID || reply.MAC != q.MAC || reply.Challenge != q.Challenge || !uuidPattern.MatchString(reply.BootID) || reply.OS != "darwin" || reply.Arch != "arm64" || q.BootID != "" && reply.BootID != q.BootID {
		return runner.GuestReply{}, ErrUnavailable
	}
	return reply, nil
}

// guestRefusalError accepts only the error from a decoded, operation-matched
// negative envelope. A classification never establishes effect cessation.
func guestRefusalError(failure *managed.MachineError, classified bool) error {
	if failure != nil {
		if classified {
			if cause := runner.DiagnosticError(failure.Message); cause != nil {
				return errors.Join(ErrGuestRefused, cause)
			}
		}
		switch failure.Code {
		case managed.InvalidRequest:
			return errors.Join(ErrGuestRefused, runner.ErrInvalid)
		case managed.Conflict:
			return errors.Join(ErrGuestRefused, runner.ErrConflict)
		case managed.RecoveryBlocked:
			return errors.Join(ErrGuestRefused, runner.ErrUnproven)
		}
	}
	return errors.Join(ErrGuestRefused, ErrUnavailable)
}

// Call first proves the current started VM and individual lease over pinned
// SSH, then issues one operation. An error after issuance is uncertainty, not
// evidence of no guest effects. Callers never repeat start on transport loss.
func (s SSH) Call(ctx context.Context, spec managed.ResourceSpec, leases io.Reader, q runner.GuestRequest) (runner.GuestReply, error) {
	if q.Deadline.IsZero() || !time.Now().Before(q.Deadline) {
		return runner.GuestReply{}, ErrUnavailable
	}
	ctx, cancel := context.WithDeadline(ctx, q.Deadline)
	defer cancel()
	raw, encodeErr := json.Marshal(spec)
	_, decodeErr := managed.DecodeResource(raw)
	if spec.Kind != "utm-macos" || spec.VM == nil || spec.Guest == nil || encodeErr != nil || decodeErr != nil {
		return runner.GuestReply{}, ErrUnavailable
	}
	mac, err := normalizeMAC(spec.VM.MACAddress)
	if err != nil {
		return runner.GuestReply{}, err
	}
	if q.VMUUID != strings.ToLower(spec.VM.UTMVMUUID) || q.MAC != mac || q.JobRoot != spec.VM.GuestJobRoot || q.RunnerDigest != s.RunnerDigest || q.Request.Identity.ResourceID != spec.ID || q.Request.Identity.ConfigRevision != spec.Revision {
		return runner.GuestReply{}, ErrUnavailable
	}
	pin, err := ReadPin(spec.Guest.KnownHostsReference, spec.VM.UTMVMUUID)
	if err != nil {
		return runner.GuestReply{}, err
	}
	var challenge [16]byte
	if _, err := rand.Read(challenge[:]); err != nil {
		return runner.GuestReply{}, err
	}
	proofNonce := hex.EncodeToString(challenge[:])
	var proofErr error
	verify := func(ctx context.Context, lease Lease, uuid, nonce string) (GuestIdentity, error) {
		probe := q
		probe.Operation = "identity"
		probe.Challenge = nonce
		probe.Files = nil
		probe.Inventory = nil
		r, e := s.exchange(ctx, spec, lease, probe)
		proofErr = e
		return GuestIdentity{VMUUID: r.VMUUID, MAC: r.MAC, Address: lease.Address, HostKey: pin, BootID: r.BootID, Nonce: r.Challenge, OS: r.OS, Arch: r.Arch}, e
	}
	lease, identity, err := s.Control.ResolveGuest(ctx, leases, q.VMUUID, mac, pin, proofNonce, time.Now, verify)
	if err != nil {
		// ResolveGuest intentionally collapses invalid identity observations.
		// Preserve only a complete native v2 refusal from its proof exchange.
		if q.Schema == "haepari.guest-request/v2" && errors.Is(proofErr, ErrGuestRefused) {
			return runner.GuestReply{}, proofErr
		}
		return runner.GuestReply{}, err
	}
	if q.BootID != "" && q.BootID != identity.BootID {
		return runner.GuestReply{}, ErrUnavailable
	}
	q.Challenge = proofNonce
	if q.Operation == "identity" {
		q.BootID = identity.BootID
		return s.exchange(ctx, spec, lease, q)
	}
	if q.BootID == "" || q.Request.BootID != identity.BootID || !lease.Expires.After(time.Now()) {
		return runner.GuestReply{}, ErrUnavailable
	}
	reply, err := s.exchange(ctx, spec, lease, q)
	if err != nil {
		return reply, errors.Join(ErrGuestUncertain, err)
	}
	if !lease.Expires.After(time.Now()) {
		return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if q.Operation == "state" || q.Operation == "retire" || q.Operation == "health" || q.Operation == "root" || q.Operation == "file" || q.Operation == "cleanup" {
		if reply.Control == nil || runner.ValidateGuestControl(*reply.Control, q) != nil || !reply.Control.Complete && q.Operation != "state" && !(q.Operation == "health" && reply.Reusable && reply.Control.Stopped) {
			return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
		}
		if reply.Request == nil {
			if q.Operation == "retire" && !reply.Control.Retired {
				return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
			}
			if q.Operation == "state" || q.Operation == "retire" || q.Operation == "cleanup" || q.Operation == "health" && reply.Reusable {
				return reply, nil
			}
			return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
		}
	}
	if reply.Request == nil {
		return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	expected := q.Request
	if q.Operation == "prepare" || expected.LogDevice == 0 && expected.LogInode == 0 {
		expected.LogDevice = reply.Request.LogDevice
		expected.LogInode = reply.Request.LogInode
	}
	if !reflect.DeepEqual(expected, *reply.Request) || reply.Request.LogInode == 0 {
		return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if q.Operation != "prepare" && q.Operation != "state" && q.Operation != "retire" && (reply.Observation == nil || runner.ValidateGuestObservation(*reply.Observation, *reply.Request) != nil) {
		return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if q.Operation == "retire" && !reply.Control.Retired {
		return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if q.Operation == "cancel" && (reply.Observation.Exit == nil || !reply.Observation.Exit.TerminationProven) {
		return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if (q.Operation == "output" || q.Operation == "file") && (len(reply.Output) > 64<<10 || reply.NextOffset != q.Offset+int64(len(reply.Output))) {
		return runner.GuestReply{}, ErrUnavailable
	}
	if reply.Absent {
		if q.Schema != "haepari.guest-request/v2" || q.Operation != "file" || (q.Area != "workspace" && q.Area != "") || q.Offset != 0 || q.ExpectedFile != nil || reply.File != nil || len(reply.Output) != 0 || reply.NextOffset != 0 || reply.Root == nil || reply.Observation == nil || reply.Observation.Exit == nil || !reply.Observation.Exit.TerminationProven {
			return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
		}
		return reply, nil
	}
	if q.Operation == "file" && (reply.File == nil || reply.File.Path != q.Path || reply.File.Inode == 0 || reply.File.Size < reply.NextOffset || reply.File.Size > 1<<30 || len(reply.File.SHA256) != 64) {
		return runner.GuestReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	return reply, nil
}

func runSSH(ctx context.Context, executable string, args []string, input []byte) (CommandResult, error) {
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return CommandResult{}, ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	r := CommandResult{Stdout: string(stdout.bytes), Stderr: string(stderr.bytes), ExitCode: -1}
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	if stdout.overflow || stderr.overflow {
		return r, ErrUnavailable
	}
	return r, err
}
