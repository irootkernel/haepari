package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task037StartupLimits", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses invalid global limits before resolving or creating controller state", func() {
		root := filepath.Join(ginkgo.GinkgoT().TempDir(), "absent-state")
		ginkgo.GinkgoT().Setenv("HAEPARI_HOME", root)
		for _, limit := range []string{"0", "-1", "bad", "999999999999999999999999"} {
			var out, err bytes.Buffer
			gomega.Expect(run([]string{"daemon", "run", "--global-limit", limit}, &out, &err)).To(gomega.Equal(2))
			_, e := os.Lstat(root)
			gomega.Expect(os.IsNotExist(e)).To(gomega.BeTrue())
		}
	})
	ginkgo.It("pins explicit positive execution limits in v2 and preserves exact v1 defaults", func() {
		parent := ginkgo.GinkgoT().TempDir()
		binary := filepath.Join(parent, "binary")
		gomega.Expect(os.WriteFile(binary, []byte("fixture"), 0700)).To(gomega.Succeed())
		legacy, err := planDevelopmentService("default-plan", binary, filepath.Join(parent, "legacy"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(legacy.Schema).To(gomega.Equal("haepari.development-service-plan/v1"))
		gomega.Expect(legacy.Plist).NotTo(gomega.ContainSubstring("--global-limit"))
		for _, limit := range []int{1, 2, 3} {
			p, err := planDevelopmentServiceLimits("explicit-plan", binary, filepath.Join(parent, "explicit"), limit, 2, true)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(p.Schema).To(gomega.Equal("haepari.development-service-plan/v2"))
			gomega.Expect(p.Plist).To(gomega.ContainSubstring("<string>--host-macos-guest-limit</string><string>2</string>"))
			file := filepath.Join(parent, "plan.json")
			data, _ := json.Marshal(p)
			gomega.Expect(os.WriteFile(file, data, 0600)).To(gomega.Succeed())
			verified, e := readDevelopmentServicePlan(file, true)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			gomega.Expect(verified).To(gomega.Equal(p))
			p.GlobalLimit++
			data, _ = json.Marshal(p)
			gomega.Expect(os.WriteFile(file, data, 0600)).To(gomega.Succeed())
			_, e = readDevelopmentServicePlan(file, true)
			gomega.Expect(e).To(gomega.MatchError(managed.ErrConflict))
		}
		for _, limits := range [][2]int{{0, 1}, {-1, 1}, {1, 0}, {1, 3}} {
			_, e := planDevelopmentServiceLimits("bad", binary, filepath.Join(parent, "bad"), limits[0], limits[1], true)
			gomega.Expect(e).To(gomega.MatchError(managed.ErrInvalid))
		}
		data, _ := json.Marshal(legacy)
		data = []byte(strings.TrimSuffix(string(data), "}") + `,"global_limit":null}`)
		file := filepath.Join(parent, "legacy.json")
		gomega.Expect(os.WriteFile(file, data, 0600)).To(gomega.Succeed())
		_, err = readDevelopmentServicePlan(file, true)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		_, e := os.Lstat(filepath.Join(parent, "explicit"))
		gomega.Expect(os.IsNotExist(e)).To(gomega.BeTrue())
	})
})
