package utm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
)

// CallRecovery proves the current mapping with the trusted recovery binary.
// Its digest is independent of the immutable legacy per-Job runner digest.
func (s SSH) CallRecovery(ctx context.Context, spec managed.ResourceSpec, leases io.Reader, q runner.GuestRecoveryRequest) (runner.GuestRecoveryReply, error) {
	var reply runner.GuestRecoveryReply
	verifierHealth := s.Transport && q.Operation == "health"
	if q.Deadline.IsZero() || !time.Now().Before(q.Deadline) || q.RecoveryDigest != s.RunnerDigest && !verifierHealth || spec.Kind != "utm-macos" || spec.VM == nil || spec.Guest == nil {
		return reply, ErrUnavailable
	}
	ctx, cancel := context.WithDeadline(ctx, q.Deadline)
	defer cancel()
	guest := q.Guest
	reboot := q.Schema == "haepari.guest-recovery-request/v2"
	if !reboot && (q.Schema != "haepari.guest-recovery-request/v1" || q.RecoveryBoot != "") || reboot && q.RecoveryBoot == "" && q.Operation != "probe" {
		return reply, ErrUnavailable
	}
	mac, err := normalizeMAC(spec.VM.MACAddress)
	if err != nil || guest.VMUUID != strings.ToLower(spec.VM.UTMVMUUID) || guest.MAC != mac || guest.JobRoot != spec.VM.GuestJobRoot || guest.Request.Identity.ResourceID != spec.ID || guest.Request.Identity.ConfigRevision != spec.Revision || guest.BootID == "" || guest.Request.BootID != guest.BootID {
		return reply, ErrUnavailable
	}
	pin, err := ReadPin(spec.Guest.KnownHostsReference, guest.VMUUID)
	if err != nil {
		return reply, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return reply, err
	}
	challenge := hex.EncodeToString(nonce[:])
	var proofErr error
	verify := func(ctx context.Context, lease Lease, uuid, nonce string) (GuestIdentity, error) {
		probe := guest
		if reboot {
			// The trusted recovery executable supports classified identity proof.
			// Do not change the original nested Guest request or its receipts.
			probe.Schema = "haepari.guest-request/v2"
			probe.BootID = q.RecoveryBoot
		}
		probe.Operation = "identity"
		probe.Challenge = nonce
		probe.RunnerDigest = s.RunnerDigest
		probe.Deadline = q.Deadline
		probe.Files = nil
		probe.Inventory = nil
		r, e := s.exchange(ctx, spec, lease, probe)
		proofErr = e
		return GuestIdentity{VMUUID: r.VMUUID, MAC: r.MAC, Address: lease.Address, HostKey: pin, BootID: r.BootID, Nonce: r.Challenge, OS: r.OS, Arch: r.Arch}, e
	}
	lease, identity, err := s.Control.ResolveGuest(ctx, leases, guest.VMUUID, mac, pin, challenge, time.Now, verify)
	if err != nil {
		if reboot && errors.Is(proofErr, ErrGuestRefused) {
			return reply, proofErr
		}
		return reply, ErrUnavailable
	}
	if !reboot && identity.BootID != guest.BootID || reboot && (identity.BootID == "" || identity.BootID == guest.BootID || q.RecoveryBoot != "" && identity.BootID != q.RecoveryBoot) || !lease.Expires.After(time.Now()) {
		return reply, ErrUnavailable
	}
	if reboot {
		q.RecoveryBoot = identity.BootID
	}
	q.Guest.Challenge = challenge
	var wire any = q
	var capability runner.GuestTransportRequest
	operation := "runner.guest-recovery"
	if verifierHealth {
		capability = runner.GuestTransportRequest{Schema: "haepari.guest-transport-request/v1", Guest: q.Guest, ExecutorDigest: s.RunnerDigest, Recovery: &q}
		wire = capability
		operation = "runner.guest-transport"
	}
	raw, err := s.exchangeWire(ctx, spec, lease, wire, !verifierHealth)
	if err != nil {
		return reply, errors.Join(ErrGuestUncertain, err)
	}
	envelope, err := managed.DecodeEnvelope(raw)
	if err != nil || envelope.Operation != operation {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if !envelope.OK {
		if reboot {
			return reply, errors.Join(ErrGuestUncertain, guestRefusalError(envelope.Error, true))
		}
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if verifierHealth {
		var r runner.GuestTransportReply
		r, err = runner.DecodeGuestTransportReply(envelope.Data)
		if err != nil || r.ProtocolDigest != runner.GuestTransportDigest(capability) || r.Recovery == nil || r.Binding != nil {
			return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
		}
		reply = *r.Recovery
	} else {
		reply, err = runner.DecodeGuestRecoveryReply(envelope.Data)
	}
	g := reply.Guest
	expectedSchema, expectedBoot := "haepari.guest-recovery-reply/v1", guest.BootID
	if reboot {
		expectedSchema, expectedBoot = "haepari.guest-recovery-reply/v2", q.RecoveryBoot
	}
	if err != nil || reply.RecoveryDigest != q.RecoveryDigest || reply.Schema != expectedSchema || reply.ProtocolDigest != managed.RecoveryDigest(q) || g.Schema != runner.GuestReplySchema(guest) || g.VMUUID != guest.VMUUID || g.MAC != guest.MAC || g.BootID != expectedBoot || g.Challenge != challenge || g.OS != "darwin" || g.Arch != "arm64" || !lease.Expires.After(time.Now()) {
		return runner.GuestRecoveryReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if reply.CompletedCleanup != nil {
		if !reboot || q.Operation != "probe" || q.Basis != nil || q.Attestation != nil || reply.Basis != nil || reply.Attestation != nil || !reply.Reusable || runner.ValidateGuestCompletedCleanup(*reply.CompletedCleanup, guest, expectedBoot) != nil || g.File != nil || g.Absent || len(g.Output) != 0 || g.NextOffset != 0 || g.Request != nil || g.Root != nil || g.Control != nil || g.Observation != nil || g.SupervisorAlive != nil || g.OwnersLive != nil || g.Reusable {
			return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
		}
		return reply, nil
	}
	if reply.Basis == nil || reply.Basis.Request.Identity != guest.Request.Identity || reply.Basis.Request.BootID != guest.BootID || guest.Root != nil && reply.Basis.Root != *guest.Root {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if reboot && reply.Basis.RecoveryBoot != expectedBoot || !reboot && (reply.Basis.RecoveryBoot != "" || len(reply.Basis.Missing) != 0) {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if reply.Attestation != nil && (runner.ValidateGuestAttestation(*reply.Attestation, guest) != nil || reply.Attestation.RecoveryDigest != q.RecoveryDigest) {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if q.Basis != nil && !reflect.DeepEqual(*q.Basis, *reply.Basis) {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if q.Attestation != nil && (reply.Attestation == nil || !reflect.DeepEqual(*q.Attestation, *reply.Attestation)) {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if q.Operation != "probe" && reply.Attestation == nil {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if g.Absent {
		if !reboot || q.Operation != "file" || guest.Schema != "haepari.guest-request/v2" || reply.Attestation == nil || reply.Basis.WorkspaceRoot == nil || !reflect.DeepEqual(*reply.Basis, reply.Attestation.Basis) || guest.Area != "" && guest.Area != "workspace" || !managed.ValidArtifactPath(guest.Path) || guest.Offset != 0 || guest.ExpectedFile != nil || g.File != nil || len(g.Output) != 0 || g.NextOffset != 0 {
			return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
		}
		return reply, nil
	}
	if q.Operation == "file" && (g.File == nil || g.File.Path != guest.Path || g.File.Inode == 0 || len(g.Output) > runner.GuestFilePageBytes(guest.Schema) || g.NextOffset != guest.Offset+int64(len(g.Output)) || g.File.Size < g.NextOffset || g.File.Size > 1<<30 || len(g.File.SHA256) != 64) {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	return reply, nil
}
