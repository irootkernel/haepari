//go:build integration

package managed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// These requests retain the original fences, including after a lost reply.
func crashAvailabilityRequest(operation string) (AvailabilityRequest, bool) {
	q := AvailabilityRequest{ResourceID: "local-a", MutationKey: operation,
		ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, ExpectedLeaseGeneration: 0,
		Reason: "maintenance"}
	resume := operation == "availability-resume"
	if resume {
		q.ExpectedAvailabilityRevision = 2
		q.Reason = ""
		q.ReadinessDigest = digest
		q.ObservationReference = "isolated-crash-local-ready"
	}
	return q, resume
}

func crashManualHoldRequest() ManualHoldRequest {
	return ManualHoldRequest{JobID: "crash-job", ExpectedJobRevision: 3,
		ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: 0, LeaseGeneration: 1,
		EnvironmentDigest: digest, ReadinessDigest: digest,
		ObservationReference: "isolated-crash-manual-ready", MutationKey: "manual-hold"}
}

func crashSchedulingOperation(l *Ledger, operation string) (Receipt, error) {
	if operation == "manual-hold" {
		return l.HoldManualCapacity(crashManualHoldRequest())
	}
	q, resume := crashAvailabilityRequest(operation)
	if _, err := l.SetAvailability(q, resume); err != nil {
		return Receipt{}, err
	}
	return l.Snapshot("crash-job")
}

type schedulingCrashMutation struct {
	Key, Digest string
	Receipt     []byte
}

type schedulingCrashAvailability struct {
	Sequence, RecordedNS int64
	Key, ResourceID      string
	Request, Status      []byte
}

type schedulingCrashState struct {
	Resource     ResourceStatus
	Job          Receipt
	DeadlineNS   int64
	Spec         []byte
	Events       []Event
	Counts       map[string]int
	Mutations    []schedulingCrashMutation
	Availability []schedulingCrashAvailability
}

func readSchedulingCrashState(t ginkgo.GinkgoTInterface, db *sql.DB, l *Ledger) schedulingCrashState {
	t.Helper()
	var s schedulingCrashState
	var err error
	s.Resource, err = l.ResourceStatus("local-a")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	s.Job, err = l.Snapshot("crash-job")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(db.QueryRow(`SELECT queue_deadline_ns,spec_json FROM managed_jobs WHERE id='crash-job'`).Scan(&s.DeadlineNS, &s.Spec)).To(gomega.Succeed())
	s.Counts = make(map[string]int)
	for _, table := range []string{"managed_jobs", "managed_admissions", "managed_job_inputs", "managed_events", "managed_event_owners", "managed_event_progress", "managed_mutations", "managed_availability_receipts", "managed_parent_nonces", "managed_runs", "managed_power_transitions"} {
		var count int
		gomega.Expect(db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)).To(gomega.Succeed())
		s.Counts[table] = count
	}
	rows, err := db.Query(`SELECT cursor,job_id,kind,payload_json FROM managed_events ORDER BY cursor`)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for rows.Next() {
		var event Event
		gomega.Expect(rows.Scan(&event.Cursor, &event.JobID, &event.Kind, &event.Payload)).To(gomega.Succeed())
		s.Events = append(s.Events, event)
	}
	gomega.Expect(rows.Err()).NotTo(gomega.HaveOccurred())
	gomega.Expect(rows.Close()).To(gomega.Succeed())
	for _, event := range s.Events {
		var owner string
		gomega.Expect(db.QueryRow(`SELECT job_id FROM managed_event_owners WHERE cursor=?`, event.Cursor).Scan(&owner)).To(gomega.Succeed())
		gomega.Expect(owner).To(gomega.Equal(event.JobID))
	}
	rows, err = db.Query(`SELECT mutation_key,digest,receipt_json FROM managed_mutations ORDER BY mutation_key`)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for rows.Next() {
		var row schedulingCrashMutation
		gomega.Expect(rows.Scan(&row.Key, &row.Digest, &row.Receipt)).To(gomega.Succeed())
		s.Mutations = append(s.Mutations, row)
	}
	gomega.Expect(rows.Err()).NotTo(gomega.HaveOccurred())
	gomega.Expect(rows.Close()).To(gomega.Succeed())
	rows, err = db.Query(`SELECT sequence,mutation_key,resource_id,request_json,status_json,recorded_at_ns FROM managed_availability_receipts ORDER BY sequence`)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for rows.Next() {
		var row schedulingCrashAvailability
		gomega.Expect(rows.Scan(&row.Sequence, &row.Key, &row.ResourceID, &row.Request, &row.Status, &row.RecordedNS)).To(gomega.Succeed())
		s.Availability = append(s.Availability, row)
	}
	gomega.Expect(rows.Err()).NotTo(gomega.HaveOccurred())
	gomega.Expect(rows.Close()).To(gomega.Succeed())
	var results int
	gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs WHERE result_json IS NOT NULL`).Scan(&results)).To(gomega.Succeed())
	gomega.Expect(results).To(gomega.BeZero())
	return s
}

// TestMain installs the production commit barrier only after opening the store.
// Every fixture commit is therefore complete before the child is armed.
var _ = ginkgo.Describe("Task007SchedulingCrash", ginkgo.Label("integration"), func() {
	for _, operation := range []string{"availability-pause", "availability-resume", "manual-hold"} {
		for _, boundary := range []string{"before_commit", "after_commit"} {
			ginkgo.It(operation+" "+boundary, func() {
				t := ginkgo.GinkgoT()
				path := filepath.Join(t.TempDir(), "state.sqlite")
				db, l := openLedger(t, path, 1)
				resource, job := validResource(), validJob()
				job.Deadlines.QueueSeconds = 600
				if operation == "manual-hold" {
					resource.Capacity = 2
					job.Mode = "manual"
					job.Manual = &Manual{ScenarioDocument: "scenarios/check.md", RequiredScenarios: []string{"one"}}
					job.Deadlines.RunSeconds = 0
					job.Deadlines.ManualSeconds = 60
				}
				gomega.Expect(l.RegisterResource(encode(t, resource))).To(gomega.Succeed())
				_, err := l.Admit(encode(t, job), digest, "crash-request", "crash-job")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				if operation == "manual-hold" {
					_, err = l.Reserve(ReserveRequest{JobID: "crash-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "setup-reserve"})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					_, err = l.AdvanceLocal("crash-job", 2, "preparing", "ready", "setup-ready")
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				if operation == "availability-resume" {
					q, _ := crashAvailabilityRequest("availability-pause")
					q.MutationKey = "setup-pause"
					_, err = l.SetAvailability(q, false)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				before := readSchedulingCrashState(t, db, l)
				gomega.Expect(before.Resource.ConfigRevision).To(gomega.Equal(int64(1)))
				gomega.Expect(before.Job.ResourceConfigRevision).To(gomega.Equal(int64(1)))
				gomega.Expect(before.Counts["managed_runs"]).To(gomega.BeZero())
				if operation == "manual-hold" {
					gomega.Expect(before.Job.JobState).To(gomega.Equal("ready"))
					gomega.Expect(before.Job.JobRevision).To(gomega.Equal(int64(3)))
					gomega.Expect(before.Job.Slot).To(gomega.BeZero())
					gomega.Expect(before.Job.LeaseGeneration).To(gomega.Equal(int64(1)))
					gomega.Expect(before.Resource.LeaseGeneration).To(gomega.Equal(int64(1)))
					gomega.Expect(before.Resource.Occupancy).To(gomega.Equal(1))
					gomega.Expect(before.Job.GlobalOccupancy).To(gomega.Equal(1))
				} else {
					gomega.Expect(before.Job.JobState).To(gomega.Equal("queued"))
					gomega.Expect(before.Job.JobRevision).To(gomega.Equal(int64(1)))
					gomega.Expect(before.Job.Slot).To(gomega.Equal(-1))
					gomega.Expect(before.Resource.LeaseGeneration).To(gomega.BeZero())
					gomega.Expect(before.Resource.Occupancy).To(gomega.BeZero())
					gomega.Expect(before.Job.GlobalOccupancy).To(gomega.BeZero())
				}
				if operation == "availability-resume" {
					gomega.Expect(before.Resource.AvailabilityRevision).To(gomega.Equal(int64(2)))
					gomega.Expect(before.Resource.Availability).To(gomega.Equal("paused"))
					gomega.Expect(before.Resource.Reason).To(gomega.Equal("maintenance"))
				} else {
					gomega.Expect(before.Resource.AvailabilityRevision).To(gomega.Equal(int64(1)))
					gomega.Expect(before.Resource.Availability).To(gomega.Equal("available"))
				}
				gomega.Expect(db.Close()).To(gomega.Succeed())
				executable, err := os.Executable()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				child := exec.Command(executable)
				child.Env = append(os.Environ(), "HAEPARI_STORAGE_PROBE="+path, "HAEPARI_STORAGE_BOUNDARY="+boundary, "HAEPARI_STORAGE_OPERATION="+operation)
				output, err := os.OpenFile(path+".output", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				defer output.Close()
				child.Stdout, child.Stderr = output, output
				startedAt := time.Now().UTC()
				gomega.Expect(child.Start()).To(gomega.Succeed())
				waited := false
				t.Cleanup(func() {
					if !waited {
						_ = child.Process.Kill()
						_ = child.Wait()
					}
				})
				ready := false
				for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
					if marker, err := os.ReadFile(path + ".barrier"); err == nil && string(marker) == boundary {
						ready = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !ready {
					log, _ := os.ReadFile(path + ".output")
					t.Fatalf("child never reached %s: %s", boundary, log)
				}
				barrierAt := time.Now().UTC()
				gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
				gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
				waited = true
				status, ok := child.ProcessState.Sys().(syscall.WaitStatus)
				gomega.Expect(ok).To(gomega.BeTrue())
				gomega.Expect(status.Signaled()).To(gomega.BeTrue())
				gomega.Expect(status.Signal()).To(gomega.Equal(syscall.SIGKILL))
				db, l = openLedger(t, path, 1)
				defer db.Close()
				after := readSchedulingCrashState(t, db, l)
				if boundary == "before_commit" {
					gomega.Expect(after).To(gomega.Equal(before))
				} else {
					assertSchedulingCrashCommit(t, db, l, operation, before, after, startedAt, barrierAt)
					// Replay the original request, not fences reconstructed from current state.
					gomega.Expect(readSchedulingCrashState(t, db, l)).To(gomega.Equal(after))
				}
				if operation == "manual-hold" {
					gomega.Expect(after.Job.ResourceOccupancy).To(gomega.Equal(1))
					gomega.Expect(after.Job.GlobalOccupancy).To(gomega.Equal(1))
					competitor := admitScheduling(t, l, "local-a", "crash-competitor")
					_, err = reserveScheduling(l, competitor, nil)
					gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
				}
				var integrity string
				gomega.Expect(db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity)).To(gomega.Succeed())
				gomega.Expect(integrity).To(gomega.Equal("ok"))
				rows, err := db.Query(`PRAGMA foreign_key_check`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(rows.Next()).To(gomega.BeFalse())
				gomega.Expect(rows.Err()).NotTo(gomega.HaveOccurred())
				gomega.Expect(rows.Close()).To(gomega.Succeed())
			})
		}
	}
})

func assertSchedulingCrashCommit(t ginkgo.GinkgoTInterface, db *sql.DB, l *Ledger, operation string, before, after schedulingCrashState, startedAt, barrierAt time.Time) {
	t.Helper()
	wantJob, wantResource := before.Job, before.Resource
	addedReceipt := "managed_availability_receipts"
	if operation == "manual-hold" {
		wantJob.JobState = "awaiting_manual"
		wantJob.JobRevision = 4
		addedReceipt = "managed_mutations"
	} else {
		wantResource.AvailabilityRevision++
		wantResource.Availability, wantResource.Reason = "paused", "maintenance"
		if operation == "availability-resume" {
			wantResource.Availability, wantResource.Reason = "available", ""
		}
		wantJob.ResourceAvailabilityRevision = wantResource.AvailabilityRevision
		wantJob.ResourceAvailability = wantResource.Availability
	}
	gomega.Expect(after.Resource).To(gomega.Equal(wantResource))
	gomega.Expect(after.Job).To(gomega.Equal(wantJob))
	gomega.Expect(after.DeadlineNS).To(gomega.Equal(before.DeadlineNS))
	gomega.Expect(after.Spec).To(gomega.Equal(before.Spec))
	for table, count := range before.Counts {
		if table == "managed_events" || table == "managed_event_owners" || table == addedReceipt {
			count++
		}
		gomega.Expect(after.Counts[table]).To(gomega.Equal(count), table)
	}
	gomega.Expect(after.Events).To(gomega.HaveLen(len(before.Events) + 1))
	gomega.Expect(after.Events[:len(before.Events)]).To(gomega.Equal(before.Events))
	event := after.Events[len(before.Events)]
	gomega.Expect(event.JobID).To(gomega.Equal("crash-job"))
	gomega.Expect(event.Cursor).To(gomega.BeNumerically(">", before.Events[len(before.Events)-1].Cursor))
	if operation == "manual-hold" {
		gomega.Expect(after.Availability).To(gomega.Equal(before.Availability))
		for _, row := range before.Mutations {
			gomega.Expect(after.Mutations).To(gomega.ContainElement(row))
		}
		gomega.Expect(event.Kind).To(gomega.Equal("awaiting_manual"))
		var facts struct {
			LeaseGeneration      int64    `json:"lease_generation"`
			EnvironmentDigest    string   `json:"environment_digest"`
			ReadinessDigest      string   `json:"readiness_digest"`
			ObservationReference string   `json:"observation_reference"`
			ScenarioDocument     string   `json:"scenario_document"`
			RequiredScenarios    []string `json:"required_scenarios"`
			ManualDeadline       string   `json:"manual_deadline"`
		}
		gomega.Expect(json.Unmarshal(event.Payload, &facts)).To(gomega.Succeed())
		gomega.Expect(facts.LeaseGeneration).To(gomega.Equal(int64(1)))
		gomega.Expect(facts.EnvironmentDigest).To(gomega.Equal(digest))
		gomega.Expect(facts.ReadinessDigest).To(gomega.Equal(digest))
		gomega.Expect(facts.ObservationReference).To(gomega.Equal("isolated-crash-manual-ready"))
		gomega.Expect(facts.ScenarioDocument).To(gomega.Equal("scenarios/check.md"))
		gomega.Expect(facts.RequiredScenarios).To(gomega.Equal([]string{"one"}))
		deadline, err := time.Parse(time.RFC3339Nano, facts.ManualDeadline)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(facts.ManualDeadline).To(gomega.Equal(deadline.UTC().Format(time.RFC3339Nano)))
		gomega.Expect(deadline.Before(startedAt.Add(60 * time.Second))).To(gomega.BeFalse())
		gomega.Expect(deadline.After(barrierAt.Add(60 * time.Second))).To(gomega.BeFalse())
		var stored []byte
		gomega.Expect(db.QueryRow(`SELECT receipt_json FROM managed_mutations WHERE mutation_key='manual-hold'`).Scan(&stored)).To(gomega.Succeed())
		var original Receipt
		gomega.Expect(json.Unmarshal(stored, &original)).To(gomega.Succeed())
		wantJob.EventCursor = event.Cursor
		gomega.Expect(original).To(gomega.Equal(wantJob))
		replay, err := l.HoldManualCapacity(crashManualHoldRequest())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(original))
	} else {
		gomega.Expect(after.Mutations).To(gomega.Equal(before.Mutations))
		for i, row := range before.Availability {
			gomega.Expect(after.Availability[i]).To(gomega.Equal(row))
		}
		row := after.Availability[len(before.Availability)]
		q, resume := crashAvailabilityRequest(operation)
		gomega.Expect(row.Key).To(gomega.Equal(q.MutationKey))
		gomega.Expect(row.ResourceID).To(gomega.Equal(q.ResourceID))
		var request struct {
			Resume  bool
			Request AvailabilityRequest
		}
		gomega.Expect(json.Unmarshal(row.Request, &request)).To(gomega.Succeed())
		gomega.Expect(request.Resume).To(gomega.Equal(resume))
		gomega.Expect(request.Request).To(gomega.Equal(q))
		gomega.Expect(row.RecordedNS).To(gomega.BeNumerically(">=", startedAt.UnixNano()))
		gomega.Expect(row.RecordedNS).To(gomega.BeNumerically("<=", barrierAt.UnixNano()))
		var original ResourceStatus
		gomega.Expect(json.Unmarshal(row.Status, &original)).To(gomega.Succeed())
		wantResource.Mutation = &AvailabilityIdentity{q.MutationKey, wantResource.AvailabilityRevision}
		gomega.Expect(original).To(gomega.Equal(wantResource))
		gomega.Expect(event.Kind).To(gomega.Equal("resource_availability"))
		var facts map[string]string
		gomega.Expect(json.Unmarshal(event.Payload, &facts)).To(gomega.Succeed())
		gomega.Expect(facts).To(gomega.Equal(map[string]string{"availability": wantResource.Availability, "reason": wantResource.Reason, "readiness_digest": q.ReadinessDigest, "observation_reference": q.ObservationReference}))
		replay, err := l.SetAvailability(q, resume)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(original))
	}
}
