package utm

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("RegisteredHostPin", ginkgo.Label("unit"), func() {
	ginkgo.It("binds the UUID alias and exact file bytes without trusting an address", func() {
		key := base64.StdEncoding.EncodeToString(append([]byte{0, 0, 0, 11, 's', 's', 'h', '-', 'e', 'd', '2', '5', '5', '1', '9', 0, 0, 0, 32}, make([]byte, 32)...))
		filename := filepath.Join(ginkgo.GinkgoT().TempDir(), "known_hosts")
		content := HostAlias(vmID) + " ssh-ed25519 " + key + "\n"
		write := func(data string) string {
			gomega.Expect(os.WriteFile(filename, []byte(data), 0600)).To(gomega.Succeed())
			digest := sha256.Sum256([]byte(data))
			return "sha256:" + hex.EncodeToString(digest[:]) + ":" + filename
		}
		ref := write(content)
		pin, err := ReadPin(ref, vmID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(pin).To(gomega.Equal("ssh-ed25519 " + key))
		_, err = ReadPin(write("# operator pin\n\n"+content+"\n"), vmID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ref = write(content)
		link := filepath.Join(filepath.Dir(filename), "pin-link")
		gomega.Expect(os.Symlink(filename, link)).To(gomega.Succeed())
		_, err = ReadPin(strings.Replace(ref, filename, link, 1), vmID)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(filename, []byte(content+"# changed\n"), 0600)).To(gomega.Succeed())
		_, err = ReadPin(ref, vmID)
		gomega.Expect(err).To(gomega.HaveOccurred())
		for _, data := range []string{content + content, strings.ReplaceAll(content, HostAlias(vmID), "192.168.64.3"), strings.ReplaceAll(content, key, "AAAA"), strings.ReplaceAll(content, "ssh-ed25519", "ssh-rsa")} {
			_, err := ReadPin(write(data), vmID)
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		ref = write(content)
		gomega.Expect(os.Chmod(filename, 0666)).To(gomega.Succeed())
		_, err = ReadPin(ref, vmID)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
