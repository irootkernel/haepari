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

	ginkgo.It("qualifies advisory rows beside an occupied managed job", func() {
		t := ginkgo.GinkgoT()
		s, c, db := availabilityFixture(t)
		ctx := context.Background()
		occupied, err := s.ledger.Reserve(managed.ReserveRequest{JobID: "queued-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "task021-occupy"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(occupied.GlobalOccupancy).To(gomega.Equal(1))
		held := func() {
			ginkgo.GinkgoHelper()
			current, err := s.ledger.Snapshot(occupied.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current.JobState).To(gomega.Equal(occupied.JobState))
			gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
			gomega.Expect(current.LeaseGeneration).To(gomega.Equal(occupied.LeaseGeneration))
			gomega.Expect(current.Slot).To(gomega.Equal(occupied.Slot))
			gomega.Expect(current.ResourceAvailability).To(gomega.Equal("available"))
		}
		other, err := client.Connect(ctx, s.root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		t.Cleanup(other.Close)
		clock := time.Now().UTC().Truncate(time.Millisecond)
		s.ledger.SetLockClock(func() (time.Time, string, error) { return clock, "task021-wake", nil })
		gomega.Expect(s.ledger.LockStartup()).To(gomega.Succeed())
		_, err = c.Register(ctx, []byte(`{"schema":"haepari.resource/v1","kind":"lock","id":"beside-job","capacity":1,"liveness_seconds":10,"max_hold_seconds":30}`))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		parent, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "beside-job", RequestKey: "owner", Holder: "parent"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(parent.Current.State).To(gomega.Equal("granted"))
		waiter, _, err := other.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "beside-job", RequestKey: "waiter", Holder: "next"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(waiter.Current.State).To(gomega.Equal("queued"))
		ref := managed.LockGrantReference{InstallationID: c.Hello.InstallationID, LockID: "beside-job", GrantID: parent.Current.GrantID, Generation: parent.Current.Generation}
		raw, err := json.Marshal(ref)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// V59: one holder, and a concurrent borrower plus a waiting caller.
		listing := make(chan error, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			var listErr error
			for i := 0; i < 3 && listErr == nil; i++ {
				page, _, err := other.LocksList(ctx, "beside-job")
				if err != nil {
					listErr = err
					break
				}
				granted := 0
				for _, request := range page.Requests {
					if request.State == "granted" {
						granted++
					}
				}
				if granted != 1 {
					listErr = errors.New("advisory lock had more than one holder")
				}
			}
			listing <- listErr
		}()
		t.Setenv(managed.LockGrantEnvironment, string(raw))
		for _, outcome := range []string{"success", "failure", "cancelled"} {
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
			gomega.Expect(childRequest.GrantID).To(gomega.Equal(parent.Current.GrantID))
			gomega.Expect(childRequest.Generation).To(gomega.Equal(parent.Current.Generation))
			if outcome == "success" {
				gomega.Expect(waitErr).NotTo(gomega.HaveOccurred(), childErrors.String())
			} else {
				gomega.Expect(waitErr).To(gomega.HaveOccurred())
			}
		}
		gomega.Eventually(listing, 20*time.Second).Should(gomega.Receive(gomega.Not(gomega.HaveOccurred())))
		checked, err := c.CheckLockGrant(ctx, ref, "beside-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(checked.GrantID).To(gomega.Equal(parent.Current.GrantID))
		gomega.Expect(checked.LivenessDeadline).To(gomega.Equal(parent.Current.LivenessDeadline))
		held()
		// V60: replay, changed key, ended replay, and wrong installation or lock.
		t.Setenv(managed.LockGrantEnvironment, "")
		replay, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "beside-job", RequestKey: "owner", Holder: "parent"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.Current.RequestID).To(gomega.Equal(parent.Current.RequestID))
		_, _, err = c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "beside-job", RequestKey: "owner", Holder: "changed"})
		expectLockIssue(err, "CONFLICT")
		for _, change := range []func(*managed.LockGrantReference){func(r *managed.LockGrantReference) { r.InstallationID = "wrong" }, func(r *managed.LockGrantReference) { r.LockID = "wrong" }} {
			bad := ref
			change(&bad)
			_, err = c.CheckLockGrant(ctx, bad, "beside-job")
			gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
		}
		var requests int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_lock_requests WHERE lock_id='beside-job'`).Scan(&requests)).To(gomega.Succeed())
		gomega.Expect(requests).To(gomega.Equal(2))
		// V64 wait timeout leaves the waiter queued. The handoff is concurrent.
		_, _, err = other.WaitLock(ctx, waiter.Current.RequestID, 20*time.Millisecond)
		expectLockIssue(err, "WAIT_TIMEOUT")
		queued, _, err := other.LocksList(ctx, "beside-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(queued.Requests[1].State).To(gomega.Equal("queued"))
		handoff := make(chan managed.LockReply, 1)
		handoffErr := make(chan error, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			reply, _, err := other.WaitLock(ctx, waiter.Current.RequestID, 3*time.Second)
			handoff <- reply
			handoffErr <- err
		}()
		t.Setenv(managed.LockGrantEnvironment, "")
		released, _, err := c.ReleaseLock(ctx, parent.Current.GrantID, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var successor managed.LockReply
		gomega.Eventually(handoff, 3*time.Second).Should(gomega.Receive(&successor))
		gomega.Eventually(handoffErr, time.Second).Should(gomega.Receive(gomega.Not(gomega.HaveOccurred())))
		gomega.Expect(successor.Current.State).To(gomega.Equal("granted"))
		gomega.Expect(successor.Current.RequestID).To(gomega.Equal(waiter.Current.RequestID))
		staleRelease, _, err := c.ReleaseLock(ctx, parent.Current.GrantID, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(staleRelease.Receipt).To(gomega.Equal(released.Receipt))
		current, _, err := other.LocksList(ctx, "beside-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.Requests[1].State).To(gomega.Equal("granted"))
		gomega.Expect(current.Requests[1].GrantID).To(gomega.Equal(successor.Current.GrantID))
		_, _, err = c.RenewLock(ctx, parent.Current.GrantID)
		expectLockIssue(err, "LOCK_EXPIRED")
		ended, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "beside-job", RequestKey: "owner", Holder: "parent"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ended.Current.State).To(gomega.Equal("released"))
		gomega.Expect(ended.Receipt).To(gomega.Equal(replay.Receipt))
		held()
		// V61: stopped renewal, unclaimed grant, keepalive until max hold, forced reason.
		_, err = c.Register(ctx, []byte(`{"schema":"haepari.resource/v1","kind":"lock","id":"expiry-beside","capacity":1,"liveness_seconds":10,"max_hold_seconds":30}`))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		holder, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "expiry-beside", RequestKey: "holder", Holder: "holder"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		unclaimed, _, err := other.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "expiry-beside", RequestKey: "unclaimed", Holder: "waiting"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clock = holder.Current.LivenessDeadline.Add(time.Second)
		gomega.Expect(s.ledger.SweepLocks()).To(gomega.Succeed())
		stopped, _, err := c.LocksList(ctx, "expiry-beside")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stopped.Requests[0].State).To(gomega.Equal("hold_expired"))
		gomega.Expect(stopped.Requests[0].RequestID).To(gomega.Equal(holder.Current.RequestID))
		_, _, err = c.RenewLock(ctx, holder.Current.GrantID)
		expectLockIssue(err, "LOCK_EXPIRED")
		claimed, _, err := other.LocksList(ctx, "expiry-beside")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(claimed.Requests[1].State).To(gomega.Equal("granted"))
		gomega.Expect(claimed.Requests[1].RequestID).To(gomega.Equal(unclaimed.Current.RequestID))
		clock = claimed.Requests[1].LivenessDeadline.Add(time.Second)
		gomega.Expect(s.ledger.SweepLocks()).To(gomega.Succeed())
		_, _, err = other.RenewLock(ctx, claimed.Requests[1].GrantID)
		expectLockIssue(err, "LOCK_EXPIRED")
		keeper, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "expiry-beside", RequestKey: "keeper", Holder: "keeper"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(keeper.Current.State).To(gomega.Equal("granted"))
		for step := 0; step < 4; step++ {
			next := clock.Add(9 * time.Second)
			if next.After(*keeper.Current.MaxHoldDeadline) {
				next = keeper.Current.MaxHoldDeadline.Add(-time.Millisecond)
			}
			clock = next
			renewed, _, err := c.RenewLock(ctx, keeper.Current.GrantID)
			if clock.Before(*keeper.Current.MaxHoldDeadline) && step < 3 {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(renewed.Current.State).To(gomega.Equal("granted"))
				continue
			}
			break
		}
		clock = keeper.Current.MaxHoldDeadline.Add(time.Second)
		_, _, err = c.RenewLock(ctx, keeper.Current.GrantID)
		expectLockIssue(err, "LOCK_EXPIRED")
		maxed, _, err := c.LocksList(ctx, "expiry-beside")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var kept managed.LockRequest
		for _, request := range maxed.Requests {
			if request.RequestID == keeper.Current.RequestID {
				kept = request
			}
		}
		gomega.Expect(kept.State).To(gomega.Equal("hold_expired"))
		gomega.Expect(kept.Reason).To(gomega.Equal("max_hold_deadline"))
		forcedTarget, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "expiry-beside", RequestKey: "forced", Holder: "forced"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		forced, _, err := c.CancelLock(ctx, forcedTarget.Current.RequestID, true, "operator holder cleanup")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(forced.Receipt.Force).To(gomega.BeTrue())
		gomega.Expect(forced.Receipt.Reason).To(gomega.Equal("operator holder cleanup"))
		held()
		// V62: locks take no managed capacity. Nested acquisition, pause, recover, and removal.
		t.Setenv(managed.ParentContextEnvironment, "")
		chain, err := s.ledger.RunnerParentChain(c.Hello.InstallationID, occupied.JobID, occupied.LeaseGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		chainJSON, err := json.Marshal(chain)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		t.Setenv(managed.ParentContextEnvironment, string(chainJSON))
		_, _, err = c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "beside-job", RequestKey: "nested", Holder: "nested"})
		expectLockIssue(err, "NESTED_ACQUISITION_REJECTED")
		t.Setenv(managed.ParentContextEnvironment, "")
		lockStatus, _, err := c.InspectResource(ctx, "beside-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(lockStatus.Occupancy).To(gomega.BeZero())
		held()
		_, err = c.Register(ctx, []byte(`{"schema":"haepari.resource/v1","kind":"lock","id":"pause-beside","capacity":1,"liveness_seconds":10,"max_hold_seconds":30}`))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pausedGrant, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "pause-beside", RequestKey: "paused-owner", Holder: "owner"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pause := managed.AvailabilityRequest{ResourceID: "pause-beside", MutationKey: "pause-beside", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "hold grants"}
		_, _, err = c.SetAvailability(ctx, pause, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pausedWaiter, _, err := other.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "pause-beside", RequestKey: "paused-waiter", Holder: "waiter"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(pausedWaiter.Current.State).To(gomega.Equal("queued"))
		_, _, err = c.ReleaseLock(ctx, pausedGrant.Current.GrantID, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		stillQueued, _, err := other.LocksList(ctx, "pause-beside")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stillQueued.Requests[1].State).To(gomega.Equal("queued"))
		_, err = c.LockResourceAction(ctx, "recover", "pause-beside", nil, 0, true)
		expectLockIssue(err, "UNSUPPORTED")
		_, err = c.LockResourceAction(ctx, "remove", "pause-beside", nil, 1, false)
		expectLockIssue(err, "RESOURCE_BUSY")
		_, _, err = other.CancelLock(ctx, pausedWaiter.Current.RequestID, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = c.LockResourceAction(ctx, "remove", "pause-beside", nil, 1, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		held()
		// V63: restart and injected wake refresh liveness without passing max hold.
		_, err = c.Register(ctx, []byte(`{"schema":"haepari.resource/v1","kind":"lock","id":"wake-beside","capacity":1,"liveness_seconds":10,"max_hold_seconds":40}`))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		live, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "wake-beside", RequestKey: "live", Holder: "live"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		waiting, _, err := other.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "wake-beside", RequestKey: "waiting", Holder: "waiting"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		beforeWake, _, err := c.LocksList(ctx, "wake-beside")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clock = live.Current.LivenessDeadline.Add(5 * time.Second)
		s.ledger.SetLockClock(func() (time.Time, string, error) { return clock, "task021-wake-restart", nil })
		gomega.Expect(s.ledger.LockStartup()).To(gomega.Succeed())
		restarted, _, err := c.LocksList(ctx, "wake-beside")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(restarted.Requests[0].State).To(gomega.Equal("granted"))
		gomega.Expect(restarted.Requests[0].GrantID).To(gomega.Equal(live.Current.GrantID))
		gomega.Expect(restarted.Requests[1].State).To(gomega.Equal("queued"))
		gomega.Expect(restarted.Requests[1].RequestID).To(gomega.Equal(waiting.Current.RequestID))
		gomega.Expect(restarted.Requests[0].QueueDeadline).To(gomega.Equal(beforeWake.Requests[0].QueueDeadline))
		gomega.Expect(*restarted.Requests[0].LivenessDeadline).To(gomega.Equal(clock.Add(10 * time.Second)))
		gomega.Expect(restarted.Requests[0].LivenessDeadline.After(*live.Current.MaxHoldDeadline)).To(gomega.BeFalse())
		clock = live.Current.MaxHoldDeadline.Add(time.Second)
		s.ledger.SetLockClock(func() (time.Time, string, error) { return clock, "task021-wake-cap", nil })
		gomega.Expect(s.ledger.LockStartup()).To(gomega.Succeed())
		capped, _, err := c.LocksList(ctx, "wake-beside")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(capped.Requests[0].State).To(gomega.Equal("hold_expired"))
		gomega.Expect(capped.Requests[1].State).To(gomega.Equal("granted"))
		held()
		// V64 continued: stale inherited grant, and queue expiry leaves no successor grant.
		t.Setenv(managed.LockGrantEnvironment, string(raw))
		_, err = c.CheckLockGrant(ctx, ref, "beside-job")
		gomega.Expect(errors.Is(err, managed.ErrLockExpired)).To(gomega.BeTrue())
		_, _, err = c.RenewLock(ctx, ref.GrantID)
		expectLockIssue(err, "LOCK_EXPIRED")
		beforeBorrow, err := s.ledger.LocksList("beside-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, _, err = c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "beside-job", RequestKey: "child", Holder: "borrower"})
		expectLockIssue(err, "LOCK_EXPIRED")
		afterBorrow, err := s.ledger.LocksList("beside-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(afterBorrow.Requests).To(gomega.Equal(beforeBorrow.Requests))
		t.Setenv(managed.LockGrantEnvironment, "")
		seconds := int64(10)
		expiring, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "wake-beside", RequestKey: "expiring", Holder: "expiring", QueueSeconds: &seconds})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(expiring.Current.State).To(gomega.Equal("queued"))
		clock = expiring.Current.QueueDeadline.Add(time.Second)
		gomega.Expect(s.ledger.SweepLocks()).To(gomega.Succeed())
		expiredQueue, _, err := c.LocksList(ctx, "wake-beside")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(expiredQueue.Requests[2].State).To(gomega.Equal("queue_expired"))
		gomega.Expect(expiredQueue.Requests[2].RequestID).To(gomega.Equal(expiring.Current.RequestID))
		held()
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
