package main

import (
	"bytes"
	"encoding/json"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task023M2aHelp", ginkgo.Label("unit"), func() {
	ginkgo.It("appends the implemented schemas and keeps the existing limit numbers", func() {
		var out, diag bytes.Buffer
		gomega.Expect(run([]string{"help", "--json"}, &out, &diag)).To(gomega.Equal(0))
		var response client.Envelope
		gomega.Expect(json.Unmarshal(out.Bytes(), &response)).To(gomega.Succeed())
		var help struct {
			Commands         []commandHelp    `json:"commands"`
			Schemas          []string         `json:"schemas"`
			Limits           map[string]int64 `json:"limits"`
			Canonicalization string           `json:"canonicalization"`
		}
		gomega.Expect(json.Unmarshal(response.Data, &help)).To(gomega.Succeed())
		gomega.Expect(help.Schemas).To(gomega.ContainElement("haepari.resource/v2"))
		gomega.Expect(help.Schemas).To(gomega.ContainElement("haepari.operator-projection/v1"))
		gomega.Expect(help.Schemas).NotTo(gomega.ContainElement("haepari.config/v1"))
		gomega.Expect(help.Limits["log_bytes"]).To(gomega.Equal(int64(256 << 20)))
		gomega.Expect(help.Limits["artifact_bytes_per_collection"]).To(gomega.Equal(int64(1 << 30)))
		gomega.Expect(help.Limits["artifacts_per_collection"]).To(gomega.Equal(int64(1000)))
		gomega.Expect(help.Commands).To(gomega.ContainElement(commandHelp{"submit", "submit --spec <json> --request-key <key> --json; omitted active phase budgets come from the accepted configuration"}))
		gomega.Expect(help.Commands).To(gomega.ContainElement(commandHelp{"run", "run --spec <json> --request-key <key> [--json]; omitted active phase budgets come from the accepted configuration"}))
		gomega.Expect(help.Commands).To(gomega.ContainElement(commandHelp{"daemon run", "daemon run [--backend local] [--root <absolute-private-root>] [--config <private-file>] [--host-macos-guest-limit 1|2] [--global-limit <positive-integer>]"}))
		gomega.Expect(help.Canonicalization).To(gomega.ContainSubstring("haepari.resource/v2 lifecycle budgets are explicit ceilings"))
	})
})
