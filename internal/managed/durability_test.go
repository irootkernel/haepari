//go:build integration

package managed

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// The child stops inside the production transaction path. Killing it tests
// SQLite recovery rather than simulating a rollback or a successful reply.
func TestMain(m *testing.M) {
	if path := os.Getenv("HAEPARI_STORAGE_PROBE"); path != "" {
		db, err := sqlite.Open(path)
		if err != nil {
			panic(err)
		}
		ledger, err := NewLedger(db, 1)
		if err != nil {
			panic(err)
		}
		ledger.UseLegacyFixtureBounds()
		ledger.transactionBoundary = func(boundary string) {
			if boundary == os.Getenv("HAEPARI_STORAGE_BOUNDARY") {
				if err := os.WriteFile(path+".barrier", []byte(boundary), 0600); err != nil {
					panic(err)
				}
				for {
					time.Sleep(time.Hour)
				}
			}
		}
		job := validJob()
		job.Deadlines.QueueSeconds = 600
		data, err := json.Marshal(job)
		if err != nil {
			panic(err)
		}
		operation := os.Getenv("HAEPARI_STORAGE_OPERATION")
		var revision int64
		if operation == "settle" {
			revision, err = strconv.ParseInt(os.Getenv("HAEPARI_STORAGE_REVISION"), 10, 64)
			if err != nil {
				panic(err)
			}
		}
		_, err = crashOperation(ledger, operation, data, revision)
		if err != nil {
			panic(err)
		}
		panic("probe did not stop at commit")
	}
	os.Exit(m.Run())
}

var _ = ginkgo.Describe("CrashTransactionBoundaries", ginkgo.Label("integration"), func() {
	for _, operation := range []string{"admit", "reserve", "cancel", "advance", "start", "settle", "interrupt"} {
		for _, boundary := range []string{"before_commit", "after_commit"} {
			ginkgo.It(operation+" "+boundary, func() {
				t := ginkgo.GinkgoT()
				path := filepath.Join(t.TempDir(), "state.sqlite")
				db, l := openLedger(t, path, 1)
				gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
				job := validJob()
				job.Deadlines.QueueSeconds = 600
				data := encode(t, job)
				var settleRevision int64
				expectedPhases := []ResultPhase{}
				if operation != "admit" {
					_, err := l.Admit(data, digest, "crash-request", "crash-job")
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}

				if operation == "advance" || operation == "start" || operation == "settle" || operation == "interrupt" {
					_, err := l.Reserve(ReserveRequest{JobID: "crash-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "setup-reserve"})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				if operation == "start" || operation == "settle" {
					_, err := l.AdvanceLocal("crash-job", 2, "preparing", "ready", "setup-ready")
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				if operation == "settle" {
					_, err := l.StartLocal("crash-job", 3, "crash-run")
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					id, err := l.LocalIdentity("crash-job")
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					_, err = l.RecordLocalOutcome(LocalOutcomeRequest{Identity: id, ExpectedRevision: 4, Outcome: LocalOutcome{Started: true, WorkloadOutcome: "pass"}, MutationKey: "setup-outcome"})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					r, err := l.CompleteLocalRun(LocalTerminationRequest{Identity: id, ExpectedRevision: 5, MutationKey: "setup-proof"})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					// Durable phase observations precede the terminal transaction;
					// a killed commit must preserve their exact timing projection.
					for _, phase := range []string{"collect", "cleanup", "verify"} {
						now := time.Now().UTC()
						p := LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: phase, BudgetSeconds: 10, StartedAt: now, Deadline: now.Add(10 * time.Second), ObservedAt: now}
						if phase == "verify" {
							p.BudgetSeconds, p.Deadline = 0, time.Time{}
						}
						r, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "setup-intent-" + phase})
						gomega.Expect(err).NotTo(gomega.HaveOccurred())
						p.Completed, p.Outcome, p.ObservedAt = true, "complete", time.Now().UTC()
						if phase == "collect" {
							p.Artifacts, p.EvidenceCompleteness = crashResult().Artifacts, "complete"
						}
						r, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "setup-observed-" + phase})
						gomega.Expect(err).NotTo(gomega.HaveOccurred())
						expectedPhases = append(expectedPhases, ResultPhase{Phase: phase, StartedAt: p.StartedAt.Format(time.RFC3339Nano), EndedAt: p.ObservedAt.Format(time.RFC3339Nano)})
						if phase != "verify" {
							from, to := "collecting", "cleaning"
							if phase == "cleanup" {
								from, to = "cleaning", "settling"
							}
							r, err = l.AdvanceLocal("crash-job", r.JobRevision, from, to, "setup-"+to)
							gomega.Expect(err).NotTo(gomega.HaveOccurred())
						}
					}
					settleRevision = r.JobRevision
				}
				var baselineMutations, baselineEvents int
				gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations`).Scan(&baselineMutations)).To(gomega.Succeed())
				gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&baselineEvents)).To(gomega.Succeed())
				gomega.Expect(db.Close()).To(gomega.Succeed())
				executable, err := os.Executable()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				child := exec.Command(executable)
				child.Env = append(os.Environ(), "HAEPARI_STORAGE_PROBE="+path, "HAEPARI_STORAGE_BOUNDARY="+boundary, "HAEPARI_STORAGE_OPERATION="+operation, "HAEPARI_STORAGE_REVISION="+strconv.FormatInt(settleRevision, 10))
				output, err := os.Create(path + ".output")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				defer output.Close()
				child.Stdout = output
				child.Stderr = output
				gomega.Expect(child.Start()).To(gomega.Succeed())
				waited := false
				t.Cleanup(func() {
					if !waited {
						_ = child.Process.Kill()
						_ = child.Wait()
					}
				})
				ready := false
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(path + ".barrier"); err == nil {
						ready = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !ready {
					log, _ := os.ReadFile(path + ".output")
					t.Fatalf("child never reached %s: %s", boundary, log)
				}
				gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
				gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
				waited = true
				db, l = openLedger(t, path, 1)
				defer db.Close()
				receipt, err := l.Snapshot("crash-job")
				if operation == "admit" && boundary == "before_commit" {
					gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeTrue())
					for _, table := range []string{"managed_admissions", "managed_jobs", "managed_job_inputs", "managed_events", "managed_event_owners", "managed_event_progress"} {
						var count int
						gomega.Expect(db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)).To(gomega.Succeed())
						gomega.Expect(count).To(gomega.BeZero())
					}
					_, err = l.Admit(data, digest, "crash-request", "replacement-job")
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return
				}
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				state, occupancy := "queued", 0
				switch operation {
				case "advance", "interrupt":
					state, occupancy = "preparing", 1
				case "start":
					state, occupancy = "ready", 1
				case "settle":
					state, occupancy = "settling", 1
				}
				if boundary == "after_commit" {
					switch operation {
					case "reserve":
						state, occupancy = "preparing", 1
					case "cancel", "settle":
						state, occupancy = "finished", 0
					case "advance":
						state = "ready"
					case "start":
						state = "running"
					case "interrupt":
						state = "reconciling"
					}
				}

				gomega.Expect(receipt.JobState).To(gomega.Equal(state))
				gomega.Expect(receipt.GlobalOccupancy).To(gomega.Equal(occupancy))
				gomega.Expect(receipt.ResourceOccupancy).To(gomega.Equal(occupancy))
				var mutations int
				gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations`).Scan(&mutations)).To(gomega.Succeed())
				expected := baselineMutations
				if boundary == "after_commit" && operation != "admit" && operation != "interrupt" {
					expected++
				}
				gomega.Expect(mutations).To(gomega.Equal(expected))
				if boundary == "after_commit" {
					var replay Receipt
					replay, err = crashOperation(l, operation, data, settleRevision)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(replay.JobID).To(gomega.Equal(receipt.JobID))
					gomega.Expect(replay.JobRevision).To(gomega.Equal(receipt.JobRevision))
					gomega.Expect(replay.JobState).To(gomega.Equal(receipt.JobState))
					gomega.Expect(replay.GlobalOccupancy).To(gomega.Equal(receipt.GlobalOccupancy))
					if operation != "admit" && operation != "interrupt" {
						var stored []byte
						gomega.Expect(db.QueryRow(`SELECT receipt_json FROM managed_mutations WHERE mutation_key=?`, crashMutationKey(operation)).Scan(&stored)).To(gomega.Succeed())
						var original Receipt
						gomega.Expect(json.Unmarshal(stored, &original)).To(gomega.Succeed())
						gomega.Expect(replay).To(gomega.Equal(original))
						gomega.Expect(replay.EventCursor).To(gomega.BeNumerically(">", 0))
					}
				}

				var events int
				gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&events)).To(gomega.Succeed())
				expectedEvents := baselineEvents
				if boundary == "after_commit" {
					expectedEvents++
				}
				gomega.Expect(events).To(gomega.Equal(expectedEvents))
				if operation == "start" {
					var runs int
					gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs`).Scan(&runs)).To(gomega.Succeed())
					expectedRuns := 0
					if boundary == "after_commit" {
						expectedRuns = 1
					}
					gomega.Expect(runs).To(gomega.Equal(expectedRuns))
				}
				if operation == "settle" {
					result, err := l.Result("crash-job")
					if boundary == "after_commit" {
						gomega.Expect(err).NotTo(gomega.HaveOccurred())
						expected := crashResult()
						expected.Timings = result.Timings
						gomega.Expect(result).To(gomega.Equal(expected))
						gomega.Expect(result.Timings.AdmittedAt).NotTo(gomega.BeEmpty())
						gomega.Expect(result.Timings.ReservedAt).NotTo(gomega.BeEmpty())
						gomega.Expect(result.Timings.Phases).To(gomega.Equal(expectedPhases))
						gomega.Expect(result.Timings.AdapterSteps).To(gomega.Equal([]ResultStep{}))
					} else {
						gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
					}
				}
				if operation == "interrupt" {
					availability := "available"
					if boundary == "after_commit" {
						availability = "quarantined"
					}
					gomega.Expect(receipt.ResourceAvailability).To(gomega.Equal(availability))
				}
				var integrity string
				gomega.Expect(db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity)).To(gomega.Succeed())
				gomega.Expect(integrity).To(gomega.Equal("ok"))
				rows, err := db.Query(`PRAGMA foreign_key_check`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				defer rows.Close()
				gomega.Expect(rows.Next()).To(gomega.BeFalse())
			})
		}
	}
	ginkgo.It("enforces input foreign keys", func() {
		db, _ := openLedger(ginkgo.GinkgoT(), filepath.Join(ginkgo.GinkgoT().TempDir(), "state.sqlite"), 1)
		defer db.Close()
		_, err := db.Exec(`INSERT INTO managed_job_inputs(job_id,sealed_digest,object_id) VALUES('missing',?,?)`, digest, digest)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})

func crashMutationKey(operation string) string {
	switch operation {
	case "start":
		return "start-crash-job"
	case "settle":
		return "settle-crash-job"
	default:
		return "crash-mutation"
	}
}

func crashResult() Result {
	return Result{Schema: "haepari.result/v1", JobID: "crash-job", ResourceID: "local-a", ResourceRevision: 1, LeaseGeneration: 1, JobState: "finished", ResourceAvailability: "available", WorkloadOutcome: "pass", EvidenceCompleteness: "complete", CleanupOutcome: "verified", FinalVerdict: Pass, Artifacts: []ResultArtifact{{Path: "results/build.json", SizeBytes: 1, SHA256: digest}}}
}

func crashOperation(l *Ledger, operation string, data []byte, revision int64) (Receipt, error) {
	if strings.HasPrefix(operation, "vm-") {
		return Receipt{}, crashPowerOperation(l, operation)
	}
	switch operation {
	case "availability-pause", "availability-resume", "manual-hold":
		return crashSchedulingOperation(l, operation)
	case "admit":
		return l.Admit(data, digest, "crash-request", "crash-job")
	case "reserve":
		return l.Reserve(ReserveRequest{JobID: "crash-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "crash-mutation"})
	case "cancel":
		return l.CancelQueued("crash-job", 1, "crash-mutation")
	case "advance":
		return l.AdvanceLocal("crash-job", 2, "preparing", "ready", "crash-mutation")
	case "start":
		return l.StartLocal("crash-job", 3, "crash-run")
	case "settle":
		return l.SettleLocal("crash-job", revision, crashResult())
	case "interrupt":
		if err := l.MarkInterrupted(); err != nil {
			return Receipt{}, err
		}
		return l.Snapshot("crash-job")
	default:
		panic("unknown crash operation")
	}
}
