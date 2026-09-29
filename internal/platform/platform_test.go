package platform

import "testing"

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name, os, arch, backend string
		wantErr                 bool
	}{
		{"supported", "darwin", "arm64", "local", false},
		{"linux", "linux", "arm64", "local", true},
		{"intel", "darwin", "amd64", "local", true},
		{"unavailable VM", "darwin", "arm64", "utm-macos", true},
		{"unknown backend", "darwin", "arm64", "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.os, tc.arch, tc.backend)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate(%q, %q, %q) = %v", tc.os, tc.arch, tc.backend, err)
			}
		})
	}
}
