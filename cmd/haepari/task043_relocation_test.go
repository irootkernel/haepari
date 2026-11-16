package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/relocation"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func relocationCLIRoundTrip(large bool) {
	t := ginkgo.GinkgoT()
	home, err := filepath.EvalSymlinks(t.TempDir())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	t.Setenv("HOME", home)
	binary := filepath.Join(home, "candidate")
	gomega.Expect(os.WriteFile(binary, []byte("fixture executable"), 0700)).To(gomega.Succeed())
	source := filepath.Join(home, "s")
	target := filepath.Join(home, "t")
	a, err := planManagedService("relocation-cli", binary, source, 1, 1, releaseServicePlanSchemaV1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	b, err := planReleaseService("relocation-cli", binary, target, 1, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	root, err := state.Resolve(source)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(root.Prepare()).To(gomega.Succeed())
	gomega.Expect(developmentRootOwner(a, true)).To(gomega.Succeed())
	lock, err := root.AcquireController()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(lock.Close()).To(gomega.Succeed())
	db, err := sqlite.Open(root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ledger, err := managed.NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = ledger.BindInstallation("local-cli-fixture")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = ledger.CommitEffectiveConfig(config.Defaults(), false)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(db.Close()).To(gomega.Succeed())
	_, err = snapshot.OpenStore(source)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	if large {
		nested := filepath.Join(source, strings.Repeat("a", 220), strings.Repeat("b", 220), strings.Repeat("c", 220))
		gomega.Expect(os.MkdirAll(nested, 0700)).To(gomega.Succeed())
		for i := 0; i < 1100; i++ {
			gomega.Expect(os.WriteFile(filepath.Join(nested, fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 100))), []byte("retained"), 0600)).To(gomega.Succeed())
		}
	}

	sourceFile, targetFile, planFile := filepath.Join(home, "a.json"), filepath.Join(home, "b.json"), filepath.Join(home, "move.json")
	raw, _ := json.Marshal(a)
	gomega.Expect(os.WriteFile(sourceFile, raw, 0600)).To(gomega.Succeed())
	raw, _ = json.Marshal(b)
	gomega.Expect(os.WriteFile(targetFile, raw, 0600)).To(gomega.Succeed())
	env := relocation.Environment{MoverBinary: binary, ValidateService: func(p relocation.ServicePlan) error {
		now, err := recreateDevelopmentServicePlan(p)
		if err != nil {
			return err
		}
		if now != p {
			return managed.ErrConflict
		}
		return nil
	}, ServicesStopped: func(relocation.ServicePlan) error { return nil }}
	provider := func() (relocation.Environment, error) { return env, nil }
	var out, diagnostic bytes.Buffer
	gomega.Expect(relocateCommandUsing([]string{"plan", "--source-plan", sourceFile, "--target-plan", targetFile, "--backup-dir", filepath.Join(home, "backup"), "--json"}, &out, &diagnostic, provider)).To(gomega.Equal(0), out.String()+diagnostic.String())
	var response struct {
		Operation string          `json:"operation"`
		Data      relocation.Plan `json:"data"`
	}
	gomega.Expect(json.Unmarshal(out.Bytes(), &response)).To(gomega.Succeed())
	gomega.Expect(response.Operation).To(gomega.Equal("service.relocate.plan"))
	raw, _ = json.Marshal(response.Data)
	if large {
		gomega.Expect(len(raw)).To(gomega.BeNumerically(">", 1<<20))
		gomega.Expect(len(response.Data.Files)).To(gomega.BeNumerically(">", 1100))
		ginkgo.AddReportEntry("generated complete plan", map[string]any{"bytes": len(raw), "entries": len(response.Data.Files), "sha256": relocation.Digest(raw)}, ginkgo.ReportEntryVisibilityAlways)
	}

	gomega.Expect(os.WriteFile(planFile, raw, 0600)).To(gomega.Succeed())
	if large {
		_, err = readSpec(planFile)
		gomega.Expect(err).To(gomega.HaveOccurred())
	}
	for _, action := range []string{"status", "apply", "status", "rollback", "status"} {
		out.Reset()
		gomega.Expect(relocateCommandUsing([]string{action, "--plan-file", planFile, "--json"}, &out, &diagnostic, provider)).To(gomega.Equal(0), out.String())
		var receipt struct {
			Operation string             `json:"operation"`
			Data      relocation.Receipt `json:"data"`
		}
		gomega.Expect(json.Unmarshal(out.Bytes(), &receipt)).To(gomega.Succeed())
		gomega.Expect(receipt.Operation).To(gomega.Equal("service.relocate." + action))
		gomega.Expect(receipt.Data.Schema).To(gomega.Equal(relocation.ReceiptSchema))
	}
	stored, err := os.ReadFile(filepath.Join(source, relocation.JournalName))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var saved relocation.Journal
	gomega.Expect(relocation.Decode(stored, &saved)).To(gomega.Succeed())
	gomega.Expect(saved.Plan).To(gomega.Equal(response.Data))
	out.Reset()
	gomega.Expect(relocateCommandUsing([]string{"apply", "--plan-file", planFile, "--json"}, &out, &diagnostic, provider)).To(gomega.Equal(2))
	var failure managed.Envelope
	gomega.Expect(json.Unmarshal(out.Bytes(), &failure)).To(gomega.Succeed())
	gomega.Expect(failure.Error.Code).To(gomega.Equal(managed.Conflict))
	gomega.Expect(failure.Error.Retry).To(gomega.Equal("none"))
	gomega.Expect(out.String()).NotTo(gomega.ContainSubstring(home))
}

var _ = ginkgo.Describe("Task043RelocationCLI", ginkgo.Label("unit"), func() {
	ginkgo.It("routes closed plan/apply/status/rollback envelopes", func() { relocationCLIRoundTrip(false) })
	ginkgo.It("Task043LargePlan round-trips a complete generated plan above the managed Job cap", func() { relocationCLIRoundTrip(true) })

	ginkgo.It("rejects unknown/irrelevant flags and missing JSON before native observations", func() {
		called := false
		provider := func() (relocation.Environment, error) { called = true; return relocation.Environment{}, nil }
		for _, args := range [][]string{{}, {"plan"}, {"wat"}, {"status", "--plan-file", "x"}, {"status", "--plan-file", "x", "--source-plan", "y", "--json"}, {"plan", "--source-plan", "a", "--target-plan", "b", "--backup-dir", "c", "--plan-file", "extra", "--json"}, {"apply", "--plan-file", "x", "--force", "--json"}} {
			var out, stderr bytes.Buffer
			gomega.Expect(relocateCommandUsing(args, &out, &stderr, provider)).To(gomega.Equal(2))
		}
		gomega.Expect(called).To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("Task043PlanFileGuards", ginkgo.Label("unit"), func() {
	ginkgo.It("retains malformed and nonregular plan refusal and the managed Job file cap", func() {
		dir := ginkgo.GinkgoT().TempDir()
		malformed := filepath.Join(dir, "malformed.json")
		gomega.Expect(os.WriteFile(malformed, []byte("{"), 0600)).To(gomega.Succeed())
		provider := func() (relocation.Environment, error) { return relocation.Environment{}, nil }
		for _, name := range []string{malformed, dir} {
			var out, diagnostic bytes.Buffer
			gomega.Expect(relocateCommandUsing([]string{"status", "--plan-file", name, "--json"}, &out, &diagnostic, provider)).NotTo(gomega.BeZero())
			var envelope managed.Envelope
			gomega.Expect(json.Unmarshal(out.Bytes(), &envelope)).To(gomega.Succeed())
			gomega.Expect(envelope.OK).To(gomega.BeFalse())
		}
		oversized := filepath.Join(dir, "job.json")
		gomega.Expect(os.WriteFile(oversized, bytes.Repeat([]byte("x"), (1<<20)+1), 0600)).To(gomega.Succeed())
		_, err := readSpec(oversized)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
