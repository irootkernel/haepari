//go:build integration

package controller

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// This subprocess entry point uses the same client as a cooperating tool. It
// is selected only by the owning integration spec, never by the suite runner.
func TestTask035LockBorrower(t *testing.T) {
	outcome := os.Getenv("HAEPARI_TEST_BORROWER_OUTCOME")
	if outcome == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	root, err := state.Resolve(os.Getenv("HAEPARI_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := managed.DecodeLockGrant(os.Getenv(managed.LockGrantEnvironment))
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Connect(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	before, err := c.CheckLockGrant(ctx, ref, ref.LockID)
	if err != nil {
		t.Fatal(err)
	}
	checkConflict := func(err error) {
		t.Helper()
		var issue *client.Issue
		if !errors.As(err, &issue) || issue.Code != "CONFLICT" {
			t.Fatalf("borrower mutation: %v", err)
		}
	}
	_, _, err = c.WaitLock(ctx, before.RequestID, time.Second)
	checkConflict(err)
	_, _, err = c.RenewLock(ctx, ref.GrantID)
	checkConflict(err)
	for _, force := range []bool{false, true} {
		reason := ""
		if force {
			reason = "borrower ended"
		}
		_, _, err = c.ReleaseLock(ctx, ref.GrantID, force, reason)
		checkConflict(err)
		_, _, err = c.CancelLock(ctx, before.RequestID, force, reason)
		checkConflict(err)
	}
	_, _, err = c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: ref.LockID, RequestKey: "child", Holder: "borrower"})
	checkConflict(err)
	after, err := c.CheckLockGrant(ctx, ref, ref.LockID)
	if err != nil || after.GrantID != before.GrantID || after.Generation != before.Generation || !after.LivenessDeadline.Equal(*before.LivenessDeadline) || !after.MaxHoldDeadline.Equal(*before.MaxHoldDeadline) {
		t.Fatalf("borrower changed parent: %+v %v", after, err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(after); err != nil {
		t.Fatal(err)
	}
	switch outcome {
	case "success":
	case "failure":
		c.Close()
		os.Exit(7)
	case "cancelled":
		<-time.After(30 * time.Second)
		t.Fatal("borrower was not cancelled")
	default:
		t.Fatal("invalid borrower outcome")
	}
}

func expectLockIssue(err error, code string) {
	ginkgo.GinkgoHelper()
	var issue *client.Issue
	gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
	gomega.Expect(issue.Code).To(gomega.Equal(code))
}
func registerWireLock(c *client.Client, id string) {
	ginkgo.GinkgoHelper()
	_, err := c.Register(context.Background(), []byte(`{"schema":"haepari.resource/v1","kind":"lock","id":"`+id+`"}`))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

var _ = ginkgo.Describe("Task035LockUnix", ginkgo.Label("integration"), func() {
	ginkgo.It("rejects noncanonical outer lock registration fields without resource, clock or history effects", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		ctx := context.Background()
		registerWireLock(c, "registration-owner")
		_, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "registration-owner", RequestKey: "owner", Holder: "owner"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before, err := s.ledger.LocksList("")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var resourceCount int
		var highwater int64
		var marker string
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_resources`).Scan(&resourceCount)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT highwater_ns,wake_marker FROM managed_lock_clock`).Scan(&highwater, &marker)).To(gomega.Succeed())
		changes := s.ledger.Changes()
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", s.root.SocketPath)
		}}
		defer transport.CloseIdleConnections()
		hc := &http.Client{Transport: transport}
		spec := `{"schema":"haepari.resource/v1","id":"case-lock","kind":"lock"}`
		for _, body := range []string{`{"SPEC":` + spec + `}`, `{"spec":` + spec + `,"SPEC":` + spec + `}`, `{"SPEC":` + spec + `,"spec":` + spec + `}`} {
			req, err := http.NewRequest("POST", "http://haepari/v1/resources", bytes.NewBufferString(body))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			req.Header.Set("X-Haepari-Protocol", protocol)
			req.Header.Set("X-Haepari-Protocol-Min", "1")
			req.Header.Set("X-Haepari-Protocol-Max", "1")
			res, err := hc.Do(req)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(res.StatusCode).To(gomega.Equal(400))
			gomega.Expect(res.Body.Close()).To(gomega.Succeed())
			_, err = s.ledger.ResourceStatus("case-lock")
			gomega.Expect(err).To(gomega.MatchError(managed.ErrNotFound))
			var currentCount int
			var currentHighwater int64
			var currentMarker string
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_resources`).Scan(&currentCount)).To(gomega.Succeed())
			gomega.Expect(currentCount).To(gomega.Equal(resourceCount))
			gomega.Expect(db.QueryRow(`SELECT highwater_ns,wake_marker FROM managed_lock_clock`).Scan(&currentHighwater, &currentMarker)).To(gomega.Succeed())
			gomega.Expect(currentHighwater).To(gomega.Equal(highwater))
			gomega.Expect(currentMarker).To(gomega.Equal(marker))
			after, err := s.ledger.LocksList("")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after.Requests).To(gomega.Equal(before.Requests))
			select {
			case <-changes:
				ginkgo.Fail("rejected registration broadcast")
			default:
			}
		}
		_, err = c.Register(ctx, []byte(spec))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		registered, _, err := c.InspectResource(ctx, "case-lock")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(registered.Kind).To(gomega.Equal("lock"))
	})
	ginkgo.It("hands off FIFO and returns scoped replay, queued timeout and ended current state", func() {
		_, c, _ := availabilityFixture(ginkgo.GinkgoT())
		ctx := context.Background()
		registerWireLock(c, "wire-lock")
		gomega.Expect(c.Hello.Capabilities).To(gomega.ContainElement("advisory-lock/v1"))
		first, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "wire-lock", RequestKey: "first", Holder: "owner"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		second, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "wire-lock", RequestKey: "second", Holder: "next"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(second.Current.State).To(gomega.Equal("queued"))
		_, _, err = c.WaitLock(ctx, second.Current.RequestID, 10*time.Millisecond)
		expectLockIssue(err, "WAIT_TIMEOUT")
		page, _, err := c.LocksPage(ctx, "wire-lock", "", 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(page.More).To(gomega.BeTrue())
		gomega.Expect(page.Requests).To(gomega.HaveLen(1))
		next, _, err := c.LocksPage(ctx, "wire-lock", page.NextCursor, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(next.Requests[0].RequestID).To(gomega.Equal(second.Current.RequestID))
		done := make(chan managed.LockReply, 1)
		waitErr := make(chan error, 1)
		go func() {
			reply, _, err := c.WaitLock(ctx, second.Current.RequestID, 2*time.Second)
			done <- reply
			waitErr <- err
		}()
		released, _, err := c.ReleaseLock(ctx, first.Current.GrantID, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(released.Receipt.Operation).To(gomega.Equal("release"))
		select {
		case reply := <-done:
			gomega.Expect(<-waitErr).NotTo(gomega.HaveOccurred())
			gomega.Expect(reply.Current.State).To(gomega.Equal("granted"))
			gomega.Expect(reply.Current.Generation).To(gomega.BeNumerically(">", first.Current.Generation))
		case <-time.After(3 * time.Second):
			ginkgo.Fail("lock wait lost atomic handoff notification")
		}
		replay, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "wire-lock", RequestKey: "first", Holder: "owner"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.Receipt).To(gomega.Equal(first.Receipt))
		gomega.Expect(replay.Current.State).To(gomega.Equal("released"))
		_, _, err = c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "wire-lock", RequestKey: "first", Holder: "different"})
		expectLockIssue(err, "CONFLICT")
	})
	ginkgo.It("validates borrowed grants read-only and retains the parent through child success, failure and cancellation", func() {
		t := ginkgo.GinkgoT()
		s, c, db := availabilityFixture(t)
		ctx := context.Background()
		registerWireLock(c, "parent-lock")
		parent, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "parent-lock", RequestKey: "owner", Holder: "parent"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ref := managed.LockGrantReference{InstallationID: c.Hello.InstallationID, LockID: "parent-lock", GrantID: parent.Current.GrantID, Generation: parent.Current.Generation}
		raw, err := json.Marshal(ref)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		t.Setenv(managed.LockGrantEnvironment, string(raw))
		var highBefore int64
		gomega.Expect(db.QueryRow(`SELECT highwater_ns FROM managed_lock_clock`).Scan(&highBefore)).To(gomega.Succeed())
		for _, outcome := range []string{"success", "failure", "cancelled"} {
			checked, err := c.CheckLockGrant(ctx, ref, "parent-lock")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(checked).To(gomega.Equal(parent.Current))
			executable, err := os.Executable()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			childCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			cmd := exec.CommandContext(childCtx, executable, "-test.run=^TestTask035LockBorrower$")
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HAEPARI_HOME=" + s.root.Path, managed.LockGrantEnvironment + "=" + string(raw), "HAEPARI_TEST_BORROWER_OUTCOME=" + outcome}
			output, err := cmd.StdoutPipe()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var childErrors bytes.Buffer
			cmd.Stderr = &childErrors
			gomega.Expect(cmd.Start()).To(gomega.Succeed())
			// On every branch the child proves read-only validation and all
			// forbidden mutations before it succeeds, fails or is cancelled.
			line, readErr := bufio.NewReader(output).ReadBytes('\n')
			var killErr error
			if outcome == "cancelled" {
				killErr = cmd.Process.Kill()
			}
			waitErr := cmd.Wait()
			cancel()
			gomega.Expect(killErr).NotTo(gomega.HaveOccurred())
			gomega.Expect(readErr).NotTo(gomega.HaveOccurred(), childErrors.String())
			var childRequest managed.LockRequest
			gomega.Expect(json.Unmarshal(line, &childRequest)).To(gomega.Succeed(), childErrors.String())
			gomega.Expect(childRequest).To(gomega.Equal(parent.Current))
			if outcome == "success" {
				gomega.Expect(waitErr).NotTo(gomega.HaveOccurred(), childErrors.String())
			} else {
				gomega.Expect(waitErr).To(gomega.HaveOccurred())
				if outcome == "failure" {
					gomega.Expect(cmd.ProcessState.ExitCode()).To(gomega.Equal(7))
				}
			}
			gomega.Expect(childCtx.Err()).NotTo(gomega.Equal(context.DeadlineExceeded))
			_, _, err = c.RenewLock(ctx, ref.GrantID)
			expectLockIssue(err, "CONFLICT")
			_, _, err = c.ReleaseLock(ctx, ref.GrantID, true, "child failure")
			expectLockIssue(err, "CONFLICT")
			_, _, err = c.CancelLock(ctx, parent.Current.RequestID, true, "child cancellation")
			expectLockIssue(err, "CONFLICT")
			_, _, err = c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "parent-lock", RequestKey: "child", Holder: "borrower"})
			expectLockIssue(err, "CONFLICT")
			checked, err = c.CheckLockGrant(ctx, ref, "parent-lock")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(checked).To(gomega.Equal(parent.Current))
		}
		var highAfter int64
		gomega.Expect(db.QueryRow(`SELECT highwater_ns FROM managed_lock_clock`).Scan(&highAfter)).To(gomega.Succeed())
		gomega.Expect(highAfter).To(gomega.Equal(highBefore))
		for _, change := range []func(*managed.LockGrantReference){func(r *managed.LockGrantReference) { r.InstallationID = "wrong" }, func(r *managed.LockGrantReference) { r.LockID = "wrong" }} {
			bad := ref
			change(&bad)
			_, err = c.CheckLockGrant(ctx, bad, "parent-lock")
			gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
		}
		unknown := ref
		unknown.GrantID = "unknown"
		_, err = c.CheckLockGrant(ctx, unknown, "parent-lock")
		gomega.Expect(errors.Is(err, managed.ErrNotFound)).To(gomega.BeTrue())
		t.Setenv(managed.LockGrantEnvironment, "")
		_, _, err = c.RenewLock(ctx, ref.GrantID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, _, err = c.ReleaseLock(ctx, ref.GrantID, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = c.CheckLockGrant(ctx, ref, "parent-lock")
		gomega.Expect(errors.Is(err, managed.ErrLockExpired)).To(gomega.BeTrue())
		data, err := s.ledger.LocksList("parent-lock")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(data.Requests).To(gomega.HaveLen(1))
	})
	ginkgo.It("rejects case variants, nulls and extra fields on lock pause and resume without effects", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		ctx := context.Background()
		registerWireLock(c, "closed-availability")
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", s.root.SocketPath)
		}}
		defer transport.CloseIdleConnections()
		hc := &http.Client{Transport: transport}
		for _, action := range []string{"pause", "resume"} {
			before, _, err := c.InspectResource(ctx, "closed-availability")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			q := managed.AvailabilityRequest{ResourceID: before.ID, MutationKey: "closed-" + action, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: before.AvailabilityRevision}
			if action == "pause" {
				q.Reason = "closed-body test"
			}
			raw, err := json.Marshal(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var fields map[string]json.RawMessage
			gomega.Expect(json.Unmarshal(raw, &fields)).To(gomega.Succeed())
			fields["expected_lease_generation"] = json.RawMessage("null")
			nullBody, err := json.Marshal(fields)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			for _, body := range [][]byte{bytes.Replace(raw, []byte("resource_id"), []byte("RESOURCE_ID"), 1), nullBody, append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"extra":true}`)...)} {
				req, err := http.NewRequest("POST", "http://haepari/v1/resources/closed-availability/"+action, bytes.NewReader(body))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				req.Header.Set("X-Haepari-Protocol", protocol)
				req.Header.Set("X-Haepari-Protocol-Min", "1")
				req.Header.Set("X-Haepari-Protocol-Max", "1")
				res, err := hc.Do(req)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(res.StatusCode).To(gomega.Equal(400))
				gomega.Expect(res.Body.Close()).To(gomega.Succeed())
				after, _, err := c.InspectResource(ctx, before.ID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(after).To(gomega.Equal(before))
			}
			var receipts int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_availability_receipts WHERE resource_id=?`, before.ID).Scan(&receipts)).To(gomega.Succeed())
			if action == "pause" {
				gomega.Expect(receipts).To(gomega.BeZero())
			} else {
				gomega.Expect(receipts).To(gomega.Equal(1))
			}
			_, _, err = c.SetAvailability(ctx, q, action == "resume")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	})
	ginkgo.It("rejects nested managed acquisition and malformed wire requests before lock effects", func() {
		t := ginkgo.GinkgoT()
		s, c, db := availabilityFixture(t)
		ctx := context.Background()
		registerWireLock(c, "guard-lock")
		_, err := s.ledger.Reserve(managed.ReserveRequest{JobID: "queued-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "parent-reserve"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		chain, err := s.ledger.RunnerParentChain(c.Hello.InstallationID, "queued-job", 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		raw, err := json.Marshal(chain)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		t.Setenv(managed.ParentContextEnvironment, string(raw))
		_, _, err = c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "guard-lock", RequestKey: "nested", Holder: "nested"})
		expectLockIssue(err, "NESTED_ACQUISITION_REJECTED")
		t.Setenv(managed.ParentContextEnvironment, "")
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", s.root.SocketPath)
		}}
		defer transport.CloseIdleConnections()
		hc := &http.Client{Transport: transport}
		for _, body := range []string{`{"LOCK_ID":"guard-lock","request_key":"bad","holder":"x"}`, `{"lock_id":"guard-lock","request_key":"bad","holder":"x","queue_seconds":null}`, `{"lock_id":"guard-lock","request_key":"bad","holder":"x","surprise":true}`} {
			req, err := http.NewRequest("POST", "http://haepari/v1/locks/acquire", bytes.NewBufferString(body))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			req.Header.Set("X-Haepari-Protocol", protocol)
			req.Header.Set("X-Haepari-Protocol-Min", "1")
			req.Header.Set("X-Haepari-Protocol-Max", "1")
			res, err := hc.Do(req)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(res.StatusCode).To(gomega.Equal(400))
			gomega.Expect(res.Body.Close()).To(gomega.Succeed())
		}
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_lock_requests`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
	})
	ginkgo.It("sweeps nearest due grants independently of client lifetime", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var ns atomic.Int64
		ns.Store(time.Now().UnixNano())
		s.ledger.SetLockClock(func() (time.Time, string, error) { return time.Unix(0, ns.Load()).UTC(), "fixture", nil })
		gomega.Expect(s.ledger.LockStartup()).To(gomega.Succeed())
		registerWireLock(c, "expiry-lock")
		first, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "expiry-lock", RequestKey: "unclaimed", Holder: "gone"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		next, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "expiry-lock", RequestKey: "successor", Holder: "waiting"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		workerDone := make(chan struct{})
		go func() { s.scheduleLocks(ctx); close(workerDone) }()
		defer func() { cancel(); <-workerDone }()
		ns.Store(first.Current.LivenessDeadline.Add(time.Second).UnixNano())
		_, err = s.ledger.BindInstallation(s.installationID())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() string {
			data, _, err := c.LocksList(context.Background(), "expiry-lock")
			if err != nil {
				return err.Error()
			}
			for _, r := range data.Requests {
				if r.RequestID == next.Current.RequestID {
					return r.State
				}
			}
			return "missing"
		}, 3*time.Second, 10*time.Millisecond).Should(gomega.Equal("granted"))
		_, _, err = c.RenewLock(context.Background(), first.Current.GrantID)
		expectLockIssue(err, "LOCK_EXPIRED")
	})
	ginkgo.It("wires pause, resume, policy update and removal without managed occupancy or grant revocation", func() {
		_, c, _ := availabilityFixture(ginkgo.GinkgoT())
		ctx := context.Background()
		raw := []byte(`{"schema":"haepari.resource/v1","kind":"lock","id":"policy-lock","capacity":2,"liveness_seconds":11,"max_hold_seconds":3}`)
		_, err := c.Register(ctx, raw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		first, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "policy-lock", RequestKey: "first", Holder: "one"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		second, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "policy-lock", RequestKey: "second", Holder: "two"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		raw = []byte(`{"schema":"haepari.resource/v1","kind":"lock","id":"policy-lock","capacity":1,"revision":2,"liveness_seconds":22,"max_hold_seconds":2}`)
		_, err = c.LockResourceAction(ctx, "update", "policy-lock", raw, 1, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		status, _, err := c.InspectResource(ctx, "policy-lock")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
		gomega.Expect(status.AdvisoryGrants).To(gomega.Equal(2))
		current, _, err := c.RenewLock(ctx, first.Current.GrantID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.Current.LivenessSeconds).To(gomega.Equal(int64(11)))
		gomega.Expect(current.Current.MaxHoldDeadline).To(gomega.Equal(first.Current.MaxHoldDeadline))
		third, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "policy-lock", RequestKey: "third", Holder: "three"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(third.Current.State).To(gomega.Equal("queued"))
		pause := managed.AvailabilityRequest{ResourceID: "policy-lock", MutationKey: "pause-policy", ExpectedConfigRevision: 2, ExpectedAvailabilityRevision: 1, Reason: "hold grants"}
		_, _, err = c.SetAvailability(ctx, pause, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, _, err = c.ReleaseLock(ctx, first.Current.GrantID, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, _, err = c.RenewLock(ctx, second.Current.GrantID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, _, err = c.ReleaseLock(ctx, second.Current.GrantID, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = c.LockResourceAction(ctx, "remove", "policy-lock", nil, 2, false)
		expectLockIssue(err, "RESOURCE_BUSY")
		resume := managed.AvailabilityRequest{ResourceID: "policy-lock", MutationKey: "resume-policy", ExpectedConfigRevision: 2, ExpectedAvailabilityRevision: 2}
		_, _, err = c.SetAvailability(ctx, resume, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		grant, _, err := c.WaitLock(ctx, third.Current.RequestID, time.Second)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(grant.Current.LivenessSeconds).To(gomega.Equal(int64(22)))
		gomega.Expect(grant.Current.MaxHoldSeconds).To(gomega.Equal(int64(2)))
		_, err = c.LockResourceAction(ctx, "recover", "policy-lock", nil, 0, true)
		expectLockIssue(err, "UNSUPPORTED")
		_, _, err = c.CancelLock(ctx, grant.Current.RequestID, true, "operator finish")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = c.LockResourceAction(ctx, "remove", "policy-lock", nil, 2, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})

	ginkgo.It("recovers an acquire whose real Unix reply is lost without a second durable request", func() {
		t := ginkgo.GinkgoT()
		s, c, db := availabilityFixture(t)
		registerWireLock(c, "lost-reply")
		root := s.root
		root.SocketPath = root.Path + "/dropped.sock"
		listener, err := net.Listen("unix", root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var dropped atomic.Bool
		handler := s.routes()
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/v1/locks/acquire" && dropped.CompareAndSwap(false, true) {
				recorded := httptest.NewRecorder()
				handler.ServeHTTP(recorded, r)
				gomega.Expect(recorded.Code).To(gomega.Equal(200))
				connection, _, err := w.(http.Hijacker).Hijack()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(connection.Close()).To(gomega.Succeed())
				return
			}
			handler.ServeHTTP(w, r)
		})}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		defer func() { _ = server.Close(); <-done }()
		direct, err := client.Connect(context.Background(), root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer direct.Close()
		q := managed.LockAcquireRequest{LockID: "lost-reply", RequestKey: "durable-before-send", Holder: "caller"}
		_, envelope, err := direct.AcquireLock(context.Background(), q)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(envelope.Schema).To(gomega.BeEmpty())
		original, err := s.ledger.LocksList("lost-reply")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original.Requests).To(gomega.HaveLen(1))
		replay, _, err := direct.AcquireLock(context.Background(), q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.Current.RequestID).To(gomega.Equal(original.Requests[0].RequestID))
		gomega.Expect(replay.Current.GrantID).To(gomega.Equal(original.Requests[0].GrantID))
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_lock_requests WHERE lock_id='lost-reply'`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.Equal(1))
	})

})
