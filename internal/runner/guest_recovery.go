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
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

// Recovery is a separate protocol: neither its executable nor its operator
// assertion can replace the original runner or manufacture termination proof.
type GuestRecoveryBasis struct {
	Root        managed.RetainedPath `json:"root"`
	Control     managed.RetainedPath `json:"control"`
	Request     Request              `json:"request"`
	Observation Observation          `json:"observation"`
	Digests     map[string]string    `json:"digests"`
}
type GuestAttestation struct {
	Schema         string                   `json:"schema"`
	Guest          GuestRequest             `json:"guest"`
	Basis          GuestRecoveryBasis       `json:"basis"`
	RecoveryDigest string                   `json:"recovery_digest"`
	MutationKey    string                   `json:"mutation_key"`
	Deadline       time.Time                `json:"deadline"`
	Evidence       managed.RecoveryEvidence `json:"evidence"`
}
type GuestRecoveryRequest struct {
	Schema           string                    `json:"schema"`
	Operation        string                    `json:"operation"`
	Guest            GuestRequest              `json:"guest"`
	RecoveryDigest   string                    `json:"recovery_digest"`
	Deadline         time.Time                 `json:"deadline"`
	Basis            *GuestRecoveryBasis       `json:"basis,omitempty"`
	Attestation      *GuestAttestation         `json:"attestation,omitempty"`
	MutationKey      string                    `json:"mutation_key"`
	Evidence         *managed.RecoveryEvidence `json:"evidence,omitempty"`
	CollectionDigest string                    `json:"collection_digest"`
}
type GuestRecoveryReply struct {
	RecoveryDigest string              `json:"recovery_digest"`
	Schema         string              `json:"schema"`
	ProtocolDigest string              `json:"protocol_digest"`
	Guest          GuestReply          `json:"guest"`
	Basis          *GuestRecoveryBasis `json:"basis,omitempty"`
	Attestation    *GuestAttestation   `json:"attestation,omitempty"`
	Reusable       bool                `json:"reusable"`
}
type guestAttestedCleanup struct {
	Schema            string    `json:"schema"`
	AttestationDigest string    `json:"attestation_digest"`
	MutationKey       string    `json:"mutation_key"`
	CollectionDigest  string    `json:"collection_digest"`
	Deadline          time.Time `json:"deadline"`
}

func decodeRecoveryJSON(raw []byte, into any) error {
	if len(raw) > receiptLimit || managed.ValidateUniqueJSONKeys(raw) != nil {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(into) != nil || d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	canonical, err := json.Marshal(into)
	if err != nil || !canonicalKeys(raw, canonical) {
		return ErrInvalid
	}
	return nil
}
func DecodeGuestRecoveryRequest(raw []byte) (GuestRecoveryRequest, error) {
	var q GuestRecoveryRequest
	err := decodeRecoveryJSON(raw, &q)
	return q, err
}

func DecodeGuestRecoveryReply(raw []byte) (GuestRecoveryReply, error) {
	var r GuestRecoveryReply
	err := decodeRecoveryJSON(raw, &r)
	return r, err
}
func GuestRecoveryCommand(in io.Reader, out, stderr io.Writer) int {
	raw, err := io.ReadAll(io.LimitReader(in, receiptLimit+1))
	var q GuestRecoveryRequest
	if err != nil || decodeRecoveryJSON(raw, &q) != nil {
		return 2
	}
	reply, err := GuestRecovery(q)
	envelope := managed.Envelope{Schema: "haepari.envelope/v1", Operation: "runner.guest-recovery", OK: err == nil}
	if err != nil {
		envelope.Error = runnerError(err)
	} else {
		envelope.Data, _ = json.Marshal(reply)
	}
	if json.NewEncoder(out).Encode(envelope) != nil {
		return 6
	}
	if err != nil {
		code, _ := managed.ExitCode(envelope.Error.Code)
		return code
	}
	return 0
}
func ValidateGuestAttestation(a GuestAttestation, guest GuestRequest) error {
	if a.Schema != "haepari.guest-attestation/v1" || !sameGuestOwner(a.Guest, guest) || a.Guest.Challenge != "" || a.Guest.Files != nil || a.Guest.Inventory != nil || a.Guest.Operation != "inspect" ||
		!guestDigest(a.RecoveryDigest) || !managed.ValidRequestKey(a.MutationKey) || a.Deadline.IsZero() || managed.ValidateRecoveryEvidence(a.Evidence) != nil || a.Evidence.Source != "operator-attested" || a.Evidence.Mechanism != "guest-workload-and-descendants-stopped" || a.Evidence.Reference != "job:"+guest.Request.Identity.JobID || a.Evidence.Digest != managed.RecoveryDigest(a.Basis) || a.Evidence.ObservedAt.After(a.Deadline) ||
		a.Basis.Root.Path != "" || !a.Basis.Root.Directory || a.Basis.Root.Inode == 0 || a.Basis.Control.Path != "" || a.Basis.Control.Directory || a.Basis.Control.Inode == 0 || a.Basis.Request.Identity != guest.Request.Identity || a.Basis.Request.BootID != guest.BootID || ValidateGuestObservation(a.Basis.Observation, a.Basis.Request) != nil || a.Basis.Observation.Intent == nil || a.Basis.Observation.Exit == nil || a.Basis.Observation.Exit.TerminationProven {
		return ErrConflict
	}
	for _, n := range []string{"guest.json", "request.json", "intent.json", "start.json", "outcome.json", "exit.json", "haepari"} {
		if !guestDigest(a.Basis.Digests[n]) {
			return ErrConflict
		}
	}
	if len(a.Basis.Digests) != 7 || a.Basis.Digests["haepari"] != guest.RunnerDigest {
		return ErrConflict
	}
	return nil
}
func recoveryGuestOwner(q GuestRequest) GuestRequest {
	q.Operation = "inspect"
	q.Challenge = ""
	q.Files = nil
	q.Inventory = nil
	q.Deadline = time.Time{}
	q.Root = nil
	q.Offset = 0
	q.Area = ""
	q.ExpectedFile = nil
	q.Path = ""
	return q
}
func recoveryOwnersStopped(view Observation, boot string) error {
	if view.Intent == nil || view.Start == nil {
		return ErrUnproven
	}
	ps, err := platform.ProcessCensus()
	if err != nil {
		return err
	}
	seeds := []platform.ProcessIdentity{view.Intent.Supervisor, view.Start.Process}
	// The census rejects any visible original group/session or descendant. An
	// escaped and already reparented child still requires the scoped operator
	// assertion; census absence itself never supplies termination proof.
	live := map[int]bool{}
	for _, p := range ps {
		if p.Zombie {
			continue
		}
		for _, seed := range seeds {
			if p.Identity.BootID == boot && (p.Identity == seed || p.Identity.PGID == seed.PGID || p.Identity.SID == seed.SID) {
				live[p.Identity.PID] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range ps {
			if !p.Zombie && live[p.ParentPID] && !live[p.Identity.PID] {
				live[p.Identity.PID] = true
				changed = true
			}
		}
	}
	if len(live) != 0 {
		return ErrUnproven
	}
	return nil
}
func recoveryFileDigest(d *directory, name string, limit int64, deadline time.Time) (string, error) {
	f, err := d.open(name, unix.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var before, after unix.Stat_t
	if unix.Fstat(int(f.Fd()), &before) != nil || before.Size < 0 || before.Size > limit {
		return "", ErrInvalid
	}
	h := sha256.New()
	n, err := guestCopyUntil(h, io.LimitReader(f, limit+1), deadline)
	if err != nil || n != before.Size || unix.Fstat(int(f.Fd()), &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim {
		return "", ErrConflict
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
func recoveryBasis(d *directory, c *guestControlFile, guest GuestRequest, deadline time.Time) (GuestRecoveryBasis, error) {
	var b GuestRecoveryBasis
	var st unix.Stat_t
	if unix.Fstat(int(c.file.Fd()), &st) != nil {
		return b, ErrConflict
	}
	b.Control = managed.RetainedPath{Device: uint64(st.Dev), Inode: st.Ino}
	if c.value.Root == nil || c.value.NativeRequest == nil {
		return b, ErrUnproven
	}
	b.Root = *c.value.Root
	if unix.Fstat(int(d.file.Fd()), &st) != nil || uint64(st.Dev) != b.Root.Device || st.Ino != b.Root.Inode {
		return b, ErrConflict
	}
	requestDir, request, err := readRequest(filepath.Join(d.file.Name(), "request.json"))
	if err != nil {
		return b, err
	}
	requestDir.close()
	if !reflect.DeepEqual(request, *c.value.NativeRequest) || request.Identity != guest.Request.Identity {
		return b, ErrConflict
	}
	b.Request = request
	b.Observation, err = inspect(d, request)
	if err != nil || ValidateGuestObservation(b.Observation, request) != nil || b.Observation.Exit == nil || b.Observation.Exit.TerminationProven {
		return b, ErrUnproven
	}
	b.Digests = map[string]string{}
	for _, n := range []string{"guest.json", "request.json", "intent.json", "start.json", "outcome.json", "exit.json", "haepari"} {
		limit := int64(receiptLimit)
		if n == "haepari" {
			limit = 64 << 20
		}
		v, e := recoveryFileDigest(d, n, limit, deadline)
		if e != nil {
			return b, e
		}
		b.Digests[n] = v
	}
	var original GuestRequest
	if d.read("guest.json", &original) != nil || !sameGuestOwner(original, guest) || b.Digests["haepari"] != guest.RunnerDigest {
		return b, ErrConflict
	}
	return b, nil
}

func GuestRecovery(q GuestRecoveryRequest) (GuestRecoveryReply, error) {
	r := GuestRecoveryReply{RecoveryDigest: q.RecoveryDigest, Schema: "haepari.guest-recovery-reply/v1", ProtocolDigest: managed.RecoveryDigest(q)}
	if q.Schema != "haepari.guest-recovery-request/v1" || !guestDigest(q.RecoveryDigest) || q.Deadline.IsZero() || !time.Now().Before(q.Deadline) {
		return r, ErrInvalid
	}
	switch q.Operation {
	case "probe", "attest", "state", "file", "cleanup", "health":
	default:
		return r, ErrInvalid
	}
	guest := q.Guest
	guest.Deadline = q.Deadline
	identity, err := guestIdentity(guest)
	if err != nil {
		return r, err
	}
	r.Guest = identity
	if guest.BootID == "" || guest.Request.BootID != identity.BootID || !managed.ValidRequestKey(guest.Request.Identity.JobID) || guest.Files != nil || guest.Inventory != nil {
		return r, ErrConflict
	}
	self, err := os.Executable()
	if err != nil {
		return r, err
	}
	binary, err := os.OpenFile(self, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return r, ErrInvalid
	}
	h := sha256.New()
	n, hashErr := guestCopyUntil(h, io.LimitReader(binary, (64<<20)+1), q.Deadline)
	binary.Close()
	if hashErr != nil || n > 64<<20 {
		return r, ErrUnproven
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != q.RecoveryDigest {
		return r, ErrConflict
	}
	parent, err := openDirectory(guest.JobRoot)
	if err != nil {
		return r, err
	}
	defer parent.close()
	name := ".haepari-control-" + guest.Request.Identity.JobID + ".json"
	f, err := parent.open(name, unix.O_RDWR)
	if err != nil {
		return r, err
	}
	defer f.Close()
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return r, ErrUnproven
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	c := guestControlFile{file: f}
	raw, err := io.ReadAll(io.NewSectionReader(f, 0, receiptLimit+1))
	if err != nil || decodeRecoveryJSON(raw, &c.value) != nil || ValidateGuestControl(c.value, guest) != nil || c.value.Root == nil || !guestOwnerStopped(c.value.Owner) {
		return r, ErrUnproven
	}
	var controlStat, current unix.Stat_t
	if unix.Fstat(int(f.Fd()), &controlStat) != nil || unix.Fstatat(int(parent.file.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || controlStat.Dev != current.Dev || controlStat.Ino != current.Ino {
		return r, ErrConflict
	}
	attestationName := ".haepari-attestation-" + guest.Request.Identity.JobID + ".json"
	cleanupName := ".haepari-attested-cleanup-" + guest.Request.Identity.JobID + ".json"
	var a GuestAttestation
	attestationErr := parent.read(attestationName, &a)
	if attestationErr != nil && !errors.Is(attestationErr, unix.ENOENT) {
		return r, attestationErr
	}
	attested := attestationErr == nil
	if attested && (ValidateGuestAttestation(a, guest) != nil || a.RecoveryDigest != q.RecoveryDigest || a.Basis.Control.Device != uint64(controlStat.Dev) || a.Basis.Control.Inode != controlStat.Ino || a.Basis.Root != *c.value.Root) {
		return r, ErrConflict
	}
	if q.Operation != "probe" && q.Operation != "attest" && (!attested || q.Attestation == nil || !reflect.DeepEqual(a, *q.Attestation) || !c.value.Retired) {
		return r, ErrUnproven
	}
	var cleanup guestAttestedCleanup
	cleanupErr := parent.read(cleanupName, &cleanup)
	if cleanupErr != nil && !errors.Is(cleanupErr, unix.ENOENT) {
		return r, cleanupErr
	}
	cleanupExists := cleanupErr == nil
	if cleanupExists && (!attested || cleanup.Schema != "haepari.guest-attested-cleanup/v1" || cleanup.AttestationDigest != managed.RecoveryDigest(a) || !managed.ValidRequestKey(cleanup.MutationKey) || !guestDigest(cleanup.CollectionDigest) || cleanup.Deadline.IsZero()) {
		return r, ErrConflict
	}
	root := filepath.Join(guest.JobRoot, guest.Request.Identity.JobID)
	d, err := openDirectory(root)
	if errors.Is(err, unix.ENOENT) && q.Operation == "health" && cleanupExists {
		if err = recoveryOwnersStopped(a.Basis.Observation, guest.BootID); err != nil {
			return r, err
		}
		if !time.Now().Before(q.Deadline) {
			return r, ErrUnproven
		}
		boundary("guest_attested_absence_before_directory_sync")
		if !time.Now().Before(q.Deadline) {
			return r, ErrUnproven
		}
		if err = parent.file.Sync(); err != nil {
			return r, err
		}
		if !time.Now().Before(q.Deadline) {
			return r, ErrUnproven
		}
		r.Attestation = &a
		r.Basis = &a.Basis
		r.Reusable = true
		return r, nil
	}
	if err != nil {
		return r, err
	}
	defer d.close()
	var st unix.Stat_t
	if unix.Fstat(int(d.file.Fd()), &st) != nil || st.Ino != c.value.Root.Inode || uint64(st.Dev) != c.value.Root.Device {
		return r, ErrConflict
	}
	lock, err := d.open("run.lock", unix.O_RDWR)
	if errors.Is(err, unix.ENOENT) && cleanupExists && c.value.Retired {
		// An admitted cleanup may already have removed the lock. Its durable
		// outside intent and retirement barrier prevent delayed legacy launch;
		// the held control inode and fresh census still fence recovery workers.
		lock = nil
		err = nil
	}
	if err != nil {
		return r, err
	}
	if lock != nil {
		defer lock.Close()
		if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
			return r, ErrUnproven
		}
		defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	}
	var basis GuestRecoveryBasis
	if cleanupExists {
		basis = a.Basis
	} else {
		basis, err = recoveryBasis(d, &c, guest, q.Deadline)
		if err != nil {
			return r, err
		}
	}
	if err = recoveryOwnersStopped(basis.Observation, guest.BootID); err != nil {
		return r, err
	}
	if !time.Now().Before(q.Deadline) {
		return r, ErrUnproven
	}
	r.Basis = &basis
	if q.Operation == "probe" {
		if attested {
			return r, ErrConflict
		}
		return r, nil
	}
	if q.Operation == "attest" {
		if q.Basis == nil || !reflect.DeepEqual(*q.Basis, basis) || q.Evidence == nil || !managed.ValidRequestKey(q.MutationKey) {
			return r, ErrConflict
		}
		proposed := GuestAttestation{Schema: "haepari.guest-attestation/v1", Guest: recoveryGuestOwner(guest), Basis: basis, RecoveryDigest: q.RecoveryDigest, MutationKey: q.MutationKey, Deadline: q.Deadline, Evidence: *q.Evidence}
		if ValidateGuestAttestation(proposed, guest) != nil || q.Evidence.ObservedAt.After(time.Now()) || q.Evidence.ObservedAt.Before(q.Deadline.Add(-2*time.Minute)) {
			return r, ErrInvalid
		}
		if attested && !reflect.DeepEqual(a, proposed) {
			return r, ErrConflict
		}
		boundary("guest_attestation_authority")
		if !time.Now().Before(q.Deadline) {
			return r, ErrUnproven
		}
		if err = parent.write(attestationName, proposed); err != nil {
			return r, err
		}
		boundary("guest_attestation_published")
		if !time.Now().Before(q.Deadline) {
			return r, ErrUnproven
		}
		c.value.Retired = true
		if err = c.save(); err != nil {
			return r, err
		}
		boundary("guest_attestation_retired")
		r.Attestation = &proposed
		return r, nil
	}
	if !reflect.DeepEqual(basis, a.Basis) {
		return r, ErrConflict
	}
	r.Attestation = &a
	switch q.Operation {
	case "state", "health":
		return r, nil
	case "file":
		if cleanupExists {
			return r, ErrConflict
		}
		if guest.Area == "receipt" {
			if guest.Path != "guest.json" && guest.Path != "request.json" && guest.Path != "intent.json" && guest.Path != "start.json" && guest.Path != "outcome.json" && guest.Path != "exit.json" {
				return r, ErrInvalid
			}
		}
		// This is an explicit attested capability, separate from guestFiles' native
		// proof gate. File integrity and private path handling are shared below.
		r.Guest, err = guestRecoveryFile(d, guest, basis.Request, r.Guest)
		return r, err
	case "cleanup":
		if !managed.ValidRequestKey(q.MutationKey) || !guestDigest(q.CollectionDigest) {
			return r, ErrInvalid
		}
		wanted := guestAttestedCleanup{"haepari.guest-attested-cleanup/v1", managed.RecoveryDigest(a), q.MutationKey, q.CollectionDigest, q.Deadline}
		if cleanupExists && !reflect.DeepEqual(cleanup, wanted) {
			return r, ErrConflict
		}
		boundary("guest_attested_cleanup_authority")
		if !time.Now().Before(q.Deadline) {
			return r, ErrUnproven
		}
		if err = parent.write(cleanupName, wanted); err != nil {
			return r, err
		}
		boundary("guest_attested_cleanup_intent")
		if err = guestRemoveBound(int(parent.file.Fd()), guest.Request.Identity.JobID, q.Deadline, &a.Basis.Root); err != nil {
			return r, err
		}
		boundary("guest_attested_cleanup_deleted")
		r.Reusable = true
		return r, nil
	}
	return r, ErrInvalid
}

// The attested capability shares the exact regular-file integrity checks; it
// never synthesizes an Exit with upgraded proof to pass the ordinary gate.
func guestRecoveryFile(d *directory, q GuestRequest, original Request, reply GuestReply) (GuestReply, error) {
	var f *os.File
	var err error
	if q.Area == "receipt" && qReceiptName(q.Path) {
		f, err = d.open(q.Path, unix.O_RDONLY)
	} else {
		f, err = guestOutputFile(d, q.Path, q.Area)
	}
	if err != nil {
		return reply, err
	}
	defer f.Close()
	if q.Area == "log" || q.Area == "receipt" {
		limit, valid := managed.GuestRecoveryArtifactLimit(q.Path, original.LogLimitBytes)
		info, e := f.Stat()
		if !valid || e != nil || info.Size() < 0 || info.Size() > limit {
			return reply, ErrConflict
		}
	}
	if q.Area == "log" {
		var st unix.Stat_t
		if unix.Fstat(int(f.Fd()), &st) != nil || uint64(st.Dev) != original.LogDevice || uint64(st.Ino) != original.LogInode {
			return reply, ErrConflict
		}
	}
	return readGuestFile(f, q, reply)
}

func qReceiptName(name string) bool {
	return name == "guest.json" || name == "request.json" || name == "intent.json" || name == "start.json" || name == "outcome.json" || name == "exit.json"
}
