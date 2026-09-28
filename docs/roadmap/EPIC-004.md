# EPIC-004: UTM macOS and SSH backend

Qualify a real resettable macOS VM without duplicating host-control logic in projects. TASK-034 probes the host early; TASK-013 through TASK-015 implement and verify against the fake UTM/SSH backend; TASK-016 is the real backend qualification and freeze gate F2. The epic may be In progress with TASK-034 or TASK-016 Blocked on a missing UTM environment while TASK-013 through TASK-015 proceed.

Identity, status and ordering belong to the [canonical roadmap](../roadmap.md). These checklists define implementation acceptance only.

## TASK-034

### Probe UTM host automation non-destructively

Requirement coverage: `HP-REQ-006` (UTM probe), `HP-REQ-026`.

#### Implement
- [ ] As soon as authorized UTM access exists, run a non-destructive probe. Its only prerequisite is TASK-001; the TASK-011 service may not exist yet.
  - Run the probe from a minimal probe LaunchAgent (`gui/<uid>`, `LimitLoadToSessionType=Aqua`) whose executable has a stable signing identity.
  - Use an operator-designated source VM, never a baseline. Clone it exactly once, and delete the clone at the end.
- [ ] Record:
  - `utmctl` version, `list` and `status`.
  - UTM UUID, MAC address and machine/serial identity of the clone, compared with the source.
  - Guest address discovery through the host DHCP lease table by MAC, compared with an explicit mapping.
  - Automation/TCC prompts and the approving executable identity.
  - Host guest limit: start the source and the clone together, then attempt a third start with one more operator-designated test VM if one exists. Record the error.
- [ ] Publish the observations as a decision record for TASK-013 and TASK-014.

#### Do not
- [ ] Do not touch a baseline, reuse a production VM, install software in a guest, upgrade UTM, or treat probe results as backend qualification.

#### Verify
- [ ] Every recorded observation cites the exact host, UTM and macOS versions. If UTM or the permission is unavailable, the task stays Blocked with that gap; TASK-013 through TASK-015 continue against the fake backend with their assumptions listed as risks.

#### Done when
- [ ] The decision record is reviewed, and each unresolved observation is named as a TASK-016 prerequisite.

## TASK-013

### Implement owned UTM baselines and registration

Requirement coverage: `HP-REQ-024`, `HP-REQ-025`.

#### Implement
- [ ] Implement the whole-package baseline/working-copy lifecycle, exact UUID/path mapping, operation receipts and partial clone/registration recovery, using the TASK-034 decision record when available.
- [ ] Bind each baseline to exactly one `utm-macos` resource. Reject a job whose profile digest differs from its resource's baseline profile (`PROFILE_MISMATCH`); install and migration starting profiles use different resources.

#### Do not
- [ ] Do not execute the template, mutate running packages, delete by display-name search, or assume snapshot support.

#### Verify
- [ ] Use fake interface tests to verify baseline integrity checks, metadata/disk completeness checks, duplicate baseline refusal, profile mismatch and partial-operation reconciliation. The actual authorized cold-copy trial belongs to TASK-016.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-014

### Implement authenticated SSH and guest runner continuity

Requirement coverage: `HP-REQ-026`, `HP-REQ-027`, `HP-REQ-028`.

#### Implement
- [ ] Transfer the digest-pinned guest runner with each job's sealed bundle rather than baking it into the baseline. Use a fixed structured remote protocol, enforce known-hosts and guest identity (including the working copy's MAC-to-address mapping), and start or reattach transient guest supervision.
- [ ] Write guest run receipts (request, spawn intent, start, exit) by temporary file, file sync, rename and directory sync, as the [runner contract](../contracts/resource-and-runner.md#runner-receipts) specifies.

#### Do not
- [ ] Do not forward the host agent, copy host auth, require QEMU guest operations, or concatenate untrusted remote shell commands.

#### Verify
- [ ] Test wrong host/nonce/baseline, argument encoding, transport loss, same-run reconnect, guest reboot, and cancelled SSH with a surviving workload.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-015

### Integrate VM collection, reset and recovery

Requirement coverage: `HP-REQ-029`, `HP-REQ-030`.

#### Implement
- [ ] Join UTM and SSH phases with the resource lease, capture evidence before reset, verify full guest restoration and implement exact working-VM recovery.
- [ ] Enforce the configured host macOS guest limit as a reservation guard. A framework VM-limit error without a guest start is a temporary prerequisite gap: clean up the working copy and finish INCOMPLETE without quarantine.

#### Do not
- [ ] Do not make project recipes host reset hooks or free a resource with unverified service/skill/config state.

#### Verify
- [ ] With the fake backend and scripted guest fixtures, seed modifications across files, skills, configuration and services; prove removal after restore and quarantine after every reset failure point. The real restoration proof belongs to TASK-016.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-016

### Qualify the real macOS VM backend; freeze VM contract fields

Requirement coverage: `HP-REQ-031`, `HP-REQ-062` (freeze gate F2).

#### Implement
- [ ] Under the development LaunchAgent from TASK-011, run the exact install/execute/failure/disconnect/cancel/collection/reset campaign on the installed UTM combination, including a real cold-copy trial, then admit a second job from a clean baseline.
- [ ] Freeze gate F2: freeze the `utm-macos` resource fields, baseline registration, reset strategy, guest runner protocol and host-session identity fields, recording each change since TASK-002 and its evidence.

#### Do not
- [ ] Do not treat mocked UTM or an SSH-only smoke test as full backend acceptance.

#### Verify
- [ ] Retain actual platform, controller/runner, VM UUID/baseline and phase evidence. Require no host runtime changes outside declared owned resource state.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## Epic acceptance

- [ ] TASK-034 and TASK-013 through TASK-016 meet their individual acceptance criteria.
- [ ] The epic goal is demonstrated through a bounded integrated scenario, including a relevant failure and recovery path.
- [ ] Every changed public contract is documented and no downstream task relies on an unstated capability.
- [ ] An explicit epic acceptance updates only the canonical roadmap; task checkboxes alone do not complete the epic.
