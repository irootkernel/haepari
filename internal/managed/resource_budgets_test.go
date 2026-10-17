package managed

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

const v1ResourceGolden = `{"schema":"haepari.resource/v1","id":"local-a","kind":"local","execution_mode":"managed","capacity":2,"revision":1,"adapter":{"qualification_reference":"local-observation"},"cleanup":{"policy_reference":"local-policy"}}`

func phaseCeilings(n int64) *LifecycleBudgets {
	return &LifecycleBudgets{QueueSeconds: n, PrepareSeconds: n, RunSeconds: n, ManualSeconds: n, CollectSeconds: n, CleanupSeconds: n}
}

var _ = ginkgo.Describe("ResourceLifecycleBudgets", ginkgo.Label("unit"), func() {
	ginkgo.It("keeps v1 canonical bytes and rejects lifecycle budgets", func() {
		raw := encode(ginkgo.GinkgoT(), validResource())
		gomega.Expect(string(raw)).To(gomega.Equal(v1ResourceGolden))
		decoded, err := DecodeResource(raw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(encode(ginkgo.GinkgoT(), decoded)).To(gomega.Equal(raw))
		for _, body := range []string{
			`,"lifecycle_budgets":null`,
			`,"lifecycle_budgets":{"queue_seconds":1,"prepare_seconds":1,"run_seconds":1,"manual_seconds":1,"collect_seconds":1,"cleanup_seconds":1}`,
		} {
			mutated := []byte(strings.Replace(v1ResourceGolden, `"revision":1`, `"revision":1`+body, 1))
			_, err = DecodeResource(mutated)
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		}
		_, err = DecodeResource([]byte(`{"schema":"haepari.resource/v1","id":"review","kind":"lock","lifecycle_budgets":null}`))
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("closes v2 around six positive ceilings", func() {
		resource := validResource()
		resource.Schema = "haepari.resource/v2"
		resource.LifecycleBudgets = phaseCeilings(10)
		raw := encode(ginkgo.GinkgoT(), resource)
		decoded, err := DecodeResource(raw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(encode(ginkgo.GinkgoT(), decoded)).To(gomega.Equal(raw))
		rawText := string(raw)
		budgets := string(encode(ginkgo.GinkgoT(), resource.LifecycleBudgets))
		singleNull := strings.Replace(rawText, `"lifecycle_budgets":`+budgets, `"lifecycle_budgets":null`, 1)
		nestedUnknown := strings.Replace(rawText, `"cleanup_seconds":10`, `"cleanup_seconds":10,"surprise":1`, 1)
		duplicateKey := strings.Replace(rawText, `"queue_seconds":10`, `"queue_seconds":10,"queue_seconds":10`, 1)
		malformed := strings.Replace(rawText, `"lifecycle_budgets":{`, `"lifecycle_budgets":null},{`, 1)
		gomega.Expect(singleNull).NotTo(gomega.Equal(rawText))
		gomega.Expect(strings.Count(singleNull, `"lifecycle_budgets"`)).To(gomega.Equal(1))
		gomega.Expect(json.Valid([]byte(singleNull))).To(gomega.BeTrue())
		gomega.Expect(json.Valid([]byte(nestedUnknown))).To(gomega.BeTrue())
		gomega.Expect(json.Valid([]byte(duplicateKey))).To(gomega.BeTrue())
		gomega.Expect(json.Valid([]byte(malformed))).To(gomega.BeFalse())
		cases := []string{
			singleNull,
			strings.Replace(rawText, `,"lifecycle_budgets":`, `,"extra":1,"lifecycle_budgets":`, 1),
			duplicateKey,
			strings.Replace(rawText, `"queue_seconds":10`, `"queue_seconds":"10"`, 1),
			strings.Replace(rawText, `"queue_seconds":10`, `"queue_seconds":9223372036854775808`, 1),
			strings.Replace(rawText, `"queue_seconds":10,`, "", 1),
			nestedUnknown,
			malformed,
		}
		without := strings.Replace(rawText, `,"lifecycle_budgets":`+budgets, "", 1)
		cases = append(cases, without)
		for _, candidate := range cases {
			_, err = DecodeResource([]byte(candidate))
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
	})

	ginkgo.It("compares only the active phase against the selected resource", func() {
		v1 := validResource()
		job := validJob()
		job.Deadlines.QueueSeconds = productPhaseCeiling + 1
		err := compareAdmissionBudgets(v1, job)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("legacy_product_ceiling")))
		job.Deadlines.QueueSeconds = 10
		gomega.Expect(compareAdmissionBudgets(v1, job)).To(gomega.Succeed())

		v2 := v1
		v2.Schema = "haepari.resource/v2"
		v2.LifecycleBudgets = phaseCeilings(100)
		v2.LifecycleBudgets.RunSeconds = 10
		job.Deadlines.RunSeconds = 11
		gomega.Expect(compareAdmissionBudgets(v2, job)).To(gomega.MatchError(gomega.ContainSubstring("run_seconds")))
		manual := job
		manual.Mode = "manual"
		manual.Deadlines.RunSeconds = 999
		manual.Deadlines.ManualSeconds = 20
		gomega.Expect(compareAdmissionBudgets(v2, manual)).To(gomega.Succeed())
	})

	ginkgo.It("parses the illustrative v2 example without workstation paths", func() {
		raw, err := os.ReadFile("../../docs/examples/resource-local-v2.template.json")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("/Users"))
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("/Volumes"))
		text := strings.ReplaceAll(string(raw), "<verified-local-supervision-receipt>", "local-receipt")
		text = strings.ReplaceAll(text, "<operator-owned-cleanup-policy>", "cleanup-policy")
		decoded, err := DecodeResource([]byte(text))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(decoded.Schema).To(gomega.Equal("haepari.resource/v2"))
		gomega.Expect(decoded.LifecycleBudgets.QueueSeconds).To(gomega.Equal(int64(7200)))
	})
})
