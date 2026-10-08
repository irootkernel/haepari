# EPIC-004: UTM macOS and SSH backend

Qualify one persistent macOS VM per resource ([ADR-0018](../architecture-decision-records/0018-persistent-vm-no-reset.md)) without duplicating host-control logic in projects. TASK-034 probed the host before bootstrap ([probe record](../records/TASK-034-utm-probe.md)); its Accepted status does not start this epic's implementation. TASK-013 through TASK-015 implement and verify against the fake UTM/SSH backend; TASK-014 may also record an optional early real-guest smoke observation. TASK-016 is the mandatory real backend qualification and freeze gate F2. Once implementation begins, the epic may be In progress with TASK-016 Blocked on a missing UTM environment while TASK-013 through TASK-015 proceed.

Identity, status and ordering belong to the [canonical roadmap](../roadmap.md). These checklists define implementation acceptance only.

## TASK-034

### Probe UTM host automation non-destructively

Requirement coverage: `HP-REQ-006` (UTM probe), `HP-REQ-026`.

Accepted on 2026-09-28 on the owner's host; the observations are in the [probe record](../records/TASK-034-utm-probe.md). The probe ran before TASK-001 with a scratch program and needs no product code.

#### Implement
- [x] Run the probe from one-shot probe LaunchAgents (`gui/<uid>`, `LimitLoadToSessionType=Aqua`) on an operator-designated VM. No signing identity is required.
- [x] Record:
  - `utmctl` `version`, `list`, `status`, `start` and `stop`, with exit codes and stderr on failure.
  - Guest address discovery through the host DHCP lease table by MAC, compared with the VM's configured MAC.
  - Automation/TCC prompts and the approving executable identity.
- [x] Publish the observations as a decision record for TASK-013 and TASK-014.

#### Do not
- [x] Do not upgrade UTM, install software in a guest, or treat probe results as backend qualification.

#### Verify
- [x] Every recorded observation cites the exact host, UTM and macOS versions.

#### Done when
- [x] The decision record is reviewed, and each unresolved observation is named as a TASK-016 prerequisite.

## TASK-013

### Implement persistent VM registration and UTM control

Requirement coverage: `HP-REQ-024`, `HP-REQ-025`.

#### Implement
- [x] Register a `utm-macos` resource by VM UUID, MAC address and pinned SSH host key. Refuse an unknown UUID, and refuse a second resource with the same UUID or MAC address. Registration never creates, clones, boots or deletes a VM.
- [x] Implement the `utmctl` adapter (`status`, `start`, forced `stop`) with the rules in the [TASK-034 probe record](../records/TASK-034-utm-probe.md): a non-zero exit or `Error from event` on stderr is a failure, and `ip-address` is not used.
- [x] Apply operator-owned `vm.allow_automatic_stop` (default false) and the existing paused state as maintenance protection. Issue no automatic stop unless the durable victim/target power-transfer intent is current; reconcile ambiguous or stale responses before releasing protection.
- [x] Find the guest address in exactly one host DHCP lease entry for the registered MAC with a known, unexpired individual expiry. Permit a pre-start unexpired entry only after current VM, SSH host-key, guest MAC and boot/run identity checks; do not infer entry freshness from the lease file mtime.
- [x] Classify an exact registered VM's proven no-effect prerequisite failure as resource-local `vm_unavailable`. Fence the failure observation by current VM/resource identity and revision; never use this classification for an uncertain power operation, a stale registration or a failure after possible guest effects.

#### Do not
- [x] Do not clone, delete or edit a VM, use AppleScript, read UTM's data container, or select a VM by display name.

#### Verify
- [x] Use fake interface tests to verify registration refusals (unknown UUID, duplicate UUID or MAC), `utmctl` failure detection including exit 0 with `Error from event`, and expired, pre-start, renewed, missing or ambiguous leases. Test a paused victim, concurrent start, ineligible victims, stale response and unregistered VM exclusion. The real trial belongs to TASK-016.
- [x] Verify that a VM A lease or identity failure pauses only A, a stale observation cannot pause a newer A registration, an already quarantined A remains quarantined, and uncertain start/stop effects stay under power reconciliation. VM B and independent local resources remain eligible. The real trial belongs to TASK-016.
- [x] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-014

### Implement authenticated SSH and guest runner continuity

Requirement coverage: `HP-REQ-026`, `HP-REQ-027`, `HP-REQ-028`.

#### Implement
- [x] Transfer the digest-pinned guest runner with each job's sealed bundle rather than preinstalling it in the guest. Use a fixed structured remote protocol, enforce known-hosts and guest identity (including the registered MAC-to-address lease), and start or reattach transient guest supervision.
- [x] Write guest run receipts (request, spawn intent, start, exit) by temporary file, file sync, rename and directory sync, as the [runner contract](../contracts/resource-and-runner.md#runner-receipts) specifies.
- [ ] When an authorized real guest with Remote Login is available, perform a harmless early smoke observation: start once over SSH, disconnect, reconnect by the same run identity, read the original receipts/output without executing again, then request cancellation and verify termination. Record it as exploratory evidence only; absence of this setup does not block fake-backend or local development and does not complete TASK-016.

Optional real-guest branch remains unobserved: no current product registration/pin/mapping is verified for the SSH alias. This does not qualify TASK-016 or F2.

#### Do not
- [x] Do not forward the host agent, copy host auth, require QEMU guest operations, or concatenate untrusted remote shell commands.

#### Verify
- [x] Test wrong host/nonce/VM, argument encoding, transport loss, same-run reconnect, guest reboot, and cancelled SSH with a surviving workload.
- [x] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-015

### Integrate VM collection, job cleanup and recovery

Requirement coverage: `HP-REQ-029`, `HP-REQ-030`.

#### Implement
- [x] Join UTM and SSH phases with the resource lease. Capture evidence before deleting guest Job files; remove those files after complete collection, or retain required uncollected evidence and quarantine the VM. Quarantine also when owned process termination or cleanup cannot be confirmed, and implement exact VM recovery.
- [x] Separate owned process termination from deletion of guest Job files. On failed collection preserve required recoverable evidence with durable references, settle proven termination INCOMPLETE while quarantined, or retain occupancy when termination is unproven. Resource recovery re-collects or records explicitly authorized loss, then cleans and verifies health; later evidence never rewrites the original verdict.
- [x] Enforce the configured host macOS guest limit before starting a stopped VM: stop only an eligible, operator-authorized registered VM under the fenced transfer intent, or keep the Job queued with its original deadline and blocker. A framework VM-limit error without a guest start is a temporary prerequisite gap: finish INCOMPLETE without quarantine.
- [x] For VM-local proven no-effect preparation failure, commit `vm_unavailable` pause and the Job's failure classification/entry into `settling` in one transaction, before INCOMPLETE/`not_needed` settlement returns capacity. Link the pause receipt's successor revision to the original Job lease. Preserve an existing pause or quarantine and require explicit verified resume after remediation; host-wide failures keep their host-wide path.

#### Do not
- [x] Do not make project recipes host hooks, reset or delete the VM, or free a resource while a Haepari runner may still be active.

#### Verify
- [x] With the fake backend and scripted guest fixtures, prove that a next job runs only after verified cleanup, workload changes outside the job directory persist, and every cleanup or retained-evidence path quarantines the resource. Include disk-full collection, controller restart, bounded re-collection, explicit disposal and unknown workload/collector termination. The real proof belongs to TASK-016.
- [x] Verify A stays unavailable to later Jobs across pause, settlement replay and restart, while B/local resources can dispatch; confirm one capacity return, stale-response rejection, quarantine preservation, and dispatch only after explicit readiness-verified resume. The real proof belongs to TASK-016.
- [x] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-016

### Qualify the real macOS VM backend; freeze VM contract fields

Requirement coverage: `HP-REQ-031`, `HP-REQ-062` (freeze gate F2).

**Owner-approved qualification deferrals (2026-10-05):** Actual registered DHCP lease-expiry rejection, host logout/post-login recovery and multi-day DHCP lease currency are waived for the current TASK-016 campaign and tracked as [DF-001 through DF-003](../deferred-feedback.md). All three remain unobserved and must be verified before TASK-027 release acceptance. This scoped waiver permits current work to proceed; it does not record either check as passed or remove its later verification obligation.

#### Implement
- [x] Under the development LaunchAgent from TASK-011, run the exact install/execute/failure/disconnect/cancel/collection/cleanup campaign on the installed UTM combination and the persistent VM. Then run a second job after verified cleanup and show that the first job's Haepari files are gone; a retained-evidence path must instead block reuse until recovery. Observe the TASK-016 prerequisites named in the [TASK-034 probe record](../records/TASK-034-utm-probe.md); the host guest limit needs three macOS VMs, so record it as unobserved if they are not available. An optional TASK-014 smoke observation is not this mandatory qualification.
- [x] Qualify DHCP lease expiry/renewal and pinned guest identity on the real backend. If enough authorized VMs exist, observe the paused-victim and fenced stop/start capacity paths; otherwise record those host-limit observations as unobserved while retaining their fake-backend proofs.
- [x] Freeze gate F2: freeze the `utm-macos` resource fields, guest runner protocol and host-session identity fields, recording each change since TASK-002 and its evidence.

#### Do not
- [x] Do not treat mocked UTM or an SSH-only smoke test as full backend acceptance.

#### Verify
- [x] Retain actual platform, controller/runner, VM UUID and phase evidence. Require no host runtime changes outside declared owned resource state.
- [x] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete, subject to the owner-approved qualification deferrals above.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## Epic acceptance

- [ ] TASK-034 and TASK-013 through TASK-016 meet their individual acceptance criteria.
- [ ] The epic goal is demonstrated through a bounded integrated scenario, including a relevant failure and recovery path.
- [ ] Every changed public contract is documented and no downstream task relies on an unstated capability.
- [ ] An explicit epic acceptance updates only the canonical roadmap; task checkboxes alone do not complete the epic.
