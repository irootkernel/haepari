package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/irootkernel/haepari/internal/config"
)

const (
	diagnosticSchema        = "haepari.operator-diagnostic/v1"
	diagnosticDataCap       = 65536
	diagnosticQueueKind     = "operator-diagnostic-queue"
	diagnosticRetentionKind = "operator-diagnostic-retention"
)

// DiagnosticQuery is one strict diagnostic read. WorkerAlive is the trusted
// in-process controller worker table. A nil function is missing proof.
type DiagnosticQuery struct {
	Section     string
	Resource    string
	Cursor      string
	Limit       int
	ObservedAt  time.Time
	WorkerAlive func(string) bool
}

// OperatorDiagnostic is the haepari.operator-diagnostic/v1 body.
type OperatorDiagnostic struct {
	Schema              string                   `json:"schema"`
	View                string                   `json:"view"`
	Section             string                   `json:"section"`
	InstallationID      string                   `json:"installation_id"`
	ObservedAt          time.Time                `json:"observed_at"`
	AnyUnknownOccupancy bool                     `json:"any_unknown_occupancy"`
	CurrentConfig       DiagnosticConfig         `json:"current_config"`
	Page                DiagnosticPageMeta       `json:"page"`
	Resources           []DiagnosticQueueRow     `json:"resources,omitempty"`
	Jobs                []DiagnosticRetentionRow `json:"jobs,omitempty"`
}

// DiagnosticConfig is the snapshot header. Unknown is absence or corruption.
// A present header has no retention day counts.
type DiagnosticConfig struct {
	Status              string `json:"status"`
	Reason              string `json:"reason,omitempty"`
	Revision            int64  `json:"revision,omitempty"`
	DocumentDigest      string `json:"document_digest,omitempty"`
	ValuesDigest        string `json:"values_digest,omitempty"`
	GlobalLimit         int64  `json:"global_limit,omitempty"`
	HostMACOSGuestLimit int64  `json:"host_macos_guest_limit,omitempty"`
}

// DiagnosticPageMeta is the closed page object. next_cursor is omitted unless more is true.
type DiagnosticPageMeta struct {
	Limit      int    `json:"limit"`
	More       bool   `json:"more"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// DiagnosticCeilings holds the six resource ceilings. queue_seconds is shown and not summed.
type DiagnosticCeilings struct {
	QueueSeconds   int64 `json:"queue_seconds"`
	PrepareSeconds int64 `json:"prepare_seconds"`
	RunSeconds     int64 `json:"run_seconds"`
	ManualSeconds  int64 `json:"manual_seconds"`
	CollectSeconds int64 `json:"collect_seconds"`
	CleanupSeconds int64 `json:"cleanup_seconds"`
}

// DiagnosticQueueRow is one resources[] element. Bounded zeroes stay present.
type DiagnosticQueueRow struct {
	ResourceID                string              `json:"resource_id"`
	Kind                      string              `json:"kind"`
	Availability              string              `json:"availability"`
	Status                    string              `json:"status"`
	Reason                    string              `json:"reason,omitempty"`
	CeilingSource             string              `json:"ceiling_source,omitempty"`
	Ceilings                  *DiagnosticCeilings `json:"ceilings,omitempty"`
	IncludedJobs              *int64              `json:"included_jobs,omitempty"`
	BacklogSeconds            *int64              `json:"backlog_seconds,omitempty"`
	ProspectiveCeilingSeconds *int64              `json:"prospective_ceiling_seconds,omitempty"`
	TotalSeconds              *int64              `json:"total_seconds,omitempty"`
}

func (p OperatorDiagnostic) MarshalJSON() ([]byte, error) {
	payload := map[string]any{
		"schema":                p.Schema,
		"view":                  p.View,
		"section":               p.Section,
		"installation_id":       p.InstallationID,
		"observed_at":           p.ObservedAt,
		"any_unknown_occupancy": p.AnyUnknownOccupancy,
		"current_config":        p.CurrentConfig,
		"page":                  p.Page,
	}
	if p.Section == "retention" {
		jobs := p.Jobs
		if jobs == nil {
			jobs = []DiagnosticRetentionRow{}
		}
		payload["jobs"] = jobs
	} else {
		resources := p.Resources
		if resources == nil {
			resources = []DiagnosticQueueRow{}
		}
		payload["resources"] = resources
	}
	return json.Marshal(payload)
}

type diagnosticItem struct {
	id       string
	sequence int64
	queue    DiagnosticQueueRow
	job      DiagnosticRetentionRow
}

// OperatorDiagnostic reads one installation snapshot. The data cap is 65536 bytes.
func (l *Ledger) OperatorDiagnostic(q DiagnosticQuery) (OperatorDiagnostic, error) {
	return l.readOperatorDiagnostic(q, diagnosticDataCap)
}

func (l *Ledger) readOperatorDiagnostic(q DiagnosticQuery, cap int) (OperatorDiagnostic, error) {
	if (q.Section != "queue" && q.Section != "retention") || q.Limit < 1 || q.Limit > 20 || cap < 1 {
		return OperatorDiagnostic{}, ErrInvalid
	}
	if q.Resource != "" && !ValidRequestKey(q.Resource) {
		return OperatorDiagnostic{}, ErrInvalid
	}
	observed := q.ObservedAt.UTC()
	if q.ObservedAt.IsZero() {
		observed = time.Now().UTC()
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OperatorDiagnostic{}, err
	}
	defer tx.Rollback()
	installation, err := installationTx(tx)
	if err != nil {
		return OperatorDiagnostic{}, err
	}
	if q.Resource != "" {
		var one int
		err = tx.QueryRow(`SELECT 1 FROM managed_resources WHERE id=?`, q.Resource).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return OperatorDiagnostic{}, ErrNotFound
		}
		if err != nil {
			return OperatorDiagnostic{}, err
		}
	}
	header, err := diagnosticHeaderTx(tx)
	if err != nil {
		return OperatorDiagnostic{}, err
	}
	kind := diagnosticQueueKind
	if q.Section == "retention" {
		kind = diagnosticRetentionKind
	}
	cursor := pageCursor{Kind: kind, Installation: installation, Resource: q.Resource}
	if q.Cursor != "" {
		cursor, err = decodePageCursor(q.Cursor)
		if err != nil {
			return OperatorDiagnostic{}, err
		}
		if err = bindDiagnosticCursor(cursor, kind, installation, q.Resource); err != nil {
			return OperatorDiagnostic{}, err
		}
	} else if q.Section == "queue" {
		if q.Resource != "" {
			cursor.UpperID = q.Resource
		} else if err = tx.QueryRow(`SELECT COALESCE(MAX(id),'') FROM managed_resources`).Scan(&cursor.UpperID); err != nil {
			return OperatorDiagnostic{}, err
		}
	} else if err = tx.QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM managed_admissions`).Scan(&cursor.Upper); err != nil {
		return OperatorDiagnostic{}, err
	}
	queueItems, anyUnknown, err := queueItemsTx(tx, q, cursor, q.WorkerAlive)
	if err != nil {
		return OperatorDiagnostic{}, err
	}
	items := queueItems
	if q.Section == "retention" {
		items, err = retentionItemsTx(tx, q, cursor, observed)
		if err != nil {
			return OperatorDiagnostic{}, err
		}
	}
	page, err := fitDiagnosticPage(OperatorDiagnostic{
		Schema: diagnosticSchema, View: "diagnostic", Section: q.Section,
		InstallationID: installation, ObservedAt: observed, AnyUnknownOccupancy: anyUnknown,
		CurrentConfig: header, Page: DiagnosticPageMeta{Limit: q.Limit},
	}, items, kind, installation, q.Resource, cursor.UpperID, cursor.Upper, q.Limit, cap)
	if err != nil {
		return OperatorDiagnostic{}, err
	}
	return page, nil
}

func diagnosticHeaderTx(tx *sql.Tx) (DiagnosticConfig, error) {
	var revision int64
	var raw []byte
	var documentDigest, valuesDigest string
	err := tx.QueryRow(`SELECT revision,document_json,document_digest,values_digest FROM managed_effective_config WHERE singleton=1`).Scan(&revision, &raw, &documentDigest, &valuesDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return DiagnosticConfig{Status: "unknown", Reason: "absent"}, nil
	}
	if err != nil {
		return DiagnosticConfig{}, err
	}
	doc, _, parseErr := config.Parse(raw)
	if parseErr != nil {
		return DiagnosticConfig{Status: "unknown", Reason: "corrupt"}, nil
	}
	canonical, gotDocument, gotValues, digestErr := doc.Digests()
	if digestErr != nil || doc.Revision != revision || !bytes.Equal(canonical, raw) || gotDocument != documentDigest || gotValues != valuesDigest {
		return DiagnosticConfig{Status: "unknown", Reason: "corrupt"}, nil
	}
	return DiagnosticConfig{
		Status: "present", Revision: doc.Revision, DocumentDigest: gotDocument, ValuesDigest: gotValues,
		GlobalLimit: doc.GlobalLimit, HostMACOSGuestLimit: doc.HostMACOSGuestLimit,
	}, nil
}

func bindDiagnosticCursor(c pageCursor, kind, installation, resource string) error {
	if c.Kind != kind || c.Installation != installation || c.Resource != resource || c.State != "" {
		return ErrInvalid
	}
	switch kind {
	case diagnosticQueueKind:
		if c.Last != 0 || c.Upper != 0 || !ValidRequestKey(c.LastID) || !ValidRequestKey(c.UpperID) || c.UpperID < c.LastID {
			return ErrInvalid
		}
		// A named resource page fixes the upper bound to that id. The installation
		// page keeps the cursor's own upper and does not reread MAX.
		if resource != "" && c.UpperID != resource {
			return ErrInvalid
		}
	case diagnosticRetentionKind:
		if c.LastID != "" || c.UpperID != "" || c.Last <= 0 || c.Upper < c.Last {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func diagnosticCursor(kind, installation, resource, upperID string, upper int64, item diagnosticItem) pageCursor {
	c := pageCursor{Kind: kind, Installation: installation, Resource: resource}
	if kind == diagnosticQueueKind {
		c.LastID = item.id
		c.UpperID = upperID
		return c
	}
	c.Last = item.sequence
	c.Upper = upper
	return c
}

// fitDiagnosticPage keeps a prefix whose full serialized body, including the
// header, current_config, section array, and page cursor, is within cap.
// It does not skip a row to fit a later one. The first row is rejected when
// that whole body cannot fit.
func fitDiagnosticPage(base OperatorDiagnostic, items []diagnosticItem, kind, installation, resource, upperID string, upper int64, limit, cap int) (OperatorDiagnostic, error) {
	if limit < 1 || cap < 1 {
		return OperatorDiagnostic{}, ErrInvalid
	}
	if len(items) == 0 {
		page := diagnosticBody(base, nil, false, kind, installation, resource, upperID, upper)
		if err := diagnosticBodyFits(page, cap); err != nil {
			return OperatorDiagnostic{}, err
		}
		return page, nil
	}
	kept := make([]diagnosticItem, 0, limit)
	for _, item := range items {
		if len(kept) == limit {
			return diagnosticBody(base, kept, true, kind, installation, resource, upperID, upper), nil
		}
		next := append(append([]diagnosticItem{}, kept...), item)
		more := len(next) < len(items)
		page := diagnosticBody(base, next, more, kind, installation, resource, upperID, upper)
		if err := diagnosticBodyFits(page, cap); err != nil {
			if len(kept) == 0 {
				return OperatorDiagnostic{}, err
			}
			return diagnosticBody(base, kept, true, kind, installation, resource, upperID, upper), nil
		}
		kept = next
		if !more {
			return page, nil
		}
	}
	return diagnosticBody(base, kept, false, kind, installation, resource, upperID, upper), nil
}

func diagnosticBody(base OperatorDiagnostic, kept []diagnosticItem, more bool, kind, installation, resource, upperID string, upper int64) OperatorDiagnostic {
	page := base
	page.Page.More = more
	page.Page.NextCursor = ""
	if more && len(kept) > 0 {
		page.Page.NextCursor = encodePageCursor(diagnosticCursor(kind, installation, resource, upperID, upper, kept[len(kept)-1]))
	}
	if page.Section == "retention" {
		page.Jobs = make([]DiagnosticRetentionRow, 0, len(kept))
		for _, item := range kept {
			page.Jobs = append(page.Jobs, item.job)
		}
		return page
	}
	page.Resources = make([]DiagnosticQueueRow, 0, len(kept))
	for _, item := range kept {
		page.Resources = append(page.Resources, item.queue)
	}
	return page
}

func diagnosticBodyFits(page OperatorDiagnostic, cap int) error {
	raw, err := json.Marshal(page)
	if err != nil {
		return err
	}
	if len(raw) > cap {
		return fmt.Errorf("%w: diagnostic row exceeds the page", ErrInvalid)
	}
	return nil
}

func diagnosticInt(v int64) *int64 { return &v }

type queueJob struct {
	id, state string
	spec      []byte
}

func queueItemsTx(tx *sql.Tx, q DiagnosticQuery, cursor pageCursor, alive func(string) bool) ([]diagnosticItem, bool, error) {
	if alive == nil {
		alive = func(string) bool { return false }
	}
	rows, err := tx.Query(`SELECT id,kind,availability,occupancy,spec_json FROM managed_resources ORDER BY id`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	type resourceRow struct {
		id, kind, availability string
		occupancy              int
		spec                   []byte
	}
	resources := []resourceRow{}
	for rows.Next() {
		var row resourceRow
		if err = rows.Scan(&row.id, &row.kind, &row.availability, &row.occupancy, &row.spec); err != nil {
			return nil, false, err
		}
		resources = append(resources, row)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	items := []diagnosticItem{}
	anyUnknown := false
	for _, resource := range resources {
		built, unknown, err := diagnosticQueueRowTx(tx, resource.id, resource.kind, resource.availability, resource.occupancy, resource.spec, alive)
		if err != nil {
			return nil, false, err
		}
		if unknown {
			anyUnknown = true
		}
		if resource.id <= cursor.LastID || (cursor.UpperID != "" && resource.id > cursor.UpperID) {
			continue
		}
		if q.Resource != "" && resource.id != q.Resource {
			continue
		}
		if q.Section != "queue" {
			continue
		}
		items = append(items, diagnosticItem{id: resource.id, queue: built})
	}
	return items, anyUnknown, nil
}

func diagnosticQueueRowTx(tx *sql.Tx, id, kind, availability string, occupancy int, specJSON []byte, alive func(string) bool) (DiagnosticQueueRow, bool, error) {
	row := DiagnosticQueueRow{ResourceID: id, Kind: kind, Availability: availability}
	spec, specErr := DecodeResource(specJSON)
	if specErr != nil {
		row.Status = "unknown"
		row.Reason = "resource_spec_unreadable"
		return row, true, nil
	}
	jobs, err := queueJobsTx(tx, id)
	if err != nil {
		return DiagnosticQueueRow{}, false, err
	}
	uncertain, err := diagnosticPowerUncertainTx(tx, kind, id)
	if err != nil {
		return DiagnosticQueueRow{}, false, err
	}
	if queueExecutionUnknown(jobs, occupancy, alive, uncertain) {
		row.Status = "unknown"
		row.Reason = "unknown_occupied_execution"
		return row, true, nil
	}
	included, parts, unreadable := queueBudgets(jobs)
	if unreadable {
		row.Status = "unknown"
		row.Reason = "unreadable_admitted_budget"
		return row, false, nil
	}
	source, ceilings := queueCeilings(spec)
	if !applyQueueTotals(&row, source, ceilings, included, parts) {
		return row, false, nil
	}
	if kind == "lock" {
		row.Status = "not_applicable"
		row.Reason = "lock_resource"
		row.CeilingSource = ""
		row.Ceilings = nil
		row.IncludedJobs = nil
		row.BacklogSeconds = nil
		row.ProspectiveCeilingSeconds = nil
		row.TotalSeconds = nil
		return row, false, nil
	}
	return row, false, nil
}

func queueJobsTx(tx *sql.Tx, resourceID string) ([]queueJob, error) {
	rows, err := tx.Query(`SELECT id,state,spec_json FROM managed_jobs WHERE resource_id=? AND state!='finished' ORDER BY id`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []queueJob{}
	for rows.Next() {
		var job queueJob
		if err = rows.Scan(&job.id, &job.state, &job.spec); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func queueExecutionUnknown(jobs []queueJob, occupancy int, alive func(string) bool, uncertain bool) bool {
	if uncertain {
		return true
	}
	executing := 0
	for _, job := range jobs {
		if job.state == "reconciling" {
			return true
		}
		if job.state != "queued" {
			executing++
			if !alive(job.id) {
				return true
			}
		}
	}
	return executing != occupancy
}

func diagnosticPowerUncertainTx(tx *sql.Tx, kind, id string) (bool, error) {
	pending, err := pendingPowerTx(tx, id)
	if err != nil || pending {
		return pending, err
	}
	if kind != "utm-macos" {
		return false, nil
	}
	var power string
	err = tx.QueryRow(`SELECT power FROM managed_vm_observations WHERE resource_id=?`, id).Scan(&power)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return power != "started" && power != "stopped", nil
}

func queueBudgets(jobs []queueJob) (int64, []int64, bool) {
	parts := []int64{}
	for _, job := range jobs {
		spec, err := DecodeJob(job.spec)
		if err != nil {
			return 0, nil, true
		}
		active, ok := activeDiagnosticBudgets(spec)
		if !ok {
			return 0, nil, true
		}
		parts = append(parts, active...)
	}
	return int64(len(jobs)), parts, false
}

func activeDiagnosticBudgets(job JobSpec) ([]int64, bool) {
	base := []int64{job.Deadlines.PrepareSeconds, job.Deadlines.CollectSeconds, job.Deadlines.CleanupSeconds}
	for _, value := range base {
		if value <= 0 {
			return nil, false
		}
	}
	switch job.Mode {
	case "auto":
		if job.Deadlines.RunSeconds <= 0 {
			return nil, false
		}
		return append(base, job.Deadlines.RunSeconds), true
	case "manual":
		if job.Deadlines.ManualSeconds <= 0 {
			return nil, false
		}
		return append(base, job.Deadlines.ManualSeconds), true
	default:
		return nil, false
	}
}

func queueCeilings(spec ResourceSpec) (string, DiagnosticCeilings) {
	if spec.Schema == "haepari.resource/v2" && spec.LifecycleBudgets != nil {
		budgets := spec.LifecycleBudgets
		return "lifecycle_budgets", DiagnosticCeilings{
			QueueSeconds: budgets.QueueSeconds, PrepareSeconds: budgets.PrepareSeconds, RunSeconds: budgets.RunSeconds,
			ManualSeconds: budgets.ManualSeconds, CollectSeconds: budgets.CollectSeconds, CleanupSeconds: budgets.CleanupSeconds,
		}
	}
	return "legacy_product_ceiling", DiagnosticCeilings{
		QueueSeconds: productPhaseCeiling, PrepareSeconds: productPhaseCeiling, RunSeconds: productPhaseCeiling,
		ManualSeconds: productPhaseCeiling, CollectSeconds: productPhaseCeiling, CleanupSeconds: productPhaseCeiling,
	}
}

// applyQueueTotals fills a bounded row, or sum_overflow with no partial sums.
// It returns false when the row is unknown because the checked sum overflowed.
func applyQueueTotals(row *DiagnosticQueueRow, source string, ceilings DiagnosticCeilings, included int64, parts []int64) bool {
	backlog, okBacklog := sumChecked(parts...)
	auto, okAuto := sumChecked(ceilings.PrepareSeconds, ceilings.RunSeconds, ceilings.CollectSeconds, ceilings.CleanupSeconds)
	manual, okManual := sumChecked(ceilings.PrepareSeconds, ceilings.ManualSeconds, ceilings.CollectSeconds, ceilings.CleanupSeconds)
	prospective := auto
	if manual > auto {
		prospective = manual
	}
	total, okTotal := addChecked(backlog, prospective)
	if !okBacklog || !okAuto || !okManual || !okTotal {
		row.Status = "unknown"
		row.Reason = "sum_overflow"
		row.CeilingSource = ""
		row.Ceilings = nil
		row.IncludedJobs = nil
		row.BacklogSeconds = nil
		row.ProspectiveCeilingSeconds = nil
		row.TotalSeconds = nil
		return false
	}
	row.Status = "bounded"
	row.CeilingSource = source
	copied := ceilings
	row.Ceilings = &copied
	row.IncludedJobs = diagnosticInt(included)
	row.BacklogSeconds = diagnosticInt(backlog)
	row.ProspectiveCeilingSeconds = diagnosticInt(prospective)
	row.TotalSeconds = diagnosticInt(total)
	return true
}

func addChecked(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}

func sumChecked(values ...int64) (int64, bool) {
	var total int64
	ok := true
	for _, value := range values {
		total, ok = addChecked(total, value)
		if !ok {
			return 0, false
		}
	}
	return total, true
}
