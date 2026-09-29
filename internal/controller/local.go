package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
)

func (s *runtimeServer) execute(_ context.Context, receipt managed.Receipt, job managed.JobSpec, objectID string) {
	if err := s.executeLocal(receipt, job, objectID); err != nil {
		s.failed(receipt.JobID, err)
	}
	s.notifyScheduler()
}

func (s *runtimeServer) executeLocal(receipt managed.Receipt, job managed.JobSpec, objectID string) error {
	if receipt.JobState != "preparing" {
		return managed.ErrConflict
	}
	jobRoot := filepath.Join(s.root.Path, "jobs", receipt.JobID)
	workspace := filepath.Join(jobRoot, "workspace")
	if err := os.Mkdir(jobRoot, 0700); err != nil {
		return err
	}
	if err := writeReceipt(jobRoot, "request.json", map[string]any{"job_id": receipt.JobID, "snapshot_object_id": objectID, "lease_generation": receipt.LeaseGeneration}); err != nil {
		return err
	}
	snap, err := s.store.Read(objectID)
	if err != nil {
		return err
	}
	if err := copyInput(snap, workspace); err != nil {
		return err
	}
	receipt, err = s.ledger.AdvanceLocal(receipt.JobID, receipt.JobRevision, "preparing", "ready", "prepared-"+receipt.JobID)
	if err != nil {
		return err
	}
	runID, err := randomID("run-")
	if err != nil {
		return err
	}
	receipt, err = s.ledger.StartLocal(receipt.JobID, receipt.JobRevision, runID)
	if err != nil {
		return err
	}
	if err := writeReceipt(jobRoot, "intent.json", map[string]any{"run_id": runID, "job_id": receipt.JobID, "lease_generation": receipt.LeaseGeneration}); err != nil {
		return err
	}

	logFile, err := os.OpenFile(filepath.Join(jobRoot, "output.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	runCtx, cancel := context.WithTimeout(context.Background(), time.Duration(job.Deadlines.RunSeconds)*time.Second)
	defer cancel()
	cmd := exec.Command(job.Workload.Argv[0], job.Workload.Argv[1:]...)
	cmd.Dir = filepath.Join(workspace, filepath.FromSlash(job.Workload.CWD))
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	keys := make([]string, 0, len(job.Workload.Environment))
	for key := range job.Workload.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		cmd.Env = append(cmd.Env, key+"="+job.Workload.Environment[key])
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	started := false
	startErr := cmd.Start()
	if startErr == nil {
		started = true
		if err := writeReceipt(jobRoot, "start.json", map[string]any{"run_id": runID, "pid": cmd.Process.Pid, "started_at": time.Now().UTC()}); err != nil {
			// Keep tracking the child even if the receipt fails. Its state stays
			// nonterminal and no second run may be claimed.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return err
		}
	}
	var waitErr error
	timedOut := false
	if started {
		ended := make(chan error, 1)
		go func() { ended <- cmd.Wait() }()
		select {
		case waitErr = <-ended:
		case <-runCtx.Done():
			timedOut = true
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case waitErr = <-ended:
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				waitErr = <-ended
			}
		}
	} else {
		waitErr = startErr
	}
	if started {
		if err := ensureGroupStopped(cmd.Process.Pid, time.Duration(job.Deadlines.CleanupSeconds)*time.Second); err != nil {
			return err
		}
	}
	if err := logFile.Sync(); err != nil {
		return err
	}
	code := 0
	var nativeCode *int
	signal := ""
	if started && cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
		if code >= 0 && code <= 255 {
			nativeCode = &code
		}
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			signal = status.Signal().String()
			nativeCode = nil
		}
	}
	if err := writeReceipt(jobRoot, "exit.json", map[string]any{"run_id": runID, "started": started, "exit_code": nativeCode, "signal": signal, "timed_out": timedOut, "wait_error": waitErr != nil}); err != nil {
		return err
	}
	receipt, err = s.ledger.AdvanceLocal(receipt.JobID, receipt.JobRevision, "running", "collecting", "exited-"+receipt.JobID)
	if err != nil {
		return err
	}
	collectDeadline := time.Now().Add(time.Duration(job.Deadlines.CollectSeconds) * time.Second)
	collected, collectionStatus := boundedPhase(collectDeadline, func() collectionAttempt {
		artifacts, complete, expired := collectOutputs(workspace, filepath.Join(jobRoot, "artifacts"), job.Workload.RequiredOutputs, collectDeadline)
		return collectionAttempt{artifacts: artifacts, complete: complete, expired: expired}
	})
	if collectionStatus == phaseUnproven {
		// The collector may still hold a filesystem operation. Reconciliation
		// retains occupancy and quarantines the resource; cleanup is unsafe.
		return errors.New("collector termination unproven after deadline")
	}
	if collectionStatus == phaseLate {
		collected.complete = false
		collected.expired = true
	}
	artifacts, complete, collectionExpired := collected.artifacts, collected.complete, collected.expired
	if err := writeReceipt(jobRoot, "collection.json", map[string]any{"complete": complete, "deadline_expired": collectionExpired, "artifacts": artifacts}); err != nil {
		return err
	}
	receipt, err = s.ledger.AdvanceLocal(receipt.JobID, receipt.JobRevision, "collecting", "cleaning", "collected-"+receipt.JobID)
	if err != nil {
		return err
	}
	var cleanupErr error
	protected := []managed.ProtectedEvidence{}
	if complete {
		cleanupDeadline := time.Now().Add(time.Duration(job.Deadlines.CleanupSeconds) * time.Second)
		var cleanupStatus phaseStatus
		cleanupErr, cleanupStatus = boundedPhase(cleanupDeadline, func() error {
			return removeTreeUntil(workspace, cleanupDeadline)
		})
		if cleanupStatus == phaseUnproven {
			// A blocked unlink may still be running. Do not claim cleanup or
			// settle while an owned filesystem effect remains unobserved.
			return errors.New("cleanup termination unproven after deadline")
		}
		if cleanupStatus == phaseLate && cleanupErr == nil {
			// Removal completed, but a late final syscall is not verified
			// cleanup. The workspace is gone, so it is not protected evidence.
			cleanupErr = context.DeadlineExceeded
		} else if cleanupErr != nil {
			protected = append(protected, managed.ProtectedEvidence{Path: "workspace", Reason: "cleanup_incomplete", RecoveryReference: "job:" + receipt.JobID})
		}
	} else {
		cleanupErr = errors.New("required output collection incomplete; workspace protected")
		reason := "required_output_uncollected"
		if collectionExpired {
			reason = "collection_deadline"
		}
		protected = append(protected, managed.ProtectedEvidence{Path: "workspace", Reason: reason, RecoveryReference: "job:" + receipt.JobID})
	}
	cleanup := "verified"
	availability := "available"
	if cleanupErr != nil {
		cleanup = "failed"
		availability = "quarantined"
	}
	if err := writeReceipt(jobRoot, "cleanup.json", map[string]any{"outcome": cleanup, "error": cleanupErr != nil, "protected_evidence": protected}); err != nil {
		return err
	}
	receipt, err = s.ledger.AdvanceLocal(receipt.JobID, receipt.JobRevision, "cleaning", "settling", "cleaned-"+receipt.JobID)
	if err != nil {
		return err
	}
	outcome := managed.WorkloadOutcome("inconclusive")
	if timedOut {
		outcome = "timed_out"
	} else if started && waitErr == nil {
		outcome = "pass"
	} else if started {
		outcome = "fail"
	}
	evidence := managed.EvidenceCompleteness("complete")
	if !complete {
		evidence = "partial"
	}
	verdict, err := managed.DeriveVerdict(managed.ResultFacts{WorkloadOutcome: outcome, EvidenceCompleteness: evidence, CleanupOutcome: managed.CleanupOutcome(cleanup), ManualEligible: true, TerminationProven: true})
	if err != nil {
		return err
	}
	result := managed.Result{Schema: "haepari.result/v1", JobID: receipt.JobID, ResourceID: receipt.ResourceID, ResourceRevision: receipt.ResourceConfigRevision, LeaseGeneration: receipt.LeaseGeneration, JobState: "finished", ResourceAvailability: availability, WorkloadOutcome: outcome, EvidenceCompleteness: evidence, CleanupOutcome: managed.CleanupOutcome(cleanup), FinalVerdict: verdict, NativeExitCode: nativeCode, NativeSignal: signal, Artifacts: artifacts, ProtectedEvidence: protected}
	if !started {
		result.Reason = "process_start_failed"
	} else if timedOut {
		result.Reason = "run_deadline"
	} else if !complete {
		result.Reason = "required_output_missing"
		if collectionExpired {
			result.Reason = "collection_deadline"
		}
	} else if errors.Is(cleanupErr, context.DeadlineExceeded) {
		result.Reason = "cleanup_deadline"
	}
	if _, err := s.ledger.SettleLocal(receipt.JobID, receipt.JobRevision, result); err != nil {
		return err
	}
	s.signalFinished(receipt.JobID)
	return nil
}

type collectionAttempt struct {
	artifacts []managed.ResultArtifact
	complete  bool
	expired   bool
}

type phaseStatus uint8

const (
	phaseInTime phaseStatus = iota
	phaseLate
	phaseUnproven
)

type phaseCompletion[T any] struct {
	value      T
	finishedAt time.Time
}

// boundedPhase bounds controller observation, not an uninterruptible kernel
// call. On timeout the caller must retain ownership and reconcile the worker.
// A completed operation carries its actual finish time so scheduler delay or
// a timer race cannot turn late cleanup into verified cleanup.
func boundedPhase[T any](deadline time.Time, work func() T) (T, phaseStatus) {
	done := make(chan phaseCompletion[T], 1)
	go func() {
		value := work()
		done <- phaseCompletion[T]{value: value, finishedAt: time.Now()}
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case result := <-done:
		return result.value, completionStatus(result.finishedAt, deadline)
	case <-timer.C:
		select {
		case result := <-done:
			return result.value, completionStatus(result.finishedAt, deadline)
		default:
			var zero T
			return zero, phaseUnproven
		}
	}
}

func completionStatus(finishedAt, deadline time.Time) phaseStatus {
	if !finishedAt.Before(deadline) {
		return phaseLate
	}
	return phaseInTime
}

func copyInput(snap snapshot.Snapshot, destination string) error {
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	for _, entry := range snap.Manifest.Entries {
		name := filepath.FromSlash(entry.Path)
		target := filepath.Join(destination, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		input, err := os.Open(filepath.Join(snap.Root, name))
		if err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if entry.Executable {
			mode = 0700
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeIn := input.Close()
		syncErr := output.Sync()
		closeOut := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeIn != nil {
			return closeIn
		}
		if syncErr != nil {
			return syncErr
		}
		if closeOut != nil {
			return closeOut
		}
	}
	return nil
}

func ensureGroupStopped(pgid int, budget time.Duration) error {
	if pgid <= 0 {
		return errors.New("missing process group identity")
	}
	if budget <= 0 {
		return errors.New("missing cleanup budget")
	}
	deadline := time.Now().Add(budget)
	termAt := time.Now()
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	killed := false
	for time.Now().Before(deadline) {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("inspect process group: %w", err)
		}
		if !killed && time.Since(termAt) >= 2*time.Second {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			killed = true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("owned process group termination unproven")
}

func openContainedOutput(root, name string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return nil, nil, managed.ErrInvalid
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			// A FIFO with no writer would block before Fstat without this flag.
			flags |= unix.O_NONBLOCK
		}
		next, openErr := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		if openErr != nil {
			return nil, nil, openErr
		}
		fd = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return nil, nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		unix.Close(fd)
		return nil, nil, managed.ErrInvalid
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func collectOutputs(workspace, artifactRoot string, required []string, deadline time.Time) ([]managed.ResultArtifact, bool, bool) {
	if err := os.Mkdir(artifactRoot, 0700); err != nil {
		return nil, false, false
	}
	artifacts := make([]managed.ResultArtifact, 0, len(required))
	complete := true
	for _, name := range required {
		if !time.Now().Before(deadline) {
			return artifacts, false, true
		}
		input, info, err := openContainedOutput(workspace, name)
		if err != nil {
			complete = false
			continue
		}
		target := filepath.Join(artifactRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			input.Close()
			complete = false
			continue
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			input.Close()
			complete = false
			continue
		}
		h := sha256.New()
		size, copyErr := copyUntil(io.MultiWriter(output, h), input, deadline)
		after, statErr := input.Stat()
		closeIn := input.Close()
		syncErr := output.Sync()
		closeOut := output.Close()
		if copyErr != nil || statErr != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || closeIn != nil || syncErr != nil || closeOut != nil || size != info.Size() {
			complete = false
			_ = os.Remove(target)
			if errors.Is(copyErr, context.DeadlineExceeded) || !time.Now().Before(deadline) {
				return artifacts, false, true
			}
			continue
		}
		artifacts = append(artifacts, managed.ResultArtifact{Path: name, SizeBytes: size, SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	if !time.Now().Before(deadline) {
		return artifacts, false, true
	}
	return artifacts, complete, false
}

func copyUntil(dst io.Writer, src io.Reader, deadline time.Time) (int64, error) {
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if !time.Now().Before(deadline) {
			return total, context.DeadlineExceeded
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

// removeTreeUntil never follows workload-created links. A deadline or error
// retains the remaining workspace and keeps its resource quarantined.
func removeTreeUntil(root string, deadline time.Time) error {
	parent, err := unix.Open(filepath.Dir(root), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	return removeEntryUntil(parent, filepath.Base(root), deadline)
}

func removeEntryUntil(parent int, name string, deadline time.Time) error {
	if !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(parent, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.Unlinkat(parent, name, 0)
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), name)
	defer dir.Close()
	for {
		if !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		entries, readErr := dir.ReadDir(64)
		for _, entry := range entries {
			if err := removeEntryUntil(fd, entry.Name(), deadline); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
}

func writeReceipt(root, name string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(root, ".receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), filepath.Join(root, name)); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
