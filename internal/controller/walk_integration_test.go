//go:build integration

package controller

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var _ = ginkgo.Describe("LocalWalkingSkeleton", ginkgo.Label("integration"), func() {
	ginkgo.It("LocalWalkingSkeleton", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-walk-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root, err := state.Resolve(filepath.Join(parent, "state"))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := root.Prepare(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		source := filepath.Join(parent, "input")
		if err := os.MkdirAll(filepath.Join(source, "source"), 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.WriteFile(filepath.Join(source, "source", "input.txt"), []byte("first\n"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.WriteFile(filepath.Join(source, "source", "metadata.txt"), []byte("metadata\n"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		store, err := snapshot.OpenStore(root.Path)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt", "source/metadata.txt"}, snapshot.Provenance{EvidenceClass: "development"})
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- Run(ctx, root) }()
		var c *client.Client
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			c, err = client.Connect(context.Background(), root)
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer c.Close()
		resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-one", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-integration"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
		for _, unsupported := range []managed.ResourceSpec{
			func() managed.ResourceSpec {
				r := resource
				r.Kind, r.Cleanup = "utm-macos", nil
				r.Adapter.UTMCtl = "/Applications/UTM.app/Contents/MacOS/utmctl"
				r.VM = &managed.VM{UTMVMUUID: "11111111-1111-4111-8111-111111111111", MACAddress: "aa:bb:cc:dd:ee:ff", AddressDiscovery: "dhcp-lease-by-mac", GuestJobRoot: "/var/haepari/jobs"}
				r.Guest = &managed.Guest{ConnectionReference: "ssh-map", KnownHostsReference: "host-key"}
				return r
			}(),
		} {
			encoded, _ := json.Marshal(unsupported)
			var issue *client.Issue
			if _, err := c.Register(context.Background(), encoded); !errors.As(err, &issue) || issue.Code != "UNSUPPORTED" {
				t.Fatalf("unsupported resource admitted: %+v, %v", unsupported, err)
			}
		}
		resourceJSON, _ := json.Marshal(resource)
		if _, err := c.Register(context.Background(), resourceJSON); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		secondResource := resource
		secondResource.ID = "local-two"
		secondJSON, _ := json.Marshal(secondResource)
		var issue *client.Issue
		if _, err := c.Register(context.Background(), secondJSON); err != nil {
			t.Fatalf("second local resource: %v", err)
		}
		digest := strings.Repeat("a", 64)
		count := filepath.Join(parent, "count.txt")
		job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-one", Mode: "auto", Suite: "walk", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt", "source/metadata.txt"}}, Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/sh", "-c", "sleep 2; mkdir -p ../results; cat input.txt > ../results/output.txt; printf x >> \"$COUNT_PATH\""}, CWD: "source", Environment: map[string]string{"COUNT_PATH": count}, RequiredOutputs: []string{"results/output.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 30, PrepareSeconds: 30, RunSeconds: 30, CollectSeconds: 30, CleanupSeconds: 30}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "isolated-fixture"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
		spec, _ := json.Marshal(job)
		if _, err := managed.DecodeJob(spec); err != nil {
			t.Fatalf("fixture job: %v", err)
		}
		missingSource := job
		missingSource.Input.Root = filepath.Join(parent, "absent-input")
		absentSpec, _ := json.Marshal(missingSource)
		if _, _, err := c.Submit(context.Background(), absentSpec, "missing-source-key"); !errors.As(err, &issue) || issue.Code != "PREREQUISITE_UNAVAILABLE" {
			t.Fatalf("missing source should reject admission as a prerequisite: %v", err)
		}
		for _, unsupported := range []managed.JobSpec{
			func() managed.JobSpec {
				j := job
				j.Mode, j.Manual = "manual", &managed.Manual{ScenarioDocument: "scenarios/check.md", RequiredScenarios: []string{"one"}}
				j.Deadlines.RunSeconds, j.Deadlines.ManualSeconds = 0, 30
				return j
			}(),
			func() managed.JobSpec { j := job; j.Provenance.EvidenceClass = "release-distribution"; return j }(),
			func() managed.JobSpec { j := job; j.Input.SelectedFiles = nil; return j }(),
		} {
			encoded, _ := json.Marshal(unsupported)
			if _, _, err := c.Submit(context.Background(), encoded, "walk-key"); !errors.As(err, &issue) || issue.Code != "UNSUPPORTED" {
				t.Fatalf("unsupported job admitted: %+v, %v", unsupported, err)
			}
		}
		// Read only the HTTP status, then discard the admission body. The server
		// sends 200 after committing admission, so the same key must recover it.
		lostBody, _ := json.Marshal(map[string]any{"spec": json.RawMessage(spec), "request_key": "walk-key"})
		conn, err := net.Dial("unix", root.SocketPath)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		header := fmt.Sprintf("POST /v1/jobs HTTP/1.1\r\nHost: haepari\r\nContent-Type: application/json\r\nX-Haepari-Protocol: managed-execution/v1\r\nX-Haepari-Protocol-Min: 1\r\nX-Haepari-Protocol-Max: 1\r\nContent-Length: %d\r\n\r\n", len(lostBody))
		if _, err := io.Copy(conn, bytes.NewReader(append([]byte(header), lostBody...))); err != nil {
			conn.Close()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		statusLine, err := bufio.NewReader(conn).ReadString('\n')
		_ = conn.Close()
		if err != nil || !strings.Contains(statusLine, " 200 ") {
			t.Fatalf("admission response was not committed before loss: %q %v", statusLine, err)
		}
		receipt, _, err := c.Submit(context.Background(), spec, "walk-key")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if receipt.JobID == "" {
			t.Fatal("empty admitted identity")
		}
		unsupportedReplay := job
		unsupportedReplay.Input.SelectedFiles = nil
		unsupportedSpec, _ := json.Marshal(unsupportedReplay)
		if _, _, err := c.Submit(context.Background(), unsupportedSpec, "walk-key"); !errors.As(err, &issue) || issue.Code != "CONFLICT" {
			t.Fatalf("consumed key with unsupported changed spec should conflict: %v", err)
		}
		_, _, err = c.Wait(context.Background(), receipt.JobID, 20*time.Millisecond)
		if !errors.As(err, &issue) || issue.Code != "WAIT_TIMEOUT" {
			t.Fatalf("expected wait timeout without cancellation: %v", err)
		}
		expiringJob := job
		expiringJob.Deadlines.QueueSeconds = 1
		expiringSpec, _ := json.Marshal(expiringJob)
		expiring, _, err := c.Submit(context.Background(), expiringSpec, "expiring-key")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		corruptSource := filepath.Join(parent, "corrupt-input")
		if err := os.MkdirAll(filepath.Join(corruptSource, "source"), 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.WriteFile(filepath.Join(corruptSource, "source", "input.txt"), []byte("other\n"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.WriteFile(filepath.Join(corruptSource, "source", "metadata.txt"), []byte("metadata\n"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		corruptSnap, err := store.Seal(context.Background(), corruptSource, []string{"source/input.txt", "source/metadata.txt"}, snapshot.Provenance{EvidenceClass: "development"})
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		corruptJob := job
		corruptJob.Input.Root = corruptSource
		corruptJob.Input.InventoryDigest = strings.TrimPrefix(corruptSnap.Manifest.TreeSHA256, "sha256:")
		corruptSpec, _ := json.Marshal(corruptJob)
		corruptReceipt, _, err := c.Submit(context.Background(), corruptSpec, "corrupt-key")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.RemoveAll(filepath.Dir(corruptSnap.Root)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		failedJob := job
		failedJob.Workload.Argv = []string{"/bin/sh", "-c", "mkdir -p ../results; printf failed > ../results/output.txt; exit 7"}
		failedSpec, _ := json.Marshal(failedJob)
		queued, _, err := c.Submit(context.Background(), failedSpec, "failure-key")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		queuedState, _, err := c.Inspect(context.Background(), queued.JobID)
		if err != nil || queuedState.Receipt.JobState != "queued" {
			t.Fatalf("capacity one did not queue second job: %+v %v", queuedState, err)
		}
		earlyExpiry, _, err := c.Wait(context.Background(), expiring.JobID, 1500*time.Millisecond)
		if err != nil || earlyExpiry.FinalVerdict != managed.Incomplete || earlyExpiry.Reason != "queue_expired" {
			t.Fatalf("queued deadline did not settle while resource occupied: %+v %v", earlyExpiry, err)
		}
		stillRunning, _, err := c.Inspect(context.Background(), receipt.JobID)
		if err != nil || stillRunning.Receipt.JobState == "finished" {
			t.Fatalf("deadline settlement waited for occupied slot: %+v %v", stillRunning, err)
		}
		result, _, err := c.Wait(context.Background(), receipt.JobID, 5*time.Second)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if result.FinalVerdict != managed.Pass || result.WorkloadOutcome != "pass" || result.EvidenceCompleteness != "complete" || result.CleanupOutcome != "verified" || len(result.Artifacts) != 1 {
			t.Fatalf("wrong terminal result: %+v", result)
		}
		expired, _, err := c.Wait(context.Background(), expiring.JobID, 5*time.Second)
		if err != nil || expired.FinalVerdict != managed.Incomplete || expired.Reason != "queue_expired" || expired.CleanupOutcome != "not_needed" {
			t.Fatalf("expired queue head: %+v %v", expired, err)
		}
		corruptResult, _, err := c.Wait(context.Background(), corruptReceipt.JobID, 5*time.Second)
		if err != nil || corruptResult.FinalVerdict != managed.Incomplete || corruptResult.Reason != "sealed_input_unavailable" || corruptResult.CleanupOutcome != "not_needed" {
			t.Fatalf("corrupt queue head: %+v %v", corruptResult, err)
		}
		counted, err := os.ReadFile(count)
		if err != nil || string(counted) != "x" {
			t.Fatalf("execution count: %q %v", counted, err)
		}
		failed, _, err := c.Wait(context.Background(), queued.JobID, 5*time.Second)
		if err != nil || failed.FinalVerdict != managed.Fail || failed.WorkloadOutcome != "fail" || failed.NativeExitCode == nil || *failed.NativeExitCode != 7 || failed.CleanupOutcome != "verified" {
			t.Fatalf("failure result: %+v %v", failed, err)
		}
		missingJob := job
		missingJob.Workload.Argv = []string{"/bin/sh", "-c", "mkdir -p ../results; printf recoverable > ../results/partial.txt; exit 7"}
		missingSpec, _ := json.Marshal(missingJob)
		missingReceipt, _, err := c.Submit(context.Background(), missingSpec, "missing-key")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		missing, _, err := c.Wait(context.Background(), missingReceipt.JobID, 5*time.Second)
		if err != nil || missing.FinalVerdict != managed.Incomplete || missing.WorkloadOutcome != "fail" || missing.NativeExitCode == nil || *missing.NativeExitCode != 7 || missing.EvidenceCompleteness != "partial" || missing.CleanupOutcome != "failed" || missing.ResourceAvailability != "quarantined" || len(missing.ProtectedEvidence) != 1 {
			t.Fatalf("protected partial evidence: %+v %v", missing, err)
		}
		if _, err := os.Stat(filepath.Join(root.Path, "jobs", missingReceipt.JobID, "workspace", "results", "partial.txt")); err != nil {
			t.Fatalf("recoverable evidence lost: %v", err)
		}
		if err := os.Remove(filepath.Join(source, "source", "metadata.txt")); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		partial, _, err := c.Submit(context.Background(), spec, "walk-key")
		if err != nil || partial.JobID != receipt.JobID {
			t.Fatalf("missing but unchanged selected file should recover sealed Job: %+v %v", partial, err)
		}
		if err := os.WriteFile(filepath.Join(source, "source", "input.txt"), []byte("changed\n"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, _, err := c.Submit(context.Background(), spec, "walk-key"); !errors.As(err, &issue) || issue.Code != "CONFLICT" {
			t.Fatalf("changed payload should conflict: %v", err)
		}
		if err := os.Remove(filepath.Join(source, "source", "input.txt")); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.Symlink(count, filepath.Join(source, "source", "input.txt")); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, _, err := c.Submit(context.Background(), spec, "walk-key"); !errors.As(err, &issue) || issue.Code != "CONFLICT" {
			t.Fatalf("type-changed replay should conflict: %v", err)
		}
		if err := os.RemoveAll(source); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		c.Close()
		c, err = client.Connect(context.Background(), root)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		again, _, err := c.Submit(context.Background(), spec, "walk-key")
		if err != nil || again.JobID != receipt.JobID {
			t.Fatalf("same-key reconnect: %+v %v", again, err)
		}
		seen, _, err := c.Inspect(context.Background(), receipt.JobID)
		if err != nil || seen.Result == nil || seen.Result.FinalVerdict != managed.Pass {
			t.Fatalf("persisted result: %+v %v", seen, err)
		}
		counted, err = os.ReadFile(count)
		if err != nil || string(counted) != "x" {
			t.Fatalf("duplicate execution: %q %v", counted, err)
		}
		cancel()
		if err := <-done; err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}

	})
})

var _ = ginkgo.Describe("RequestSizeAndChangedSourceMapping", ginkgo.Label("integration"), func() {
	ginkgo.It("RequestSizeAndChangedSourceMapping", func() {
		t := ginkgo.GinkgoT()
		// The spec fits the CLI limit while its surrounding request exceeds 1 MiB.
		valid := `{"spec":"` + strings.Repeat("a", 1<<20-2) + `"}`
		var input struct {
			Spec string `json:"spec"`
		}
		if err := decodeBody(httptest.NewRequest("POST", "/v1/jobs", strings.NewReader(valid)), &input); err != nil || len(input.Spec) != 1<<20-2 {
			t.Fatalf("CLI-sized spec rejected: %v, %d", err, len(input.Spec))
		}
		oversized := valid + strings.Repeat(" ", 4096)
		if err := decodeBody(httptest.NewRequest("POST", "/v1/jobs", strings.NewReader(oversized)), &input); !errors.Is(err, managed.ErrInvalid) {
			t.Fatalf("oversized trailing whitespace accepted: %v", err)
		}
		for _, duplicate := range []string{`{"spec":"a","spec":"b"}`, `{"spec":"a","request_key":"one","request_key":"two"}`} {
			if err := decodeBody(httptest.NewRequest("POST", "/v1/jobs", strings.NewReader(duplicate)), &input); !errors.Is(err, managed.ErrInvalid) {
				t.Fatalf("duplicate envelope key accepted: %s: %v", duplicate, err)
			}
		}
		reply := httptest.NewRecorder()
		reject(reply, "submit", snapshot.ErrChanged)
		var envelope client.Envelope
		if err := json.Unmarshal(reply.Body.Bytes(), &envelope); err != nil || envelope.Error == nil || envelope.Error.Code != "CONFLICT" || envelope.Error.Message == envelope.Error.Code || envelope.Error.Retry != "none" {
			t.Fatalf("changed source response: %+v, %v", envelope, err)
		}
		for _, timeout := range []string{"25h", "0s", "notaduration"} {
			reply := httptest.NewRecorder()
			request := httptest.NewRequest("GET", "/v1/jobs/missing/wait?timeout="+timeout, nil)
			request.Header.Set("X-Haepari-Protocol", protocol)
			request.Header.Set("X-Haepari-Protocol-Min", "1")
			request.Header.Set("X-Haepari-Protocol-Max", "1")
			(&runtimeServer{}).routes().ServeHTTP(reply, request)
			var response client.Envelope
			if err := json.Unmarshal(reply.Body.Bytes(), &response); err != nil || reply.Code != 400 || response.Error == nil || response.Error.Code != "INVALID_REQUEST" {
				t.Fatalf("invalid wait timeout %q: status=%d response=%+v error=%v", timeout, reply.Code, response, err)
			}
		}

	})
})
