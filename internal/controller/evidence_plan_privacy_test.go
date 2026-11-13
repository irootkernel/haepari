package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/utm"
)

func TestEvidenceRecoveryPlanDiagnosticPrivacy(t *testing.T) {
	private := "/private/fixture/account/auth.json secret-token-value request_bytes"
	for _, cause := range []error{
		managed.ErrBlocked, managed.ErrInvalid, managed.ErrUnsupported,
		managed.ErrStale, managed.ErrNotFound, managed.ErrConflict,
		managed.ErrBusy, managed.ErrResultUnavailable, managed.ErrLockExpired,
		managed.ErrNestedAcquisition, managed.ErrNestedWait,
		snapshot.ErrChanged, snapshot.ErrInvalid, snapshot.ErrSourceUnavailable,
		utm.ErrUnavailable, errors.New("storage"),
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			raw := fmt.Errorf("%w: %s", cause, private)
			refusal := evidencePlanRefusal("failed-job", "ownership_observation", raw)
			if !errors.Is(refusal, cause) {
				t.Fatal("cause classification lost")
			}
			baseline, got := httptest.NewRecorder(), httptest.NewRecorder()
			reject(baseline, "resource.recover", cause)
			reject(got, "resource.recover", refusal)
			before, err := managed.DecodeEnvelope(baseline.Body.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			after, err := managed.DecodeEnvelope(got.Body.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if got.Code != baseline.Code || after.Error.Code != before.Error.Code || after.Error.Retry != before.Error.Retry {
				t.Fatalf("HTTP/code/retry changed: %s", got.Body.String())
			}
			exitBefore, err := managed.ExitCode(before.Error.Code)
			if err != nil {
				t.Fatal(err)
			}
			exitAfter, err := managed.ExitCode(after.Error.Code)
			if err != nil || exitAfter != exitBefore {
				t.Fatal("exit semantics changed")
			}
			for _, want := range []string{"Job failed-job", "stage=ownership_observation", "category="} {
				if !strings.Contains(after.Error.Message, want) {
					t.Fatalf("missing %s: %s", want, got.Body.String())
				}
			}
			if strings.Contains(got.Body.String(), private) || strings.Contains(got.Body.String(), "secret-token-value") || strings.Contains(got.Body.String(), "plan_token") || len(after.Data) != 0 {
				t.Fatalf("private details or apply authority leaked: %s", got.Body.String())
			}
		})
	}
}

func TestEvidenceRecoveryPlanDiagnosticCategories(t *testing.T) {
	for _, tc := range []struct {
		cause    error
		category string
	}{
		{context.DeadlineExceeded, managed.DiagnosticDeadlineExceeded},
		{runner.ErrConflict, managed.DiagnosticIdentityConflict},
		{runner.ErrUnproven, managed.DiagnosticTerminationUnproven},
		{runner.ErrAmbiguous, managed.DiagnosticReceiptAmbiguous},
		{runner.ErrInvalid, managed.DiagnosticInvalidReceipt},
		{os.ErrPermission, managed.DiagnosticPermissionDenied},
		{os.ErrNotExist, managed.DiagnosticPathAbsent},
		{managed.ErrStale, managed.DiagnosticStaleRevision},
		{utm.ErrGuestUncertain, managed.DiagnosticGuestEffectUncertain},
		{utm.ErrUnavailable, managed.DiagnosticTransportUnavailable},
		{errors.New("private raw output"), managed.DiagnosticObservationFailed},
		{errVMNativeTermination, managed.DiagnosticTerminationUnproven},
	} {
		refusal := evidencePlanRefusal("failed-job", "ownership_observation", tc.cause)
		if !strings.Contains(refusal.Error(), "category="+tc.category+".") {
			t.Fatalf("wrong fixed category: %s", refusal)
		}
	}
	// A malformed retained identifier cannot become an arbitrary message payload.
	if strings.Contains(evidencePlanRefusal("/private/secret", "job_snapshot", managed.ErrBlocked).Error(), "/private/secret") {
		t.Fatal("invalid identifier leaked")
	}
}
