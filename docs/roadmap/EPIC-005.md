# EPIC-005: Generic project and manual test integration

Provide a small project-owned automatic/manual test loop and freeze the consumer integration contract (F3). Consumer-specific skill and migration qualification is optional under [ADR-0020](../architecture-decision-records/0020-local-development-scope.md).

Identity, status and ordering belong to the [canonical roadmap](../roadmap.md). These checklists define implementation acceptance only.

Consume the accepted EPIC-009 core VM and isolated caller outcomes without another campaign. Product implementation remains in these planned tasks; no new runtime capability is asserted by the scope revision.

## TASK-017

### Publish the project Make and guest integration contract

Requirement coverage: `HP-REQ-032`, `HP-REQ-033`, `HP-REQ-034`.

Implementation and observation boundary: [TASK-017 record](../records/TASK-017-project-integration.md).

#### Implement
- [x] Provide one small synthetic project’s host `make test-vm` wrapper, distinct guest entrypoint, sealed selected-file bundle and saved-key submit/wait/result reporting. Default the sample to regression; auto/manual is independent of suite. Optional all dispatch uses only consumer-declared suites.

#### Do not
- [x] Do not install candidates on the host, duplicate UTM control, require install/migration suites, create a mandatory integration repository or enqueue from the guest.

#### Verify
- [x] Exercise the sample with local resources and the fake/native guest fixture: success, meaningful failure, same-key replay, changed queued source, truthful aggregate results and host refusal of the guest entrypoint. Implement the lightweight running-VM path for TASK-027 with read-only running/session/SSH preflight and actionable prerequisite failures, without power controls or automatic environment repair; no live campaign is required to complete this task.
- [x] Run focused unit/integration checks for changed behavior and relevant negative cases; record commands and results. Missing required observations are not passes.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-018

### Implement generic candidate and environment input helpers

Requirement coverage: `HP-REQ-035`, `HP-REQ-036`.

#### Implement
- [x] Reuse selected-file digest/spec helpers. Pin candidate/test bytes and consumer profile bytes into the existing Job profile_id/profile_digest and provenance fields. Treat files as opaque payloads; provide a minimal runnable profile example without mandatory component, skill or installation inventories.

#### Do not
- [x] Do not add a global compatibility registry, inspect host installation trees, resolve latest at dispatch, require product-specific versions/discovery, or change F1/F2 schemas.

#### Verify
- [x] Verify reproducible profile/payload digests, skill-only opaque input, executable modes, queued source changes, missing/corrupt selected files and original-admission replay after resource updates using local/fake fixtures.
- [x] Run focused unit/integration checks for changed behavior and relevant negative cases; record commands and results. Missing required observations are not passes.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-019

### Implement general manual jobs and recorded observations

Requirement coverage: `HP-REQ-040`, `HP-REQ-041`.

#### Implement
- [x] Connect existing manual schema and capacity accounting to observed setup, awaiting_manual, revision-bound human completion, exact retry, collection/cleanup, cancellation and abandonment. Required scenarios and evidence references are consumer-declared; Haepari records pass/fail/unobserved without interpreting product behavior.

#### Do not
- [x] Do not mark ready as PASS, synthesize observations, require installed-skill/session discovery or invoke model providers by default.

#### Verify
- [x] Use small local manual sessions and fake/native guest fixtures to verify exact/conflicting completion, unobserved required checks, caller exit, timeout/cancel, cleanup and successor reuse. TASK-027 observes one ordinary manual scenario on the running VM without a provider.
- [x] Run focused unit/integration checks for changed behavior and relevant negative cases; record commands and results. Missing required observations are not passes.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-020

### Validate the generic consumer loop; freeze the integration contract

Requirement coverage: `HP-REQ-062` (freeze gate F3).

#### Implement
- [ ] Combine the small automatic/manual project flow and freeze F3: host wrapper, separate guest entrypoint, immutable consumer environment reference and general manual-result format. Record the accepted surface and evidence. Rich installation locks, skill inventories and migration schemas remain optional consumer design, outside F3.

#### Do not
- [ ] Do not make prior-product installation, migration interruption, installed-skill behavior or external-project adoption prerequisites of this freeze.

#### Verify
- [ ] Run the small combined local/fake-guest flow through success, meaningful failure, manual completion, saved-key recovery and next-job readiness. TASK-027 checks its running-VM release path; do not repeat accepted TASK-038 qualification.
- [ ] Run focused unit/integration checks for changed behavior and relevant negative cases; record commands and results. Missing required observations are not passes.

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
