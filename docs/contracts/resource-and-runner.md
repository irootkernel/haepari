# Resource, UTM, and runner contract

## Resource definition

`haepari.resource/v1` is operator-owned configuration. It names `id`, `kind` (`local`, `utm-macos` or `lock`), `execution_mode` (`managed`, for `local` and `utm-macos` only), positive capacity and configuration revision. A `lock` resource has no adapter or execution; the [lock contract](locks.md) defines it, and it counts toward no managed or global capacity. `local` and `utm-macos` require managed execution. Definitions include owned paths, pinned adapter identities and lifecycle budgets. A `utm-macos` definition names one persistent, operator-owned UTM VM ([ADR-0018](../architecture-decision-records/0018-persistent-vm-no-reset.md)): its UTM VM UUID, the MAC address used for address discovery, the pinned SSH host key and SSH mapping, the guest job root that Haepari owns, and `vm.allow_automatic_stop` (boolean, default `false`). This operator authorization permits capacity-driven forced stops only while the resource is otherwise eligible; absence of a Haepari Job does not establish that guest services or operator work are safe to interrupt. Two resources cannot name the same VM UUID or MAC address. A job's profile digest is consumer evidence that Haepari records; it is not matched against the VM, which keeps whatever earlier jobs left. A local job names an explicit local environment reference instead. Public external resources are deferred; reject them as unsupported in v0.1.0.

A submitted job cannot overwrite this definition. Local resources may use a trusted operator-defined prepare/cleanup command; job inputs supply only workload bytes and permitted parameters. A resource advertised as isolated must actually use an isolating backend. The generic local adapter makes no sandbox claim. For a local command resource, reusable means the owned process group has settled and its declared cleanup/health checks passed; it does not mean arbitrary host filesystem changes were rolled back. Shared-state resources such as a test DB require operator-provided reset and verification hooks.

Registering or updating a `utm-macos` resource is revision-fenced. It checks that UTM lists a VM with that UUID, that no other resource names the same UUID or MAC address, and that the SSH host key pin is present. Registration never creates, clones, boots or deletes a VM. Configuration changes need an idle resource with no pending or uncertain power transition. Removing a resource never deletes its VM.



## Adapter phases

Every Job-phase managed adapter call carries the job ID, resource ID, expected resource/configuration revision, lease generation, nonce, input/profile digests, and phase deadline. Responses identify the same tuple. Capacity-driven victim stops use the separate internal power-transition intent, which binds the victim and target identities/revisions and target Job/lease generation; an idle victim has no Job lease to borrow.

| Phase | Allowed effects | Required evidence |
| --- | --- | --- |
| Inspect | Read registration/capabilities only | Exact identity and readiness/gaps |
| Prepare | Start the VM if it is stopped, connect, stage input into a fresh job directory | VM identity (UUID, MAC-to-address lease, SSH host key), guest/run identity, staged digests |
| Execute | Start or reattach one exact runner | Durable run claim, start identity, observation endpoint |
| Collect | Read declared outputs into the owned host job root | Artifact inventory, sizes/digests, completeness |
| Cleanup | Prove owned workload/collector termination; remove Haepari-owned files only when no required uncollected evidence is protected | Termination proof, removed or protected-path inventory and reason |
| Verify | Check the VM identity, that no Haepari runner remains and that no protected owned path blocks reuse | Verified reusable state or quarantine reason |

If preparation produced no owned Job effect requiring cleanup and left no protected evidence obligation, Job cleanup can be `not_needed`; a VM start is a separate power prerequisite that must still be reconciled. An effectful path whose cleanup succeeds records `verified`. After any possible Job effect, failure or timeout invokes collection and cleanup within their own limits. No adapter returns reusable=true from a process exit alone.

## UTM control

UTM documents a CLI wrapper around AppleScript. Its examples for guest command and file access assume a QEMU Guest Agent, which a macOS guest on the Apple backend does not have. The Haepari protocol does not invent unavailable `utmctl` flags. See [S1 through S3](../sources.md).

TASK-034 probed the installed UTM version, Apple backend, host OS and `utmctl` behaviour in a logged-in, unlocked session ([probe record](../records/TASK-034-utm-probe.md)). TASK-016 qualifies them, together with the guest OS and the behaviour when the screen is locked or the user is logged out. Missing ability to complete a safe job cycle blocks the backend. Do not silently switch to QEMU, a beta build, or a different VM manager.

The v0.1.0 strategy is one persistent VM per resource, with no reset between jobs ([ADR-0018](../architecture-decision-records/0018-persistent-vm-no-reset.md)). The adapter must:

1. Control the VM only through `utmctl` `status`, `start` and `stop`, addressed by the registered UUID. It never runs `clone` or `delete`. It uses neither AppleScript, which raised an Automation prompt in the probe, nor UTM's data container, which was unreadable.
2. Treat a `utmctl` call as failed when it exits non-zero or prints `Error from event` on stderr. On the Apple backend, `ip-address` fails this way with exit 0.
3. Use the VM when `status` is `started`, and start it when it is `stopped`, within the host guest limit. Any other status, such as `paused` or `starting`, makes the VM unavailable (see below). Find its address in the host DHCP lease table by the registered MAC address under the lease validity rule below, and connect over SSH with the pinned host key.
4. Transfer only the sealed input bundle and the job's guest runner into a fresh job directory under the guest job root. Validate it before project installation begins.
5. After execution, establish owned workload termination, collect evidence with a bounded collector, establish collector termination, and remove the job directory only when doing so will not destroy required uncollected evidence. If evidence must remain, durably record its owned paths and gaps, quarantine the VM, and use the [recovery contract](managed-transitions.md#evidence-preserving-settlement). Whatever the workload changed elsewhere in the guest persists; consumer recipes own their starting state and cleanup.
6. Do not shut the VM down after a job. Force-stop it with `utmctl stop` only to make room under the host guest limit (see below) or during recovery. `utmctl stop --request` is not used; it did not stop the probed guest within 240 s.

A name match, empty `utmctl` output, or a successful command alone is not identity evidence. Haepari never edits a VM package.

Reset strategies, such as a whole-VM copy, UTM snapshots or a disposable start, are outside v0.1.0. A beta UTM release documents snapshots and a disposable start mode for Apple Virtualization guests on macOS 27 ([S9](../sources.md)); adopting one later needs its own decision and qualification.

## SSH and guest supervision

Use the configured system SSH/SFTP executable through argv, with pinned known-hosts material, BatchMode, no agent forwarding, no ambient remote command configuration, and bounded connection timeouts. Paths and values that cross the remote shell boundary are encoded through a fixed runner request file; a project command is not concatenated into `ssh ... "..."`.

The guest identity check combines the pinned SSH host key, the registered VM UUID and MAC-to-address lease, observed OS/architecture, boot identity, and a fresh nonce. Credentials are handled as secret references or preprovisioned guest state with explicit approval, never as input-bundle files.

The transient runner accepts an authenticated, contained run request and provides `start`, `inspect`, `watch`, and `cancel` operations. It is launched using a qualified detach/supervision method that survives transport loss. It writes a durable request/spawn intent, actual start receipt when known, append-only output, and exit receipt under an owned guest job path, following the [receipt protocol](#runner-receipts). The digest-pinned runner artifact travels with each job's sealed bundle. Repeated start for the same run is idempotent; a conflicting request is rejected. A recorded ambiguous spawning state cannot be restarted automatically.

`watch` blocks on the same run. On transport loss the host may reconnect and observe; it must not invoke the original test command again. A guest reboot changes boot identity and prevents blind PID reuse. Cancellation waits for owned workload termination and still requires the job's cleanup. Stopping an SSH process does not terminate the semantic job.

## Runner receipts

Local and guest runners keep receipts as files in an owned run directory: `request.json`, `intent.json` (spawn intent), `start.json`, `exit.json` and append-only output.

**How each receipt is written:**

1. Write to a temporary file in the same directory.
2. Sync the file.
3. Rename it into place.
4. Sync the directory.

**Order of events:**

1. The controller commits the run claim and spawn intent to SQLite.
2. The runner writes `intent.json` before spawning.
3. It writes `start.json` once the process identity is observed.
4. It writes `exit.json` after termination.

**How the controller uses them.** The controller imports receipts into SQLite. A missing later receipt is uncertainty, never success. After a restart, `intent.json` without `start.json` marks the ambiguous spawn window: resolve it through process evidence or leave it uncertain, and never spawn again.

## Recovery and fencing

Before forced recovery, produce a plan naming the exact VM, resource revision, current lease/run, evidence that may be lost, and proposed stop effects. The confirmation token binds those observations. A changed revision or new lease invalidates the token.

Recovery either proves the original execution state and finishes normal collection and cleanup, or records INCOMPLETE with protected required evidence. If owned workload or collector termination is unproven, the Job retains occupancy in reconciliation. If termination is proven but required guest evidence is retained, the Job may settle INCOMPLETE and return occupancy while the resource stays quarantined. Resource recovery re-collects or explicitly disposes of that evidence, removes owned files and verifies health before reuse. Recovery never fabricates a workload PASS or rewrites a terminal verdict. Other resources can continue.

The managed resource lease authorizes Job effects. The internal power-transition intent authorizes a capacity-driven stop of an idle victim; it cannot be inferred from the target Job lease alone. A recovery token prevents stale cooperating components from changing newer state; it is not an OS security barrier against same-user code.

## Host session and VM identity

Host UTM control runs `utmctl` from the development or release LaunchAgent in the logged-in Aqua session. In the TASK-034 probe, an ad-hoc-signed LaunchAgent ran every `utmctl` command it tried without an Automation prompt, so no stable signing identity is required. The UTM command-line tool states that it does not work from SSH sessions or before login ([S10](../sources.md)). SSH execution inside the guest is a separate transport and has its own login/key prerequisites. The controller's local queue and local-command backend do not require an Aqua session merely because UTM control may. **Recording.** Record the observed UTM, macOS, backend, user-session and permission tuple during TASK-034 and TASK-016. A version list in a report is not qualification evidence.

**Loss of session or capacity.** Session or host-capacity loss blocks new VM dispatch temporarily. A VM whose job processes or cleanup Haepari cannot confirm is quarantined.

**Host macOS guest limit.**

- Apple's macOS license allows at most two additional macOS instances in virtual machines per Mac, and the Virtualization framework reports a limit error when one is exceeded ([S11](../sources.md)). The operator's own VMs count too.
- The controller setting `host_macos_guest_limit` (default 1, schema maximum 2) bounds registered VMs that are running, starting, or in an uncertain power transition. When a Job needs a stopped VM at the limit, Haepari may force-stop the longest-idle **registered** VM with `vm.allow_automatic_stop=true`, no Job occupancy, availability `available`, and no pending/uncertain power transition. Paused resources are protected for maintenance even with no Job. Quarantined, recovering, occupied and transitioning resources are also ineligible. No unregistered VM is selected or stopped.
- **Longest-idle ordering.** Persist a UTC `idle_since` when managed occupancy changes from one to zero, or at registration of a new resource with zero occupancy. Compare this value ascending, then resource ID ascending. Restart, configuration updates, pause/resume and queued cancellation/expiry do not reset it. A legacy resource whose historical idle time cannot be established has an unknown value and cannot be selected automatically until a trustworthy one-to-zero transition supplies it.
- One short SQLite transaction reserves the requesting Job and its target-start claim, records the selected victim's power-transition intent, and binds the target/victim resource IDs, VM UUIDs, configuration revisions, Job/lease generation and operation generation before a subprocess runs. The intent blocks new Job reservations, configuration changes and conflicting power operations on the victim. The target claim is tied to that victim: the victim continues to count until its stop is observed; only then may the target start consume the transferred place. A pending start and an uncertain start or stop each count conservatively, so concurrent starts cannot spend the same place. No SQLite transaction spans `utmctl`.
- On restart or a stale response, reconcile the exact command intent against observed `utmctl` state and recorded identities before clearing protection. A revision mismatch cannot undo a command already issued. If stop issuance or its effect is uncertain, keep the victim protected and the target unstarted; do not retry blindly. A confirmed no-effect prerequisite failure follows the existing INCOMPLETE/no-start path. If no eligible victim exists before reservation, the requesting Job remains queued under its original queue deadline; inspect/events report `host_capacity_no_eligible_victim` and whether policy, pause, occupation, transition, unknown idle time or uncertain power blocks progress. Raise the limit only to the bound observed in TASK-016.
- A framework VM-limit error without a guest start is a temporary prerequisite gap, not quarantine. The affected job finishes INCOMPLETE. The controller pauses every `utm-macos` resource on this host with reason `host_vm_limit`, so queued VM jobs wait instead of failing one after another. The operator resumes the resources after freeing host capacity.

**DHCP lease validity and guest identity.** Select exactly one entry for the registered MAC from the supported host lease source. Its individual expiry must be known and later than the current time, whether the entry was written before or after the current VM start. An unexpired pre-start entry may be used only after the registered VM is observed started and the connection proves the pinned SSH host key, guest MAC, current boot identity and fresh run nonce. A lease-file modification time is not proof of an individual entry's freshness; an entry creation time is not assumed available. Recheck expiry and identity on renewal and after restart. Missing, ambiguous or unparseable entry/expiry, or failed identity checks, is a prerequisite gap; do not try another address-discovery mechanism.

**VM unavailable.** An unknown UUID, a `status` other than `started` or `stopped`, a proven failed start, no valid DHCP lease or failed guest identity check can be a prerequisite gap for one exact registered VM. When preparation has no owned Job effect requiring cleanup and every associated power operation is reconciled, durably pause only that resource with reason `vm_unavailable` before returning its reserved capacity; its Job settles INCOMPLETE with `cleanup_outcome=not_needed`. Reservation alone does not require Job-file cleanup. A confirmed, reconciled VM start before guest staging may also leave Job cleanup `not_needed`; a start or stop with uncertain effects cannot. Other VMs and local resources retain their availability and may progress. A failure after possible guest effects follows collection, cleanup or reconciliation instead. An older observation cannot pause a newer registration or replace an existing quarantine. The operator fixes the VM or its registration and explicitly resumes the resource after revision-bound readiness verification. Host-wide session, permission and framework-capacity failures retain their host-wide handling.

The adapter binds the registered UTM VM UUID, MAC address, pinned SSH host key, boot nonce and guest job root. A name or IP address alone is not VM identity. The MAC address is operator-asserted at registration, read from the VM's network settings in UTM, because `utmctl` does not report it; Haepari confirms it indirectly at every Prepare, when a current lease for that MAC must lead to a host with the pinned SSH host key. The address comes from the host DHCP lease table, looked up by the registered MAC address. `utmctl ip-address` is not used, because the Apple backend has no guest agent. The boot identity distinguishes boots; a fresh run nonce fences requests. No beta upgrade is a default prerequisite.
