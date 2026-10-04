# Operator runbook

The local recovery, isolated development service and advisory lock sections describe the current candidate. UTM host registration and fenced idle updates are implemented; automatic development VM execution and revision-bound recovery are implemented; real backend qualification and release procedures remain planned; execute them only after their implementation and operator configuration exist. UTM-specific control arguments must come from the installed version's qualified `utmctl` help.

## Provision the controller

Install the verified Haepari release and its complete `use-haepari` tree through an explicit installation action. Select a private local state root. Run version/capabilities and read-only doctor. Start the daemon through the approved foreground or user-service path. Do not initialize by copying another user's state database.

Register a local command resource first and execute harmless deterministic work. Verify submit, wait, output collection, cancellation, and restart before registering a VM resource. The local resource runs commands on the host and is not a sandbox.

## Current local controller recovery limit

The foreground local controller uses global active limit one. Startup moves unfinished occupied Jobs to `reconciling` and blocks affected resources before observing exact receipts. A proven live supervisor is reattached under its original deadline; known stopped work resumes durable collection/cleanup/settlement. Unknown workload or collector state retains occupancy and quarantine. That slot blocks dispatch across all resources; queued work keeps its original deadline. A successful command or closed listener is not termination proof.

Use `inspect <job-id> --json` to read the Job and global occupancy, and `resource inspect <id> --json` for availability, reason and fences. `job recover <id> --plan --json` is read-only and applies to reconciling Jobs; use `inspect` for other states. A supported local or VM plan carries a two-minute controller-bound token. Apply with the saved `--plan-token` and `--request-key`, then observe the original Job. A changed revision, lease, receipt or native observation rejects an unapplied token; controller restart invalidates it. An identical committed request replays its permanent receipt even after restart or token expiry. Changing the saved token or evidence under that key conflicts. Pause/resume cannot clear quarantine. Preserve state and owned evidence: deleting the database or submitting a new key does not reconcile unknown effects.

For diagnosis, `haepari runner inspect --request <absolute-owned-job-path>/request.json --json` reads identity-bound receipts without signaling or changing occupancy. `outcome.json` records workload completion; only a consistent stopped proof permits collection and cleanup. Preserve ambiguous intents, missing receipts and runner diagnostics. Never repeat `runner start` to resolve unknown effects. If independently established local termination needs an operator attestation, request `job recover <id> --plan --action attest-stopped --json` (or `attest-no-start`) and apply with `--evidence-file <json>`. Record source `operator-attested`, observer, nonzero UTC observed time (`Z` or `+00:00`), mechanism, digest and reference. The plan binds its scope; a positively observed live execution or incomplete filesystem owner blocks attestation and retains occupancy. Establish termination before requesting another plan. Proven terminal receipts remain consumable while the supervisor finishes publishing them. Provenance stays attested across later recovery and cannot produce PASS.

An occupied resource returns `RECOVERY_BLOCKED` on recovery. After Job settlement, `resource recover <id> --plan --json` can inspect idle recoverable quarantine; use `resource inspect` for other states. A supported local health-only plan applies with a saved token/key and verifies readiness plus owned workspace absence; its committed intent can resume on startup. Retained required evidence and pending/uncertain adapter effects prevent reuse. Plans expose the exact adapter operation identity and progress and list protected paths; they never infer cancellation of an issued backend command. TASK-012 implements local protected re-collection/disposal; VM state binding remains TASK-013/TASK-015. No resource recovery rewrites a Job result.

## Isolated development LaunchAgent

Prepare `haepari service plan --instance <id> --binary <absolute-candidate> --root <private-development-root> --json` and save its `data` object as the plan file. Planning writes no service or state. Review the binary digest, private root, exact `gui/<uid>` domain/label, `LimitLoadToSessionType=Aqua` and `AbandonProcessGroup=true`, then separately authorize `service install --plan-file <json> --json`. The default state root, nonempty foreign state and an existing plist are refused. The root ownership marker permits reuse only by that same development instance; the candidate is never installed into the stable toolchain.

Qualify product survival with the explicit installed-service check in [TESTING.md](../TESTING.md), then separately stop/remove the exact instance with `service uninstall --plan-file <json> --json`. A reachable controller must have no occupied resource; inaccessible or uncertain state is preserved. Uninstall removes only its exact plist after successful bootout, leaving state and evidence. Failed bootstrap/bootout preserves the instance for inspection. TASK-011's current candidate has observed the actual product runner surviving SIGTERM and SIGKILL of its isolated Aqua controller on macOS arm64, with exact incarnation reattachment, unchanged run deadline and one workload execution. The Job finished PASS with verified cleanup and zero occupancy. This observation covers that development instance; UTM and power-loss qualification remain later gates.

## Current advisory lock operations

Register a lock resource without adapter/execution fields. Save the full acquire payload and request key, wait or replay to claim its current grant, then keep renewal in the acquiring process or `lock keepalive`. A disconnected wait never cancels the queued request; explicit cancellation and force release record the action and its reason. List all pages to inspect FIFO predecessors, grant generations and deadlines before operator action. Pause controls future grants only; update changes future policy only. Removal refuses live/queued requests and does not erase terminal keys or slot generations. Recovery is unsupported for advisory locks.

Pass an inherited grant only to a cooperating borrower that checks the selected installation and intended lock read-only. The owner renews and eventually releases; child completion or failure does neither. A stopped owner expires within its original liveness window, while every grant ends by its original maximum hold. Restart and observed wake extend liveness before expiry within that maximum. A clock-read failure prevents lock mutations rather than releasing capacity from uncertain evidence. These grants do not reconcile or free managed Job occupancy. See the [lock contract](contracts/locks.md) for the implemented Unix surface and exits.

## Prepare the macOS VM

Create one test-only macOS VM using UTM's supported path. Record host/guest OS, UTM/backend identity, CPU/memory/disk allocation, test user, GUI login/service requirements, and automation permissions. On a host other than the one in the [TASK-034 probe record](records/TASK-034-utm-probe.md), repeat its `utmctl` checks from a LaunchAgent before registering the VM. Control UTM only from the Haepari LaunchAgent in the logged-in Aqua session; `utmctl` does not work from SSH sessions or before login. Enable Remote Login only for the required test user and provision a dedicated SSH credential/known-hosts mapping. This is a separate setup effect.

Install the chosen stable product combination and native skill locations using approved exact artifacts. External global skills and per-home rules are part of the combination. Establish new-session discovery and service/MCP readiness. Keep real production accounts and data out of the VM. Provider authentication uses a separately approved test setup; document expiration and refresh rather than copying host authentication files.

Haepari uses this one VM for every VM job and never resets it ([ADR-0018](architecture-decision-records/0018-persistent-vm-no-reset.md)). Keep your own backup of the VM if you want a way back; restoring it is an operator action outside Haepari. A backup copy keeps the VM's MAC address, so never run it alongside the resource VM, and run `resource update` after a restore that changes the VM UUID. Qualify UTM control, SSH identity, guest job nonce, candidate installation and job cleanup on it.

Register the VM in Haepari as a `utm-macos` resource with its UTM UUID, MAC address, pinned SSH host key and guest job root. Automatic force-stop for host capacity is disabled by default; set `vm.allow_automatic_stop=true` only after deciding that this VM's persistent guest services and operator work may be interrupted while it is eligible and idle. Pause the resource for maintenance regardless of that policy. The address comes from a unique unexpired host DHCP lease entry for the MAC, as the [TASK-034 probe record](records/TASK-034-utm-probe.md) observed; a pre-start lease still needs current guest identity checks. Never disable host-key checking to make a connection work.

## Current VM Job recovery

Use only a registered UUID/MAC with the exact private mapping and dedicated host-key pin described by the [guest protocol](contracts/resource-and-runner.md#ssh-and-guest-supervision). The ordinary SSH alias is insufficient. Automatic development Jobs use the same submit/wait/cancel commands as local Jobs; no real backend qualification is implied. Select the foreground host limit with `daemon run --host-macos-guest-limit 1|2` (default 1). VM removal remains unsupported.

After a disconnect or controller restart, inspect the original Job. Recovery reads its original guest boot, request, frozen runner, filesystem worker and native receipts; never issue a new workload to discover whether the old one ran. A no-start recovery first retires further start delivery. A live or unknown filesystem worker prevents collection/deletion and keeps occupancy. Guest reboot, foreign root incarnation or an ambiguous launch blocks recovery.

A terminal INCOMPLETE Job with retained evidence leaves its VM quarantined. The protected `workspace` / `job:<id>` reference identifies the entire owned guest Job directory, including logs and receipts. Use `resource recover <id> --plan --action recollect --json` to inspect exact supplemental collection effects, or `--action dispose` to inspect evidence loss. Applying the exact disposal token explicitly authorizes that loss. After resolution, obtain and apply a separate health plan; neither action rewrites the original verdict. Minimal fenced control metadata remains outside the deleted directory. Do not remove it or guest files manually to make a resource appear healthy.

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
| Local resource quarantined with `local_cleanup_failed` | Inspect the Job's cleanup outcome, `protected_evidence` paths and collection gaps. Its result reason may be `evidence_retained`, `cleanup_deadline`, an earlier preparation/run cause, or omitted for an ordinary cleanup failure. Preserve the owned workspace and follow the current local recovery limit above. TASK-011/012 provide bounded re-collection or separately authorized disposal naming the evidence loss, followed by cleanup and health verification. The original Job verdict stays fixed. |
| VM resource quarantined with retained evidence | Inspect protected guest paths and collection gaps. Use a current `resource recover --plan` for bounded re-collection, or a separately authorized disposal plan that names the evidence loss, followed by cleanup and health verification. The original Job verdict stays fixed. |
| Resource quarantined for another reason | Inspect the exact failed phase and request a current recovery plan. |
| A forced stop or disposal would discard uncollected evidence | Disclose the specific loss and obtain the applicable recovery authority before applying the plan. |
| State database is newer than executable | Install a compatible stable controller; never downgrade the schema blindly. |
| Startup reports a missing required table or column | Keep the controller stopped and preserve the database and WAL evidence. Restore a verified consistent backup through the authorized recovery procedure; do not create an empty receipt table or reset the schema version to reuse a key. |

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

TASK-006 preserves keys and immutable admissions independently of bulk payloads. TASK-012 owns actual retention/purge, including protection of unresolved recovery evidence. Before deleting a migrated admission's original sealed object, bind its small inventory from that verified object through `BindAdmissionInventory`; missing or changed objects require recovery, never reconstruction from mutable source. `RetainAdmissionTombstone` is an internal metadata-only retention operation: it refuses non-terminal or quarantined work and object-backed admissions without retained inventories, deletes nothing, and never frees or reuses a key. A known unavailable result is recovered by the same request key or inspected by Job ID. A deliberately new execution requires a new persisted key and explicit submission; an uncertain reply or unavailable result must not trigger automatic resubmission under a new key.

## Native client observations and cancellation

Use `jobs list --json` and `resources list --json` for bounded pages. Keep the exact filters with a Job cursor. Use blocking `wait` or `events --follow --json` for completion rather than repeatedly inspecting status. Persist an event checkpoint only after applying its page or resynchronization snapshot. For logs, retain byte offset and file identity; inspect an explicit gap before combining output. Disconnecting or interrupting an observer does not cancel or resubmit work. Interrupting a follow before its terminal `end` leaves the observation incomplete and exits 6; inspect the existing Job before deciding to reconnect.

For explicit cancellation, first inspect the current Job revision, then use `cancel <id> --expected-revision <n> --request-key <saved-key> --json`. Preserve these values across an uncertain reply. The returned intent is not a termination proof; wait for the final result. Queued cancellation returns without acquiring capacity. Active cancellation performs bounded owned-process termination and cleanup; unproven termination retains occupancy and quarantine for recovery. A late request preserves an already known workload outcome. After restart, never kill an arbitrary stored PID or remove uncertain evidence to make capacity appear available.

## Retained evidence and explicit purge

Inspect the finished result and resource state before retention work. Export with `haepari export <job-id> --to <new-absolute-directory> --json`; keep the final manifest and independently verifiable hashes with the original result. An existing destination is refused. A failed export leaves its partial directory and no completion manifest; inspect that directory and choose a new explicit destination for another attempt.

For protected evidence, inspect `resource recover <id> --plan --action recollect --json`. Save the returned token and request key before apply. If collection remains impossible and the owner authorizes the listed loss, request a separate `--action dispose` plan; save and apply its exact token/key. Never delete the workspace manually to make a health check pass. Completed evidence recovery leaves quarantine until a separate default resource-health plan/apply succeeds. Original terminal results remain unchanged.

If an owned root, receipt or native identity is corrupt or unverifiable, plans/apply fail closed. Preserve the root and permanent keys; compare saved immutable receipts and current inspected identity before seeking an owner-directed recovery. A bare PID, timeout, exit code or matching pathname supplies no deletion authority. A live old controller or collector must be observed stopped before retry; fixed recovery deadlines are not renewed by restart. Unregistered or unqualified VM inspection provides no apply token.

For a finished, unprotected Job on a verified resource, save `inspect`'s Job revision and run `purge <job-id> --expected-revision <n> --json`. Save that original revision for interrupted retries. Purge records unavailable admission before deletion, checks every native incarnation, and preserves shared input objects until their other available references disappear. `complete=true` acknowledges the explicit operation. Subsequent same-key submit returns that unavailable Job; `run`/`wait` exit 2 and cannot rerun it. Saved cancellation mutations and cursor ownership remain permanent. Automatic retention expiry and host-wide cleanup are not implemented or authorized by this command.

## Current UTM host registration

Provision the persistent VM, its registered MAC and guest authentication separately. Do not create a VM or infer trust from its display name. Create a dedicated operator-owned known-hosts file with the exact alias `haepari-vm-<lowercase-VM-UUID>` and one `ssh-ed25519` public key. Bind its raw file SHA-256 and absolute path in `guest.known_hosts_reference` as specified by the [runner contract](contracts/resource-and-runner.md). Registration runs read-only UTM UUID inspection; missing identity or pin is `PREREQUISITE_UNAVAILABLE`, durable UUID/MAC duplication is `CONFLICT`, and storage uncertainty remains `STORAGE_UNAVAILABLE`. Reconcile uncertain registration by inspecting the exact resource before retrying.

Inspect configuration and availability revisions before `resource update`; supply both expected revisions and a successor configuration revision. An occupied or pending/uncertain power-protected resource refuses the update, and an idle update preserves maintenance pause. Keep automatic-stop authorization false unless the operator permits interrupting that persistent VM when eligible. This checkpoint accepts no VM Jobs, resumes no VM and provides no VM removal/recovery command. TASK-014 provides the internal authenticated SSH transport; production collection and recovery follow in TASK-015; the separate development-service campaign remains TASK-016.


## TASK-014 SSH prerequisites and diagnosis

Target: one already registered persistent Darwin arm64 guest with operator-provisioned Remote Login, dedicated key authorization and a verified VM host-key pin. Provisioning accounts, enabling Remote Login and installing guest services require separate operator authority. Haepari transfers its transient executable per Job; it requires no permanent guest installation.

Prepare a private host mapping file containing canonical compact JSON, for example `{"schema":"haepari.ssh-mapping/v1","user":"<guest-user>","port":22,"identity_file":"<absolute-private-key-path>"}`. Replace the placeholders, protect both files as owner-private regular files, hash the mapping bytes and bind the result in `guest.connection_reference`. Update an existing resource only through its fenced idle successor configuration. Keep credentials outside selected input and generated artifacts. A configured `ssh UTM` alias may show how the operator connects; its address is not a replacement for Haepari's registered MAC lease and dedicated pin.

On authentication, lease, platform or boot mismatch, verify the exact resource configuration, current unique lease, mapping digest and dedicated pin. Do not use trust-on-first-use, disable strict checking, forward an agent or try another address. On transport loss after issuance, preserve the exact request/boot/run identity and guest files. Reconnect with inspect/watch/output, never relaunch the original command. `launching` without runner receipts remains ambiguous. Cancellation is complete only with the original run's termination-proven exit receipt. Collection and cleanup remain separate even then.

TASK-014's tests use fake UTM/SSH proof and local native guest protocol fixtures. No actual guest smoke is claimed without current registered UUID/MAC/pin and authorized setup. TASK-016 owns mandatory real qualification, installed controller behavior, cleanup/reuse, locked/logged-out host behavior and F2. Escalate partial preparation, ambiguous launch, retained evidence and cleanup to TASK-015's managed recovery owner; never delete guest Job files as diagnostic cleanup.
