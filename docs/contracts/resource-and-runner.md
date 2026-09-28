# Resource, UTM, and runner contract

## Resource definition

`haepari.resource/v1` is operator-owned configuration. It names `id`, `kind` (`local`, `utm-macos` or `lock`), `execution_mode` (`managed`, for `local` and `utm-macos` only), positive capacity and configuration revision. A `lock` resource has no adapter, baseline or execution; the [lock contract](locks.md) defines it, and it counts toward no managed or global capacity. `local` and `utm-macos` require managed execution. Definitions include owned paths, pinned adapter identities and lifecycle budgets. VM definitions add baseline/profile identity, template registration, working-VM ownership, guest mapping and reset strategy. Each baseline is bound to exactly one `utm-macos` resource. A job whose profile digest differs from its resource's baseline profile is rejected with `PROFILE_MISMATCH`; install and migration starting profiles therefore use different resources. Local resources have no baseline; a local job names an explicit local environment reference instead, and `PROFILE_MISMATCH` does not apply. Public external resources are deferred; reject them as unsupported in v0.1.0.

A submitted job cannot overwrite this definition. Local resources may use a trusted operator-defined prepare/cleanup command; job inputs supply only workload bytes and permitted parameters. A resource advertised as isolated must actually use an isolating backend. The generic local adapter makes no sandbox claim. For a local command resource, reusable means the owned process group has settled and its declared cleanup/health checks passed; it does not mean arbitrary host filesystem changes were rolled back. Shared-state resources such as a test DB require operator-provided reset and verification hooks.

`haepari.baseline-registration/v1` binds resource/revision, baseline ID, powered-off whole-package digest and owned reference, observed UTM backend/UUID, guest identity and pinned SSH host-key references, and real cold-restore qualification receipt. Registration is revision-fenced and fails on duplicate/conflicting identity. It is refused when another resource already uses the baseline, or when the baseline's MAC address, machine/serial identity or SSH host key matches another registered baseline; a baseline derived by copying another must regenerate them first. It never clones or boots a VM implicitly. Configuration changes need an idle resource. Referenced baseline generations remain available while accepted jobs or retained reproduction records need them. Removing a baseline is a separate explicit operation with reference checks; it never deletes the only recovery copy of an active environment.



## Adapter phases

Every managed adapter call carries the job ID, resource ID, expected resource/configuration revision, lease generation, nonce, input/profile digests, and phase deadline. Responses identify the same tuple.

| Phase | Allowed effects | Required evidence |
| --- | --- | --- |
| Inspect | Read registration/capabilities only | Exact identity and readiness/gaps |
| Prepare | Materialize the declared owned environment, boot, connect, stage input | Before identity, qualified baseline, guest/run identity, staged digests |
| Execute | Start or reattach one exact runner | Durable run claim, start identity, observation endpoint |
| Collect | Read declared outputs into the owned host job root | Artifact inventory, sizes/digests, completeness |
| Cleanup | Stop owned workload/session/services and restore owned working environment | Workload-stop evidence and restoration result |
| Verify | Check baseline identity and consumer inventory after restoration | Verified reusable state or quarantine reason |

If preparation had no effects, cleanup can be `not_needed`. After any possible effect, failure or timeout invokes collection and cleanup within their own limits. No adapter returns reusable=true from a process exit alone.

## UTM qualification and cold restore

UTM documents a CLI wrapper around AppleScript and whole-VM copying/export. Its examples for guest command and file access assume a QEMU Guest Agent. These are the limits of the source-backed assumptions; the Haepari protocol does not invent unavailable `utmctl` flags. See [S1 through S3](../sources.md).

TASK-034 probes, and TASK-016 qualifies, the installed UTM version, Apple backend, host/guest OS, automation permission, GUI-session requirement, current scripting dictionary/CLI capabilities, and cold restore commands. Missing ability to complete a safe cycle blocks the backend. Do not silently switch to QEMU, a beta build, or a different VM manager.

The v0.1.0 strategy is an immutable powered-off whole-package baseline and an owned working copy. The adapter must:

1. Establish that the baseline is powered off, stable, and never registered as a job target.
2. Verify source/package identity and available space. Preserve required metadata and every disk/configuration dependency during copying; do not copy only a disk image.
3. Create or restore only an owned working package, reconcile its UTM registration, and record the actual UUID rather than guessing it from a display name.
4. Ensure duplicate template identities cannot run concurrently. Each baseline is bound to one resource, registered baselines have distinct MAC, machine/serial and host-key identities, each resource has one working VM and capacity one, and the controller's `host_macos_guest_limit` bounds Haepari-managed macOS guests across resources.
5. Boot, establish an explicitly provisioned SSH endpoint, verify host key, guest baseline marker and OS identity, then bind a fresh job nonce and boot identity.
6. Transfer only the sealed input bundle. Validate it before project installation begins.
7. After execution, collect evidence, stop the guest/owned processes, and restore the full baseline before reporting reusable state. A verification boot followed by clean shutdown may be used; it must be part of the qualified cycle.

Record partial clone/registration operations durably. If the controller crashes after creating a VM but before recording completion, recover using the owned operation ID and exact path/identity, not by deleting all similarly named VMs. Do not edit a running VM package in place. A name match, empty `utmctl` output, or successful copy command alone is not ownership or restoration evidence.

No snapshot/disposable mode is required. A beta UTM release documents snapshots and a disposable start mode for Apple Virtualization guests on macOS 27 ([S9](../sources.md)); `reset_strategy` keeps such a strategy possible later. A future strategy must prove equivalent restoration and ownership semantics and receive its own capability/version tests.

## SSH and guest supervision

Use the configured system SSH/SFTP executable through argv, with pinned known-hosts material, BatchMode, no agent forwarding, no ambient remote command configuration, and bounded connection timeouts. Paths and values that cross the remote shell boundary are encoded through a fixed runner request file; a project command is not concatenated into `ssh ... "..."`.

The guest identity check combines the expected SSH identity, operator-controlled VM/endpoint mapping, baseline marker, observed OS/architecture, boot identity, and a fresh nonce. A cloned marker alone does not distinguish two guests. Credentials are handled as secret references or preprovisioned guest state with explicit approval, never as input-bundle files.

The transient runner accepts an authenticated, contained run request and provides `start`, `inspect`, `watch`, and `cancel` operations. It is launched using a qualified detach/supervision method that survives transport loss. It writes a durable request/spawn intent, actual start receipt when known, append-only output, and exit receipt under an owned guest job path, following the [receipt protocol](#runner-receipts). The digest-pinned runner artifact travels with each job's sealed bundle. Repeated start for the same run is idempotent; a conflicting request is rejected. A recorded ambiguous spawning state cannot be restarted automatically.

`watch` blocks on the same run. On transport loss the host may reconnect and observe; it must not invoke the original test command again. A guest reboot changes boot identity and prevents blind PID reuse. Cancellation waits for owned workload termination and still requires UTM cleanup. Stopping an SSH process does not terminate the semantic job.

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

Before forced recovery, produce a plan naming the exact working VM, resource revision, current lease/run, evidence that may be lost, and proposed stop/reset effects. The confirmation token binds those observations. A changed revision or new lease invalidates the token.

Recovery either proves the original execution state and finishes normal collection/cleanup, or records INCOMPLETE and safely restores the resource. It never fabricates a workload PASS. If restoration is unproven, leave the resource quarantined. Other resources can continue.

The resource lease remains the authority for all host-side mutation. A token prevents stale cooperating components from changing newer state; it is not an OS security barrier against same-user code.

## Host session and VM identity

Host UTM control uses the exact installed UTM automation interface from the development or release LaunchAgent in the logged-in Aqua session, with Automation permission granted to the exact, stably signed controlling executable. The UTM command-line tool states that it does not work from SSH sessions or before login ([S10](../sources.md)). SSH execution inside the guest is a separate transport and has its own login/key prerequisites. The controller's local queue and local-command backend do not require an Aqua session merely because UTM control may. **Recording.** Record the observed UTM, macOS, backend, user-session and permission tuple during TASK-034 and TASK-016. A version list in a report is not qualification evidence.

**Loss of session or capacity.** Session or host-capacity loss blocks new VM dispatch temporarily. A working VM with uncertain changes or failed reset is quarantined.

**Host macOS guest limit.**

- Apple's macOS license allows at most two additional macOS instances in virtual machines per Mac, and the Virtualization framework reports a limit error when one is exceeded ([S11](../sources.md)). The operator's own VMs count too.
- The controller setting `host_macos_guest_limit` (default 1, schema maximum 2) bounds Haepari-managed macOS guests, including verification boots during recovery. Raise it only to the bound observed in TASK-034 and TASK-016.
- A framework VM-limit error without a guest start is a temporary prerequisite gap, not quarantine. The affected job finishes INCOMPLETE. The controller pauses every `utm-macos` resource on this host with reason `host_vm_limit`, so queued VM jobs wait instead of failing one after another. The operator resumes the resources after freeing host capacity.

The adapter binds an operator-owned baseline package identity, UTM registration/VM UUID, owned working-copy path, pinned SSH host key, guest identity marker, boot nonce and a qualified endpoint mapping. A cloned marker or IP address alone is not VM identity. Endpoint discovery uses the method recorded by TASK-034. The default candidate is the host DHCP lease table, looked up by the working copy's MAC address. It is compared with an explicit operator mapping and validated with the SSH identity after every boot. A cold copy of a working VM from its own baseline duplicates that baseline's MAC address, serial identity and SSH host key. That is safe because each baseline has one resource and one working VM, and registration refuses baselines whose identities collide. The boot nonce distinguishes boots. A cold powered-off whole-package copy is the initial reset strategy; snapshot/disposable approaches require exact-version qualification later. No beta upgrade is a default prerequisite.
