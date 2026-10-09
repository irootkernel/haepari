# EPIC-006: Failure, privacy and platform hardening

Prove finite recovery, resource bounds, supported platforms and self-test isolation.

Identity, status and ordering belong to the [canonical roadmap](../roadmap.md). These checklists define implementation acceptance only.

## TASK-021

### Complete concurrency and crash qualification

Requirement coverage: `HP-REQ-045`, `HP-REQ-046`.

#### Implement
- [ ] Execute the mandatory failure matrix across admissions, leases, runners, events, manual state and terminal settlement with deterministic, bounded fault injection.
- [ ] Evaluate repeated settlement/receipt-import paths, filesystem durability primitives and test-only export/substitute paths identified by the [operational feedback disposition](../TODO-VM-OPERATIONAL-READINESS.md#feedback-disposition). Consolidate only where a concrete invariant or drift justifies it; no universal backend abstraction is prescribed.

#### Do not
- [ ] Do not use sleeps alone as concurrency proof or rerun unknown external effects to make a flaky test pass.

#### Verify
- [ ] Run managed capacity cases including nested acquisition at global limit one and multiple occupied slots, a nested caller's rejected blocking wait, independent resource progress with `global_limit` of at least two, and stale return races.
- [ ] Run the advisory lock cases (V59–V64) under concurrent callers alongside managed jobs.
- [ ] Map every matrix row to assertions, run repeated stress scenarios with finite bounds, and review resource/job state after restart.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-022

### Harden trust, privacy and external-effect boundaries

Requirement coverage: `HP-REQ-047`, `HP-REQ-048`, `HP-REQ-049`.

#### Implement
- [ ] Audit socket/path ownership, registered host commands, scoped input snapshots, SSH identity, secret references, evidence redaction and offline/live separation.
- [ ] Check event-visible metadata versus private replay authority and investigate concrete error-classification/atomicity scenarios from the [feedback disposition](../TODO-VM-OPERATIONAL-READINESS.md#feedback-disposition); metadata alone is not mutation authorization.

#### Do not
- [ ] Do not claim same-user sandboxing or automatic rollback of external messages, database writes or charges.

#### Verify
- [ ] Verify managed parent-context privacy, recovery provenance and refusal to manufacture stop evidence from lost process handles.
- [ ] Run malicious-path/request fixtures, wrong-target VM tests, log/secret scans and missing-live-prerequisite rejection. Review each destructive path against ownership evidence.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-023

### Measure bounds and qualify supported platforms

Requirement coverage: `HP-REQ-050`, `HP-REQ-051`.

#### Implement
- [ ] Implement/configure input, output, event and phase bounds; measure controller memory/log streaming; execute local backend on qualified macOS targets and test control without Aquarium dependencies.
- [ ] Complete the general operator configuration loader and doctor diagnostics declared by the configuration contract, including explicit retention policy. TASK-037 owns only minimum startup global-limit selection and safe runner payload disposal; automatic deletion must preserve protected evidence and permanent replay identities.
- [ ] Verify running-VM log usability and measure idle lock-clock writes, native process-census cost, controller/collection performance and check duration. Preserve clock, process-identity and evidence guarantees before optimizing; [feedback disposition](../TODO-VM-OPERATIONAL-READINESS.md#feedback-disposition) records which claims still need measurement.

#### Do not
- [ ] Do not hide output truncation or publish unmeasured performance/platform claims.

#### Verify
- [ ] Meet or explicitly resolve the documented memory budgets, record machine/load conditions, and prove inspection/cancellation/recovery without tools under test.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-024

### Qualify storage compatibility and self-test isolation

Requirement coverage: `HP-REQ-052`, `HP-REQ-053`.

#### Implement
- [ ] Exercise Haepari schema migration under exclusive ownership, consistent backup/recovery, too-new rejection and candidate execution with an isolated state/socket.
- [ ] Review repeated migration/replay authority and retained-event allowlists from the [feedback disposition](../TODO-VM-OPERATIONAL-READINESS.md#feedback-disposition). Preserve existing-store and legacy-evidence safety; this plan does not authorize deleting migration history.

#### Do not
- [ ] Do not replace the stable controller running its own campaign or downgrade a live SQLite store.

#### Verify
- [ ] Test initial schema creation, unsupported newer schema rejection and synthetic migration-framework fixtures. An actual prior-public-release upgrade is not applicable until a predecessor exists.
- [ ] Create synthetic prior-schema fixtures, fail each migration boundary, restore safely and verify production controller/state/lease identities are unchanged.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## Epic acceptance

- [ ] TASK-021 through TASK-024 meet their individual acceptance criteria.
- [ ] The epic goal is demonstrated through a bounded integrated scenario, including a relevant failure and recovery path.
- [ ] Every changed public contract is documented and no downstream task relies on an unstated capability.
- [ ] An explicit epic acceptance updates only the canonical roadmap; task checkboxes alone do not complete the epic.
