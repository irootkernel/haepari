//go:build integration

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task007UnixScheduling", ginkgo.Label("integration"), func() {
	ginkgo.It("dispatches beyond a paused FIFO head and enforces inherited context on real Unix requests and child processes", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-schedule-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		gomega.Expect(os.Mkdir(filepath.Join(root.Path, "jobs"), 0700)).To(gomega.Succeed())
		db, err := sqlite.Open(root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		ledger, err := managed.NewLedger(db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ledger.UseLegacyFixtureBounds()
		store, err := snapshot.OpenStore(root.Path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		runtime, err := newRuntime(root, ledger, store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: root.SocketPath, Net: "unix"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Chmod(root.SocketPath, 0600)).To(gomega.Succeed())
		server := &http.Server{Handler: runtime.routes()}
		served := make(chan error, 1)
		go func() { served <- server.Serve(listener) }()
		defer func() { _ = server.Close(); <-served }()
		c, err := client.Connect(context.Background(), root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer c.Close()
		r := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-a", Kind: "local", ExecutionMode: "managed", Capacity: 2, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-development"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
		for _, id := range []string{"local-a", "local-b"} {
			r.ID = id
			raw, _ := json.Marshal(r)
			_, err := c.Register(context.Background(), raw)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		pause := managed.AvailabilityRequest{MutationKey: "pause-a", ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}
		paused, _, err := c.SetAvailability(context.Background(), pause, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		replay, _, err := c.SetAvailability(context.Background(), pause, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.AvailabilityRevision).To(gomega.Equal(paused.AvailabilityRevision))
		source := filepath.Join(parent, "input")
		gomega.Expect(os.MkdirAll(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "source/input.txt"), []byte("input"), 0600)).To(gomega.Succeed())
		sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		digest := strings.Repeat("a", 64)
		job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-a", Mode: "auto", Suite: "scheduling", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt"}}, Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/sh", "-c", "mkdir -p ../results; printf '%s' \"$HAEPARI_PARENT_CONTEXT\" > ../results/output.json"}, CWD: "source", RequiredOutputs: []string{"results/output.json"}}, Deadlines: managed.Deadlines{QueueSeconds: 60, PrepareSeconds: 30, RunSeconds: 30, CollectSeconds: 30, CleanupSeconds: 30}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "isolated-scheduling-fixture"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
		raw, _ := json.Marshal(job)
		parentJob, _, err := c.Submit(context.Background(), raw, "paused-head")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		job.ResourceID = "local-b"
		raw, _ = json.Marshal(job)
		other, _, err := c.Submit(context.Background(), raw, "other-resource")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		startScheduler := func() (context.CancelFunc, <-chan struct{}) {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { runtime.schedule(ctx); close(done) }()
			return cancel, done
		}
		cancel, done := startScheduler()
		result, _, err := c.Wait(context.Background(), other.JobID, 10*time.Second)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		cancel()
		<-done
		original, err := ledger.Snapshot(parentJob.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original.JobState).To(gomega.Equal("queued"))
		gomega.Expect(original.LeaseGeneration).To(gomega.BeZero())
		resume := managed.AvailabilityRequest{MutationKey: "resume-a", ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 2}
		gomega.Expect(os.Chmod(filepath.Join(root.Path, "jobs"), 0755)).To(gomega.Succeed())
		_, _, err = c.SetAvailability(context.Background(), resume, true)
		var readinessIssue *client.Issue
		gomega.Expect(errors.As(err, &readinessIssue)).To(gomega.BeTrue())
		gomega.Expect(readinessIssue.Code).To(gomega.Equal("RECOVERY_BLOCKED"))
		gomega.Expect(os.Chmod(filepath.Join(root.Path, "jobs"), 0700)).To(gomega.Succeed())
		resumed, _, err := c.SetAvailability(context.Background(), resume, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var readinessRaw []byte
		gomega.Expect(db.QueryRow(`SELECT request_json FROM managed_availability_receipts WHERE mutation_key='resume-a'`).Scan(&readinessRaw)).To(gomega.Succeed())
		var stored struct{ Request managed.AvailabilityRequest }
		gomega.Expect(json.Unmarshal(readinessRaw, &stored)).To(gomega.Succeed())
		binding := sha256.Sum256([]byte(stored.Request.ObservationReference))
		gomega.Expect(stored.Request.ReadinessDigest).To(gomega.Equal(hex.EncodeToString(binding[:])))
		gomega.Expect(stored.Request.ObservationReference).To(gomega.ContainSubstring(`"resource":"local-a"`))
		gomega.Expect(stored.Request.ObservationReference).To(gomega.ContainSubstring(`"path":"jobs"`))
		gomega.Expect(stored.Request.ObservationReference).To(gomega.ContainSubstring(`"mode":448`))
		gomega.Expect(os.Chmod(filepath.Join(root.Path, "jobs"), 0755)).To(gomega.Succeed())
		// Committed exact replay and conflicts are resolved without fresh effects.
		resumeReplay, _, err := c.SetAvailability(context.Background(), resume, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resumeReplay.AvailabilityRevision).To(gomega.Equal(resumed.AvailabilityRevision))
		gomega.Expect(resumeReplay.IdleSince.Equal(*paused.IdleSince)).To(gomega.BeTrue())
		gomega.Expect(resumeReplay.Mutation).To(gomega.Equal(resumed.Mutation))
		changedResume := resume
		changedResume.ExpectedConfigRevision++
		_, _, err = c.SetAvailability(context.Background(), changedResume, true)
		gomega.Expect(errors.As(err, &readinessIssue)).To(gomega.BeTrue())
		gomega.Expect(readinessIssue.Code).To(gomega.Equal("CONFLICT"))
		newResume := resume
		newResume.MutationKey = "new-unsafe-resume"
		_, _, err = c.SetAvailability(context.Background(), newResume, true)
		gomega.Expect(errors.As(err, &readinessIssue)).To(gomega.BeTrue())
		gomega.Expect(readinessIssue.Code).To(gomega.Equal("RECOVERY_BLOCKED"))
		var consumed int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_availability_receipts WHERE mutation_key=?`, newResume.MutationKey).Scan(&consumed)).To(gomega.Succeed())
		gomega.Expect(consumed).To(gomega.BeZero())
		gomega.Expect(os.Chmod(filepath.Join(root.Path, "jobs"), 0700)).To(gomega.Succeed())
		var configRaw []byte
		gomega.Expect(db.QueryRow(`SELECT spec_json FROM managed_resources WHERE id='local-a'`).Scan(&configRaw)).To(gomega.Succeed())
		configDigest := sha256.Sum256(configRaw)
		gomega.Expect(resumeReplay.ConfigurationDigest).To(gomega.Equal(hex.EncodeToString(configDigest[:])))
		held, err := ledger.Reserve(managed.ReserveRequest{JobID: parentJob.JobID, ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 3, MutationKey: "hold-parent"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		chain, err := ledger.RunnerParentChain(runtime.installationID(), held.JobID, held.LeaseGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		chainJSON, _ := json.Marshal(chain)
		queued, _, err := c.Submit(context.Background(), raw, "wait-target")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ledger.RecordQueueBlocker(queued.JobID, queued.JobRevision, managed.QueueBlocker{Reason: "host_capacity_no_eligible_victim", Details: json.RawMessage(`{"policy":["isolated-protocol-fixture"]}`)})).To(gomega.Succeed())
		blockerInspection, blockerEnvelope, err := c.Inspect(context.Background(), queued.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(blockerInspection.QueueBlocker).NotTo(gomega.BeNil())
		gomega.Expect(blockerInspection.QueueBlocker.Reason).To(gomega.Equal("host_capacity_no_eligible_victim"))
		gomega.Expect(blockerInspection.QueueBlocker.Details).To(gomega.Equal(json.RawMessage(`{"policy":["isolated-protocol-fixture"]}`)))
		gomega.Expect(string(blockerEnvelope.Data)).To(gomega.ContainSubstring(`"queue_blocker"`))
		gomega.Expect(string(blockerEnvelope.Data)).To(gomega.ContainSubstring("host_capacity_no_eligible_victim"))
		// Raw Unix traffic also rejects a malformed inherited header.
		httpClient := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", root.SocketPath)
		}}}
		defer httpClient.CloseIdleConnections()
		request, err := http.NewRequest("POST", "http://haepari/v1/jobs", strings.NewReader(`{}`))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		request.Header.Set("X-Haepari-Protocol", protocol)
		request.Header.Set("X-Haepari-Protocol-Min", "1")
		request.Header.Set("X-Haepari-Protocol-Max", "1")
		request.Header.Set(managed.ParentContextHeader, "invalid")
		response, err := httpClient.Do(request)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(response.StatusCode).To(gomega.Equal(http.StatusBadRequest))
		response.Body.Close()
		t.Setenv(managed.ParentContextEnvironment, string(chainJSON))
		job.Input.Root = filepath.Join(parent, "missing-source")
		raw, _ = json.Marshal(job)
		_, _, err = c.Submit(context.Background(), raw, "nested-before-seal")
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("NESTED_ACQUISITION_REJECTED"))
		_, err = ledger.Request("nested-before-seal")
		gomega.Expect(errors.Is(err, managed.ErrNotFound)).To(gomega.BeTrue())
		_, _, err = c.Wait(context.Background(), queued.JobID, time.Second)
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("NESTED_WAIT_REJECTED"))
		_, _, err = c.Inspect(context.Background(), queued.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, _, err = c.Wait(context.Background(), other.JobID, time.Second)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		inheritedNonce := strings.Repeat("b", 64)
		foreign := managed.ParentChain{{InstallationID: "foreign-installation", JobID: "foreign-job", LeaseGeneration: 7, Nonce: inheritedNonce}}
		foreignJSON, _ := json.Marshal(foreign)
		t.Setenv(managed.ParentContextEnvironment, string(foreignJSON))
		job.Input.Root = source
		job.Workload.Environment = map[string]string{managed.ParentContextEnvironment: "attempted replacement"}
		raw, _ = json.Marshal(job)
		inherited, _, err := c.Submit(context.Background(), raw, "foreign-chain")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// Remove the earlier wait target without dispatch; then observe one actual
		// runner append a fresh entry while preserving the foreign installation.
		_, err = ledger.CancelQueued(queued.JobID, queued.JobRevision, "cancel-target")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		proof := managed.NoEffectProof{ObservationReference: "parent-no-effect", NoOwnedJobEffect: true, NoUnprovenProcess: true, NoProtectedPath: true, PowerReconciled: true}
		observedParent, err := ledger.RecordNoEffectObservation(managed.NoEffectObservationRequest{JobID: held.JobID, ExpectedJobRevision: held.JobRevision, ResourceID: held.ResourceID, ExpectedConfigRevision: 1, Slot: held.Slot, LeaseGeneration: held.LeaseGeneration, Source: "machine", Mechanism: "unissued-fixture-reservation", EvidenceDigest: digest, Proof: proof, MutationKey: "parent-observed"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		classifiedParent, err := ledger.ClassifyNoEffect(managed.ClassifyNoEffectRequest{JobID: held.JobID, ExpectedJobRevision: observedParent.JobRevision, ResourceID: held.ResourceID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 3, Slot: held.Slot, LeaseGeneration: held.LeaseGeneration, FailureKind: "cancel", Proof: proof, MutationKey: "parent-classified"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.SettleNoEffect(managed.SettleNoEffectRequest{JobID: held.JobID, ExpectedJobRevision: classifiedParent.JobRevision, ResourceID: held.ResourceID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 3, Slot: held.Slot, LeaseGeneration: held.LeaseGeneration, MutationKey: "parent-settled"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cancel, done = startScheduler()
		result, _, err = c.Wait(context.Background(), inherited.JobID, 10*time.Second)
		cancel()
		<-done
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		contextFile, err := os.ReadFile(filepath.Join(root.Path, "jobs", inherited.JobID, "artifacts/results/output.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		observed, err := managed.DecodeParentChain(string(contextFile))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(observed).To(gomega.HaveLen(2))
		gomega.Expect(observed[0]).To(gomega.Equal(foreign[0]))
		gomega.Expect(observed[1].InstallationID).To(gomega.Equal(runtime.installationID()))
		gomega.Expect(observed[1].JobID).To(gomega.Equal(inherited.JobID))
		gomega.Expect(observed[1].Nonce).NotTo(gomega.Equal(inheritedNonce))
		gomega.Expect(observed[1].Nonce).NotTo(gomega.BeEmpty())
		requestRaw, err := os.ReadFile(filepath.Join(root.Path, "jobs", inherited.JobID, "request.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(requestRaw)).To(gomega.ContainSubstring(inheritedNonce))
		inspection, _, err := c.Inspect(context.Background(), inherited.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		report, _, err := c.DoctorResource(context.Background(), inherited.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var publicEvents []byte
		after := int64(0)
		complete := false
		for pageN := 0; pageN < 20; pageN++ {
			page, _, err := c.Events(context.Background(), inherited.JobID, client.EventsOptions{After: after, Limit: 100})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			encoded, err := json.Marshal(page)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			publicEvents = append(publicEvents, encoded...)
			if !page.More {
				complete = true
				break
			}
			gomega.Expect(page.NextCursor).To(gomega.BeNumerically(">", after))
			after = page.NextCursor
		}
		gomega.Expect(complete).To(gomega.BeTrue())
		for _, surface := range []any{result, inspection, report} {
			encoded, err := json.Marshal(surface)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(string(encoded)).NotTo(gomega.ContainSubstring(inheritedNonce))
		}
		gomega.Expect(string(publicEvents)).NotTo(gomega.ContainSubstring(inheritedNonce))
		gomega.Expect(string(publicEvents)).To(gomega.ContainSubstring(observed[1].Nonce))
		// A queued pin survives admission; an idle registration update cannot
		// silently dispatch it under the new configuration.
		changed, _, err := c.Submit(context.Background(), raw, "configuration-pin")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		r.ID = "local-b"
		r.Revision = 2
		gomega.Expect(ledger.UpdateResource(mustJSONScheduling(r), 1, 1)).To(gomega.Succeed())
		cancel, done = startScheduler()
		configurationResult, _, err := c.Wait(context.Background(), changed.JobID, 10*time.Second)
		cancel()
		<-done
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(configurationResult.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(configurationResult.Reason).To(gomega.Equal("resource_configuration_changed"))
	})
})

func mustJSONScheduling(value any) []byte {
	raw, err := json.Marshal(value)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return raw
}
