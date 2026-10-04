package utm

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type Lease struct {
	Address netip.Addr
	MAC     string
	Expires time.Time
}

func normalizeMAC(value string) (string, error) {
	mac, err := net.ParseMAC(value)
	if err != nil || len(mac) != 6 {
		return "", ErrUnavailable
	}
	return mac.String(), nil
}

// ReadLease uses the individual expiry, never file modification time. Even an
// expired duplicate makes the MAC lookup ambiguous and therefore unusable.
func ReadLease(reader io.Reader, mac string, now time.Time) (Lease, error) {
	mac, err := normalizeMAC(mac)
	if err != nil {
		return Lease{}, err
	}
	scanner := bufio.NewScanner(io.LimitReader(reader, commandLimit+1))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var fields map[string]string
	var found Lease
	matches, consumed := 0, 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		consumed += len(scanner.Bytes()) + 1
		if consumed > commandLimit {
			return Lease{}, ErrUnavailable
		}
		switch line {
		case "":
			continue
		case "{":
			if fields != nil {
				return Lease{}, ErrUnavailable
			}
			fields = make(map[string]string)
		case "}":
			if fields == nil {
				return Lease{}, ErrUnavailable
			}
			hardware := strings.SplitN(fields["hw_address"], ",", 2)
			if len(hardware) == 2 && hardware[0] == "1" {
				entryMAC, e := normalizeMAC(hardware[1])
				if e == nil && entryMAC == mac {
					matches++
					address, e := netip.ParseAddr(fields["ip_address"])
					expiry, e2 := strconv.ParseInt(fields["lease"], 0, 64)
					if e != nil || !address.Is4() || !address.IsGlobalUnicast() || e2 != nil || expiry <= 0 {
						return Lease{}, ErrUnavailable
					}
					found = Lease{Address: address, MAC: mac, Expires: time.Unix(expiry, 0).UTC()}
				}
			}
			fields = nil
		default:
			key, value, ok := strings.Cut(line, "=")
			if fields == nil || !ok || key == "" {
				return Lease{}, ErrUnavailable
			}
			if _, duplicate := fields[key]; duplicate {
				return Lease{}, ErrUnavailable
			}
			fields[key] = value
		}
	}
	if scanner.Err() != nil || fields != nil || matches != 1 || !found.Expires.After(now) {
		return Lease{}, ErrUnavailable
	}
	return found, nil
}

type GuestIdentity struct {
	VMUUID  string
	MAC     string
	Address netip.Addr
	HostKey string
	BootID  string
	Nonce   string
	OS      string
	Arch    string
}

type IdentityVerifier func(context.Context, Lease, string, string) (GuestIdentity, error)

// ResolveGuest accepts a pre-start lease only when fresh guest proof establishes
// the currently started VM, pinned key, registered MAC, boot and supplied nonce.
// TASK-014 supplies the authenticated transport; no fallback discovery is used.
func (c Control) ResolveGuest(ctx context.Context, reader io.Reader, uuid, mac, hostKey, nonce string, now func() time.Time, verify IdentityVerifier) (Lease, GuestIdentity, error) {
	if now == nil || verify == nil || hostKey == "" || nonce == "" {
		return Lease{}, GuestIdentity{}, ErrUnavailable
	}
	state, err := c.Status(ctx, uuid)
	if err != nil || state != "started" {
		return Lease{}, GuestIdentity{}, ErrUnavailable
	}
	lease, err := ReadLease(reader, mac, now())
	if err != nil {
		return Lease{}, GuestIdentity{}, err
	}
	identity, err := verify(ctx, lease, strings.ToLower(uuid), nonce)
	identityMAC, macErr := normalizeMAC(identity.MAC)
	if err != nil || macErr != nil || !strings.EqualFold(identity.VMUUID, uuid) || identityMAC != lease.MAC ||
		identity.Address != lease.Address || identity.HostKey != hostKey || !uuidPattern.MatchString(identity.BootID) ||
		identity.Nonce != nonce || identity.OS != "darwin" || identity.Arch != "arm64" || !lease.Expires.After(now()) {
		return Lease{}, GuestIdentity{}, ErrUnavailable
	}
	state, err = c.Status(ctx, uuid)
	if err != nil || state != "started" || !lease.Expires.After(now()) {
		return Lease{}, GuestIdentity{}, ErrUnavailable
	}
	return lease, identity, nil
}
