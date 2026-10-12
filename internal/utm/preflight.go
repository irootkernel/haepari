package utm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
)

// This probe evaluates no project text, transfers no runner and writes nothing
// in the guest. The registered MAC and fresh challenge are stdin data only.
const guestPrerequisiteProbe = `/bin/sh -c 'set -eu
IFS= read -r challenge
IFS= read -r mac
/sbin/ifconfig -a | /usr/bin/awk -v expected="$mac" '\''$1 == "ether" && tolower($2) == expected { found = 1 } END { if (!found) exit 2 }'\''
/usr/sbin/sysctl -n kern.bootsessionuuid
/usr/bin/uname -s
/usr/bin/uname -m
printf "%s\n%s\n" "$challenge" "$mac"
'`

// PrerequisiteError exposes only a bounded check name, never key paths,
// SSH diagnostics or guest output. Unwrap preserves the original cause.
type PrerequisiteError struct {
	Check string
	Cause error
}

func (e *PrerequisiteError) Error() string { return "UTM prerequisite unavailable: " + e.Check }
func (e *PrerequisiteError) Unwrap() error { return e.Cause }

func prerequisiteFailure(check string, err error) error {
	return &PrerequisiteError{Check: check, Cause: errors.Join(ErrUnavailable, err)}
}

func (s SSH) Preflight(ctx context.Context, spec managed.ResourceSpec, leases io.Reader) (GuestIdentity, error) {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return GuestIdentity{}, prerequisiteFailure("resource_spec", ErrUnavailable)
	}
	if _, err := managed.DecodeResource(encoded); err != nil || spec.Kind != "utm-macos" || spec.VM == nil || spec.Guest == nil ||
		!filepath.IsAbs(s.Executable) || filepath.Clean(s.Executable) != s.Executable {
		return GuestIdentity{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	key, err := ReadPin(spec.Guest.KnownHostsReference, spec.VM.UTMVMUUID)
	if err != nil {
		return GuestIdentity{}, prerequisiteFailure("pinned_key", err)
	}
	mapping, err := readMapping(spec.Guest.ConnectionReference)
	if err != nil {
		return GuestIdentity{}, prerequisiteFailure("ssh_mapping", err)
	}
	dir, err := os.MkdirTemp("/private/tmp", "haepari-preflight-pin-*")
	if err != nil {
		return GuestIdentity{}, prerequisiteFailure("host_snapshot", err)
	}
	defer os.Remove(dir)
	pin := filepath.Join(dir, "known_hosts")
	defer os.Remove(pin)
	if err := os.WriteFile(pin, []byte(HostAlias(spec.VM.UTMVMUUID)+" "+key+"\n"), 0600); err != nil {
		return GuestIdentity{}, prerequisiteFailure("host_snapshot", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return GuestIdentity{}, err
	}
	mac, err := normalizeMAC(spec.VM.MACAddress)
	if err != nil {
		return GuestIdentity{}, err
	}
	run := s.Run
	if run == nil {
		run = runSSH
	}
	verify := func(ctx context.Context, lease Lease, uuid, challenge string) (GuestIdentity, error) {
		args := pinnedSSHArguments(mapping, pin, uuid, lease.Address.String(), guestPrerequisiteProbe)
		r, err := run(ctx, s.Executable, args, []byte(challenge+"\n"+mac+"\n"))
		if err != nil || r.ExitCode != 0 || len(r.Stdout) > 1024 || len(r.Stderr) > commandLimit {
			return GuestIdentity{}, errors.Join(ErrUnavailable, err)
		}
		fields := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
		if len(fields) != 5 {
			return GuestIdentity{}, ErrUnavailable
		}
		return GuestIdentity{VMUUID: uuid, MAC: fields[4], Address: lease.Address, HostKey: key,
			BootID: fields[0], OS: strings.ToLower(fields[1]), Arch: fields[2], Nonce: fields[3]}, nil
	}
	check := "guest_identity"
	_, identity, err := s.Control.resolveGuest(ctx, leases, spec.VM.UTMVMUUID, mac, key, hex.EncodeToString(nonce[:]), time.Now, verify, &check)
	if err != nil {
		return GuestIdentity{}, prerequisiteFailure(check, err)
	}
	return identity, err
}
