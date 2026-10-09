package runner

import (
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

var rebootReceiptNames = []string{"guest.json", "request.json", "intent.json", "start.json", "outcome.json", "exit.json", "haepari"}

func validateRebootAttestation(a GuestAttestation, guest GuestRequest) error {
	b := a.Basis
	if guest.Schema == "haepari.guest-request/v2" && !validGuestWorkspaceRoot(b.WorkspaceRoot) || guest.Schema != "haepari.guest-request/v2" && b.WorkspaceRoot != nil {
		return ErrConflict
	}
	if !sameGuestOwner(a.Guest, guest) || a.Guest.Challenge != "" || a.Guest.Files != nil || a.Guest.Inventory != nil || a.Guest.Operation != "inspect" || b.RecoveryBoot == "" || b.RecoveryBoot == guest.BootID ||
		!guestDigest(a.RecoveryDigest) || !managed.ValidRequestKey(a.MutationKey) || a.Deadline.IsZero() || managed.ValidateRecoveryEvidence(a.Evidence) != nil || a.Evidence.Reference != "job:"+guest.Request.Identity.JobID || a.Evidence.Digest != managed.RecoveryDigest(b) || a.Evidence.ObservedAt.After(a.Deadline) ||
		b.Root.Path != "" || !b.Root.Directory || b.Root.Inode == 0 || b.Control.Path != "" || b.Control.Directory || b.Control.Inode == 0 || !reflect.DeepEqual(b.Request, guest.Request) || b.Request.BootID != guest.BootID || ValidateGuestObservation(b.Observation, b.Request) != nil {
		return ErrConflict
	}
	if !(a.Evidence.Source == "operator-attested" && a.Evidence.Mechanism == "guest-workload-and-descendants-stopped" || a.Evidence.Source == "machine-observed" && a.Evidence.Mechanism == "guest-reboot-observed") {
		return ErrConflict
	}
	missing := map[string]bool{}
	for _, name := range b.Missing {
		if missing[name] || name != "intent.json" && name != "start.json" && name != "outcome.json" && name != "exit.json" {
			return ErrConflict
		}
		missing[name] = true
	}
	if len(b.Digests)+len(missing) != len(rebootReceiptNames) || b.Digests["haepari"] != guest.RunnerDigest {
		return ErrConflict
	}
	for _, name := range rebootReceiptNames {
		if missing[name] {
			if _, exists := b.Digests[name]; exists {
				return ErrConflict
			}
		} else if !guestDigest(b.Digests[name]) {
			return ErrConflict
		}
	}
	for _, entry := range []struct {
		name    string
		present bool
	}{{"intent.json", b.Observation.Intent != nil}, {"start.json", b.Observation.Start != nil}, {"outcome.json", b.Observation.Outcome != nil}, {"exit.json", b.Observation.Exit != nil}} {
		if missing[entry.name] == entry.present {
			return ErrConflict
		}
	}
	return nil
}

// Cross-boot recovery never changes native receipts. A freshly observed different
// boot stops every process incarnation from the original boot, including escaped
// descendants; absence in a same-boot process census does not supply that proof.
func recoveryBasisOwnersStopped(b GuestRecoveryBasis, originalBoot string) error {
	if b.RecoveryBoot == "" {
		return recoveryOwnersStopped(b.Observation, originalBoot)
	}
	boot, err := platform.BootIdentity()
	if err != nil || boot != b.RecoveryBoot || boot == originalBoot {
		return ErrUnproven
	}
	return nil
}

func rebootRecoveryBasis(d *directory, c *guestControlFile, guest GuestRequest, boot string, deadline time.Time) (GuestRecoveryBasis, error) {
	b := GuestRecoveryBasis{RecoveryBoot: boot, Digests: map[string]string{}}
	if err := validateGuestWorkspace(d, c.value); err != nil {
		return b, err
	}
	b.WorkspaceRoot = c.value.WorkspaceRoot
	var st unix.Stat_t
	if c.value.Root == nil || c.value.NativeRequest == nil || unix.Fstat(int(c.file.Fd()), &st) != nil {
		return b, ErrUnproven
	}
	b.Control = managed.RetainedPath{Device: uint64(st.Dev), Inode: st.Ino}
	b.Root = *c.value.Root
	if unix.Fstat(int(d.file.Fd()), &st) != nil || uint64(st.Dev) != b.Root.Device || st.Ino != b.Root.Inode {
		return b, ErrConflict
	}
	requestDir, request, err := readRequest(filepath.Join(d.file.Name(), "request.json"))
	if err != nil {
		return b, err
	}
	requestDir.close()
	if !reflect.DeepEqual(request, *c.value.NativeRequest) || !reflect.DeepEqual(request, guest.Request) {
		return b, ErrConflict
	}
	b.Request = request
	b.Observation, err = inspect(d, request)
	if err != nil || ValidateGuestObservation(b.Observation, request) != nil {
		return b, ErrConflict
	}
	for _, name := range rebootReceiptNames {
		limit := int64(receiptLimit)
		if name == "haepari" {
			limit = 64 << 20
		}
		digest, e := recoveryFileDigest(d, name, limit, deadline)
		if errors.Is(e, unix.ENOENT) && name != "guest.json" && name != "request.json" && name != "haepari" {
			b.Missing = append(b.Missing, name)
			continue
		}
		if e != nil {
			return b, e
		}
		b.Digests[name] = digest
	}
	var original GuestRequest
	if d.read("guest.json", &original) != nil || !sameGuestOwner(original, guest) || b.Digests["haepari"] != guest.RunnerDigest {
		return b, ErrConflict
	}
	return b, recoveryBasisOwnersStopped(b, guest.BootID)
}

// This separate read-only variant consumes a durable ordinary cleanup receipt
// after a different boot. It cannot replace a root that is merely missing.
type GuestCompletedCleanup struct {
	RecoveryBoot string               `json:"recovery_boot"`
	ControlInode managed.RetainedPath `json:"control_inode"`
	Control      GuestControl         `json:"control"`
}

func ValidateGuestCompletedCleanup(p GuestCompletedCleanup, guest GuestRequest, boot string) error {
	c := p.Control
	if boot == "" || boot == guest.BootID || p.RecoveryBoot != boot || guest.Root == nil || p.ControlInode.Path != "" || p.ControlInode.Directory || p.ControlInode.Inode == 0 ||
		ValidateGuestControl(c, guest) != nil || c.Phase != "cleanup" || !c.Complete || !c.Stopped || c.Error != "" || c.NativeRequest == nil || !reflect.DeepEqual(*c.NativeRequest, guest.Request) || c.Root == nil || *c.Root != *guest.Root ||
		c.Observation == nil || ValidateGuestObservation(*c.Observation, guest.Request) != nil || c.Observation.Exit == nil || !c.Observation.Exit.TerminationProven {
		return ErrConflict
	}
	return nil
}
