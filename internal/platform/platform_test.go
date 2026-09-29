package platform

import (
	"github.com/onsi/ginkgo/v2"
)

var _ = ginkgo.Describe("Validate", ginkgo.Label("unit"), func() {
	ginkgo.It("Validate", func() {
		t := ginkgo.GinkgoT()
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
			ginkgo.By(tc.name)
			func(t ginkgo.GinkgoTInterface) {
				err := Validate(tc.os, tc.arch, tc.backend)
				if (err != nil) != tc.wantErr {
					t.Fatalf("Validate(%q, %q, %q) = %v", tc.os, tc.arch, tc.backend, err)
				}
			}(t)
		}

	})
})
