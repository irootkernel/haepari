package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"golang.org/x/sys/unix"
)

const artifactPageBytes = 64 << 10

func checkedArtifact(file *os.File, expected *managed.ResultArtifact, name string) (managed.ResultArtifact, error) {
	return checkedArtifactUntil(file, expected, name, time.Now().Add(30*time.Second))
}

func checkedArtifactUntil(file *os.File, expected *managed.ResultArtifact, name string, deadline time.Time) (managed.ResultArtifact, error) {
	var result managed.ResultArtifact
	if !time.Now().Before(deadline) {
		return result, context.DeadlineExceeded
	}
	before, err := file.Stat()
	if err != nil {
		return result, err
	}
	if before.Size() < 0 || before.Size() > 1<<30 {
		return result, fmt.Errorf("artifact exceeds delivery bound")
	}
	if expected != nil && expected.SizeBytes != before.Size() {
		return result, fmt.Errorf("artifact size changed")
	}
	h := sha256.New()
	size, err := copyUntil(h, io.LimitReader(file, before.Size()+1), deadline)
	if err != nil {
		return result, err
	}
	after, err := file.Stat()
	if err != nil {
		return result, err
	}
	result = managed.ResultArtifact{Path: name, SizeBytes: size, SHA256: hex.EncodeToString(h.Sum(nil))}
	if !os.SameFile(before, after) || before.Size() != size || after.Size() != size || !before.ModTime().Equal(after.ModTime()) || expected != nil && expected.SHA256 != result.SHA256 {
		return result, fmt.Errorf("artifact integrity changed")
	}
	_, err = file.Seek(0, io.SeekStart)
	return result, err
}

func (s *runtimeServer) artifactManifest(id string) (managed.ArtifactManifest, error) {
	m := managed.ArtifactManifest{Schema: "haepari.artifact-manifest/v1", JobID: id, Files: []managed.DeliveryFile{}}
	r, err := s.ledger.Snapshot(id)
	if err != nil {
		return m, err
	}
	if r.ResultStatus == "unavailable" {
		return m, managed.ErrResultUnavailable
	}
	if r.JobState != "finished" {
		return m, managed.ErrBusy
	}
	m.Result, err = s.ledger.Result(id)
	if err != nil {
		return m, err
	}
	m.Supplementary, err = s.ledger.PublicSupplementaryEvidence(id)
	if err != nil {
		return m, err
	}
	private, err := s.ledger.SupplementaryEvidence(id)
	if err != nil {
		return m, err
	}
	count := len(m.Result.Artifacts)
	for _, record := range private {
		count += len(record.Artifacts) + len(record.GuestArtifacts)
	}
	if count > 3000 {
		return m, managed.ErrBlocked
	}
	for _, expected := range m.Result.Artifacts {
		f, err := openOwnedEvidence(s.root.Path, id, "artifacts/"+expected.Path)
		if err != nil {
			return m, err
		}
		entry, err := checkedArtifact(f, &expected, "artifacts/"+expected.Path)
		delivery, tokenErr := s.bindArtifactPage(id, entry, f)
		f.Close()
		if tokenErr != nil {
			return m, tokenErr
		}
		if err != nil {
			return m, err
		}
		m.Files = append(m.Files, delivery)
	}
	for _, record := range private {
		for _, set := range []struct {
			root  string
			files []managed.ResultArtifact
		}{{record.ArtifactRoot, record.Artifacts}, {record.ArtifactRoot + "-native", record.GuestArtifacts}} {
			for _, a := range set.files {
				name := set.root + "/" + a.Path
				f, err := openOwnedEvidence(s.root.Path, id, name)
				if err != nil {
					return m, err
				}
				entry, err := checkedArtifact(f, &a, name)
				delivery, tokenErr := s.bindArtifactPage(id, entry, f)
				f.Close()
				if err != nil {
					return m, err
				}
				if tokenErr != nil {
					return m, tokenErr
				}
				m.Files = append(m.Files, delivery)
			}
		}
	}
	if len(m.Files) > 3000 {
		return m, managed.ErrBlocked
	}
	o, err := s.ledger.LogObservation(id)
	if err != nil {
		return m, err
	}
	if !missingLogAllowed(o) {
		f, _, _, err := openPrivateLog(s.root.Path, id, o.RunID)
		if err != nil {
			if err == errLogMissing && supplementaryGuestLog(m, private) {
				return m, nil
			}
			return m, err
		}
		var expected *managed.ResultArtifact
		if len(m.Result.Logs) > 0 {
			expected = &m.Result.Logs[0]
		}
		entry, err := checkedArtifact(f, expected, "output.log")
		delivery, tokenErr := s.bindArtifactPage(id, entry, f)
		f.Close()
		if tokenErr != nil {
			return m, tokenErr
		}
		if err != nil {
			return m, err
		}
		m.Files = append(m.Files, delivery)
	}
	return m, nil
}

// A declared original collection gap stays in the immutable result. A
// separately verified guest log may be delivered under its supplementary path;
// it does not impersonate the original host log or excuse a lost accepted log.
func supplementaryGuestLog(m managed.ArtifactManifest, private []managed.EvidenceRecoveryOutcome) bool {
	declared := false
	for _, gap := range m.Result.EvidenceGaps {
		declared = declared || gap.Path == "output.log" && gap.Reason == "guest_log_uncollected"
	}
	if !declared {
		return false
	}
	for _, record := range private {
		for _, file := range record.GuestArtifacts {
			if file.Path == "output.log" {
				return true
			}
		}
	}
	return false
}

func (s *runtimeServer) artifacts(w http.ResponseWriter, r *http.Request) {
	s.evidenceMu.RLock()
	defer s.evidenceMu.RUnlock()
	values, err := artifactQuery(r)
	if err != nil {
		reject(w, "artifacts", err)
		return
	}
	id := r.PathValue("id")
	name := values["path"]
	if name == "" {
		m, err := s.artifactManifest(id)
		if err != nil {
			reject(w, "artifacts", err)
			return
		}
		send(w, 200, "artifacts", m, nil)
		return
	}
	rc, err := s.ledger.Snapshot(id)
	if err != nil {
		reject(w, "artifacts", err)
		return
	}
	if rc.ResultStatus == "unavailable" {
		reject(w, "artifacts", managed.ErrResultUnavailable)
		return
	}
	if rc.JobState != "finished" {
		reject(w, "artifacts", managed.ErrBusy)
		return
	}
	token, err := s.readRecoveryToken(values["page_token"])
	if err != nil {
		reject(w, "artifacts", err)
		return
	}
	size, _ := strconv.ParseInt(values["size"], 10, 64)
	expected := managed.ResultArtifact{Path: name, SizeBytes: size, SHA256: values["sha256"]}
	if token.Installation != s.installationID() || token.JobID != id || token.Action != "artifact:"+name || !time.Now().Before(token.ExpiresAt) || token.SnapshotDigest != managed.RecoveryDigest(expected) {
		reject(w, "artifacts", managed.ErrStale)
		return
	}
	after, _ := strconv.ParseInt(values["after"], 10, 64)
	if after > size {
		reject(w, "artifacts", managed.ErrInvalid)
		return
	}
	var file *os.File
	if name == "output.log" {
		o, e := s.ledger.LogObservation(id)
		if e != nil {
			reject(w, "artifacts", e)
			return
		}
		file, _, _, err = openPrivateLog(s.root.Path, id, o.RunID)
	} else {
		file, err = openOwnedEvidence(s.root.Path, id, name)
	}
	if err != nil {
		reject(w, "artifacts", err)
		return
	}
	defer file.Close()
	if facts, e := artifactFileFacts(file); e != nil || managed.RecoveryDigest(facts) != token.ObservationDigest {
		reject(w, "artifacts", managed.ErrConflict)
		return
	}
	p := managed.ArtifactPage{Schema: "haepari.artifact-page/v1", JobID: id, File: expected, Offset: after, NextOffset: after, Data: []byte{}}
	p.Data = make([]byte, min(int64(artifactPageBytes), expected.SizeBytes-after))
	if len(p.Data) > 0 {
		if err := r.Context().Err(); err != nil {
			reject(w, "artifacts", err)
			return
		}
		n, err := file.ReadAt(p.Data, after)
		if err != nil || n != len(p.Data) {
			reject(w, "artifacts", fmt.Errorf("artifact read failed: %w", err))
			return
		}
	}
	p.NextOffset += int64(len(p.Data))
	p.More = p.NextOffset < expected.SizeBytes
	send(w, 200, "artifacts", p, nil)
}

func artifactQuery(r *http.Request) (map[string]string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, managed.ErrInvalid
	}
	if err = r.Context().Err(); err != nil {
		return nil, err
	}
	result := map[string]string{}
	for k, v := range values {
		if len(v) != 1 || k != "path" && k != "after" && k != "sha256" && k != "size" && k != "page_token" {
			return nil, managed.ErrInvalid
		}
		result[k] = v[0]
	}
	if result["path"] == "" {
		if len(result) != 0 {
			return nil, managed.ErrInvalid
		}
		return result, nil
	}
	if !managed.ValidArtifactPath(result["path"]) || result["sha256"] == "" {
		return nil, managed.ErrInvalid
	}
	n, err := strconv.ParseInt(result["after"], 10, 64)
	if err != nil || n < 0 {
		return nil, managed.ErrInvalid
	}
	size, err := strconv.ParseInt(result["size"], 10, 64)
	if err != nil || size < 0 || size > 1<<30 || len(result["page_token"]) > 4096 {
		return nil, managed.ErrInvalid
	}
	return result, nil
}

func openOwnedEvidence(root, id, name string) (*os.File, error) {
	if !managed.ValidArtifactPath(name) {
		return nil, managed.ErrInvalid
	}
	fd, err := openPrivateJob(root, id)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		directory := i < len(parts)-1
		if directory {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
		if _, err = privateLogStat(fd, directory); err != nil {
			unix.Close(fd)
			return nil, err
		}
	}
	return os.NewFile(uintptr(fd), name), nil
}

// This fingerprint is a file-incarnation fence, not a substitute for the
// manifest's full content checksum. Export verifies the complete streamed hash.
func artifactFileFacts(f *os.File) (any, error) {
	st, err := privateLogStat(int(f.Fd()), false)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return struct {
		Device   uint64
		Inode    uint64
		Size     int64
		Modified int64
	}{uint64(st.Dev), st.Ino, info.Size(), info.ModTime().UnixNano()}, nil
}

func (s *runtimeServer) bindArtifactPage(id string, a managed.ResultArtifact, f *os.File) (managed.DeliveryFile, error) {
	d := managed.DeliveryFile{ResultArtifact: a}
	facts, err := artifactFileFacts(f)
	if err != nil {
		return d, err
	}
	d.PageToken = s.signRecoveryToken(recoveryToken{s.installationID(), id, "artifact:" + a.Path, managed.RecoveryDigest(a), managed.RecoveryDigest(facts), time.Now().UTC().Add(time.Hour), ""})
	return d, nil
}
