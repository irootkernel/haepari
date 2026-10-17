//go:build integration

package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func cancellationRuntime(t ginkgo.GinkgoTInterface, script string, options ...func(*managed.JobSpec)) (*sql.DB, *runtimeServer, managed.Receipt, managed.JobSpec, string) {
	return cancellationRuntimeKind(t, script, "local", options...)
}

func cancellationRuntimeKind(t ginkgo.GinkgoTInterface, script, kind string, options ...func(*managed.JobSpec)) (*sql.DB, *runtimeServer, managed.Receipt, managed.JobSpec, string) {
	return cancellationRuntimeJobKind(t, script, kind, "cancel-job", options...)
}

func cancellationRuntimeJobKind(t ginkgo.GinkgoTInterface, script, kind, jobID string, options ...func(*managed.JobSpec)) (*sql.DB, *runtimeServer, managed.Receipt, managed.JobSpec, string) {
	t.Helper()
	root, err := state.Resolve(filepath.Join(t.TempDir(), "state"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(root.Prepare()).To(gomega.Succeed())
	gomega.Expect(os.Mkdir(filepath.Join(root.Path, "jobs"), 0700)).To(gomega.Succeed())
	db, err := sqlite.Open(root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(func() { _ = db.Close() })
	l, err := managed.NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	l.UseLegacyFixtureBounds()
	store, err := snapshot.OpenStore(root.Path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	s, err := newRuntime(root, l, store)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-cancel", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-development"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
	if kind == "utm-macos" {
		resource.Kind = kind
		resource.Cleanup = nil
		resource.Adapter.UTMCtl = "/fixture/utmctl-not-invoked"
		resource.VM = &managed.VM{UTMVMUUID: fakeGuestUUID, MACAddress: "aa:bb:cc:dd:ee:ff", AddressDiscovery: "dhcp-lease-by-mac", GuestJobRoot: "/var/haepari/jobs"}
		resource.Guest = &managed.Guest{ConnectionReference: "fixture-ssh-not-invoked", KnownHostsReference: "fixture-key"}
	}
	raw, _ := json.Marshal(resource)
	gomega.Expect(l.RegisterResource(raw)).To(gomega.Succeed())
	source := filepath.Join(t.TempDir(), "input")
	gomega.Expect(os.MkdirAll(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(filepath.Join(source, "source/input.txt"), []byte("sealed"), 0600)).To(gomega.Succeed())
	sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	digest := strings.Repeat("a", 64)
	job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: resource.ID, Mode: "auto", Suite: "cancel", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt"}}, Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/sh", "-c", script}, CWD: "source", RequiredOutputs: []string{"results/output.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 60, PrepareSeconds: 5, RunSeconds: 10, CollectSeconds: 3, CleanupSeconds: 2}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "isolated-cancel"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
	for _, option := range options {
		option(&job)
	}
	raw, _ = json.Marshal(job)
	admitted, err := l.AdmitSealed(raw, job.Input.InventoryDigest, sealed.ObjectID, "cancel-key", jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	reserve := managed.ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: admitted.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-cancel"}
	if kind == "utm-macos" {
		revision, err := l.ObserveVM(managed.VMObservation{ResourceID: resource.ID, ConfigRevision: 1, VMUUID: fakeGuestUUID, Power: "started", EvidenceDigest: digest})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reserve.VMGuard = &managed.VMReservationGuard{HostLimit: 1, ObservationRevisions: map[string]int64{resource.ID: revision}}
	}
	r, err := l.Reserve(reserve)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return db, s, r, job, sealed.ObjectID
}

func cancellationBarrier(t ginkgo.GinkgoTInterface, s *runtimeServer, r managed.Receipt, job managed.JobSpec, objectID, phase string) (*localRunControl, func(), <-chan error) {
	t.Helper()
	c, err := s.registerLocal(r)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	c.beforePhase = func(p string) {
		if p == phase {
			close(reached)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() {
		err := s.executeLocalControlled(c, job, objectID, time.Now().Add(5*time.Second))
		s.unregisterLocal(c)
		done <- err
	}()
	t.Cleanup(unblock)
	select {
	case <-reached:
	case err := <-done:
		t.Fatalf("runner ended before %s barrier: %v", phase, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("runner missed %s barrier", phase)
	}
	return c, unblock, done
}
func awaitCancellation(t ginkgo.GinkgoTInterface, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	case <-time.After(6 * time.Second):
		t.Fatal("local cancellation did not finish within bounded phases")
	}
}

var _ = ginkgo.Describe("Task008CancellationController", ginkgo.Label("integration"), func() {
	ginkgo.It("preserves natural PASS and journals execution deadline separately from explicit cancellation", func() {
		t := ginkgo.GinkgoT()
		for _, deadline := range []bool{false, true} {
			script := "mkdir -p ../results; printf passed > ../results/output.txt"
			if deadline {
				script = "exec /bin/sleep 30"
			}
			db, s, r, job, object := cancellationRuntime(t, script, func(j *managed.JobSpec) {
				if deadline {
					j.Deadlines.RunSeconds = 1
				}
			})
			gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
			result, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if deadline {
				gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("timed_out")))
				gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			} else {
				gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
			}
			status, err := s.ledger.Cancellation(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(status.Requested).To(gomega.BeFalse())
			gomega.Expect(status.TerminationProven).To(gomega.BeTrue())
			var count int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE kind='execution_deadline'`).Scan(&count)).To(gomega.Succeed())
			if deadline {
				gomega.Expect(count).To(gomega.Equal(1))
			} else {
				gomega.Expect(count).To(gomega.BeZero())
			}
		}
	})
	ginkgo.It("publishes completion once while concurrent exact replays and new cancels observe termination", func() {
		t := ginkgo.GinkgoT()
		_, s, r, job, object := cancellationRuntime(t, "trap '' TERM; exec /bin/sleep 30")
		c, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		running, terminating := make(chan struct{}), make(chan struct{})
		runRelease, stopRelease := make(chan struct{}), make(chan struct{})
		var runOnce, stopOnce sync.Once
		unblockRun := func() { runOnce.Do(func() { close(runRelease) }) }
		unblockStop := func() { stopOnce.Do(func() { close(stopRelease) }) }
		t.Cleanup(unblockRun)
		t.Cleanup(unblockStop)
		c.beforePhase = func(phase string) {
			switch phase {
			case "running":
				close(running)
				<-runRelease
			case "termination_wait":
				close(terminating)
				<-stopRelease
			}
		}
		done := make(chan error, 1)
		go func() {
			done <- s.executeLocalControlled(c, job, object, time.Now().Add(5*time.Second))
			s.unregisterLocal(c)
		}()
		select {
		case <-running:
		case <-time.After(5 * time.Second):
			t.Fatal("running barrier missing")
		}
		current, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q := managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: "concurrent-cancel"}
		original, err := s.requestCancel(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		unblockRun()
		select {
		case <-terminating:
		case <-time.After(5 * time.Second):
			t.Fatal("termination barrier missing")
		}
		results := make(chan error, 12)
		for i := 0; i < 12; i++ {
			i := i
			go func() {
				if i%2 == 0 {
					replay, err := s.requestCancel(q)
					if err == nil && replay.Mutation != original.Mutation {
						err = errors.New("exact cancellation ACK changed")
					}
					results <- err
					return
				}
				current, err := s.ledger.Snapshot(r.JobID)
				if err == nil {
					_, err = s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: "additional-" + strconv.Itoa(i)})
				}
				if errors.Is(err, managed.ErrStale) {
					err = nil
				}
				results <- err
			}()
		}
		for i := 0; i < 12; i++ {
			select {
			case err := <-results:
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			case <-time.After(time.Second):
				t.Fatal("cancel blocked behind termination effect")
			}
		}
		unblockStop()
		awaitCancellation(t, done)
		gomega.Expect(c.nativeCompletion()).NotTo(gomega.BeNil())
		current, err = s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
	})
	for _, phase := range []string{"preparing", "ready", "collecting", "cleaning", "settling"} {
		phase := phase
		ginkgo.It("handles cancellation at deterministic "+phase+" barrier without repeating effects or stale quarantine", func() {
			t := ginkgo.GinkgoT()
			db, s, r, job, object := cancellationRuntime(t, "mkdir -p ../results; printf failure > ../results/output.txt; exit 7")
			_, release, done := cancellationBarrier(t, s, r, job, object, phase)
			current, err := s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			q := managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: "phase-cancel"}
			ack, err := s.requestCancel(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(ack.Receipt.GlobalOccupancy).To(gomega.Equal(1))
			early := phase == "preparing" || phase == "ready"
			gomega.Expect(ack.Cancellation.BeforeOutcome).To(gomega.Equal(early))
			replay, err := s.requestCancel(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(replay.Mutation).To(gomega.Equal(ack.Mutation))
			if !early {
				_, err = s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: ack.Receipt.JobRevision, MutationKey: "another-late-cancel"})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			release()
			awaitCancellation(t, done)
			final, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			expected := managed.Fail
			if early {
				expected = managed.Cancelled
			}
			gomega.Expect(final.FinalVerdict).To(gomega.Equal(expected))
			if early {
				gomega.Expect(final.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("cancelled")))
			} else {
				gomega.Expect(final.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("fail")))
				gomega.Expect(final.NativeExitCode).To(gomega.HaveValue(gomega.Equal(7)))
			}
			current, err = s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
			gomega.Expect(current.ResourceAvailability).To(gomega.Equal("available"))
			var runs int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs`).Scan(&runs)).To(gomega.Succeed())
			if early {
				gomega.Expect(runs).To(gomega.BeZero())
			} else {
				gomega.Expect(runs).To(gomega.Equal(1))
			}
			replay, err = s.requestCancel(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(replay.Mutation).To(gomega.Equal(ack.Mutation))
			gomega.Expect(replay.Receipt.JobState).To(gomega.Equal("finished"))
		})
	}
	ginkgo.It("registers after an already committed cancel and proves no owned effect without touching input or spawning", func() {
		t := ginkgo.GinkgoT()
		db, s, r, job, _ := cancellationRuntime(t, "exit 0")
		ack, err := s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: r.JobRevision, MutationKey: "before-registration"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ack.Cancellation.TerminationProven).To(gomega.BeFalse())
		// Passing an invalid object ID proves the durable recheck precedes store access.
		gomega.Expect(s.executeLocal(r, job, "not-an-object", time.Now().Add(time.Second))).To(gomega.Succeed())
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Cancelled))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("not_needed")))
		expectPhaseTimings(result, "prepare", "verify")
		_, err = os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
	})
	ginkgo.It("imports published native completion before deciding cancel, keeping failure and making the old public revision stale", func() {
		t := ginkgo.GinkgoT()
		_, s, r, _, _ := cancellationRuntime(t, "exit 7")
		r, err := s.ledger.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "ready-fixture")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		r, err = s.ledger.StartLocal(r.JobID, r.JobRevision, "run-fixture")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		c, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer s.unregisterLocal(c)
		c.identity.RunID = "run-fixture"
		code := 7
		c.publishCompletion(nativeCompletion{outcome: &managed.LocalOutcome{Started: true, WorkloadOutcome: "fail", NativeExitCode: &code}, at: time.Now()})
		_, err = s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: r.JobRevision, MutationKey: "native-first"})
		gomega.Expect(errors.Is(err, managed.ErrStale)).To(gomega.BeTrue())
		outcome, err := s.ledger.LocalOutcome(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(outcome.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("fail")))
		current, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ack, err := s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: "native-first-retry"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ack.Cancellation.BeforeOutcome).To(gomega.BeFalse())
		gomega.Expect(ack.Cancellation.TerminationProven).To(gomega.BeFalse())
	})
	ginkgo.It("bounds active cancellation, retains missing output evidence and quarantines after proven stop", func() {
		t := ginkgo.GinkgoT()
		_, s, r, job, object := cancellationRuntime(t, "printf started > started.txt; exec /bin/sleep 30")
		_, release, done := cancellationBarrier(t, s, r, job, object, "running")
		gomega.Eventually(func() string {
			data, _ := os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace/source/started.txt"))
			return string(data)
		}, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal("started"))
		current, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ack, err := s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: "active-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ack.Receipt.JobState).To(gomega.Equal("cancelling"))
		gomega.Expect(ack.Cancellation.TerminationProven).To(gomega.BeFalse())
		gomega.Expect(ack.Receipt.GlobalOccupancy).To(gomega.Equal(1))
		release()
		awaitCancellation(t, done)
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("cancelled")))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
		current, err = s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
		gomega.Expect(current.ResourceAvailability).To(gomega.Equal("quarantined"))
		_, err = os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace", "source", "input.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
	ginkgo.It("validates cancellation route identity and returns an ACK independent of terminal success", func() {
		t := ginkgo.GinkgoT()
		_, s, r, _, _ := cancellationRuntime(t, "exit 0")
		for _, id := range []string{"wrong-job", r.JobID} {
			raw, _ := json.Marshal(managed.CancelRequest{JobID: id, ExpectedRevision: r.JobRevision, MutationKey: "route-cancel"})
			request := httptest.NewRequest("POST", "/v1/jobs/"+r.JobID+"/cancel", strings.NewReader(string(raw)))
			request.Header.Set("X-Haepari-Protocol", protocol)
			request.Header.Set("X-Haepari-Protocol-Min", "1")
			request.Header.Set("X-Haepari-Protocol-Max", "1")
			w := httptest.NewRecorder()
			s.routes().ServeHTTP(w, request)
			if id == "wrong-job" {
				gomega.Expect(w.Code).To(gomega.Equal(400))
			} else {
				gomega.Expect(w.Code).To(gomega.Equal(200))
				var envelope struct{ Data managed.CancelReceipt }
				gomega.Expect(json.Unmarshal(w.Body.Bytes(), &envelope)).To(gomega.Succeed())
				gomega.Expect(envelope.Data.Cancellation.Requested).To(gomega.BeTrue())
				gomega.Expect(envelope.Data.Cancellation.TerminationProven).To(gomega.BeFalse())
			}
		}
	})
})

var _ = ginkgo.Describe("Task008CancellationMutationNamespace", ginkgo.Label("integration"), func() {
	for _, phase := range []string{"preparing", "ready", "running"} {
		phase := phase
		ginkgo.It("settles CANCELLED without quarantine when public keys use runner prefixes at the "+phase+" barrier", func() {
			t := ginkgo.GinkgoT()
			db, s, r, job, object := cancellationRuntime(t, "mkdir -p ../results; printf ready > ../results/output.txt; exec /bin/sleep 30")
			_, release, done := cancellationBarrier(t, s, r, job, object, phase)
			if phase == "running" {
				// The runner remains at the barrier and the child stays alive.
				// Observe its actual required output before requesting cancellation.
				output := filepath.Join(s.root.Path, "jobs", r.JobID, "workspace", "results", "output.txt")
				gomega.Eventually(func() string {
					data, _ := os.ReadFile(output)
					return string(data)
				}, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal("ready"))
			}
			var requests []managed.CancelRequest
			var mutations []managed.Receipt
			for _, prefix := range []string{"outcome-", "termination-", "settle-", "start-"} {
				current, err := s.ledger.Snapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				q := managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: prefix + r.JobID}
				ack, err := s.requestCancel(q)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(ack.Cancellation.MutationKey).To(gomega.Equal("outcome-" + r.JobID))
				gomega.Expect(ack.Cancellation.BeforeOutcome).To(gomega.BeTrue())
				gomega.Expect(ack.Receipt.GlobalOccupancy).To(gomega.Equal(1))
				gomega.Expect(ack.Cancellation.TerminationProven).To(gomega.BeFalse())
				replay, err := s.requestCancel(q)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(replay.Mutation).To(gomega.Equal(ack.Mutation))
				changed := q
				changed.ExpectedRevision++
				_, err = s.requestCancel(changed)
				gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
				requests = append(requests, q)
				mutations = append(mutations, ack.Mutation)
			}
			release()
			awaitCancellation(t, done)
			result, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("cancelled")))
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Cancelled))
			gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
			gomega.Expect(result.ProtectedEvidence).To(gomega.BeEmpty())
			current, err := s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current.JobState).To(gomega.Equal("finished"))
			gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
			for i, q := range requests {
				replay, err := s.requestCancel(q)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(replay.Mutation).To(gomega.Equal(mutations[i]))
				gomega.Expect(replay.Receipt.JobRevision).To(gomega.Equal(current.JobRevision))
				changed := q
				changed.ExpectedRevision++
				_, err = s.requestCancel(changed)
				gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
				var count int
				gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations WHERE mutation_key=?`, "cancel:"+q.MutationKey).Scan(&count)).To(gomega.Succeed())
				gomega.Expect(count).To(gomega.Equal(1))
				gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations WHERE mutation_key=?`, q.MutationKey).Scan(&count)).To(gomega.Succeed())
				if phase != "running" && q.MutationKey != "settle-"+r.JobID {
					gomega.Expect(count).To(gomega.BeZero())
				} else {
					gomega.Expect(count).To(gomega.Equal(1))
				}
			}
			var count int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, r.JobID).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.Equal(1))
		})
	}
	for _, exit := range []int{0, 7} {
		exit := exit
		ginkgo.It("preserves a natural terminal verdict and internal settlement receipt when the late cancel key is settle-jobID: exit "+strconv.Itoa(exit), func() {
			t := ginkgo.GinkgoT()
			db, s, r, job, object := cancellationRuntime(t, "mkdir -p ../results; printf completed > ../results/output.txt; exit "+strconv.Itoa(exit))
			gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
			before, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			expected := managed.Pass
			if exit != 0 {
				expected = managed.Fail
			}
			gomega.Expect(before.FinalVerdict).To(gomega.Equal(expected))
			gomega.Expect(before.NativeExitCode).To(gomega.HaveValue(gomega.Equal(exit)))
			current, err := s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			key := "settle-" + r.JobID
			var internalDigest string
			var internalReceipt []byte
			gomega.Expect(db.QueryRow(`SELECT digest,receipt_json FROM managed_mutations WHERE mutation_key=?`, key).Scan(&internalDigest, &internalReceipt)).To(gomega.Succeed())
			q := managed.CancelRequest{JobID: r.JobID, ExpectedRevision: r.JobRevision, MutationKey: key}
			ack, err := s.requestCancel(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(ack.Receipt).To(gomega.Equal(current))
			gomega.Expect(ack.Cancellation.Requested).To(gomega.BeFalse())
			replay, err := s.requestCancel(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(replay).To(gomega.Equal(ack))
			changed := q
			changed.ExpectedRevision++
			_, err = s.requestCancel(changed)
			gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
			after, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after).To(gomega.Equal(before))
			var retainedDigest string
			var retainedReceipt []byte
			gomega.Expect(db.QueryRow(`SELECT digest,receipt_json FROM managed_mutations WHERE mutation_key=?`, key).Scan(&retainedDigest, &retainedReceipt)).To(gomega.Succeed())
			gomega.Expect(retainedDigest).To(gomega.Equal(internalDigest))
			gomega.Expect(retainedReceipt).To(gomega.Equal(internalReceipt))
			var count int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations WHERE mutation_key=?`, "cancel:"+key).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.Equal(1))
		})
	}
})
