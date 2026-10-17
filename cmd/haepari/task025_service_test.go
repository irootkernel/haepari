package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task025ReleaseServicePlan", ginkgo.Label("unit"), func() {
	ginkgo.It("plans the explicit release identity with pinned limits and executable bytes", func() {
		gomega.Expect(runtime.GOOS+"/"+runtime.GOARCH).To(gomega.Equal("darwin/arm64"), "release-service planning requires its supported host; a missing prerequisite must not pass")
		parent := ginkgo.GinkgoT().TempDir()
		binary := filepath.Join(parent, "candidate")
		gomega.Expect(os.WriteFile(binary, []byte("release"), 0700)).To(gomega.Succeed())
		root := filepath.Join(parent, "release-state")
		p, err := planReleaseService("h7-t25", binary, root, 2, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Schema).To(gomega.Equal(releaseServicePlanSchema))
		gomega.Expect(p.Label).To(gomega.Equal("io.rootkernel.haepari.h7-t25"))
		gomega.Expect(p.PlistPath).To(gomega.HaveSuffix("io.rootkernel.haepari.h7-t25.plist"))
		gomega.Expect(p.GlobalLimit).To(gomega.Equal(2))
		gomega.Expect(p.HostMACOSGuestLimit).To(gomega.Equal(1))
		gomega.Expect(p.Plist).To(gomega.ContainSubstring(plistString("--global-limit") + plistString("2")))
		gomega.Expect(p.Plist).To(gomega.ContainSubstring(plistString("--host-macos-guest-limit") + plistString("1")))
		gomega.Expect(p.Plist).To(gomega.ContainSubstring("<key>AbandonProcessGroup</key><true/>"))
		gomega.Expect(p.Plist).To(gomega.ContainSubstring("<key>LimitLoadToSessionType</key><string>Aqua</string>"))
		_, err = os.Lstat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		file := filepath.Join(parent, "release-plan.json")
		raw, _ := json.Marshal(p)
		gomega.Expect(os.WriteFile(file, raw, 0600)).To(gomega.Succeed())
		verified, err := readDevelopmentServicePlan(file, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(verified).To(gomega.Equal(p))
		gomega.Expect(os.WriteFile(binary, []byte("changed"), 0700)).To(gomega.Succeed())
		_, err = readDevelopmentServicePlan(file, true)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrConflict))
	})
	ginkgo.It("defaults the release plan limits to one and refuses invalid input like the development plan", func() {
		parent := ginkgo.GinkgoT().TempDir()
		binary := filepath.Join(parent, "candidate")
		gomega.Expect(os.WriteFile(binary, []byte("release"), 0700)).To(gomega.Succeed())
		root := filepath.Join(parent, "release-state")
		p, err := planReleaseService("h7-t25", binary, root, 1, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.GlobalLimit).To(gomega.Equal(1))
		gomega.Expect(p.Plist).To(gomega.ContainSubstring(plistString("--global-limit") + plistString("1")))
		_, err = planReleaseService("h7-t25", binary, root, 0, 1)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		_, err = planReleaseService("h7-t25", binary, root, 1, 3)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		_, err = planReleaseService("../foreign", binary, root, 1, 1)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		defaultRoot, err := state.Resolve("")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = planReleaseService("h7-t25", binary, defaultRoot.Path, 1, 1)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
	})
	ginkgo.It("rejects malformed release plans and keeps development plan shapes unchanged", func() {
		parent := ginkgo.GinkgoT().TempDir()
		binary := filepath.Join(parent, "candidate")
		gomega.Expect(os.WriteFile(binary, []byte("release"), 0700)).To(gomega.Succeed())
		root := filepath.Join(parent, "release-state")
		release, err := planReleaseService("h7-t25", binary, root, 1, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		write := func(mutate func(map[string]any)) string {
			document := map[string]any{}
			raw, _ := json.Marshal(release)
			gomega.Expect(json.Unmarshal(raw, &document)).To(gomega.Succeed())
			mutate(document)
			path := filepath.Join(parent, "mutated.json")
			gomega.Expect(os.WriteFile(path, mustJSON(document), 0600)).To(gomega.Succeed())
			return path
		}
		_, err = readDevelopmentServicePlan(write(func(d map[string]any) { d["unexpected"] = true }), false)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		_, err = readDevelopmentServicePlan(write(func(d map[string]any) { delete(d, "global_limit") }), false)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		_, err = readDevelopmentServicePlan(write(func(d map[string]any) { d["label"] = "io.rootkernel.haepari.dev.h7-t25" }), false)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		_, err = readDevelopmentServicePlan(write(func(d map[string]any) { d["global_limit"] = 0 }), false)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		dev, err := planDevelopmentService("h7-t25", binary, root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(dev.Schema).To(gomega.Equal(devServicePlanSchemaV1))
		gomega.Expect(dev.Label).To(gomega.Equal("io.rootkernel.haepari.dev.h7-t25"))
		gomega.Expect(dev.Plist).NotTo(gomega.ContainSubstring("--global-limit"))
		devLimits, err := planDevelopmentServiceLimits("h7-t25", binary, root, 2, 2, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(devLimits.Schema).To(gomega.Equal(devServicePlanSchemaV2))
		gomega.Expect(devLimits.Label).To(gomega.Equal("io.rootkernel.haepari.dev.h7-t25"))
		gomega.Expect(devLimits.Plist).To(gomega.ContainSubstring(plistString("--global-limit") + plistString("2")))
	})
	ginkgo.It("separates release and development owner markers on the same root", func() {
		root := filepath.Join(ginkgo.GinkgoT().TempDir(), "state")
		gomega.Expect(os.MkdirAll(root, 0700)).To(gomega.Succeed())
		release := developmentServicePlan{Schema: releaseServicePlanSchema, Root: root, Instance: "h7-t25", OwnerUID: os.Geteuid()}
		gomega.Expect(developmentRootOwner(release, true)).To(gomega.Succeed())
		raw, err := os.ReadFile(filepath.Join(root, ".development-service.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(raw)).To(gomega.ContainSubstring(releaseServiceOwnerSchema))
		dev := developmentServicePlan{Schema: devServicePlanSchemaV1, Root: root, Instance: "h7-t25", OwnerUID: os.Geteuid()}
		gomega.Expect(developmentRootOwner(dev, true)).To(gomega.MatchError(managed.ErrConflict))
	})
})

func mustJSON(document map[string]any) []byte {
	raw, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return raw
}
