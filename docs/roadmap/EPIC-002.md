# EPIC-002: Durable queue, resource ownership and advisory locks

Admit immutable work, serialize shared resources, expose durable client operations and provide advisory named locks for cooperating tools.

Identity, status and ordering belong to the [canonical roadmap](../roadmap.md). These checklists define implementation acceptance only.

## TASK-005

### Implement durable storage and controller ownership

Requirement coverage: `HP-REQ-007`, `HP-REQ-008`.

#### Implement
- [ ] Extend the TASK-033 production-path managed admission and mutation-receipt store; keep the slot ledger typed so later modes require an explicit schema decision.
- [ ] Implement schema versioning, transactional jobs/events/leases/admissions, exclusive daemon lock, private Unix socket lifecycle and short database transactions. Apply the TASK-004 durability decision (darwin `fullfsync` and `checkpoint_fullfsync`) and the short socket-root rule.

#### Do not
- [ ] Do not hold transactions around runner or adapter effects or repair a live competing controller by deleting its socket.

#### Verify
- [ ] Inject crashes at transaction boundaries, race two controllers, validate schema-too-new rejection, and check file permissions and foreign keys.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-006

### Implement admission and durable deduplication

Requirement coverage: `HP-REQ-009`, `HP-REQ-010`.

#### Implement
- [ ] Seal inputs before acceptance, commit request-key identity, recover lost replies, and keep tombstones after bulk purge. Deduplicate on the canonical submitted request; record the resolved environment and resource-configuration identities separately so that an identical replay after a resource update returns the original admission. A rejection never consumes a request key.

#### Do not
- [ ] Do not acknowledge uncommitted jobs, expire keys into re-executable requests, or re-open mutable source paths during dispatch.

#### Verify
- [ ] Race same-key submissions, lose replies before/after commit, submit conflicting payloads, purge then retry, and inject staging/disk/database failure.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-007

### Implement resource scheduling and fenced leases

Requirement coverage: `HP-REQ-011`, `HP-REQ-012`, `HP-REQ-013`.

#### Implement
- [ ] Reserve resource and global capacity for managed preparation, running, manual, cleanup and uncertain work; do not implement public external grants.
- [ ] Implement FIFO per-resource heads, capacities, global active limit, revisions/generations, pause/resume and quarantine. Lease all effectful phases atomically before adapter execution.
- [ ] Accept reservation guards supplied by adapters, such as the host macOS guest limit, without adding priorities. Reject nested acquisition before admission and reject a nested caller's blocking wait on a non-terminal job, as specified in [nested acquisition](../contracts/managed-transitions.md#nested-acquisition).
- [ ] Reserve VM victim stop intent and target start claim with the Job/lease and exact resource revisions before effects. Serialize concurrent host starts in the existing ledger, block victim reservations/configuration/power conflicts until state reconciliation, and leave an unserviceable request queued with an actionable reason.

#### Do not
- [ ] Do not add DAGs, priorities, multi-resource deadlock logic or time-based automatic lease takeover.

#### Verify
- [ ] Prove exclusivity through cleanup/manual use, fairness under competing resources, no head-of-line blocking across unavailable resources, stale token rejection, and no double spending of a VM host place across concurrent starts or crash/restart.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-008

### Implement client waits, events and cancellation admission

Requirement coverage: `HP-REQ-014`, `HP-REQ-015`.

#### Implement
- [ ] Expose native submit/run, paged job/resource lists, inspect/wait/events/logs and cancellation commands over the local API. Return job IDs early and retain a durable event cursor for reconnection.
- [ ] Negotiate the protocol version between CLI and controller before any mutation; an unsupported version returns `PROTOCOL_UNSUPPORTED` without effects.

#### Do not
- [ ] Do not make CLI lifetime own job lifetime, poll status from the paired contract, or treat cancel acknowledgement as termination.

#### Verify
- [ ] Exercise wait deadline, dropped clients, cursor resynchronization, inspect of failed jobs, and cancel-before/after-finish races.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## TASK-035

### Implement advisory named locks

Requirement coverage: `HP-REQ-080`, `HP-REQ-081`.

#### Implement
- [ ] Implement `kind: lock` resources and the `haepari lock` family from the [lock contract](../contracts/locks.md):
  - atomic queue-or-grant, FIFO grants and a blocking wait on the existing event path
  - replay that reports the current state
  - liveness renewal and `keepalive`, bounded by the maximum hold
  - `queue_expired` and `hold_expired` states, with persisted UTC deadlines applied at the start of every mutation
  - release and cancellation that hand the slot to the next waiter atomically
  - operator force actions and `locks list` with request and grant IDs
  - the effect of pause, resume, remove and update on lock resources
- [ ] Keep locks out of managed and global capacity accounting, and reject acquisition from nested managed callers.
- [ ] Expose the stable installation identity in handshake/lock inspection for read-only inherited-grant validation. Distinguish the acquiring owner's renew/release/cancel from a nested borrower's check-only use; reject wrong-installation, wrong-lock and stale grants without reacquisition.

#### Do not
- [ ] Do not add execution permits, continuation, provider routing or any observation of the holder's work, and do not let a stale grant release a later one.

#### Verify
- [ ] Run V59–V64: FIFO exclusivity and atomic hand-off; replay, key conflicts and ended requests; liveness expiry, unclaimed grants and force actions; independence from managed capacity, nested rejection and resource commands; restart and sleep/wake; stale IDs and every exit code. Prove that child success, failure and cancellation never end the parent's grant, while the direct owner renews and releases it.
- [ ] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [ ] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [ ] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [ ] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [ ] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

## Epic acceptance

- [ ] TASK-005 through TASK-008 and TASK-035 meet their individual acceptance criteria.
- [ ] The epic goal is demonstrated through a bounded integrated scenario, including a relevant failure and recovery path.
- [ ] Every changed public contract is documented and no downstream task relies on an unstated capability.
- [ ] An explicit epic acceptance updates only the canonical roadmap; task checkboxes alone do not complete the epic.
