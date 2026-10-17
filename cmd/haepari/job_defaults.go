package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
)

// materializeActiveDeadlines splices only absent active phase keys into the
// raw deadlines object. Malformed documents are returned unchanged so the
// strict decoder still sees the original keys. fetch runs only when a fill
// is required.
func materializeActiveDeadlines(spec []byte, fetch func() (config.Deadlines, error)) ([]byte, bool, error) {
	if managed.ValidateUniqueJSONKeys(spec) != nil {
		return spec, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(spec))
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil {
		return spec, false, nil
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return spec, false, nil
	}
	var mode string
	var sawMode, sawDeadlines bool
	var deadlinesAt, deadlinesLen int
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return spec, false, nil
		}
		key, ok := keyToken.(string)
		if !ok {
			return spec, false, nil
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return spec, false, nil
		}
		end := int(dec.InputOffset())
		switch key {
		case "mode":
			if json.Unmarshal(raw, &mode) != nil {
				return spec, false, nil
			}
			sawMode = true
		case "deadlines":
			start := end - len(raw)
			if start < 0 || !bytes.Equal(spec[start:end], raw) {
				return spec, false, nil
			}
			deadlinesAt = start
			deadlinesLen = len(raw)
			sawDeadlines = true
		}
	}
	if !sawMode || (mode != "auto" && mode != "manual") || !sawDeadlines {
		return spec, false, nil
	}
	deadlines := spec[deadlinesAt : deadlinesAt+deadlinesLen]
	present, object := deadlineKeys(deadlines)
	if !object {
		return spec, false, nil
	}
	active := []string{"queue_seconds", "prepare_seconds", "run_seconds", "collect_seconds", "cleanup_seconds"}
	if mode == "manual" {
		active = []string{"queue_seconds", "prepare_seconds", "manual_seconds", "collect_seconds", "cleanup_seconds"}
	}
	var missing []string
	for _, key := range active {
		if !present[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return spec, false, nil
	}
	defaults, err := fetch()
	if err != nil {
		return nil, false, err
	}
	values := map[string]int64{
		"queue_seconds": defaults.QueueSeconds, "prepare_seconds": defaults.PrepareSeconds,
		"run_seconds": defaults.RunSeconds, "manual_seconds": defaults.ManualSeconds,
		"collect_seconds": defaults.CollectSeconds, "cleanup_seconds": defaults.CleanupSeconds,
	}
	if len(deadlines) < 2 || deadlines[0] != '{' || deadlines[len(deadlines)-1] != '}' {
		return spec, false, nil
	}
	empty := len(bytes.TrimSpace(deadlines[1:len(deadlines)-1])) == 0
	insertion := make([]byte, 0, 64*len(missing))
	for i, key := range missing {
		value := values[key]
		if value < 1 {
			return nil, false, fmt.Errorf("accepted configuration deadline %s is not positive", key)
		}
		if !empty || i > 0 {
			insertion = append(insertion, ',')
		}
		insertion = append(insertion, fmt.Sprintf("%q:%d", key, value)...)
	}
	rewritten := append(append(append([]byte{}, deadlines[:len(deadlines)-1]...), insertion...), '}')
	out := make([]byte, 0, len(spec)-len(deadlines)+len(rewritten))
	out = append(out, spec[:deadlinesAt]...)
	out = append(out, rewritten...)
	out = append(out, spec[deadlinesAt+len(deadlines):]...)
	return out, true, nil
}

func deadlineKeys(raw []byte) (map[string]bool, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil {
		return nil, false
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, false
	}
	present := map[string]bool{}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, false
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, false
		}
		present[key] = true
	}
	token, err = dec.Token()
	if err != nil || token != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return present, true
}
