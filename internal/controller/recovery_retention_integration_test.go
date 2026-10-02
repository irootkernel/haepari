//go:build integration

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task012RecoveryReplayRetention", ginkgo.Label("integration"), func() {
	for _, action := range []string{"reconcile", "attest-stopped"} {
		for _, prefix := range []bool{false, true} {
			name := "bulk purge"
			if prefix {
				name = "event prefix and bulk purge"
			}
			ginkgo.It("preserves "+action+" replay across "+name+" and database reopening", func() {
				t := ginkgo.GinkgoT()
				db, s, r, job, object := cancellationRuntime(t, "mkdir -p ../results; printf retained > ../results/output.txt")
				_, release, done := cancellationBarrier(t, s, r, job, object, "settling")
				s.detachWorkers()
				release()
				gomega.Eventually(done, 3*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
				s.workers.Wait()
				gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
				fresh, err := newRuntime(s.root, s.ledger, s.store)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				t.Cleanup(func() { fresh.detachWorkers(); fresh.workers.Wait() })
				path := "/v1/jobs/" + r.JobID + "/recover"
				w := recoveryHTTP(fresh, path, managed.RecoveryRequest{Plan: true, Action: action})
				gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
				var plan jobRecoveryPlan
				recoveryData(w, &plan)
				gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
				q := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "retained-recovery"}
				if action != "reconcile" {
					evidence := recoveryObservation("scoped-stop", "fixture", "job:"+r.JobID)
					evidence.Source, evidence.Observer = "operator-attested", "fixture-operator"
					q.Evidence = &evidence
				}
				w = recoveryHTTP(fresh, path, q)
				gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
				var original managed.Receipt
				recoveryData(w, &original)
				gomega.Eventually(func() string { snap, _ := fresh.ledger.Snapshot(r.JobID); return snap.JobState }, 3*time.Second).Should(gomega.Equal("finished"))
				fresh.workers.Wait()
				result, err := fresh.ledger.Result(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
				if action != "reconcile" {
					gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
					gomega.Expect(result.Observations.OperatorAttested).To(gomega.BeTrue())
				}
				status, err := fresh.ledger.ResourceStatus(r.ResourceID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				if status.Availability == "quarantined" {
					resourcePath := "/v1/resources/" + r.ResourceID + "/recover"
					w = recoveryHTTP(fresh, resourcePath, managed.RecoveryRequest{Plan: true})
					gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
					var health resourceRecoveryPlan
					recoveryData(w, &health)
					gomega.Expect(health.PlanToken).NotTo(gomega.BeEmpty())
					w = recoveryHTTP(fresh, resourcePath, managed.RecoveryRequest{PlanToken: health.PlanToken, MutationKey: "retained-health"})
					gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
				}
				finished, err := fresh.ledger.Snapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				if prefix {
					current, err := fresh.ledger.EventsAfter(r.JobID, 0, 1)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(fresh.ledger.PurgeEventsThrough(r.JobID, current.CurrentCursor)).To(gomega.Succeed())
					batch, err := fresh.ledger.EventsAfter(r.JobID, 0, 100)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(batch.Resync).NotTo(gomega.BeNil())
					gomega.Expect(batch.Events).To(gomega.BeEmpty())
					w = recoveryHTTP(fresh, path, q)
					gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
					var replay managed.Receipt
					recoveryData(w, &replay)
					gomega.Expect(replay).To(gomega.Equal(original))
					finished, err = fresh.ledger.Snapshot(r.JobID)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				raw, err := json.Marshal(map[string]int64{"expected_revision": finished.JobRevision})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				w = httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/jobs/"+r.JobID+"/purge", bytes.NewReader(raw))
				req.Header.Set("X-Haepari-Protocol-Min", "1")
				req.Header.Set("X-Haepari-Protocol-Max", "1")
				req.Header.Set("X-Haepari-Protocol", protocol)
				fresh.routes().ServeHTTP(w, req)
				gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
				var purged managed.PurgeReceipt
				recoveryData(w, &purged)
				gomega.Expect(purged.Complete).To(gomega.BeTrue())
				fresh.workers.Wait()
				var floor int64
				gomega.Expect(db.QueryRow("SELECT purged_through FROM managed_event_progress WHERE job_id=?", r.JobID).Scan(&floor)).To(gomega.Succeed())
				gomega.Expect(floor).To(gomega.BeNumerically(">", 0))
				var oldPayloads int
				gomega.Expect(db.QueryRow("SELECT COUNT(*) FROM managed_events WHERE job_id=? AND cursor<=? AND kind NOT IN ('recovery_intent','phase_reconciled')", r.JobID, floor).Scan(&oldPayloads)).To(gomega.Succeed())
				gomega.Expect(oldPayloads).To(gomega.BeZero())
				_, err = os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "output.log"))
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
				_, err = s.store.Read(object)
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
				gomega.Expect(db.Close()).To(gomega.Succeed())
				reopened, err := sqlite.Open(s.root.DBPath)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				t.Cleanup(func() { _ = reopened.Close() })
				ledger, err := managed.NewLedger(reopened, 1)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				rotated, err := newRuntime(s.root, ledger, s.store)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(rotated.recoveryKey).NotTo(gomega.Equal(fresh.recoveryKey))
				c := artifactClient(rotated)
				before, err := ledger.Snapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				resourceBefore, err := ledger.ResourceStatus(r.ResourceID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				var mutationsBefore, eventsBefore, runsBefore int
				gomega.Expect(reopened.QueryRow("SELECT COUNT(*) FROM managed_mutations").Scan(&mutationsBefore)).To(gomega.Succeed())
				gomega.Expect(reopened.QueryRow("SELECT COUNT(*) FROM managed_events").Scan(&eventsBefore)).To(gomega.Succeed())
				gomega.Expect(reopened.QueryRow("SELECT COUNT(*) FROM managed_runs").Scan(&runsBefore)).To(gomega.Succeed())
				envelope, err := c.RecoverJob(context.Background(), r.JobID, q)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				var replay managed.Receipt
				gomega.Expect(json.Unmarshal(envelope.Data, &replay)).To(gomega.Succeed())
				gomega.Expect(replay).To(gomega.Equal(original))
				changed := q
				changed.PlanToken = "different-token"
				_, err = c.RecoverJob(context.Background(), r.JobID, changed)
				gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("CONFLICT")))
				if q.Evidence != nil {
					evidence := *q.Evidence
					evidence.Reference += "-changed"
					changed = q
					changed.Evidence = &evidence
					_, err = c.RecoverJob(context.Background(), r.JobID, changed)
					gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("CONFLICT")))
				}
				inspection, _, err := c.Inspect(context.Background(), r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(inspection.Receipt.ResultStatus).To(gomega.Equal("unavailable"))
				gomega.Expect(inspection.Result).To(gomega.BeNil())
				_, _, err = c.Wait(context.Background(), r.JobID, time.Second)
				gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("RESULT_UNAVAILABLE")))
				_, _, err = c.Events(context.Background(), r.JobID, client.EventsOptions{})
				gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("RESULT_UNAVAILABLE")))
				_, _, err = c.Logs(context.Background(), r.JobID, client.LogsOptions{})
				gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("RESULT_UNAVAILABLE")))
				_, _, err = c.Artifacts(context.Background(), r.JobID)
				gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("RESULT_UNAVAILABLE")))
				private, err := ledger.BulkSnapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(private.Result).To(gomega.Equal(result))
				after, err := ledger.Snapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(after).To(gomega.Equal(before))
				resourceAfter, err := ledger.ResourceStatus(r.ResourceID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(resourceAfter).To(gomega.Equal(resourceBefore))
				for query, want := range map[string]int{
					"SELECT COUNT(*) FROM managed_mutations": mutationsBefore,
					"SELECT COUNT(*) FROM managed_events":    eventsBefore,
					"SELECT COUNT(*) FROM managed_runs":      runsBefore,
				} {
					var got int
					gomega.Expect(reopened.QueryRow(query).Scan(&got)).To(gomega.Succeed())
					gomega.Expect(got).To(gomega.Equal(want))
				}
			})
		}
	}
})
