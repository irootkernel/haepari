package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type observerTransport struct {
	body   string
	status int
	closed bool
	calls  int
	check  func(*http.Request)
}

type observerBody struct {
	io.Reader
	closed *bool
}

func (b observerBody) Close() error { *b.closed = true; return nil }

func (t *observerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls++
	if t.check != nil {
		t.check(r)
	}
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-ndjson"}}, Body: observerBody{Reader: strings.NewReader(t.body), closed: &t.closed}}, nil
}

func observerLines(records ...Record) string {
	var b bytes.Buffer
	for _, r := range records {
		gomega.Expect(json.NewEncoder(&b).Encode(r)).To(gomega.Succeed())
	}
	return b.String()
}

func observerEventRecords() []Record {
	snapshot := managed.Receipt{JobID: "job-1", JobState: "finished", JobRevision: 2}
	return []Record{
		{Schema: ObserverSchema, Type: "page", JobID: "job-1", Cursor: 3, Events: &managed.EventBatch{Snapshot: snapshot, Events: []managed.Event{{JobID: "job-1", Cursor: 3, Kind: "finished", Payload: json.RawMessage(`{}`)}}, CurrentCursor: 3, NextCursor: 3}},
		{Schema: ObserverSchema, Type: "heartbeat", JobID: "job-1", Cursor: 3},
		{Schema: ObserverSchema, Type: "end", JobID: "job-1", Cursor: 3, Reason: "finished"},
	}
}

var _ = ginkgo.Describe("Task008ObserverClient", ginkgo.Label("unit"), func() {
	ginkgo.It("uses the shared protocol and parent request builder and validates a scoped stream", func() {
		t := ginkgo.GinkgoT()
		parent := managed.ParentChain{{InstallationID: "install-1", JobID: "parent-1", LeaseGeneration: 1, Nonce: strings.Repeat("a", 64)}}
		raw, err := json.Marshal(parent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		t.Setenv(managed.ParentContextEnvironment, string(raw))
		transport := &observerTransport{body: observerLines(observerEventRecords()...), check: func(r *http.Request) {
			gomega.Expect(r.Header.Get("X-Haepari-Protocol-Min")).To(gomega.Equal("1"))
			gomega.Expect(r.Header.Get("X-Haepari-Protocol-Max")).To(gomega.Equal("1"))
			gomega.Expect(r.Header.Get(managed.ParentContextHeader)).To(gomega.Equal(string(raw)))
			gomega.Expect(r.URL.Query().Get("follow")).To(gomega.Equal("1"))
		}}
		c := &Client{http: &http.Client{Transport: transport}}
		var types []string
		err = c.FollowEvents(context.Background(), "job-1", EventsOptions{}, func(r Record) error { types = append(types, r.Type); return nil })
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(types).To(gomega.Equal([]string{"page", "heartbeat", "end"}))
		gomega.Expect(transport.calls).To(gomega.Equal(1))
		gomega.Expect(transport.closed).To(gomega.BeTrue())
	})

	ginkgo.DescribeTable("rejects invalid bounded records and incomplete streams",
		func(change func([]Record) string) {
			transport := &observerTransport{body: change(observerEventRecords())}
			c := &Client{http: &http.Client{Transport: transport}}
			gomega.Expect(c.FollowEvents(context.Background(), "job-1", EventsOptions{}, func(Record) error { return nil })).To(gomega.HaveOccurred())
			gomega.Expect(transport.closed).To(gomega.BeTrue())
		},
		ginkgo.Entry("EOF before end", func(r []Record) string { return observerLines(r[:1]...) }),
		ginkgo.Entry("end without terminal snapshot", func(r []Record) string { r[0].Events.Snapshot.JobState = "queued"; return observerLines(r...) }),
		ginkgo.Entry("end without any page", func(r []Record) string { r[2].Cursor = 0; return observerLines(r[2]) }),
		ginkgo.Entry("wrong version", func(r []Record) string { r[0].Schema = "haepari.observer/v2"; return observerLines(r...) }),
		ginkgo.Entry("wrong job", func(r []Record) string { r[0].JobID = "other"; return observerLines(r...) }),
		ginkgo.Entry("cross-job event", func(r []Record) string { r[0].Events.Events[0].JobID = "other"; return observerLines(r...) }),
		ginkgo.Entry("duplicate cursor", func(r []Record) string {
			r[0].Events.Events = append(r[0].Events.Events, r[0].Events.Events[0])
			return observerLines(r...)
		}),
		ginkgo.Entry("checkpoint skips event", func(r []Record) string { r[0].Events.NextCursor = 2; r[0].Cursor = 2; return observerLines(r...) }),
		ginkgo.Entry("more without progress", func(r []Record) string {
			r[0].Events.Events = nil
			r[0].Events.NextCursor = 0
			r[0].Cursor = 0
			r[0].Events.More = true
			return observerLines(r...)
		}),
		ginkgo.Entry("heartbeat advances cursor", func(r []Record) string { r[1].Cursor++; return observerLines(r...) }),
		ginkgo.Entry("unknown type", func(r []Record) string { r[0].Type = "progress"; return observerLines(r...) }),
		ginkgo.Entry("trailing JSON", func(r []Record) string { return strings.TrimSpace(observerLines(r[0])) + " {}\n" }),
		ginkgo.Entry("duplicate JSON key", func([]Record) string { return `{"schema":"haepari.observer/v1","schema":"haepari.observer/v1"}` + "\n" }),
		ginkgo.Entry("unknown field", func(r []Record) string {
			return strings.Replace(observerLines(r...), `"type":"page"`, `"type":"page","unexpected":true`, 1)
		}),
		ginkgo.Entry("invalid UTF8 JSON", func([]Record) string { return "{\"schema\":\"\xff\"}\n" }),
		ginkgo.Entry("oversized record", func([]Record) string { return strings.Repeat("x", maxRecordBytes+1) + "\n" }),
	)

	ginkgo.It("applies an explicit retained event checkpoint before resuming", func() {
		r := observerEventRecords()
		r[0].Type = "resync"
		r[0].Events.Events = nil
		snapshot := r[0].Events.Snapshot
		r[0].Events.Resync = &snapshot
		transport := &observerTransport{body: observerLines(r...)}
		c := &Client{http: &http.Client{Transport: transport}}
		gomega.Expect(c.FollowEvents(context.Background(), "job-1", EventsOptions{After: 1}, func(Record) error { return nil })).To(gomega.Succeed())
	})

	ginkgo.It("retains exact invalid UTF8 bytes across a reported log replacement", func() {
		bytes := []byte{0xff, 0x00, 0xfe}
		snapshot := managed.Receipt{JobID: "job-1", JobState: "finished"}
		records := []Record{
			{Schema: ObserverSchema, Type: "resync", JobID: "job-1", Identity: "new", Logs: &LogPage{JobID: "job-1", Identity: "new", Size: 3, Data: []byte{}, More: true, Snapshot: snapshot, Gap: &LogGap{Reason: "replaced", RequestedOffset: 8, PreviousIdentity: "old"}}},
			{Schema: ObserverSchema, Type: "page", JobID: "job-1", Identity: "new", Offset: 3, Logs: &LogPage{JobID: "job-1", Identity: "new", NextOffset: 3, Size: 3, Data: bytes, Snapshot: snapshot}},
			{Schema: ObserverSchema, Type: "end", JobID: "job-1", Offset: 3, Identity: "new", Reason: "finished"},
		}
		transport := &observerTransport{body: observerLines(records...)}
		c := &Client{http: &http.Client{Transport: transport}}
		var got []byte
		gomega.Expect(c.FollowLogs(context.Background(), "job-1", LogsOptions{After: 8, Identity: "old"}, func(r Record) error {
			if r.Logs != nil {
				got = append(got, r.Logs.Data...)
			}
			return nil
		})).To(gomega.Succeed())
		gomega.Expect(got).To(gomega.Equal(bytes))
	})

	ginkgo.DescribeTable("preserves retained terminal log evidence gaps", func(reason string) {
		retained := []byte("abcdefgh")
		for _, after := range []int64{0, 4, 8} {
			identity := "file-1"
			if after == 0 {
				identity = ""
			}
			page := LogPage{JobID: "job-1", Identity: "file-1", Offset: after, NextOffset: 8, Size: 8, Data: retained[after:], Snapshot: managed.Receipt{JobID: "job-1", JobState: "finished"}, Gap: &LogGap{Reason: reason, RequestedOffset: after, ResumeOffset: 8, PreviousIdentity: "file-1"}}
			body := mustJSON(ginkgo.GinkgoT(), Envelope{Schema: "haepari.envelope/v1", Operation: "logs", OK: true, Data: mustJSON(ginkgo.GinkgoT(), page)})
			c := &Client{http: &http.Client{Transport: responseTransport{body: body}}}
			got, _, err := c.Logs(context.Background(), "job-1", LogsOptions{After: after, Identity: identity})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(got.Data).To(gomega.Equal(retained[after:]))
			gomega.Expect(got.Gap).To(gomega.Equal(page.Gap))
			gomega.Expect(got.NextOffset).To(gomega.Equal(int64(8)))

			transport := &observerTransport{body: observerLines(
				Record{Schema: ObserverSchema, Type: "resync", JobID: "job-1", Offset: 8, Identity: "file-1", Logs: &page},
				Record{Schema: ObserverSchema, Type: "end", JobID: "job-1", Offset: 8, Identity: "file-1", Reason: "finished"},
			)}
			c = &Client{http: &http.Client{Transport: transport}}
			followed := []byte{}
			var types []string
			err = c.FollowLogs(context.Background(), "job-1", LogsOptions{After: after, Identity: identity}, func(r Record) error {
				types = append(types, r.Type)
				if r.Logs != nil {
					followed = append(followed, r.Logs.Data...)
					gomega.Expect(r.Logs.Gap).To(gomega.Equal(page.Gap))
				}
				return nil
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(followed).To(gomega.Equal([]byte(retained[after:])))
			gomega.Expect(types).To(gomega.Equal([]string{"resync", "end"}))
			gomega.Expect(transport.closed).To(gomega.BeTrue())
		}
	},
		ginkgo.Entry("native log bound", "log_limit"),
		ginkgo.Entry("native write failure", "log_write_failed"),
		ginkgo.Entry("native read failure", "log_read_failed"),
		ginkgo.Entry("pinned bound", "pinned_log_exceeded"),
		ginkgo.Entry("missing native manifest", "native_log_manifest_missing"),
		ginkgo.Entry("uncollected guest log", "guest_log_uncollected"),
	)

	ginkgo.DescribeTable("rejects malformed terminal log evidence gaps", func(change func(*LogPage)) {
		page := LogPage{JobID: "job-1", Identity: "file-1", Offset: 4, NextOffset: 8, Size: 8, Data: []byte("efgh"), Snapshot: managed.Receipt{JobID: "job-1", JobState: "finished"}, Gap: &LogGap{Reason: "log_limit", RequestedOffset: 4, ResumeOffset: 8, PreviousIdentity: "file-1"}}
		change(&page)
		body := mustJSON(ginkgo.GinkgoT(), Envelope{Schema: "haepari.envelope/v1", Operation: "logs", OK: true, Data: mustJSON(ginkgo.GinkgoT(), page)})
		c := &Client{http: &http.Client{Transport: responseTransport{body: body}}}
		_, _, err := c.Logs(context.Background(), "job-1", LogsOptions{After: 4, Identity: "file-1"})
		gomega.Expect(err).To(gomega.HaveOccurred())
	},
		ginkgo.Entry("unknown reason", func(p *LogPage) { p.Gap.Reason = "other" }),
		ginkgo.Entry("wrong requested offset", func(p *LogPage) { p.Gap.RequestedOffset++ }),
		ginkgo.Entry("wrong previous identity", func(p *LogPage) { p.Gap.PreviousIdentity = "other" }),
		ginkgo.Entry("reset checkpoint", func(p *LogPage) { p.Gap.ResumeOffset = 0 }),
		ginkgo.Entry("unfinished job", func(p *LogPage) { p.Snapshot.JobState = "running" }),
		ginkgo.Entry("undrained bytes", func(p *LogPage) { p.Size++; p.More = true }),
		ginkgo.Entry("file replacement", func(p *LogPage) { p.Identity = "other"; p.Gap.PreviousIdentity = "other" }),
		ginkgo.Entry("missing identity", func(p *LogPage) { p.Identity = ""; p.Gap.PreviousIdentity = "" }),
		ginkgo.Entry("rewound retained prefix", func(p *LogPage) { p.Offset = 3; p.Data = []byte("defgh") }),
	)

	ginkgo.DescribeTable("rejects log range and identity corruption", func(change func(*LogPage)) {
		p := LogPage{JobID: "job-1", Identity: "file-1", Offset: 3, NextOffset: 6, Size: 6, Data: []byte("abc"), Snapshot: managed.Receipt{JobID: "job-1", JobState: "finished"}}
		change(&p)
		gomega.Expect(validateLogs("job-1", 3, "file-1", p)).To(gomega.HaveOccurred())
	},
		ginkgo.Entry("byte count", func(p *LogPage) { p.NextOffset++ }),
		ginkgo.Entry("offset rewind", func(p *LogPage) { p.Offset--; p.NextOffset-- }),
		ginkgo.Entry("replacement without gap", func(p *LogPage) { p.Identity = "other" }),
		ginkgo.Entry("cross-job snapshot", func(p *LogPage) { p.Snapshot.JobID = "other" }),
		ginkgo.Entry("oversized raw page", func(p *LogPage) {
			p.Data = make([]byte, MaxLogBytes+1)
			p.Size = int64(len(p.Data)) + 3
			p.NextOffset = p.Size
		}),
		ginkgo.Entry("false more", func(p *LogPage) { p.More = true }),
	)

	ginkgo.It("closes only observation on callback failure and does not retry", func() {
		transport := &observerTransport{body: observerLines(observerEventRecords()...)}
		c := &Client{http: &http.Client{Transport: transport}}
		stop := errors.New("consumer stopped")
		gomega.Expect(c.FollowEvents(context.Background(), "job-1", EventsOptions{}, func(Record) error { return stop })).To(gomega.MatchError(stop))
		gomega.Expect(transport.closed).To(gomega.BeTrue())
		gomega.Expect(transport.calls).To(gomega.Equal(1))
	})

	ginkgo.It("delivers a bounded error record and returns its typed issue without waiting for end", func() {
		transport := &observerTransport{body: observerLines(Record{Schema: ObserverSchema, Type: "error", JobID: "job-1", Error: &Issue{Code: "STORAGE_UNAVAILABLE", Message: "owned evidence missing", Retry: "reconcile"}})}
		c := &Client{http: &http.Client{Transport: transport}}
		records := 0
		err := c.FollowEvents(context.Background(), "job-1", EventsOptions{}, func(r Record) error { records++; gomega.Expect(r.Type).To(gomega.Equal("error")); return nil })
		var issue *Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
		gomega.Expect(records).To(gomega.Equal(1))
		gomega.Expect(transport.closed).To(gomega.BeTrue())
	})

	ginkgo.It("rejects malformed parent context before transport and returns typed admission errors", func() {
		t := ginkgo.GinkgoT()
		transport := &observerTransport{}
		c := &Client{http: &http.Client{Transport: transport}}
		t.Setenv(managed.ParentContextEnvironment, "invalid")
		gomega.Expect(c.FollowLogs(context.Background(), "job-1", LogsOptions{}, func(Record) error { return nil })).To(gomega.HaveOccurred())
		gomega.Expect(transport.calls).To(gomega.BeZero())
		t.Setenv(managed.ParentContextEnvironment, "")
		raw, err := json.Marshal(Envelope{Schema: "haepari.envelope/v1", Operation: "events", Error: &Issue{Code: "NESTED_WAIT_REJECTED", Message: "blocked", Retry: "none"}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		transport.body, transport.status = string(raw), http.StatusBadRequest
		var issue *Issue
		err = c.FollowEvents(context.Background(), "job-1", EventsOptions{}, func(Record) error { return nil })
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("NESTED_WAIT_REJECTED"))
	})
})
