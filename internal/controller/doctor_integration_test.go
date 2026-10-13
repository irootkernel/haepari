//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task017ResourceDoctor", ginkgo.Label("integration"), func() {
	ginkgo.It("observes local readiness and missing resources without mutation", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		before, err := s.ledger.ResourceStatus("local-a")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		mutations, events := availabilityCounts(db)
		report, _, err := c.DoctorResource(context.Background(), "local-a")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Ready).To(gomega.BeTrue())
		gomega.Expect(report.InstallationID).To(gomega.Equal(c.Hello.InstallationID))
		report, _, err = c.DoctorResource(context.Background(), "missing")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Ready).To(gomega.BeFalse())
		gomega.Expect(report.Gaps[0].Code).To(gomega.Equal("resource_not_found"))
		after, err := s.ledger.ResourceStatus("local-a")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		m, e := availabilityCounts(db)
		gomega.Expect([]int{m, e}).To(gomega.Equal([]int{mutations, events}))
	})

	ginkgo.It("observes a ready running VM and bounds stalled probes without mutation", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "unused")
		c, err := client.Connect(context.Background(), f.s.root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer c.Close()
		before, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		jobBefore, err := f.s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		guestBefore, err := os.ReadDir(f.guest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.s.doctorHostSession = func(context.Context) error { return nil }
		probes := 0
		readyProbe := func(ctx context.Context, _ string, args []string, input []byte) (utm.CommandResult, error) {
			probes++
			deadline, bounded := ctx.Deadline()
			if !bounded || time.Until(deadline) > 15*time.Second || !strings.Contains(args[len(args)-1], "/sbin/ifconfig -a") {
				return utm.CommandResult{}, errors.New("unexpected unbounded or staging probe")
			}
			fields := strings.Split(string(input), "\n")
			if len(fields) != 3 || len(fields[0]) != 32 || fields[1] != strings.ToLower(f.spec.VM.MACAddress) {
				return utm.CommandResult{}, errors.New("unexpected challenge or MAC")
			}
			return utm.CommandResult{Stdout: f.spec.VM.UTMVMUUID + "\nDarwin\narm64\n" + fields[0] + "\n" + fields[1] + "\n"}, nil
		}
		f.s.sshCommand = readyProbe
		report, _, err := c.DoctorResource(context.Background(), f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Ready).To(gomega.BeTrue())
		gomega.Expect(report.Gaps).To(gomega.BeEmpty())
		gomega.Expect(report.Resource).NotTo(gomega.BeNil())
		gomega.Expect(*report.Resource).To(gomega.Equal(before))
		gomega.Expect(probes).To(gomega.Equal(1))
		stall := func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
				return errors.New("caller deadline did not reach probe")
			}
		}
		for _, stage := range []string{"aqua_session_unavailable", "guest_identity_unavailable"} {
			f.s.doctorHostSession = func(context.Context) error { return nil }
			f.s.sshCommand = readyProbe
			if stage == "aqua_session_unavailable" {
				f.s.doctorHostSession = stall
			} else {
				f.s.sshCommand = func(ctx context.Context, _ string, _ []string, _ []byte) (utm.CommandResult, error) {
					return utm.CommandResult{}, stall(ctx)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			request := httptest.NewRequest("GET", "/doctor/"+f.spec.ID, nil).WithContext(ctx)
			request.SetPathValue("id", f.spec.ID)
			response := httptest.NewRecorder()
			started := time.Now()
			f.s.resourceDoctor(response, request)
			cancel()
			gomega.Expect(time.Since(started)).To(gomega.BeNumerically("<", 500*time.Millisecond))
			gomega.Expect(response.Code).To(gomega.Equal(200))
			var envelope client.Envelope
			gomega.Expect(json.Unmarshal(response.Body.Bytes(), &envelope)).To(gomega.Succeed())
			gomega.Expect(envelope.OK).To(gomega.BeTrue())
			gomega.Expect(json.Unmarshal(envelope.Data, &report)).To(gomega.Succeed())
			gomega.Expect(report.Ready).To(gomega.BeFalse())
			gomega.Expect(report.Gaps).To(gomega.HaveLen(1))
			gomega.Expect(report.Gaps[0].Code).To(gomega.Equal(stage))
			gomega.Expect(report.Gaps[0].Message).To(gomega.ContainSubstring("No VM or environment repair was performed."))
		}
		after, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		jobAfter, err := f.s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(jobAfter).To(gomega.Equal(jobBefore))
		guestAfter, err := os.ReadDir(f.guest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(guestAfter).To(gomega.Equal(guestBefore))
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses unavailable Aqua and private mapping without guest or power effects", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		spec := vmControllerSpec(ginkgo.GinkgoT(), "preflight-vm", 1, false)
		gomega.Expect(s.ledger.RegisterResource(mustJSONScheduling(spec))).To(gomega.Succeed())
		control := &fakeUTMControl{states: map[string]string{spec.VM.UTMVMUUID: "stopped"}}
		s.utmCommand = control.command
		s.vmLeases = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }
		s.sshCommand = func(context.Context, string, []string, []byte) (utm.CommandResult, error) {
			ginkgo.Fail("stopped/unavailable VM must not receive an SSH command")
			return utm.CommandResult{}, nil
		}
		s.doctorHostSession = func(context.Context) error { return errors.New("Aqua unavailable") }
		mutations, events := availabilityCounts(db)
		report, _, err := c.DoctorResource(context.Background(), spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Ready).To(gomega.BeFalse())
		gomega.Expect(report.Gaps[0].Code).To(gomega.Equal("aqua_session_unavailable"))
		s.doctorHostSession = func(context.Context) error { return nil }
		// Invalid private mapping is also a prerequisite failure, never repair.
		report, _, err = c.DoctorResource(context.Background(), spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Ready).To(gomega.BeFalse())
		gomega.Expect(report.Gaps[0].Code).To(gomega.Equal("ssh_mapping_unavailable"))
		gomega.Expect(strings.Join(control.effects, " ")).To(gomega.BeEmpty())
		m, e := availabilityCounts(db)
		gomega.Expect([]int{m, e}).To(gomega.Equal([]int{mutations, events}))
	})

	ginkgo.It("distinguishes a failed post-probe ledger read from a changed configuration", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		spec := vmControllerSpec(ginkgo.GinkgoT(), "read-failure-vm", 1, false)
		gomega.Expect(s.ledger.RegisterResource(mustJSONScheduling(spec))).To(gomega.Succeed())
		control := &fakeUTMControl{states: map[string]string{spec.VM.UTMVMUUID: "stopped"}}
		s.utmCommand = control.command
		s.vmLeases = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }
		s.doctorHostSession = func(context.Context) error { return db.Close() }
		s.sshCommand = func(context.Context, string, []string, []byte) (utm.CommandResult, error) {
			ginkgo.Fail("invalid private mapping must not contact a guest")
			return utm.CommandResult{}, nil
		}
		report, _, err := c.DoctorResource(context.Background(), spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Ready).To(gomega.BeFalse())
		gomega.Expect(report.Gaps).To(gomega.ContainElement(gomega.HaveField("Code", "resource_observation_unavailable")))
		gomega.Expect(report.Gaps).NotTo(gomega.ContainElement(gomega.HaveField("Code", "configuration_changed")))
		gomega.Expect(report.Resource.ConfigRevision).To(gomega.Equal(int64(1)))
		gomega.Expect(control.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses unsafe local storage and advisory resources without mutation", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		gomega.Expect(s.ledger.RegisterResource([]byte(`{"schema":"haepari.resource/v1","id":"doctor-lock","kind":"lock"}`))).To(gomega.Succeed())
		mutations, events := availabilityCounts(db)
		root := filepath.Join(s.root.Path, "jobs")
		gomega.Expect(os.Chmod(root, 0777)).To(gomega.Succeed())
		defer os.Chmod(root, 0700)
		report, _, err := c.DoctorResource(context.Background(), "local-a")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Ready).To(gomega.BeFalse())
		gomega.Expect(report.Gaps[0].Code).To(gomega.Equal("local_root_unready"))
		report, _, err = c.DoctorResource(context.Background(), "doctor-lock")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Ready).To(gomega.BeFalse())
		gomega.Expect(report.Gaps[0].Code).To(gomega.Equal("unsupported_resource"))
		m, e := availabilityCounts(db)
		gomega.Expect([]int{m, e}).To(gomega.Equal([]int{mutations, events}))
	})

	ginkgo.It("detects configuration changes across the real prerequisite observation", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "unused")
		c, err := client.Connect(context.Background(), f.s.root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer c.Close()
		f.s.doctorHostSession = func(context.Context) error {
			status, err := f.s.ledger.ResourceStatus(f.spec.ID)
			if err != nil {
				return err
			}
			next := f.spec
			next.Revision++
			return f.s.ledger.UpdateResource(mustJSONScheduling(next), status.ConfigRevision, status.AvailabilityRevision)
		}
		f.s.sshCommand = func(_ context.Context, _ string, _ []string, input []byte) (utm.CommandResult, error) {
			fields := strings.Split(string(input), "\n")
			return utm.CommandResult{Stdout: f.spec.VM.UTMVMUUID + "\nDarwin\narm64\n" + fields[0] + "\n" + fields[1] + "\n"}, nil
		}
		report, _, err := c.DoctorResource(context.Background(), f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Ready).To(gomega.BeFalse())
		gomega.Expect(report.Gaps).To(gomega.ContainElement(gomega.HaveField("Code", "configuration_changed")))
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("distinguishes running, lease, pin and guest identity gaps without capacity changes", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "unused")
		c, err := client.Connect(context.Background(), f.s.root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer c.Close()
		before, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.s.doctorHostSession = func(context.Context) error { return nil }
		f.s.sshCommand = func(context.Context, string, []string, []byte) (utm.CommandResult, error) {
			ginkgo.Fail("failed host prerequisites must not contact a guest")
			return utm.CommandResult{}, nil
		}
		observe := func(code string) {
			report, _, err := c.DoctorResource(context.Background(), f.spec.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(report.Ready).To(gomega.BeFalse())
			gomega.Expect(report.Gaps[0].Code).To(gomega.Equal(code))
		}
		f.control.failStatus = map[string]bool{f.spec.VM.UTMVMUUID: true}
		observe("vm_state_unavailable")
		f.control.failStatus = nil
		fresh := f.s.vmLeases
		f.s.vmLeases = func() (io.ReadCloser, error) { return nil, errors.New("test-owned lease read failure") }
		observe("dhcp_leases_unreadable")
		f.s.vmLeases = fresh
		f.control.states[f.spec.VM.UTMVMUUID] = "stopped"
		observe("running_vm_unavailable")
		f.control.states[f.spec.VM.UTMVMUUID] = "started"
		f.s.vmLeases = func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(fmt.Sprintf("{\nip_address=192.168.64.3\nhw_address=1,%s\nlease=%d\n}\n", f.spec.VM.MACAddress, time.Now().Add(-time.Minute).Unix()))), nil
		}
		observe("dhcp_lease_unavailable")
		f.s.vmLeases = fresh
		pin := strings.SplitN(f.spec.Guest.KnownHostsReference, ":", 3)[2]
		data, err := os.ReadFile(pin)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Remove(pin)).To(gomega.Succeed())
		observe("pinned_key_unavailable")
		gomega.Expect(os.WriteFile(pin, data, 0600)).To(gomega.Succeed())
		f.s.sshCommand = func(_ context.Context, _ string, _ []string, input []byte) (utm.CommandResult, error) {
			fields := strings.Split(string(input), "\n")
			return utm.CommandResult{Stdout: f.spec.VM.UTMVMUUID + "\nDarwin\narm64\nforeign-challenge\n" + fields[1] + "\n"}, nil
		}
		observe("guest_identity_unavailable")
		after, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})
})
