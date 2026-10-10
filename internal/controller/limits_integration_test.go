//go:build integration

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task037ControllerStartupLimits", ginkgo.Label("integration"), func() {
	ginkgo.It("refuses invalid options before creating a state root", func() {
		root, err := state.Resolve(filepath.Join(ginkgo.GinkgoT().TempDir(), "absent"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, options := range []Options{{GlobalLimit: 0, HostMACOSGuestLimit: 1}, {GlobalLimit: -1, HostMACOSGuestLimit: 1}, {GlobalLimit: 2, HostMACOSGuestLimit: 3}} {
			gomega.Expect(RunWithOptions(context.Background(), root, options)).To(gomega.MatchError(managed.ErrInvalid))
			_, err = os.Lstat(root.Path)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})
	for _, limit := range []int{1, 2, 3} {
		ginkgo.It(fmt.Sprintf("admits %d independent native workers through the real Unix controller and reuses a freed slot", limit), func() {
			t := ginkgo.GinkgoT()
			parent, err := filepath.EvalSymlinks(t.TempDir())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			root, err := state.Resolve(filepath.Join(parent, "state"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			source := filepath.Join(parent, "input")
			signal := filepath.Join(parent, "signal")
			gomega.Expect(os.Mkdir(source, 0700)).To(gomega.Succeed())
			gomega.Expect(os.Mkdir(signal, 0700)).To(gomega.Succeed())
			gomega.Expect(os.Mkdir(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
			gomega.Expect(os.Mkdir(filepath.Join(parent, "sealing"), 0700)).To(gomega.Succeed())
			gomega.Expect(os.WriteFile(filepath.Join(source, "source", "input.txt"), []byte("sealed"), 0600)).To(gomega.Succeed())
			store, err := snapshot.OpenStore(filepath.Join(parent, "sealing"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				if limit == 1 {
					done <- Run(ctx, root)
				} else {
					done <- RunWithOptions(ctx, root, Options{GlobalLimit: limit, HostMACOSGuestLimit: 1})
				}
			}()
			t.Cleanup(func() { cancel(); gomega.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.Succeed())) })
			var c *client.Client
			gomega.Eventually(func() error { var e error; c, e = client.Connect(context.Background(), root); return e }, 5*time.Second, 25*time.Millisecond).Should(gomega.Succeed())
			defer c.Close()
			ids := map[string]string{}
			digest := strings.Repeat("a", 64)
			for i := 0; i < 3; i++ {
				name := fmt.Sprintf("local-%d", i)
				resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: name, Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-native-limits"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
				_, err = c.Register(context.Background(), mustJSONScheduling(resource))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: name, Mode: "auto", Suite: "limits", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt"}}, Environment: managed.Environment{ProfileID: "development", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/sh", "-c", "printf started > \"$SIGNAL/$NAME.started\"; while [ ! -f \"$SIGNAL/$NAME.release\" ]; do sleep .02; done; mkdir -p ../results; printf complete > ../results/output.txt"}, CWD: "source", Environment: map[string]string{"SIGNAL": signal, "NAME": name}, RequiredOutputs: []string{"results/output.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 60, PrepareSeconds: 30, RunSeconds: 30, CollectSeconds: 30, CleanupSeconds: 30}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "isolated-native-workers"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
				r, _, e := c.Submit(context.Background(), mustJSONScheduling(job), "limit-"+name)
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				ids[name] = r.JobID
			}
			started := func() []string {
				entries, _ := os.ReadDir(signal)
				names := []string{}
				for _, entry := range entries {
					if strings.HasSuffix(entry.Name(), ".started") {
						names = append(names, strings.TrimSuffix(entry.Name(), ".started"))
					}
				}
				return names
			}
			gomega.Eventually(func() int { return len(started()) }, 10*time.Second, 25*time.Millisecond).Should(gomega.Equal(limit))
			gomega.Consistently(func() int { return len(started()) }, 200*time.Millisecond, 25*time.Millisecond).Should(gomega.Equal(limit))
			first := started()[0]
			gomega.Expect(os.WriteFile(filepath.Join(signal, first+".release"), nil, 0600)).To(gomega.Succeed())
			if limit < 3 {
				gomega.Eventually(func() int { return len(started()) }, 10*time.Second, 25*time.Millisecond).Should(gomega.Equal(limit + 1))
			}
			for name := range ids {
				gomega.Expect(os.WriteFile(filepath.Join(signal, name+".release"), nil, 0600)).To(gomega.Succeed())
			}
			for _, id := range ids {
				var result managed.Result
				gomega.Eventually(func() error {
					var e error
					result, _, e = c.Wait(context.Background(), id, 100*time.Millisecond)
					return e
				}, 10*time.Second, 25*time.Millisecond).Should(gomega.Succeed())
				gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
			}
		})
	}
})
