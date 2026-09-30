package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
)

// Advisory commands use exact field spelling as well as closed shapes. The
// resource document itself is validated by its discriminated contract parser.
func decodeLockBody(r *http.Request, target any) error {
	defer r.Body.Close()
	const limit = 1<<20 + 4096
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || len(raw) > limit {
		return managed.ErrInvalid
	}
	return decodeLockJSON(raw, target)
}

func decodeLockJSON(raw []byte, target any) error {
	if err := managed.ValidateUniqueJSONKeys(raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return managed.ErrInvalid
	}
	typ := reflect.TypeOf(target).Elem()
	allowed := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		allowed[strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	for key, value := range fields {
		if !allowed[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return managed.ErrInvalid
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(target) != nil {
		return managed.ErrInvalid
	}
	var tail any
	if dec.Decode(&tail) != io.EOF {
		return managed.ErrInvalid
	}
	return nil
}
