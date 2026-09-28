# EPIC-005: Project, skill, manual and migration integration

Support project-owned VM suites and complete installation-unit evidence, then freeze the consumer integration contract (gate F3).

Identity, status and ordering belong to the [canonical roadmap](../roadmap.md). These checklists define implementation acceptance only.

## TASK-017

### Publish the project Make and guest integration contract

Requirement coverage: `HP-REQ-032`, `HP-REQ-033`, `HP-REQ-034`.

#### Implement
- [ ] Provide sample host submission wrappers and distinct guest entrypoints, immutable bundle packaging and separate install/migration/regression/all dispatch. Keep ordinary test targets independent.

#### Do not
- [ ] Do not create a mandatory integration-test repository, install products on the host, or enqueue recursively inside the guest.

#### Verify
- [ ] Integrate two synthetic project fixtures against the frozen F1 contract, change source while queued, run with local resources and the fake UTM backend, and verify aggregate suite outcome/ID reporting. The real UTM run of these fixtures is part of TASK-027.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-018

### Implement environment locks and installation inventories

Requirement coverage: `HP-REQ-035`, `HP-REQ-036`, `HP-REQ-037`, `HP-REQ-038`.

#### Implement
- [ ] Validate and transport frozen consumer profiles and before/candidate/restored inventory evidence. Add fixture installers for complete skill trees, independent binary/skill refs and declared home scopes.

#### Do not
- [ ] Do not resolve latest at execution, invent a global compatibility registry, vendor real third-party skills, or assume all skills share a binary tag.

#### Verify
- [ ] Test skill-only candidate, obsolete/extra/missing files, duplicate discovery roots, changed effective settings and baseline revision changes.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-019

### Implement manual jobs and submitted installed-skill evidence

Requirement coverage: `HP-REQ-039`, `HP-REQ-040`, `HP-REQ-041`.

#### Implement
- [ ] Add awaiting_manual, revision-bound human results, exact retry semantics and long-lived resource ownership. Provide installed-session discovery/behavior scenario templates and use-haepari behavior cases.

#### Do not
- [ ] Do not mark environment-ready as PASS, synthesize observations, add a default LLM evaluator, or authorize providers from tool availability.

#### Verify
- [ ] Test unobserved checks, conflicting completion, caller exit, abandonment recovery, fresh-session identity and required manual-result omissions with local manual sessions and fake-backend guest fixtures. Real guest installed-skill and manual evidence is part of TASK-027.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-020

### Add migration and recovery recipe examples; freeze the consumer integration contract

Requirement coverage: `HP-REQ-042`, `HP-REQ-043`, `HP-REQ-044`, `HP-REQ-062` (freeze gate F3).

#### Implement
- [ ] Create an old-version synthetic product with data/config/skills and an upgrade candidate. Exercise old-state creation, in-place upgrade, preserved invariants, post-upgrade writes, restart, interruption and supported busy-work policy.
- [ ] Freeze gate F3: freeze the project Make and guest entrypoint conventions, `haepari.environment-lock/v1`, installation inventories, the manual result format and migration evidence fields, recording each change since TASK-002 and its evidence.

#### Do not
- [ ] Do not reset between old-state creation and upgrade assertions or present VM restore as product downgrade.

#### Verify
- [ ] Run a genuine old-state migration of the synthetic product through the VM job contract on the fake backend, include failed/interrupted upgrade and a first-release not-applicable case, and validate source/target evidence. The real UTM migration run is part of TASK-027.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## Epic acceptance

- [ ] TASK-017 through TASK-020 meet their individual acceptance criteria.
- [ ] The epic goal is demonstrated through a bounded integrated scenario, including a relevant failure and recovery path.
- [ ] Every changed public contract is documented and no downstream task relies on an unstated capability.
- [ ] An explicit epic acceptance updates only the canonical roadmap; task checkboxes alone do not complete the epic.
