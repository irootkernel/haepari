package relocation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func TestRelocation(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Relocation Suite")
}

type fixture struct {
	parent, source, target, backup, binary string
	a, b                                   ServicePlan
	env                                    Environment
}

func newFixture() fixture {
	parent, err := os.MkdirTemp("/tmp", "hr-")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	parent, err = filepath.EvalSymlinks(parent)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ginkgo.DeferCleanup(func() { gomega.Expect(os.RemoveAll(parent)).To(gomega.Succeed()) })
	f := fixture{parent: parent, source: filepath.Join(parent, "s"), target: filepath.Join(parent, "t"), backup: filepath.Join(parent, "b"), binary: filepath.Join(parent, "candidate")}
	gomega.Expect(os.WriteFile(f.binary, []byte("fixture executable\n"), 0700)).To(gomega.Succeed())
	f.a = ServicePlan{GlobalLimit: 1, HostMACOSGuestLimit: 1, Schema: "haepari.release-service-plan/v1", Instance: "relocation-fixture", Label: "io.rootkernel.haepari.relocation-fixture", Domain: "gui/" + strconv.Itoa(os.Geteuid()), OwnerUID: os.Geteuid(), Binary: f.binary, BinarySHA256: Digest([]byte("fixture executable\n")), Root: f.source, PlistPath: filepath.Join(parent, "service.plist"), Plist: "source fixture"}
	f.b = f.a
	f.b.Schema = "haepari.release-service-plan/v2"
	f.b.Root = f.target
	f.b.Plist = "target fixture"
	root, err := state.Resolve(f.source)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(root.Prepare()).To(gomega.Succeed())
	controller, err := root.AcquireController()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(controller.Close()).To(gomega.Succeed())
	db, err := sqlite.Open(root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ledger, err := managed.NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = ledger.BindInstallation("local-relocation-fixture")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = ledger.CommitEffectiveConfig(config.Defaults(), false)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(db.Close()).To(gomega.Succeed())
	_, err = snapshot.OpenStore(f.source)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	owner := struct {
		Schema   string `json:"schema"`
		Instance string `json:"instance"`
		OwnerUID int    `json:"owner_uid"`
		Root     string `json:"root"`
	}{"haepari.release-service-owner/v1", f.a.Instance, os.Geteuid(), f.source}
	gomega.Expect(os.WriteFile(filepath.Join(f.source, OwnerName), canonical(owner), 0600)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(filepath.Join(f.source, "retained.txt"), []byte("never rewritten"), 0600)).To(gomega.Succeed())
	f.env = Environment{MoverBinary: f.binary, ServicesStopped: func(ServicePlan) error { return nil }, ValidateService: func(p ServicePlan) error {
		raw, err := os.ReadFile(p.Binary)
		if err != nil {
			return err
		}
		if Digest(raw) != p.BinarySHA256 {
			return managed.ErrConflict
		}
		return nil
	}}
	return f
}

var _ = ginkgo.Describe("Task043Relocation", ginkgo.Label("unit"), func() {
	ginkgo.It("plans/status without source writes and moves/replays/rolls back the same installation and file identities", func() {
		f := newFixture()
		root, err := openDirectory(f.source)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before, err := inventory(root, f.source)
		root.close()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(equalFiles(before, p.Files)).To(gomega.BeTrue())
		r, err := Operate(p, "status", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(r.Phase).To(gomega.Equal("planned"))
		gomega.Expect(r.Sequence).To(gomega.BeZero())
		root, err = openDirectory(f.source)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		after, err := inventory(root, f.source)
		root.close()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(equalFiles(before, after)).To(gomega.BeTrue())
		_, err = os.Stat(f.backup)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		r, err = Operate(p, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(r.Phase).To(gomega.Equal("complete"))
		gomega.Expect(r.CurrentRoot).To(gomega.Equal(f.target))
		gomega.Expect(r.BackupState).To(gomega.Equal("verified"))
		again, err := Operate(p, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again).To(gomega.Equal(r))
		gomega.Expect(os.ReadFile(filepath.Join(f.target, "retained.txt"))).To(gomega.Equal([]byte("never rewritten")))
		back, err := Operate(p, "rollback", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(back.Phase).To(gomega.Equal("rolled_back"))
		same, err := Operate(p, "rollback", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(same).To(gomega.Equal(back))
		_, err = Operate(p, "apply", f.env)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("relocation refused")))
		root, err = openDirectory(f.source)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		files, err := inventory(root, f.source)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		j, _, _, err := readJournal(root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(inventoryMatches(root, f.source, p, &j, files)).To(gomega.Succeed())
		root.close()
	})
	ginkgo.It("rejects omitted/null required fields without converting false or zero into presence", func() {
		f := newFixture()
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, alias := range []string{"PLAN_SHA256", "GLOBAL_LIMIT"} {
			var wire map[string]any
			gomega.Expect(json.Unmarshal(canonical(p), &wire)).To(gomega.Succeed())
			if alias == "PLAN_SHA256" {
				wire[alias] = wire["plan_sha256"]
				delete(wire, "plan_sha256")
			} else {
				nested := wire["source_plan"].(map[string]any)
				nested[alias] = nested["global_limit"]
				delete(nested, "global_limit")
			}
			_, err = DecodePlan(canonical(wire))
			gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		}
		raw := canonical(p)
		var fields map[string]any
		gomega.Expect(json.Unmarshal(raw, &fields)).To(gomega.Succeed())
		delete(fields, "source_parent_identity")
		_, err = DecodePlan(canonical(fields))
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
		fields["source_parent_identity"] = nil
		_, err = DecodePlan(canonical(fields))
		gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid))
	})
})

var _ = ginkgo.Describe("Task043StatusAuthority", ginkgo.Label("unit"), func() {
	ginkgo.It("inspects exact archived receipts with a later binary while current controller ownership is held", func() {
		f := newFixture()
		old, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = Operate(old, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		historical, err := Operate(old, "rollback", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		later := filepath.Join(f.parent, "later-candidate")
		gomega.Expect(os.WriteFile(later, []byte("later executable"), 0700)).To(gomega.Succeed())
		a, b := f.a, f.b
		a.Binary = later
		b.Binary = later
		a.BinarySHA256 = Digest([]byte("later executable"))
		b.BinarySHA256 = a.BinarySHA256
		env := f.env
		env.MoverBinary = later
		next, err := NewPlan(a, b, filepath.Join(f.parent, "later-backup"), env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		completion, err := Operate(next, "apply", env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Remove(f.binary)).To(gomega.Succeed())
		root, err := state.Resolve(f.target)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		lock, err := root.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer lock.Close()
		env.ValidateService = func(ServicePlan) error { return managed.ErrBlocked }
		env.ServicesStopped = env.ValidateService
		h, err := openDirectory(f.target)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer h.close()
		before, err := inventory(h, f.target)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		got, err := Operate(old, "status", env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(got).To(gomega.Equal(historical))
		got, err = Operate(next, "status", env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(got).To(gomega.Equal(completion))
		after, err := inventory(h, f.target)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		for _, action := range []string{"apply", "rollback"} {
			_, err = Operate(old, action, env)
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
	})
})

var _ = ginkgo.Describe("Task043ClosedJournal", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses omitted false activation, null optional identity and inconsistent owner/receipt records without writes", func() {
		for _, kind := range []string{"activation", "null", "owner", "receipt"} {
			f := newFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			name := filepath.Join(f.target, JournalName)
			raw, err := os.ReadFile(name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var fields map[string]any
			gomega.Expect(json.Unmarshal(raw, &fields)).To(gomega.Succeed())
			switch kind {
			case "activation":
				delete(fields, "activated")
			case "null":
				fields["predecessor_archive"] = nil
			case "owner":
				fields["target_owner"] = fields["original_owner"]
			case "receipt":
				fields["completion_receipt"].(map[string]any)["installation_id"] = "foreign"
			}
			bad := canonical(fields)
			gomega.Expect(os.WriteFile(name, bad, 0600)).To(gomega.Succeed())
			for _, action := range []string{"status", "apply", "rollback"} {
				_, err = Operate(p, action, f.env)
				gomega.Expect(err).To(gomega.HaveOccurred())
			}
			gomega.Expect(os.ReadFile(name)).To(gomega.Equal(bad))
		}
	})
})
