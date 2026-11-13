//go:build integration

package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// This in-process fixture never opens a listener or invokes a runner/backend.
func TestEvidenceRecoveryPlanDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name, kind, reason          string
		quarantined, backendBlocked bool
	}{
		{name: "resource availability", kind: "local", reason: "stage=resource_availability"},
		{name: "empty retained set", kind: "local", quarantined: true, reason: "stage=retained_job_selection"},
		{name: "backend refusal", kind: "utm-macos", quarantined: true, backendBlocked: true, reason: "stage=backend_selection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, err := os.MkdirTemp("/tmp", "hp-ed-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(parent) })
			root, err := state.Resolve(filepath.Join(parent, "state"))
			if err != nil {
				t.Fatal(err)
			}
			if err = root.Prepare(); err != nil {
				t.Fatal(err)
			}
			if err = os.Mkdir(filepath.Join(root.Path, "jobs"), 0700); err != nil {
				t.Fatal(err)
			}
			db, err := sqlite.Open(root.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ledger, err := managed.NewLedger(db, 1)
			if err != nil {
				t.Fatal(err)
			}
			ledger.UseLegacyFixtureBounds()
			store, err := snapshot.OpenStore(root.Path)
			if err != nil {
				t.Fatal(err)
			}
			server, err := newRuntime(root, ledger, store)
			if err != nil {
				t.Fatal(err)
			}
			spec := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "diagnostic-fixture", Kind: tc.kind, ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "offline-fixture"}}
			if tc.kind == "local" {
				spec.Cleanup = &managed.CleanupPolicy{PolicyReference: "fixture-owned"}
			} else {
				spec.Adapter.UTMCtl = "/fixture/not-invoked"
				spec.VM = &managed.VM{UTMVMUUID: "11111111-1111-4111-8111-111111111111", MACAddress: "aa:bb:cc:dd:ee:ff", AddressDiscovery: "dhcp-lease-by-mac", GuestJobRoot: "/fixture/guest-not-opened"}
				spec.Guest = &managed.Guest{ConnectionReference: "fixture-not-read", KnownHostsReference: "fixture-not-read"}
			}
			raw, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			if err = ledger.RegisterResource(raw); err != nil {
				t.Fatal(err)
			}
			if tc.quarantined {
				// Fault state in this disposable database only; no production transition or live ledger.
				if _, err = db.Exec(`UPDATE managed_resources SET availability='quarantined' WHERE id=?`, spec.ID); err != nil {
					t.Fatal(err)
				}
			}
			private := filepath.Join(root.Path, "auth-account-secret")
			if tc.backendBlocked {
				server.backendEvidence = func(string, string) (evidenceBackend, error) {
					return nil, fmt.Errorf("%w: %s", managed.ErrBlocked, private)
				}
			}
			before, err := ledger.ResourceRecoverySnapshot(spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			var eventsBefore int
			if err = db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&eventsBefore); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("POST", "/v1/resources/"+spec.ID+"/recover", bytes.NewBufferString(`{"plan":true,"action":"recollect"}`))
			request.Header.Set("X-Haepari-Protocol", protocol)
			request.Header.Set("X-Haepari-Protocol-Min", "1")
			request.Header.Set("X-Haepari-Protocol-Max", "1")
			response := httptest.NewRecorder()
			server.routes().ServeHTTP(response, request)
			if response.Code != 409 {
				t.Fatalf("HTTP status changed: %d %s", response.Code, response.Body.String())
			}
			envelope, err := managed.DecodeEnvelope(response.Body.Bytes())
			if err != nil {
				t.Fatalf("frozen envelope refused response: %v", err)
			}
			if envelope.OK || envelope.Operation != "resource.recover" || envelope.Error == nil || string(envelope.Error.Code) != "RECOVERY_BLOCKED" || envelope.Error.Retry != "none" {
				t.Fatalf("error contract changed: %s", response.Body.String())
			}
			if !strings.Contains(envelope.Error.Message, tc.reason) {
				t.Fatalf("failed plan lost classified refusal (%s): %s", tc.reason, envelope.Error.Message)
			}
			if strings.Contains(response.Body.String(), root.Path) || strings.Contains(response.Body.String(), private) || strings.Contains(response.Body.String(), "plan_token") || len(envelope.Data) != 0 {
				t.Fatalf("private state or plan authority leaked: %s", response.Body.String())
			}
			after, err := ledger.ResourceRecoverySnapshot(spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("read-only refused plan changed resource/retention state")
			}
			var eventsAfter int
			if err = db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&eventsAfter); err != nil {
				t.Fatal(err)
			}
			if eventsBefore != eventsAfter {
				t.Fatal("read-only refused plan published a mutation event")
			}
		})
	}
}

// An observation-only fault backend inherits no effect implementation. Any
// accidental Collect/Cleanup/Health call therefore fails this isolated test.
type refusedEvidenceObservation struct {
	evidenceBackend
	cause error
}

func (b refusedEvidenceObservation) Observe(managed.LocalIdentity, string, time.Time) (managed.RetainedPath, error) {
	return managed.RetainedPath{}, b.cause
}

var _ = ginkgo.Describe("EvidenceRecoveryPlanDiagnostic", ginkgo.Label("integration"), func() {
	for _, action := range []string{"recollect", "dispose"} {
		for _, fault := range []string{"worker_liveness", "ownership_observation"} {
			ginkgo.It("identifies the failed Job for "+action+" at "+fault+" without changing retained state", func() {
				s, r, _ := protectedLocalFixture()
				if fault == "worker_liveness" {
					release := make(chan struct{})
					gomega.Expect(s.startJobWorker(r.JobID, func() { <-release })).To(gomega.BeTrue())
					defer func() { close(release); s.workers.Wait() }()
				} else {
					// Corrupt only this fixture's ownership binding. Both v1 and v2
					// must refuse it without inspecting or deleting retained evidence.
					input := filepath.Join(s.root.Path, "jobs", r.JobID, "input.json")
					gomega.Expect(os.WriteFile(input, []byte(`{}`), 0600)).To(gomega.Succeed())
				}
				before, err := s.ledger.ResourceRecoverySnapshot(r.ResourceID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				response := recoveryHTTP(s, "/v1/resources/"+r.ResourceID+"/recover", managed.RecoveryRequest{Plan: true, Action: action})
				gomega.Expect(response.Code).To(gomega.Equal(409), response.Body.String())
				envelope, err := managed.DecodeEnvelope(response.Body.Bytes())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(envelope.Error.Code).To(gomega.Equal(managed.RecoveryBlocked))
				gomega.Expect(envelope.Error.Retry).To(gomega.Equal("none"))
				gomega.Expect(envelope.Error.Message).To(gomega.ContainSubstring("Job " + r.JobID))
				gomega.Expect(envelope.Error.Message).To(gomega.ContainSubstring("stage=" + fault))
				gomega.Expect(envelope.Error.Message).To(gomega.ContainSubstring("category=observation_failed"))
				gomega.Expect(envelope.Data).To(gomega.BeEmpty())
				gomega.Expect(response.Body.String()).NotTo(gomega.ContainSubstring("plan_token"))
				gomega.Expect(response.Body.String()).NotTo(gomega.ContainSubstring(s.root.Path))
				after, err := s.ledger.ResourceRecoverySnapshot(r.ResourceID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(after).To(gomega.Equal(before))
			})
		}
		ginkgo.It("preserves the native-termination tokenless plan for "+action+" and redacts wrapped backend details", func() {
			s, r, _ := protectedGuestFixture()
			private := "/private/guest/auth.json ssh-output-secret"
			s.backendEvidence = func(string, string) (evidenceBackend, error) {
				return refusedEvidenceObservation{cause: fmt.Errorf("%w: %s", errVMNativeTermination, private)}, nil
			}
			before, err := s.ledger.ResourceRecoverySnapshot(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			p, err := s.planEvidenceRecovery(r.ResourceID, action)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(p.PlanToken).To(gomega.BeEmpty())
			gomega.Expect(p.Unknowns).To(gomega.ContainElement(errVMNativeTermination.Error()))
			gomega.Expect(strings.Join(p.Unknowns, " ")).To(gomega.ContainSubstring("Job " + r.JobID + "; stage=ownership_observation; category=termination_unproven"))
			response := recoveryHTTP(s, "/v1/resources/"+r.ResourceID+"/recover", managed.RecoveryRequest{Plan: true, Action: action})
			gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
			gomega.Expect(response.Body.String()).NotTo(gomega.ContainSubstring(private))
			gomega.Expect(response.Body.String()).NotTo(gomega.ContainSubstring("plan_token"))
			after, err := s.ledger.ResourceRecoverySnapshot(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after).To(gomega.Equal(before))
		})
		ginkgo.It("keeps backend error semantics and safe categories for "+action+" on the actual Job path", func() {
			s, r, _ := protectedGuestFixture()
			for _, tc := range []struct {
				cause           error
				status          int
				code            managed.ErrorCode
				retry, category string
			}{
				{errors.Join(managed.ErrBlocked, os.ErrPermission), 409, managed.RecoveryBlocked, "none", "permission_denied"},
				{managed.ErrStale, 409, managed.StaleRevision, "none", "stale_revision"},
				{errors.New("private-ssh-output"), 503, managed.StorageUnavailable, "reconcile", "observation_failed"},
			} {
				s.backendEvidence = func(string, string) (evidenceBackend, error) {
					return refusedEvidenceObservation{cause: tc.cause}, nil
				}
				response := recoveryHTTP(s, "/v1/resources/"+r.ResourceID+"/recover", managed.RecoveryRequest{Plan: true, Action: action})
				gomega.Expect(response.Code).To(gomega.Equal(tc.status), response.Body.String())
				envelope, err := managed.DecodeEnvelope(response.Body.Bytes())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(envelope.Error.Code).To(gomega.Equal(tc.code))
				gomega.Expect(envelope.Error.Retry).To(gomega.Equal(tc.retry))
				gomega.Expect(envelope.Error.Message).To(gomega.ContainSubstring("Job " + r.JobID + "; stage=ownership_observation; category=" + tc.category))
				gomega.Expect(response.Body.String()).NotTo(gomega.ContainSubstring("private-ssh-output"))
				gomega.Expect(response.Body.String()).NotTo(gomega.ContainSubstring("plan_token"))
				gomega.Expect(envelope.Data).To(gomega.BeEmpty())
			}
		})
	}
})
