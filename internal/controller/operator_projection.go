package controller

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
)

func (s *runtimeServer) operatorProjection(w http.ResponseWriter, r *http.Request) {
	if rawDiagnosticSelector(r.URL.RawQuery) {
		s.operatorDiagnostic(w, r)
		return
	}
	query := r.URL.Query()
	view := ""
	if values := query["view"]; len(values) > 0 {
		view = values[0]
	}
	if view == "" {
		view = "config"
	}
	if view == "diagnostic" {
		s.operatorDiagnostic(w, r)
		return
	}
	if view != "config" {
		send(w, http.StatusBadRequest, "operator.projection", nil, &responseError{
			Code:    "INVALID_REQUEST",
			Message: fmt.Sprintf("operator projection view %q is not implemented", view),
			Retry:   "none",
		})
		return
	}
	projection, err := s.ledger.ReadAcceptedConfiguration()
	if err != nil {
		reject(w, "operator.projection", err)
		return
	}
	installation := s.installationID()
	observed := time.Now().UTC()
	if installation == "" || observed.IsZero() {
		reject(w, "operator.projection", fmt.Errorf("%w: operator projection is missing installation_id or observed_at", managed.ErrInvalid))
		return
	}
	projection.Schema = "haepari.operator-projection/v1"
	projection.View = "config"
	projection.InstallationID = installation
	projection.ObservedAt = observed
	send(w, http.StatusOK, "operator.projection", projection, nil)
}

// rawDiagnosticSelector reports an exact view=diagnostic pair that url.ParseQuery
// would drop because its field contains a semicolon. Boundaries are '&' and ';'.
// The comparison uses the unescaped key and value, so preview=diagnostic and
// q=view=diagnostic are not selectors. An earlier '&' field whose unescaped
// view is config or empty stays on the legacy first-value path. A bare field
// with no '=' is that key and an empty value, matching url.Query.
func rawDiagnosticSelector(raw string) bool {
	for raw != "" {
		var field string
		field, raw, _ = strings.Cut(raw, "&")
		if strings.Contains(field, ";") {
			if semicolonDiagnostic(field) {
				return true
			}
			continue
		}
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			key, value = field, ""
		}
		ukey, errKey := url.QueryUnescape(key)
		uval, errVal := url.QueryUnescape(value)
		if errKey != nil || errVal != nil {
			continue
		}
		if ukey == "view" && (uval == "config" || uval == "") {
			return false
		}
	}
	return false
}

func semicolonDiagnostic(field string) bool {
	for field != "" {
		var segment string
		segment, field, _ = strings.Cut(field, ";")
		key, value, ok := strings.Cut(segment, "=")
		if !ok {
			continue
		}
		ukey, errKey := url.QueryUnescape(key)
		uval, errVal := url.QueryUnescape(value)
		if errKey != nil || errVal != nil {
			continue
		}
		if ukey == "view" && uval == "diagnostic" {
			return true
		}
	}
	return false
}

func (s *runtimeServer) operatorDiagnostic(w http.ResponseWriter, r *http.Request) {
	section, resource, cursor, limit, err := diagnosticQuery(r)
	if err != nil {
		reject(w, "operator.projection", err)
		return
	}
	s.workerMu.Lock()
	alive := make(map[string]bool, len(s.jobWorkers))
	for id, count := range s.jobWorkers {
		if count != 0 {
			alive[id] = true
		}
	}
	s.workerMu.Unlock()
	body, err := s.ledger.OperatorDiagnostic(managed.DiagnosticQuery{
		Section: section, Resource: resource, Cursor: cursor, Limit: limit,
		ObservedAt: time.Now().UTC(),
		WorkerAlive: func(jobID string) bool {
			return alive[jobID]
		},
	})
	if err != nil {
		reject(w, "operator.projection", err)
		return
	}
	send(w, http.StatusOK, "operator.projection", body, nil)
}

func diagnosticQuery(r *http.Request) (section, resource, cursor string, limit int, err error) {
	// Query drops parse failures and would omit a broken resource filter.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("%w: diagnostic query", managed.ErrInvalid)
	}
	allowed := map[string]bool{"view": true, "section": true, "limit": true, "cursor": true, "resource": true}
	values := map[string]string{}
	for key, list := range query {
		if !allowed[key] || len(list) != 1 || list[0] == "" {
			return "", "", "", 0, fmt.Errorf("%w: diagnostic query", managed.ErrInvalid)
		}
		values[key] = list[0]
	}
	if values["view"] != "diagnostic" {
		return "", "", "", 0, fmt.Errorf("%w: diagnostic query", managed.ErrInvalid)
	}
	section = values["section"]
	if section != "queue" && section != "retention" {
		return "", "", "", 0, fmt.Errorf("%w: diagnostic section", managed.ErrInvalid)
	}
	limit = 10
	if raw, ok := values["limit"]; ok {
		parsed, ok := canonicalDiagnosticLimit(raw)
		if !ok {
			return "", "", "", 0, fmt.Errorf("%w: diagnostic limit", managed.ErrInvalid)
		}
		limit = parsed
	}
	resource = values["resource"]
	if resource != "" && !managed.ValidRequestKey(resource) {
		return "", "", "", 0, fmt.Errorf("%w: diagnostic resource", managed.ErrInvalid)
	}
	return section, resource, values["cursor"], limit, nil
}

func canonicalDiagnosticLimit(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	if len(raw) > 1 && raw[0] == '0' {
		return 0, false
	}
	n := 0
	for _, c := range raw {
		n = n*10 + int(c-'0')
		if n > 20 {
			return 0, false
		}
	}
	if n < 1 {
		return 0, false
	}
	return n, true
}
