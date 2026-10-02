package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
)

const DefaultLogBytes int64 = 256 << 20

type LogManifest struct {
	Artifact     managed.ResultArtifact `json:"artifact"`
	LimitBytes   int64                  `json:"limit_bytes"`
	DroppedBytes int64                  `json:"dropped_bytes"`
	Gap          string                 `json:"gap,omitempty"`
}

// Keep a bounded prefix while draining the workload's pipe. A limit never
// changes the workload's native exit, and the gap remains explicit evidence.
type boundedLog struct {
	destination io.Writer
	limit       int64
	written     int64
	dropped     int64
	digest      hash.Hash
	gap         string
}

func newBoundedLog(w io.Writer, limit int64) *boundedLog {
	if limit == 0 {
		limit = DefaultLogBytes
	}
	return &boundedLog{destination: w, limit: limit, digest: sha256.New()}
}

func (w *boundedLog) Write(p []byte) (int, error) {
	if w.gap == "log_write_failed" {
		w.dropped += int64(len(p))
		return len(p), nil
	}
	keep := int(min(int64(len(p)), w.limit-w.written))
	if keep > 0 {
		n, err := w.destination.Write(p[:keep])
		w.digest.Write(p[:n])
		w.written += int64(n)
		if err != nil || n != keep {
			w.gap = "log_write_failed"
			w.dropped += int64(len(p) - n)
			// Drain inherited writers even after a disk failure. Closing the
			// pipe here would turn evidence loss into a workload SIGPIPE.
			return len(p), nil
		}
	}
	if keep < len(p) {
		w.dropped += int64(len(p) - keep)
		w.gap = "log_limit"
	}
	return len(p), nil
}

func (w *boundedLog) manifest() LogManifest {
	return LogManifest{Artifact: managed.ResultArtifact{Path: "output.log", SizeBytes: w.written, SHA256: hex.EncodeToString(w.digest.Sum(nil))}, LimitBytes: w.limit, DroppedBytes: w.dropped, Gap: w.gap}
}

func validLogManifest(m LogManifest, q Request) bool {
	limit := q.LogLimitBytes
	if limit == 0 {
		limit = DefaultLogBytes
	}
	_, err := hex.DecodeString(m.Artifact.SHA256)
	return m.Artifact.Path == "output.log" && m.Artifact.SizeBytes >= 0 && m.Artifact.SizeBytes <= limit && m.LimitBytes == limit && m.DroppedBytes >= 0 && len(m.Artifact.SHA256) == 64 && m.Artifact.SHA256 == strings.ToLower(m.Artifact.SHA256) && err == nil && (m.Gap == "" && m.DroppedBytes == 0 || m.Gap == "log_limit" && m.DroppedBytes > 0 || m.Gap == "log_write_failed" || m.Gap == "log_read_failed")
}
