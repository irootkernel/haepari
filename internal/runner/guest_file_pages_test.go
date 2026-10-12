package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("GuestFileCollectionPages", ginkgo.Label("unit"), func() {
	ginkgo.DescribeTable("collects a real 50 MiB file with bounded pages and an exact whole-file hash", func(schema string, pageBytes, expectedCalls int) {
		f, err := os.OpenFile(filepath.Join(ginkgo.GinkgoT().TempDir(), "output.log"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer f.Close()
		gomega.Expect(f.Truncate(50 << 20)).To(gomega.Succeed())
		q := GuestRequest{Schema: schema, Operation: "file", Area: "log", Path: "output.log", Deadline: time.Now().Add(time.Minute)}
		hash := sha256.New()
		calls := 0
		for q.Offset < 50<<20 {
			reply, err := readGuestFile(f, q, GuestReply{Schema: GuestReplySchema(q)})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(reply.Output).To(gomega.HaveLen(pageBytes))
			gomega.Expect(reply.NextOffset).To(gomega.Equal(q.Offset + int64(len(reply.Output))))
			if q.ExpectedFile != nil {
				gomega.Expect(reply.File).To(gomega.Equal(q.ExpectedFile))
			}
			// Exercise the actual nested reply and envelope bounds, including base64.
			raw, err := json.Marshal(reply)
			if schema == "haepari.guest-request/v2" {
				raw, err = marshalGuestFileReply("runner.guest", &reply, &reply)
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = DecodeGuestReply(raw)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			wire, err := json.Marshal(managed.Envelope{Schema: "haepari.envelope/v1", Operation: "runner.guest", OK: true, Data: raw})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(len(wire)).To(gomega.BeNumerically("<", 1<<20))
			hash.Write(reply.Output)
			q.ExpectedFile, q.Offset = reply.File, reply.NextOffset
			calls++
		}
		gomega.Expect(calls).To(gomega.Equal(expectedCalls))
		gomega.Expect(hex.EncodeToString(hash.Sum(nil))).To(gomega.Equal("8565a714dca840f8652c5bae9249ab05f5fb5a4f9f13fbe23304b10f68252da2"))
		gomega.Expect(q.ExpectedFile.SHA256).To(gomega.Equal(hex.EncodeToString(hash.Sum(nil))))
	}, ginkgo.Entry("v1 retains 64 KiB pages", "haepari.guest-request/v1", 64<<10, 800),
		ginkgo.Entry("v2 uses 512 KiB pages", "haepari.guest-request/v2", 512<<10, 100))

	ginkgo.DescribeTable("budgets the complete file envelope with large admitted request metadata", func(operation string) {
		root := ginkgo.GinkgoT().TempDir()
		request := requestFixture(root)
		request.Argv = append(request.Argv, strings.Repeat("a", 64<<10))
		request.Environment = append(request.Environment, "LARGE="+strings.Repeat("e", 64<<10))
		gomega.Expect(request.Validate(root)).To(gomega.Succeed())
		payload := append(bytes.Repeat([]byte("bounded!"), 128<<10), []byte("tail")...)
		f, err := os.OpenFile(filepath.Join(root, "output.log"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer f.Close()
		_, err = f.Write(payload)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.Seek(0, io.SeekStart)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q := GuestRequest{Schema: "haepari.guest-request/v2", Operation: "file", Request: request, Path: "output.log", Deadline: time.Now().Add(time.Minute)}
		var collected []byte
		for q.Offset < int64(len(payload)) {
			reply, err := readGuestFile(f, q, GuestReply{Schema: GuestReplySchema(q), Request: &request, Control: &GuestControl{Request: q, NativeRequest: &request}})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var data any = &reply
			page := &reply
			switch operation {
			case "runner.guest-transport":
				owned := &GuestTransportReply{Schema: "haepari.guest-transport-reply/v1", Guest: reply, Binding: &GuestTransportBinding{Guest: q}}
				data, page = owned, &owned.Guest
			case "runner.guest-recovery":
				basis := GuestRecoveryBasis{Request: request}
				attested := &GuestRecoveryReply{Schema: "haepari.guest-recovery-reply/v2", Guest: reply, Basis: &basis, Attestation: &GuestAttestation{Guest: q, Basis: basis}}
				data, page = attested, &attested.Guest
			}
			if q.Offset == 0 {
				unbounded, err := json.Marshal(data)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(unbounded)).To(gomega.BeNumerically(">", 1<<20))
			}
			metadata := *page.File
			raw, err := marshalGuestFileReply(operation, data, page)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var wire bytes.Buffer
			gomega.Expect(json.NewEncoder(&wire).Encode(managed.Envelope{Schema: "haepari.envelope/v1", Operation: operation, OK: true, Data: raw})).To(gomega.Succeed())
			gomega.Expect(wire.Len()).To(gomega.BeNumerically("<=", 1<<20))
			envelope, err := managed.DecodeEnvelope(wire.Bytes())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			switch operation {
			case "runner.guest":
				_, err = DecodeGuestReply(envelope.Data)
			case "runner.guest-transport":
				_, err = DecodeGuestTransportReply(envelope.Data)
			case "runner.guest-recovery":
				_, err = DecodeGuestRecoveryReply(envelope.Data)
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(page.Output).NotTo(gomega.BeEmpty())
			gomega.Expect(len(page.Output)).To(gomega.BeNumerically("<=", 512<<10))
			gomega.Expect(page.NextOffset).To(gomega.Equal(q.Offset + int64(len(page.Output))))
			gomega.Expect(*page.File).To(gomega.Equal(metadata))
			gomega.Expect(*page.Request).To(gomega.Equal(request))
			gomega.Expect(page.Control.Request).To(gomega.Equal(q))
			collected = append(collected, page.Output...)
			q.ExpectedFile, q.Offset = page.File, page.NextOffset
		}
		gomega.Expect(collected).To(gomega.Equal(payload))
		sum := sha256.Sum256(collected)
		gomega.Expect(q.ExpectedFile.SHA256).To(gomega.Equal(hex.EncodeToString(sum[:])))
	}, ginkgo.Entry("ordinary", "runner.guest"), ginkgo.Entry("owned", "runner.guest-transport"), ginkgo.Entry("attested", "runner.guest-recovery"))

	ginkgo.It("refuses an envelope whose metadata cannot fit without losing file progress", func() {
		request := Request{Argv: []string{strings.Repeat("a", 1<<20)}}
		reply := GuestReply{Request: &request, Output: []byte("page"), NextOffset: 4}
		_, err := marshalGuestFileReply("runner.guest", &reply, &reply)
		gomega.Expect(err).To(gomega.MatchError(ErrInvalid))
	})

	ginkgo.It("refuses changed file metadata between v2 pages", func() {
		f, err := os.OpenFile(filepath.Join(ginkgo.GinkgoT().TempDir(), "output.log"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer f.Close()
		gomega.Expect(f.Truncate(1 << 20)).To(gomega.Succeed())
		q := GuestRequest{Schema: "haepari.guest-request/v2", Operation: "file", Path: "output.log", Deadline: time.Now().Add(time.Minute)}
		first, err := readGuestFile(f, q, GuestReply{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.Offset, q.ExpectedFile = first.NextOffset, first.File
		gomega.Expect(f.Truncate((1 << 20) + 1)).To(gomega.Succeed())
		_, err = readGuestFile(f, q, GuestReply{})
		gomega.Expect(err).To(gomega.MatchError(ErrConflict))
	})
})
