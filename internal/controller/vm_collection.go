package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
	"golang.org/x/sys/unix"
)

// A failed SSH observer is not a stopped collector. Read its exact durable
// worker receipt before deciding whether incomplete collection can settle.
func (s *runtimeServer) vmWorkerStopped(q runner.GuestRequest) bool {
	q.Operation = "state"
	q.Files = nil
	q.Inventory = nil
	q.Offset = 0
	q.Path = ""
	q.Deadline = time.Now().Add(5 * time.Second)
	reply, err := s.vmCall(q)
	return err == nil && reply.Control != nil && reply.Control.Stopped
}
func (s *runtimeServer) collectVMFile(q runner.GuestRequest, name, target string, deadline time.Time, remaining int64) (managed.ResultArtifact, error) {
	return s.collectVMFileUsing(q, name, target, deadline, remaining, s.observeVMWithRetry)
}
func (s *runtimeServer) collectVMFileUsing(q runner.GuestRequest, name, target string, deadline time.Time, remaining int64, call func(runner.GuestRequest) (runner.GuestReply, error)) (managed.ResultArtifact, error) {
	result := managed.ResultArtifact{Path: name}
	q.Operation = "file"
	q.Path = name
	q.Offset = 0
	q.ExpectedFile = nil
	q.Deadline = deadline
	first, err := call(q)
	if err != nil {
		return result, err
	}
	if first.Absent {
		return result, errOutputAbsent
	}
	if first.File == nil || first.File.Size > remaining {
		return result, managed.ErrInvalid
	}
	metadata := *first.File
	q.ExpectedFile = &metadata
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return result, err
	}
	expected := managed.ResultArtifact{Path: name, SizeBytes: metadata.Size, SHA256: metadata.SHA256}
	syncParents := func() error {
		observeRetentionBoundary("guest-copy-before-directory-sync")
		if !time.Now().Before(deadline) {
			return runner.ErrUnproven
		}
		stop := filepath.Join(s.root.Path, "jobs", q.Request.Identity.JobID)
		var e error
		if s.vmSyncParents != nil {
			e = s.vmSyncParents(target, stop)
		} else {
			e = syncVMParentsUntil(target, stop, deadline)
		}
		if e == nil && !time.Now().Before(deadline) {
			e = runner.ErrUnproven
		}
		return e
	}
	if old, e := os.OpenFile(target, os.O_RDONLY|unix.O_NOFOLLOW, 0); e == nil {
		defer old.Close()
		if _, e = privateLogStat(int(old.Fd()), false); e != nil {
			return result, e
		}
		artifact, e := checkedArtifactUntil(old, &expected, name, deadline)
		if e != nil {
			return result, e
		}
		if !time.Now().Before(deadline) {
			return result, runner.ErrUnproven
		}
		if e = old.Sync(); e != nil {
			return result, e
		}
		if e = syncParents(); e != nil {
			return result, e
		}
		return artifact, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return result, e
	}
	nonce, err := randomID(".guest-copy-")
	if err != nil {
		return result, err
	}
	parent, err := unix.Open(filepath.Dir(target), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return result, err
	}
	defer unix.Close(parent)
	if _, err = privateLogStat(parent, true); err != nil {
		return result, err
	}
	fd, err := unix.Openat(parent, nonce, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return result, err
	}
	defer unix.Unlinkat(parent, nonce, 0)
	f := os.NewFile(uintptr(fd), nonce)
	defer f.Close()
	h := sha256.New()
	reply := first
	for {
		if !time.Now().Before(deadline) {
			return result, runner.ErrUnproven
		}
		if reply.File == nil || !reflect.DeepEqual(metadata, *reply.File) || reply.NextOffset > metadata.Size {
			return result, managed.ErrBlocked
		}
		write := f.Write
		if s.vmWrite != nil {
			write = func(data []byte) (int, error) { return s.vmWrite(f, data) }
		}
		if n, e := write(reply.Output); e != nil || n != len(reply.Output) {
			err = e
			if err == nil {
				err = io.ErrShortWrite
			}
			return result, err
		}
		h.Write(reply.Output)
		q.Offset = reply.NextOffset
		if q.Offset == metadata.Size {
			break
		}
		if len(reply.Output) == 0 {
			return result, managed.ErrBlocked
		}
		reply, err = call(q)
		if err != nil {
			return result, err
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != metadata.SHA256 {
		return result, managed.ErrBlocked
	}
	if !time.Now().Before(deadline) {
		return result, runner.ErrUnproven
	}
	if err = f.Sync(); err != nil {
		return result, err
	}
	if !time.Now().Before(deadline) {
		return result, runner.ErrUnproven
	}
	if err = platform.PublishExclusive(parent, nonce, filepath.Base(target)); err != nil {
		return result, err
	}
	observeRetentionBoundary("guest-copy-published")
	if err = syncParents(); err != nil {
		return result, err
	}
	result.SizeBytes = metadata.Size
	result.SHA256 = metadata.SHA256
	return result, nil
}
func syncVMParents(name, stop string) error {
	return syncVMParentsUntil(name, stop, time.Time{})
}
func syncVMParentsUntil(name, stop string, deadline time.Time) error {
	if name == stop || !strings.HasPrefix(filepath.Clean(name), filepath.Clean(stop)+string(filepath.Separator)) {
		return managed.ErrInvalid
	}
	for dir := filepath.Dir(name); ; dir = filepath.Dir(dir) {
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return runner.ErrUnproven
		}
		f, err := os.OpenFile(dir, os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
		if dir == stop {
			return nil
		}
	}
}
func (s *runtimeServer) collectVM(q runner.GuestRequest, required []string, destination string, deadline time.Time) collectionAttempt {
	a := collectionAttempt{complete: true}
	if len(required) > 1000 {
		return collectionAttempt{}
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		if !errors.Is(err, os.ErrExist) || privateVMDirectory(destination) != nil {
			return collectionAttempt{}
		}
	}
	var total int64
	for _, name := range required {
		if !time.Now().Before(deadline) {
			a.complete = false
			a.expired = true
			break
		}
		result, err := s.collectVMFile(q, name, filepath.Join(destination, filepath.FromSlash(name)), deadline, (1<<30)-total)
		if err != nil {
			a.complete = false
			if errors.Is(err, errOutputAbsent) {
				a.absent = append(a.absent, name)
			}
			if errors.Is(err, utm.ErrGuestUncertain) && !s.vmWorkerStopped(q) {
				a.unproven = true
				return a
			}
			if !time.Now().Before(deadline) {
				a.expired = true
				return a
			}
			continue
		}
		a.artifacts = append(a.artifacts, result)
		total += result.SizeBytes
	}
	return a
}
func (s *runtimeServer) cleanupVM(q runner.GuestRequest, deadline time.Time) error {
	// An earlier successful deletion may have lost its reply. Observe it rather
	// than repeating an effect against an absent or recreated Job directory.
	health := q
	health.Operation = "health"
	health.Deadline = deadline
	if reply, err := s.vmCall(health); err == nil && reply.Reusable {
		return nil
	}
	q.Operation = "cleanup"
	q.Deadline = deadline
	if _, err := s.vmCall(q); err != nil {
		return err
	}
	health.Deadline = deadline
	reply, err := s.vmCall(health)
	if err != nil {
		return err
	}
	if !reply.Reusable {
		return managed.ErrBlocked
	}
	return nil
}
func (s *runtimeServer) settleVMPhases(c *localRunControl, job managed.JobSpec, q runner.GuestRequest, out managed.LocalOutcome) error {
	jobRoot := filepath.Join(s.root.Path, "jobs", c.identity.JobID)
	// Restart consumes durable collection and cleanup outcomes; deadlines are
	// never renewed for an existing phase.
	collect, exists := c.phases["collect"]
	if !exists {
		if err := s.beginPhase(c, "collect", job.Deadlines.CollectSeconds, time.Now().Add(time.Duration(job.Deadlines.CollectSeconds)*time.Second)); err != nil {
			return err
		}
		collect = c.phases["collect"]
	}
	if !collect.Completed || collect.Outcome == "unproven" {
		required := job.Workload.RequiredOutputs
		if !out.Started {
			required = nil
		}
		collected, status := boundedPhaseWithWorker(collect.Deadline, s.detached, func(work func()) bool { return s.startJobWorker(c.identity.JobID, work) }, func() collectionAttempt {
			return s.collectVM(q, required, filepath.Join(jobRoot, "artifacts"), collect.Deadline)
		})
		logs, gaps, err := s.ledger.LocalLogManifest(c.identity.JobID)
		if err != nil {
			return err
		}
		if out.Started && len(logs) == 0 {
			collected.complete = false
			gaps = append(gaps, managed.EvidenceGap{Path: "output.log", Reason: "native_log_manifest_missing"})
		}
		if len(logs) > 0 {
			log, err := s.collectVMFile(vmLogRequest(q), "output.log", filepath.Join(jobRoot, "output.log"), collect.Deadline, 256<<20)
			if err != nil || log != logs[0] {
				collected.complete = false
				gaps = append(gaps, managed.EvidenceGap{Path: "output.log", Reason: "guest_log_uncollected"})
				if errors.Is(err, utm.ErrGuestUncertain) && !s.vmWorkerStopped(q) {
					collected.unproven = true
				}
			} else {
				f, err := os.OpenFile(filepath.Join(jobRoot, "output.log"), os.O_RDONLY|unix.O_NOFOLLOW, 0)
				if err != nil {
					return err
				}
				err = s.bindLocalLog(c.receipt, c.identity.RunID, f)
				f.Close()
				if err != nil {
					return err
				}
			}
		}
		if len(gaps) > 0 {
			collected.complete = false
		}
		if status != phaseInTime || collected.expired || collected.unproven {
			collected.complete = false
		}
		present := map[string]bool{}
		for _, a := range collected.artifacts {
			present[a.Path] = true
		}
		for _, name := range required {
			if !present[name] {
				gaps = append(gaps, managed.EvidenceGap{Path: name, Reason: outputGapReason(job, name, collected.absent)})
			}
		}
		evidence := managed.EvidenceCompleteness("complete")
		phaseOutcome := "complete"
		protected := []managed.ProtectedEvidence{}
		if !collected.complete {
			evidence = "partial"
			phaseOutcome = "failed"
			if !absenceAllowsCleanup(job, gaps, collected, status) {
				protected = append(protected, managed.ProtectedEvidence{Path: "workspace", Reason: "guest_evidence_retained", RecoveryReference: "job:" + c.identity.JobID})
			}
		}
		if status == phaseLate || collected.expired {
			phaseOutcome = "late"
			evidence = "partial"
		}
		if status == phaseUnproven || collected.unproven {
			phaseOutcome = "unproven"
			evidence = "partial"
		}
		phase, err := s.manualCollectionPhase(job, c.identity.JobID, managed.LocalPhase{Outcome: phaseOutcome, ProtectedEvidence: protected, Artifacts: collected.artifacts, Logs: logs, EvidenceGaps: gaps, EvidenceCompleteness: evidence})
		if err != nil {
			return err
		}
		evidence, gaps, phaseOutcome = phase.EvidenceCompleteness, phase.EvidenceGaps, phase.Outcome
		if err = s.endPhase(c, "collect", phase); err != nil {
			return err
		}
		if phaseOutcome == "unproven" {
			return runner.ErrUnproven
		}
		collect = c.phases["collect"]
	}
	if c.receipt.JobState == "collecting" {
		if err := s.advancePhase(c, "collecting", "cleaning", "collected"); err != nil {
			return err
		}
	}
	cleanup, exists := c.phases["cleanup"]
	if !exists {
		if err := s.beginPhase(c, "cleanup", job.Deadlines.CleanupSeconds, time.Now().Add(time.Duration(job.Deadlines.CleanupSeconds)*time.Second)); err != nil {
			return err
		}
		cleanup = c.phases["cleanup"]
	}
	if !cleanup.Completed || cleanup.Outcome == "unproven" {
		outcome := "complete"
		protected := collect.ProtectedEvidence
		if !collectionAllowsCleanup(job, collect) {
			outcome = "failed"
		} else {
			err, status := boundedPhaseWithWorker(cleanup.Deadline, s.detached, func(work func()) bool { return s.startJobWorker(c.identity.JobID, work) }, func() error {
				if err := s.verifyVMStored(c.identity.JobID, collect, cleanup.Deadline); err != nil {
					return err
				}
				return s.cleanupVM(q, cleanup.Deadline)
			})
			if status == phaseUnproven || errors.Is(err, utm.ErrGuestUncertain) && !s.vmWorkerStopped(q) {
				outcome = "unproven"
			} else if status == phaseLate {
				outcome = "late"
			} else if err != nil {
				outcome = "failed"
			}
			if outcome != "complete" {
				protected = []managed.ProtectedEvidence{{Path: "workspace", Reason: "guest_cleanup_incomplete", RecoveryReference: "job:" + c.identity.JobID}}
			}
		}
		if err := s.endPhase(c, "cleanup", managed.LocalPhase{Outcome: outcome, ProtectedEvidence: protected}); err != nil {
			return err
		}
		if outcome == "unproven" {
			return runner.ErrUnproven
		}
		cleanup = c.phases["cleanup"]
	}
	if c.receipt.JobState == "cleaning" {
		if err := s.advancePhase(c, "cleaning", "settling", "cleaned"); err != nil {
			return err
		}
	}
	result := managed.Result{WorkloadOutcome: out.WorkloadOutcome, NativeExitCode: out.NativeExitCode, NativeSignal: out.NativeSignal, EvidenceCompleteness: collect.EvidenceCompleteness, CleanupOutcome: "verified", ResourceAvailability: "available", Artifacts: collect.Artifacts, Logs: collect.Logs, EvidenceGaps: collect.EvidenceGaps, ProtectedEvidence: cleanup.ProtectedEvidence}
	if !out.Started {
		result.Reason = "process_start_failed"
		if out.WorkloadOutcome == "cancelled" {
			result.Reason = "cancel_before_start"
		}
	}
	if c.manual {
		m, err := s.ledger.ManualSession(c.identity.JobID)
		if err != nil {
			return err
		}
		if m.AbortReason != "" {
			result.Reason = m.AbortReason
		}
	}
	if cleanup.Outcome != "complete" {
		result.CleanupOutcome = "failed"
		result.ResourceAvailability = "quarantined"
		result.Reason = "guest_evidence_retained"
	}
	return s.verifyLocalResult(c, result, false)
}

func vmLogRequest(q runner.GuestRequest) runner.GuestRequest { q.Area = "log"; return q }
func (s *runtimeServer) verifyVMStored(jobID string, collect managed.LocalPhase, deadline time.Time) error {
	for _, a := range collect.Artifacts {
		f, err := openOwnedEvidence(s.root.Path, jobID, "artifacts/"+a.Path)
		if err != nil {
			return err
		}
		_, err = checkedArtifactUntil(f, &a, a.Path, deadline)
		f.Close()
		if err != nil {
			return err
		}
	}
	for _, a := range collect.Logs {
		f, err := openOwnedEvidence(s.root.Path, jobID, "output.log")
		if err != nil {
			return err
		}
		_, err = checkedArtifactUntil(f, &a, a.Path, deadline)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *runtimeServer) collectVMLog(q runner.GuestRequest, deadline time.Time) error {
	logs, gaps, err := s.ledger.LocalLogManifest(q.Request.Identity.JobID)
	if err != nil {
		return err
	}
	if len(gaps) > 0 {
		return managed.ErrBlocked
	}
	if len(logs) == 0 {
		return nil
	}
	result, err := s.collectVMFile(vmLogRequest(q), "output.log", filepath.Join(s.root.Path, "jobs", q.Request.Identity.JobID, "output.log"), deadline, 256<<20)
	if err != nil {
		return err
	}
	if result != logs[0] {
		return managed.ErrBlocked
	}
	f, err := os.OpenFile(filepath.Join(s.root.Path, "jobs", q.Request.Identity.JobID, "output.log"), os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	receipt, err := s.ledger.Snapshot(q.Request.Identity.JobID)
	if err != nil {
		return err
	}
	return s.bindLocalLog(receipt, q.Request.Identity.RunID, f)
}

func privateVMDirectory(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = privateLogStat(int(f.Fd()), true)
	return err
}
