# EPIC-001: Contract, first local slice and OS decisions

Define a provisional contract, deliver the first production-path local slice, and record observed macOS supervision and durability decisions. The UTM probe is TASK-034 in EPIC-004.

Identity, status and ordering belong to the [canonical roadmap](../roadmap.md). These checklists define implementation acceptance only.

## TASK-001

### Bootstrap the independent Go product

Requirement coverage: `HP-REQ-001`, `HP-REQ-002`.

#### Implement
- [ ] Create the Go module, native CLI/daemon/runner entrypoint structure, platform build matrix, private state-root resolver and machine-readable identity. Select and pin a supported Go toolchain and pure-Go SQLite dependency after primary-documentation and build verification.
- [ ] Record the bootstrap decisions. The owner confirmed these on 2026-09-28; they follow sibling repositories:
  - Module path: `github.com/irootkernel/haepari`, as used by seongge, mulgae, gaori and sanho.
  - License: MIT, as used by gaebokchi, mulgae, podway and sorage.
  - Root `README.md` and `AGENTS.md`: authority summary, commands and prohibited actions.
  - Make targets: `build`, `test`, `lint`, `test-integration` and a combined `check`, plus the explicit real-environment gates `test-macos-supervision` and `test-utm`.
  - CI: local `make check` is the required gate. Hosted CI is optional and never runs real UTM.
  - Supported macOS test environment and its owner.
- [ ] Tests create Unix sockets under a short private root such as `/tmp/hp-<random>`. macOS limits `sun_path` to 104 bytes, and `t.TempDir()` paths can exceed it.

#### Do not
- [ ] Do not install or replace any host service, create Aquarium coupling, or claim a platform from cross-compilation alone.

#### Verify
- [ ] Build each target, exercise isolated local startup/version, reject unsupported platform/backend selection, and record dependency/license review.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-002

### Define provisional managed schemas and state invariants

Requirement coverage: `HP-REQ-003`, `HP-REQ-062`.

#### Implement
- [ ] Define initial managed schemas, capacity accounting, invariants and examples. Mark them provisional during internal implementation.
- [ ] Keep closed job/resource/result/envelope schemas, lifecycle/outcome enums, error codes, CLI exit codes, canonicalization, event cursors and manual results provisional. Later tasks record changes from TASK-033 and failure evidence. This task does not freeze the contract; freeze gate F1 (TASK-012) and gate F2 (TASK-016) do.

#### Do not
- [ ] Do not allow unknown JSON to trigger best-effort execution or add a second source of lifecycle truth.

#### Verify
- [ ] Test malformed/duplicate/unknown fields, missing required output, invalid transitions, stale revisions and every final verdict combination.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-003

### Implement contained snapshots and identity

Requirement coverage: `HP-REQ-004`, `HP-REQ-005`.

#### Implement
- [ ] Implement explicit selected-file ingestion, versioned tree digests, executable modes, regular-file containment and content sealing. Preserve base Git identity separately from actual dirty snapshot identity.

#### Do not
- [ ] Do not read the whole home, follow links outside scope, copy credentials, mutate Git state, or infer current bytes from HEAD.

#### Verify
- [ ] Test concurrent file edits, removed inputs, path traversal, links, case collisions, changed executable bits and input reuse after source deletion.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-033

### Deliver the first production-path local walking skeleton

Requirement coverage: `HP-REQ-007`, `HP-REQ-009`, `HP-REQ-010`, `HP-REQ-011`, `HP-REQ-012`, `HP-REQ-014`, `HP-REQ-015`, `HP-REQ-016` (initial slice; TASK-005–012 harden the same path).

#### Implement
- [ ] Start an isolated controller using the intended state store and client transport. Submit a harmless local argv command with sealed input and a persisted request key; commit admission before returning its identity. Wait through the intended blocking client path, read the truthful terminal result, then reconnect using the same key without another execution. Reuse the production admission, runner and result path; later tasks extend it.
- [ ] Limit the slice to:
  - one local resource with capacity one and a global limit of one
  - the real scheduler code path that TASK-007 later extends
  - the managed transition rows for submit, reserve, prepare, start automatic run, exit, collection, cleanup and settlement

#### Do not
- [ ] Do not create a demo scheduler, bypass durable identity, hide failure as PASS, operate a VM, or treat the initial slice as full release qualification.

#### Verify
- [ ] Assert one admission and one observed execution after lost/repeated submission, terminal workload/evidence/cleanup fields, wait disconnect/reconnect and a changed-payload conflict in isolated state.

#### Done when
- [ ] The exact integrated slice and its failure case are reviewed; provisional schema changes are recorded for freeze gate F1 in TASK-012.

## TASK-004

### Qualify general macOS OS supervision

Requirement coverage: `HP-REQ-006`.

#### Implement
- [ ] On an isolated macOS host, observe and record a decision for each of the following. TASK-005, TASK-009 and TASK-011 consume the decision record.
  1. **Development LaunchAgent.** Configuration: `gui/<uid>` domain, `LimitLoadToSessionType=Aqua`, `AbandonProcessGroup=true`. A runner started in its own session and process group must survive controller exit and restart.
  2. **SQLite durability.** On darwin, use `synchronous=FULL` with `PRAGMA fullfsync=ON` and `PRAGMA checkpoint_fullfsync=ON`. Record whether the pinned pure-Go driver honors these settings.
  3. **Unix socket paths.** Record the path limits and adopt the short-root rule.
  4. **Process groups.** Record process-group termination and escaped-descendant detection limits. Detection is best effort on macOS.

#### Do not
- [ ] Do not assume `setsid` alone defeats service-manager cleanup, claim power-loss durability from kill tests, or operate UTM. TASK-034 probes UTM.

#### Verify
- [ ] Record the observed behavior behind each decision.
- [ ] If the driver ignores the fullfsync settings, record that the power-loss durability claim is blocked.
- [ ] Restart reconciliation itself is verified in TASK-011; TASK-016 owns actual UTM identification, SSH and reset qualification.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## Epic acceptance

- [ ] TASK-001 through TASK-004 and TASK-033 meet their individual acceptance criteria.
- [ ] The epic goal is demonstrated through a bounded integrated scenario, including a relevant failure and recovery path.
- [ ] Every changed public contract is documented and no downstream task relies on an unstated capability.
- [ ] An explicit epic acceptance updates only the canonical roadmap; task checkboxes alone do not complete the epic.
