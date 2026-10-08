package runner

import (
	"encoding/json"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("GuestRecoveryWire", ginkgo.Label("unit"), func() {
	ginkgo.It("rejects missing, unknown, null, duplicate, case-aliased and oversized recovery fields", func() {
		raw, err := json.Marshal(GuestRecoveryRequest{Schema: "haepari.guest-recovery-request/v1", Operation: "probe"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = DecodeGuestRecoveryRequest(raw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		variants := []string{strings.Replace(string(raw), `"schema":`, `"Schema":`, 1), strings.Replace(string(raw), `"schema":"haepari.guest-recovery-request/v1",`, "", 1), strings.Replace(string(raw), `"schema":"haepari.guest-recovery-request/v1"`, `"schema":null`, 1), strings.Replace(string(raw), `"schema":`, `"unexpected":true,"schema":`, 1), strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1), string(raw) + "{}", strings.Repeat(" ", receiptLimit+1)}
		for _, v := range variants {
			_, err = DecodeGuestRecoveryRequest([]byte(v))
			gomega.Expect(err).To(gomega.HaveOccurred(), v[:min(len(v), 100)])
		}
	})
})
