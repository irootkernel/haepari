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
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
)

func (s *runtimeServer) execute(_ context.Context, receipt managed.Receipt, job managed.JobSpec, objectID string, prepareDeadline time.Time) {
	status, err := s.ledger.ResourceStatus(receipt.ResourceID)
	if err == nil {
		if status.Kind == "utm-macos" {
			err = s.executeVM(receipt, job, objectID, prepareDeadline)
		} else {
			err = s.executeLocal(receipt, job, objectID, prepareDeadline)
		}
	}
	if err != nil {
		s.failed(receipt.JobID, err)
	}
	s.notifyScheduler()
}

var errPrepareUnproven = errors.New("preparation termination unproven after deadline")

func (s *runtimeServer) executeLocal(receipt managed.Receipt, job managed.JobSpec, objectID string, prepareDeadline time.Time) error {
	c, err := s.registerLocal(receipt)
	if err != nil {
		return err
	}
	defer s.unregisterLocal(c)
	return s.executeLocalControlled(c, job, objectID, prepareDeadline)
}

func (s *runtimeServer) executeLocalControlled(c *localRunControl, job managed.JobSpec, objectID string, prepareDeadline time.Time) error {
	jobRoot := filepath.Join(s.root.Path, "jobs", c.identity.JobID)
	workspace := filepath.Join(jobRoot, "workspace")
	if err := s.beginPhase(c, "prepare", job.Deadlines.PrepareSeconds, prepareDeadline); err != nil {
		return err
	}
	c.phase("preparing")
	c.mu.Lock()
	status, err := s.ledger.Cancellation(c.identity.JobID)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if status.Requested {
		c.mu.Unlock()
		if err := s.endPhase(c, "prepare", managed.LocalPhase{Outcome: "not_needed"}); err != nil {
			return err
		}
		return s.settleCancelledPreparation(c, job, jobRoot, workspace)
	}
	if c.receipt.JobState != "preparing" {
		c.mu.Unlock()
		return managed.ErrConflict
	}
	c.mu.Unlock()
	prepareErr := prepareOwnedLocal(s, prepareDeadline, func() error {
		if !time.Now().Before(prepareDeadline) {
			return context.DeadlineExceeded
		}
		c.mu.Lock()
		// Once the worker is authorized, only observing its completion permits
		// cleanup. Exclusive creation separately proves ownership of the path.
		c.effects = true
		c.mu.Unlock()
		if err := os.Mkdir(jobRoot, 0700); err != nil {
			return err
		}
		c.mu.Lock()
		c.ownsJobRoot = true
		c.mu.Unlock()
		if err := writeReceipt(jobRoot, "input.json", map[string]any{"identity": c.identity, "snapshot_object_id": objectID}); err != nil {
			return err
		}
		if c.beforeInputCopy != nil {
			if err := c.beforeInputCopy(); err != nil {
				return err
			}
		}
		snap, err := s.store.Read(objectID)
		if err != nil {
			return err
		}
		return copyInputWithCancel(snap, workspace, prepareDeadline, c.wake)
	})
	prepareOutcome := "complete"
	if errors.Is(prepareErr, errPrepareUnproven) {
		prepareOutcome = "unproven"
	} else if errors.Is(prepareErr, context.DeadlineExceeded) {
		prepareOutcome = "late"
	} else if prepareErr != nil {
		prepareOutcome = "failed"
	}
	if err := s.endPhase(c, "prepare", managed.LocalPhase{Outcome: prepareOutcome}); err != nil {
		return err
	}
	if errors.Is(prepareErr, errPrepareUnproven) {
		return prepareErr
	}
	if prepareErr != nil {
		reason := "prepare_failed"
		if errors.Is(prepareErr, context.DeadlineExceeded) {
			reason = "prepare_deadline"
		}
		return s.finishPreparation(c, job, jobRoot, workspace, reason)
	}
	c.mu.Lock()
	status, err = s.ledger.Cancellation(c.identity.JobID)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if status.Requested {
		c.mu.Unlock()
		return s.settleCancelledPreparation(c, job, jobRoot, workspace)
	}
	receipt, err := s.ledger.AdvanceLocal(c.identity.JobID, c.receipt.JobRevision, "preparing", "ready", "prepared-"+c.identity.JobID)
	if err == nil {
		c.receipt = receipt
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	c.phase("ready")
	select {
	case <-s.detached:
		return errControllerDetached
	default:
	}
	runID, err := randomID("run-")
	if err != nil {
		return err
	}
	c.mu.Lock()
	status, err = s.ledger.Cancellation(c.identity.JobID)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if status.Requested {
		c.mu.Unlock()
		return s.settleCancelledPreparation(c, job, jobRoot, workspace)
	}
	receipt, err = s.ledger.StartLocal(c.identity.JobID, c.receipt.JobRevision, runID)
	if err == nil {
		c.receipt = receipt
		c.identity.RunID = runID
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	started, logFile, err := s.runWithSupervisor(c, job, jobRoot, workspace, runID)
	if logFile != nil {
		defer logFile.Close()
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	// A cancellation committed after native observation changes only intent.
	receipt, err = s.ledger.CompleteLocalRun(managed.LocalTerminationRequest{Identity: c.identity, ExpectedRevision: c.receipt.JobRevision, MutationKey: "termination-" + c.identity.JobID})
	if err == nil {
		c.receipt = receipt
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := s.endPhase(c, "run", managed.LocalPhase{Outcome: "complete"}); err != nil {
		return err
	}
	frozen, err := s.ledger.LocalOutcome(c.identity.JobID)
	if err != nil {
		return err
	}
	reason := ""
	if !started {
		reason = "process_start_failed"
	}
	return s.settleLocalPhases(c, job, jobRoot, workspace, frozen, false, reason, logFile)
}

func prepareOwnedLocal(s *runtimeServer, deadline time.Time, work func() error) error {
	err, status := boundedOwnedPhase(s, deadline, work)
	if status == phaseUnproven {
		return errPrepareUnproven
	}
	if status == phaseLate {
		return context.DeadlineExceeded
	}
	return err
}

type collectionAttempt struct {
	artifacts []managed.ResultArtifact
	complete  bool
	expired   bool
	unproven  bool // remote filesystem owner could still be acting
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

func completionStatus(finishedAt, deadline time.Time) phaseStatus {
	if !finishedAt.Before(deadline) {
		return phaseLate
	}
	return phaseInTime
}

func copyInput(snap snapshot.Snapshot, destination string, deadline time.Time) error {
	return copyInputWithCancel(snap, destination, deadline, nil)
}

func copyInputWithCancel(snap snapshot.Snapshot, destination string, deadline time.Time, wake <-chan struct{}) error {
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	for _, entry := range snap.Manifest.Entries {
		select {
		case <-wake:
			return context.Canceled
		default:
		}
		if !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		name := filepath.FromSlash(entry.Path)
		target := filepath.Join(destination, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		input, info, err := openContainedOutput(snap.Root, entry.Path)
		if err != nil {
			return fmt.Errorf("%w: open %s: %v", snapshot.ErrCorrupt, entry.Path, err)
		}
		if (info.Mode().Perm()&0111 != 0) != entry.Executable {
			input.Close()
			return fmt.Errorf("%w: mode of %s", snapshot.ErrCorrupt, entry.Path)
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
		h := sha256.New()
		size, copyErr := copyUntil(io.MultiWriter(output, h), input, deadline)
		after, statErr := input.Stat()
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
		if statErr != nil || !os.SameFile(info, after) || after.Size() != entry.Size || (after.Mode().Perm()&0111 != 0) != entry.Executable {
			return fmt.Errorf("%w: changed source metadata of %s", snapshot.ErrCorrupt, entry.Path)
		}
		if size != entry.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("%w: copied bytes of %s", snapshot.ErrCorrupt, entry.Path)
		}
	}
	return nil
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
	return collectOutputsBounded(workspace, artifactRoot, required, deadline, 1<<30, 1000)
}

func collectOutputsBounded(workspace, artifactRoot string, required []string, deadline time.Time, byteLimit int64, countLimit int) ([]managed.ResultArtifact, bool, bool) {
	return collectOutputsWithCopy(workspace, artifactRoot, required, deadline, byteLimit, countLimit, copyUntil)
}

func collectOutputsWithCopy(workspace, artifactRoot string, required []string, deadline time.Time, byteLimit int64, countLimit int, copyFile func(io.Writer, io.Reader, time.Time) (int64, error)) ([]managed.ResultArtifact, bool, bool) {
	if byteLimit <= 0 || countLimit <= 0 || len(required) > countLimit {
		return nil, false, false
	}
	if err := os.Mkdir(artifactRoot, 0700); err != nil {
		return nil, false, false
	}
	artifacts := make([]managed.ResultArtifact, 0, len(required))
	complete := true
	var collectedBytes int64
	for _, name := range required {
		if !time.Now().Before(deadline) {
			return artifacts, false, true
		}
		input, info, err := openContainedOutput(workspace, name)
		if err != nil {
			complete = false
			continue
		}
		if info.Size() > byteLimit-collectedBytes {
			input.Close()
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
		// Read one extra byte to detect growth without allowing an unbounded copy.
		size, copyErr := copyFile(io.MultiWriter(output, h), io.LimitReader(input, info.Size()+1), deadline)
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
		collectedBytes += size
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

// Both startup and resume use the same no-symlink private-directory predicate.
type localReadinessRefusal struct{ cause string }

func (e *localReadinessRefusal) Error() string { return "Local prerequisites unavailable: " + e.cause }
func (e *localReadinessRefusal) Unwrap() error { return managed.ErrBlocked }

func inspectPrivateJobsRoot(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		cause := "the controller jobs directory could not be inspected."
		if errors.Is(err, os.ErrNotExist) {
			cause = "the controller jobs directory is missing."
		}
		return nil, &localReadinessRefusal{cause: cause}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, &localReadinessRefusal{cause: "the controller jobs path is a symlink; a real private directory is required."}
	}
	if !info.IsDir() {
		return nil, &localReadinessRefusal{cause: "the controller jobs path is not a directory."}
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, &localReadinessRefusal{cause: "the controller jobs directory permits group or other access; remove that access before retrying."}
	}
	return info, nil
}

func (s *runtimeServer) observeLocalReadiness(resourceID string) (string, string, error) {
	info, err := inspectPrivateJobsRoot(filepath.Join(s.root.Path, "jobs"))
	if err != nil {
		return "", "", err
	}
	ref, err := randomID("local-")
	if err != nil {
		return "", "", err
	}
	// Store the bound facts as the reference, so the durable digest remains
	// verifiable. The path is relative to the named controller installation.
	facts, err := json.Marshal(struct {
		Schema       string `json:"schema"`
		Installation string `json:"installation"`
		Resource     string `json:"resource"`
		Path         string `json:"path"`
		Mode         uint32 `json:"mode"`
		Modified     string `json:"modified"`
		Observed     string `json:"observed"`
		Outcome      string `json:"outcome"`
		Reference    string `json:"reference"`
	}{Schema: "local-readiness/v1", Installation: s.installationID(), Resource: resourceID, Path: "jobs", Mode: uint32(info.Mode().Perm()), Modified: info.ModTime().UTC().Format(time.RFC3339Nano), Observed: time.Now().UTC().Format(time.RFC3339Nano), Outcome: "ready", Reference: ref})
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(facts)
	return hex.EncodeToString(digest[:]), string(facts), nil
}
