//go:build integration

package runner

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("GuestWorkspaceIdentity", ginkgo.Label("integration"), func() {
	ginkgo.It("classifies native recovery v2 refusals while preserving recovery v1 history", func() {
		for _, schema := range []string{"haepari.guest-recovery-request/v1", "haepari.guest-recovery-request/v2"} {
			guest, parent := guestFixture("printf secret-workload-content")
			defer os.RemoveAll(parent)
			guest.MAC = "00:00:00:00:00:00"
			q := GuestRecoveryRequest{Schema: schema, Operation: "probe", Guest: guest, RecoveryDigest: guest.RunnerDigest, Deadline: guest.Deadline}
			if schema == "haepari.guest-recovery-request/v2" {
				q.RecoveryBoot = guest.BootID
			}
			envelope, err, stderr := nativeGuestProtocolObserved(q, true, "")
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(envelope.Operation).To(gomega.Equal("runner.guest-recovery"))
			gomega.Expect(envelope.OK).To(gomega.BeFalse())
			gomega.Expect(envelope.Data).To(gomega.BeEmpty())
			gomega.Expect(envelope.Error.Code).To(gomega.Equal(managed.Conflict))
			if schema == "haepari.guest-recovery-request/v1" {
				gomega.Expect(envelope.Error.Message).To(gomega.Equal(ErrConflict.Error()))
				gomega.Expect(stderr).To(gomega.BeEmpty())
			} else {
				gomega.Expect(envelope.Error.Message).To(gomega.Equal(managed.DiagnosticIdentityConflict))
				gomega.Expect(stderr).To(gomega.Equal(managed.DiagnosticIdentityConflict + "\n"))
			}
			for _, path := range []string{guest.Request.Identity.JobID, ".haepari-control-" + guest.Request.Identity.JobID + ".json", ".haepari-attestation-" + guest.Request.Identity.JobID + ".json"} {
				_, err = os.Lstat(filepath.Join(parent, path))
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			}
		}
	})

	ginkgo.It("preserves the v1 error envelope while redacting native stderr for both versions", func() {
		for _, schema := range []string{"haepari.guest-request/v1", "haepari.guest-request/v2"} {
			q, parent := guestFixture("printf secret-workload-content")
			defer os.RemoveAll(parent)
			q.Schema, q.MAC = schema, "00:00:00:00:00:00"
			envelope, err, stderr := nativeGuestProtocolObserved(q, false, "")
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(envelope.OK).To(gomega.BeFalse())
			gomega.Expect(envelope.Error.Code).To(gomega.Equal(managed.Conflict))
			gomega.Expect(stderr).To(gomega.Equal(managed.DiagnosticIdentityConflict + "\n"))
			if schema == "haepari.guest-request/v1" {
				gomega.Expect(envelope.Error.Message).To(gomega.Equal(ErrConflict.Error()))
			} else {
				gomega.Expect(envelope.Error.Message).To(gomega.Equal(managed.DiagnosticIdentityConflict))
			}
			_, err = os.Stat(filepath.Join(parent, q.Request.Identity.JobID))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})

	ginkgo.DescribeTable("retains output after the workload changes its prepared workspace", func(replacement string) {
		q, parent := guestFixture("printf evidence > required.txt; cd ..; mv workspace saved" + replacement)
		q.Schema = "haepari.guest-request/v2"
		defer os.RemoveAll(parent)
		envelope, err := nativeGuestProtocol(q, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		gomega.Expect(reply.Control.Schema).To(gomega.Equal("haepari.guest-control/v2"))
		gomega.Expect(reply.Control.WorkspaceRoot).NotTo(gomega.BeNil())
		q.Request, q.Root, q.Files, q.Inventory = *reply.Request, reply.Root, nil, nil
		q.Operation = "start"
		_, err = nativeGuestProtocol(q, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reply = awaitNativeGuestExit(q)
		gomega.Expect(reply.Observation.Exit.TerminationProven).To(gomega.BeTrue())
		root := filepath.Join(parent, q.Request.Identity.JobID)
		for _, operation := range []string{"file", "cleanup"} {
			q.Operation, q.Area, q.Path = operation, "workspace", "required.txt"
			envelope, err = nativeGuestProtocol(q, false, "")
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(envelope.OK).To(gomega.BeFalse())
			gomega.Expect(os.ReadFile(filepath.Join(root, "saved/required.txt"))).To(gomega.Equal([]byte("evidence")))
		}
		q.Operation = "health"
		envelope, err = nativeGuestProtocol(q, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		gomega.Expect(reply.Reusable).To(gomega.BeFalse())
	}, ginkgo.Entry("missing workspace after rename", ""), ginkgo.Entry("recreated empty workspace", "; mkdir workspace"), ginkgo.Entry("replaced workspace with foreign output", "; mkdir workspace; printf foreign > workspace/required.txt"))

	ginkgo.It("proves a missing file only inside the unchanged prepared workspace", func() {
		q, parent := guestFixture("true")
		q.Schema = "haepari.guest-request/v2"
		defer os.RemoveAll(parent)
		prepared := guestOperation(q, "prepare")
		q.Request, q.Root, q.Files, q.Inventory = *prepared.Request, prepared.Root, nil, nil
		guestOperation(q, "start")
		stopped := awaitGuestExit(q, func(request GuestRequest) GuestReply { return guestOperation(request, "watch") })
		gomega.Expect(stopped.Observation.Exit.TerminationProven).To(gomega.BeTrue())
		q.Area, q.Path = "workspace", "missing.txt"
		gomega.Expect(guestOperation(q, "file").Absent).To(gomega.BeTrue())
		guestOperation(q, "cleanup")
		gomega.Expect(guestOperation(q, "health").Reusable).To(gomega.BeTrue())
	})
})
