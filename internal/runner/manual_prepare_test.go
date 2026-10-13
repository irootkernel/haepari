package runner

import (
	"bytes"
	"encoding/json"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"strings"
)

var _ = ginkgo.Describe("Task019PrivatePreparationRefusals", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses absent parent authority and malformed/unknown/oversized structured steps without executing", func() {
		t := ginkgo.GinkgoT()
		t.Setenv(managed.ParentContextEnvironment, "")
		var out, diag bytes.Buffer
		argv, err := PreparationArgv("haepari", PreparationSteps{Workload: []string{"/bin/true"}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(Command(argv[2:], &out, &diag)).To(gomega.Equal(126))
		gomega.Expect(diag.String()).To(gomega.ContainSubstring("managed Job/lease context"))
		chain, err := json.Marshal(managed.ParentChain{{InstallationID: "fixture", JobID: "job-one", LeaseGeneration: 1, Nonce: strings.Repeat("a", 64)}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		t.Setenv(managed.ParentContextEnvironment, string(chain))
		for _, raw := range []string{`{}`, `{"workload":["/bin/true"],"unknown":true}`, `{"workload":["/bin/true"],"workload":["/bin/true"]}`, `{"workload":[""]}`, strings.Repeat(" ", (1<<20)+1)} {
			out.Reset()
			diag.Reset()
			gomega.Expect(Command([]string{manualPreparationVerb, manualPreparationFlag, raw}, &out, &diag)).To(gomega.Equal(126))
			gomega.Expect(out.Len()).To(gomega.BeZero())
			gomega.Expect(diag.Len()).To(gomega.BeNumerically(">", 0))
		}
	})
})
