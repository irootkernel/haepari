package relocation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func jobStateFixture(f fixture, kind string) {
	db, err := sqlite.Open(filepath.Join(f.source, "state.sqlite"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer db.Close()
	ledger, err := managed.NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(ledger.RegisterResource([]byte(`{"schema":"haepari.resource/v1","id":"local","kind":"local","execution_mode":"managed","capacity":1,"revision":1,"adapter":{"qualification_reference":"eligibility-fixture"},"cleanup":{"policy_reference":"owned-workspace"}}`))).To(gomega.Succeed())
	input := filepath.Join(f.parent, "input")
	gomega.Expect(os.MkdirAll(filepath.Join(input, "source"), 0700)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(filepath.Join(input, "source/input.txt"), []byte("retained"), 0600)).To(gomega.Succeed())
	store, err := snapshot.OpenStore(f.source)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	sealed, err := store.Seal(context.Background(), input, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	digest := strings.Repeat("a", 64)
	job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local", Mode: "auto", Suite: "eligibility", Input: managed.Input{Root: input, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt"}}, Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/true"}, CWD: "source", RequiredOutputs: []string{"out.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 60, PrepareSeconds: 5, RunSeconds: 5, CollectSeconds: 5, CleanupSeconds: 5}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "eligibility-fixture"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
	r, err := ledger.AdmitWithParent(canonical(job), job.Input.InventoryDigest, sealed.ObjectID, canonical(sealed.Manifest), "key", "job", "local-relocation-fixture", nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	if kind == "active" || kind == "reconciling" {
		_, err = ledger.Reserve(managed.ReserveRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		if kind == "reconciling" {
			gomega.Expect(ledger.MarkInterrupted()).To(gomega.Succeed())
		}
		return
	}
	if kind == "queued" {
		return
	}
	_, err = ledger.Cancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: r.JobRevision, MutationKey: "cancel"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	switch kind {
	case "occupied":
		_, err = db.Exec("UPDATE managed_resources SET occupancy=1 WHERE id='local'")
	case "unknown":
		_, err = db.Exec("UPDATE managed_resources SET availability='unknown' WHERE id='local'")
	case "recovery":
		_, err = db.Exec("INSERT INTO managed_evidence_recovery(mutation_key,resource_id,request_json) VALUES('pending','local','{}')")
	case "purge":
		_, err = db.Exec("INSERT INTO managed_purges(job_id,request_json,complete) VALUES('job','{}',0)")
	case "native":
		_, err = db.Exec("INSERT INTO managed_power_transitions(operation_generation,victim_resource_id,target_resource_id,identity_json,state) VALUES(1,'local','local','{}','pending')")
	case "schema":
		_, err = db.Exec("CREATE TABLE unsupported_durable_control(value TEXT)")
	case "external-config":
		doc := config.Defaults()
		doc.Revision = 2
		doc.DefaultDeadlines.RunSeconds++
		_, err = ledger.CommitEffectiveConfig(doc, true)
	case "grant", "queue":
		err = ledger.RegisterResource([]byte(`{"schema":"haepari.resource/v1","id":"lock","kind":"lock","capacity":1,"revision":1,"liveness_seconds":120,"max_hold_seconds":3600}`))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.AcquireLock(managed.LockAcquireRequest{LockID: "lock", RequestKey: "one", Holder: "fixture"}, "local-relocation-fixture", nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		if kind == "queue" {
			_, err = ledger.AcquireLock(managed.LockAcquireRequest{LockID: "lock", RequestKey: "two", Holder: "fixture"}, "local-relocation-fixture", nil)
		}
	}
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

var _ = ginkgo.Describe("Task043Eligibility", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses current durable Job/occupancy/grant/recovery/purge/native/schema/config blockers through planning and apply without clearing them", func() {
		for _, kind := range []string{"queued", "active", "reconciling", "occupied", "unknown", "grant", "queue", "recovery", "purge", "native", "schema", "external-config"} {
			ginkgo.By(kind)
			f := newFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			jobStateFixture(f, kind)
			root, err := openDirectory(f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			before, err := inventory(root, f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			root.close()
			_, err = NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).To(gomega.HaveOccurred())
			if kind != "schema" {
				gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue(), kind)
			}
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(err).To(gomega.HaveOccurred())
			root, err = openDirectory(f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			after, err := inventory(root, f.source)
			root.close()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(equalFiles(before, after)).To(gomega.BeTrue())
			_, err = os.Stat(f.backup)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})
})

var _ = ginkgo.Describe("Task043ReferenceClassification", ginkgo.Label("unit"), func() {
	ginkgo.It("classifies decoded live host references by path boundaries and preserves known nonpath values", func() {
		for _, kind := range []string{"inside", "escaped", "key-inside", "outside-sibling", "nonpath"} {
			ginkgo.By(kind)
			f := newFixture()
			db, err := sqlite.Open(filepath.Join(f.source, "state.sqlite"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ledger, err := managed.NewLedger(db, 1)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			mappingDir := filepath.Join(f.parent, "s-sibling")
			if kind == "inside" || kind == "escaped" {
				mappingDir = f.source
			}
			gomega.Expect(os.MkdirAll(mappingDir, 0700)).To(gomega.Succeed())
			key := filepath.Join(mappingDir, "key")
			if kind == "key-inside" {
				key = filepath.Join(f.source, "key")
			}
			gomega.Expect(os.WriteFile(key, []byte("fixture-key"), 0600)).To(gomega.Succeed())
			mapping := map[string]any{"schema": "haepari.ssh-mapping/v1", "user": "fixture", "port": 22, "identity_file": key}
			raw := canonical(mapping)
			if kind == "escaped" {
				raw = []byte(strings.ReplaceAll(string(raw), "/", "\\/"))
			}
			mappingPath := filepath.Join(mappingDir, "mapping.json")
			gomega.Expect(os.WriteFile(mappingPath, raw, 0600)).To(gomega.Succeed())
			pin := filepath.Join(f.parent, "host-key")
			gomega.Expect(os.WriteFile(pin, []byte("fixture-pin"), 0600)).To(gomega.Succeed())
			spec := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "vm", Kind: "utm-macos", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "fixture-nonpath:" + f.source, UTMCtl: "/fixture/utmctl-not-invoked"}, VM: &managed.VM{UTMVMUUID: "11111111-1111-4111-8111-111111111111", MACAddress: "aa:bb:cc:dd:ee:ff", AddressDiscovery: "dhcp-lease-by-mac", GuestJobRoot: "/guest/jobs"}, Guest: &managed.Guest{ConnectionReference: Digest(raw) + ":" + mappingPath, KnownHostsReference: Digest([]byte("fixture-pin")) + ":" + pin}}
			gomega.Expect(ledger.RegisterResource(canonical(spec))).To(gomega.Succeed())
			gomega.Expect(db.Close()).To(gomega.Succeed())
			_, err = NewPlan(f.a, f.b, f.backup, f.env)
			if kind == "inside" || kind == "escaped" || kind == "key-inside" {
				gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
			} else {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
		}
	})
})
