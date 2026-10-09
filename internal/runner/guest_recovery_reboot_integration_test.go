//go:build integration

package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// This fixture models historical bytes from an earlier boot. It is not evidence
// that a VM reboot occurred; real reboot qualification belongs to TASK-038.
func retainedPreviousBootGuest() (GuestRecoveryRequest, string) {
	return retainedPreviousBootGuestWithProtocol("haepari.guest-request/v1")
}

func retainedPreviousBootGuestWithProtocol(schema string) (GuestRecoveryRequest, string) {
	q, parent := retainedNativeGuestWithProtocol(0, schema)
	root := filepath.Join(parent, q.Guest.Request.Identity.JobID)
	old := "previous-fixture-boot"
	q.Guest.BootID, q.Guest.Request.BootID = old, old
	write := func(path string, value any) {
		raw, err := json.Marshal(value)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(path, raw, 0600)).To(gomega.Succeed())
	}
	read := func(path string, value any) {
		raw, err := os.ReadFile(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(raw, value)).To(gomega.Succeed())
	}
	var original GuestRequest
	read(filepath.Join(root, "guest.json"), &original)
	original.BootID, original.Request.BootID = old, old
	write(filepath.Join(root, "guest.json"), original)
	write(filepath.Join(root, "request.json"), q.Guest.Request)
	var control GuestControl
	path := filepath.Join(parent, ".haepari-control-"+q.Guest.Request.Identity.JobID+".json")
	read(path, &control)
	control.Request.BootID, control.Request.Request.BootID, control.NativeRequest.BootID, control.Owner.BootID = old, old, old, old
	write(path, control)
	var intent Intent
	read(filepath.Join(root, "intent.json"), &intent)
	intent.Binding, intent.Supervisor.BootID = q.Guest.Request.Binding(), old
	write(filepath.Join(root, "intent.json"), intent)
	var start Start
	read(filepath.Join(root, "start.json"), &start)
	start.Binding, start.Process.BootID = q.Guest.Request.Binding(), old
	write(filepath.Join(root, "start.json"), start)
	// The simulated reboot interrupted supervision before either receipt.
	gomega.Expect(os.Remove(filepath.Join(root, "outcome.json"))).To(gomega.Succeed())
	gomega.Expect(os.Remove(filepath.Join(root, "exit.json"))).To(gomega.Succeed())
	boot, err := platform.BootIdentity()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	q.Schema, q.Operation, q.RecoveryBoot = "haepari.guest-recovery-request/v2", "probe", boot
	rootBinding := q.Basis.Root
	q.Guest.Root = &rootBinding
	q.Basis, q.Evidence, q.Attestation, q.MutationKey = nil, nil, nil, ""
	return q, parent
}

var _ = ginkgo.Describe("GuestRebootRecoveryNative", ginkgo.Label("integration"), func() {
	ginkgo.It("retains a v2 prepared workspace changed before either the reboot basis or attested collection", func() {
		for _, attested := range []bool{false, true} {
			q, parent := retainedPreviousBootGuestWithProtocol("haepari.guest-request/v2")
			defer os.RemoveAll(parent)
			root := filepath.Join(parent, q.Guest.Request.Identity.JobID)
			if attested {
				envelope, err := nativeGuestProtocol(q, true, "")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				var reply GuestRecoveryReply
				gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
				gomega.Expect(reply.Basis.WorkspaceRoot).NotTo(gomega.BeNil())
				q.Basis, q.Operation, q.MutationKey = reply.Basis, "attest", "fixture-workspace-reboot"
				q.Evidence = &managed.RecoveryEvidence{Source: "machine-observed", Observer: "synthetic-cross-boot-fixture", ObservedAt: time.Now().UTC(), Mechanism: "guest-reboot-observed", Digest: managed.RecoveryDigest(*q.Basis), Reference: "job:" + q.Guest.Request.Identity.JobID}
				envelope, err = nativeGuestProtocol(q, true, "")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
				q.Attestation = reply.Attestation
			}
			gomega.Expect(os.Rename(filepath.Join(root, "workspace"), filepath.Join(root, "saved"))).To(gomega.Succeed())
			gomega.Expect(os.Mkdir(filepath.Join(root, "workspace"), 0700)).To(gomega.Succeed())
			operations := []string{"probe"}
			if attested {
				operations = []string{"file", "cleanup", "health"}
			}
			for _, operation := range operations {
				q.Operation, q.Guest.Path, q.CollectionDigest = operation, "count.txt", managed.RecoveryDigest("fixture-collected")
				_, err := nativeGuestProtocol(q, true, "")
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(os.ReadFile(filepath.Join(root, "saved/count.txt"))).To(gomega.Equal([]byte("once")))
			}
		}
	})
	ginkgo.DescribeTable("binds missing historical receipts without upgrading native proof and collects before cleanup", func(schema string) {
		q, parent := retainedPreviousBootGuestWithProtocol(schema)
		defer os.RemoveAll(parent)
		root := filepath.Join(parent, q.Guest.Request.Identity.JobID)
		before, err := os.ReadFile(filepath.Join(root, "request.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		envelope, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestRecoveryReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		gomega.Expect(reply.Basis.Missing).To(gomega.Equal([]string{"outcome.json", "exit.json"}))
		gomega.Expect(reply.Basis.Observation.Exit).To(gomega.BeNil())
		q.Basis, q.Operation, q.MutationKey = reply.Basis, "attest", "fixture-reboot-recovery"
		q.Evidence = &managed.RecoveryEvidence{Source: "machine-observed", Observer: "synthetic-cross-boot-fixture", ObservedAt: time.Now().UTC(), Mechanism: "guest-reboot-observed", Digest: managed.RecoveryDigest(*q.Basis), Reference: "job:" + q.Guest.Request.Identity.JobID}
		envelope, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		q.Attestation = reply.Attestation
		gomega.Expect(ValidateGuestAttestation(*reply.Attestation, q.Guest)).To(gomega.Succeed())
		gomega.Expect(os.ReadFile(filepath.Join(root, "request.json"))).To(gomega.Equal(before))
		_, err = os.Stat(filepath.Join(root, "exit.json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		q.Operation, q.Guest.Path = "file", "count.txt"
		envelope, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		gomega.Expect(reply.Guest.Output).To(gomega.Equal([]byte("once")))
		q.Operation, q.CollectionDigest = "cleanup", managed.RecoveryDigest("fixture-collected")
		envelope, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		gomega.Expect(reply.Reusable).To(gomega.BeTrue())
		q.Operation = "health"
		_, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}, ginkgo.Entry("original v1 guest", "haepari.guest-request/v1"), ginkgo.Entry("original v2 guest", "haepari.guest-request/v2"))
	ginkgo.DescribeTable("accepts only bounded v2 attested workspace absence", func(schema string) {
		q, parent := retainedPreviousBootGuestWithProtocol(schema)
		defer os.RemoveAll(parent)
		envelope, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestRecoveryReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		q.Basis, q.Operation, q.MutationKey = reply.Basis, "attest", "fixture-absence-recovery"
		q.Evidence = &managed.RecoveryEvidence{Source: "machine-observed", Observer: "synthetic-cross-boot-fixture", ObservedAt: time.Now().UTC(), Mechanism: "guest-reboot-observed", Digest: managed.RecoveryDigest(*q.Basis), Reference: "job:" + q.Guest.Request.Identity.JobID}
		envelope, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		q.Attestation, q.Operation, q.Guest.Path = reply.Attestation, "file", "missing.txt"
		envelope, err = nativeGuestProtocol(q, true, "")
		if schema == "haepari.guest-request/v1" {
			gomega.Expect(err).To(gomega.HaveOccurred())
		} else {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
			gomega.Expect(reply.Guest.Absent).To(gomega.BeTrue())
			gomega.Expect(reply.Guest.File).To(gomega.BeNil())
			gomega.Expect(reply.Guest.Output).To(gomega.BeEmpty())
			for _, change := range []func(*GuestRecoveryRequest){
				func(v *GuestRecoveryRequest) { v.Guest.Area = "log" },
				func(v *GuestRecoveryRequest) { v.Guest.Area = "receipt" },
				func(v *GuestRecoveryRequest) { v.Guest.Offset = 1 },
				func(v *GuestRecoveryRequest) { v.Guest.ExpectedFile = &GuestFile{Path: v.Guest.Path, Inode: 1} },
			} {
				changed := q
				change(&changed)
				_, err = nativeGuestProtocol(changed, true, "")
				gomega.Expect(err).To(gomega.HaveOccurred())
			}
		}
		gomega.Expect(os.ReadFile(filepath.Join(parent, q.Guest.Request.Identity.JobID, "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
	}, ginkgo.Entry("original v1 guest", "haepari.guest-request/v1"), ginkgo.Entry("original v2 guest", "haepari.guest-request/v2"))
	ginkgo.It("rejects same boot, changed recovery boot, changed root and unexplained receipt absence", func() {
		q, parent := retainedPreviousBootGuest()
		defer os.RemoveAll(parent)
		for _, change := range []func(*GuestRecoveryRequest){
			func(v *GuestRecoveryRequest) { v.RecoveryBoot = v.Guest.BootID },
			func(v *GuestRecoveryRequest) { v.RecoveryBoot = "foreign-current-boot" },
			func(v *GuestRecoveryRequest) { v.Schema, v.RecoveryBoot = "haepari.guest-recovery-request/v1", "" },
			func(v *GuestRecoveryRequest) { root := *v.Guest.Root; root.Inode++; v.Guest.Root = &root },
		} {
			changed := q
			change(&changed)
			_, err := nativeGuestProtocol(changed, true, "")
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		root := filepath.Join(parent, q.Guest.Request.Identity.JobID)
		gomega.Expect(os.Remove(filepath.Join(root, "intent.json"))).To(gomega.Succeed())
		_, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).To(gomega.HaveOccurred()) // start cannot survive a missing intent.
		gomega.Expect(os.ReadFile(filepath.Join(root, "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
	})
})
