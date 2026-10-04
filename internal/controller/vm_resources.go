package controller

import (
	"encoding/json"
	"net/http"

	"github.com/irootkernel/haepari/internal/managed"
)

func (s *runtimeServer) vmResourceUpdate(w http.ResponseWriter, r *http.Request) {
	const op = "resource.update"
	var q struct {
		Spec                         json.RawMessage `json:"spec"`
		ExpectedRevision             int64           `json:"expected_revision"`
		ExpectedAvailabilityRevision int64           `json:"expected_availability_revision"`
	}
	if err := decodeBody(r, &q); err != nil {
		reject(w, op, err)
		return
	}
	spec, err := managed.DecodeResource(q.Spec)
	if err != nil {
		reject(w, op, err)
		return
	}
	if spec.ID != r.PathValue("id") || spec.Kind != "utm-macos" || q.ExpectedRevision <= 0 || q.ExpectedAvailabilityRevision <= 0 {
		reject(w, op, managed.ErrInvalid)
		return
	}
	// Refuse stale/occupied updates before the read-only UTM inspection, then
	// let the transaction enforce the same fences after the external observation.
	status, err := s.ledger.ResourceStatus(spec.ID)
	if err != nil {
		reject(w, op, err)
		return
	}
	if status.ConfigRevision != q.ExpectedRevision || status.AvailabilityRevision != q.ExpectedAvailabilityRevision || spec.Revision != q.ExpectedRevision+1 {
		reject(w, op, managed.ErrStale)
		return
	}
	if status.Occupancy != 0 {
		reject(w, op, managed.ErrBusy)
		return
	}
	if err := s.verifyVMRegistration(r.Context(), spec); err != nil {
		reject(w, op, err)
		return
	}
	if err := s.ledger.UpdateResource(q.Spec, q.ExpectedRevision, q.ExpectedAvailabilityRevision); err != nil {
		reject(w, op, err)
		return
	}
	updated, err := s.ledger.ResourceStatus(spec.ID)
	if err != nil {
		reject(w, op, err)
		return
	}
	send(w, 200, op, updated, nil)
}
