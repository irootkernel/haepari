package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

func privateFixtureDirectory() string {
	root, err := os.MkdirTemp("/tmp", "hp-runner-")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	root, err = filepath.EvalSymlinks(root)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return root
}
func requestFixture(root string) Request {
	return Request{Schema: "haepari.runner-request/v1", Identity: managed.LocalIdentity{JobID: "job-fixture", ResourceID: "local-fixture", ConfigRevision: 1, Slot: 0, LeaseGeneration: 1, RunID: "run-fixture", Nonce: "nonce-fixture"}, BootID: "boot-fixture", WorkDir: filepath.Join(root, "workspace"), Argv: []string{"/bin/true"}, Environment: []string{"PATH=/usr/bin:/bin"}, Deadline: time.Now().Add(5 * time.Second).UTC(), RunSeconds: 5, TerminationSeconds: 2, LogInode: 1}
}

var _ = ginkgo.Describe("Runner request and receipt boundaries", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses omitted or null required zero-valued identity fields", func() {
		for _, value := range []string{"omit", "null"} {
			root := privateFixtureDirectory()
			defer os.RemoveAll(root)
			q := requestFixture(root)
			raw, err := json.Marshal(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var fields map[string]json.RawMessage
			gomega.Expect(json.Unmarshal(raw, &fields)).To(gomega.Succeed())
			var identity map[string]json.RawMessage
			gomega.Expect(json.Unmarshal(fields["identity"], &identity)).To(gomega.Succeed())
			if value == "omit" {
				delete(identity, "Slot")
			} else {
				identity["Slot"] = json.RawMessage("null")
			}
			fields["identity"], err = json.Marshal(identity)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			raw, err = json.Marshal(fields)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.WriteFile(filepath.Join(root, "request.json"), raw, 0600)).To(gomega.Succeed())
			_, err = Inspect(filepath.Join(root, "request.json"))
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		}
	})
	ginkgo.It("keeps recovery guidance when joined errors also contain invalid or conflicting evidence", func() {
		for _, blocked := range []error{ErrAmbiguous, ErrUnproven} {
			for _, other := range []error{ErrInvalid, ErrConflict} {
				for _, joined := range []error{errors.Join(blocked, other), errors.Join(other, blocked)} {
					failure := runnerError(joined)
					gomega.Expect(failure.Code).To(gomega.Equal(managed.RecoveryBlocked))
					gomega.Expect(failure.Retry).To(gomega.Equal("reconcile"))
					gomega.Expect(failure.Message).To(gomega.Equal(blocked.Error()))
				}
			}
		}
	})
	ginkgo.It("bounds watch without starting a prepared workload and explains malformed usage", func() {
		root := privateFixtureDirectory()
		defer os.RemoveAll(root)
		q := requestFixture(root)
		q.Deadline = time.Now().Add(-time.Second).UTC()
		q.TerminationSeconds = 1
		gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
		var stdout, stderr bytes.Buffer
		before := time.Now()
		gomega.Expect(Command([]string{"watch", "--request", filepath.Join(root, "request.json")}, &stdout, &stderr)).To(gomega.Equal(2))
		gomega.Expect(time.Since(before)).To(gomega.BeNumerically("<", 3*time.Second))
		envelope, err := managed.DecodeEnvelope(stdout.Bytes())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(envelope.Error.Code).To(gomega.Equal(managed.RecoveryBlocked))
		gomega.Expect(envelope.Error.Retry).To(gomega.Equal("reconcile"))
		_, err = os.Stat(filepath.Join(root, "intent.json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		for _, args := range [][]string{nil, {"stat"}, {"inspect"}, {"inspect", "--request"}, {"inspect", "--unknown"}, {"inspect", "--request", "relative.json", "extra"}} {
			stdout.Reset()
			stderr.Reset()
			gomega.Expect(Command(args, &stdout, &stderr)).To(gomega.Equal(2))
			gomega.Expect(stdout.Bytes()).To(gomega.BeEmpty())
			gomega.Expect(stderr.String()).To(gomega.ContainSubstring(usage))
		}
	})
	ginkgo.It("accepts durable cancellation from a competing lock holder and concurrent writers", func() {
		root := privateFixtureDirectory()
		defer os.RemoveAll(root)
		q := requestFixture(root)
		gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
		d, err := openDirectory(root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer d.close()
		lock, err := d.open("cancel.lock", unix.O_RDWR|unix.O_CREAT)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer lock.Close()
		gomega.Expect(unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)).To(gomega.Succeed())
		path := filepath.Join(root, "request.json")
		done := make(chan error, 1)
		go func() { done <- Cancel(path) }()
		select {
		case err := <-done:
			ginkgo.Fail("contended cancellation returned before durable intent: " + fmt.Sprint(err))
		case <-time.After(50 * time.Millisecond):
		}
		gomega.Expect(d.write("cancel.json", Cancellation{Schema: "haepari.runner-cancel/v1", Binding: q.Binding(), RequestedAt: time.Now().UTC()})).To(gomega.Succeed())
		gomega.Eventually(done, time.Second).Should(gomega.Receive(gomega.BeNil()))
		gomega.Expect(unix.Flock(int(lock.Fd()), unix.LOCK_UN)).To(gomega.Succeed())
		gomega.Expect(os.Remove(filepath.Join(root, "cancel.json"))).To(gomega.Succeed())
		errorsReturned := make(chan error, 16)
		for i := 0; i < cap(errorsReturned); i++ {
			go func() { errorsReturned <- Cancel(path) }()
		}
		for i := 0; i < cap(errorsReturned); i++ {
			gomega.Eventually(errorsReturned, time.Second).Should(gomega.Receive(gomega.BeNil()))
		}
	})
	ginkgo.It("bounds cancellation when contention never publishes an intent", func() {
		root := privateFixtureDirectory()
		defer os.RemoveAll(root)
		q := requestFixture(root)
		q.TerminationSeconds = 1
		gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
		d, err := openDirectory(root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer d.close()
		lock, err := d.open("cancel.lock", unix.O_RDWR|unix.O_CREAT)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer lock.Close()
		gomega.Expect(unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)).To(gomega.Succeed())
		done := make(chan error, 1)
		go func() { done <- Cancel(filepath.Join(root, "request.json")) }()
		gomega.Eventually(done, 2*time.Second).Should(gomega.Receive(gomega.MatchError(ErrUnproven)))
		_, err = os.Stat(filepath.Join(root, "cancel.json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("admits only inconclusive or cancelled outcomes when workload exec never started", func() {
		for _, outcome := range []managed.WorkloadOutcome{"pass", "fail", "timed_out", "inconclusive", "cancelled"} {
			gomega.Expect(validOutcome(managed.LocalOutcome{WorkloadOutcome: outcome})).To(gomega.Equal(outcome == "inconclusive" || outcome == "cancelled"))
		}
	})
	ginkgo.It("rejects a recycled leader incarnation before accepting a same-session claim", func() {
		original := processCensus
		defer func() { processCensus = original }()
		leader := platform.ProcessIdentity{BootID: "boot", PID: 41, PGID: 41, SID: 41, StartedSeconds: 10, UID: 501}
		recycled := leader
		recycled.StartedSeconds = 20
		processCensus = func() ([]platform.ProcessObservation, error) {
			return []platform.ProcessObservation{{Identity: recycled}}, nil
		}
		tracker := newTracker(leader)
		alive, err := tracker.scan()
		gomega.Expect(errors.Is(err, ErrUnproven)).To(gomega.BeTrue())
		gomega.Expect(alive).To(gomega.BeNil())
		gomega.Expect(tracker.known[leader.PID]).To(gomega.Equal(leader))
	})
	ginkgo.It("resolves declared PATH and slash-relative commands while skipping non-executable candidates", func() {
		root := privateFixtureDirectory()
		defer os.RemoveAll(root)
		for _, name := range []string{"first", "second"} {
			gomega.Expect(os.Mkdir(filepath.Join(root, name), 0700)).To(gomega.Succeed())
		}
		gomega.Expect(os.WriteFile(filepath.Join(root, "first/tool"), []byte("not executable"), 0600)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(root, "second/tool"), []byte("#!/bin/sh\n"), 0700)).To(gomega.Succeed())
		command, err := resolveCommand("tool", root, []string{"PATH=first:second"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(command).To(gomega.Equal(filepath.Join(root, "second/tool")))
		command, err = resolveCommand("./second/tool", root, []string{"PATH=/not-used"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(command).To(gomega.Equal(filepath.Join(root, "second/tool")))
		_, err = resolveCommand("tool", root, []string{"PATH=first"})
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
	ginkgo.It("rejects invalid request limits before any run claim", func() {
		root := privateFixtureDirectory()
		defer os.RemoveAll(root)
		for _, change := range []func(*Request){
			func(q *Request) { q.Argv = make([]string, 4097) },
			func(q *Request) { q.Environment = []string{"PATH=/bin", "PATH=/usr/bin"} },
			func(q *Request) { q.LogInode = 0 },
			func(q *Request) { q.RunSeconds = (1<<63-1)/int64(time.Second) + 1 },
			func(q *Request) { q.TerminationSeconds = (1<<63-1)/int64(time.Second) + 1 },
		} {
			q := requestFixture(root)
			change(&q)
			gomega.Expect(errors.Is(Prepare(root, q), ErrInvalid)).To(gomega.BeTrue())
		}
		_, err := os.Stat(filepath.Join(root, "request.json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("publishes one immutable request when conflicting writers race", func() {
		root := privateFixtureDirectory()
		defer os.RemoveAll(root)
		q := requestFixture(root)
		other := q
		other.Identity.Nonce = "competing-nonce"
		var wait sync.WaitGroup
		errorsReturned := make(chan error, 2)
		for _, request := range []Request{q, other} {
			wait.Add(1)
			go func(q Request) { defer wait.Done(); errorsReturned <- Prepare(root, q) }(request)
		}
		wait.Wait()
		close(errorsReturned)
		succeeded, conflicted := 0, 0
		for err := range errorsReturned {
			if err == nil {
				succeeded++
			} else if errors.Is(err, ErrConflict) {
				conflicted++
			} else {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
		}
		gomega.Expect(succeeded).To(gomega.Equal(1))
		gomega.Expect(conflicted).To(gomega.Equal(1))
	})
	ginkgo.It("rejects duplicate and case-aliased request keys before trusting identity", func() {
		for _, alias := range []bool{false, true} {
			root := privateFixtureDirectory()
			q := requestFixture(root)
			data, err := json.Marshal(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if alias {
				data = bytes.Replace(data, []byte(`"boot_id"`), []byte(`"BOOT_ID"`), 1)
			} else {
				data = bytes.Replace(data, []byte(`"boot_id":`), []byte(`"boot_id":"other","boot_id":`), 1)
			}
			gomega.Expect(os.WriteFile(filepath.Join(root, "request.json"), data, 0600)).To(gomega.Succeed())
			_, err = Inspect(filepath.Join(root, "request.json"))
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
			gomega.Expect(os.RemoveAll(root)).To(gomega.Succeed())
		}
	})
	ginkgo.It("keeps request identity immutable and rejects traversal, links, conflicting nonce and receipt bindings", func() {
		root := privateFixtureDirectory()
		defer os.RemoveAll(root)
		q := requestFixture(root)
		gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
		gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
		changed := q
		changed.Identity.Nonce = "another-nonce"
		gomega.Expect(errors.Is(Prepare(root, changed), ErrConflict)).To(gomega.BeTrue())
		changed = q
		changed.WorkDir = filepath.Join(root, "outside")
		gomega.Expect(errors.Is(changed.Validate(root), ErrInvalid)).To(gomega.BeTrue())
		gomega.Expect(os.Symlink(filepath.Join(root, "request.json"), filepath.Join(root, "intent.json"))).To(gomega.Succeed())
		_, err := Inspect(filepath.Join(root, "request.json"))
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(os.Remove(filepath.Join(root, "intent.json"))).To(gomega.Succeed())
		d, err := openDirectory(root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer d.close()
		binding := q.Binding()
		binding.Identity.RunID = "wrong-run"
		gomega.Expect(d.write("intent.json", Intent{Schema: "haepari.runner-intent/v1", Binding: binding, RecordedAt: time.Now()})).To(gomega.Succeed())
		_, err = Inspect(filepath.Join(root, "request.json"))
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
	})
	ginkgo.It("makes cancellation an exact-request durable replay without signaling a PID", func() {
		root := privateFixtureDirectory()
		defer os.RemoveAll(root)
		q := requestFixture(root)
		gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
		path := filepath.Join(root, "request.json")
		gomega.Expect(Cancel(path)).To(gomega.Succeed())
		before, err := os.ReadFile(filepath.Join(root, "cancel.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(Cancel(path)).To(gomega.Succeed())
		after, err := os.ReadFile(filepath.Join(root, "cancel.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
	})
})
