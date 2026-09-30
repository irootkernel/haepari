package controller

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/irootkernel/haepari/internal/managed"
)

func pageQuery(r *http.Request, jobs bool) (url.Values, int, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, 0, managed.ErrInvalid
	}
	for key, values := range q {
		if len(values) != 1 || (key != "limit" && key != "cursor" && !(jobs && (key == "resource" || key == "state"))) {
			return nil, 0, managed.ErrInvalid
		}
	}
	limit := 50
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			return nil, 0, managed.ErrInvalid
		}
	}
	if limit < 1 || limit > 100 {
		return nil, 0, managed.ErrInvalid
	}
	return q, limit, nil
}
func (s *runtimeServer) jobsList(w http.ResponseWriter, r *http.Request) {
	q, limit, err := pageQuery(r, true)
	if err != nil {
		reject(w, "jobs.list", err)
		return
	}
	page, err := s.ledger.JobsPage(managed.JobsQuery{Resource: q.Get("resource"), State: q.Get("state"), Cursor: q.Get("cursor"), Limit: limit})
	if err != nil {
		reject(w, "jobs.list", err)
		return
	}
	send(w, 200, "jobs.list", page, nil)
}
func (s *runtimeServer) resourcesList(w http.ResponseWriter, r *http.Request) {
	q, limit, err := pageQuery(r, false)
	if err != nil {
		reject(w, "resources.list", err)
		return
	}
	page, err := s.ledger.ResourcesPage(q.Get("cursor"), limit)
	if err != nil {
		reject(w, "resources.list", err)
		return
	}
	send(w, 200, "resources.list", page, nil)
}
