//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func measuredRecoveryPlan(s *runtimeServer, jobID string) jobRecoveryPlan {
	path := "/v1/jobs/" + jobID + "/recover"
	request := managed.RecoveryRequest{Plan: true, Action: "reconcile"}
	response := recoveryHTTP(s, path, request)
	gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
	var plan jobRecoveryPlan
	recoveryData(response, &plan)
	gomega.Expect(plan.Schema).To(gomega.Equal("haepari.recovery-plan/v4"))
	gomega.Expect(plan.Unknowns).To(gomega.BeEmpty())
	gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
	before, err := s.ledger.EventsAfter(jobID, 0, 1000)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	response = recoveryHTTP(s, path, request)
	gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
	var next jobRecoveryPlan
	recoveryData(response, &next)
	gomega.Expect(next.Snapshot).To(gomega.Equal(plan.Snapshot))
	gomega.Expect(next.Observation).To(gomega.Equal(plan.Observation))
	after, err := s.ledger.EventsAfter(jobID, 0, 1000)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(len(after.Events)).To(gomega.BeNumerically(">", len(before.Events)))
	probes := 0
	for _, event := range after.Events[len(before.Events):] {
		if event.Kind == "vm_ssh_exchange_start" || event.Kind == "vm_ssh_exchange_complete" {
			var exchange managed.VMSSHExchange
			gomega.Expect(json.Unmarshal(event.Payload, &exchange)).To(gomega.Succeed())
			if exchange.Operation == "probe" {
				probes++
			}
		}
	}
	gomega.Expect(probes).To(gomega.Equal(2))
	return plan
}

func applyMeasuredRecovery(s *runtimeServer, jobID string, plan jobRecoveryPlan) {
	path := "/v1/jobs/" + jobID + "/recover"
	q := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "measured-recovery"}
	response := recoveryHTTP(s, path, q)
	gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
	s.workers.Wait()
	before, err := s.ledger.EventsAfter(jobID, 0, 1000)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	replay := recoveryHTTP(s, path, q)
	gomega.Expect(replay.Code).To(gomega.Equal(200), replay.Body.String())
	gomega.Expect(replay.Body.String()).To(gomega.Equal(response.Body.String()))
	after, err := s.ledger.EventsAfter(jobID, 0, 1000)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(after.Events).To(gomega.Equal(before.Events))
	result, err := s.ledger.Result(jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
	state, err := s.ledger.Snapshot(jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(state.GlobalOccupancy).To(gomega.BeZero())
	finished := 0
	for _, event := range after.Events {
		if event.Kind == "finished" {
			finished++
		}
	}
	gomega.Expect(finished).To(gomega.Equal(1))
}

var _ = ginkgo.Describe("VMRecoveryMeasuredProbe", ginkgo.Label("integration"), func() {
	ginkgo.It("keeps cross-boot public plan/apply stable with the production measurement callback", func() {
		f, r := previousBootVMFixture()
		// Model the current controller recovering an existing original Job.
		// Its original runner/admission bytes remain untouched.
		f.s.vmTransport = true
		plan := measuredRecoveryPlan(f.s, r.JobID)
		applyMeasuredRecovery(f.s, r.JobID, plan)
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, r.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
	})
	ginkgo.It("consumes completed-cleanup proof without another start or deletion under measured observation", func() {
		f, r, effects := completedCleanupPreviousBootFixture("haepari.job/v2")
		gomega.Expect(effects).To(gomega.Equal([]string{"start", "cleanup"}))
		old := f.s
		fresh, err := newRuntime(old.root, old.ledger, old.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fresh.utmCommand, fresh.sshCommand, fresh.vmLeases = old.utmCommand, old.sshCommand, old.vmLeases
		fresh.vmExecutable, fresh.vmTransport = old.vmExecutable, true
		f.s = fresh
		native := fresh.sshCommand
		var extraEffects []string
		fresh.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			operation := vmTransportRequest(input).Operation
			if operation == "start" || operation == "cleanup" {
				extraEffects = append(extraEffects, operation)
			}
			return native(ctx, exe, args, input)
		}
		controlPath := filepath.Join(f.guest, ".haepari-control-"+r.JobID+".json")
		control, err := os.ReadFile(controlPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		plan := measuredRecoveryPlan(fresh, r.JobID)
		applyMeasuredRecovery(fresh, r.JobID, plan)
		gomega.Expect(extraEffects).To(gomega.BeEmpty())
		gomega.Expect(os.ReadFile(controlPath)).To(gomega.Equal(control))
	})
})
