package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/irootkernel/haepari/internal/managed"
	"golang.org/x/sys/unix"
)

const logPageBytes = 64 << 10

var observerJobID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var errLogMissing = errors.New("owned job output not created or retained")

type outputIdentity struct {
	Schema string `json:"schema"`
	JobID  string `json:"job_id"`
	RunID  string `json:"run_id"`
	Dev    uint64 `json:"dev"`
	Inode  uint64 `json:"inode"`
}

func (s *runtimeServer) bindLocalLog(receipt managed.Receipt, runID string, file *os.File) error {
	if file == nil || !observerJobID.MatchString(runID) || receipt.LeaseGeneration <= 0 {
		return managed.ErrInvalid
	}
	observation, err := s.ledger.LogObservation(receipt.JobID)
	if err != nil {
		return err
	}
	if observation.RunID != runID || observation.Snapshot.LeaseGeneration != receipt.LeaseGeneration {
		return managed.ErrStale
	}
	st, err := privateLogStat(int(file.Fd()), false)
	if err != nil {
		return err
	}
	fd, err := openPrivateJob(s.root.Path, receipt.JobID)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ownedFD, err := unix.Openat(fd, "output.log", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	owned, err := privateLogStat(ownedFD, false)
	unix.Close(ownedFD)
	if err != nil {
		return err
	}
	if owned.Dev != st.Dev || owned.Ino != st.Ino {
		return managed.ErrInvalid
	}
	binding := outputIdentity{Schema: "haepari.output-identity/v1", JobID: receipt.JobID, RunID: runID, Dev: uint64(st.Dev), Inode: st.Ino}
	prior, err := readOutputIdentity(fd)
	if err == nil {
		if prior != binding {
			return managed.ErrConflict
		}
		return nil
	}
	if !errors.Is(err, errLogMissing) {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return writeReceipt(filepath.Join(s.root.Path, "jobs", receipt.JobID), "output-identity.json", binding)
}

type logGap struct {
	Reason           string `json:"reason"`
	RequestedOffset  int64  `json:"requested_offset"`
	ResumeOffset     int64  `json:"resume_offset"`
	PreviousIdentity string `json:"previous_identity,omitempty"`
}

type logPage struct {
	JobID      string          `json:"job_id"`
	Identity   string          `json:"identity"`
	Offset     int64           `json:"offset"`
	NextOffset int64           `json:"next_offset"`
	Size       int64           `json:"size"`
	Data       []byte          `json:"data"`
	More       bool            `json:"more"`
	Snapshot   managed.Receipt `json:"snapshot"`
	Gap        *logGap         `json:"gap,omitempty"`
}

// Keep the last file open across follow reads: its inode cannot be reused while
// comparing a replacement. Reads still reopen the contained path to observe
// replacement or removal, rather than following a detached file silently.
type logReader struct {
	root           string
	jobID          string
	file           *os.File
	identity       string
	lastSize       int64
	beforeGateWait func() // deterministic publication-race barrier; nil in production
}

func (l *logReader) close() {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

func privateLogStat(fd int, directory bool) (unix.Stat_t, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return st, err
	}
	want := uint32(unix.S_IFREG)
	if directory {
		want = unix.S_IFDIR
	}
	if uint32(st.Mode)&unix.S_IFMT != want || st.Mode&0077 != 0 || st.Uid != uint32(os.Geteuid()) || (!directory && st.Nlink != 1) {
		return st, managed.ErrInvalid
	}
	return st, nil
}

func openPrivateJob(root, jobID string) (int, error) {
	if !observerJobID.MatchString(jobID) {
		return -1, managed.ErrInvalid
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if _, err := privateLogStat(fd, true); err != nil {
		unix.Close(fd)
		return -1, err
	}
	for _, part := range []string{"jobs", jobID} {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			if part == jobID && errors.Is(err, unix.ENOENT) {
				return -1, errLogMissing
			}
			return -1, err
		}
		fd = next
		if _, err := privateLogStat(fd, true); err != nil {
			unix.Close(fd)
			return -1, err
		}
	}
	return fd, nil
}

func readOutputIdentity(jobFD int) (outputIdentity, error) {
	var identity outputIdentity
	fd, err := unix.Openat(jobFD, "output-identity.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return identity, errLogMissing
		}
		return identity, err
	}
	file := os.NewFile(uintptr(fd), "output-identity.json")
	defer file.Close()
	if _, err := privateLogStat(fd, false); err != nil {
		return identity, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return identity, err
	}
	if len(data) > 4096 {
		return identity, fmt.Errorf("owned output identity exceeds receipt bound")
	}
	if err := managed.ValidateUniqueJSONKeys(data); err != nil {
		return identity, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil {
		return identity, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return identity, fmt.Errorf("trailing output identity receipt")
	}
	if identity.Schema != "haepari.output-identity/v1" || !observerJobID.MatchString(identity.JobID) || !observerJobID.MatchString(identity.RunID) || identity.Inode == 0 {
		return identity, fmt.Errorf("invalid owned output identity")
	}
	return identity, nil
}

func openPrivateLog(root, jobID, runID string) (*os.File, unix.Stat_t, bool, error) {
	fd, err := openPrivateJob(root, jobID)
	if err != nil {
		return nil, unix.Stat_t{}, false, err
	}
	defer unix.Close(fd)
	binding, bindingErr := readOutputIdentity(fd)
	if bindingErr != nil && !errors.Is(bindingErr, errLogMissing) {
		return nil, unix.Stat_t{}, false, bindingErr
	}
	next, err := unix.Openat(fd, "output.log", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, unix.Stat_t{}, bindingErr == nil, errLogMissing
		}
		return nil, unix.Stat_t{}, bindingErr == nil, err
	}
	st, err := privateLogStat(next, false)
	if err != nil {
		unix.Close(next)
		return nil, st, bindingErr == nil, err
	}
	if errors.Is(bindingErr, errLogMissing) {
		unix.Close(next)
		return nil, st, false, fmt.Errorf("owned output binding is missing")
	}
	if bindingErr != nil || binding.JobID != jobID || binding.RunID != runID || runID == "" || binding.Dev != uint64(st.Dev) || binding.Inode != st.Ino {
		unix.Close(next)
		return nil, st, bindingErr == nil, fmt.Errorf("owned output log lost its durable run or file binding")
	}
	return os.NewFile(uintptr(next), "output.log"), st, true, nil
}

func missingLogAllowed(o managed.LogObservation) bool {
	if o.Outcome != nil && o.Outcome.Started {
		return false
	}
	if o.Outcome != nil && !o.Outcome.Started && o.TerminationProven {
		return true
	}
	if o.RunID != "" {
		return false
	}
	if o.Snapshot.JobState == "queued" || o.Snapshot.JobState == "preparing" || o.Snapshot.JobState == "ready" {
		return true
	}
	if o.Snapshot.JobState != "finished" || o.Result == nil || (o.Result.WorkloadOutcome != "cancelled" && o.Result.WorkloadOutcome != "inconclusive") || o.Result.NativeExitCode != nil || o.Result.NativeSignal != "" {
		return false
	}
	switch o.Result.Reason {
	case "cancelled", "queue_expired", "resource_configuration_changed", "sealed_input_unavailable", "prepare_failed", "prepare_deadline":
		return true
	}
	return false
}

func (l *logReader) read(after int64, identity string, observation managed.LogObservation) (logPage, error) {
	p := logPage{JobID: l.jobID, Offset: after, NextOffset: after, Data: []byte{}, Snapshot: observation.Snapshot}
	if after < 0 || observation.Snapshot.JobID != l.jobID {
		return p, managed.ErrInvalid
	}
	f, st, accepted, err := openPrivateLog(l.root, l.jobID, observation.RunID)
	if err != nil {
		if !errors.Is(err, errLogMissing) {
			return p, err
		}
		// Only this live reader's prior verified file proves an observed removal.
		if l.file != nil && l.identity != "" {
			p.Gap = &logGap{Reason: "missing", RequestedOffset: after, PreviousIdentity: identity}
			p.Offset, p.NextOffset = 0, 0
		} else if accepted || after != 0 || identity != "" || !missingLogAllowed(observation) {
			return p, fmt.Errorf("accepted or possibly started output log is missing")
		}
		l.close()
		l.identity, l.lastSize = "", 0
		return p, nil
	}
	p.Identity = fmt.Sprintf("%x:%x", st.Dev, st.Ino)
	p.Size = st.Size
	reason := ""
	if identity != "" && identity != p.Identity {
		reason = "replaced"
	} else if after > st.Size || (l.identity == p.Identity && st.Size < l.lastSize) {
		reason = "truncated"
	}
	l.close()
	l.file, l.identity, l.lastSize = f, p.Identity, st.Size
	if reason != "" {
		p.Gap = &logGap{Reason: reason, RequestedOffset: after, PreviousIdentity: identity}
		p.Offset, p.NextOffset, p.More = 0, 0, st.Size > 0
		return p, nil
	}
	n := min(int64(logPageBytes), st.Size-after)
	p.Data = make([]byte, int(n))
	read, err := f.ReadAt(p.Data, after)
	if err != nil && !errors.Is(err, io.EOF) {
		return p, err
	}
	if int64(read) != n {
		// Truncation raced the fixed-size read. Do not return a partial page
		// whose checkpoint falsely promises the original observed range.
		p.Data = []byte{}
		p.Gap = &logGap{Reason: "truncated", RequestedOffset: after, PreviousIdentity: identity}
		p.Offset, p.NextOffset = 0, 0
		var current unix.Stat_t
		if err := unix.Fstat(int(f.Fd()), &current); err != nil {
			return p, err
		}
		p.Size, l.lastSize = current.Size, current.Size
		p.More = p.Size > 0
		return p, nil
	}
	p.NextOffset += int64(read)
	p.More = p.NextOffset < p.Size
	if !p.More && observation.Result != nil && len(observation.Result.EvidenceGaps) > 0 {
		for _, gap := range observation.Result.EvidenceGaps {
			if gap.Path == "output.log" {
				p.Gap = &logGap{Reason: gap.Reason, RequestedOffset: after, ResumeOffset: p.NextOffset, PreviousIdentity: p.Identity}
				break
			}
		}
	}
	return p, nil
}
