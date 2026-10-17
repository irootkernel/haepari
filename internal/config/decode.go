package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func Parse(data []byte) (Document, Presence, error) {
	var presence Presence
	if len(data) == 0 || len(data) > 1<<20 {
		return Document{}, presence, fmt.Errorf("%w: configuration document is empty or too large", ErrRefused)
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return Document{}, presence, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return Document{}, presence, fmt.Errorf("%w: configuration must be an object", ErrRefused)
	}
	allowed := map[string]bool{
		"schema": true, "revision": true, "global_limit": true, "host_macos_guest_limit": true,
		"default_deadlines": true, "limits": true,
	}
	for key, raw := range object {
		if !allowed[key] {
			return Document{}, presence, fmt.Errorf("%w: unknown field %q", ErrRefused, key)
		}
		if isNull(raw) {
			return Document{}, presence, fmt.Errorf("%w: null field %q", ErrRefused, key)
		}
	}
	if _, ok := object["schema"]; !ok {
		return Document{}, presence, fmt.Errorf("%w: schema is required", ErrRefused)
	}
	doc := Defaults()
	var schema string
	if err := json.Unmarshal(object["schema"], &schema); err != nil || schema != Schema {
		return Document{}, presence, fmt.Errorf("%w: schema must be %s", ErrRefused, Schema)
	}
	if raw, ok := object["revision"]; ok {
		value, err := parseInteger(raw)
		if err != nil {
			return Document{}, presence, err
		}
		doc.Revision = value
	}
	if raw, ok := object["global_limit"]; ok {
		value, err := parseInteger(raw)
		if err != nil {
			return Document{}, presence, err
		}
		doc.GlobalLimit = value
		presence.GlobalLimit = true
	}
	if raw, ok := object["host_macos_guest_limit"]; ok {
		value, err := parseInteger(raw)
		if err != nil {
			return Document{}, presence, err
		}
		doc.HostMACOSGuestLimit = value
		presence.HostLimit = true
	}
	if raw, ok := object["default_deadlines"]; ok {
		if err := parseFields(raw, map[string]*int64{
			"queue_seconds": &doc.DefaultDeadlines.QueueSeconds, "prepare_seconds": &doc.DefaultDeadlines.PrepareSeconds,
			"run_seconds": &doc.DefaultDeadlines.RunSeconds, "manual_seconds": &doc.DefaultDeadlines.ManualSeconds,
			"collect_seconds": &doc.DefaultDeadlines.CollectSeconds, "cleanup_seconds": &doc.DefaultDeadlines.CleanupSeconds,
		}); err != nil {
			return Document{}, presence, err
		}
	}
	if raw, ok := object["limits"]; ok {
		if err := parseFields(raw, map[string]*int64{
			"request_bytes": &doc.Limits.RequestBytes, "log_bytes_per_stream": &doc.Limits.LogBytesPerStream,
			"artifact_bytes": &doc.Limits.ArtifactBytes, "artifact_count": &doc.Limits.ArtifactCount,
			"event_retention_days": &doc.Limits.EventRetentionDays, "result_retention_days": &doc.Limits.ResultRetentionDays,
		}); err != nil {
			return Document{}, presence, err
		}
	}
	if err := doc.Validate(); err != nil {
		return Document{}, presence, err
	}
	return doc, presence, nil
}

func parseFields(raw []byte, fields map[string]*int64) error {
	if isNull(raw) {
		return fmt.Errorf("%w: null object", ErrRefused)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("%w: expected an object", ErrRefused)
	}
	for key, value := range object {
		dest, ok := fields[key]
		if !ok {
			return fmt.Errorf("%w: unknown field %q", ErrRefused, key)
		}
		if isNull(value) {
			return fmt.Errorf("%w: null field %q", ErrRefused, key)
		}
		parsed, err := parseInteger(value)
		if err != nil {
			return err
		}
		*dest = parsed
	}
	return nil
}

func parseInteger(raw []byte) (int64, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || strings.ContainsAny(text, ".eE+") || strings.HasPrefix(text, "\"") {
		return 0, fmt.Errorf("%w: expected an integer", ErrRefused)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return 0, fmt.Errorf("%w: integer overflow", ErrRefused)
		}
		return 0, fmt.Errorf("%w: expected an integer", ErrRefused)
	}
	return value, nil
}

func isNull(raw []byte) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanValue(decoder); err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON", ErrRefused)
	}
	return nil
}

func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return fmt.Errorf("duplicate object key")
			}
			seen[key] = true
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	_, err = decoder.Token()
	return err
}
