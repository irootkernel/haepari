package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func writeTask023File(path string, data []byte) {
	ginkgo.GinkgoT().Helper()
	gomega.Expect(os.WriteFile(path, data, 0600)).To(gomega.Succeed())
}

var _ = ginkgo.Describe("Task023DaemonConfig", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses a missing or conflicting config before creating the root", func() {
		base, err := os.MkdirTemp("/tmp", "hp-cli-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoT().Cleanup(func() { _ = os.RemoveAll(base) })
		home := filepath.Join(base, "missing", "state")
		ginkgo.GinkgoT().Setenv("HAEPARI_HOME", home)

		var out, errBuf bytes.Buffer
		gomega.Expect(run([]string{"daemon", "run", "--config", filepath.Join(base, "absent.json")}, &out, &errBuf)).To(gomega.Equal(2))
		gomega.Expect(errBuf.String()).To(gomega.ContainSubstring("missing"))
		_, statErr := os.Lstat(filepath.Join(base, "missing"))
		gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue())

		doc := config.Defaults()
		doc.GlobalLimit = 2
		raw, err := json.Marshal(doc)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		file := filepath.Join(base, "private.json")
		writeTask023File(file, raw)
		errBuf.Reset()
		gomega.Expect(run([]string{"daemon", "run", "--config", file, "--global-limit", "1"}, &out, &errBuf)).To(gomega.Equal(2))
		gomega.Expect(errBuf.String()).To(gomega.ContainSubstring("differs"))
		_, statErr = os.Lstat(filepath.Join(base, "missing"))
		gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue())

		errBuf.Reset()
		gomega.Expect(run([]string{"daemon", "run", "--config", file}, &out, &errBuf)).To(gomega.Equal(2))
		gomega.Expect(errBuf.String()).To(gomega.ContainSubstring("parent directory must exist"))
		gomega.Expect(errBuf.String()).NotTo(gomega.ContainSubstring("differs"))
		_, statErr = os.Lstat(filepath.Join(base, "missing"))
		gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue())
	})

	ginkgo.It("keeps a relative config file from selecting the root", func() {
		base, err := os.MkdirTemp("/tmp", "hp-cli-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoT().Cleanup(func() { _ = os.RemoveAll(base) })
		cwd, err := os.Getwd()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Chdir(base)).To(gomega.Succeed())
		ginkgo.GinkgoT().Cleanup(func() { _ = os.Chdir(cwd) })
		doc := config.Defaults()
		doc.GlobalLimit = 2
		doc.HostMACOSGuestLimit = 2
		raw, err := json.Marshal(doc)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		writeTask023File(filepath.Join(base, "private.json"), raw)
		home := filepath.Join(base, "missing", "state")
		ginkgo.GinkgoT().Setenv("HAEPARI_HOME", home)

		var out, errBuf bytes.Buffer
		gomega.Expect(run([]string{"daemon", "run", "--root", "relative-root"}, &out, &errBuf)).To(gomega.Equal(2))
		gomega.Expect(errBuf.String()).To(gomega.ContainSubstring("absolute"))
		_, statErr := os.Lstat(filepath.Join(base, "relative-root"))
		gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue())

		errBuf.Reset()
		gomega.Expect(run([]string{"daemon", "run", "--config", "private.json"}, &out, &errBuf)).To(gomega.Equal(2))
		gomega.Expect(errBuf.String()).To(gomega.ContainSubstring("parent directory must exist"))
		gomega.Expect(errBuf.String()).NotTo(gomega.ContainSubstring("differs"))
		_, statErr = os.Lstat(filepath.Join(base, "missing"))
		gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue())
		_, statErr = os.Lstat(filepath.Join(base, "state"))
		gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue())
	})

	ginkgo.It("publishes the config flag in daemon usage", func() {
		var out, errBuf bytes.Buffer
		gomega.Expect(run([]string{"daemon"}, &out, &errBuf)).To(gomega.Equal(2))
		gomega.Expect(errBuf.String()).To(gomega.ContainSubstring("--config"))
		gomega.Expect(errBuf.String()).To(gomega.ContainSubstring("--root"))
		out.Reset()
		gomega.Expect(run([]string{"help", "--json"}, &out, &errBuf)).To(gomega.Equal(0))
		var help struct {
			Data struct {
				Commands []struct {
					Name  string `json:"name"`
					Usage string `json:"usage"`
				} `json:"commands"`
			} `json:"data"`
		}
		gomega.Expect(json.Unmarshal(out.Bytes(), &help)).To(gomega.Succeed())
		var usage string
		for _, command := range help.Data.Commands {
			if command.Name == "daemon run" {
				usage = command.Usage
			}
		}
		gomega.Expect(usage).To(gomega.ContainSubstring("--config <private-file>"))
		gomega.Expect(usage).To(gomega.ContainSubstring("--root <absolute-private-root>"))
		gomega.Expect(usage).NotTo(gomega.ContainSubstring("absolute-private-file"))
	})
})
