# TASK-034 UTM probe record

Status: accepted host probe, run on 2026-09-28 (Design Revision 3.3). It is evidence for TASK-013 through TASK-016, not backend qualification.

## Environment

| Item | Value |
| --- | --- |
| Host | Apple M5 Pro, 64 GB, macOS 27.0 (26A428) |
| UTM | 4.7.5 (`/Applications/UTM.app/Contents/MacOS/utmctl`) |
| VM | The owner's existing macOS guest on the Apple Virtualization backend, designated by the owner |
| Session | Logged-in Aqua session with the screen unlocked |
| Probe | A scratch Go program, ad-hoc signed by the Go linker. Each command ran once as its own LaunchAgent in `gui/501`, with `LimitLoadToSessionType=Aqua` and `AbandonProcessGroup=true`, and each job was booted out after it ran. |

The probe ran before TASK-001; no Haepari code existed yet.

## Observations

**Control from the service context**
- These `utmctl` commands all ran from the LaunchAgent: `version`, `list`, `status`, `start`, `stop --request`, forced `stop`, `clone` and `delete`.
- No Automation (TCC) prompt appeared. UTM checked each call with TCC, and every check passed without user input. TCC attributed each call to the probe executable (code identifier `a.out`, ad-hoc signature, no team ID) as the responsible process.
- No signing identity was needed. The keychain held no code-signing identity at all.
- By contrast, `osascript` sending Apple Events to UTM raised an Automation prompt for the terminal app. UTM's AppleScript `configuration` returns the MAC address but no machine identifier.
- Reading `~/Library/Containers/com.utmapp.UTM/Data/`, for example a VM's `config.plist`, failed with `EPERM` and no prompt. This happened from the LaunchAgent and from the terminal alike.

**Result signalling**

| Command | Output | Exit |
| --- | --- | --- |
| `utmctl ip-address` (Apple backend) | stderr: `Error from event: ... (OSStatus error -2700.) Operation not supported by the backend.` | 0 |
| `utmctl status` with an unknown UUID | `Error: Virtual machine not found.` | 1 |
| `clone`, `start`, `stop`, `delete` (success) | Nothing | 0 |

**Stopping**
- `utmctl stop --request` exited 0 at once, but the macOS guest was still running 240 s later. The owner shut it down from inside the guest.
- A forced `utmctl stop` (the default) stopped a guest in about 0.35 s.

**Address discovery**
- `/var/db/dhcpd_leases` is world-readable. The running guest's lease, found by MAC, matched both the host ARP entry and the MAC in the VM's configuration.
- Each lease lasted one hour. The file still held an expired lease for a VM that last ran in August.
- DHCP DISCOVER arrived about 8 s after `utmctl start`.
- The guest's TCP port 22 was closed, because that VM did not have Remote Login enabled.

**Clone behaviour (recorded for completeness; the design no longer clones, see [ADR-0018](../architecture-decision-records/0018-persistent-vm-no-reset.md))**
- `utmctl clone` took 0.4 s and used about 4 MB (APFS copy-on-write).
- The clone got a new VM UUID but kept the source's MAC address and drive identifier.
- Source and clone were then run together:
  - The host DHCP server acknowledged both guests.
  - The lease table held one entry for the shared MAC.
  - The host bridge learned that MAC on only one of the two guest interfaces, so only one guest could be reached.
- The clone was force-stopped and deleted by UUID, and the source was started again.

## Decisions for TASK-013 and TASK-014

- Control UTM only through `utmctl`. Treat a non-zero exit, or `Error from event` on stderr, as a failure.
- Do not rely on `stop --request`. Haepari does not shut the VM down in normal operation; during recovery it may force-stop it.
- Find the guest address from the DHCP lease for the registered MAC, accept only a current lease, and verify the SSH host key.
- Require no signing identity. Do not use AppleScript or read UTM's data container.
- Refuse to register two resources with the same UUID or MAC address.

## TASK-016 prerequisites (not observed here)

- The host macOS guest limit, because no third macOS VM was available; observing Apple's limit needs three macOS VMs.
- Whether DHCP leases stay current for a VM that runs for days; each lease lasted one hour.
- How `utmctl` behaves while the screen is locked or the user is logged out.
- A guest with Remote Login and the test user's SSH key, needed to time boot-to-SSH.
