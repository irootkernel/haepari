package utm

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// HostAlias is independent of a DHCP address. It is the only name under which
// a resource's operator-provisioned OpenSSH host key may be accepted.
func HostAlias(uuid string) string { return "haepari-vm-" + strings.ToLower(uuid) }

// ReadPin accepts one exact ed25519 pin for the VM alias in a regular,
// operator-owned known-hosts file. No key discovery or trust-on-first-use occurs.
func ReadPin(reference, uuid string) (string, error) {
	parts := strings.SplitN(reference, ":", 3)
	if len(parts) != 3 || parts[0] != "sha256" || len(parts[1]) != 64 {
		return "", ErrUnavailable
	}
	filename := parts[2]
	if !uuidPattern.MatchString(uuid) || !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
		return "", ErrUnavailable
	}
	f, err := os.OpenFile(filename, os.O_RDONLY, 0)
	if err != nil {
		return "", ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	link, linkErr := os.Lstat(filename)
	if err != nil || linkErr != nil || !info.Mode().IsRegular() || !os.SameFile(info, link) || info.Mode().Perm()&0022 != 0 {
		return "", ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, commandLimit+1))
	if err != nil || len(data) > commandLimit {
		return "", ErrUnavailable
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != parts[1] {
		return "", ErrUnavailable
	}
	s := bufio.NewScanner(bytes.NewReader(data))
	var pin string
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != HostAlias(uuid) {
			return "", ErrUnavailable
		}
		if pin != "" || fields[1] != "ssh-ed25519" {
			return "", ErrUnavailable
		}
		key, err := base64.StdEncoding.DecodeString(fields[2])
		// SSH wire encoding: length-prefixed key type and 32-byte public key.
		if err != nil || len(key) != 51 || binary.BigEndian.Uint32(key[:4]) != 11 ||
			string(key[4:15]) != "ssh-ed25519" || binary.BigEndian.Uint32(key[15:19]) != 32 {
			return "", ErrUnavailable
		}
		pin = fields[1] + " " + fields[2]
	}
	if s.Err() != nil || pin == "" {
		return "", ErrUnavailable
	}
	return pin, nil
}
