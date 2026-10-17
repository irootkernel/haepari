package managed

import (
	"encoding/json"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// Extraction only. These values mirror field names and do not call the
// production methods or prove occupancy, receipts, or effects. An empty
// result is not coverage: mutate then skips the bounds guard.
var _ = ginkgo.Describe("EffectJobID", ginkgo.Label("unit"), func() {
	ginkgo.It("resolves every concrete request that reaches mutate", func() {
		const sentinel = "job-bounds-sentinel"
		id := LocalIdentity{JobID: sentinel}
		phase := LocalPhase{Identity: id}
		cases := []struct {
			name    string
			request any
		}{
			{"manual_claim", struct {
				Identity     LocalIdentity
				Revision     int64
				Deadline     time.Time
				Installation string
			}{Identity: id}},
			{"manual_deadline", struct {
				Identity LocalIdentity
				Revision int64
				Reason   string
			}{Identity: id}},
			{"manual_abort", struct {
				Identity LocalIdentity
				Revision int64
				Reason   string
				NoEffect bool
			}{Identity: id}},
			{"job_recovery", JobRecoveryRequest{Identity: id}},
			{"recovery_clock", struct {
				Identity       LocalIdentity
				SnapshotDigest string
				ObservedAt     time.Time
			}{Identity: id}},
			{"phase_recovery", PhaseResolutionRequest{Record: phase}},
			{"vm_claim", struct {
				Identity LocalIdentity
				Revision int64
				Plan     json.RawMessage
			}{Identity: id}},
			{"vm_start", struct {
				Identity LocalIdentity
				Revision int64
			}{Identity: id}},
			{"manual_hold", ManualHoldRequest{JobID: sentinel}},
			{"local_outcome", LocalOutcomeRequest{Identity: id}},
			{"local_preparation_stop", LocalPreparationStopRequest{Identity: id}},
			{"local_termination", LocalTerminationRequest{Identity: id}},
			{"local_deadline", struct {
				Identity LocalIdentity
				Revision int64
			}{Identity: id}},
			{"reserve", ReserveRequest{JobID: sentinel}},
			{"record_no_effect_observation", NoEffectObservationRequest{JobID: sentinel}},
			{"classify_no_effect", ClassifyNoEffectRequest{JobID: sentinel}},
			{"settle_no_effect", SettleNoEffectRequest{JobID: sentinel}},
			{"finish_queued", struct {
				JobID            string
				ExpectedRevision int64
			}{JobID: sentinel}},
			{"power_cessation", PowerCessation{Identity: PowerTransitionIdentity{TargetJobID: sentinel}}},
			{"local_phase", struct {
				JobID    string
				Revision int64
				From, To string
			}{JobID: sentinel}},
			{"local_start", struct {
				JobID    string
				Revision int64
				RunID    string
			}{JobID: sentinel}},
			{"local_settle", struct {
				JobID    string
				Revision int64
				Result   Result
			}{JobID: sentinel}},
			{"local_phase_observation", LocalPhaseRequest{Record: phase}},
		}
		gomega.Expect(cases).To(gomega.HaveLen(23))
		for _, tc := range cases {
			gomega.Expect(effectJobID(tc.request)).To(gomega.Equal(sentinel), tc.name)
		}
		gomega.Expect(effectJobID(&ReserveRequest{JobID: sentinel})).To(gomega.Equal(sentinel))
		gomega.Expect(effectJobID(struct{ MutationKey string }{MutationKey: "unbound"})).To(gomega.BeEmpty())
	})
})
