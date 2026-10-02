//go:build integration

package managed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type lockFixture struct {
	db      *sql.DB
	path    string
	ledger  *Ledger
	now     time.Time
	wake    string
	failure error
}

func newLockFixture() *lockFixture {
	t := ginkgo.GinkgoT()
	path := filepath.Join(t.TempDir(), "locks.sqlite")
	db, l := openLedger(t, path, 1)
	f := &lockFixture{db: db, path: path, ledger: l, now: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	t.Cleanup(func() { f.db.Close() })
	l.SetLockClock(func() (time.Time, string, error) { return f.now, f.wake, f.failure })
	_, err := l.BindInstallation("installation-lock-test")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(l.RegisterOnlyLocal([]byte(`{"schema":"haepari.resource/v1","id":"review","kind":"lock"}`))).To(gomega.Succeed())
	return f
}
func (f *lockFixture) acquire(key string) LockReply {
	r, err := f.ledger.AcquireLock(LockAcquireRequest{LockID: "review", RequestKey: key, Holder: "holder-" + key}, "installation-lock-test", nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}
func (f *lockFixture) list() LockList {
	r, err := f.ledger.LocksList("review")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}
func (f *lockFixture) update(capacity int, live, max int64) {
	status, err := f.ledger.ResourceStatus("review")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	raw := fmt.Sprintf(`{"schema":"haepari.resource/v1","id":"review","kind":"lock","capacity":%d,"revision":%d,"liveness_seconds":%d,"max_hold_seconds":%d}`, capacity, status.ConfigRevision+1, live, max)
	gomega.Expect(f.ledger.UpdateResource([]byte(raw), status.ConfigRevision, status.AvailabilityRevision)).To(gomega.Succeed())
}

var _ = ginkgo.Describe("AdvisoryLocks", ginkgo.Label("integration"), func() {
	ginkgo.It("validates the closed resource union and bounded deadlines", func() {
		for _, field := range []string{`"execution_mode":""`, `"adapter":{}`, `"cleanup":null`, `"vm":null`, `"guest":{}`, `"capacity":0`, `"capacity":null`, `"revision":0`, `"liveness_seconds":0`, `"max_hold_seconds":null`, `"liveness_seconds":9223372036854775807`, `"Capacity":1`, `"capacity":1,"capacity":2`} {
			_, err := DecodeResource([]byte(`{"schema":"haepari.resource/v1","id":"review","kind":"lock",` + field + `}`))
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		spec, err := DecodeResource([]byte(`{"schema":"haepari.resource/v1","id":"review","kind":"lock","liveness_seconds":100,"max_hold_seconds":1}`))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		raw, err := json.Marshal(spec)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("adapter"))
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("execution_mode"))
		for _, field := range []string{"liveness_seconds", "max_hold_seconds"} {
			r := validResource()
			raw, _ := json.Marshal(r)
			var fields map[string]any
			gomega.Expect(json.Unmarshal(raw, &fields)).To(gomega.Succeed())
			fields[field] = nil
			raw, _ = json.Marshal(fields)
			_, err := DecodeResource(raw)
			gomega.Expect(err).To(gomega.MatchError(ErrInvalid))
		}
		f := newLockFixture()
		f.now = time.Unix(0, math.MaxInt64-1).UTC()
		a := f.acquire("near-clock-bound")
		gomega.Expect(*a.Current.MaxHoldDeadline).To(gomega.Equal(f.now.Add(21600 * time.Second)))
		seconds := int64(math.MaxInt64 / int64(time.Second))
		queued, err := f.ledger.AcquireLock(LockAcquireRequest{LockID: "review", RequestKey: "long-queue", Holder: "holder", QueueSeconds: &seconds}, "installation-lock-test", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(*queued.Current.QueueDeadline).To(gomega.Equal(f.now.Add(time.Duration(seconds) * time.Second)))
		before := f.list()
		f.now = time.Unix(0, math.MaxInt64).UTC().Add(time.Nanosecond)
		err = f.ledger.SweepLocks()
		gomega.Expect(err).To(gomega.MatchError(ErrInvalid))
		f.now = before.Now
		gomega.Expect(f.list()).To(gomega.Equal(before))
	})
	ginkgo.DescribeTable("persists long registered policy deadlines as JSON timestamps", func(field string) {
		f := newLockFixture()
		before := f.list()
		changes := f.ledger.Changes()
		// Raw duration overflow is still rejected without changing the clock or resources.
		f.now = time.Unix(0, 1).UTC()
		raw := fmt.Sprintf(`{"schema":"haepari.resource/v1","id":"other","kind":"lock","%s":9223372037}`, field)
		_, err := DecodeResource([]byte(raw))
		gomega.Expect(err).To(gomega.MatchError(ErrInvalid))
		gomega.Expect(f.ledger.RegisterResource([]byte(raw))).To(gomega.MatchError(ErrInvalid))
		_, err = f.ledger.ResourceStatus("other")
		gomega.Expect(err).To(gomega.MatchError(ErrNotFound))
		gomega.Expect(f.list()).To(gomega.Equal(before))
		select {
		case <-changes:
			ginkgo.Fail("rejected registration broadcast")
		default:
		}
		raw = fmt.Sprintf(`{"schema":"haepari.resource/v1","id":"other","kind":"lock","%s":9223372036}`, field)
		gomega.Expect(f.ledger.RegisterResource([]byte(raw))).To(gomega.Succeed())
		grant, err := f.ledger.AcquireLock(LockAcquireRequest{LockID: "other", RequestKey: "owner", Holder: "owner"}, "installation-lock-test", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(*grant.Current.MaxHoldDeadline).To(gomega.Equal(before.Now.Add(time.Duration(grant.Current.MaxHoldSeconds) * time.Second)))
		live := before.Now.Add(time.Duration(grant.Current.LivenessSeconds) * time.Second)
		if live.After(*grant.Current.MaxHoldDeadline) {
			live = *grant.Current.MaxHoldDeadline
		}
		gomega.Expect(*grant.Current.LivenessDeadline).To(gomega.Equal(live))
		_, err = f.ledger.ReleaseLock(grant.Current.GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	},
		ginkgo.Entry("liveness", "liveness_seconds"),
		ginkgo.Entry("maximum hold", "max_hold_seconds"),
	)
	ginkgo.DescribeTable("keeps an accepted policy usable after UTC advances past signed nanosecond deadline slack", func(field string) {
		f := newLockFixture()
		f.update(1, 3, 3)
		a := f.acquire("a")
		b := f.acquire("b")
		c := f.acquire("c")
		before := f.list()
		status, err := f.ledger.ResourceStatus("review")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		changes := f.ledger.Changes()
		f.now = time.Unix(0, 1).UTC()
		raw := fmt.Sprintf(`{"schema":"haepari.resource/v1","id":"review","kind":"lock","revision":3,"%s":9223372037}`, field)
		gomega.Expect(f.ledger.UpdateResource([]byte(raw), status.ConfigRevision, status.AvailabilityRevision)).To(gomega.MatchError(ErrInvalid))
		after, err := f.ledger.ResourceStatus("review")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(status))
		gomega.Expect(f.list()).To(gomega.Equal(before))
		select {
		case <-changes:
			ginkgo.Fail("rejected update broadcast")
		default:
		}
		f.now = before.Now
		boundary := (int64(math.MaxInt64) - f.now.UnixNano()) / int64(time.Second)
		live, maxHold := int64(3), boundary
		if field == "liveness_seconds" {
			live = boundary
		}
		f.update(1, live, maxHold)
		// An unrelated mutation must commit the original holder's due FIFO handoff.
		f.now = before.Now.Add(4 * time.Second)
		gomega.Expect(f.ledger.RegisterResource([]byte(`{"schema":"haepari.resource/v1","id":"other","kind":"lock"}`))).To(gomega.Succeed())
		requests := f.list().Requests
		gomega.Expect(requests[0].State).To(gomega.Equal("hold_expired"))
		gomega.Expect(requests[0].Reason).To(gomega.Equal("max_hold_deadline"))
		gomega.Expect(requests[1].RequestID).To(gomega.Equal(b.Current.RequestID))
		gomega.Expect(requests[1].State).To(gomega.Equal("granted"))
		gomega.Expect(requests[1].Generation).To(gomega.Equal(a.Current.Generation + 1))
		gomega.Expect(requests[1].LivenessSeconds).To(gomega.Equal(live))
		gomega.Expect(requests[1].MaxHoldSeconds).To(gomega.Equal(maxHold))
		gomega.Expect(*requests[1].LivenessDeadline).To(gomega.Equal(f.now.Add(time.Duration(live) * time.Second)))
		gomega.Expect(*requests[1].MaxHoldDeadline).To(gomega.Equal(f.now.Add(time.Duration(maxHold) * time.Second)))
		gomega.Expect(requests[2]).To(gomega.Equal(c.Current))
		maximum := *requests[1].MaxHoldDeadline
		expectedLiveness := func() time.Time {
			deadline := f.now.Add(time.Duration(live) * time.Second)
			if deadline.After(maximum) {
				return maximum
			}
			return deadline
		}
		// Renewal, observed wake and an actual reopen/startup use the same long-deadline helper.
		f.now = f.now.Add(time.Second)
		renewed, err := f.ledger.RenewLock(requests[1].GrantID, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(*renewed.Current.LivenessDeadline).To(gomega.Equal(expectedLiveness()))
		f.now = f.now.Add(time.Second)
		f.wake = "policy-boundary-wake"
		gomega.Expect(f.ledger.SweepLocks()).To(gomega.Succeed())
		gomega.Expect(*f.list().Requests[1].LivenessDeadline).To(gomega.Equal(expectedLiveness()))
		beforeRestart := f.list()
		gomega.Expect(f.db.Close()).To(gomega.Succeed())
		db, err := sqlite.Open(f.path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.db = db
		f.ledger, err = NewLedger(db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.ledger.SetLockClock(func() (time.Time, string, error) { return f.now, f.wake, f.failure })
		gomega.Expect(f.list()).To(gomega.Equal(beforeRestart))
		f.now = f.now.Add(time.Second)
		gomega.Expect(f.ledger.LockStartup()).To(gomega.Succeed())
		requests = f.list().Requests
		// The original queued receipt has no grant; the durable successor identity stays fixed.
		gomega.Expect(requests[1].GrantID).To(gomega.Equal(renewed.Current.GrantID))
		gomega.Expect(*requests[1].LivenessDeadline).To(gomega.Equal(expectedLiveness()))
		_, err = f.ledger.ReleaseLock(requests[1].GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		requests = f.list().Requests
		gomega.Expect(requests[1].State).To(gomega.Equal("released"))
		gomega.Expect(requests[2].RequestID).To(gomega.Equal(c.Current.RequestID))
		gomega.Expect(requests[2].State).To(gomega.Equal("granted"))
		other, err := f.ledger.AcquireLock(LockAcquireRequest{LockID: "other", RequestKey: "owner", Holder: "owner"}, "installation-lock-test", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(other.Current.State).To(gomega.Equal("granted"))
	},
		ginkgo.Entry("liveness", "liveness_seconds"),
		ginkgo.Entry("maximum hold", "max_hold_seconds"),
	)
	ginkgo.It("serializes concurrent requests and preserves original key receipts with current state", func() {
		f := newLockFixture()
		var wg sync.WaitGroup
		replies := make([]LockReply, 16)
		errs := make([]error, 16)
		for i := range replies {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				replies[i], errs[i] = f.ledger.AcquireLock(LockAcquireRequest{LockID: "review", RequestKey: "same", Holder: "owner"}, "installation-lock-test", nil)
			}(i)
		}
		wg.Wait()
		for i := range replies {
			gomega.Expect(errs[i]).NotTo(gomega.HaveOccurred())
			gomega.Expect(replies[i].Receipt).To(gomega.Equal(replies[0].Receipt))
			gomega.Expect(replies[i].Current.RequestID).To(gomega.Equal(replies[0].Current.RequestID))
		}
		gomega.Expect(f.list().Requests).To(gomega.HaveLen(1))
		for i := range replies {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				replies[i], errs[i] = f.ledger.AcquireLock(LockAcquireRequest{LockID: "review", RequestKey: fmt.Sprintf("distinct-%d", i), Holder: "owner"}, "installation-lock-test", nil)
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		list := f.list()
		gomega.Expect(list.Requests).To(gomega.HaveLen(17))
		for i, r := range list.Requests {
			if i == 0 {
				gomega.Expect(r.State).To(gomega.Equal("granted"))
			} else {
				gomega.Expect(r.State).To(gomega.Equal("queued"))
				gomega.Expect(r.Sequence).To(gomega.BeNumerically(">", list.Requests[i-1].Sequence))
			}
		}
		first := list.Requests[0]
		released, err := f.ledger.ReleaseLock(first.GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(released.Receipt.Operation).To(gomega.Equal("release"))
		list = f.list()
		gomega.Expect(list.Requests[1].State).To(gomega.Equal("granted"))
		gomega.Expect(list.Requests[1].Generation).To(gomega.BeNumerically(">", first.Generation))
		replay, err := f.ledger.AcquireLock(LockAcquireRequest{LockID: "review", RequestKey: "same", Holder: "owner"}, "installation-lock-test", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.Receipt.State).To(gomega.Equal("granted"))
		gomega.Expect(replay.Current.State).To(gomega.Equal("released"))
		_, err = f.ledger.AcquireLock(LockAcquireRequest{LockID: "review", RequestKey: "same", Holder: "changed"}, "installation-lock-test", nil)
		gomega.Expect(err).To(gomega.MatchError(ErrConflict))
		again, err := f.ledger.ReleaseLock(first.GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again).To(gomega.Equal(released))
		gomega.Expect(f.list()).To(gomega.Equal(list))
	})
	ginkgo.It("expires renewals atomically and advances unclaimed grants in FIFO order", func() {
		f := newLockFixture()
		a := f.acquire("a")
		b := f.acquire("b")
		c := f.acquire("c")
		f.now = f.now.Add(121 * time.Second)
		reply, err := f.ledger.RenewLock(a.Current.GrantID, nil)
		gomega.Expect(err).To(gomega.MatchError(ErrLockExpired))
		gomega.Expect(reply.Current.State).To(gomega.Equal("hold_expired"))
		list := f.list()
		gomega.Expect(list.Requests[1].RequestID).To(gomega.Equal(b.Current.RequestID))
		gomega.Expect(list.Requests[1].State).To(gomega.Equal("granted"))
		gomega.Expect(list.Requests[2].State).To(gomega.Equal("queued"))
		f.now = f.now.Add(121 * time.Second)
		gomega.Expect(f.ledger.SweepLocks()).To(gomega.Succeed())
		list = f.list()
		gomega.Expect(list.Requests[1].State).To(gomega.Equal("hold_expired"))
		gomega.Expect(list.Requests[2].State).To(gomega.Equal("granted"))
		gomega.Expect(list.Requests[2].RequestID).To(gomega.Equal(c.Current.RequestID))
		released, err := f.ledger.ReleaseLock(a.Current.GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(released.Receipt.Operation).To(gomega.Equal("expire"))
		gomega.Expect(released.Receipt).To(gomega.Equal(reply.Receipt))
		f.now = f.now.Add(30 * time.Second)
		claimed, err := f.ledger.ClaimLock(c.Current.RequestID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(*claimed.Current.LivenessDeadline).To(gomega.Equal(f.now.Add(120 * time.Second)))
	})
	ginkgo.It("cancels queued and granted requests, records forces and keeps terminal receipts immutable", func() {
		f := newLockFixture()
		a := f.acquire("a")
		b := f.acquire("b")
		c := f.acquire("c")
		_, err := f.ledger.CancelLock(b.Current.RequestID, true, "", nil)
		gomega.Expect(err).To(gomega.MatchError(ErrInvalid))
		cancelled, err := f.ledger.CancelLock(b.Current.RequestID, true, "operator queue cleanup", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cancelled.Current.State).To(gomega.Equal("cancelled"))
		gomega.Expect(cancelled.Receipt.Force).To(gomega.BeTrue())
		ended, err := f.ledger.CancelLock(a.Current.RequestID, true, "operator holder cleanup", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ended.Current.State).To(gomega.Equal("released"))
		gomega.Expect(ended.Receipt.Operation).To(gomega.Equal("cancel"))
		gomega.Expect(f.list().Requests[2].State).To(gomega.Equal("granted"))
		stale, err := f.ledger.ReleaseLock(a.Current.GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stale.Receipt).To(gomega.Equal(ended.Receipt))
		gomega.Expect(stale.Receipt.Operation).To(gomega.Equal("cancel"))
		forced, err := f.ledger.ReleaseLock(f.list().Requests[2].GrantID, true, "operator release", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(forced.Receipt.Operation).To(gomega.Equal("release"))
		gomega.Expect(forced.Current.RequestID).To(gomega.Equal(c.Current.RequestID))
		wrong, err := f.ledger.ReleaseLock(forced.Current.GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(wrong.Receipt.Force).To(gomega.BeTrue())
		gomega.Expect(wrong.Receipt.Reason).To(gomega.Equal("operator release"))
		same, err := f.ledger.ReleaseLock(forced.Current.GrantID, true, "operator release", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(same).To(gomega.Equal(forced))
	})
	ginkgo.It("honors pause/resume and queue expiry without broadcasting clock-only progress", func() {
		f := newLockFixture()
		pause := AvailabilityRequest{ResourceID: "review", MutationKey: "pause", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "operator"}
		_, err := f.ledger.SetAvailability(pause, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		a := f.acquire("a")
		seconds := int64(10)
		b, err := f.ledger.AcquireLock(LockAcquireRequest{LockID: "review", RequestKey: "b", Holder: "waiter", QueueSeconds: &seconds}, "installation-lock-test", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(b.Current.State).To(gomega.Equal("queued"))
		changes := f.ledger.Changes()
		f.now = f.now.Add(time.Second)
		gomega.Expect(f.ledger.SweepLocks()).To(gomega.Succeed())
		claimed, err := f.ledger.ClaimLock(a.Current.RequestID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(claimed.Current.State).To(gomega.Equal("queued"))
		select {
		case <-changes:
			ginkgo.Fail("clock-only sweep/queued claim broadcast")
		default:
		}
		f.now = f.now.Add(10 * time.Second)
		_, err = f.ledger.SetAvailability(pause, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.list().Requests[1].State).To(gomega.Equal("queue_expired"))
		status, err := f.ledger.SetAvailability(AvailabilityRequest{ResourceID: "review", MutationKey: "resume", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 2}, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.AdvisoryGrants).To(gomega.Equal(1))
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
		gomega.Expect(f.list().Requests[0].State).To(gomega.Equal("granted"))
	})
	ginkgo.It("freezes grant policies and preserves keys, sequence and generations through shrink and removal", func() {
		f := newLockFixture()
		f.update(2, 10, 30)
		a := f.acquire("a")
		b := f.acquire("b")
		f.update(1, 50, 100)
		c := f.acquire("c")
		gomega.Expect(c.Current.State).To(gomega.Equal("queued"))
		_, err := f.ledger.ReleaseLock(a.Current.GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.list().Requests[2].State).To(gomega.Equal("queued"))
		f.now = f.now.Add(9 * time.Second)
		renewed, err := f.ledger.RenewLock(b.Current.GrantID, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(renewed.Current.LivenessSeconds).To(gomega.Equal(int64(10)))
		gomega.Expect(*renewed.Current.LivenessDeadline).To(gomega.Equal(f.now.Add(10 * time.Second)))
		gomega.Expect(f.ledger.RemoveLockResource("review", 3)).To(gomega.MatchError(ErrBusy))
		_, err = f.ledger.CancelLock(c.Current.RequestID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.ledger.ReleaseLock(b.Current.GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.ledger.RemoveLockResource("review", 3)).To(gomega.Succeed())
		gomega.Expect(f.ledger.RegisterOnlyLocal([]byte(`{"schema":"haepari.resource/v1","id":"review","kind":"lock","capacity":2}`))).To(gomega.Succeed())
		replay := f.acquire("a")
		gomega.Expect(replay.Receipt).To(gomega.Equal(a.Receipt))
		gomega.Expect(replay.Current.State).To(gomega.Equal("released"))
		d := f.acquire("d")
		e := f.acquire("e")
		gomega.Expect(d.Current.Sequence).To(gomega.BeNumerically(">", c.Current.Sequence))
		gomega.Expect(d.Current.Generation).To(gomega.BeNumerically(">", a.Current.Generation))
		gomega.Expect(e.Current.Generation).To(gomega.BeNumerically(">", b.Current.Generation))
		page, err := f.ledger.LocksPage("review", "", 2)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(page.More).To(gomega.BeTrue())
		all := page.Requests
		for page.More {
			page, err = f.ledger.LocksPage("review", page.NextCursor, 2)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			all = append(all, page.Requests...)
		}
		gomega.Expect(all).To(gomega.Equal(f.list().Requests))
	})
	ginkgo.It("validates inherited grants read-only and refuses every parent mutation before effects", func() {
		f := newLockFixture()
		a := f.acquire("a")
		ref := LockGrantReference{a.Current.InstallationID, a.Current.LockID, a.Current.GrantID, a.Current.Generation}
		raw, _ := json.Marshal(ref)
		decoded, err := DecodeLockGrant(string(raw))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(decoded).To(gomega.Equal(ref))
		for _, raw := range []string{"", `null`, `{"installation_id":"x","lock_id":"review","grant_id":"y","generation":1,"Generation":1}`, `{"installation_id":"x","lock_id":"review","grant_id":"y","generation":1,"generation":1}`} {
			_, err := DecodeLockGrant(raw)
			gomega.Expect(err).To(gomega.MatchError(ErrInvalid))
		}
		before := f.list()
		observed, err := f.ledger.CheckLockGrant(ref, "review")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(observed).To(gomega.Equal(a.Current))
		changes := f.ledger.Changes()
		_, err = f.ledger.RenewLock(ref.GrantID, &ref)
		gomega.Expect(err).To(gomega.MatchError(ErrConflict))
		_, err = f.ledger.ReleaseLock(ref.GrantID, true, "child cancel", &ref)
		gomega.Expect(err).To(gomega.MatchError(ErrConflict))
		_, err = f.ledger.CancelLock(a.Current.RequestID, true, "child fail", &ref)
		gomega.Expect(err).To(gomega.MatchError(ErrConflict))
		gomega.Expect(f.list()).To(gomega.Equal(before))
		select {
		case <-changes:
			ginkgo.Fail("borrower broadcast mutation")
		default:
		}
		for _, tc := range []struct {
			ref      LockGrantReference
			intended string
			err      error
		}{{ref, "other", ErrConflict}, {LockGrantReference{"wrong", ref.LockID, ref.GrantID, ref.Generation}, "review", ErrConflict}, {LockGrantReference{ref.InstallationID, "wrong", ref.GrantID, ref.Generation}, "wrong", ErrConflict}, {LockGrantReference{ref.InstallationID, ref.LockID, "unknown", 1}, "review", ErrNotFound}, {LockGrantReference{ref.InstallationID, ref.LockID, ref.GrantID, 2}, "review", ErrLockExpired}} {
			_, err := f.ledger.CheckLockGrant(tc.ref, tc.intended)
			gomega.Expect(err).To(gomega.MatchError(tc.err))
		}
		f.now = f.now.Add(121 * time.Second)
		_, err = f.ledger.CheckLockGrant(ref, "review")
		gomega.Expect(err).To(gomega.MatchError(ErrLockExpired))
		inspect, err := f.ledger.InspectLockRequest(a.Current.RequestID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(inspect.Current.State).To(gomega.Equal("granted"))
		_, err = f.ledger.ReleaseLock(ref.GrantID, true, "child expired", &ref)
		gomega.Expect(err).To(gomega.MatchError(ErrLockExpired))
		gomega.Expect(f.list().Requests[0].State).To(gomega.Equal("granted"))
	})
	ginkgo.It("keeps managed local and VM guards separate and rejects nested replay or spare capacity", func() {
		f := newLockFixture()
		gomega.Expect(f.ledger.RegisterResource(encode(ginkgo.GinkgoT(), validResource()))).To(gomega.Succeed())
		a := f.acquire("a")
		job := validJob()
		admitted, err := f.ledger.Admit(encode(ginkgo.GinkgoT(), job), digest, "managed", "managed-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reserved, err := f.ledger.Reserve(ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-managed"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(reserved.GlobalOccupancy).To(gomega.Equal(1))
		chain, err := f.ledger.RunnerParentChain("installation-lock-test", reserved.JobID, reserved.LeaseGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.update(2, 120, 21600)
		for _, key := range []string{"a", "nested-new"} {
			_, err := f.ledger.AcquireLock(LockAcquireRequest{LockID: "review", RequestKey: key, Holder: "holder-" + key}, "installation-lock-test", chain)
			gomega.Expect(err).To(gomega.MatchError(ErrNestedAcquisition))
		}
		gomega.Expect(f.list().Requests).To(gomega.HaveLen(1))
		gomega.Expect(f.acquire("spare").Current.State).To(gomega.Equal("granted"))
		job.ResourceID = "review"
		_, err = f.ledger.Admit(encode(ginkgo.GinkgoT(), job), digest, "lock-job", "bad-job")
		gomega.Expect(err).To(gomega.MatchError(ErrUnsupported))
		var count int
		gomega.Expect(f.db.QueryRow(`SELECT COUNT(*) FROM managed_admissions WHERE request_key='lock-job'`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
		status, err := f.ledger.ResourceStatus("review")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
		gomega.Expect(status.LeaseGeneration).To(gomega.BeZero())
		gomega.Expect(status.AdvisoryGrants).To(gomega.Equal(2))
		_, err = f.ledger.ReleaseLock(a.Current.GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current, err := f.ledger.Snapshot(reserved.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
		// Only ledger observations are injected; no VM or native adapter is run.
		f.ledger.globalLimit = 2
		vmScheduling(ginkgo.GinkgoT(), f.ledger, "fixture-vm", 1, "stopped", false)
		vm := admitScheduling(ginkgo.GinkgoT(), f.ledger, "fixture-vm", "fixture-vm-job")
		vmReserved, err := reserveScheduling(f.ledger, vm, vmGuard(ginkgo.GinkgoT(), f.db, 2))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(vmReserved.GlobalOccupancy).To(gomega.Equal(2))
		f.acquire("with-vm")
		var starts int
		gomega.Expect(f.db.QueryRow(`SELECT COUNT(*) FROM managed_power_transitions WHERE state='pending'`).Scan(&starts)).To(gomega.Succeed())
		gomega.Expect(starts).To(gomega.Equal(1))
	})
	ginkgo.It("extends liveness at restart and injected wake before expiry, caps maximum hold and uses backward highwater", func() {
		f := newLockFixture()
		f.update(1, 10, 100)
		a := f.acquire("a")
		f.acquire("b")
		f.now = f.now.Add(20 * time.Second)
		beforeRestart := f.list()
		gomega.Expect(f.db.Close()).To(gomega.Succeed())
		db, err := sqlite.Open(f.path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.db = db
		reopened, err := NewLedger(f.db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reopened.SetLockClock(func() (time.Time, string, error) { return f.now, f.wake, f.failure })
		f.ledger = reopened
		gomega.Expect(f.list()).To(gomega.Equal(beforeRestart))
		gomega.Expect(reopened.LockStartup()).To(gomega.Succeed())
		r := f.list().Requests[0]
		gomega.Expect(r.GrantID).To(gomega.Equal(a.Current.GrantID))
		gomega.Expect(*r.LivenessDeadline).To(gomega.Equal(f.now.Add(10 * time.Second)))
		f.now = f.now.Add(20 * time.Second)
		f.wake = "aabbcc"
		gomega.Expect(f.ledger.SweepLocks()).To(gomega.Succeed())
		gomega.Expect(f.list().Requests[0].State).To(gomega.Equal("granted"))
		high := f.now
		f.now = f.now.Add(-30 * time.Second)
		list := f.list()
		gomega.Expect(list.Now).To(gomega.Equal(high))
		renewed, err := f.ledger.RenewLock(a.Current.GrantID, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(*renewed.Current.LivenessDeadline).To(gomega.Equal(high.Add(10 * time.Second)))
		f.now = a.Current.QueuedAt.Add(95 * time.Second)
		f.wake = "ddeeff"
		gomega.Expect(f.ledger.SweepLocks()).To(gomega.Succeed())
		gomega.Expect(*f.list().Requests[0].LivenessDeadline).To(gomega.Equal(a.Current.QueuedAt.Add(100 * time.Second)))
		f.now = a.Current.QueuedAt.Add(101 * time.Second)
		f.wake = "001122"
		gomega.Expect(f.ledger.LockStartup()).To(gomega.Succeed())
		list = f.list()
		gomega.Expect(list.Requests[0].State).To(gomega.Equal("hold_expired"))
		gomega.Expect(list.Requests[0].Reason).To(gomega.Equal("max_hold_deadline"))
		gomega.Expect(list.Requests[1].State).To(gomega.Equal("granted"))
		var n int
		gomega.Expect(f.db.QueryRow(`SELECT COUNT(*) FROM managed_lock_discontinuities`).Scan(&n)).To(gomega.Succeed())
		gomega.Expect(n).To(gomega.Equal(4))
	})
	ginkgo.It("fails closed on a clock error or missing schema7 tables and clock row", func() {
		f := newLockFixture()
		a := f.acquire("a")
		f.acquire("b")
		before := f.list()
		changes := f.ledger.Changes()
		f.failure = errors.New("clock unavailable")
		f.now = f.now.Add(time.Hour)
		gomega.Expect(f.ledger.SweepLocks()).To(gomega.MatchError(f.failure))
		gomega.Expect(f.ledger.LockStartup()).To(gomega.MatchError(f.failure))
		_, err := f.ledger.RenewLock(a.Current.GrantID, nil)
		gomega.Expect(err).To(gomega.MatchError(f.failure))
		_, err = f.ledger.LocksList("review")
		gomega.Expect(err).To(gomega.MatchError(f.failure))
		f.failure = nil
		f.now = before.Now
		gomega.Expect(f.list()).To(gomega.Equal(before))
		select {
		case <-changes:
			ginkgo.Fail("failed clock broadcast")
		default:
		}
		for _, statement := range []string{`DROP TABLE managed_lock_requests`, `DROP TABLE managed_lock_slots`, `DROP TABLE managed_lock_clock`, `DROP TABLE managed_lock_discontinuities`, `DELETE FROM managed_lock_clock`} {
			other := newLockFixture()
			gomega.Expect(other.db.Exec(statement)).Error().NotTo(gomega.HaveOccurred())
			_, err := NewLedger(other.db, 1)
			gomega.Expect(err).To(gomega.HaveOccurred())
			var version int
			gomega.Expect(other.db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
			gomega.Expect(version).To(gomega.Equal(9))
		}
	})
	ginkgo.It("replays a newly granted waiter with its queued receipt and original policy, then expires at maximum hold", func() {
		f := newLockFixture()
		f.update(1, 10, 25)
		a := f.acquire("a")
		b := f.acquire("b")
		gomega.Expect(b.Receipt.State).To(gomega.Equal("queued"))
		_, err := f.ledger.ReleaseLock(a.Current.GrantID, false, "", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.update(1, 120, 1000)
		f.now = f.now.Add(8 * time.Second)
		replayed := f.acquire("b")
		gomega.Expect(replayed.Receipt).To(gomega.Equal(b.Receipt))
		gomega.Expect(replayed.Current.State).To(gomega.Equal("granted"))
		gomega.Expect(replayed.Current.LivenessSeconds).To(gomega.Equal(int64(10)))
		gomega.Expect(*replayed.Current.LivenessDeadline).To(gomega.Equal(f.now.Add(10 * time.Second)))
		for _, seconds := range []int{16, 24} {
			f.now = b.Current.QueuedAt.Add(time.Duration(seconds) * time.Second)
			renewed, err := f.ledger.RenewLock(replayed.Current.GrantID, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(*renewed.Current.LivenessDeadline).To(gomega.Equal(b.Current.QueuedAt.Add(25 * time.Second)))
		}
		f.now = b.Current.QueuedAt.Add(25 * time.Second)
		_, err = f.ledger.RenewLock(replayed.Current.GrantID, nil)
		gomega.Expect(err).To(gomega.MatchError(ErrLockExpired))
		ended := f.acquire("b")
		gomega.Expect(ended.Current.State).To(gomega.Equal("hold_expired"))
		gomega.Expect(ended.Receipt).To(gomega.Equal(b.Receipt))
	})
	ginkgo.It("rolls back expiry, handoff and generation together on storage failure and permits exact key recovery", func() {
		f := newLockFixture()
		a := f.acquire("a")
		f.acquire("b")
		before := f.list()
		changes := f.ledger.Changes()
		f.now = f.now.Add(121 * time.Second)
		_, err := f.db.Exec(`CREATE TRIGGER deny_lock_handoff BEFORE UPDATE ON managed_lock_requests WHEN NEW.state='granted' AND OLD.state='queued' BEGIN SELECT RAISE(ABORT,'handoff denied'); END`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.ledger.RenewLock(a.Current.GrantID, nil)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, ErrLockExpired)).To(gomega.BeFalse())
		after := f.list()
		gomega.Expect(after.Requests).To(gomega.Equal(before.Requests))
		select {
		case <-changes:
			ginkgo.Fail("rolled-back handoff broadcast")
		default:
		}
		var high int64
		gomega.Expect(f.db.QueryRow(`SELECT highwater_ns FROM managed_lock_clock`).Scan(&high)).To(gomega.Succeed())
		gomega.Expect(high).To(gomega.Equal(before.Now.UnixNano()))
		_, err = f.db.Exec(`DROP TRIGGER deny_lock_handoff`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.ledger.RenewLock(a.Current.GrantID, nil)
		gomega.Expect(err).To(gomega.MatchError(ErrLockExpired))
		gomega.Expect(f.list().Requests[1].Generation).To(gomega.Equal(int64(2)))
		_, err = f.db.Exec(`CREATE TRIGGER deny_lock_receipt BEFORE UPDATE OF acquire_receipt ON managed_lock_requests BEGIN SELECT RAISE(ABORT,'receipt denied'); END`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.ledger.AcquireLock(LockAcquireRequest{LockID: "review", RequestKey: "failed-key", Holder: "holder-failed-key"}, "installation-lock-test", nil)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(f.list().Requests).To(gomega.HaveLen(2))
		_, err = f.db.Exec(`DROP TRIGGER deny_lock_receipt`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.acquire("failed-key").Current.State).To(gomega.Equal("queued"))
		var integrity string
		gomega.Expect(f.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity)).To(gomega.Succeed())
		gomega.Expect(integrity).To(gomega.Equal("ok"))
	})
	ginkgo.It("binds page cursors to installation/filter/upper sequence and reads the clock in the transaction", func() {
		f := newLockFixture()
		f.acquire("a")
		f.acquire("b")
		f.acquire("c")
		page, err := f.ledger.LocksPage("review", "", 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(page.More).To(gomega.BeTrue())
		f.acquire("after-page")
		all := page.Requests
		for page.More {
			page, err = f.ledger.LocksPage("review", page.NextCursor, 1)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			all = append(all, page.Requests...)
		}
		gomega.Expect(all).To(gomega.HaveLen(3))
		first, err := f.ledger.LocksPage("review", "", 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.ledger.LocksPage("other", first.NextCursor, 1)
		gomega.Expect(err).To(gomega.MatchError(ErrInvalid))
		c, err := decodePageCursor(first.NextCursor)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		c.Installation = "wrong"
		_, err = f.ledger.LocksPage("review", encodePageCursor(c), 1)
		gomega.Expect(err).To(gomega.MatchError(ErrInvalid))
		_, err = f.ledger.LocksPage("review", "invalid", 1)
		gomega.Expect(err).To(gomega.MatchError(ErrInvalid))
		// Each page uses its own observed clock alongside its request snapshot.
		f.ledger.SetLockClock(func() (time.Time, string, error) { f.now = f.now.Add(time.Second); return f.now, "", nil })
		page, err = f.ledger.LocksPage("review", "", 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(page.Now).To(gomega.Equal(f.now))
	})
	ginkgo.It("preserves an existing schema6 installation and job through schema7 migration", func() {
		f := newLockFixture()
		gomega.Expect(f.ledger.RegisterResource(encode(ginkgo.GinkgoT(), validResource()))).To(gomega.Succeed())
		job, err := f.ledger.Admit(encode(ginkgo.GinkgoT(), validJob()), digest, "old-job-key", "old-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.db.Exec(`DELETE FROM managed_resources WHERE kind='lock'; DROP TABLE managed_lock_requests; DROP TABLE managed_lock_slots; DROP TABLE managed_lock_clock; DROP TABLE managed_lock_discontinuities; DROP TABLE managed_purges; DROP TABLE managed_evidence_recovery; DELETE FROM schema_migrations WHERE version=9; DROP TABLE managed_runner_receipts; DELETE FROM schema_migrations WHERE version=8; DELETE FROM schema_migrations WHERE version=7; PRAGMA user_version=6`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		l, err := NewLedger(f.db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current, err := l.Snapshot(job.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(job))
		installation, err := l.BindInstallation("new-seed")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(installation).To(gomega.Equal("installation-lock-test"))
	})

})
