//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/controller"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// m2aForwarder counts request lines and copies every byte to the real
// controller socket. It is not a substitute daemon.
type m2aForwarder struct {
	listener   net.Listener
	projection atomic.Int64
	submits    atomic.Int64
}

type m2aController struct {
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
	err    error
}

// m2aHarness owns one CLI fixture. release cancels and awaits every admitted
// job, then deletes the test root only after public cleanup, zero occupancy,
// and owned process stop. Uncertainty keeps the root and a diagnostic.
type m2aHarness struct {
	parent     string
	root       state.Root
	proxyPath  string
	configPath string
	forwarder  *m2aForwarder
	current    *m2aController
	dev        uint64
	ino        uint64
	jobs       []string
	cancelSeq  int
}

func writeM2aConfig(path string, revision int64, deadlines config.Deadlines) {
	doc := config.Defaults()
	doc.Revision = revision
	doc.GlobalLimit = 4
	doc.DefaultDeadlines = deadlines
	raw, err := doc.Canonical()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(os.WriteFile(path, raw, 0600)).To(gomega.Succeed())
}

func phaseDeadlines(n int64) config.Deadlines {
	return config.Deadlines{
		QueueSeconds: n, PrepareSeconds: n, RunSeconds: n,
		ManualSeconds: n, CollectSeconds: n, CleanupSeconds: n,
	}
}

func (h *m2aHarness) start(configPath string) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	h.current = &m2aController{cancel: cancel, done: done}
	go func() {
		done <- controller.RunWithOptions(ctx, h.root, controller.Options{
			HostMACOSGuestLimit: 1,
			GlobalLimit:         4,
			ConfigPath:          configPath,
			ConfigExplicit:      true,
		})
	}()
	gomega.Eventually(func() error {
		_, err := os.Lstat(h.root.SocketPath)
		return err
	}, 5*time.Second, 10*time.Millisecond).Should(gomega.Succeed())
}

func (c *m2aController) stopErr() error {
	if c == nil || c.cancel == nil {
		return errors.New("controller was not started")
	}
	c.once.Do(func() {
		c.cancel()
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		select {
		case c.err = <-c.done:
		case <-timer.C:
			c.err = errors.New("controller did not stop")
		}
	})
	return c.err
}

func (h *m2aHarness) serve(proxy state.Root) {
	gomega.Expect(os.MkdirAll(proxy.Path, 0700)).To(gomega.Succeed())
	_ = os.Remove(proxy.SocketPath)
	listener, err := net.Listen("unix", proxy.SocketPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	h.forwarder = &m2aForwarder{listener: listener}
	gomega.Expect(os.Chmod(proxy.SocketPath, 0600)).To(gomega.Succeed())
	target := h.root.SocketPath
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go bridgeM2a(conn, target, h.forwarder)
		}
	}()
}

func bridgeM2a(client net.Conn, target string, forwarder *m2aForwarder) {
	defer client.Close()
	upstream, err := net.DialTimeout("unix", target, time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		copyM2aRequests(upstream, client, forwarder)
		_ = upstream.Close()
	}()
	go func() {
		defer group.Done()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
	}()
	group.Wait()
}

func copyM2aRequests(dst io.Writer, src io.Reader, forwarder *m2aForwarder) {
	buf := make([]byte, 32<<10)
	var pending []byte
	for {
		n, err := src.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			pending = append(pending, chunk...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				line := pending[:i]
				pending = pending[i+1:]
				countM2aRequest(line, forwarder)
			}
			if len(pending) > 65536 {
				pending = append([]byte(nil), pending[len(pending)-4096:]...)
			}
			if _, werr := dst.Write(chunk); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func countM2aRequest(line []byte, forwarder *m2aForwarder) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	method, rest, ok := bytes.Cut(line, []byte(" "))
	if !ok {
		return
	}
	target, _, ok := bytes.Cut(rest, []byte(" "))
	if !ok {
		return
	}
	switch {
	case bytes.Equal(method, []byte("GET")) && bytes.HasPrefix(target, []byte("/v1/operator/projection")):
		forwarder.projection.Add(1)
	case bytes.Equal(method, []byte("POST")) && bytes.Equal(target, []byte("/v1/jobs")):
		forwarder.submits.Add(1)
	}
}

func (f *m2aForwarder) around(do func()) (projection, submits int64) {
	beforeProjection := f.projection.Load()
	beforeSubmits := f.submits.Load()
	do()
	return f.projection.Load() - beforeProjection, f.submits.Load() - beforeSubmits
}

func cliOut(args ...string) (bytes.Buffer, bytes.Buffer, int) {
	var out, diagnostic bytes.Buffer
	code := run(args, &out, &diagnostic)
	return out, diagnostic, code
}

func withStateDB(path string, do func(dbExec func(string, ...any) int, query func(string, ...any) []byte)) {
	db, err := sqlite.Open(path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer db.Close()
	count := func(query string, args ...any) int {
		var n int
		gomega.Expect(db.QueryRow(query, args...).Scan(&n)).To(gomega.Succeed())
		return n
	}
	blob := func(query string, args ...any) []byte {
		var raw []byte
		gomega.Expect(db.QueryRow(query, args...).Scan(&raw)).To(gomega.Succeed())
		return raw
	}
	do(count, blob)
}

func storedCLIJob(dbPath, key string) managed.JobSpec {
	var job managed.JobSpec
	withStateDB(dbPath, func(_ func(string, ...any) int, query func(string, ...any) []byte) {
		decoded, err := managed.DecodeJob(query(`SELECT spec_json FROM managed_jobs WHERE request_key=?`, key))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		job = decoded
	})
	return job
}

func stateCounts(dbPath string) (jobs, events, admissions int) {
	withStateDB(dbPath, func(count func(string, ...any) int, _ func(string, ...any) []byte) {
		jobs = count(`SELECT COUNT(*) FROM managed_jobs`)
		events = count(`SELECT COUNT(*) FROM managed_events`)
		admissions = count(`SELECT COUNT(*) FROM managed_admissions`)
	})
	return jobs, events, admissions
}

func withDeadlines(spec []byte, deadlines string) []byte {
	var object map[string]json.RawMessage
	gomega.Expect(json.Unmarshal(spec, &object)).To(gomega.Succeed())
	object["deadlines"] = json.RawMessage(deadlines)
	out, err := json.Marshal(object)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return out
}

func cliJob(source, digest string, files []string) managed.JobSpec {
	sum := strings.Repeat("a", 64)
	return managed.JobSpec{
		Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-one", Mode: "auto", Suite: "cli",
		Input:       managed.Input{Root: source, InventoryDigest: digest, SelectedFiles: files},
		Environment: managed.Environment{ProfileID: "local", ProfileDigest: sum},
		Workload:    managed.Workload{Argv: []string{"/bin/sh", "-c", "mkdir -p ../results; printf ok > ../results/out.txt"}, CWD: "source", RequiredOutputs: []string{"results/out.txt"}},
		Effects:     managed.Effects{Mode: "offline", AuthorizationReference: "cli-integration"},
		Provenance:  managed.Provenance{CandidateDigest: sum, TestsDigest: sum, TargetProfileDigest: sum, EvidenceClass: "development"},
	}
}

func writeM2aJSON(path string, value any) {
	raw, err := json.Marshal(value)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(os.WriteFile(path, raw, 0600)).To(gomega.Succeed())
}

func submitCLI(path, key string) (managed.Receipt, bytes.Buffer, bytes.Buffer, int) {
	out, diagnostic, code := cliOut("submit", "--spec", path, "--request-key", key, "--json")
	var response struct {
		Data managed.Receipt `json:"data"`
	}
	if code == 0 {
		gomega.Expect(json.Unmarshal(out.Bytes(), &response)).To(gomega.Succeed(), diagnostic.String())
	}
	return response.Data, out, diagnostic, code
}

func waitCLI(jobID string) {
	out, diagnostic, code := cliOut("wait", jobID, "--timeout", "20s", "--json")
	gomega.Expect(code).To(gomega.BeZero(), "%s %s", out.String(), diagnostic.String())
}

func directoryIdentity(path string) (uint64, uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() {
		return 0, 0, errors.New("test root is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("test root identity is unavailable")
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}

func startM2aFixture() *m2aHarness {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		ginkgo.Skip("local controller requires Darwin arm64")
	}
	parent, err := os.MkdirTemp("/tmp", "hp-m2a-")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	dev, ino, err := directoryIdentity(parent)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	root, err := state.Resolve(filepath.Join(parent, "state"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	proxy, err := state.Resolve(filepath.Join(parent, "proxy"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	configPath := filepath.Join(parent, "config.json")
	writeM2aConfig(configPath, 1, phaseDeadlines(40))
	harness := &m2aHarness{parent: parent, root: root, proxyPath: proxy.Path, configPath: configPath, dev: dev, ino: ino}
	ginkgo.GinkgoT().Cleanup(harness.release)
	harness.start(configPath)
	harness.serve(proxy)
	ginkgo.GinkgoT().Setenv("HAEPARI_HOME", proxy.Path)
	return harness
}

func (h *m2aHarness) track(id string) {
	if id == "" {
		return
	}
	for _, existing := range h.jobs {
		if existing == id {
			return
		}
	}
	h.jobs = append(h.jobs, id)
}

func (h *m2aHarness) release() {
	previous, present := os.LookupEnv("HAEPARI_HOME")
	if h.proxyPath != "" {
		_ = os.Setenv("HAEPARI_HOME", h.proxyPath)
	}
	defer func() {
		if present {
			_ = os.Setenv("HAEPARI_HOME", previous)
		} else {
			_ = os.Unsetenv("HAEPARI_HOME")
		}
	}()
	var problems []string
	note := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	qualified := map[string]bool{}
	listed, listErr := listJobIDs()
	if listErr != nil {
		note("jobs list: %v", listErr)
	}
	seen := map[string]bool{}
	queue := append([]string{}, h.jobs...)
	queue = append(queue, listed...)
	var ids []string
	for _, id := range queue {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, id := range ids {
		if err := h.cancelAndAwait(id); err != nil {
			note("%s", err.Error())
			continue
		}
		qualified[id] = true
	}
	if listErr == nil {
		again, err := listJobIDs()
		if err != nil {
			note("jobs relist: %v", err)
		} else {
			for _, id := range again {
				if qualified[id] {
					continue
				}
				if err := h.cancelAndAwait(id); err != nil {
					note("unknown job %s: %v", id, err)
					continue
				}
				qualified[id] = true
			}
			final, err := listJobIDs()
			if err != nil {
				note("jobs confirmation: %v", err)
			} else {
				for _, id := range final {
					if !qualified[id] {
						note("unknown job %s remains", id)
					}
				}
			}
		}
	}
	if err := requireZeroOccupancy(); err != nil {
		note("occupancy: %v", err)
	}
	if h.current == nil {
		note("controller was not started")
	} else if err := h.current.stopErr(); err != nil {
		note("controller stop: %v", err)
	}
	if h.forwarder != nil && h.forwarder.listener != nil {
		if err := h.forwarder.listener.Close(); err != nil {
			note("forwarder close: %v", err)
		}
	}
	if h.root.Path == "" {
		note("state root was not captured")
	} else if err := ownedProcessesStopped(h.root.Path, qualified); err != nil {
		note("owned processes: %v", err)
	}
	if err := h.confirmRoot(); err != nil {
		note("%s", err.Error())
	}
	if len(problems) == 0 {
		if err := os.RemoveAll(h.parent); err != nil {
			problems = append(problems, fmt.Sprintf("remove test root: %v", err))
		}
	}
	if len(problems) == 0 {
		return
	}
	text := strings.Join(problems, "\n") + "\n"
	_ = os.WriteFile(filepath.Join(h.parent, "cleanup-diagnostic.txt"), []byte(text), 0600)
	ginkgo.Fail("owned cleanup uncertain; retained " + h.parent + "\n" + text)
}

func (h *m2aHarness) cancelAndAwait(jobID string) error {
	var stale string
	cancelled := false
	for attempt := 0; attempt < 4; attempt++ {
		receipt, err := inspectCLI(jobID)
		if err != nil {
			return err
		}
		h.cancelSeq++
		key := fmt.Sprintf("m2a-cancel-%d", h.cancelSeq)
		envelope, out, diagnostic, code := cliEnvelope("cancel", jobID, "--expected-revision", fmt.Sprintf("%d", receipt.JobRevision), "--request-key", key, "--json")
		if code == 0 {
			cancelled = true
			break
		}
		if staleRevision(envelope, out, diagnostic) {
			stale = strings.TrimSpace(out.String() + " " + diagnostic.String())
			continue
		}
		return fmt.Errorf("cancel %s: exit %d %s %s", jobID, code, out.String(), diagnostic.String())
	}
	if !cancelled {
		return fmt.Errorf("cancel %s stayed stale: %s", jobID, stale)
	}
	result, code, detail, err := waitCLIResult(jobID, "45s")
	if err != nil {
		return fmt.Errorf("await %s: %w", jobID, err)
	}
	if !cleanupQualified(code, result) {
		return fmt.Errorf("job %s lacks verified cleanup: exit %d cleanup %s lease %d workload %s (%s)", jobID, code, result.CleanupOutcome, result.LeaseGeneration, result.WorkloadOutcome, detail)
	}
	return nil
}

func (h *m2aHarness) confirmRoot() error {
	dev, ino, err := directoryIdentity(h.parent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("test root disappeared before owned cleanup")
		}
		return err
	}
	if dev != h.dev || ino != h.ino {
		return errors.New("test root identity changed")
	}
	if h.root.SocketPath == "" {
		return errors.New("controller socket path is unset")
	}
	return socketGone(h.root.SocketPath)
}

func cliEnvelope(args ...string) (client.Envelope, bytes.Buffer, bytes.Buffer, int) {
	out, diagnostic, code := cliOut(args...)
	var envelope client.Envelope
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			diagnostic.WriteString("\n" + err.Error())
		}
	}
	return envelope, out, diagnostic, code
}

func inspectCLI(jobID string) (managed.Receipt, error) {
	envelope, out, diagnostic, code := cliEnvelope("inspect", jobID, "--json")
	if code != 0 || !envelope.OK {
		return managed.Receipt{}, fmt.Errorf("inspect %s: exit %d %s %s", jobID, code, out.String(), diagnostic.String())
	}
	var data struct {
		Receipt managed.Receipt `json:"receipt"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return managed.Receipt{}, fmt.Errorf("inspect %s: %w", jobID, err)
	}
	if data.Receipt.JobID == "" {
		return managed.Receipt{}, fmt.Errorf("inspect %s returned no job", jobID)
	}
	return data.Receipt, nil
}

func waitCLIResult(jobID, timeout string) (managed.Result, int, string, error) {
	envelope, out, diagnostic, code := cliEnvelope("wait", jobID, "--timeout", timeout, "--json")
	detail := strings.TrimSpace(out.String() + " " + diagnostic.String())
	if code != 0 && code != 1 && code != 3 && code != 4 {
		return managed.Result{}, code, detail, fmt.Errorf("exit %d %s", code, detail)
	}
	var result managed.Result
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return managed.Result{}, code, detail, err
	}
	return result, code, detail, nil
}

func cleanupQualified(code int, result managed.Result) bool {
	if code != 0 && code != 1 && code != 3 && code != 4 {
		return false
	}
	if result.CleanupOutcome == "verified" {
		return true
	}
	return code == 4 && result.CleanupOutcome == "not_needed" && result.LeaseGeneration == 0 && result.WorkloadOutcome == "cancelled"
}

func staleRevision(envelope client.Envelope, out, diagnostic bytes.Buffer) bool {
	if envelope.Error != nil && envelope.Error.Code == "STALE_REVISION" {
		return true
	}
	return bytes.Contains(out.Bytes(), []byte("STALE_REVISION")) || bytes.Contains(diagnostic.Bytes(), []byte("STALE_REVISION"))
}

func listJobIDs() ([]string, error) {
	var ids []string
	cursor := ""
	for page := 0; page < 5; page++ {
		args := []string{"jobs", "list", "--limit", "100", "--json"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		envelope, out, diagnostic, code := cliEnvelope(args...)
		if code != 0 || !envelope.OK {
			return nil, fmt.Errorf("exit %d %s %s", code, out.String(), diagnostic.String())
		}
		var listed managed.JobsPage
		if err := json.Unmarshal(envelope.Data, &listed); err != nil {
			return nil, err
		}
		for _, job := range listed.Jobs {
			if job.Receipt.JobID != "" {
				ids = append(ids, job.Receipt.JobID)
			}
		}
		if !listed.More {
			return ids, nil
		}
		if listed.NextCursor == "" || listed.NextCursor == cursor {
			return nil, errors.New("jobs list page did not advance")
		}
		cursor = listed.NextCursor
	}
	return nil, errors.New("jobs list exceeded the page bound")
}

func requireZeroOccupancy() error {
	cursor := ""
	for page := 0; page < 5; page++ {
		args := []string{"resources", "list", "--limit", "100", "--json"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		envelope, out, diagnostic, code := cliEnvelope(args...)
		if code != 0 || !envelope.OK {
			return fmt.Errorf("exit %d %s %s", code, out.String(), diagnostic.String())
		}
		var listed managed.ResourcesPage
		if err := json.Unmarshal(envelope.Data, &listed); err != nil {
			return err
		}
		for _, resource := range listed.Resources {
			if resource.Occupancy != 0 {
				return fmt.Errorf("resource %s occupancy %d", resource.ID, resource.Occupancy)
			}
		}
		if !listed.More {
			return nil
		}
		if listed.NextCursor == "" || listed.NextCursor == cursor {
			return errors.New("resources list page did not advance")
		}
		cursor = listed.NextCursor
	}
	return errors.New("resources list exceeded the page bound")
}

const (
	incarnationWaitBudget = 2 * time.Second
	incarnationWaitPoll   = 25 * time.Millisecond
)

type pendingIncarnation struct {
	Expected platform.ProcessIdentity
	Observed platform.ProcessIdentity
	Zombie   bool
}

type incarnationWaitFailure struct {
	Pending  []pendingIncarnation
	Expected platform.ProcessIdentity
	Err      error
}

func (e *incarnationWaitFailure) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("expected %s observation failed: %v", formatIdentity(e.Expected), e.Err)
	}
	parts := make([]string, 0, len(e.Pending))
	for _, item := range e.Pending {
		parts = append(parts, fmt.Sprintf("expected %s observed %s zombie=%t", formatIdentity(item.Expected), formatIdentity(item.Observed), item.Zombie))
	}
	return "incarnation still recorded: " + strings.Join(parts, "; ")
}

func (e *incarnationWaitFailure) Unwrap() error { return e.Err }

func formatIdentity(id platform.ProcessIdentity) string {
	return fmt.Sprintf("boot_id=%s pid=%d started_seconds=%d started_micros=%d pgid=%d sid=%d uid=%d", id.BootID, id.PID, id.StartedSeconds, id.StartedMicros, id.PGID, id.SID, id.UID)
}

func sameRecordedIncarnation(expected, observed platform.ProcessIdentity) bool {
	return expected.BootID == observed.BootID && expected.PID == observed.PID && expected.StartedSeconds == observed.StartedSeconds && expected.StartedMicros == observed.StartedMicros && expected.PGID == observed.PGID && expected.SID == observed.SID && expected.UID == observed.UID
}

func ownedProcessesStopped(root string, qualified map[string]bool) error {
	jobsDir := filepath.Join(root, "jobs")
	entries, err := os.ReadDir(jobsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var problems []string
	var ids []platform.ProcessIdentity
	for _, entry := range entries {
		if !entry.IsDir() {
			problems = append(problems, "unexpected job entry "+entry.Name())
			continue
		}
		id := entry.Name()
		if !qualified[id] {
			problems = append(problems, "unqualified job directory "+id)
		}
		found, failed := recordedIncarnations(filepath.Join(jobsDir, id))
		problems = append(problems, failed...)
		ids = append(ids, found...)
	}
	if len(ids) > 0 {
		if err := waitIncarnations(ids, platform.ObserveProcess, time.Now, time.Sleep); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func recordedIncarnations(jobDir string) ([]platform.ProcessIdentity, []string) {
	var ids []platform.ProcessIdentity
	var problems []string
	jobID := filepath.Base(jobDir)
	for _, name := range []struct{ file, field string }{{"intent.json", "supervisor"}, {"start.json", "process"}} {
		identity, present, err := readRecordedIdentity(filepath.Join(jobDir, name.file), name.field)
		if err != nil {
			problems = append(problems, jobID+": "+name.file+": "+err.Error())
			continue
		}
		if present {
			ids = append(ids, identity)
		}
	}
	return ids, problems
}

func readRecordedIdentity(path, field string) (platform.ProcessIdentity, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return platform.ProcessIdentity{}, false, nil
	}
	if err != nil {
		return platform.ProcessIdentity{}, false, errors.New("identity file is unreadable")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return platform.ProcessIdentity{}, false, errors.New("malformed identity")
	}
	rawField, ok := body[field]
	if !ok || bytes.Equal(bytes.TrimSpace(rawField), []byte("null")) {
		return platform.ProcessIdentity{}, false, errors.New("missing identity")
	}
	identity, err := decodeProcessIdentity(rawField)
	if err != nil {
		return platform.ProcessIdentity{}, false, err
	}
	return identity, true, nil
}

func decodeProcessIdentity(raw json.RawMessage) (platform.ProcessIdentity, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return platform.ProcessIdentity{}, errors.New("missing identity")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return platform.ProcessIdentity{}, errors.New("malformed identity")
	}
	bootRaw, ok := fields["boot_id"]
	if !ok || bytes.Equal(bytes.TrimSpace(bootRaw), []byte("null")) {
		return platform.ProcessIdentity{}, errors.New("invalid identity")
	}
	var boot string
	if err := json.Unmarshal(bootRaw, &boot); err != nil || boot == "" {
		return platform.ProcessIdentity{}, errors.New("invalid identity")
	}
	pid, err := identityFieldInt(fields, "pid")
	if err != nil || pid <= 0 || pid > int64(math.MaxInt) {
		return platform.ProcessIdentity{}, errors.New("invalid identity")
	}
	started, err := identityFieldInt(fields, "started_seconds")
	if err != nil || started <= 0 {
		return platform.ProcessIdentity{}, errors.New("invalid identity")
	}
	micros, err := identityFieldInt(fields, "started_micros")
	if err != nil || micros < 0 {
		return platform.ProcessIdentity{}, errors.New("invalid identity")
	}
	pgid, err := identityFieldInt(fields, "pgid")
	if err != nil || pgid <= 0 || pgid > int64(math.MaxInt) {
		return platform.ProcessIdentity{}, errors.New("invalid identity")
	}
	sid, err := identityFieldInt(fields, "sid")
	if err != nil || sid <= 0 || sid > int64(math.MaxInt) {
		return platform.ProcessIdentity{}, errors.New("invalid identity")
	}
	uid, err := identityFieldInt(fields, "uid")
	if err != nil || uid < 0 || uid > math.MaxUint32 {
		return platform.ProcessIdentity{}, errors.New("invalid identity")
	}
	return platform.ProcessIdentity{BootID: boot, PID: int(pid), StartedSeconds: started, StartedMicros: micros, PGID: int(pgid), SID: int(sid), UID: uint32(uid)}, nil
}

func identityFieldInt(fields map[string]json.RawMessage, key string) (int64, error) {
	raw, ok := fields[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, errors.New("invalid identity")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return 0, errors.New("invalid identity")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return 0, errors.New("invalid identity")
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("invalid identity")
	}
	parsed, err := number.Int64()
	if err != nil {
		return 0, errors.New("invalid identity")
	}
	return parsed, nil
}

func waitIncarnations(ids []platform.ProcessIdentity, observe func(int) (platform.ProcessObservation, error), now func() time.Time, sleep func(time.Duration)) error {
	if len(ids) == 0 {
		return nil
	}
	deadline := now().Add(incarnationWaitBudget)
	pending := append([]platform.ProcessIdentity(nil), ids...)
	for {
		still := make([]pendingIncarnation, 0, len(pending))
		next := make([]platform.ProcessIdentity, 0, len(pending))
		for _, expected := range pending {
			observed, err := observe(expected.PID)
			if err != nil {
				if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT) {
					continue
				}
				return &incarnationWaitFailure{Expected: expected, Err: err}
			}
			if !sameRecordedIncarnation(expected, observed.Identity) {
				continue
			}
			still = append(still, pendingIncarnation{Expected: expected, Observed: observed.Identity, Zombie: observed.Zombie})
			next = append(next, expected)
		}
		if len(still) == 0 {
			return nil
		}
		if !now().Before(deadline) {
			return &incarnationWaitFailure{Pending: still}
		}
		pending = next
		sleep(incarnationWaitPoll)
	}
}

func socketGone(path string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("controller socket: %w", err)
		}
		if time.Now().After(deadline) {
			return errors.New("controller socket remains after shutdown")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runAdmissionID(raw []byte) string {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event struct {
			Kind string `json:"kind"`
			Data struct {
				JobID string `json:"job_id"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if event.Kind == "admission" && event.Data.JobID != "" {
			return event.Data.JobID
		}
	}
	return ""
}

var _ = ginkgo.Describe("Task023M2aCLI", ginkgo.Label("integration"), func() {
	ginkgo.It("materializes accepted defaults through the real CLI and daemon", func() {
		harness := startM2aFixture()
		parent, root := harness.parent, harness.root
		configPath, forwarder := harness.configPath, harness.forwarder

		source := filepath.Join(parent, "input")
		gomega.Expect(os.MkdirAll(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "source", "input.txt"), []byte("input\n"), 0600)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "source", "check.md"), []byte("check\n"), 0600)).To(gomega.Succeed())
		store, err := snapshot.OpenStore(root.Path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		manualSealed, err := store.Seal(context.Background(), source, []string{"source/input.txt", "source/check.md"}, snapshot.Provenance{EvidenceClass: "development"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-one", Kind: "local", ExecutionMode: "managed", Capacity: 4, Revision: 1, Adapter: managed.Adapter{QualificationReference: "cli-integration"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
		resourcePath := filepath.Join(parent, "resource.json")
		writeM2aJSON(resourcePath, resource)
		out, diagnostic, code := cliOut("resource", "register", "--file", resourcePath, "--json")
		gomega.Expect(code).To(gomega.BeZero(), diagnostic.String())

		autoDigest := strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:")
		auto := cliJob(source, autoDigest, []string{"source/input.txt"})
		autoRaw, err := json.Marshal(auto)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		explicitPath := filepath.Join(parent, "explicit.json")
		gomega.Expect(os.WriteFile(explicitPath, withDeadlines(autoRaw, `{"queue_seconds":15,"prepare_seconds":15,"run_seconds":15,"manual_seconds":0,"collect_seconds":15,"cleanup_seconds":15}`), 0600)).To(gomega.Succeed())
		omittedPath := filepath.Join(parent, "omitted.json")
		gomega.Expect(os.WriteFile(omittedPath, withDeadlines(autoRaw, `{"manual_seconds":0}`), 0600)).To(gomega.Succeed())

		var explicit managed.Receipt
		projection, submits := forwarder.around(func() {
			explicit, _, diagnostic, code = submitCLI(explicitPath, "explicit-key")
			gomega.Expect(code).To(gomega.BeZero(), diagnostic.String())
			harness.track(explicit.JobID)
		})
		gomega.Expect(projection).To(gomega.BeZero())
		gomega.Expect(submits).To(gomega.Equal(int64(1)))
		waitCLI(explicit.JobID)
		stored := storedCLIJob(root.DBPath, "explicit-key")
		gomega.Expect(stored.Deadlines).To(gomega.Equal(managed.Deadlines{QueueSeconds: 15, PrepareSeconds: 15, RunSeconds: 15, CollectSeconds: 15, CleanupSeconds: 15}))
		var replay managed.Receipt
		projection, submits = forwarder.around(func() {
			replay, _, diagnostic, code = submitCLI(explicitPath, "explicit-key")
			gomega.Expect(code).To(gomega.BeZero(), diagnostic.String())
		})
		gomega.Expect(projection).To(gomega.BeZero())
		gomega.Expect(submits).To(gomega.Equal(int64(1)))
		gomega.Expect(replay.JobID).To(gomega.Equal(explicit.JobID))
		gomega.Expect(replay.Admission).To(gomega.Equal(explicit.Admission))

		var omitted managed.Receipt
		projection, submits = forwarder.around(func() {
			omitted, _, diagnostic, code = submitCLI(omittedPath, "omitted-key")
			gomega.Expect(code).To(gomega.BeZero(), diagnostic.String())
			harness.track(omitted.JobID)
		})
		gomega.Expect(projection).To(gomega.Equal(int64(1)))
		gomega.Expect(submits).To(gomega.Equal(int64(1)))
		waitCLI(omitted.JobID)
		stored = storedCLIJob(root.DBPath, "omitted-key")
		gomega.Expect(stored.Deadlines).To(gomega.Equal(managed.Deadlines{QueueSeconds: 40, PrepareSeconds: 40, RunSeconds: 40, CollectSeconds: 40, CleanupSeconds: 40}))

		projection, _ = forwarder.around(func() {
			out, diagnostic, code = cliOut("run", "--spec", omittedPath, "--request-key", "run-key", "--json")
			harness.track(runAdmissionID(out.Bytes()))
			gomega.Expect(code).To(gomega.BeZero(), "%s %s", out.String(), diagnostic.String())
		})
		gomega.Expect(projection).To(gomega.Equal(int64(1)))
		gomega.Expect(out.String()).To(gomega.ContainSubstring(`"kind":"admission"`))
		gomega.Expect(out.String()).To(gomega.ContainSubstring(`"kind":"final"`))
		gomega.Expect(out.String()).To(gomega.ContainSubstring(`"final_verdict":"PASS"`))
		gomega.Expect(storedCLIJob(root.DBPath, "run-key").Deadlines.RunSeconds).To(gomega.Equal(int64(40)))

		writeM2aConfig(configPath, 1, phaseDeadlines(55))
		file, err := os.ReadFile(configPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(file)).To(gomega.ContainSubstring(`"queue_seconds":55`))
		var edited managed.Receipt
		projection, submits = forwarder.around(func() {
			edited, _, diagnostic, code = submitCLI(omittedPath, "unaccepted-file-key")
			gomega.Expect(code).To(gomega.BeZero(), diagnostic.String())
			harness.track(edited.JobID)
		})
		gomega.Expect(projection).To(gomega.Equal(int64(1)))
		gomega.Expect(submits).To(gomega.Equal(int64(1)))
		waitCLI(edited.JobID)
		gomega.Expect(storedCLIJob(root.DBPath, "unaccepted-file-key").Deadlines.QueueSeconds).To(gomega.Equal(int64(40)))
		var acceptedRaw []byte
		withStateDB(root.DBPath, func(_ func(string, ...any) int, query func(string, ...any) []byte) {
			acceptedRaw = query(`SELECT document_json FROM managed_effective_config WHERE singleton=1`)
		})
		accepted, _, err := config.Parse(acceptedRaw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(accepted.DefaultDeadlines.QueueSeconds).To(gomega.Equal(int64(40)))

		gomega.Expect(harness.current.stopErr()).To(gomega.Succeed())
		gomega.Eventually(func() bool {
			_, err := os.Lstat(root.SocketPath)
			return os.IsNotExist(err)
		}, 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
		writeM2aConfig(configPath, 2, phaseDeadlines(41))
		harness.start(configPath)
		jobs, events, admissions := stateCounts(root.DBPath)
		projection, submits = forwarder.around(func() {
			_, out, diagnostic, code = submitCLI(omittedPath, "omitted-key")
		})
		gomega.Expect(code).To(gomega.Equal(2), diagnostic.String())
		gomega.Expect(out.String()).To(gomega.ContainSubstring("CONFLICT"))
		gomega.Expect(projection).To(gomega.Equal(int64(1)))
		gomega.Expect(submits).To(gomega.Equal(int64(1)))
		afterJobs, afterEvents, afterAdmissions := stateCounts(root.DBPath)
		gomega.Expect(afterJobs).To(gomega.Equal(jobs))
		gomega.Expect(afterEvents).To(gomega.Equal(events))
		gomega.Expect(afterAdmissions).To(gomega.Equal(admissions))
		gomega.Expect(storedCLIJob(root.DBPath, "omitted-key").Deadlines.QueueSeconds).To(gomega.Equal(int64(40)))

		manualDigest := strings.TrimPrefix(manualSealed.Manifest.TreeSHA256, "sha256:")
		manual := cliJob(source, manualDigest, []string{"source/input.txt", "source/check.md"})
		manual.Mode = "manual"
		manual.Manual = &managed.Manual{ScenarioDocument: "source/check.md", RequiredScenarios: []string{"check"}}
		manualRaw, err := json.Marshal(manual)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		manualPath := filepath.Join(parent, "manual.json")
		gomega.Expect(os.WriteFile(manualPath, withDeadlines(manualRaw, `{"run_seconds":0}`), 0600)).To(gomega.Succeed())
		var manualReceipt managed.Receipt
		projection, submits = forwarder.around(func() {
			manualReceipt, _, diagnostic, code = submitCLI(manualPath, "manual-key")
			gomega.Expect(code).To(gomega.BeZero(), diagnostic.String())
			harness.track(manualReceipt.JobID)
		})
		gomega.Expect(manualReceipt.JobID).NotTo(gomega.BeEmpty())
		gomega.Expect(projection).To(gomega.Equal(int64(1)))
		gomega.Expect(submits).To(gomega.Equal(int64(1)))
		manualStored := storedCLIJob(root.DBPath, "manual-key")
		gomega.Expect(manualStored.Deadlines.ManualSeconds).To(gomega.Equal(int64(41)))
		gomega.Expect(manualStored.Deadlines.RunSeconds).To(gomega.Equal(int64(0)))
		gomega.Expect(manualStored.Deadlines.QueueSeconds).To(gomega.Equal(int64(41)))
		gomega.Expect(manualStored.Mode).To(gomega.Equal("manual"))
	})

	ginkgo.It("refuses omitted budgets when the accepted row is missing or corrupt", func() {
		harness := startM2aFixture()
		parent, root, forwarder := harness.parent, harness.root, harness.forwarder
		specPath := filepath.Join(parent, "omitted.json")
		gomega.Expect(os.WriteFile(specPath, []byte(`{"mode":"auto","deadlines":{"manual_seconds":0}}`), 0600)).To(gomega.Succeed())
		var saved []byte
		withStateDB(root.DBPath, func(_ func(string, ...any) int, query func(string, ...any) []byte) {
			saved = append([]byte(nil), query(`SELECT document_json FROM managed_effective_config WHERE singleton=1`)...)
		})
		db, err := sqlite.Open(root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		_, err = db.Exec(`UPDATE managed_effective_config SET document_json=? WHERE singleton=1`, []byte("{"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		projection, submits := forwarder.around(func() {
			_, out, diagnostic, code := submitCLI(specPath, "corrupt-key")
			gomega.Expect(code).To(gomega.Equal(2))
			gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("accepted configuration projection is unavailable"))
			gomega.Expect(out.Len()).To(gomega.BeZero())
		})
		gomega.Expect(projection).To(gomega.Equal(int64(1)))
		gomega.Expect(submits).To(gomega.BeZero())
		corruptJobs, corruptEvents, corruptAdmissions := stateCounts(root.DBPath)
		gomega.Expect(corruptJobs).To(gomega.BeZero())
		gomega.Expect(corruptEvents).To(gomega.BeZero())
		gomega.Expect(corruptAdmissions).To(gomega.BeZero())

		_, err = db.Exec(`UPDATE managed_effective_config SET document_json=? WHERE singleton=1`, saved)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DELETE FROM managed_effective_config`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		projection, submits = forwarder.around(func() {
			_, out, diagnostic, code := submitCLI(specPath, "missing-key")
			gomega.Expect(code).To(gomega.Equal(2))
			gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("accepted configuration projection is unavailable"))
			gomega.Expect(out.Len()).To(gomega.BeZero())
		})
		gomega.Expect(projection).To(gomega.Equal(int64(1)))
		gomega.Expect(submits).To(gomega.BeZero())
		jobs, events, admissions := stateCounts(root.DBPath)
		gomega.Expect(jobs).To(gomega.BeZero())
		gomega.Expect(events).To(gomega.BeZero())
		gomega.Expect(admissions).To(gomega.BeZero())
	})
})

type scriptClock struct {
	at time.Time
}

func (c *scriptClock) now() time.Time { return c.at }

func (c *scriptClock) sleep(d time.Duration) { c.at = c.at.Add(d) }

func testIdentity(pid int) platform.ProcessIdentity {
	return platform.ProcessIdentity{BootID: "boot-1", PID: pid, StartedSeconds: 10, StartedMicros: 20, PGID: pid, SID: pid, UID: 501}
}

func requireWaitFailure(err error) *incarnationWaitFailure {
	var failure *incarnationWaitFailure
	gomega.Expect(errors.As(err, &failure)).To(gomega.BeTrue())
	return failure
}

var _ = ginkgo.Describe("Task023M2a incarnation", ginkgo.Label("integration"), func() {
	ginkgo.It("waits until the same identity is gone", func() {
		id := testIdentity(41)
		clock := &scriptClock{at: time.Unix(1_700_000_000, 0)}
		calls := 0
		err := waitIncarnations([]platform.ProcessIdentity{id}, func(pid int) (platform.ProcessObservation, error) {
			gomega.Expect(pid).To(gomega.Equal(id.PID))
			calls++
			if calls == 1 {
				gomega.Expect(clock.at).To(gomega.Equal(time.Unix(1_700_000_000, 0)))
				return platform.ProcessObservation{Identity: id}, nil
			}
			return platform.ProcessObservation{}, syscall.ESRCH
		}, clock.now, clock.sleep)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(calls).To(gomega.Equal(2))
		gomega.Expect(clock.at).To(gomega.Equal(time.Unix(1_700_000_000, 0).Add(incarnationWaitPoll)))
	})

	ginkgo.It("fails when the identity stays through the deadline", func() {
		first, second := testIdentity(42), testIdentity(43)
		clock := &scriptClock{at: time.Unix(1_700_000_000, 0)}
		calls := map[int]int{}
		err := waitIncarnations([]platform.ProcessIdentity{first, second}, func(pid int) (platform.ProcessObservation, error) {
			calls[pid]++
			if pid == first.PID {
				return platform.ProcessObservation{Identity: first}, nil
			}
			return platform.ProcessObservation{Identity: second}, nil
		}, clock.now, clock.sleep)
		failure := requireWaitFailure(err)
		gomega.Expect(failure.Err).NotTo(gomega.HaveOccurred())
		gomega.Expect(failure.Pending).To(gomega.Equal([]pendingIncarnation{{Expected: first, Observed: first}, {Expected: second, Observed: second}}))
		gomega.Expect(clock.at).To(gomega.Equal(time.Unix(1_700_000_000, 0).Add(incarnationWaitBudget)))
		gomega.Expect(calls[first.PID]).To(gomega.Equal(calls[second.PID]))
		gomega.Expect(calls[first.PID]).To(gomega.Equal(int(incarnationWaitBudget/incarnationWaitPoll) + 1))
	})

	ginkgo.It("waits out a zombie that later disappears", func() {
		id := testIdentity(44)
		clock := &scriptClock{at: time.Unix(1_700_000_000, 0)}
		calls := 0
		err := waitIncarnations([]platform.ProcessIdentity{id}, func(int) (platform.ProcessObservation, error) {
			calls++
			if calls == 1 {
				return platform.ProcessObservation{Identity: id, Zombie: true}, nil
			}
			return platform.ProcessObservation{}, syscall.ESRCH
		}, clock.now, clock.sleep)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(calls).To(gomega.Equal(2))
	})

	ginkgo.It("fails when a zombie remains at the deadline", func() {
		id := testIdentity(45)
		clock := &scriptClock{at: time.Unix(1_700_000_000, 0)}
		err := waitIncarnations([]platform.ProcessIdentity{id}, func(int) (platform.ProcessObservation, error) {
			return platform.ProcessObservation{Identity: id, Zombie: true}, nil
		}, clock.now, clock.sleep)
		failure := requireWaitFailure(err)
		gomega.Expect(failure.Pending).To(gomega.Equal([]pendingIncarnation{{Expected: id, Observed: id, Zombie: true}}))
		gomega.Expect(clock.at).To(gomega.Equal(time.Unix(1_700_000_000, 0).Add(incarnationWaitBudget)))
	})

	ginkgo.It("treats a different identity as gone", func() {
		id := testIdentity(46)
		other := id
		other.BootID = "boot-2"
		clock := &scriptClock{at: time.Unix(1_700_000_000, 0)}
		calls := 0
		err := waitIncarnations([]platform.ProcessIdentity{id}, func(int) (platform.ProcessObservation, error) {
			calls++
			return platform.ProcessObservation{Identity: other}, nil
		}, clock.now, clock.sleep)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(calls).To(gomega.Equal(1))
		gomega.Expect(clock.at).To(gomega.Equal(time.Unix(1_700_000_000, 0)))
	})

	ginkgo.It("treats ESRCH as gone", func() {
		id := testIdentity(47)
		clock := &scriptClock{at: time.Unix(1_700_000_000, 0)}
		calls := 0
		err := waitIncarnations([]platform.ProcessIdentity{id}, func(int) (platform.ProcessObservation, error) {
			calls++
			return platform.ProcessObservation{}, syscall.ESRCH
		}, clock.now, clock.sleep)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(calls).To(gomega.Equal(1))
		gomega.Expect(clock.at).To(gomega.Equal(time.Unix(1_700_000_000, 0)))
	})

	ginkgo.It("treats ENOENT as gone", func() {
		id := testIdentity(48)
		clock := &scriptClock{at: time.Unix(1_700_000_000, 0)}
		err := waitIncarnations([]platform.ProcessIdentity{id}, func(int) (platform.ProcessObservation, error) {
			return platform.ProcessObservation{}, syscall.ENOENT
		}, clock.now, clock.sleep)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(clock.at).To(gomega.Equal(time.Unix(1_700_000_000, 0)))
	})

	ginkgo.It("stops on an observation error", func() {
		id := testIdentity(49)
		clock := &scriptClock{at: time.Unix(1_700_000_000, 0)}
		uncertain := errors.New("observation unavailable")
		calls := 0
		err := waitIncarnations([]platform.ProcessIdentity{id}, func(int) (platform.ProcessObservation, error) {
			calls++
			return platform.ProcessObservation{}, uncertain
		}, clock.now, clock.sleep)
		failure := requireWaitFailure(err)
		gomega.Expect(failure.Err).To(gomega.MatchError(uncertain))
		gomega.Expect(failure.Expected).To(gomega.Equal(id))
		gomega.Expect(failure.Pending).To(gomega.BeEmpty())
		gomega.Expect(calls).To(gomega.Equal(1))
		gomega.Expect(clock.at).To(gomega.Equal(time.Unix(1_700_000_000, 0)))
	})

	ginkgo.It("rejects a malformed present record", func() {
		dir, err := os.MkdirTemp("", "hp-m2a-identity-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(dir)
		valid := platform.ProcessIdentity{BootID: "boot-1", PID: 7, StartedSeconds: 3, StartedMicros: 0, PGID: 7, SID: 7, UID: 0}
		raw, err := json.Marshal(map[string]any{"supervisor": valid})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		path := filepath.Join(dir, "intent.json")
		gomega.Expect(os.WriteFile(path, raw, 0600)).To(gomega.Succeed())
		got, present, err := readRecordedIdentity(path, "supervisor")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(present).To(gomega.BeTrue())
		gomega.Expect(got).To(gomega.Equal(valid))

		rejected := []string{
			`{"supervisor":{"pid":4242}}`,
			`{"supervisor":{"boot_id":"boot-1","pid":7,"started_seconds":3,"pgid":7,"sid":7,"uid":0}}`,
			`{"supervisor":{"boot_id":"boot-1","pid":7,"started_seconds":3,"started_micros":0,"pgid":7,"sid":7,"uid":"501"}}`,
			`{"supervisor":{"boot_id":"boot-1","pid":7,"started_seconds":3,"started_micros":null,"pgid":7,"sid":7,"uid":0}}`,
			`{"supervisor":{"boot_id":"","pid":7,"started_seconds":3,"started_micros":0,"pgid":7,"sid":7,"uid":0}}`,
			`{"supervisor":null}`,
			`{}`,
			`{`,
		}
		for _, body := range rejected {
			gomega.Expect(os.WriteFile(path, []byte(body), 0600)).To(gomega.Succeed())
			_, present, err = readRecordedIdentity(path, "supervisor")
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(present).To(gomega.BeFalse())
		}
	})
})
