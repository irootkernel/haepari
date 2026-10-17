package config

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

func writePrivate(path string, data []byte) {
	ginkgo.GinkgoT().Helper()
	gomega.Expect(os.WriteFile(path, data, 0600)).To(gomega.Succeed())
	gomega.Expect(os.Chmod(path, 0600)).To(gomega.Succeed())
}

func validBytes(doc Document) []byte {
	ginkgo.GinkgoT().Helper()
	data, err := json.Marshal(doc)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return data
}

var _ = ginkgo.Describe("Task023ClosedConfig", ginkgo.Label("unit"), func() {
	ginkgo.It("accepts the documented defaults and keeps partial objects closed", func() {
		gomega.Expect(Defaults().Validate()).To(gomega.Succeed())
		example, err := os.ReadFile("../../docs/examples/config.template.json")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		parsed, presence, err := Parse(example)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(parsed).To(gomega.Equal(Defaults()))
		gomega.Expect(presence).To(gomega.Equal(Presence{GlobalLimit: true, HostLimit: true}))

		partial := []byte(`{"schema":"haepari.config/v1","default_deadlines":{"queue_seconds":10}}`)
		parsed, presence, err = Parse(partial)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(parsed.DefaultDeadlines.QueueSeconds).To(gomega.Equal(int64(10)))
		gomega.Expect(parsed.DefaultDeadlines.RunSeconds).To(gomega.Equal(int64(3600)))
		gomega.Expect(presence.GlobalLimit).To(gomega.BeFalse())
	})

	ginkgo.It("rejects unknown, duplicate, null, wrong-type, and overflowing fields", func() {
		base := `{"schema":"haepari.config/v1","revision":1}`
		for _, raw := range []string{
			`{"schema":"other"}`,
			base + `,"extra":1}`,
			`{"schema":"haepari.config/v1","schema":"haepari.config/v1"}`,
			`{"schema":"haepari.config/v1","revision":null}`,
			`{"schema":"haepari.config/v1","global_limit":"1"}`,
			`{"schema":"haepari.config/v1","global_limit":1.5}`,
			`{"schema":"haepari.config/v1","global_limit":true}`,
			`{"schema":"haepari.config/v1","revision":9223372036854775808}`,
			`{"schema":"haepari.config/v1","limits":{"nope":1}}`,
			`{"schema":"haepari.config/v1","default_deadlines":{"queue_seconds":1,"queue_seconds":2}}`,
			base + ` trailing`,
			``,
		} {
			_, _, err := Parse([]byte(raw))
			gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("configuration refused")))
		}
	})

	ginkgo.It("refuses non-positive values and caps without clamping", func() {
		doc := Defaults()
		doc.Revision = 0
		gomega.Expect(doc.Validate()).To(gomega.MatchError(gomega.ContainSubstring("revision")))
		doc = Defaults()
		doc.HostMACOSGuestLimit = 3
		gomega.Expect(doc.Validate()).To(gomega.MatchError(gomega.ContainSubstring("1 or 2")))
		doc = Defaults()
		doc.Limits.RequestBytes = MaxRequestBytes + 1
		gomega.Expect(doc.Validate()).To(gomega.MatchError(gomega.ContainSubstring("1048576")))
		gomega.Expect(doc.Limits.RequestBytes).To(gomega.Equal(MaxRequestBytes + 1))
		doc = Defaults()
		doc.Limits.LogBytesPerStream = MaxLogBytes + 1
		gomega.Expect(doc.Validate()).To(gomega.MatchError(gomega.ContainSubstring("268435456")))
		doc = Defaults()
		doc.Limits.ArtifactBytes = MaxArtifactBytes + 1
		gomega.Expect(doc.Validate()).To(gomega.MatchError(gomega.ContainSubstring("1073741824")))
		doc = Defaults()
		doc.Limits.ArtifactCount = MaxArtifactCount + 1
		gomega.Expect(doc.Validate()).To(gomega.MatchError(gomega.ContainSubstring("1000")))
		doc = Defaults()
		doc.Limits.EventRetentionDays = 0
		gomega.Expect(doc.Validate()).To(gomega.MatchError(gomega.ContainSubstring("retention")))
		doc = Defaults()
		doc.DefaultDeadlines.CleanupSeconds = -1
		gomega.Expect(doc.Validate()).To(gomega.MatchError(gomega.ContainSubstring("deadlines")))
	})

	ginkgo.It("separates the revision from the values digest", func() {
		doc := Defaults()
		_, document, values, err := doc.Digests()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		doc.Revision = 2
		_, changed, sameValues, err := doc.Digests()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(changed).NotTo(gomega.Equal(document))
		gomega.Expect(sameValues).To(gomega.Equal(values))
	})
})

var _ = ginkgo.Describe("Task023ConfigFiles", ginkgo.Label("unit"), func() {
	ginkgo.It("uses defaults when the root file is absent and refuses an explicit gap", func() {
		root := filepath.Join(ginkgo.GinkgoT().TempDir(), "missing-root")
		doc, fromFile, err := Load(root, Sources{GlobalLimit: 2, HostLimit: 1})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fromFile).To(gomega.BeFalse())
		gomega.Expect(doc.GlobalLimit).To(gomega.Equal(int64(2)))
		gomega.Expect(doc.HostMACOSGuestLimit).To(gomega.Equal(int64(1)))
		_, err = os.Lstat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())

		_, _, err = Load(root, Sources{Path: filepath.Join(root, "absent.json"), ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("missing")))
		_, err = os.Lstat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})

	ginkgo.It("loads a private file outside the root and refuses unsafe paths", func() {
		dir := ginkgo.GinkgoT().TempDir()
		root := filepath.Join(dir, "state")
		external := filepath.Join(dir, "private.json")
		doc := Defaults()
		doc.GlobalLimit = 2
		doc.HostMACOSGuestLimit = 2
		writePrivate(external, validBytes(doc))
		loaded, fromFile, err := Load(root, Sources{Path: external, ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fromFile).To(gomega.BeTrue())
		gomega.Expect(loaded.GlobalLimit).To(gomega.Equal(int64(2)))
		gomega.Expect(loaded.HostMACOSGuestLimit).To(gomega.Equal(int64(2)))
		_, err = os.Lstat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())

		inside := filepath.Join(dir, "inside.json")
		writePrivate(inside, validBytes(Defaults()))
		_, fromFile, err = Load(dir, Sources{Path: inside, ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fromFile).To(gomega.BeTrue())

		_, _, err = Load(dir, Sources{Path: "\x00", ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("unsafe")))
		_, _, err = Load(dir, Sources{ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("empty")))

		link := filepath.Join(dir, "linked.json")
		gomega.Expect(os.Symlink(external, link)).To(gomega.Succeed())
		_, _, err = Load(dir, Sources{Path: link, ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("symlink")))
		linkedDir := filepath.Join(dir, "linked-dir")
		gomega.Expect(os.Symlink(dir, linkedDir)).To(gomega.Succeed())
		_, _, err = Load(dir, Sources{Path: filepath.Join(linkedDir, "private.json"), ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("symlink")))

		shared := filepath.Join(dir, "shared.json")
		writePrivate(shared, validBytes(Defaults()))
		gomega.Expect(os.Chmod(shared, 0644)).To(gomega.Succeed())
		_, _, err = Load(dir, Sources{Path: shared, ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("private")))
		gomega.Expect(os.Chmod(shared, 0600)).To(gomega.Succeed())
		alias := filepath.Join(dir, "alias.json")
		gomega.Expect(os.Link(shared, alias)).To(gomega.Succeed())
		_, _, err = Load(dir, Sources{Path: alias, ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("private")))

		gomega.Expect(os.Mkdir(filepath.Join(dir, "subdir"), 0700)).To(gomega.Succeed())
		_, _, err = Load(dir, Sources{Path: filepath.Join(dir, "subdir"), ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("private")))

		socketPath := filepath.Join(dir, "c.sock")
		listener, err := net.Listen("unix", socketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer listener.Close()
		_, _, err = Load(dir, Sources{Path: socketPath, ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).NotTo(gomega.ContainSubstring("differs"))

		empty := filepath.Join(dir, "empty.json")
		writePrivate(empty, nil)
		_, _, err = Load(dir, Sources{Path: empty, ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("empty or too large")))
		huge := filepath.Join(dir, "huge.json")
		writePrivate(huge, []byte(strings.Repeat("a", 1<<20+1)))
		_, _, err = Load(dir, Sources{Path: huge, ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("empty or too large")))
	})

	ginkgo.It("refuses conflicting explicit limits and keeps implicit defaults out of the file", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "limits.json")
		doc := Defaults()
		doc.GlobalLimit = 2
		doc.HostMACOSGuestLimit = 2
		writePrivate(path, validBytes(doc))
		sources := Sources{Path: path, ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1, GlobalExplicit: true}
		_, _, err := Load(dir, sources)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("differs")))

		sources.GlobalExplicit = false
		sources.HostExplicit = true
		sources.HostLimit = 1
		_, _, err = Load(dir, sources)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("host_macos_guest_limit")))

		sources.HostExplicit = false
		loaded, _, err := Load(dir, sources)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(loaded.GlobalLimit).To(gomega.Equal(int64(2)))
		gomega.Expect(loaded.HostMACOSGuestLimit).To(gomega.Equal(int64(2)))

		omitted := filepath.Join(dir, "omitted.json")
		writePrivate(omitted, []byte(`{"schema":"haepari.config/v1","revision":1}`))
		loaded, _, err = Load(dir, Sources{Path: omitted, ConfigExplicit: true, GlobalLimit: 4, HostLimit: 2, GlobalExplicit: true, HostExplicit: true})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(loaded.GlobalLimit).To(gomega.Equal(int64(4)))
		gomega.Expect(loaded.HostMACOSGuestLimit).To(gomega.Equal(int64(2)))
		loaded, _, err = Load(dir, Sources{Path: omitted, ConfigExplicit: true, GlobalLimit: 9, HostLimit: 2})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(loaded.GlobalLimit).To(gomega.Equal(int64(1)))
		gomega.Expect(loaded.HostMACOSGuestLimit).To(gomega.Equal(int64(1)))
	})

	ginkgo.It("resolves a relative private file from the working directory and leaves the root unchanged", func() {
		parent := ginkgo.GinkgoT().TempDir()
		cwd, err := os.Getwd()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Chdir(parent)).To(gomega.Succeed())
		defer func() { gomega.Expect(os.Chdir(cwd)).To(gomega.Succeed()) }()
		doc := Defaults()
		doc.GlobalLimit = 2
		doc.HostMACOSGuestLimit = 2
		writePrivate(filepath.Join(parent, "private.json"), validBytes(doc))
		root := filepath.Join(parent, "missing-root")
		loaded, fromFile, err := Load(root, Sources{Path: "private.json", ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fromFile).To(gomega.BeTrue())
		gomega.Expect(loaded.GlobalLimit).To(gomega.Equal(int64(2)))
		gomega.Expect(loaded.HostMACOSGuestLimit).To(gomega.Equal(int64(2)))
		_, err = os.Lstat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())

		_, _, err = Load(root, Sources{Path: "missing.json", ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("missing")))
		_, err = os.Lstat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses a named pipe without blocking", func() {
		dir := ginkgo.GinkgoT().TempDir()
		fifo := filepath.Join(dir, "config.fifo")
		gomega.Expect(unix.Mkfifo(fifo, 0600)).To(gomega.Succeed())
		done := make(chan error, 1)
		go func() {
			_, _, err := Load(dir, Sources{Path: fifo, ConfigExplicit: true, GlobalLimit: 1, HostLimit: 1})
			done <- err
		}()
		select {
		case err := <-done:
			gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("private")))
		case <-time.After(2 * time.Second):
			ginkgo.Fail("opening a configuration fifo blocked")
		}
	})
})
