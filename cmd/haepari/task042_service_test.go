package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task042ReleaseServiceDefault", ginkgo.Label("unit"), func() {
	var home, binary, planFile string
	writePlan := func(p developmentServicePlan) {
		raw, err := json.Marshal(p)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(planFile, raw, 0600)).To(gomega.Succeed())
	}
	ginkgo.BeforeEach(func() {
		var err error
		home, err = filepath.EvalSymlinks(ginkgo.GinkgoT().TempDir())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoT().Setenv("HOME", home)
		binary = filepath.Join(home, "candidate")
		planFile = filepath.Join(home, "plan.json")
		gomega.Expect(os.WriteFile(binary, []byte("task042-fixture\n"), 0700)).To(gomega.Succeed())
	})

	ginkgo.It("plans default and nondefault release v2 through the CLI without creating state", func() {
		for _, root := range []string{filepath.Join(home, ".haepari"), filepath.Join(home, "other-state")} {
			var out, stderr bytes.Buffer
			gomega.Expect(serviceCommand([]string{"plan", "--release", "--instance", "task042-fixture", "--binary", binary, "--root", root, "--json"}, &out, &stderr)).To(gomega.Equal(0), stderr.String())
			var envelope struct {
				Data developmentServicePlan `json:"data"`
			}
			gomega.Expect(json.Unmarshal(out.Bytes(), &envelope)).To(gomega.Succeed())
			p := envelope.Data
			gomega.Expect(p.Schema).To(gomega.Equal(releaseServicePlanSchemaV2))
			gomega.Expect(p.Label).To(gomega.Equal("io.rootkernel.haepari.task042-fixture"))
			gomega.Expect(p.GlobalLimit).To(gomega.Equal(1))
			gomega.Expect(p.HostMACOSGuestLimit).To(gomega.Equal(1))
			writePlan(p)
			for _, verifyBinary := range []bool{true, false} {
				read, err := readDevelopmentServicePlan(planFile, verifyBinary)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(read).To(gomega.Equal(p))
			}
			_, err := os.Lstat(root)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			_, err = os.Lstat(p.PlistPath)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})

	ginkgo.It("reads and recreates exact legacy release v1 and development v1/v2 wire fixtures", func() {
		// Golden data objects were captured from the unchanged TASK-041 candidate.
		// Only the private HOME and owning UID are localized; fixture bytes stay fixed.
		for _, name := range []string{"release-v1", "development-v1", "development-v2"} {
			raw, err := os.ReadFile(filepath.Join("testdata", "task042-"+name+".json.tmpl"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			localized := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(string(raw), "@HOME@", home), "@UID@", strconv.Itoa(os.Geteuid())))
			gomega.Expect(os.WriteFile(planFile, []byte(localized), 0600)).To(gomega.Succeed())
			for _, verifyBinary := range []bool{true, false} {
				p, err := readDevelopmentServicePlan(planFile, verifyBinary)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), name)
				recreated, err := recreateDevelopmentServicePlan(p)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				encoded, err := json.Marshal(recreated)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(string(encoded)).To(gomega.Equal(localized), name)
			}
		}
	})

	ginkgo.It("refuses legacy release and development default roots on planning and both saved-plan paths", func() {
		root := filepath.Join(home, ".haepari")
		p, err := planReleaseService("task042-fixture", binary, root, 2, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, schema := range []string{releaseServicePlanSchemaV1, devServicePlanSchemaV1, devServicePlanSchemaV2} {
			_, err := planManagedService(p.Instance, binary, root, 2, 1, schema)
			gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
			legacy := p
			legacy.Schema = schema
			legacy.Label = serviceLabel(schema, p.Instance)
			legacy.PlistPath = filepath.Join(home, "Library", "LaunchAgents", legacy.Label+".plist")
			if schema == devServicePlanSchemaV1 {
				legacy.GlobalLimit, legacy.HostMACOSGuestLimit = 0, 0
			}
			legacy.Plist = servicePlist(legacy)
			writePlan(legacy)
			for _, verifyBinary := range []bool{true, false} {
				_, err = readDevelopmentServicePlan(planFile, verifyBinary)
				gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid), schema)
			}
			called := false
			err = uninstallDevelopmentServiceWithLauncher(legacy, func(...string) error { called = true; return nil })
			gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
			gomega.Expect(called).To(gomega.BeFalse())
		}
	})

	ginkgo.It("installs and uninstalls default and nondefault roots with controlled native calls and retained state", func() {
		for _, root := range []string{filepath.Join(home, ".haepari"), filepath.Join(home, "other-state")} {
			p, err := planReleaseService("task042-fixture", binary, root, 2, 1)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var calls [][]string
			launch := func(args ...string) error {
				calls = append(calls, append([]string{}, args...))
				raw, err := os.ReadFile(p.PlistPath)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(string(raw)).To(gomega.Equal(p.Plist))
				return nil
			}
			gomega.Expect(installDevelopmentServiceWithLauncher(p, launch)).To(gomega.Succeed())
			marker, err := os.ReadFile(filepath.Join(root, ".development-service.json"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(string(marker)).To(gomega.ContainSubstring(releaseServiceOwnerSchema))
			retained := filepath.Join(root, "retained-result.json")
			gomega.Expect(os.WriteFile(retained, []byte("immutable result sentinel"), 0600)).To(gomega.Succeed())
			gomega.Expect(uninstallDevelopmentServiceWithLauncher(p, launch)).To(gomega.Succeed())
			gomega.Expect(calls).To(gomega.Equal([][]string{{"bootstrap", p.Domain, p.PlistPath}, {"bootout", p.Domain + "/" + p.Label}}))
			_, err = os.Lstat(p.PlistPath)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			contents, err := os.ReadFile(retained)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(string(contents)).To(gomega.Equal("immutable result sentinel"))
			contents, err = os.ReadFile(filepath.Join(root, ".development-service.json"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(contents).To(gomega.Equal(marker))
		}
	})

	ginkgo.It("removes an exact owned nondefault legacy service when the unrelated default is unsafe", func() {
		for i, schema := range []string{releaseServicePlanSchemaV1, devServicePlanSchemaV1, devServicePlanSchemaV2} {
			root := filepath.Join(home, "legacy-"+strconv.Itoa(i))
			p, err := planManagedService("task042-fixture", binary, root, 2, 1, schema)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			calls := 0
			launch := func(...string) error { calls++; return nil }
			gomega.Expect(installDevelopmentServiceWithLauncher(p, launch)).To(gomega.Succeed())
			writePlan(p)
			defaultPath := filepath.Join(home, ".haepari")
			gomega.Expect(os.Symlink(filepath.Join(home, "foreign-default"), defaultPath)).To(gomega.Succeed())
			owned, err := readDevelopmentServicePlan(planFile, false)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(owned).To(gomega.Equal(p))
			gomega.Expect(uninstallDevelopmentServiceWithLauncher(owned, launch)).To(gomega.Succeed())
			gomega.Expect(calls).To(gomega.Equal(2))
			gomega.Expect(os.Remove(defaultPath)).To(gomega.Succeed())
		}
	})

	ginkgo.It("refuses foreign state and unsafe root permissions before native installation", func() {
		root := filepath.Join(home, ".haepari")
		p, err := planReleaseService("task042-fixture", binary, root, 1, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Mkdir(root, 0700)).To(gomega.Succeed())
		foreign := filepath.Join(root, "foreign")
		gomega.Expect(os.WriteFile(foreign, []byte("keep"), 0600)).To(gomega.Succeed())
		calls := 0
		launch := func(...string) error { calls++; return nil }
		gomega.Expect(installDevelopmentServiceWithLauncher(p, launch)).To(gomega.MatchError(managed.ErrConflict))
		gomega.Expect(os.Remove(foreign)).To(gomega.Succeed())
		owner := p
		owner.Schema = devServicePlanSchemaV2
		gomega.Expect(developmentRootOwner(owner, true)).To(gomega.Succeed())
		gomega.Expect(installDevelopmentServiceWithLauncher(p, launch)).To(gomega.MatchError(managed.ErrConflict))
		gomega.Expect(os.Chmod(root, 0755)).To(gomega.Succeed())
		gomega.Expect(errors.Is(installDevelopmentServiceWithLauncher(p, launch), state.ErrInvalidRoot)).To(gomega.BeTrue())
		gomega.Expect(calls).To(gomega.Equal(0))
	})

	ginkgo.It("refuses changed executables and mismatched plan fields before native installation", func() {
		p, err := planReleaseService("task042-fixture", binary, filepath.Join(home, ".haepari"), 1, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		calls := 0
		launch := func(...string) error { calls++; return nil }
		for _, mutate := range []func(*developmentServicePlan){
			func(p *developmentServicePlan) { p.Plist += "\n" },
			func(p *developmentServicePlan) { p.OwnerUID++ },
			func(p *developmentServicePlan) { p.Label = "io.rootkernel.haepari.foreign" },
			func(p *developmentServicePlan) { p.Domain = "gui/0" },
			func(p *developmentServicePlan) { p.PlistPath += ".foreign" },
		} {
			changed := p
			mutate(&changed)
			gomega.Expect(installDevelopmentServiceWithLauncher(changed, launch)).To(gomega.MatchError(managed.ErrConflict))
		}
		gomega.Expect(os.WriteFile(binary, []byte("changed"), 0700)).To(gomega.Succeed())
		gomega.Expect(installDevelopmentServiceWithLauncher(p, launch)).To(gomega.MatchError(managed.ErrConflict))
		gomega.Expect(calls).To(gomega.Equal(0))
		_, err = os.Lstat(p.Root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses symlink roots and executable aliases before planning state", func() {
		actual := filepath.Join(home, "actual-state")
		gomega.Expect(os.Mkdir(actual, 0700)).To(gomega.Succeed())
		root := filepath.Join(home, ".haepari")
		gomega.Expect(os.Symlink(actual, root)).To(gomega.Succeed())
		_, err := planReleaseService("task042-fixture", binary, root, 1, 1)
		gomega.Expect(errors.Is(err, state.ErrInvalidRoot)).To(gomega.BeTrue())
		gomega.Expect(os.Remove(root)).To(gomega.Succeed())
		alias := filepath.Join(home, "candidate-link")
		gomega.Expect(os.Symlink(binary, alias)).To(gomega.Succeed())
		_, err = planReleaseService("task042-fixture", alias, root, 1, 1)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		_, err = os.Lstat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})

	ginkgo.It("preserves exact owned uninstall with an unavailable executable and refuses changed plist or uncertain bootout", func() {
		p, err := planReleaseService("task042-fixture", binary, filepath.Join(home, ".haepari"), 1, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		calls := 0
		launch := func(...string) error { calls++; return nil }
		gomega.Expect(installDevelopmentServiceWithLauncher(p, launch)).To(gomega.Succeed())
		writePlan(p)
		gomega.Expect(os.Remove(binary)).To(gomega.Succeed())
		owned, err := readDevelopmentServicePlan(planFile, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(owned).To(gomega.Equal(p))
		gomega.Expect(os.WriteFile(p.PlistPath, []byte(p.Plist+"\n"), 0600)).To(gomega.Succeed())
		gomega.Expect(uninstallDevelopmentServiceWithLauncher(owned, launch)).To(gomega.MatchError(managed.ErrConflict))
		gomega.Expect(calls).To(gomega.Equal(1))
		gomega.Expect(os.WriteFile(p.PlistPath, []byte(p.Plist), 0600)).To(gomega.Succeed())
		uncertain := errors.New("controlled bootout uncertainty")
		gomega.Expect(uninstallDevelopmentServiceWithLauncher(owned, func(...string) error { return uncertain })).To(gomega.MatchError(uncertain))
		gomega.Expect(os.ReadFile(p.PlistPath)).To(gomega.Equal([]byte(p.Plist)))
		gomega.Expect(uninstallDevelopmentServiceWithLauncher(owned, launch)).To(gomega.Succeed())
		gomega.Expect(calls).To(gomega.Equal(2))
	})

	ginkgo.It("rejects unknown, duplicate, null limit and mismatched v2 plan data on saved reads", func() {
		p, err := planReleaseService("task042-fixture", binary, filepath.Join(home, ".haepari"), 1, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		raw, err := json.Marshal(p)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, mutate := range []func(map[string]any){
			func(d map[string]any) { d["unexpected"] = true },
			func(d map[string]any) { d["schema"] = "haepari.release-service-plan/v3" },
			func(d map[string]any) { d["global_limit"] = nil },
			func(d map[string]any) { delete(d, "host_macos_guest_limit") },
			func(d map[string]any) { d["binary"] = filepath.Join(home, "different") },
			func(d map[string]any) { d["plist"] = p.Plist + "\n" },
		} {
			d := map[string]any{}
			gomega.Expect(json.Unmarshal(raw, &d)).To(gomega.Succeed())
			mutate(d)
			gomega.Expect(os.WriteFile(planFile, mustJSON(d), 0600)).To(gomega.Succeed())
			_, err = readDevelopmentServicePlan(planFile, false)
			gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		}
		duplicate := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"root":"/foreign"}`)...)
		gomega.Expect(os.WriteFile(planFile, duplicate, 0600)).To(gomega.Succeed())
		_, err = readDevelopmentServicePlan(planFile, false)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
	})
})
