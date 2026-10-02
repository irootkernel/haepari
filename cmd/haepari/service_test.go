package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task011DevelopmentServicePlan", ginkgo.Label("unit"), func() {
	ginkgo.It("plans an isolated Aqua service without installation and pins the executable bytes", func() {
		gomega.Expect(runtime.GOOS+"/"+runtime.GOARCH).To(gomega.Equal("darwin/arm64"), "development-service qualification requires its supported host; a missing prerequisite must not pass")
		t := ginkgo.GinkgoT()
		parent := t.TempDir()
		binary := filepath.Join(parent, "candidate&binary")
		gomega.Expect(os.WriteFile(binary, []byte("candidate"), 0700)).To(gomega.Succeed())
		root := filepath.Join(parent, "state")
		p, err := planDevelopmentService("fixture-plan", binary, root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		resolved, err := state.Resolve(root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Root).To(gomega.Equal(resolved.Path))
		gomega.Expect(p.Plist).To(gomega.ContainSubstring("candidate&amp;binary"))
		gomega.Expect(p.Plist).To(gomega.ContainSubstring("<key>AbandonProcessGroup</key><true/>"))
		gomega.Expect(p.Plist).To(gomega.ContainSubstring("<key>LimitLoadToSessionType</key><string>Aqua</string>"))
		gomega.Expect(p.Domain).To(gomega.HavePrefix("gui/"))
		_, err = os.Lstat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		file := filepath.Join(parent, "plan.json")
		raw, _ := json.Marshal(p)
		gomega.Expect(os.WriteFile(file, raw, 0600)).To(gomega.Succeed())
		verified, err := readDevelopmentServicePlan(file, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(verified).To(gomega.Equal(p))
		gomega.Expect(os.WriteFile(binary, []byte("changed"), 0700)).To(gomega.Succeed())
		_, err = readDevelopmentServicePlan(file, true)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrConflict))
		_, err = readDevelopmentServicePlan(file, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = planDevelopmentService("../foreign", binary, root)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		defaultRoot, err := state.Resolve("")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = planDevelopmentService("fixture-plan", binary, defaultRoot.Path)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
	})
	ginkgo.It("refuses adopting nonempty or foreign instance state and preserves owned state", func() {
		root := filepath.Join(ginkgo.GinkgoT().TempDir(), "state")
		gomega.Expect(os.Mkdir(root, 0700)).To(gomega.Succeed())
		p := developmentServicePlan{Root: root, Instance: "owner", OwnerUID: os.Geteuid()}
		foreign := filepath.Join(root, "foreign")
		gomega.Expect(os.WriteFile(foreign, []byte("keep"), 0600)).To(gomega.Succeed())
		gomega.Expect(developmentRootOwner(p, true)).To(gomega.MatchError(managed.ErrConflict))
		gomega.Expect(os.Remove(foreign)).To(gomega.Succeed())
		gomega.Expect(developmentRootOwner(p, true)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(foreign, []byte("keep"), 0600)).To(gomega.Succeed())
		gomega.Expect(developmentRootOwner(p, true)).To(gomega.Succeed())
		p.Instance = "different"
		gomega.Expect(developmentRootOwner(p, true)).To(gomega.MatchError(managed.ErrConflict))
		data, err := os.ReadFile(foreign)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(strings.TrimSpace(string(data))).To(gomega.Equal("keep"))
	})
})
