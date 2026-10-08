//go:build integration

package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

func nativeGuestProtocol(q any, recovery bool, crash string) (managed.Envelope, error) {
	envelope, err, _ := nativeGuestProtocolObserved(q, recovery, crash)
	return envelope, err
}

func nativeGuestProtocolObserved(q any, recovery bool, crash string) (managed.Envelope, error, string) {
	exe, err := os.Executable()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	verb := "guest"
	if recovery {
		verb = "guest-recovery"
	}
	cmd := exec.Command(exe, "runner", verb, "--stdio")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "GORACE=atexit_sleep_ms=0", "HAEPARI_TEST_GUEST_RECOVERY_CRASH=" + crash}
	if request, ok := q.(GuestRecoveryRequest); ok {
		cmd.Env = append(cmd.Env, "HAEPARI_TEST_GUEST_RECOVERY_ROOT="+filepath.Join(request.Guest.JobRoot, request.Guest.Request.Identity.JobID))
	}
	raw, _ := json.Marshal(q)
	cmd.Stdin = bytes.NewReader(raw)
	var out, diag bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diag
	err = cmd.Run()
	var envelope managed.Envelope
	if out.Len() > 0 {
		gomega.Expect(json.Unmarshal(out.Bytes(), &envelope)).To(gomega.Succeed())
	}
	return envelope, err, diag.String()
}
func retainedNativeGuest() (GuestRecoveryRequest, string) {
	return retainedNativeGuestWithLogLimit(0)
}

func retainedNativeGuestWithLogLimit(limit int64) (GuestRecoveryRequest, string) {
	guest, parent := guestFixture("printf once > count.txt; printf native-log")
	guest.Request.LogLimitBytes = limit
	guest.Request.Environment = append(guest.Request.Environment, "GORACE=atexit_sleep_ms=0")
	envelope, err := nativeGuestProtocol(guest, false, "")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var prepared GuestReply
	gomega.Expect(json.Unmarshal(envelope.Data, &prepared)).To(gomega.Succeed())
	guest.Request = *prepared.Request
	guest.Files = nil
	guest.Inventory = nil
	guest.Operation = "start"
	_, err = nativeGuestProtocol(guest, false, "")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	guest.Operation = "watch"
	envelope, err = nativeGuestProtocol(guest, false, "")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var stopped GuestReply
	gomega.Expect(json.Unmarshal(envelope.Data, &stopped)).To(gomega.Succeed())
	gomega.Expect(stopped.Observation.Exit.TerminationProven).To(gomega.BeTrue())
	gomega.Eventually(func() bool {
		p, e := platform.ObserveProcess(stopped.Observation.Intent.Supervisor.PID)
		return errors.Is(e, unix.ESRCH) || e == nil && p.Identity != stopped.Observation.Intent.Supervisor
	}, 3*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
	// Deliberately synthetic false-proof receipt after observed native exit;
	// no production or campaign receipt is modified by this fixture.
	path := filepath.Join(parent, guest.Request.Identity.JobID, "exit.json")
	exit := *stopped.Observation.Exit
	exit.TerminationProven = false
	exit.Reason = "native_observation_uncertain"
	if limit != 0 {
		// Synthetic historical optional-manifest absence, not real migration proof.
		exit.Log = nil
	}
	raw, _ := json.Marshal(exit)
	gomega.Expect(os.WriteFile(path, raw, 0600)).To(gomega.Succeed())
	q := GuestRecoveryRequest{Schema: "haepari.guest-recovery-request/v1", Operation: "probe", Guest: guest, RecoveryDigest: guest.RunnerDigest, Deadline: time.Now().Add(time.Minute).UTC()}
	envelope, err = nativeGuestProtocol(q, true, "")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var probe GuestRecoveryReply
	gomega.Expect(json.Unmarshal(envelope.Data, &probe)).To(gomega.Succeed())
	q.Basis = probe.Basis
	q.Operation = "attest"
	q.MutationKey = "native-guest-attestation"
	q.Evidence = &managed.RecoveryEvidence{Source: "operator-attested", Observer: "owned-native-fixture", ObservedAt: time.Now().UTC(), Mechanism: "guest-workload-and-descendants-stopped", Digest: managed.RecoveryDigest(*q.Basis), Reference: "job:" + guest.Request.Identity.JobID}
	return q, parent
}

var _ = ginkgo.Describe("GuestRecoveryNative", ginkgo.Label("integration"), func() {
	ginkgo.It("enforces original and historical default log bounds without an optional exit checksum", func() {
		for _, limit := range []int64{8, 0} {
			q, parent := retainedNativeGuestWithLogLimit(limit)
			defer os.RemoveAll(parent)
			envelope, err := nativeGuestProtocol(q, true, "")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var reply GuestRecoveryReply
			gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
			q.Attestation = reply.Attestation
			q.Operation = "file"
			q.Guest.Area, q.Guest.Path = "log", "output.log"
			_, err = nativeGuestProtocol(q, true, "")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if limit == 0 {
				limit = DefaultLogBytes
			}
			root := filepath.Join(parent, q.Guest.Request.Identity.JobID)
			// Sparse extension tests refusal before hashing/streaming oversized bytes.
			gomega.Expect(os.Truncate(filepath.Join(root, "output.log"), limit+1)).To(gomega.Succeed())
			_, err = nativeGuestProtocol(q, true, "")
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(os.ReadFile(filepath.Join(root, "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
		}
	})
	ginkgo.It("completes outside receipt directory sync on exact replay before retirement or deletion", func() {
		q, parent := retainedNativeGuest()
		defer os.RemoveAll(parent)
		root := filepath.Join(parent, q.Guest.Request.Identity.JobID)
		attestationPath := filepath.Join(parent, ".haepari-attestation-"+q.Guest.Request.Identity.JobID+".json")
		for _, point := range []string{"receipt_published", "receipt_before_directory_sync"} {
			_, err := nativeGuestProtocol(q, true, point)
			var stopped *exec.ExitError
			gomega.Expect(errors.As(err, &stopped)).To(gomega.BeTrue())
			gomega.Expect(stopped.ExitCode()).To(gomega.Equal(91))
			var control GuestControl
			raw, e := os.ReadFile(filepath.Join(parent, ".haepari-control-"+q.Guest.Request.Identity.JobID+".json"))
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			gomega.Expect(json.Unmarshal(raw, &control)).To(gomega.Succeed())
			gomega.Expect(control.Retired).To(gomega.BeFalse())
		}
		original, err := os.ReadFile(attestationPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		envelope, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestRecoveryReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		q.Attestation = reply.Attestation
		q.Operation = "cleanup"
		q.MutationKey = "directory-sync-cleanup"
		q.CollectionDigest = managed.RecoveryDigest(q.Basis.Digests) // guest mechanics only
		cleanupPath := filepath.Join(parent, ".haepari-attested-cleanup-"+q.Guest.Request.Identity.JobID+".json")
		for _, point := range []string{"receipt_published", "receipt_before_directory_sync"} {
			_, err = nativeGuestProtocol(q, true, point)
			var stopped *exec.ExitError
			gomega.Expect(errors.As(err, &stopped)).To(gomega.BeTrue())
			gomega.Expect(stopped.ExitCode()).To(gomega.Equal(91))
			gomega.Expect(os.ReadFile(filepath.Join(root, "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
		}
		cleanup, err := os.ReadFile(cleanupPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = os.Stat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(attestationPath)).To(gomega.Equal(original))
		gomega.Expect(os.ReadFile(cleanupPath)).To(gomega.Equal(cleanup))
	})
	ginkgo.It("refuses file and final directory unlink when observation consumes the saved deadline", func() {
		exe, err := os.Executable()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, point := range []string{"file", "directory"} {
			parent, err := os.MkdirTemp("", "guest-unlink-deadline-")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			parent, err = filepath.EvalSymlinks(parent)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer os.RemoveAll(parent)
			path := filepath.Join(parent, "subject")
			if point == "file" {
				gomega.Expect(os.WriteFile(path, []byte("preserve"), 0600)).To(gomega.Succeed())
			} else {
				gomega.Expect(os.Mkdir(path, 0700)).To(gomega.Succeed())
			}
			cmd := exec.Command(exe, "guest-remove-deadline-fixture", point, path)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
			output, err := cmd.CombinedOutput()
			var stopped *exec.ExitError
			gomega.Expect(errors.As(err, &stopped)).To(gomega.BeTrue())
			gomega.Expect(stopped.ExitCode()).To(gomega.Equal(77), string(output))
			gomega.Expect(string(output)).To(gomega.ContainSubstring("guest-cleanup-unlink-boundary-reached"))
			info, err := os.Stat(path)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(info.IsDir()).To(gomega.Equal(point == "directory"))
			if point == "file" {
				gomega.Expect(os.ReadFile(path)).To(gomega.Equal([]byte("preserve")))
			}
		}
	})
	ginkgo.It("refuses authority that expires after admission but before attestation or cleanup effects", func() {
		q, parent := retainedNativeGuest()
		defer os.RemoveAll(parent)
		q.Deadline = time.Now().Add(time.Second).UTC()
		_, err, diagnostic := nativeGuestProtocolObserved(q, true, "expire:guest_attestation_authority")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(diagnostic).To(gomega.ContainSubstring("guest-recovery-deadline-boundary-reached"))
		_, err = os.Stat(filepath.Join(parent, ".haepari-attestation-"+q.Guest.Request.Identity.JobID+".json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		var control GuestControl
		raw, err := os.ReadFile(filepath.Join(parent, ".haepari-control-"+q.Guest.Request.Identity.JobID+".json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(raw, &control)).To(gomega.Succeed())
		gomega.Expect(control.Retired).To(gomega.BeFalse())
		// Fresh fixture authority after a proven absent effect, not an extension
		// of an existing saved host intent or published guest attestation.
		q.MutationKey = "fresh-after-absent-expired-effect"
		q.Deadline = time.Now().Add(time.Minute).UTC()
		q.Evidence.ObservedAt = time.Now().UTC()
		envelope, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestRecoveryReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		q.Attestation = reply.Attestation
		q.Operation = "cleanup"
		q.MutationKey = "expired-before-cleanup"
		q.CollectionDigest = managed.RecoveryDigest(q.Basis.Digests)
		q.Deadline = time.Now().Add(time.Second).UTC()
		_, err, diagnostic = nativeGuestProtocolObserved(q, true, "expire:guest_attested_cleanup_authority")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(diagnostic).To(gomega.ContainSubstring("guest-recovery-deadline-boundary-reached"))
		_, err = os.Stat(filepath.Join(parent, ".haepari-attested-cleanup-"+q.Guest.Request.Identity.JobID+".json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(filepath.Join(parent, q.Guest.Request.Identity.JobID, "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
	})
	ginkgo.It("preserves replacement bytes when the root is exchanged after the cleanup intent and before deletion", func() {
		q, parent := retainedNativeGuest()
		defer os.RemoveAll(parent)
		envelope, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestRecoveryReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		q.Attestation = reply.Attestation
		q.Operation = "cleanup"
		q.MutationKey = "root-transition-cleanup"
		q.CollectionDigest = managed.RecoveryDigest(q.Basis.Digests)
		_, err, diagnostic := nativeGuestProtocolObserved(q, true, "replace-root")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(strings.Contains(diagnostic, "guest-recovery-root-replaced")).To(gomega.BeTrue())
		root := filepath.Join(parent, q.Guest.Request.Identity.JobID)
		gomega.Expect(os.ReadFile(filepath.Join(root, "keep.txt"))).To(gomega.Equal([]byte("replacement")))
		gomega.Expect(os.ReadFile(filepath.Join(root+"-retained", "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
	})
	ginkgo.It("refuses foreign boot, digest, owner, basis, expired authority and contended native locks before attestation", func() {
		q, parent := retainedNativeGuest()
		defer os.RemoveAll(parent)
		for _, alter := range []func(*GuestRecoveryRequest){
			func(v *GuestRecoveryRequest) { v.Guest.BootID = "foreign-boot" },
			func(v *GuestRecoveryRequest) { v.RecoveryDigest = managed.RecoveryDigest("foreign-runner") },
			func(v *GuestRecoveryRequest) { v.Guest.Request.Identity.LeaseGeneration++ },
			func(v *GuestRecoveryRequest) { v.Deadline = time.Now().Add(-time.Second) },
			func(v *GuestRecoveryRequest) {
				basis := *v.Basis
				basis.Root.Inode++
				v.Basis = &basis
			},
		} {
			changed := q
			alter(&changed)
			_, err := nativeGuestProtocol(changed, true, "")
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		jobRoot := filepath.Join(parent, q.Guest.Request.Identity.JobID)
		lock, err := os.OpenFile(filepath.Join(jobRoot, "run.lock"), os.O_RDWR, 0)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)).To(gomega.Succeed())
		_, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).To(gomega.HaveOccurred())
		lock.Close()
		current, err := platform.ObserveProcess(os.Getpid())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		observation := q.Basis.Observation
		intent := *observation.Intent
		intent.Supervisor = current.Identity
		observation.Intent = &intent
		gomega.Expect(recoveryOwnersStopped(observation, current.Identity.BootID)).To(gomega.MatchError(ErrUnproven))
		_, err = os.Stat(filepath.Join(parent, ".haepari-attestation-"+q.Guest.Request.Identity.JobID+".json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(filepath.Join(jobRoot, "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
	})
	ginkgo.It("retains the published attestation across native death before retirement and replays the exact barrier", func() {
		q, parent := retainedNativeGuest()
		defer os.RemoveAll(parent)
		_, err := nativeGuestProtocol(q, true, "guest_attestation_published")
		gomega.Expect(err).To(gomega.HaveOccurred())
		original, err := os.ReadFile(filepath.Join(parent, ".haepari-attestation-"+q.Guest.Request.Identity.JobID+".json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		state := q
		state.Operation = "state"
		state.Attestation = new(GuestAttestation)
		gomega.Expect(json.Unmarshal(original, state.Attestation)).To(gomega.Succeed())
		_, err = nativeGuestProtocol(state, true, "")
		gomega.Expect(err).To(gomega.HaveOccurred())
		envelope, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestRecoveryReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		gomega.Expect(reply.Attestation).NotTo(gomega.BeNil())
		gomega.Expect(os.ReadFile(filepath.Join(parent, ".haepari-attestation-"+q.Guest.Request.Identity.JobID+".json"))).To(gomega.Equal(original))
		start := q.Guest
		start.Operation = "start"
		_, err = nativeGuestProtocol(start, false, "")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(os.ReadFile(filepath.Join(parent, start.Request.Identity.JobID, "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
	})
	ginkgo.It("preserves a saved cleanup intent across native death and observes absence after completed deletion", func() {
		q, parent := retainedNativeGuest()
		defer os.RemoveAll(parent)
		envelope, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestRecoveryReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		q.Attestation = reply.Attestation
		q.Operation = "cleanup"
		q.MutationKey = "native-saved-cleanup"
		q.CollectionDigest = managed.RecoveryDigest(q.Basis.Digests)
		_, err = nativeGuestProtocol(q, true, "guest_attested_cleanup_intent")
		gomega.Expect(err).To(gomega.HaveOccurred())
		_, err = os.Stat(filepath.Join(parent, q.Guest.Request.Identity.JobID))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		changed := q
		changed.CollectionDigest = managed.RecoveryDigest("different-copy")
		_, err = nativeGuestProtocol(changed, true, "")
		gomega.Expect(err).To(gomega.HaveOccurred())
		_, err = nativeGuestProtocol(q, true, "guest_attested_cleanup_deleted")
		gomega.Expect(err).To(gomega.HaveOccurred())
		health := q
		health.Operation = "health"
		health.Deadline = time.Now().Add(5 * time.Second).UTC()
		envelope, err = nativeGuestProtocol(health, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		gomega.Expect(reply.Reusable).To(gomega.BeTrue())
		// Recreating the protected name can never be adopted as the old root.
		gomega.Expect(os.Mkdir(filepath.Join(parent, q.Guest.Request.Identity.JobID), 0700)).To(gomega.Succeed())
		_, err = nativeGuestProtocol(health, true, "")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
	ginkgo.It("completes parent sync before adopting absence after native death immediately after root unlink", func() {
		q, parent := retainedNativeGuest()
		defer os.RemoveAll(parent)
		envelope, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestRecoveryReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		q.Attestation = reply.Attestation
		q.Operation = "cleanup"
		q.MutationKey = "native-unlinked-cleanup"
		q.CollectionDigest = managed.RecoveryDigest(q.Basis.Digests)
		_, err = nativeGuestProtocol(q, true, "guest_cleanup_root_unlinked")
		gomega.Expect(err).To(gomega.HaveOccurred())
		var exitError *exec.ExitError
		gomega.Expect(errors.As(err, &exitError)).To(gomega.BeTrue())
		gomega.Expect(exitError.ExitCode()).To(gomega.Equal(91))
		_, err = os.Stat(filepath.Join(parent, q.Guest.Request.Identity.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		intentPath := filepath.Join(parent, ".haepari-attested-cleanup-"+q.Guest.Request.Identity.JobID+".json")
		intent, err := os.ReadFile(intentPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.Operation = "health"
		// Reuse this saved health budget across its own interrupted observation.
		_, err = nativeGuestProtocol(q, true, "guest_attested_absence_before_directory_sync")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.As(err, &exitError)).To(gomega.BeTrue())
		gomega.Expect(exitError.ExitCode()).To(gomega.Equal(91))
		envelope, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		gomega.Expect(reply.Reusable).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(intentPath)).To(gomega.Equal(intent))
	})
})
