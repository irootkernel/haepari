//go:build integration

package managed

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func crashPowerOperation(l *Ledger, operation string) error {
	if operation == "vm-reserve" {
		guard := &VMReservationGuard{HostLimit: 1, ObservationRevisions: map[string]int64{"victim": 1, "target": 1}}
		_, err := l.Reserve(ReserveRequest{JobID: "crash-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "vm-crash-reserve", VMGuard: guard})
		return err
	}
	p, err := l.PowerIntent(1)
	if err != nil {
		return err
	}
	switch operation {
	case "vm-stop":
		return l.ClaimPowerEffect(p.Identity, "stop")
	case "vm-stopped":
		return l.ConfirmPower(PowerConfirmation{Identity: p.Identity, ExpectedStage: "stop_issued", VictimPower: "stopped", TargetPower: "stopped", EvidenceDigest: digest})
	case "vm-start":
		return l.ClaimPowerEffect(p.Identity, "start")
	case "vm-started":
		return l.ConfirmPower(PowerConfirmation{Identity: p.Identity, ExpectedStage: "start_issued", VictimPower: "stopped", TargetPower: "started", EvidenceDigest: digest})
	default:
		panic("unknown VM crash operation")
	}
}

var _ = ginkgo.Describe("Task007PowerCrash", ginkgo.Label("integration"), func() {
	for _, operation := range []string{"vm-reserve", "vm-stop", "vm-stopped", "vm-start", "vm-started"} {
		for _, boundary := range []string{"before_commit", "after_commit"} {
			ginkgo.It(operation+" "+boundary, func() {
				t := ginkgo.GinkgoT()
				path := filepath.Join(t.TempDir(), "state.sqlite")
				db, l := openLedger(t, path, 1)
				vmScheduling(t, l, "victim", 1, "started", true)
				vmScheduling(t, l, "target", 2, "stopped", false)
				job := validJob()
				job.ResourceID = "target"
				job.Deadlines.QueueSeconds = 600
				_, err := l.Admit(encode(t, job), digest, "crash-key", "crash-job")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				before, after := "queued", "preparing"
				if operation != "vm-reserve" {
					gomega.Expect(crashPowerOperation(l, "vm-reserve")).To(gomega.Succeed())
					before, after = "stop_ready", "stop_issued"
				}
				if operation == "vm-stopped" || operation == "vm-start" || operation == "vm-started" {
					gomega.Expect(crashPowerOperation(l, "vm-stop")).To(gomega.Succeed())
					before, after = "stop_issued", "start_ready"
				}
				if operation == "vm-start" || operation == "vm-started" {
					gomega.Expect(crashPowerOperation(l, "vm-stopped")).To(gomega.Succeed())
					before, after = "start_ready", "start_issued"
				}
				if operation == "vm-started" {
					gomega.Expect(crashPowerOperation(l, "vm-start")).To(gomega.Succeed())
					before, after = "start_issued", "done"
				}
				gomega.Expect(db.Close()).To(gomega.Succeed())
				executable, err := os.Executable()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				child := exec.Command(executable)
				child.Env = append(os.Environ(), "HAEPARI_STORAGE_PROBE="+path, "HAEPARI_STORAGE_BOUNDARY="+boundary, "HAEPARI_STORAGE_OPERATION="+operation)
				output, err := os.Create(path + ".output")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				defer output.Close()
				child.Stdout = output
				child.Stderr = output
				gomega.Expect(child.Start()).To(gomega.Succeed())
				waited := false
				t.Cleanup(func() {
					if !waited {
						_ = child.Process.Kill()
						_ = child.Wait()
					}
				})
				ready := false
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(path + ".barrier"); err == nil {
						ready = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !ready {
					log, _ := os.ReadFile(path + ".output")
					t.Fatalf("missing commit barrier: %s", log)
				}
				gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
				gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
				waited = true
				db, l = openLedger(t, path, 1)
				defer db.Close()
				expected := before
				if boundary == "after_commit" {
					expected = after
				}
				r, err := l.Snapshot("crash-job")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				occupancy := 1
				if operation == "vm-reserve" && boundary == "before_commit" {
					occupancy = 0
				}
				gomega.Expect(r.GlobalOccupancy).To(gomega.Equal(occupancy))
				if operation == "vm-reserve" {
					gomega.Expect(r.JobState).To(gomega.Equal(expected))
					var intents, nonces int
					gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_power_transitions`).Scan(&intents)).To(gomega.Succeed())
					gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_parent_nonces`).Scan(&nonces)).To(gomega.Succeed())
					count := 1
					if boundary == "before_commit" {
						count = 0
						gomega.Expect(r.GlobalOccupancy).To(gomega.BeZero())
					}
					gomega.Expect(intents).To(gomega.Equal(count))
					gomega.Expect(nonces).To(gomega.Equal(count))
				} else {
					p, err := l.PowerIntent(1)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(p.Stage).To(gomega.Equal(expected))
					// An issued command retains protection on reopen. This check never
					// substitutes a subprocess exit for an observed VM state.
					if expected == "stop_issued" {
						gomega.Expect(l.ClaimPowerEffect(p.Identity, "stop")).To(gomega.HaveOccurred())
						gomega.Expect(l.ClaimPowerEffect(p.Identity, "start")).To(gomega.HaveOccurred())
					}
					if expected == "start_issued" {
						gomega.Expect(l.ClaimPowerEffect(p.Identity, "start")).To(gomega.HaveOccurred())
					}
				}
			})
		}
	}
})
