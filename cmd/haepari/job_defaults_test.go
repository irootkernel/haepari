package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func deadlineFetch(value int64, calls *int) func() (config.Deadlines, error) {
	return func() (config.Deadlines, error) {
		*calls++
		return config.Deadlines{
			QueueSeconds: value, PrepareSeconds: value, RunSeconds: value,
			ManualSeconds: value, CollectSeconds: value, CleanupSeconds: value,
		}, nil
	}
}

var _ = ginkgo.Describe("MaterializeActiveDeadlines", ginkgo.Label("unit"), func() {
	const explicit = `{"mode":"auto","deadlines":{"queue_seconds":10,"prepare_seconds":10,"run_seconds":10,"collect_seconds":10,"cleanup_seconds":10}}`

	ginkgo.It("leaves a fully explicit request unchanged and does not fetch", func() {
		calls := 0
		out, filled, err := materializeActiveDeadlines([]byte(explicit), deadlineFetch(10, &calls))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(filled).To(gomega.BeFalse())
		gomega.Expect(string(out)).To(gomega.Equal(explicit))
		gomega.Expect(calls).To(gomega.BeZero())
	})

	ginkgo.It("fills only an absent active phase", func() {
		calls := 0
		spec := []byte(`{"mode":"auto","deadlines":{"prepare_seconds":10,"run_seconds":10,"collect_seconds":10,"cleanup_seconds":10}}`)
		out, filled, err := materializeActiveDeadlines(spec, deadlineFetch(10, &calls))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(filled).To(gomega.BeTrue())
		gomega.Expect(calls).To(gomega.Equal(1))
		gomega.Expect(string(out)).To(gomega.ContainSubstring(`"queue_seconds":10`))
		gomega.Expect(string(out)).NotTo(gomega.ContainSubstring("manual_seconds"))

		manual := []byte(`{"mode":"manual","deadlines":{"queue_seconds":10,"prepare_seconds":10,"collect_seconds":10,"cleanup_seconds":10}}`)
		out, filled, err = materializeActiveDeadlines(manual, deadlineFetch(10, &calls))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(filled).To(gomega.BeTrue())
		gomega.Expect(string(out)).To(gomega.ContainSubstring(`"manual_seconds":10`))
		gomega.Expect(string(out)).NotTo(gomega.ContainSubstring("run_seconds"))
	})

	ginkgo.It("does not fetch or rewrite explicit zero, null, wrong type, duplicate, invalid mode, or a deadlines array", func() {
		cases := []string{
			strings.Replace(explicit, `"run_seconds":10`, `"run_seconds":0`, 1),
			strings.Replace(explicit, `"queue_seconds":10`, `"queue_seconds":null`, 1),
			strings.Replace(explicit, `"queue_seconds":10`, `"queue_seconds":"10"`, 1),
			strings.Replace(explicit, `"queue_seconds":10`, `"queue_seconds":10,"queue_seconds":10`, 1),
			strings.Replace(explicit, `"mode":"auto"`, `"mode":"batch"`, 1),
			`{"mode":"auto","deadlines":[]}`,
		}
		for _, spec := range cases {
			calls := 0
			out, filled, err := materializeActiveDeadlines([]byte(spec), deadlineFetch(10, &calls))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(filled).To(gomega.BeFalse())
			gomega.Expect(string(out)).To(gomega.Equal(spec))
			gomega.Expect(calls).To(gomega.BeZero())
		}
	})

	ginkgo.It("keeps an explicit inactive zero and rejects an unclamped huge default", func() {
		job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-a", Mode: "auto", Suite: "build",
			Input:       managed.Input{Root: "/tmp/sealed-input", InventoryDigest: strings.Repeat("a", 64)},
			Environment: managed.Environment{ProfileID: "build-profile", ProfileDigest: strings.Repeat("a", 64)},
			Workload:    managed.Workload{Argv: []string{"make", "build"}, CWD: "source", RequiredOutputs: []string{"results/build.json"}},
			Deadlines:   managed.Deadlines{QueueSeconds: 10, PrepareSeconds: 10, RunSeconds: 10, CollectSeconds: 10, CleanupSeconds: 10},
			Effects:     managed.Effects{Mode: "offline", AuthorizationReference: "approved-job"},
			Provenance:  managed.Provenance{CandidateDigest: strings.Repeat("a", 64), TestsDigest: strings.Repeat("a", 64), TargetProfileDigest: strings.Repeat("a", 64), EvidenceClass: "development"}}
		raw, err := json.Marshal(job)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		inactive := strings.Replace(string(raw), `"cleanup_seconds":10`, `"cleanup_seconds":10,"manual_seconds":0`, 1)
		calls := 0
		out, filled, err := materializeActiveDeadlines([]byte(inactive), deadlineFetch(10, &calls))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(filled).To(gomega.BeFalse())
		gomega.Expect(calls).To(gomega.BeZero())
		gomega.Expect(string(out)).To(gomega.Equal(inactive))
		_, err = managed.DecodeJob(out)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		omitted := strings.Replace(string(raw), `"queue_seconds":10,`, "", 1)
		huge := int64(366*24*60*60 + 1)
		out, filled, err = materializeActiveDeadlines([]byte(omitted), deadlineFetch(huge, &calls))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(filled).To(gomega.BeTrue())
		gomega.Expect(string(out)).To(gomega.ContainSubstring(`"queue_seconds":` + strconv.FormatInt(huge, 10)))
		_, err = managed.DecodeJob(out)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("keeps two fetched defaults distinct and returns a fetch error", func() {
		firstCalls, secondCalls := 0, 0
		spec := []byte(`{"mode":"auto","deadlines":{"prepare_seconds":10,"run_seconds":10,"collect_seconds":10,"cleanup_seconds":10}}`)
		first, _, err := materializeActiveDeadlines(spec, deadlineFetch(10, &firstCalls))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		second, _, err := materializeActiveDeadlines(spec, deadlineFetch(11, &secondCalls))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(first)).NotTo(gomega.Equal(string(second)))
		_, _, err = materializeActiveDeadlines(spec, func() (config.Deadlines, error) {
			return config.Deadlines{}, errors.New("projection down")
		})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("projection down")))
	})
})
