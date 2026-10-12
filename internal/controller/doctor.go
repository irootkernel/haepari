package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
	"golang.org/x/sys/unix"
)

func (s *runtimeServer) resourceDoctor(w http.ResponseWriter, r *http.Request) {
	const op = "doctor"
	if !managed.ValidRequestKey(r.PathValue("id")) {
		reject(w, op, managed.ErrInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	report := managed.DoctorReport{Schema: "haepari.doctor/v1", InstallationID: s.installationID(),
		ResourceID: r.PathValue("id"), Gaps: []managed.DoctorGap{}}
	gap := func(code, message string) {
		report.Gaps = append(report.Gaps, managed.DoctorGap{Code: code, Message: message})
	}
	before, err := s.ledger.ResourceStatus(report.ResourceID)
	if err != nil {
		if !errors.Is(err, managed.ErrNotFound) {
			reject(w, op, err)
			return
		}
		gap("resource_not_found", "Register the intended resource explicitly before selecting this gate.")
	} else {
		report.Resource = &before
		switch before.Kind {
		case "local":
			if _, _, err := s.observeLocalReadiness(before.ID); err != nil {
				gap("local_root_unready", "Inspect the selected local resource's owned jobs directory and permissions.")
			}
		case "utm-macos":
			if failure := s.doctorVM(ctx, before.ID); failure != nil {
				gap(failure.Code, failure.Message)
			}
		default:
			gap("unsupported_resource", "Project execution requires a local or utm-macos resource, not an advisory lock.")
		}
		after, err := s.ledger.ResourceStatus(before.ID)
		if err != nil {
			gap("resource_observation_unavailable", "Cannot re-read the selected resource; inspect controller storage and the resource, then observe again before a new admission.")
		} else if after.ConfigRevision != before.ConfigRevision || after.ConfigurationDigest != before.ConfigurationDigest {
			gap("configuration_changed", "The resource configuration changed during preflight; observe it again before a new admission.")
		} else {
			report.Resource = &after
		}
	}
	report.Ready = len(report.Gaps) == 0
	report.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	send(w, http.StatusOK, op, report, nil)
}

func (s *runtimeServer) doctorVM(ctx context.Context, id string) *managed.DoctorGap {
	failure := func(code, message string) *managed.DoctorGap {
		return &managed.DoctorGap{Code: code, Message: message + " No VM or environment repair was performed."}
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return failure("host_platform_unsupported", "Use a macOS arm64 controller for the selected UTM resource.")
	}
	spec, err := s.vmSpec(id)
	if err != nil {
		return failure("resource_configuration_unavailable", "Inspect and explicitly repair the selected registered VM configuration.")
	}
	probe := s.doctorHostSession
	if probe == nil {
		probe = func(ctx context.Context) error {
			command := exec.CommandContext(ctx, "/bin/launchctl", "print", fmt.Sprintf("gui/%d", os.Geteuid()))
			command.Stdout, command.Stderr = io.Discard, io.Discard
			return command.Run()
		}
	}
	if err := probe(ctx); err != nil {
		return failure("aqua_session_unavailable", "Run the intended controller in the same user's logged-in Aqua session.")
	}
	var leases io.ReadCloser
	if s.vmLeases != nil {
		leases, err = s.vmLeases()
	} else {
		leases, err = os.OpenFile("/var/db/dhcpd_leases", os.O_RDONLY|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return failure("dhcp_leases_unreadable", "Check read access to the native DHCP lease database for the intended controller.")
	}
	defer leases.Close()
	ssh := utm.SSH{Executable: "/usr/bin/ssh", Control: s.vmControl(spec), Run: s.sshCommand}
	_, err = ssh.Preflight(ctx, spec, leases)
	if err == nil {
		return nil
	}
	var prerequisite *utm.PrerequisiteError
	if errors.As(err, &prerequisite) {
		messages := map[string]string{
			"resource_spec":  "Inspect the registered VM's UUID, MAC, SSH references and guest root configuration.",
			"pinned_key":     "Check the registered digest-pinned SSH host key; do not discover or replace trust automatically.",
			"ssh_mapping":    "Check the registered private SSH mapping and its explicit identity file.",
			"host_snapshot":  "Check the controller's private temporary storage for the SSH pin snapshot.",
			"vm_state":       "Check read-only UTM access and the registered UUID in the intended Aqua session.",
			"running_vm":     "The registered VM is not currently running; select an already-running authorized VM.",
			"dhcp_lease":     "Check that exactly one individual unexpired DHCP lease matches the registered MAC.",
			"guest_identity": "Check pinned SSH access, Remote Login and fresh guest boot, MAC and Darwin arm64 identity.",
		}
		if message, ok := messages[prerequisite.Check]; ok {
			return failure(prerequisite.Check+"_unavailable", message)
		}
	}
	return failure("vm_prerequisite_unavailable", "VM prerequisite observation is unavailable; inspect the selected resource and retry the read-only check.")
}
