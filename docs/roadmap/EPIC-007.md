# EPIC-007: Release and Aquarium producer handoff

Ship an independent, documented, tested release that Aquarium can consume.

Identity, status and ordering belong to the [canonical roadmap](../roadmap.md). These checklists define implementation acceptance only.

## TASK-025

### Package the release, user services and paired skill

Requirement coverage: `HP-REQ-054`, `HP-REQ-055`.

#### Implement
- [x] Expose only qualified managed protocol, local/UTM, blocking wait, manual, cancellation, recovery and `advisory-lock/v1` capabilities. Teach use-haepari same-key observation, truthful results and the lock flow: the acquiring owner waits, renews while its work runs, and releases on every completion path; a nested borrower only validates and uses the grant.
- [x] Produce checksummed native artifacts, complete use-haepari skill, explicit user-service installers, and frozen capability output. Keep binaries independent of a source checkout or plugin cache.
- [x] Package the release LaunchAgent with the configuration decided in TASK-004 and verified under TASK-011's development service. No signing identity is required ([TASK-034 probe record](../records/TASK-034-utm-probe.md)). TASK-027 re-verifies the packaged service with real UTM.

#### Do not
- [x] Do not auto-install into production while packaging, auto-enroll UTM, or add a required MCP transport.

#### Verify
- [x] Install into an isolated test root in the same user, verify packaged skill files/documented CLI flow and service lifecycle, and run native submit/wait/reconnect/manual/cancel scenarios. No extra host account or model/provider behavior session is required.
- [x] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-026

### Finalize operator and developer documentation

Requirement coverage: `HP-REQ-056`, `HP-REQ-057`.

#### Implement
- [x] Reconcile requirements, architecture, CLI help, examples, security guidance, project-owned testing, optional consumer skill/migration guidance and legacy-retirement boundaries against implemented behavior.
- [x] Reconcile stale implementation/candidate wording and command availability across public and maintainer documents, using the [operational feedback disposition](../TODO-VM-OPERATIONAL-READINESS.md#feedback-disposition) without rewriting historical records as current observations.

#### Do not
- [x] Do not describe planned capabilities as shipped or leave executable-looking unmarked placeholders.

#### Verify
- [x] Validate local links/examples, inspect command help, follow the runbook in a disposable environment and record unresolved setup limitations.
- [x] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-027

### Run exact release acceptance

Requirement coverage: `HP-REQ-031`, `HP-REQ-058`, `HP-REQ-059`.

#### Implement
- [x] Confirm that freeze gates F1, F2 and F3 hold for the candidate, freeze candidate binary/runner/skill identities, and execute release acceptance: deterministic failure coverage, real local and one already-running UTM resource, packaged skill/CLI flow, ordinary manual evidence and self-test protection.
- [x] Run the small TASK-017 through TASK-020 core auto/manual sample on the running VM under the packaged release service: success, meaningful failure, cancel, timeout, logs/outputs, bounded SSH observation loss/reconnection, cleanup and successor reuse. Bind actual candidate/input/result identities automatically. Do not rerun the full historical TASK-038 campaign or require DF-001–004, live identity-refusal, product skill/migration, soak or benchmark experiments.

#### Do not
- [x] Do not infer distribution success from a local build or publish automatically from a test result.

#### Verify
- [x] Require managed local and real UTM evidence, all 58 mandatory V-invariants in their declared local/fake/live layers, exact artifacts and truthful skill/runtime limits. E01 through E20 remain deferred.
- [x] Record exact artifacts, platform inventory, all required outcomes and evidence completeness; a changed artifact invalidates affected acceptance.
- [x] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-028

### Deliver the Aquarium consumer handoff

Requirement coverage: `HP-REQ-060`, `HP-REQ-061`.

#### Implement
- [x] Deliver the managed schemas, real local/UTM evidence, the advisory lock contract that Mulgae's review queuing needs, and the managed-first Mulgae VM adoption packet. External lease schemas and evidence are deferred.
- [x] Complete the capability and artifact handoff with verified examples, VM prerequisites, operator actions, known limits and the independent installation path. Supply the implementation-complete result without changing Aquarium.

#### Do not
- [x] Do not require Aquarium adoption to complete Haepari, send Sorage messages without the current authorized delivery scope, or remove aquarium-dev state.

#### Verify
- [x] Review the handoff against the frozen managed contract and ensure Aquarium can start Aquarium:TASK-086 without a future Haepari capability. State the Aquarium-side plan alignment in [external requests](../to-external.md) as a consumer prerequisite, not a Haepari completion dependency. Preserve publication/install as separate effects.
- [x] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## Epic acceptance

- [ ] TASK-025 through TASK-028 meet their individual acceptance criteria.
- [ ] The epic goal is demonstrated through a bounded integrated scenario, including a relevant failure and recovery path.
- [ ] Every changed public contract is documented and no downstream task relies on an unstated capability.
- [ ] An explicit epic acceptance updates only the canonical roadmap; task checkboxes alone do not complete the epic.
