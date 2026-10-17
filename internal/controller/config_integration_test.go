//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func writePrivateConfig(path string, doc config.Document) {
	ginkgo.GinkgoT().Helper()
	raw, err := json.Marshal(doc)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(os.WriteFile(path, raw, 0600)).To(gomega.Succeed())
}

var _ = ginkgo.Describe("Task023ConfigStartup", ginkgo.Label("integration"), func() {
	ginkgo.It("binds the accepted host limit", func() {
		server := &runtimeServer{}
		doc := config.Defaults()
		doc.HostMACOSGuestLimit = 2
		bindAcceptedLimits(server, doc)
		gomega.Expect(server.vmHostLimit).To(gomega.Equal(2))
	})

	ginkgo.It("validates an explicit file before creating a missing root", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-config-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoT().Cleanup(func() { _ = os.RemoveAll(parent) })
		file := filepath.Join(parent, "bad.json")
		gomega.Expect(os.WriteFile(file, []byte(`{"schema":"nope"}`), 0600)).To(gomega.Succeed())
		root, err := state.Resolve(filepath.Join(parent, "absent", "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = RunWithOptions(context.Background(), root, Options{
			HostMACOSGuestLimit: 1, GlobalLimit: 1,
			ConfigPath: file, ConfigExplicit: true,
		})
		gomega.Expect(errors.Is(err, config.ErrRefused)).To(gomega.BeTrue())
		_, statErr := os.Lstat(filepath.Join(parent, "absent"))
		gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue())

		external := filepath.Join(parent, "private.json")
		doc := config.Defaults()
		doc.GlobalLimit = 2
		doc.HostMACOSGuestLimit = 2
		writePrivateConfig(external, doc)
		err = RunWithOptions(context.Background(), root, Options{
			HostMACOSGuestLimit: 1, GlobalLimit: 1,
			ConfigPath: external, ConfigExplicit: true,
		})
		gomega.Expect(errors.Is(err, state.ErrInvalidRoot)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).NotTo(gomega.ContainSubstring("differs"))
		_, statErr = os.Lstat(filepath.Join(parent, "absent"))
		gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue())
	})

	ginkgo.It("commits the external file before refusing a foreign socket and keeps a rejected digest", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-config-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoT().Cleanup(func() { _ = os.RemoveAll(parent) })
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(root.SocketPath, []byte("foreign"), 0600)).To(gomega.Succeed())
		external := filepath.Join(parent, "private.json")
		doc := config.Defaults()
		doc.GlobalLimit = 2
		doc.HostMACOSGuestLimit = 2
		writePrivateConfig(external, doc)
		err = RunWithOptions(context.Background(), root, Options{
			HostMACOSGuestLimit: 1, GlobalLimit: 1,
			ConfigPath: external, ConfigExplicit: true,
		})
		gomega.Expect(errors.Is(err, ErrSocketExists)).To(gomega.BeTrue())
		data, err := os.ReadFile(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("foreign"))

		db, err := sqlite.Open(root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer func() { _ = db.Close() }()
		var raw []byte
		var revision int64
		var jobs, audits int
		gomega.Expect(db.QueryRow(`SELECT revision,document_json FROM managed_effective_config WHERE singleton=1`).Scan(&revision, &raw)).To(gomega.Succeed())
		var stored config.Document
		gomega.Expect(json.Unmarshal(raw, &stored)).To(gomega.Succeed())
		gomega.Expect(revision).To(gomega.Equal(int64(1)))
		gomega.Expect(stored.GlobalLimit).To(gomega.Equal(int64(2)))
		gomega.Expect(stored.HostMACOSGuestLimit).To(gomega.Equal(int64(2)))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&jobs)).To(gomega.Succeed())
		gomega.Expect(jobs).To(gomega.Equal(0))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&audits)).To(gomega.Succeed())
		gomega.Expect(audits).To(gomega.Equal(1))
		gomega.Expect(db.Close()).To(gomega.Succeed())

		doc.GlobalLimit = 1
		writePrivateConfig(external, doc)
		err = RunWithOptions(context.Background(), root, Options{
			HostMACOSGuestLimit: 1, GlobalLimit: 1,
			ConfigPath: external, ConfigExplicit: true,
		})
		gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("changed the accepted digest"))
		data, err = os.ReadFile(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("foreign"))
		db, err = sqlite.Open(root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.QueryRow(`SELECT revision,document_json FROM managed_effective_config WHERE singleton=1`).Scan(&revision, &raw)).To(gomega.Succeed())
		gomega.Expect(json.Unmarshal(raw, &stored)).To(gomega.Succeed())
		gomega.Expect(stored.GlobalLimit).To(gomega.Equal(int64(2)))
		gomega.Expect(stored.HostMACOSGuestLimit).To(gomega.Equal(int64(2)))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&jobs)).To(gomega.Succeed())
		gomega.Expect(jobs).To(gomega.Equal(0))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&audits)).To(gomega.Succeed())
		gomega.Expect(audits).To(gomega.Equal(1))
	})
})
