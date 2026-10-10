package utm

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/irootkernel/haepari/internal/runner"
)

var guestTransportBootstrap = strings.Replace(guestBootstrap, "runner guest --stdio", "runner guest-transport --stdio", 1)

// Only the small fixed-name manifest enters the shell. Paths are read as data
// and always quoted; the native capability repeats all no-follow checks.
const guestOwnedScript = `set -eu
umask 077
scratch=$(/usr/bin/mktemp -d /private/tmp/haepari-ssh.XXXXXXXX)
trap '/bin/rm -f "$scratch/haepari" "$scratch/request.json" "$scratch/digest" "$scratch/owned"; /bin/rmdir "$scratch"' EXIT
/usr/bin/tar -xf - -C "$scratch"
{
 IFS= read -r root
 IFS= read -r root_id
 IFS= read -r runner_id
} < "$scratch/owned"
case "$root" in /*) ;; *) exit 2 ;; esac
case "$root_id:$runner_id" in *[!0-9:]*|'') exit 2 ;; esac
check_path() {
 component="$root"
 while [ "$component" != / ]; do
  [ ! -L "$component" ] || return 2
  component=${component%/*}
  [ -n "$component" ] || component=/
 done
 [ -d "$root" ] && [ ! -L "$root/haepari" ] && [ -f "$root/haepari" ]
 [ "$(/usr/bin/stat -f '%d:%i' "$root")" = "$root_id" ]
 [ "$(/usr/bin/stat -f '%d:%i' "$root/haepari")" = "$runner_id" ]
 [ "$(/usr/bin/stat -f '%u' "$root")" = "$(/usr/bin/id -u)" ]
 [ "$(/usr/bin/stat -f '%u:%l' "$root/haepari")" = "$(/usr/bin/id -u):1" ]
 size=$(/usr/bin/stat -f '%z' "$root/haepari")
 [ "$size" -ge 1 ] && [ "$size" -le 67108864 ]
 permission=$(/usr/bin/stat -f '%Lp' "$root")
 [ $((0$permission & 077)) -eq 0 ]
 permission=$(/usr/bin/stat -f '%Lp' "$root/haepari")
 [ $((0$permission & 077)) -eq 0 ]
}
check_path
exec 3<"$root/haepari"
[ "$(/usr/bin/stat -L -f '%i' /dev/fd/3)" = "${runner_id#*:}" ]
/bin/dd bs=65536 count=1025 <&3 > "$scratch/haepari" 2>/dev/null
exec 3<&-
check_path
expected=$(/bin/cat "$scratch/digest")
case "$expected" in *[!0-9a-f]*|'') exit 2 ;; esac
[ ${#expected} -eq 64 ]
actual=$(/usr/bin/shasum -a 256 "$scratch/haepari")
[ "${actual%% *}" = "$expected" ]
/bin/chmod 700 "$scratch/haepari"
"$scratch/haepari" runner guest-transport --stdio < "$scratch/request.json"`

// OpenSSH passes the command to the configured login shell. Encode only this
// fixed script so non-POSIX shells cannot interpret its newlines or history
// characters. Variable values remain stdin data, never executable shell text.
var guestOwnedBootstrap = "/bin/sh -c 'exec /bin/sh -c \"$(/usr/bin/printf %s " + base64.StdEncoding.EncodeToString([]byte(guestOwnedScript)) + " | /usr/bin/base64 -D)\"'"

func ownedManifest(q runner.GuestTransportRequest) ([]byte, error) {
	if q.Binding == nil || runner.ValidateGuestTransportBinding(*q.Binding, q.Guest) != nil {
		return nil, ErrUnavailable
	}
	root := filepath.Join(q.Guest.JobRoot, q.Guest.Request.Identity.JobID)
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsAny(root, "\x00\r\n") {
		return nil, ErrUnavailable
	}
	return []byte(fmt.Sprintf("%s\n%d:%d\n%d:%d\n", root, q.Binding.Root.Device, q.Binding.Root.Inode, q.Binding.Runner.Device, q.Binding.Runner.Inode)), nil
}

func compactOperation(op string) bool {
	switch op {
	case "identity", "start", "inspect", "watch", "cancel", "output", "root", "file", "cleanup":
		return true
	}
	return false
}
