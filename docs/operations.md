# Operator runbook

The local recovery, isolated development service and advisory lock sections describe the current candidate. UTM host registration and fenced idle updates are implemented; automatic development VM execution and revision-bound recovery are implemented; TASK-016 records the bounded real backend qualification and F2 decision. Release procedures remain planned; execute them only after their implementation and operator configuration exist. UTM-specific control arguments must come from the installed version's qualified `utmctl` help.

## Ordinary development scope

The supported default is the same user’s local machine and already-running registered persistent VM. Establish a development scope once: selected repository/test commands, controller/resource, permitted offline workload effects and finite budgets. Reuse it for ordinary submit/wait, logs/results, original-run reconnection, semantic cancellation and Haepari-owned Job cleanup. Normal candidate changes need recorded identities, not another approval question or per-Job authority file. Resource registration alone is not authorization for arbitrary recipes.

VM power/reboot, host logout/network experiments, host installation, installed-service replacement, evidence disposal, providers and publication remain distinct effects. Routine validation does not initiate them. If the VM/session/SSH prerequisite is unavailable, report that precise gap and continue independent local development; do not run an environment experiment to manufacture readiness. Existing product power/recovery semantics remain unchanged and are not implicitly authorized by this scope.

The runnable project-owned `make test-vm` target in `examples/project-test` uses the same public CLI scope and read-only selected-resource prerequisites for an already-running VM. The current `make test-utm` is optional full-campaign tooling and keeps its exact manifest/authority inputs. It is not required for every task or first release. DF-001–004 are optional hardening; no TASK-027 deadline remains. See [ADR-0020](architecture-decision-records/0020-local-development-scope.md).

## Provision the controller

Install the verified Haepari release and its complete `use-haepari` tree through an explicit installation action. Select a private local state root. Run version/capabilities. Start the daemon through the approved foreground or user-service path. Do not initialize by copying another user's state database.

Register a local command resource first, then run `haepari doctor --resource <registered-id> --json` against that running controller. A doctor report checks the selected registered resource; it is not a pre-installation or daemon-start step. Execute harmless deterministic work only within the approved scope. Verify submit, wait, output collection, cancellation, and restart before registering a VM resource. The local resource runs commands on the host and is not a sandbox.

## Current local controller recovery limit

The foreground local controller defaults to global active limit one; `--global-limit 2` is the recommended mixed local/VM development profile. Startup moves unfinished occupied Jobs to `reconciling` and blocks affected resources before observing exact receipts. A proven live supervisor is reattached under its original deadline; known stopped work resumes durable collection/cleanup/settlement. Unknown workload or collector state retains occupancy and quarantine. At global limit one that slot blocks dispatch across all resources; with available global capacity, independent resources continue; queued work keeps its original deadline. A successful command or closed listener is not termination proof.

Use `inspect <job-id> --json` to read the Job and global occupancy, and `resource inspect <id> --json` for availability, reason and fences. `job recover <id> --plan --json` is read-only and applies to reconciling Jobs; use `inspect` for other states. A supported local or VM plan carries a two-minute controller-bound token. Apply with the saved `--plan-token` and `--request-key`, then observe the original Job. A changed revision, lease, receipt or native observation rejects an unapplied token; controller restart invalidates it. An identical committed request replays its permanent receipt even after restart or token expiry. Changing the saved token or evidence under that key conflicts. Pause/resume cannot clear quarantine. Preserve state and owned evidence: deleting the database or submitting a new key does not reconcile unknown effects.

For diagnosis, `haepari runner inspect --request <absolute-owned-job-path>/request.json --json` reads identity-bound receipts without signaling or changing occupancy. `outcome.json` records workload completion; only a consistent stopped proof permits collection and cleanup. Preserve ambiguous intents, missing receipts and runner diagnostics. Never repeat `runner start` to resolve unknown effects. If independently established local termination needs an operator attestation, request `job recover <id> --plan --action attest-stopped --json` (or `attest-no-start`) and apply with `--evidence-file <json>`. Record source `operator-attested`, observer, nonzero UTC observed time (`Z` or `+00:00`), mechanism, digest and reference. The plan binds its scope; a positively observed live execution or incomplete filesystem owner blocks attestation and retains occupancy. Establish termination before requesting another plan. Proven terminal receipts remain consumable while the supervisor finishes publishing them. Provenance stays attested across later recovery and cannot produce PASS.

An occupied resource returns `RECOVERY_BLOCKED` on recovery. After Job settlement, `resource recover <id> --plan --json` can inspect idle recoverable quarantine; use `resource inspect` for other states. A supported local health-only plan applies with a saved token/key and verifies readiness plus owned workspace absence; its committed intent can resume on startup. Retained required evidence and pending/uncertain adapter effects prevent reuse. Plans expose the exact adapter operation identity and progress and list protected paths; they never infer cancellation of an issued backend command. TASK-012 implements local protected re-collection/disposal; VM state binding remains TASK-013/TASK-015. No resource recovery rewrites a Job result.

## Isolated development LaunchAgent

Prepare `haepari service plan --instance <id> --binary <absolute-candidate> --root <private-development-root> --json` and save its `data` object as the plan file. Planning writes no service or state. Review the binary digest, private root, exact `gui/<uid>` domain/label, `LimitLoadToSessionType=Aqua` and `AbandonProcessGroup=true`, then separately authorize `service install --plan-file <json> --json`. The default state root, nonempty foreign state and an existing plist are refused. The root ownership marker permits reuse only by that same development instance; the candidate is never installed into the stable toolchain.

Qualify product survival with the explicit installed-service check in [TESTING.md](../TESTING.md), then separately stop/remove the exact instance with `service uninstall --plan-file <json> --json`. A reachable controller must have no occupied resource; inaccessible or uncertain state is preserved. Uninstall removes only its exact plist after successful bootout, leaving state and evidence. Failed bootstrap/bootout preserves the instance for inspection. TASK-011's current candidate has observed the actual product runner surviving SIGTERM and SIGKILL of its isolated Aqua controller on macOS arm64, with exact incarnation reattachment, unchanged run deadline and one workload execution. The Job finished PASS with verified cleanup and zero occupancy. This observation covers that development instance. TASK-016/TASK-038 retain their accepted UTM observations; TASK-027 requires the small exact-release running-VM loop. Power-loss durability remains unqualified and optional unless advertised; it is not a mandatory power-cut experiment.

## Current advisory lock operations

Register a lock resource without adapter/execution fields. Save the full acquire payload and request key, wait or replay to claim its current grant, then keep renewal in the acquiring process or `lock keepalive`. A disconnected wait never cancels the queued request; explicit cancellation and force release record the action and its reason. List all pages to inspect FIFO predecessors, grant generations and deadlines before operator action. Pause controls future grants only; update changes future policy only. Removal refuses live/queued requests and does not erase terminal keys or slot generations. Recovery is unsupported for advisory locks.

Pass an inherited grant only to a cooperating borrower that checks the selected installation and intended lock read-only. The owner renews and eventually releases; child completion or failure does neither. A stopped owner expires within its original liveness window, while every grant ends by its original maximum hold. Restart and observed wake extend liveness before expiry within that maximum. A clock-read failure prevents lock mutations rather than releasing capacity from uncertain evidence. These grants do not reconcile or free managed Job occupancy. See the [lock contract](contracts/locks.md) for the implemented Unix surface and exits.

## Prepare the macOS VM

Create one test-only macOS VM using UTM's supported path. Record host/guest OS, UTM/backend identity, CPU/memory/disk allocation, test user, GUI login/service requirements, and automation permissions. On another host, check the declared running-VM UTM/session/SSH prerequisites read-only from the chosen controller context. A full historical probe is not a setup requirement. Control UTM only from the Haepari LaunchAgent in the logged-in Aqua session; `utmctl` does not work from SSH sessions or before login. Enable Remote Login only for the required test user and provision a dedicated SSH credential/known-hosts mapping. This is a separate setup effect.

Use a guest test user, writable Haepari Job root and the selected runner/recipe prerequisites. Installing a full product/skill/MCP combination and checking its discovery is optional consumer recipe setup, not generic Haepari readiness. Keep real production accounts and data out of the VM. Provider authentication uses a separately approved test setup; document expiration and refresh rather than copying host authentication files.

Haepari uses this one VM for every VM job and never resets it ([ADR-0018](architecture-decision-records/0018-persistent-vm-no-reset.md)). Keep your own backup of the VM if you want a way back; restoring it is an operator action outside Haepari. A backup copy keeps the VM's MAC address, so never run it alongside the resource VM, and run `resource update` after a restore that changes the VM UUID. Use read-only preflight to check the selected running VM and existing UTM/SSH identity prerequisites. Execute generic Jobs and verify cleanup within the selected authorized test scope. Product installation is an optional consumer recipe, not a VM setup prerequisite.

Register the VM in Haepari as a `utm-macos` resource with its UTM UUID, MAC address, pinned SSH host key and guest job root. Automatic force-stop for host capacity is disabled by default; set `vm.allow_automatic_stop=true` only after deciding that this VM's persistent guest services and operator work may be interrupted while it is eligible and idle. Pause the resource for maintenance regardless of that policy. The address comes from a unique unexpired host DHCP lease entry for the MAC, as the [TASK-034 probe record](records/TASK-034-utm-probe.md) observed; a pre-start lease still needs current guest identity checks. Never disable host-key checking to make a connection work.

## Planned operational-readiness changes

[EPIC-009](roadmap/EPIC-009.md) has a separate [execution dossier](TODO-VM-OPERATIONAL-READINESS.md). TASK-036 implements the approved safe observation/reconciliation, missing-output and cross-boot decisions. TASK-037 implements bounded cold readiness, owned runner transport, explicit runner purge and startup limit selection. The following candidate controls do not qualify a live VM or replace TASK-038 observations.

TASK-038–039 are accepted historical qualifications with their recorded scope. Optional reruns of that full campaign keep the original exact candidate/service/resource and budget interface; ordinary development uses the scope above. Service registration or a running LaunchAgent is not a health result: require the control socket and a successful public read-only observation before workload. Do not bypass permissions, relocate binaries or manually delete guest files to unblock a campaign. The dossier states restoration, evidence and escalation requirements; DF-001–004 are optional hardening under Design Revision 3.5, with unobserved limits preserved.

## Current VM Job recovery

Use only a registered UUID/MAC with the exact private mapping and dedicated host-key pin described by the [guest protocol](contracts/resource-and-runner.md#ssh-and-guest-supervision). The ordinary SSH alias is insufficient. Automatic development Jobs use the same submit/wait/cancel commands as local Jobs; no real backend qualification is implied. Select the foreground host limit with `daemon run --host-macos-guest-limit 1|2` (default 1). VM removal remains unsupported.

After a disconnect or controller restart, inspect the original Job. Recovery reads its original guest boot, request, frozen runner, filesystem worker and native receipts; never issue a new workload to discover whether the old one ran. A no-start recovery first retires further start delivery. A live or unknown filesystem worker prevents collection/deletion and keeps occupancy. Foreign root incarnation or an ambiguous launch blocks recovery. A fresh boot of the same registered guest permits the explicit v2 recovery path below; ordinary execution remains bound to the original boot.

A terminal INCOMPLETE Job with retained evidence leaves its VM quarantined. The protected `workspace` / `job:<id>` reference identifies the entire owned guest Job directory, including logs and receipts. Use `resource recover <id> --plan --action recollect --json` to inspect exact supplemental collection effects, or `--action dispose` to inspect evidence loss. Applying the exact disposal token explicitly authorizes that loss. After resolution, obtain and apply a separate health plan; neither action rewrites the original verdict. Minimal fenced control metadata remains outside the deleted directory. Do not remove it or guest files manually to make a resource appear healthy.

## Run a project test

The [runnable project-owned wrapper](../examples/project-test/README.md) freezes selected candidate/test/profile/scenario files, saves exact specs and keys before submission, and prints the private run directory and Job IDs. Select `HAEPARI` and `RESOURCE` explicitly. Reconnect using the same `RUN_DIR`, resource, suite and mode; it reuses saved keys and observes the original Jobs. Read-only `doctor --resource <id> --json` checks current prerequisites without power or repair. Continue host development without installing the candidate into the stable host toolchain. Manual controller execution remains TASK-019 work.

To inspect a dropped session, use `inspect`, `events`, or `wait` on the original job. Do not submit a replacement merely because the caller lost its result. An explicit new run after a settled failure uses a new key and retains the link to the old result.

Before a bounded successor, inspect the resource and wait for `available` with zero occupancy after any required recovery. Admission can queue a Job while the resource is unavailable or quarantined; its original queue deadline can expire without reservation or workload execution. A terminal predecessor alone does not prove that the resource is reusable.

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

Use `job recover --plan` and `resource recover --plan` to inspect exact job/run, resource revision, generation, observations, uncertainty and proposed effects. Apply with a still-current token and provenance-bearing evidence within the established scope. Machine-proven ordinary reconciliation/health checks need no repeated approval; operator attestation and evidence-loss disposal retain their specific authority. An attestation stays labeled attested; unsupported or unknown execution remains unresolved. A mutation receipt records the exact outcome. Confirm identity and reusable state afterward. A successful recovery changes resource readiness, not the failed/inconclusive workload verdict.

## Update the VM

Make operator changes outside jobs, such as OS updates or the stable versions of shared tools, only in an explicit maintenance action: pause the resource, let running work and any power transition finish or reconcile, change the VM, record what changed, then resume. A paused resource cannot be selected for automatic forced stop, even if `vm.allow_automatic_stop=true`. Jobs change products through consumer recipes; those recipes define any product restoration. Haepari removes only its own Job files and does not require a full stable-product reinstall after ordinary tests. Consumer profiles change separately; create a new immutable profile rather than editing an old one.

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

**Queue deadlines on a capacity-one VM resource.** Set `queue_seconds` to at least (jobs ahead + 1) × the maximum job duration. The project must choose that budget before admission; `haepari doctor` observes resource prerequisites and does not calculate queue budgets.

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

Inspect configuration and availability revisions before `resource update`; supply both expected revisions and a successor configuration revision. An occupied or pending/uncertain power-protected resource refuses the update, and an idle update preserves maintenance pause. Keep automatic-stop authorization false unless the operator permits interrupting that persistent VM when eligible. TASK-015 implements automatic development VM Jobs, readiness-verified resume, bounded collection and revision-bound Job/resource recovery; VM resource removal remains unavailable. Follow the [VM recovery boundary](records/TASK-015-vm-job-recovery.md) for protected evidence and immutable terminal results. The accepted [F2 decision](records/TASK-016-vm-contract-freeze.md) records the real development-service qualification and its operating limits.


## TASK-014 SSH prerequisites and diagnosis

Target: one already registered persistent Darwin arm64 guest with operator-provisioned Remote Login, dedicated key authorization and a verified VM host-key pin. Provisioning accounts, enabling Remote Login and installing guest services require separate operator authority. Haepari transfers its transient executable per Job; it requires no permanent guest installation.

Prepare a private host mapping file containing canonical compact JSON, for example `{"schema":"haepari.ssh-mapping/v1","user":"<guest-user>","port":22,"identity_file":"<absolute-private-key-path>"}`. Replace the placeholders, protect both files as owner-private regular files, hash the mapping bytes and bind the result in `guest.connection_reference`. Update an existing resource only through its fenced idle successor configuration. Keep credentials outside selected input and generated artifacts. A configured `ssh UTM` alias may show how the operator connects; its address is not a replacement for Haepari's registered MAC lease and dedicated pin.

On authentication, lease, platform or boot mismatch, verify the exact resource configuration, current unique lease, mapping digest and dedicated pin. Do not use trust-on-first-use, disable strict checking, forward an agent or try another address. On transport loss after issuance, preserve the exact request/boot/run identity and guest files. Reconnect with inspect/watch/output, never relaunch the original command. `launching` without runner receipts remains ambiguous. Cancellation is complete only with the original run's termination-proven exit receipt. Collection and cleanup remain separate even then.

TASK-014's tests use fake UTM/SSH proof and local native guest protocol fixtures. No actual guest smoke is claimed without current registered UUID/MAC/pin and authorized setup. The [TASK-016 decision](records/TASK-016-vm-contract-freeze.md) records real installed-controller qualification, cleanup/reuse, screen-lock behavior and the candidate F2 boundary; the canonical roadmap owns its current acceptance. Actual DHCP expiry rejection, logout/post-login recovery and multi-day DHCP lease currency remain [DF-001 through DF-003](deferred-feedback.md) as optional hardening, with no TASK-027 deadline under Design Revision 3.5; screen lock does not prove logout recovery. Escalate partial preparation, ambiguous launch, retained evidence and cleanup to TASK-015's managed recovery owner; never delete guest Job files as diagnostic cleanup.

### Recover retained guest evidence with a scoped attestation

Use this path only for a finished retained VM Job with an immutable false native termination receipt and a quarantined, unoccupied resource. First obtain `haepari resource recover <resource-id> --plan --action attest-guest-stopped --json`. Unknowns or a missing token block the action. Inspect the original Job/run/root/boot, native receipt and legacy runner digests, stopped-owner observations and recovery executable digest. Preserve the original Job and controller attestation; neither is replaced.

Prepare an operator evidence file with source `operator-attested`, the operator identity and current UTC observation time, mechanism `guest-workload-and-descendants-stopped`, reference `job:<original-job-id>` and digest of the returned guest recovery basis. The assertion must cover the full original workload and descendants; observing absent PIDs is insufficient. Apply once with `--plan-token <token> --request-key <saved-key> --evidence-file <file> --json`. Save and reuse those exact arguments after a reply loss; never obtain a new deadline or key to repeat an uncertain guest effect.

After a completed attestation, obtain and apply a separate `recollect` plan. Verify its supplementary required-output inventory and native `guest_artifacts` inventory. Only complete verified collection permits cleanup of the one original guest Job. Attested evidence-loss `dispose` is unsupported. Collection or cleanup uncertainty retains quarantine and evidence; a recreated root, changed receipt, conflicting key or native observation failure must be reconciled rather than manually deleted. Obtain and apply a fresh resource health plan after resolved cleanup. Original results remain `INCOMPLETE` when originally inconclusive, even when supplementary collection succeeds and the resource becomes reusable. Requalification on the exact installed candidate remains a new TASK-016 campaign.

## EPIC-009 recovery candidate (TASK-036)

Use `haepari.job/v2` for absence-aware settlement. A positively absent required output yields INCOMPLETE with `required_output_absent`; verified collection of existing evidence, cleanup and health permit reuse. Existing uncollected evidence and uncertain identity/transport retain their fences. v1 admissions retain the historical quarantine policy. Inspect bounded `diagnostic` Job events and guest control classification codes; no diagnostic establishes termination. These record individual failed observations, including retries that later succeed; inspect the terminal result for the settled outcome. Read-only watch and file observations use at most three attempts with 100 ms and 200 ms backoff inside the existing absolute deadline. Start, cancel, retirement and cleanup are not retried by that mechanism. The controller revisits interrupted Jobs once per reconciliation pass, independently of scheduling, under their original run and phase budgets. A v2 VM Job may restore the exact quarantine caused by its live observation failure only at verified terminal settlement; operator restrictions and changed resource revisions remain in force.

For a guest reboot, first inspect the original Job and request `job recover <job-id> --plan --action reconcile --json`. A `haepari.recovery-plan/v2` token requires current registered identity/pin, a different observed guest boot and the exact original owned root/receipt inventory. Apply its saved token and key once. The recovery capability preserves original requests and receipts, retires delayed launch, proves old-boot cessation and settles INCOMPLETE with retained evidence. Then use `resource recover <resource-id> --plan --action recollect --json`, followed by a separate default resource-health plan/apply, to collect and verify evidence, remove only the bound root and permit reuse. Missing outcome/exit is explicit absence, never synthetic success. Unknown root, registration, original runner or receipt predecessors block the plan.

For an orphaned power intent, `resource recover <target-or-victim-id> --plan --action attest-power-stopped --json` returns `haepari.power-recovery-plan/v2`. Its required operator evidence must establish that the exact original command, descendants and framework effects can no longer execute, using mechanism `power-command-and-effects-ceased/v1` and reference `power:<generation>`. A VM's current stopped state alone is insufficient. Obtain the plan before making the cessation observation, then apply while both the plan and the observation are within their two-minute freshness windows. No unresolved transition means there is no power intent to retire. Multiple matches are outside the supported ledger invariant: preserve the state and investigate its history; no transition-selection or disposal bypass is supported. Apply the exact token, saved key and evidence file; apply re-observes both registered resources and fences their configuration, observation revisions, original claim/progress, generation and lease. It publishes separate `haepari.power-cessation/v2` authority and never reissues power or changes the original command-return proof. Job settlement and resource health remain separate. Unknown power, stale observations or unverifiable cessation stay protected.

Implementation fixtures do not establish TASK-038 real-VM qualification or revise TASK-016's historical observations or DF-001–003. A moved, missing or replaced prepared workspace blocks evidence deletion and reuse; preserve its original bytes and request a current recovery plan.

Cross-boot recollection requires the original owned guest root and receipt inventory. For v2 Jobs, a missing required output may resolve supplementary recovery only with positive absence under the original prepared workspace and validated reboot authority; inspect the private recovery receipt's separate absent-path inventory. Existing files and native logs must still be collected and verified. A newly existing file before cleanup blocks deletion, and original INCOMPLETE results stay unchanged.

An interruption after completed ordinary guest cleanup followed by a reboot has a separate read-only proof path. It requires the exact original completed control/native termination record, original host collection and cleanup intents, stopped filesystem owners and reverified host artifacts/logs. Controller reconciliation may consume that proof once without another workload or deletion. A fresh explicit reconcile plan exposes the same supported authority. A late host completion observation keeps the cleanup deadline and INCOMPLETE classification; a separately planned resource-health action may restore a quarantined resource only after the evidence hold is resolved and native readiness is freshly verified. Arbitrary pathname absence, false native proof, changed control, replacement root or missing/corrupt host evidence remains blocked.

A published reboot-recovery intent retains its original mutation key, evidence and expiry. After a lost guest reply, a fresh read-only observation may consume the exact completed guest attestation only when the original control retirement barrier is also durable. It grants no extra time to an incomplete or expired guest effect. A matching attestation without retirement remains blocked after expiry. Before the host recovery transaction commits, request a fresh reconcile plan and apply with the saved mutation key; the new plan does not replace or extend the original guest intent. After the host transaction commits, controller reconciliation may resume that exact explicit authority under the original phase deadlines. It issues no new guest attestation or workload. Retained native outcomes project their original workload outcome, exit code and signal; unavailable native outcomes remain inconclusive. Neither case upgrades missing evidence to PASS or rewrites a false native termination receipt.

After an unsuccessful recovery/v2 plan or apply, inspect the retained Job's bounded diagnostic events for its native classification and any uncertain-effects code. They provide context only; use fresh identity-bound plans and retained proof before an effect. Later diagnostics supplement a finished outcome without rewriting it, and successful observations add no failure event.

## TASK-037 operating controls and TASK-038 handoff

This section preserves the accepted historical campaign and its optional rerun procedure. It is not the ordinary development scope or TASK-027 release checklist; follow [ADR-0020](architecture-decision-records/0020-local-development-scope.md) for those boundaries.

For TASK-038, reuse one existing registered capacity-one VM with `daemon run
--global-limit 1 --host-macos-guest-limit 1`. For a development service, prepare a
fresh private `service plan --instance <id> --binary <absolute-candidate>
--root <private-root> --global-limit 1 --host-macos-guest-limit 1 --json`; save the
complete v2 plan before requesting installation authority. The defaults remain
one for each limit. Existing plans and running services do not change. The
implemented `--global-limit 3 --host-macos-guest-limit 2` profile supports two
distinct capacity-one VM resources plus independent local work, but its real
qualification is deferred to [DF-004](deferred-feedback.md#df-004-two-vm-and-local-concurrency).

Set finite queue/prepare/run/collect/cleanup budgets in each Job before admission.
Cold readiness consumes prepare time; safe observation and compact collection
retain original deadlines. Save original spec bytes and request keys and inspect
original Jobs after interruption. A failed pinned/lease/readiness check never
permits early guest staging. A failed or unreturned power effect retains its
intent for explicit recovery. After verified terminal cleanup, request explicit
revision-bound `job purge`; active/unresolved or unknown runner payloads refuse.
Purge preserves permanent keys and required receipts. Do not remove payloads
manually to cure an uncertain Job.

TASK-038 must bind exact source/executable/guest-runner identities, the approved
service plan and the existing registered VM UUID/MAC/pin mapping before live execution.
Record public phase timings and versioned SSH exchange start/completion facts
before any event-prefix purge. Keep incomplete pairs and the detail-limit gap
visible. If optional network/protocol measurements are recorded, distinguish them
from application archive bytes. No separate network benchmark or identity-refusal
fault experiment is required for the same-machine, same-user EPIC-009 scope.
Record log/output sizes and hashes, occupancy and runner storage
before/after explicit purge and successor reuse. Readiness, 50 MiB collection,
30-Job soak and permitted observation fault injection need their separately
approved finite campaign and restoration plan. Master excluded VM power-on/off,
cold-start, reboot and power-intent scenario qualification from EPIC-009 on
2026-10-12; reuse the already-running VM without switching its power. The fixture measurements in ADR-0019 establish
transport behavior only. TASK-016 history and the unresolved DF-001–003 uncertainties remain; ADR-0020 removes their release deadlines and makes the experiments optional.

Use the tracked [single-VM campaign instructions](../tests/qualification/README.md)
for private manifest/authority inputs and `make test-utm` preflight/run/resume/report.
Preflight makes read-only public observations and binds the actual running
controller; it grants no service replacement or VM effect authority. Preserve
the frozen spec bytes and original keys/deadlines when resuming. An uncertain
control return blocks reissue and requires the disclosed restoration. The driver
collects observations; the owning workflow assesses the native matrix before
TASK-038 acceptance.
