//go:build integration

package controller

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

type attestationExchange struct {
	Operation string
	Schema    string
	Deadline  time.Time
	At        time.Time
}

// Preparation effects use short-lived native guest owners. An in-process
// preparation owner would still be alive and must block attestation.
func attestationObservationFixture() (*vmJobFixture, managed.Result, []byte) {
	f := attestedVMFixture()
	f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
	original := f.run("attestation-observation")
	exit := falseVMExit(f, original.JobID)
	f.s.vmWrite = nil
	return f, original, exit
}

// Reuse verified executable bytes in native controller fixtures. The exact
// incoming archive digest and pinned guest/recovery image select a real native
// process for every command, including effects. Its production handler retains
// identity and deadline checks. The original bootstrap has separate coverage.
func installNativeGuestCommands(f *vmJobFixture) {
	f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (result utm.CommandResult, callErr error) {
		q := vmTransportRequest(input)
		started := time.Now()
		defer func() {
			if recorder := recorderFor(f); recorder != nil {
				recorder.note(input, []byte(result.Stdout), result.ExitCode, len(result.Stderr), callErr, time.Since(started))
			}
		}()
		if err := ctx.Err(); err != nil {
			return utm.CommandResult{ExitCode: -1}, err
		}
		archive := tar.NewReader(bytes.NewReader(input))
		entries := map[string][]byte{}
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return utm.CommandResult{}, err
			}
			if header.Name != "haepari" && header.Name != "request.json" && header.Name != "digest" {
				return utm.CommandResult{}, errors.New("unexpected fixture archive entry")
			}
			if _, exists := entries[header.Name]; exists || header.Typeflag != tar.TypeReg {
				return utm.CommandResult{}, errors.New("invalid fixture archive entry")
			}
			entries[header.Name], err = io.ReadAll(archive)
			if err != nil {
				return utm.CommandResult{}, err
			}
		}
		imageDigest := sha256.Sum256(entries["haepari"])
		if len(entries) != 3 || string(entries["digest"]) != hex.EncodeToString(imageDigest[:]) {
			return utm.CommandResult{}, errors.New("fixture archive digest mismatch")
		}
		resolveExecutable := os.Executable
		if f.s.vmExecutable != nil {
			resolveExecutable = f.s.vmExecutable
		}
		current, err := resolveExecutable()
		if err != nil {
			return utm.CommandResult{}, err
		}
		candidates := []string{current}
		if managed.ValidRequestKey(q.Request.Identity.JobID) {
			candidates = append(candidates, filepath.Join(f.s.root.Path, "jobs", q.Request.Identity.JobID, "guest-runner"))
		}
		executable := ""
		for _, candidate := range candidates {
			data, err := os.ReadFile(candidate)
			if err == nil && sha256.Sum256(data) == imageDigest {
				executable = candidate
				break
			}
		}
		if executable == "" {
			return utm.CommandResult{}, errors.New("fixture selected image unavailable")
		}
		var shape struct {
			Schema string `json:"schema"`
		}
		raw := entries["request.json"]
		if err := json.Unmarshal(raw, &shape); err != nil {
			return utm.CommandResult{}, err
		}
		verb := "guest"
		if strings.HasPrefix(shape.Schema, "haepari.guest-recovery-request/") {
			verb = "guest-recovery"
		}
		if shape.Schema == "haepari.guest-transport-request/v1" {
			verb = "guest-transport"
		}
		cmd := exec.CommandContext(ctx, executable, "runner", verb, "--stdio")
		cmd.Stdin = bytes.NewReader(raw)
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "GORACE=atexit_sleep_ms=0"}
		cmd.WaitDelay = time.Second
		var out, diag bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diag
		err = cmd.Run()
		if ctx.Err() != nil {
			return utm.CommandResult{ExitCode: -1}, ctx.Err()
		}
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			return utm.CommandResult{ExitCode: -1}, err
		}
		return utm.CommandResult{Stdout: out.String(), Stderr: diag.String(), ExitCode: cmd.ProcessState.ExitCode()}, nil
	}
}

func expectAttestationObservationPreserved(f *vmJobFixture, original managed.Result, exit []byte) {
	ginkgo.GinkgoHelper()
	snap, err := f.s.ledger.ResourceRecoverySnapshot(f.spec.ID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(snap.EvidenceIntent).To(gomega.BeNil())
	gomega.Expect(snap.Resource.Availability).To(gomega.Equal("quarantined"))
	gomega.Expect(snap.Resource.Occupancy).To(gomega.BeZero())
	current, err := f.s.ledger.Result(original.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(current).To(gomega.Equal(original))
	gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "exit.json"))).To(gomega.Equal(exit))
	gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
	expectNoPower(f)
}

func expectAttestationClock(rows []attestationExchange, started time.Time) {
	ginkgo.GinkgoHelper()
	gomega.Expect(rows).NotTo(gomega.BeEmpty())
	for _, row := range rows {
		gomega.Expect(row.Deadline).To(gomega.Equal(rows[0].Deadline))
		gomega.Expect(row.Deadline).To(gomega.BeTemporally("~", started.Add(recoveryObservationBudget), time.Second))
		gomega.Expect(row.At.Before(row.Deadline)).To(gomega.BeTrue())
	}
}

var _ = ginkgo.Describe("GuestAttestationObservationBudget", ginkgo.Label("integration"), func() {
	ginkgo.It("accepts a six second probe delay on one identity and recovery clock", func() {
		f, original, exit := attestationObservationFixture()
		native := f.s.sshCommand
		var rows []attestationExchange
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			rows = append(rows, attestationExchange{q.Operation, q.Schema, q.Deadline, time.Now()})
			if q.Operation == "probe" {
				timer := time.NewTimer(6 * time.Second)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return utm.CommandResult{}, ctx.Err()
				case <-timer.C:
				}
			}
			return native(ctx, exe, args, input)
		}
		started := time.Now()
		plan, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Unknowns).To(gomega.BeEmpty())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		gomega.Expect(rows).To(gomega.HaveLen(2))
		gomega.Expect(rows[0].Operation).To(gomega.Equal("identity"))
		gomega.Expect(rows[1].Operation).To(gomega.Equal("probe"))
		expectAttestationClock(rows, started)
		gomega.Expect(time.Since(started)).To(gomega.BeNumerically(">", 5*time.Second))
		gomega.Expect(time.Now().Before(rows[0].Deadline)).To(gomega.BeTrue())
		expectAttestationObservationPreserved(f, original, exit)
	})

	ginkgo.It("keeps the same deadline when a refused v1 probe falls back to v2 identity", func() {
		f, original, exit := attestationObservationFixture()
		native := f.s.sshCommand
		var rows []attestationExchange
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			rows = append(rows, attestationExchange{q.Operation, q.Schema, q.Deadline, time.Now()})
			if q.Operation == "probe" && q.Schema == "haepari.guest-recovery-request/v1" {
				return utm.CommandResult{}, errors.New("fixture refused v1 probe")
			}
			return native(ctx, exe, args, input)
		}
		started := time.Now()
		plan, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// The current boot cannot qualify a cross-boot v2 fallback.
		gomega.Expect(plan.Unknowns).NotTo(gomega.BeEmpty())
		gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(rows).To(gomega.HaveLen(3))
		gomega.Expect(rows[2].Operation).To(gomega.Equal("identity"))
		gomega.Expect(rows[2].Schema).To(gomega.Equal("haepari.guest-request/v2"))
		expectAttestationClock(rows, started)
		expectAttestationObservationPreserved(f, original, exit)
	})

	ginkgo.It("expires one observation without opening a fallback exchange or creating recovery authority", func() {
		f, original, exit := attestationObservationFixture()
		var rows []attestationExchange
		f.s.sshCommand = func(ctx context.Context, _ string, _ []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			rows = append(rows, attestationExchange{q.Operation, q.Schema, q.Deadline, time.Now()})
			<-ctx.Done()
			return utm.CommandResult{}, ctx.Err()
		}
		started := time.Now()
		plan, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Unknowns).NotTo(gomega.BeEmpty())
		gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Operation).To(gomega.Equal("identity"))
		expectAttestationClock(rows, started)
		gomega.Expect(time.Since(started)).To(gomega.BeNumerically(">=", recoveryObservationBudget))
		gomega.Expect(time.Since(started)).To(gomega.BeNumerically("<", recoveryObservationBudget+3*time.Second))
		// Failed v2 fallback may append diagnostics. That is not recovery authority.
		expectAttestationObservationPreserved(f, original, exit)
	})
})
