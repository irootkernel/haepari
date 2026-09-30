# EPIC-002: Durable queue, resource ownership and advisory locks

Admit immutable work, serialize shared resources, expose durable client operations and provide advisory named locks for cooperating tools.

Identity, status and ordering belong to the [canonical roadmap](../roadmap.md). These checklists define implementation acceptance only.

## TASK-005

### Implement durable storage and controller ownership

Requirement coverage: `HP-REQ-007`, `HP-REQ-008`.

#### Implement
- [x] Extend the TASK-033 production-path managed admission and mutation-receipt store; keep the slot ledger typed so later modes require an explicit schema decision.
- [x] Implement schema versioning, transactional jobs/events/leases/admissions, exclusive daemon lock, private Unix socket lifecycle and short database transactions. Apply the TASK-004 durability decision (darwin `fullfsync` and `checkpoint_fullfsync`) and the short socket-root rule.

#### Do not
- [x] Do not hold transactions around runner or adapter effects or repair a live competing controller by deleting its socket.

#### Verify
- [x] Inject crashes at transaction boundaries, race two controllers, validate schema-too-new rejection, and check file permissions and foreign keys.
- [x] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

### Implementation and verification

The existing SQLite managed ledger remains the only lifecycle owner. Schema version 3 preserves typed managed slots, request identity, mutation receipts and event cursor ownership; newer versions are refused. Lifecycle commits return only after SQLite commit. Runner and adapter work occurs outside ledger transactions. The controller acquires `controller.lock` with nonblocking kernel exclusion before opening its database or inspecting its socket. The private lock inode is retained across exits; the socket identity is synced into the locked file before publication. A refused socket is recovered only under exclusive ownership and that matching identity. Reachable, unknown legacy, foreign and replaced paths are preserved; unknown ownership requires manual recovery after confirming the prior controller is stopped.

Observed on 2026-09-30, macOS 27.0 build 26A428 arm64, Go 1.27.1 and SQLite module v1.59.0. Verification on this owner-operated host: `go run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration --fail-on-empty --fail-on-pending --label-filter='unit || integration' ./cmd/haepari ./internal/state ./internal/managed ./internal/controller ./internal/store/sqlite` passed 84 specs and exited 0. `go vet -tags=integration ./cmd/haepari ./internal/state ./internal/managed ./internal/controller ./internal/store/sqlite` exited 0; `make test-e2e` passed its real CLI case. The probes kill separate processes immediately before and after real admission, reservation, queued cancellation, phase transition, run-intent, terminal settlement and interruption-reconciliation commits, reopen the store, check atomic state and receipt recovery, and verify SQLite integrity/foreign keys. Ownership tests cover a live competitor, abrupt process death, reacquisition, private permissions and linked paths; controller tests cover simultaneous startup, refusal before effects, a live legacy listener with a full backlog, socket replacement and stale/live/foreign sockets; a real foreground daemon is killed and restarted through its recorded socket identity. Documentation validation `python3 docs/tools/validate_docs.py` exited 0. Candidate implementation and test input identities are listed below; generated documentation changes only regenerate their corresponding manifest entries. These checks do not qualify sudden power loss, installed-service recovery or real VM execution.

Implementation/test baseline: commit `2a3894f99f51436d3cd2eda843f4fc6c79d2f683`; all other production, test and runner inputs are unchanged from that baseline. The changed inputs for this observation are the following Git blob identities. This is source evidence, not product or release qualification.

| Input | Git blob |
| --- | --- |
| `cmd/haepari/main.go` | `7ca47fd66fd1d5b34ed4f66a0f612732d358e569` |
| `cmd/haepari/main_integration_test.go` | `d781a042004b2cc61a13d62ee8442e88e68409a4` |
| `internal/controller/run.go` | `b2f7db927ac6e21aad7aaa5c58e0c98dd0228f98` |
| `internal/controller/run_integration_test.go` | `1edaab1851d417726ac2a8e3df38651ca0a0455c` |
| `internal/managed/ledger.go` | `70d9e0eeeadf01b721f8ab64e95badf0868f94da` |
| `internal/managed/local.go` | `09b75952c49b671c1219b12ac0600afb7722eb77` |
| `internal/managed/durability_test.go` | `eaf3d15fc1aecf6867eed7ba940fd9a03c17c32e` |
| `internal/state/ownership.go` | `3c9fe91b9ef6c8fa343691bb9e955f57ae2c7577` |
| `internal/state/ownership_test.go` | `ae5d2a7eb3672f9d5f75f2dcb98a6a1e0f2ce8f4` |

## TASK-006

### Implement admission and durable deduplication

Requirement coverage: `HP-REQ-009`, `HP-REQ-010`.

#### Implement
- [x] Seal inputs before acceptance, commit request-key identity, recover lost replies, and keep tombstones after bulk purge. Deduplicate on the canonical submitted request; record the resolved environment and resource-configuration identities separately so that an identical replay after a resource update returns the original admission. A rejection never consumes a request key.

#### Do not
- [x] Do not acknowledge uncommitted jobs, expire keys into re-executable requests, or re-open mutable source paths during dispatch.

#### Verify
- [x] Race same-key submissions, lose replies before/after commit, submit conflicting payloads, purge then retry, and inject staging/disk/database failure.
- [x] Run focused unit/integration checks for changed behavior and the relevant negative cases. Record exact commands and outcomes; do not turn a missing prerequisite into a pass.

#### Done when
- [x] The listed behavior and failure paths are implemented and reviewed against the normative contracts.
- [x] Evidence binds the exact candidate and relevant environment; required real/manual checks are observed or the task remains incomplete.
- [x] Changed documentation and examples match the implementation. Unrelated source, state, credentials and Git work are preserved.
- [x] The task has a bounded handoff with remaining gaps stated; its commit occurs only under the applicable authority.

### Implementation and verification

Schema version 4 stores durable `managed_admissions` independently of bulk Job/input/run/event payloads. The same transaction commits the key, canonical submitted digest, global sequence, sealed input/object and inventory, original pinned resource revision, frozen environment profile identity, UTC admission time, Job and initial event. Replay rechecks the key inside that transaction and returns the original admission alongside a current read-only lifecycle snapshot; configuration updates do not change deduplication or original resolved identities. Invalid keys are rejected before sealing. Same-key recovery compares surviving selected files without recreating an object. Database failures remain storage uncertainty unless a UNIQUE or PRIMARY KEY constraint conflicts; trigger failures remain storage uncertainty.

An explicit terminal retention receipt returns the known admission with `result_status: unavailable`; it has no verdict, capacity or lease claims and cannot create another Job. `run` emits that admission and exits 2 without waiting; `wait` reports `RESULT_UNAVAILABLE` with exit 2, and inspection omits a fabricated result. Missing accepted rows or objects without an explicit retention receipt remain storage uncertainty. The internal retention metadata guard rejects non-terminal/quarantined work and an object-backed legacy admission without its inventory. A legacy inventory binds only from the verified original object, never from mutable source. Production bulk deletion, unresolved-evidence policy and retention authorization remain TASK-012; the synthetic deletion in these checks touches only temporary fixtures.

Observed on 2026-09-30, macOS 27.0 build 26A428 arm64, Go 1.27.1, SQLite module v1.59.0 and a non-root state owner. `go run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration --fail-on-empty --fail-on-pending --label-filter='unit || integration' ./cmd/haepari ./internal/managed ./internal/controller ./internal/snapshot ./internal/store/sqlite` passed 97 specs and exited 0. `go vet -tags=integration ./cmd/haepari ./internal/managed ./internal/controller ./internal/snapshot ./internal/store/sqlite` exited 0; `make test-e2e` passed one real CLI case; `python3 docs/tools/validate_docs.py` exited 0. These checks race 16 identical and 16 mixed/conflicting ledger admissions and eight Unix clients, discard a committed Unix reply, observe one actual workload execution, preserve identity across configuration update and v3 migration, reopen a synthetic purged tombstone, reject changed surviving input, and prove that staging permission failure, read-only SQLite and a rolled-back insertion consume no key. They classify missing admission rows (including a loss during legacy inventory binding), a missing schema-4 admission table, a missing unpurged sealed object and missing or corrupt retained inventory as storage uncertainty. A reopened synthetic v3 store replays through the real Unix controller and binds the original verified inventory; changed source still conflicts. The CLI fixture checks successful `submit --json` separately from `run` exit 2 and a single `wait --json` RESULT_UNAVAILABLE envelope with exit 2. The follow-up checks cover accepted 128-character and rejected 129-character keys, leading separators and non-ASCII, invalid and oversized inventory without key consumption, and the exact six-key tombstone envelope. Existing process-death probes now include the independent admission table before/after real commits. The unavailable-result CLI branch uses a bounded Unix response fixture; the controller tombstone path uses the actual ledger, Unix client and runtime. No common `make test`, production purge, installed service, real VM or sudden-power-loss qualification is claimed.

Implementation/test baseline: commit `ea1e18ffc4fb98288579ed5918545fb9766fdddd`; all other production, test and runner inputs are unchanged. Changed inputs for this observation:

| Input | Git blob |
| --- | --- |
| `cmd/haepari/main.go` | `eb872f0a6a898c7c9c361b25fc688e151663ba30` |
| `cmd/haepari/main_integration_test.go` | `d1b633d66f8e8e8687cb8d75ad90b1adf76993cc` |
| `internal/controller/server.go` | `9beeea226ddbfb12c8d63b0955258f97fb246f98` |
| `internal/controller/admission_integration_test.go` | `1f0bff4bd69c114ce2960f739d1f8223a82974ce` |
| `internal/managed/admission.go` | `30f5adeed00546f5e94060b3163bf5eaaca654fc` |
| `internal/managed/admission_integration_test.go` | `0367f3879cae61767269631cd79446056ce9020b` |
| `internal/managed/durability_test.go` | `90c82ab3b752972662de893e979e462280cd9abb` |
| `internal/managed/ledger.go` | `80a99483a85a74f88de020226ee810ccd9002af1` |
| `internal/managed/ledger_test.go` | `2d481e3c442ffd4510893aa55291c6f1ec713f42` |
| `internal/managed/local.go` | `4e3a702409b1472a70b169fa99013302f68c748e` |
| `internal/snapshot/snapshot.go` | `c3a4b46a20e55a279e81caf352ebc7d216d39a59` |

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
