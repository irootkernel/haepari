# EPIC-003: Local execution, results and recovery

Complete the ordinary command resource with safe termination, settlement, recovery and evidence, then freeze the managed core contract (gate F1).

Identity, status and ordering belong to the [canonical roadmap](../roadmap.md). These checklists define implementation acceptance only.

## TASK-009

### Implement the local per-job supervisor

Requirement coverage: `HP-REQ-016`, `HP-REQ-017`.

#### Implement
- [ ] Add the local transient `runner` entrypoint, durable run claims, spawn-intent/start/exit receipts, explicit argv/env, process groups, streaming files, bounded termination/escalation and pipe cleanup.

#### Do not
- [ ] Do not silently shell-expand arguments or assume CommandContext kills all descendants.

#### Verify
- [ ] Run forking and output-heavy fixtures, intentional pipe inheritance, supervisor death, boot/PID identity cases and ambiguous spawn recovery.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-010

### Implement phase settlement and safe resource return

Requirement coverage: `HP-REQ-018`, `HP-REQ-019`.

#### Implement
- [ ] Connect common prepare/run/collect/cleanup/verify transitions, independent phase budgets, verdict derivation, and quarantine on unproven reuse. TASK-019 owns manual completion/session details.
- [ ] Implement the transition-table rows for cancel during preparing or ready, prepare deadline, collection deadline with partial evidence, termination-proof failure while cancelling, and queued input corruption or lost prerequisite.
- [ ] Derive every final verdict in one common path from the authoritative decision table. Preserve a known workload failure when evidence or cleanup is incomplete; support safe `not_needed`, queued cancellation/rejection, manual omissions, deadlines and late cancellation without rewriting a terminal result.
- [ ] Settle a reserved proven no-effect Job through the same verdict path with `cleanup_outcome=not_needed`, one atomic result/lease/`(-1,-1)`/event commit and unchanged availability. For VM-local `vm_unavailable`, use the committed pause receipt and successor resource revision before returning capacity; keep occupancy through a crash between pause and settlement. Reject this shortcut while process or power effects are uncertain.

#### Do not
- [ ] Do not release on command exit or hide required cleanup/evidence failure behind a product PASS.

#### Verify
- [ ] Fail each phase after partial effects; assert lease retention, bounded cleanup and correct result/resource state for all outcomes. Use table-driven tests for workload outcome, required evidence, cleanup, manual eligibility and cancellation ordering.
- [ ] Assert reserved cancel is CANCELLED and proven no-effect preparation failure is INCOMPLETE, both with `not_needed` and one `(-1,-1)` return; queued cancel stays `(0,0)`. Replay or restart around terminal commit cannot duplicate a result or return. Pause/quarantine survive settlement and uncertain effects cannot take the no-effect row.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-011

### Implement crash reconciliation, explicit recovery and the development service

Requirement coverage: `HP-REQ-020`, `HP-REQ-021`.

#### Implement
- [ ] On daemon restart reattach proven runs and receipts or enter a known interrupted-execution path; block affected resources, classify uncertainty, and implement revision-bound recovery plans/tokens with machine-observed versus operator-attested provenance.
- [ ] Resume interrupted cleaning or settling from its durable intent. Refuse `resource recover` with `RECOVERY_BLOCKED` while a job still occupies the resource; job recovery comes first.
- [ ] Provide the generic restart/recovery path for durable adapter intents and protected evidence references. TASK-013/TASK-015 bind VM power intents to exact VM/resource/operation generations and observed state; an issued `utmctl` command is not cancelled by a stale revision, and uncertain victims stay protected. Recovery plans must list any protected evidence loss before authorized disposal.
- [ ] Install the development LaunchAgent decided in TASK-004 for isolated development instances only (`gui/<uid>`, `LimitLoadToSessionType=Aqua`, `AbandonProcessGroup=true`). No signing identity is required ([TASK-034 probe record](../records/TASK-034-utm-probe.md)). Prove that a runner in its own session survives controller exit and restart and is reattached. TASK-016 runs under this service.

#### Do not
- [ ] Do not apply managed-runner restart or VM cleanup assumptions to future external caller state. Preserve typed ownership for the later external recovery implementation.
- [ ] Do not replay uncertain workloads, trust a bare PID, or let an old plan reset a newer lease.

#### Verify
- [ ] Inject restart before/after spawn and exit receipt, conflicting controller observations, stale plans, and unidentifiable descendants.
- [ ] Restart the development service while a runner works; assert reattachment, or a known interrupted path with retained occupancy, and no rerun.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-012

### Implement artifact delivery and retention; freeze the managed core contract

Requirement coverage: `HP-REQ-022`, `HP-REQ-023`, `HP-REQ-062` (freeze gate F1).

#### Implement
- [ ] Collect declared logs/results with size/digest manifests, stream large files, expose exact offsets, export to explicit paths and purge finished bulk data with retained admissions.
- [ ] Protect recoverable required evidence after collection failure: persist owned paths, gaps and reason; prevent cleanup/purge/GC from deleting it until bounded re-collection or explicit disposal is recorded. Keep supplementary evidence separate from immutable results.
- [ ] Freeze gate F1: freeze the managed core contract. It covers job, resource, result, event and envelope schemas; lifecycle/outcome enums; error codes; CLI exit codes; canonicalization; cursor semantics; and the advisory lock contract from TASK-035. The manual result format freezes later at F3 (TASK-020). Record every change since TASK-002 and its evidence. Later changes need a recorded contract decision and a schema version change.

#### Do not
- [ ] Do not discard required uncollected evidence during cleanup, silently truncate required results, or delete active/foreign paths.

#### Verify
- [ ] Test missing outputs, partial copies, exhausted disk, checksum mismatch, retained VM inspection and purge/retry interactions. Include controller restart with protected guest evidence, authorized disposal and an unproven collector.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## Epic acceptance

- [ ] TASK-009 through TASK-012 meet their individual acceptance criteria.
- [ ] The epic goal is demonstrated through a bounded integrated scenario, including a relevant failure and recovery path.
- [ ] Every changed public contract is documented and no downstream task relies on an unstated capability.
- [ ] An explicit epic acceptance updates only the canonical roadmap; task checkboxes alone do not complete the epic.
