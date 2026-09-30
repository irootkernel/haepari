package controller

import (
	"encoding/json"
	"net/http"

	"github.com/irootkernel/haepari/internal/managed"
)

func (s *runtimeServer) lockResourceAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	op := "resource." + action
	var q struct {
		Spec             json.RawMessage `json:"spec,omitempty"`
		ExpectedRevision int64           `json:"expected_revision,omitempty"`
		Plan             bool            `json:"plan,omitempty"`
	}
	if err := decodeLockBody(r, &q); err != nil {
		reject(w, op, err)
		return
	}
	status, err := s.ledger.ResourceStatus(r.PathValue("id"))
	if err != nil {
		reject(w, op, err)
		return
	}
	if status.Kind != "lock" {
		reject(w, op, managed.ErrUnsupported)
		return
	}
	switch action {
	case "update":
		if len(q.Spec) == 0 || q.ExpectedRevision <= 0 || q.Plan {
			reject(w, op, managed.ErrInvalid)
			return
		}
		spec, err := managed.DecodeResource(q.Spec)
		if err != nil {
			reject(w, op, err)
			return
		}
		if spec.ID != r.PathValue("id") || spec.Kind != "lock" {
			reject(w, op, managed.ErrInvalid)
			return
		}
		err = s.ledger.UpdateResource(q.Spec, q.ExpectedRevision, status.AvailabilityRevision)
		if err != nil {
			reject(w, op, err)
			return
		}
		updated, err := s.ledger.ResourceStatus(spec.ID)
		if err != nil {
			reject(w, op, err)
			return
		}
		sendLock(w, op, updated)
	case "remove":
		if len(q.Spec) != 0 || q.ExpectedRevision <= 0 || q.Plan {
			reject(w, op, managed.ErrInvalid)
			return
		}
		if err := s.ledger.RemoveLockResource(status.ID, q.ExpectedRevision); err != nil {
			reject(w, op, err)
			return
		}
		sendLock(w, op, map[string]any{"resource_id": status.ID, "removed": true})
	case "recover":
		if len(q.Spec) != 0 || q.ExpectedRevision != 0 || !q.Plan {
			reject(w, op, managed.ErrInvalid)
			return
		}
		reject(w, op, managed.ErrUnsupported)
	default:
		reject(w, op, managed.ErrInvalid)
	}
}
