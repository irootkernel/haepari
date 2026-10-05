package managed

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("VMPreparationStepValidation", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses unobserved, foreign, repeated and negative preparation durations", func() {
		now := time.Now().UTC()
		p := LocalPhase{Schema: "haepari.phase-receipt/v1", Phase: "prepare", BudgetSeconds: 10, StartedAt: now, Deadline: now.Add(10 * time.Second), ObservedAt: now.Add(time.Second), Completed: true, Outcome: "complete", AdapterSteps: []ResultStep{{Step: "ssh_ready", DurationMillis: 20}, {Step: "transfer", DurationMillis: 30}}}
		gomega.Expect(validLocalPhase(p)).To(gomega.BeTrue())
		for _, change := range []func(*LocalPhase){
			func(q *LocalPhase) { q.Completed, q.Outcome = false, "" },
			func(q *LocalPhase) { q.Phase = "run" },
			func(q *LocalPhase) { q.AdapterSteps[0].Step = "foreign" },
			func(q *LocalPhase) { q.AdapterSteps[1].Step = "ssh_ready" },
			func(q *LocalPhase) { q.AdapterSteps[0].DurationMillis = -1 },
		} {
			q := p
			q.AdapterSteps = append([]ResultStep{}, p.AdapterSteps...)
			change(&q)
			gomega.Expect(validLocalPhase(q)).To(gomega.BeFalse())
		}
	})
})
