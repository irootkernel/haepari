//go:build integration

package managed

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task036PowerCessationLedger", ginkgo.Label("integration"), func() {
	ginkgo.It("fences every original power identity field and command-return bytes", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "power.sqlite"), 1)
		defer db.Close()
		victimSpec := vmScheduling(t, l, "victim", 1, "started", true)
		targetSpec := vmScheduling(t, l, "target", 2, "started", false)
		_, err := l.BindInstallation("power-recovery-fixture")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		j := validJob()
		j.ResourceID = "target"
		j.Deadlines.QueueSeconds = 600
		r, err := l.Admit(encode(t, j), digest, "power-admission", "power-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reserved, err := l.Reserve(ReserveRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "power-reserve", VMGuard: &VMReservationGuard{HostLimit: 1, ObservationRevisions: map[string]int64{"victim": 1, "target": 1}}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		seedHistoricalPower(t, db, reserved, targetSpec, &victimSpec, PowerStopReady)
		p, err := l.PowerIntent(1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.ClaimHostPowerEffect(p.Identity, "stop")).To(gomega.Succeed())
		gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
		s, err := l.PowerRecoverySnapshot("target")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q := PowerCessation{Schema: "haepari.power-cessation/v2", Identity: p.Identity, ExpectedStage: s.Intent.Stage, SnapshotDigest: RecoveryDigest(s), ClientDigest: RecoveryDigest("public-request"), MutationKey: "power-cessation", TargetPower: "stopped", VictimPower: "stopped", Evidence: RecoveryEvidence{Source: "operator-attested", Observer: "fixture", ObservedAt: time.Now().UTC(), Mechanism: "power-command-and-effects-ceased/v1", Digest: RecoveryDigest("external-proof"), Reference: "power:1"}}
		for i, alter := range []func(*PowerTransitionIdentity){
			func(id *PowerTransitionIdentity) { id.OperationGeneration++ },
			func(id *PowerTransitionIdentity) { id.TargetLeaseGeneration++ },
			func(id *PowerTransitionIdentity) { id.TargetJobID = "wrong-job" },
			func(id *PowerTransitionIdentity) { id.TargetResourceID = "wrong-target" },
			func(id *PowerTransitionIdentity) { id.TargetConfigRevision++ },
			func(id *PowerTransitionIdentity) { id.TargetVMUUID = id.VictimVMUUID },
			func(id *PowerTransitionIdentity) { id.VictimResourceID = "wrong-victim" },
			func(id *PowerTransitionIdentity) { id.VictimConfigRevision++ },
			func(id *PowerTransitionIdentity) { id.VictimVMUUID = id.TargetVMUUID },
		} {
			bad := q
			bad.MutationKey = fmt.Sprintf("wrong-power-%d", i)
			alter(&bad.Identity)
			bad.Evidence.Reference = fmt.Sprintf("power:%d", bad.Identity.OperationGeneration)
			_, err = l.ResolvePowerCessation(bad)
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		// Same stage and state, different durable command-return receipt: an
		// earlier plan must still become stale.
		gomega.Expect(l.RecordPowerCommandReturn(p.Identity, "stop", true, digest)).To(gomega.Succeed())
		_, err = l.ResolvePowerCessation(q)
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		s, err = l.PowerRecoverySnapshot("target")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.SnapshotDigest = RecoveryDigest(s)
		// A failed transaction must preserve both protections and the original
		// progress bytes, without publishing a partial cessation event.
		_, err = db.Exec(`CREATE TRIGGER power_proof_failure BEFORE INSERT ON managed_events WHEN NEW.kind='power_cessation' BEGIN SELECT RAISE(ABORT,'fixture storage failure'); END`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.ResolvePowerCessation(q)
		gomega.Expect(err).To(gomega.HaveOccurred())
		after, err := l.PowerRecoverySnapshot("target")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(RecoveryDigest(after)).To(gomega.Equal(RecoveryDigest(s)))
		_, err = db.Exec(`DROP TRIGGER power_proof_failure`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.ResolvePowerCessation(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		afterJob, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(afterJob.JobRevision).To(gomega.Equal(s.Job.JobRevision))
		gomega.Expect(afterJob.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(afterJob.ResourceOccupancy).To(gomega.Equal(1))
		_, err = l.PowerRecoverySnapshot("target")
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
	})
})
