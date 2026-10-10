# ADR-0019: Reuse an immutable per-Job runner for bounded SSH exchanges

Status: Implemented and fixture-verified for TASK-037 under the approved EPIC-009 envelope; real-VM qualification remains TASK-038.

## Measured basis

Before protocol changes, the existing SSHRunner seam was exercised with three
`inspect` calls and a 20,040,352-byte product executable. The scripted transport
received six archives, six complete executable transfers, 120,271,872 archive
bytes and 4,221 response bytes. Each Call preserves its separate pinned identity
proof and operation. These are observed application bytes in a fixture, not SSH
packet overhead, network throughput or a real VM result. The retained test also
uses a deterministic 8 MiB payload when no selected binary is supplied.

## F2 decision, recorded before protocol code

Add a separate closed `haepari.guest-transport-request/v1` /
`haepari.guest-transport-reply/v1` capability around the unchanged guest v1/v2
request and reply. Preparation uses the full bootstrap and returns the immutable
Job root and owned runner device/inode, digest, original request and VM identity.
The host saves that binding separately from frozen native receipts. Prepared
operations transfer structured data without executable bytes; the constant
bootstrap verifies the original root/file incarnation and copies those pinned
bytes into its own private scratch directory before execution. The native entry
point independently checks the binding with no-follow descriptors. A replacement
root or runner, including equal bytes in a new inode, refuses. No shared mutable
executable, permanent guest service, SSH fallback or second issued effect exists.

Retain both authenticated exchanges, individual lease expiry, challenge, host-key
pin, original boot/run/lease/configuration identity and original phase deadline.
Reads remain bounded to 64 KiB with their existing file-incarnation checks.
A new immutable admission marker selects this capability before preparation.
Uncertain capable preparation retains its full new bootstrap; historical Jobs
without that marker keep the full original bootstrap against their old binary. Loss of compact transport never reissues a workload.

After completed cleanup, a separately digest-pinned current verifier may execute
only the original read-only `health` request through this capability. It cannot
prepare, start, collect or delete using replacement workload bytes. The original
outside control, native termination, root absence and full guest reply validation
remain mandatory. The same capability carries an optional original
`guest-recovery-request` for read-only attested health: the separate executor
digest never changes the saved recovery digest, attestation or cleanup receipt,
and an existing Job root refuses this verifier path. This permits finished host runner payloads to enter explicit
purge while old request keys and required receipts remain retained and future
resource health remains observable after a controller build changes.

The capability has its own versioned wire shape; it does not change frozen guest,
native runner, public result, Unix envelope or SQLite schema identifiers. The new
host binding is additive private event data. Isolated development-service plans
with explicit execution limits use a new v2 plan; historical v1 plans keep exact
default-one arguments. General configuration loading remains TASK-023.

## Verification and qualification boundary

Compare the same actual archive seam before and after, including bounded file
reads, changed root/runner/file refusal and interrupted transfer. Exercise safe
finished purge, protected active/unresolved runners and post-purge health with
different verifier bytes. TASK-038 must measure real network/protocol overhead
and elapsed campaign behavior for an explicitly approved installed candidate;
fixture archive savings cannot establish that result.


## Candidate fixture comparison and operating handoff

The same 20,040,352-byte executable (SHA-256
`5dc51892072704a1ba6abc2db19d789af056e4d8fb2bee9b6851a65d22d39227`)
and three prepared inspect Calls produced six compact exchanges, zero executable
transfers, 39,936 archive bytes and 13,767 response bytes at the scripted seam.
The original baseline was six transfers and 120,271,872 archive bytes. Both
retain six separately authenticated exchanges. The generated wrapper increases
response bytes; neither result measures SSH framing or network speed. Actual
native bootstrap tests independently verify execution, bounded file reads and
changed-incarnation refusal rather than treating this mock reply as native proof.

For TASK-038 use the documented explicit global-three/host-two startup or v2
service plan, finite admitted phase budgets, permanent request keys, public
phase timings and bounded exchange-pair records. Collect incomplete-pair/detail-
limit gaps before purge and measure packet overhead separately. The operating
controls and exact-candidate live authority boundary are in
[operations](../operations.md#task-037-operating-controls-and-task-038-handoff).
