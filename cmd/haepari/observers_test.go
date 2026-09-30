package main

import (
	"bytes"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task008CLIInvalidObservers", ginkgo.Label("unit"), func() {
	ginkgo.It("rejects unsafe observer and cancellation arguments before controller contact", func() {
		t := ginkgo.GinkgoT()
		t.Setenv("HAEPARI_HOME", "/unavailable-controller-parent/state")
		for _, args := range [][]string{
			{"jobs", "list", "--limit", "101", "--json"},
			{"resources", "list", "--limit", "0", "--json"},
			{"events", "job-1", "--after", "-1", "--json"},
			{"events", "job-1", "--follow"},
			{"events", "job-1", "--limit", "1001", "--json"},
			{"events", "job-1", "--limit", "0", "--json"},
			{"logs", "job-1", "--after", "-1"},
			{"cancel", "job-1", "--expected-revision", "0", "--json"},
			{"cancel", "job-1", "--expected-revision", "1", "--request-key", "bad key", "--json"},
		} {
			var out, err bytes.Buffer
			gomega.Expect(run(args, &out, &err)).To(gomega.Equal(2), args)
			gomega.Expect(out.Len()).To(gomega.Equal(0))
			gomega.Expect(err.Len()).To(gomega.BeNumerically(">", 0))
		}
	})
})
