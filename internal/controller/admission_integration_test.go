//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("ConcurrentSealedAdmission", ginkgo.Label("integration"), func() {
	ginkgo.It("reopens a v3 admission and binds its verified inventory through Unix replay", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-admit-")
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
		store, err := snapshot.OpenStore(root.Path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		source := filepath.Join(parent, "input")
		gomega.Expect(os.MkdirAll(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "source/input.txt"), []byte("original"), 0600)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "source/meta.txt"), []byte("metadata"), 0600)).To(gomega.Succeed())
		sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt", "source/meta.txt"}, snapshot.Provenance{EvidenceClass: "development"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		digest := strings.Repeat("a", 64)
		job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-one", Mode: "auto", Suite: "legacy", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt", "source/meta.txt"}}, Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/usr/bin/true"}, CWD: "source", RequiredOutputs: []string{"results/output.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 30, PrepareSeconds: 30, RunSeconds: 30, CollectSeconds: 30, CleanupSeconds: 30}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "isolated-upgrade-fixture"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
		spec, err := json.Marshal(job)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-one", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-integration"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
		encoded, err := json.Marshal(resource)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ledger.RegisterOnlyLocal(encoded)).To(gomega.Succeed())
		original, err := ledger.AdmitSealed(spec, job.Input.InventoryDigest, sealed.ObjectID, "legacy-key", "legacy-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DROP TABLE managed_admissions; DELETE FROM schema_migrations WHERE version=4; PRAGMA user_version=3`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, err = sqlite.Open(root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		ledger, err = managed.NewLedger(db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var inventory []byte
		gomega.Expect(db.QueryRow(`SELECT inventory_json FROM managed_admissions WHERE request_key='legacy-key'`).Scan(&inventory)).To(gomega.Succeed())
		gomega.Expect(inventory).To(gomega.BeEmpty())
		runtime := newRuntime(root, ledger, store)
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
		replay, _, err := c.Submit(context.Background(), spec, "legacy-key")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.Admission).To(gomega.Equal(original.Admission))
		gomega.Expect(replay.JobID).To(gomega.Equal(original.JobID))
		gomega.Expect(db.QueryRow(`SELECT inventory_json FROM managed_admissions WHERE request_key='legacy-key'`).Scan(&inventory)).To(gomega.Succeed())
		expected, err := json.Marshal(sealed.Manifest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(inventory).To(gomega.Equal(expected))
		gomega.Expect(os.WriteFile(filepath.Join(source, "source/input.txt"), []byte("changed"), 0600)).To(gomega.Succeed())
		_, _, err = c.Submit(context.Background(), spec, "legacy-key")
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("CONFLICT"))
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.Equal(1))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
	})

	ginkgo.It("executes once across failed sealing, lost replies, concurrent retries and retained tombstones", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-admit-")
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
		store, err := snapshot.OpenStore(root.Path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		source := filepath.Join(parent, "input")
		gomega.Expect(os.MkdirAll(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "source/input.txt"), []byte("original"), 0600)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "source/meta.txt"), []byte("metadata"), 0600)).To(gomega.Succeed())
		sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt", "source/meta.txt"}, snapshot.Provenance{EvidenceClass: "development"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		count := filepath.Join(parent, "executions")
		digest := strings.Repeat("a", 64)
		job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-one", Mode: "auto", Suite: "admission", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt", "source/meta.txt"}}, Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/sh", "-c", "mkdir -p ../results; cat input.txt > ../results/output.txt; printf x >> \"$COUNT_PATH\""}, CWD: "source", Environment: map[string]string{"COUNT_PATH": count}, RequiredOutputs: []string{"results/output.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 30, PrepareSeconds: 30, RunSeconds: 30, CollectSeconds: 30, CleanupSeconds: 30}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "isolated-admission-fixture"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
		spec, err := json.Marshal(job)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-one", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-integration"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
		encoded, err := json.Marshal(resource)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ledger.RegisterOnlyLocal(encoded)).To(gomega.Succeed())
		runtime := newRuntime(root, ledger, store)
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: root.SocketPath, Net: "unix"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Chmod(root.SocketPath, 0600)).To(gomega.Succeed())
		server := &http.Server{Handler: runtime.routes()}
		served := make(chan error, 1)
		go func() { served <- server.Serve(listener) }()
		ctx, cancel := context.WithCancel(context.Background())
		scheduled := make(chan struct{})
		go func() { runtime.schedule(ctx); close(scheduled) }()
		defer func() {
			cancel()
			_ = server.Close()
			<-served
			select {
			case <-scheduled:
			case <-time.After(5 * time.Second):
				t.Fatal("scheduler did not stop")
			}
		}()
		c, err := client.Connect(context.Background(), root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer c.Close()
		_, _, err = c.Submit(context.Background(), spec, "invalid key")
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("INVALID_REQUEST"))
		objects := filepath.Join(root.Path, "objects")
		if os.Getuid() == 0 {
			t.Fatal("staging failure requires the supported non-root owner fixture")
		}
		gomega.Expect(os.Chmod(objects, 0500)).To(gomega.Succeed())
		_, _, err = c.Submit(context.Background(), spec, "durable-key")
		gomega.Expect(os.Chmod(objects, 0700)).To(gomega.Succeed())
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
		var admitted int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_admissions`).Scan(&admitted)).To(gomega.Succeed())
		gomega.Expect(admitted).To(gomega.BeZero())
		// Submit over the production Unix HTTP path and discard the complete
		// response. Reconnecting with the same key must discover this admission.
		body, err := json.Marshal(map[string]any{"spec": json.RawMessage(spec), "request_key": "durable-key"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", root.SocketPath)
		}}
		defer transport.CloseIdleConnections()
		request, err := http.NewRequest("POST", "http://haepari/v1/jobs", strings.NewReader(string(body)))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		request.Header.Set("X-Haepari-Protocol", protocol)
		response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(request)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(response.StatusCode).To(gomega.Equal(200))
		gomega.Expect(response.Body.Close()).To(gomega.Succeed())
		const clients = 8
		start := make(chan struct{})
		receipts := make(chan managed.Receipt, clients)
		failures := make(chan error, clients)
		var group sync.WaitGroup
		for i := 0; i < clients; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				connection, err := client.Connect(context.Background(), root)
				if err != nil {
					failures <- err
					return
				}
				defer connection.Close()
				r, _, err := connection.Submit(context.Background(), spec, "durable-key")
				receipts <- r
				failures <- err
			}()
		}
		close(start)
		group.Wait()
		close(receipts)
		close(failures)
		for err := range failures {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		var identity managed.Receipt
		for r := range receipts {
			if identity.JobID == "" {
				identity = r
			}
			gomega.Expect(r.JobID).To(gomega.Equal(identity.JobID))
			gomega.Expect(r.Admission).To(gomega.Equal(identity.Admission))
		}
		result, _, err := c.Wait(context.Background(), identity.JobID, 5*time.Second)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		contents, err := os.ReadFile(count)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(contents)).To(gomega.Equal("x"))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_admissions`).Scan(&admitted)).To(gomega.Succeed())
		gomega.Expect(admitted).To(gomega.Equal(1))

		objectRoot := filepath.Dir(sealed.Root)
		hidden := objectRoot + ".fixture-hidden"
		gomega.Expect(os.Rename(objectRoot, hidden)).To(gomega.Succeed())
		_, _, err = c.Submit(context.Background(), spec, "durable-key")
		gomega.Expect(os.Rename(hidden, objectRoot)).To(gomega.Succeed())
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_admissions`).Scan(&admitted)).To(gomega.Succeed())
		gomega.Expect(admitted).To(gomega.Equal(1))

		// Registry loss is storage uncertainty, not absent admission or a
		// conflicting new request. Preserve and restore only this fixture row.
		_, err = db.Exec(`CREATE TEMP TABLE saved_admissions AS SELECT * FROM managed_admissions`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DELETE FROM managed_admissions`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, _, err = c.Inspect(context.Background(), identity.JobID)
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
		_, _, err = c.Submit(context.Background(), spec, "durable-key")
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
		_, err = db.Exec(`INSERT INTO managed_admissions SELECT * FROM saved_admissions`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DROP TABLE saved_admissions`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ledger.RetainAdmissionTombstone(identity.JobID)).To(gomega.Succeed())
		// Corrupt retained metadata is storage uncertainty, not caller conflict.
		for _, damaged := range [][]byte{nil, []byte(`{}`)} {
			_, err = db.Exec(`UPDATE managed_admissions SET inventory_json=? WHERE request_key='durable-key'`, damaged)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, _, err = c.Submit(context.Background(), spec, "durable-key")
			gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
			gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
		}
		verifiedInventory, err := json.Marshal(sealed.Manifest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`UPDATE managed_admissions SET inventory_json=? WHERE request_key='durable-key'`, verifiedInventory)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// This is synthetic, fixture-only retention. Production GC is TASK-012.
		gomega.Expect(os.RemoveAll(filepath.Dir(sealed.Root))).To(gomega.Succeed())
		for _, table := range []string{"managed_job_inputs", "managed_runs", "managed_events", "managed_event_owners", "managed_event_progress", "managed_jobs"} {
			_, err := db.Exec("DELETE FROM " + table)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		gomega.Expect(os.Remove(filepath.Join(source, "source/meta.txt"))).To(gomega.Succeed())
		replay, _, err := c.Submit(context.Background(), spec, "durable-key")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.Admission).To(gomega.Equal(identity.Admission))
		gomega.Expect(replay.ResultStatus).To(gomega.Equal("unavailable"))
		seen, _, err := c.Inspect(context.Background(), identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(seen.Result).To(gomega.BeNil())
		gomega.Expect(seen.Receipt.ResultStatus).To(gomega.Equal("unavailable"))
		_, _, err = c.Wait(context.Background(), identity.JobID, time.Second)
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("RESULT_UNAVAILABLE"))
		gomega.Expect(os.WriteFile(filepath.Join(source, "source/input.txt"), []byte("changed"), 0600)).To(gomega.Succeed())
		_, _, err = c.Submit(context.Background(), spec, "durable-key")
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("CONFLICT"))
		gomega.Expect(os.RemoveAll(source)).To(gomega.Succeed())
		replay, _, err = c.Submit(context.Background(), spec, "durable-key")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.JobID).To(gomega.Equal(identity.JobID))
		contents, err = os.ReadFile(count)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(contents)).To(gomega.Equal("x"))
		var jobs int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&jobs)).To(gomega.Succeed())
		gomega.Expect(jobs).To(gomega.BeZero())
	})
})
