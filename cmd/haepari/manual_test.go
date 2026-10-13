package main

import (
	"bytes"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"os"
	"path/filepath"
	"strings"
)

var _ = ginkgo.Describe("Task019ManualCLIRefusals", ginkgo.Label("unit"), func() {
	ginkgo.It("names missing required flags and refuses an oversized document before connecting", func() {
		for _, args := range [][]string{{"manual", "complete", "job-one", "--input", "record.json", "--expected-revision", "1"}, {"manual", "complete", "job-one", "--json", "--expected-revision", "1"}, {"manual", "complete", "job-one", "--input", "record.json", "--expected-revision", "0", "--json"}} {
			var out, diag bytes.Buffer
			gomega.Expect(run(args, &out, &diag)).To(gomega.Equal(2))
			gomega.Expect(diag.String()).To(gomega.ContainSubstring("usage: haepari manual complete"))
			gomega.Expect(out.Len()).To(gomega.BeZero())
		}
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "large.json")
		gomega.Expect(os.WriteFile(path, []byte(strings.Repeat(" ", (1<<20)+1)), 0600)).To(gomega.Succeed())
		var out, diag bytes.Buffer
		gomega.Expect(run([]string{"manual", "complete", "job-one", "--input", path, "--expected-revision", "1", "--json"}, &out, &diag)).To(gomega.Equal(2))
		gomega.Expect(diag.String()).To(gomega.ContainSubstring("invalid manual observation document"))
		gomega.Expect(out.Len()).To(gomega.BeZero())
	})
})
