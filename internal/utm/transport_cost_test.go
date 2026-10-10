package utm

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"io"
	"os"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// This measures archive and response bytes at SSHRunner, not network packets
// or throughput. The same operation sequence is retained for the candidate.
var _ = ginkgo.Describe("Task037TransportCost", ginkgo.Label("unit"), func() {
	ginkgo.It("records the same three-call cost without retransferring a prepared executable", func() {
		s, spec, q := sshFixture()
		s.Transport = true
		s.Runner = bytes.Repeat([]byte("x"), 8<<20)
		if path := os.Getenv("HAEPARI_COST_BINARY"); path != "" {
			var err error
			s.Runner, err = os.ReadFile(path)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		sum := sha256.Sum256(s.Runner)
		s.RunnerDigest = "sha256:" + hex.EncodeToString(sum[:])
		q.RunnerDigest = s.RunnerDigest
		q.Root = &managed.RetainedPath{Directory: true, Device: 1, Inode: 2}
		owner := q
		owner.Operation, owner.Challenge = "prepare", ""
		s.Binding = &runner.GuestTransportBinding{Schema: "haepari.guest-transport-binding/v1", Guest: owner, Root: *q.Root, Runner: managed.RetainedPath{Path: "haepari", Device: 1, Inode: 3}}
		cost := struct {
			Schema          string `json:"schema"`
			RunnerDigest    string `json:"runner_digest"`
			RunnerBytes     int    `json:"runner_bytes"`
			Calls           int    `json:"calls"`
			RunnerTransfers int    `json:"runner_transfers"`
			ArchiveBytes    int    `json:"archive_bytes"`
			ResponseBytes   int    `json:"response_bytes"`
		}{Schema: "haepari.fixture-transport-cost/v1", RunnerDigest: s.RunnerDigest, RunnerBytes: len(s.Runner)}
		s.Run = func(_ context.Context, _ string, args []string, input []byte) (CommandResult, error) {
			gomega.Expect(args[len(args)-1]).To(gomega.Equal(guestOwnedBootstrap))
			cost.Calls++
			cost.ArchiveBytes += len(input)
			archive := tar.NewReader(bytes.NewReader(input))
			var raw []byte
			for {
				h, err := archive.Next()
				if err == io.EOF {
					break
				}
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				if h.Name == "haepari" {
					cost.RunnerTransfers++
				}
				if h.Name == "request.json" {
					raw, err = io.ReadAll(archive)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
			}
			var request runner.GuestTransportRequest
			gomega.Expect(json.Unmarshal(raw, &request)).To(gomega.Succeed())
			gomega.Expect(request.Binding).To(gomega.Equal(s.Binding))
			legacy := fakeSSHReply(request.Guest)
			var envelope managed.Envelope
			gomega.Expect(json.Unmarshal([]byte(legacy.Stdout), &envelope)).To(gomega.Succeed())
			var guest runner.GuestReply
			gomega.Expect(json.Unmarshal(envelope.Data, &guest)).To(gomega.Succeed())
			body, _ := json.Marshal(runner.GuestTransportReply{Schema: "haepari.guest-transport-reply/v1", ProtocolDigest: runner.GuestTransportDigest(request), Guest: guest, Binding: request.Binding})
			encoded, _ := json.Marshal(managed.Envelope{Schema: "haepari.envelope/v1", Operation: "runner.guest-transport", OK: true, Data: body})
			cost.ResponseBytes += len(encoded)
			return CommandResult{Stdout: string(encoded)}, nil
		}
		for i := 0; i < 3; i++ {
			_, err := s.Call(context.Background(), spec, freshSSHLeases(), q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		gomega.Expect(cost.Calls).To(gomega.Equal(6))
		gomega.Expect(cost.RunnerTransfers).To(gomega.BeZero())
		gomega.Expect(cost.ArchiveBytes).To(gomega.BeNumerically("<", 64<<10))
		if path := os.Getenv("HAEPARI_CANDIDATE_COST_RECORD"); path != "" {
			data, err := json.MarshalIndent(cost, "", "  ")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.WriteFile(path, data, 0600)).To(gomega.Succeed())
		}
	})
	ginkgo.It("records the existing three-call archive cost", func() {
		s, spec, q := sshFixture()
		path := os.Getenv("HAEPARI_COST_BINARY")
		if path == "" {
			s.Runner = bytes.Repeat([]byte("x"), 8<<20)
		} else {
			var err error
			s.Runner, err = os.ReadFile(path)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		sum := sha256.Sum256(s.Runner)
		s.RunnerDigest = "sha256:" + hex.EncodeToString(sum[:])
		q.RunnerDigest = s.RunnerDigest
		cost := struct {
			Schema          string `json:"schema"`
			RunnerDigest    string `json:"runner_digest"`
			RunnerBytes     int    `json:"runner_bytes"`
			Calls           int    `json:"calls"`
			RunnerTransfers int    `json:"runner_transfers"`
			ArchiveBytes    int    `json:"archive_bytes"`
			ResponseBytes   int    `json:"response_bytes"`
		}{Schema: "haepari.fixture-transport-cost/v1", RunnerDigest: s.RunnerDigest, RunnerBytes: len(s.Runner)}
		s.Run = func(_ context.Context, _ string, _ []string, input []byte) (CommandResult, error) {
			cost.Calls++
			cost.ArchiveBytes += len(input)
			archive := tar.NewReader(bytes.NewReader(input))
			for {
				h, err := archive.Next()
				if err == io.EOF {
					break
				}
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				if h.Name == "haepari" {
					cost.RunnerTransfers++
				}
			}
			r := fakeSSHReply(decodeSSHInput(input))
			cost.ResponseBytes += len(r.Stdout)
			return r, nil
		}
		for i := 0; i < 3; i++ {
			_, err := s.Call(context.Background(), spec, freshSSHLeases(), q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		gomega.Expect(cost.Calls).To(gomega.Equal(6))
		gomega.Expect(cost.RunnerTransfers).To(gomega.Equal(6))
		gomega.Expect(cost.ArchiveBytes).To(gomega.BeNumerically(">=", 6*len(s.Runner)))
		if destination := os.Getenv("HAEPARI_COST_RECORD"); destination != "" {
			data, err := json.MarshalIndent(cost, "", "  ")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.WriteFile(destination, data, 0600)).To(gomega.Succeed())
		}
	})
})
