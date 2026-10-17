package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

const Schema = "haepari.config/v1"

// Resource-protection caps. A larger file value is refused, never clamped.
// LogBytesPerStream is one combined stdout/stderr bucket, not a per-stream
// allocation and not twice this size.
const (
	MaxRequestBytes  int64 = 1 << 20
	MaxLogBytes      int64 = 256 << 20
	MaxArtifactBytes int64 = 1 << 30
	MaxArtifactCount int64 = 1000
)

var ErrRefused = errors.New("configuration refused")

type Deadlines struct {
	QueueSeconds   int64 `json:"queue_seconds"`
	PrepareSeconds int64 `json:"prepare_seconds"`
	RunSeconds     int64 `json:"run_seconds"`
	ManualSeconds  int64 `json:"manual_seconds"`
	CollectSeconds int64 `json:"collect_seconds"`
	CleanupSeconds int64 `json:"cleanup_seconds"`
}

type Limits struct {
	RequestBytes        int64 `json:"request_bytes"`
	LogBytesPerStream   int64 `json:"log_bytes_per_stream"`
	ArtifactBytes       int64 `json:"artifact_bytes"`
	ArtifactCount       int64 `json:"artifact_count"`
	EventRetentionDays  int64 `json:"event_retention_days"`
	ResultRetentionDays int64 `json:"result_retention_days"`
}

type Document struct {
	Schema              string    `json:"schema"`
	Revision            int64     `json:"revision"`
	GlobalLimit         int64     `json:"global_limit"`
	HostMACOSGuestLimit int64     `json:"host_macos_guest_limit"`
	DefaultDeadlines    Deadlines `json:"default_deadlines"`
	Limits              Limits    `json:"limits"`
}

type Presence struct {
	GlobalLimit bool
	HostLimit   bool
}

func Defaults() Document {
	return Document{
		Schema:              Schema,
		Revision:            1,
		GlobalLimit:         1,
		HostMACOSGuestLimit: 1,
		DefaultDeadlines: Deadlines{
			QueueSeconds: 14400, PrepareSeconds: 900, RunSeconds: 3600,
			ManualSeconds: 7200, CollectSeconds: 300, CleanupSeconds: 900,
		},
		Limits: Limits{
			RequestBytes: MaxRequestBytes, LogBytesPerStream: MaxLogBytes,
			ArtifactBytes: MaxArtifactBytes, ArtifactCount: MaxArtifactCount,
			EventRetentionDays: 30, ResultRetentionDays: 90,
		},
	}
}

func (d Document) Validate() error {
	if d.Schema != Schema {
		return fmt.Errorf("%w: schema must be %s", ErrRefused, Schema)
	}
	if d.Revision < 1 {
		return fmt.Errorf("%w: revision must be positive", ErrRefused)
	}
	if d.GlobalLimit < 1 || d.GlobalLimit > int64(math.MaxInt) {
		return fmt.Errorf("%w: global_limit must be positive", ErrRefused)
	}
	if d.HostMACOSGuestLimit < 1 || d.HostMACOSGuestLimit > 2 {
		return fmt.Errorf("%w: host_macos_guest_limit must be 1 or 2", ErrRefused)
	}
	for _, value := range []int64{
		d.DefaultDeadlines.QueueSeconds, d.DefaultDeadlines.PrepareSeconds, d.DefaultDeadlines.RunSeconds,
		d.DefaultDeadlines.ManualSeconds, d.DefaultDeadlines.CollectSeconds, d.DefaultDeadlines.CleanupSeconds,
	} {
		if value < 1 {
			return fmt.Errorf("%w: deadlines must be positive", ErrRefused)
		}
	}
	if d.Limits.RequestBytes < 1 || d.Limits.RequestBytes > MaxRequestBytes {
		return fmt.Errorf("%w: request_bytes must be positive and at most %d", ErrRefused, MaxRequestBytes)
	}
	if d.Limits.LogBytesPerStream < 1 || d.Limits.LogBytesPerStream > MaxLogBytes {
		return fmt.Errorf("%w: log_bytes_per_stream must be positive and at most %d", ErrRefused, MaxLogBytes)
	}
	if d.Limits.ArtifactBytes < 1 || d.Limits.ArtifactBytes > MaxArtifactBytes {
		return fmt.Errorf("%w: artifact_bytes must be positive and at most %d", ErrRefused, MaxArtifactBytes)
	}
	if d.Limits.ArtifactCount < 1 || d.Limits.ArtifactCount > MaxArtifactCount {
		return fmt.Errorf("%w: artifact_count must be positive and at most %d", ErrRefused, MaxArtifactCount)
	}
	if d.Limits.EventRetentionDays < 1 || d.Limits.ResultRetentionDays < 1 {
		return fmt.Errorf("%w: retention days must be positive", ErrRefused)
	}
	return nil
}

func (d Document) Canonical() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(d)
}

func (d Document) Digests() (raw []byte, documentDigest, valuesDigest string, err error) {
	raw, err = d.Canonical()
	if err != nil {
		return nil, "", "", err
	}
	values, err := json.Marshal(struct {
		Schema              string    `json:"schema"`
		GlobalLimit         int64     `json:"global_limit"`
		HostMACOSGuestLimit int64     `json:"host_macos_guest_limit"`
		DefaultDeadlines    Deadlines `json:"default_deadlines"`
		Limits              Limits    `json:"limits"`
	}{d.Schema, d.GlobalLimit, d.HostMACOSGuestLimit, d.DefaultDeadlines, d.Limits})
	if err != nil {
		return nil, "", "", err
	}
	return raw, digest(raw), digest(values), nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
