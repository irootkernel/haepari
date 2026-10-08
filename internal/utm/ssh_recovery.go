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
	if q.Deadline.IsZero() || !time.Now().Before(q.Deadline) || q.RecoveryDigest != s.RunnerDigest || spec.Kind != "utm-macos" || spec.VM == nil || spec.Guest == nil {
		return reply, ErrUnavailable
	}
	ctx, cancel := context.WithDeadline(ctx, q.Deadline)
	defer cancel()
	guest := q.Guest
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
	verify := func(ctx context.Context, lease Lease, uuid, nonce string) (GuestIdentity, error) {
		probe := guest
		probe.Operation = "identity"
		probe.Challenge = nonce
		probe.RunnerDigest = s.RunnerDigest
		probe.Deadline = q.Deadline
		probe.Files = nil
		probe.Inventory = nil
		r, e := s.exchange(ctx, spec, lease, probe)
		return GuestIdentity{VMUUID: r.VMUUID, MAC: r.MAC, Address: lease.Address, HostKey: pin, BootID: r.BootID, Nonce: r.Challenge, OS: r.OS, Arch: r.Arch}, e
	}
	lease, identity, err := s.Control.ResolveGuest(ctx, leases, guest.VMUUID, mac, pin, challenge, time.Now, verify)
	if err != nil || identity.BootID != guest.BootID || !lease.Expires.After(time.Now()) {
		return reply, ErrUnavailable
	}
	q.Guest.Challenge = challenge
	raw, err := s.exchangeWire(ctx, spec, lease, q, true)
	if err != nil {
		return reply, errors.Join(ErrGuestUncertain, err)
	}
	envelope, err := managed.DecodeEnvelope(raw)
	if err != nil || !envelope.OK || envelope.Operation != "runner.guest-recovery" {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	reply, err = runner.DecodeGuestRecoveryReply(envelope.Data)
	g := reply.Guest
	if err != nil || reply.RecoveryDigest != s.RunnerDigest || reply.Schema != "haepari.guest-recovery-reply/v1" || reply.ProtocolDigest != managed.RecoveryDigest(q) || g.VMUUID != guest.VMUUID || g.MAC != guest.MAC || g.BootID != guest.BootID || g.Challenge != challenge || g.OS != "darwin" || g.Arch != "arm64" || !lease.Expires.After(time.Now()) {
		return runner.GuestRecoveryReply{}, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if reply.Basis == nil || reply.Basis.Request.Identity != guest.Request.Identity || reply.Basis.Request.BootID != guest.BootID || guest.Root != nil && reply.Basis.Root != *guest.Root {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	if reply.Attestation != nil && (runner.ValidateGuestAttestation(*reply.Attestation, guest) != nil || reply.Attestation.RecoveryDigest != s.RunnerDigest) {
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
	if q.Operation == "file" && (g.File == nil || g.File.Path != guest.Path || g.File.Inode == 0 || len(g.Output) > 64<<10 || g.NextOffset != guest.Offset+int64(len(g.Output)) || g.File.Size < g.NextOffset || g.File.Size > 1<<30 || len(g.File.SHA256) != 64) {
		return reply, errors.Join(ErrGuestUncertain, ErrUnavailable)
	}
	return reply, nil
}
