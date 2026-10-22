//go:build integration

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

func guestFixture(script string) (GuestRequest, string) {
	parent := privateFixtureDirectory()
	root := filepath.Join(parent, "job-fixture")
	q := requestFixture(root)
	q.LogDevice, q.LogInode = 0, 0
	boot, err := platform.BootIdentity()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	q.BootID = boot
	q.Argv = []string{"/bin/sh", "-c", script}
	q.Deadline = time.Now().Add(15 * time.Second).UTC()
	q.RunSeconds = 15
	executable, err := os.Executable()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	binary, err := os.ReadFile(executable)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	sum := sha256.Sum256(binary)
	interfaces, err := net.Interfaces()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	mac := ""
	for _, iface := range interfaces {
		if len(iface.HardwareAddr) == 6 {
			mac = iface.HardwareAddr.String()
			break
		}
	}
	gomega.Expect(mac).NotTo(gomega.BeEmpty())
	storeRoot := privateFixtureDirectory()
	defer os.RemoveAll(storeRoot)
	source := privateFixtureDirectory()
	defer os.RemoveAll(source)
	gomega.Expect(os.WriteFile(filepath.Join(source, "input.txt"), []byte("sealed"), 0600)).To(gomega.Succeed())
	store, err := snapshot.OpenStore(storeRoot)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	snap, err := store.Seal(context.Background(), source, []string{"input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	inventory, err := json.Marshal(snap.Manifest)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return GuestRequest{Schema: "haepari.guest-request/v1", Operation: "prepare", VMUUID: "11111111-1111-4111-8111-111111111111", MAC: mac, Challenge: "proof", BootID: boot, JobRoot: parent, RunnerDigest: "sha256:" + hex.EncodeToString(sum[:]), Request: q, ObjectID: snap.ObjectID, InputDigest: snap.Manifest.TreeSHA256, ProfileDigest: "sha256:" + hex.EncodeToString(sum[:]), Deadline: time.Now().Add(time.Minute).UTC(), Inventory: inventory, Files: [][]byte{[]byte("sealed")}}, parent
}

func guestOperation(q GuestRequest, op string) GuestReply {
	q.Operation = op
	r, err := Guest(q)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}

// A guest watch is a bounded observation, not a promise of terminal receipts.
// Follow the same run without extending its original execution/stop window.
func awaitGuestExit(q GuestRequest, observe func(GuestRequest) GuestReply) GuestReply {
	q.Operation = "watch"
	q.Deadline = minTime(q.Deadline, q.Request.Deadline.Add(time.Duration(q.Request.TerminationSeconds)*time.Second))
	for {
		gomega.Expect(time.Now().Before(q.Deadline)).To(gomega.BeTrue(), "guest exit was not observed within the original run/stop window: %s", q.JobRoot)
		reply := observe(q)
		gomega.Expect(reply.Observation).NotTo(gomega.BeNil(), "guest watch omitted its observation: %s", q.JobRoot)
		if reply.Observation.Exit != nil {
			return reply
		}
	}
}

var _ = ginkgo.Describe("Task014GuestContinuity", ginkgo.Label("integration"), func() {
	ginkgo.It("follows a bounded nonterminal watch to the original native exit without another start", func() {
		q, parent := guestFixture("printf once > count.txt; while [ ! -f ../finish ]; do /bin/sleep .02; done")
		defer func() {
			if ginkgo.CurrentSpecReport().Failed() {
				ginkgo.GinkgoWriter.Printf("guest watch fixture retained: %s\n", parent)
				return
			}
			os.RemoveAll(parent)
		}()
		q.Request = *guestOperation(q, "prepare").Request
		q.Files, q.Inventory = nil, nil
		guestOperation(q, "start")
		first := guestOperation(q, "watch")
		gomega.Expect(first.Observation).NotTo(gomega.BeNil())
		gomega.Expect(first.Observation.Exit).To(gomega.BeNil())
		gomega.Expect(os.WriteFile(filepath.Join(parent, q.Request.Identity.JobID, "finish"), nil, 0600)).To(gomega.Succeed())
		stopped := awaitNativeGuestExit(q)
		gomega.Expect(stopped.Observation.Exit.TerminationProven).To(gomega.BeTrue())
		gomega.Expect(stopped.Observation.Exit.Binding).To(gomega.Equal(q.Request.Binding()))
		gomega.Expect(stopped.Observation.Exit.Outcome.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		gomega.Expect(os.ReadFile(filepath.Join(parent, q.Request.Identity.JobID, "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
	})

	ginkgo.It("reattaches original receipts and literal output after the protocol caller returns, then cancels once", func() {
		q, parent := guestFixture("printf 'literal $HOME ; quote\n'; printf x >> count.txt; while :; do sleep .05; done")
		defer func() {
			path := filepath.Join(parent, q.Request.Identity.JobID, "request.json")
			if view, err := Inspect(path); err == nil && view.Intent != nil && (view.Exit == nil || !view.Exit.TerminationProven) {
				q.Operation = "cancel"
				r, e := Guest(q)
				if e != nil || r.Observation == nil || r.Observation.Exit == nil || !r.Observation.Exit.TerminationProven {
					ginkgo.Fail("guest fixture termination unproven; retain " + parent)
					return
				}
			}
			os.RemoveAll(parent)
		}()
		prepared := guestOperation(q, "prepare")
		gomega.Expect(prepared.Request.LogInode).NotTo(gomega.BeZero())
		// Repeating completed preparation observes the same request without writes.
		gomega.Expect(guestOperation(q, "prepare").Request).To(gomega.Equal(prepared.Request))
		q.Request = *prepared.Request
		q.Files = nil
		q.Inventory = nil
		q.Deadline = q.Request.Deadline.Add(-time.Second)
		guestOperation(q, "start")
		var start *Start
		gomega.Eventually(func() bool { r := guestOperation(q, "inspect"); start = r.Observation.Start; return start != nil }, 3*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
		original := *start
		for i := 0; i < 3; i++ {
			gomega.Expect(guestOperation(q, "start").Observation.Start).To(gomega.Equal(&original))
		}
		changedDeadline := q
		changedDeadline.Operation = "start"
		changedDeadline.Deadline = q.Deadline.Add(time.Second)
		_, err := Guest(changedDeadline)
		gomega.Expect(err).To(gomega.Equal(ErrConflict))
		gomega.Eventually(func() string { return string(guestOperation(q, "output").Output) }, 3*time.Second, 25*time.Millisecond).Should(gomega.ContainSubstring("literal $HOME ; quote"))
		wrong := q
		wrong.Request.Identity.Nonce = "foreign"
		_, err = Guest(wrong)
		gomega.Expect(err).To(gomega.HaveOccurred())
		wrong = q
		wrong.BootID = "old-boot"
		_, err = Guest(wrong)
		gomega.Expect(err).To(gomega.HaveOccurred())
		for _, change := range []func(*GuestRequest){func(q *GuestRequest) { q.ProfileDigest = "sha256:" + strings.Repeat("0", 64) }, func(q *GuestRequest) { q.InputDigest = "sha256:" + strings.Repeat("0", 64) }, func(q *GuestRequest) { q.Deadline = time.Now().Add(-time.Second) }} {
			wrong = q
			change(&wrong)
			_, err = Guest(wrong)
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		r := guestOperation(q, "cancel")
		gomega.Expect(r.Observation.Exit.TerminationProven).To(gomega.BeTrue())
		gomega.Expect(string(r.Observation.Exit.Outcome.WorkloadOutcome)).To(gomega.Equal("cancelled"))
		gomega.Expect(guestOperation(q, "start").Observation).To(gomega.Equal(r.Observation))
		data, err := os.ReadFile(filepath.Join(parent, q.Request.Identity.JobID, "workspace/count.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("x"))
	})
	ginkgo.It("refuses foreign identity, corrupt inventory and runner digest before creating job files", func() {
		q, parent := guestFixture("true")
		defer os.RemoveAll(parent)
		for _, change := range []func(*GuestRequest){func(q *GuestRequest) { q.MAC = "00:00:00:00:00:00" }, func(q *GuestRequest) { q.BootID = "wrong" }, func(q *GuestRequest) { q.RunnerDigest = "sha256:" + string(make([]byte, 64)) }, func(q *GuestRequest) { q.Files = [][]byte{[]byte("changed")} }, func(q *GuestRequest) { q.Request.WorkDir = parent }} {
			bad := q
			change(&bad)
			_, err := Guest(bad)
			gomega.Expect(err).To(gomega.HaveOccurred())
			_, err = os.Stat(filepath.Join(parent, q.Request.Identity.JobID))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})
	ginkgo.It("retains an ambiguous durable launch claim without restarting an absent supervisor", func() {
		q, parent := guestFixture("printf duplicate > effect.txt")
		defer os.RemoveAll(parent)
		q.Request = *guestOperation(q, "prepare").Request
		root := filepath.Join(parent, q.Request.Identity.JobID)
		d, err := openDirectory(root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer d.close()
		claim := struct {
			Binding      Binding `json:"binding"`
			RunnerDigest string  `json:"runner_digest"`
		}{q.Request.Binding(), q.RunnerDigest}
		gomega.Expect(d.write("launch.json", claim)).To(gomega.Succeed())
		r := guestOperation(q, "start")
		gomega.Expect(r.Observation.Intent).To(gomega.BeNil())
		gomega.Expect(reflect.DeepEqual(r.Request, &q.Request)).To(gomega.BeTrue())
		_, err = os.Stat(filepath.Join(root, "workspace/effect.txt"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("Task015GuestCleanup", ginkgo.Label("integration"), func() {
	ginkgo.DescribeTable("preserves a replacement root introduced immediately before cleanup", func(partial bool) {
		q, parent := guestFixture("true")
		stopped := partial
		defer func() {
			if !stopped {
				ginkgo.Fail("guest fixture termination unproven; retain " + parent)
				return
			}
			os.RemoveAll(parent)
		}()
		root := filepath.Join(parent, q.Request.Identity.JobID)
		original := root + "-retained"
		ownedFile := "owned.txt"
		if partial {
			gomega.Expect(os.Mkdir(root, 0700)).To(gomega.Succeed())
			gomega.Expect(os.WriteFile(filepath.Join(root, ownedFile), []byte("owned"), 0600)).To(gomega.Succeed())
		} else {
			q.Request = *guestOperation(q, "prepare").Request
			q.Files, q.Inventory = nil, nil
			guestOperation(q, "start")
			gomega.Eventually(func() bool {
				view := guestOperation(q, "watch").Observation
				return view.Exit != nil && view.Exit.TerminationProven
			}, 6*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
			stopped = true
			ownedFile = "workspace/input.txt"
		}
		before, err := os.ReadFile(filepath.Join(root, ownedFile))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p, err := openDirectory(parent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer p.close()
		var st unix.Stat_t
		gomega.Expect(unix.Fstatat(int(p.file.Fd()), q.Request.Identity.JobID, &st, unix.AT_SYMLINK_NOFOLLOW)).To(gomega.Succeed())
		bound := &managed.RetainedPath{Device: uint64(st.Dev), Inode: uint64(st.Ino), Directory: true}
		target := "guest_cleanup_bound_root"
		if partial {
			target = "guest_cleanup_bound_preparation"
		}
		previous := spawnBoundary
		defer func() { spawnBoundary = previous }()
		replaced := false
		spawnBoundary = func(name string) {
			if name != target {
				return
			}
			gomega.Expect(replaced).To(gomega.BeFalse())
			gomega.Expect(os.Rename(root, original)).To(gomega.Succeed())
			gomega.Expect(os.Mkdir(root, 0700)).To(gomega.Succeed())
			gomega.Expect(os.WriteFile(filepath.Join(root, "foreign.txt"), []byte("preserve"), 0600)).To(gomega.Succeed())
			replaced = true
		}
		if partial {
			err = cleanupGuestPreparation(p, q, bound, nil)
		} else {
			q.Operation = "cleanup"
			_, err = Guest(q)
		}
		gomega.Expect(err).To(gomega.Equal(ErrConflict))
		gomega.Expect(replaced).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(filepath.Join(root, "foreign.txt"))).To(gomega.Equal([]byte("preserve")))
		gomega.Expect(os.ReadFile(filepath.Join(original, ownedFile))).To(gomega.Equal(before))
		if !partial {
			gomega.Expect(guestOperation(q, "health").Reusable).To(gomega.BeFalse())
		}
	}, ginkgo.Entry("completed native workload", false), ginkgo.Entry("partial preparation", true))
})
