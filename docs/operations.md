# Operator runbook

This is the planned operational contract. Commands are the proposed Haepari surface; UTM-specific control arguments must come from the installed version's qualified `utmctl` help. Do not execute examples before the implementation and operator configuration exist.

## Provision the controller

Install the verified Haepari release and its complete `use-haepari` tree through an explicit installation action. Select a private local state root. Run version/capabilities and read-only doctor. Start the daemon through the approved foreground or user-service path. Do not initialize by copying another user's state database.

Register a local command resource first and execute harmless deterministic work. Verify submit, wait, output collection, cancellation, and restart before registering a VM resource. The local resource runs commands on the host and is not a sandbox.

## Current local controller recovery limit

TASK-007's foreground local controller uses global active limit one. Restarting with an occupying Job moves it to `reconciling`, quarantines the resource, and retains its slot. That slot blocks dispatch across all resources, including newly registered ones; new submissions remain queued and can expire under their original deadline. This is conservative accounting, not proof that the original process ended.

Use `inspect <job-id> --json` to read the retained Job and global occupancy, and `resource inspect <id> --json` to read availability, reason and fences. Sibling queued Job events record a quarantine change. Do not expect `pause`/`resume` or registration to clear quarantine. `job recover` and `resource recover` are planned under TASK-011 and are not available in this slice. Preserve the affected state root and owned evidence, stop admitting dependent work, and retain the exact Job identity for that recovery implementation. Deleting the database or submitting a fresh key does not reconcile an unknown effect. A separate development scratch root is usable only for independent work after verifying it cannot duplicate or interfere with the unresolved workload.

## Prepare the macOS VM

Create one test-only macOS VM using UTM's supported path. Record host/guest OS, UTM/backend identity, CPU/memory/disk allocation, test user, GUI login/service requirements, and automation permissions. On a host other than the one in the [TASK-034 probe record](records/TASK-034-utm-probe.md), repeat its `utmctl` checks from a LaunchAgent before registering the VM. Control UTM only from the Haepari LaunchAgent in the logged-in Aqua session; `utmctl` does not work from SSH sessions or before login. Enable Remote Login only for the required test user and provision a dedicated SSH credential/known-hosts mapping. This is a separate setup effect.

Install the chosen stable product combination and native skill locations using approved exact artifacts. External global skills and per-home rules are part of the combination. Establish new-session discovery and service/MCP readiness. Keep real production accounts and data out of the VM. Provider authentication uses a separately approved test setup; document expiration and refresh rather than copying host authentication files.

Haepari uses this one VM for every VM job and never resets it ([ADR-0018](architecture-decision-records/0018-persistent-vm-no-reset.md)). Keep your own backup of the VM if you want a way back; restoring it is an operator action outside Haepari. A backup copy keeps the VM's MAC address, so never run it alongside the resource VM, and run `resource update` after a restore that changes the VM UUID. Qualify UTM control, SSH identity, guest job nonce, candidate installation and job cleanup on it.

Register the VM in Haepari as a `utm-macos` resource with its UTM UUID, MAC address, pinned SSH host key and guest job root. Automatic force-stop for host capacity is disabled by default; set `vm.allow_automatic_stop=true` only after deciding that this VM's persistent guest services and operator work may be interrupted while it is eligible and idle. Pause the resource for maintenance regardless of that policy. The address comes from a unique unexpired host DHCP lease entry for the MAC, as the [TASK-034 probe record](records/TASK-034-utm-probe.md) observed; a pre-start lease still needs current guest identity checks. Never disable host-key checking to make a connection work.

## Run a project test

From the project, use its documented `make test-vm` with a named suite and mode. The wrapper freezes candidate/test input, displays its identity, submits once, and prints the job ID. It waits on the same ID. Continue host development without installing that candidate into the stable host toolchain.

To inspect a dropped session, use `inspect`, `events`, or `wait` on the original job. Do not submit a replacement merely because the caller lost its result. An explicit new run after a settled failure uses a new key and retains the link to the old result.

For hands-on, wait for awaiting_manual, open the exact owned UTM guest, and follow the project checklist. Do not install unrelated tools while another project's lease is active. Complete the result with observed and unobserved items. The VM remains occupied through collection and cleanup.

## Diagnose failures

| Observation | Action |
| --- | --- |
| Job queued | Inspect resource availability, FIFO predecessors, and controller-wide capacity. |
| Missing VM, automation permission, SSH or native login | Report prerequisite gap; do not install on the host instead. |
| Host macOS guest limit reached | The affected job finishes INCOMPLETE with a temporary prerequisite gap. The controller pauses every `utm-macos` resource on this host with reason `host_vm_limit`; queued VM jobs wait. Stop an unrelated VM, then `resource resume` them. Nothing is quarantined. |
| VM Job stays queued with `host_capacity_no_eligible_victim` | Inspect the reported policy, pause, occupation, transition, unknown idle time or uncertain power blocker. Wait for capacity or explicitly change the resource policy/maintenance state when safe; the original queue deadline still applies. Haepari never stops an unregistered or protected VM. |
| One resource paused with reason `vm_unavailable` | This VM's proven no-effect preparation failed because it was unknown or in an unexpected state, lacked a valid DHCP lease, or failed guest identity checks. Its reserved Job finishes INCOMPLETE with `not_needed` cleanup only after the pause is committed; later Jobs for this VM remain queued. Other VMs and local resources keep their own availability. Fix this VM or its registration, then use revision-bound `resource resume` with readiness verification. Host-wide session, permission or framework-limit blocks follow the separate host-wide rule above; uncertain power effects require reconciliation. |
| `RECOVERY_BLOCKED` on resource recovery | Recover the occupying job first with `job recover`, then recover the resource. |
| `RECOVERY_BLOCKED` on resource pause/resume | Read the refusal message: the transition requires the stated availability, or local readiness failed. The controller jobs path must be a real directory with no group/other access. Correct that prerequisite, inspect current fences and retry the same intent. Pause/resume cannot clear quarantine; use the applicable recovery workflow. |
| Workload failed, cleanup verified | Read the retained product evidence; a new run is a separate submission. |
| Connection lost | Reattach the existing run/job; do not re-execute the original command. |
| Resource quarantined with `evidence_retained` | Inspect protected guest paths and collection gaps. Use a current `resource recover --plan` for bounded re-collection, or a separately authorized disposal plan that names the evidence loss, followed by cleanup and health verification. The original Job verdict stays fixed. |
| Resource quarantined for another reason | Inspect the exact failed phase and request a current recovery plan. |
| A forced stop or disposal would discard uncollected evidence | Disclose the specific loss and obtain the applicable recovery authority before applying the plan. |
| State database is newer than executable | Install a compatible stable controller; never downgrade the schema blindly. |

Use `job recover --plan` and `resource recover --plan` to inspect exact job/run, resource revision, generation, observations, uncertainty and proposed effects. Apply with a still-current token and provenance-bearing evidence only after authorized operator review. An attestation stays labeled attested; unsupported or unknown execution remains unresolved. A mutation receipt records the exact outcome. Confirm identity and reusable state afterward. A successful recovery changes resource readiness, not the failed/inconclusive workload verdict.

## Update the VM

Make operator changes outside jobs, such as OS updates or the stable versions of shared tools, only in an explicit maintenance action: pause the resource, let running work and any power transition finish or reconcile, change the VM, record what changed, then resume. A paused resource cannot be selected for automatic forced stop, even if `vm.allow_automatic_stop=true`. Jobs change their own products through their recipes and return them to the stable version when they finish. Consumer profiles change separately; create a new immutable profile rather than editing an old one.

## Update Haepari itself

Pause admission or resources and drain/reconcile active work. Take a quiesced, consistent SQLite backup including the required WAL state through a supported backup method. Verify the new release's storage compatibility and installation identity. Apply the update without running two controllers. Startup migrations run under the exclusive controller lock. Preserve the prior backup and executable for documented recovery; a schema downgrade is not automatically supported.

Haepari development uses a separate `HAEPARI_HOME`, socket, artifact area, and synthetic resources or a guest installation. Never test a candidate by replacing the stable controller managing its own VM campaign.

## Retire Aquarium's old development channel

Haepari never performs this as an installation side effect. The consumer rollout first proves Aquarium and each producer's new VM path, then detaches exact owned post-commit markers, stops/retires producer-owned development services, checks open workers/sessions, and settles preservation needs. Remove the shared launcher/MCP references/runtime only after all consumers no longer need them. The final removal of `~/.aquarium-dev/` has a separate explicit destructive-action boundary.

Preserve stable host tools, production state, Sorage control-plane data and Vault, unrelated Git hooks, native skill installations still needed on the host, and current user authentication. Do not delete every `use-*` skill or every path containing the word `dev`. Repository source cleanup and live host cleanup are different deliverables.

## Deferred external resource

The R2 external operator path is preserved in [the deferred contract](contracts/external-leases.md). It has no v0.1.0 command or release gate. B1/B2 must close before operators receive a supported runbook.


## Advisory locks

Register the `mulgae-review` lock resource (`kind: lock`, capacity one) before Mulgae's integration uses it; see the [lock resource example](examples/resource-lock.template.json).

- **Inspect.** `haepari locks list` shows request and grant IDs, the holder, waiters in FIFO order, and every deadline.
- **Abandoned grant.** A grant whose holder process died stops being renewed and ends after one liveness window. To end it sooner, use `haepari lock release <grant-id> --force --reason <text>`.
- **Stale queued request.** Remove it with `haepari lock cancel <request-id> --force --reason <text>`.
- **Limits of the lock.** Locks are advisory. A review started without the lock is not coordinated.
- **Nested grant use.** The invocation that acquired a grant keeps renewing and releases it after its protected work. A child receiving `HAEPARI_LOCK_GRANT` checks the current installation, lock and grant; it borrows the grant and never releases, cancels or renews it. An invalid inherited grant is returned to the owner for handling, not reacquired silently.

## Configuration and changes

The [configuration contract](contracts/configuration.md) owns the paths, precedence, `haepari.config/v1` fields, defaults and change rules, and gives the development-loop profile for 10–30 VM jobs a day.

**Changing global configuration.**

1. Stop the controller.
2. Edit the private configuration file.
3. Start the controller again. It validates the file and records an audit receipt before dispatch.

Running local jobs survive the restart and are reattached.

**Changing resources.** Use `resource update`, `pause`, `resume` and `remove` with expected revisions.

**Queue deadlines on a capacity-one VM resource.** Set `queue_seconds` to at least (jobs ahead + 1) × the maximum job duration. `haepari doctor` warns when a default queue deadline is too short.

Queue deadlines use persisted UTC instants; after sleep/wake/restart, queue expiry is evaluated before reservation. Running/manual deadlines request cancellation and reconciliation, never automatic release. Cleanup deadline leaves quarantine if cleanup is unproven. A CLI wait timeout does not change a Job. See the [transition table](contracts/managed-transitions.md).

## Durable admission retention boundary

TASK-006 preserves keys and immutable admissions independently of bulk payloads. TASK-012 owns actual retention/purge, including protection of unresolved recovery evidence. Before deleting a migrated admission's original sealed object, bind its small inventory from that verified object through `BindAdmissionInventory`; missing or changed objects require recovery, never reconstruction from mutable source. `RetainAdmissionTombstone` is the internal metadata side of that future retention transaction: it refuses non-terminal or quarantined work and object-backed admissions without retained inventories, deletes nothing, and never frees or reuses a key. A known unavailable result is recovered by the same request key or inspected by Job ID. A deliberately new execution requires a new persisted key and explicit submission; an uncertain reply or unavailable result must not trigger automatic resubmission under a new key.

## Native client observations and cancellation

Use `jobs list --json` and `resources list --json` for bounded pages. Keep the exact filters with a Job cursor. Use blocking `wait` or `events --follow --json` for completion rather than repeatedly inspecting status. Persist an event checkpoint only after applying its page or resynchronization snapshot. For logs, retain byte offset and file identity; inspect an explicit gap before combining output. Disconnecting or interrupting an observer does not cancel or resubmit work. Interrupting a follow before its terminal `end` leaves the observation incomplete and exits 6; inspect the existing Job before deciding to reconnect.

For explicit cancellation, first inspect the current Job revision, then use `cancel <id> --expected-revision <n> --request-key <saved-key> --json`. Preserve these values across an uncertain reply. The returned intent is not a termination proof; wait for the final result. Queued cancellation returns without acquiring capacity. Active cancellation performs bounded owned-process termination and cleanup; unproven termination retains occupancy and quarantine for recovery. A late request preserves an already known workload outcome. After restart, never kill an arbitrary stored PID or remove uncertain evidence to make capacity appear available.
