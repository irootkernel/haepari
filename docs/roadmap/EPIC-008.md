# EPIC-008 (deferred): External leases and linked recovery segments

Implement caller-executed capacity grants using the existing durable scheduler. Keep native execution, role recovery and composition with the consumer.

Identity, status and execution order belong to the [canonical roadmap](../roadmap.md). This epic is Deferred by ADR-0016 and has no execution order in v0.1.0; re-entry requires B1/B2 closure and a recorded decision. The [deferred external lease contract](../contracts/external-leases.md) holds the R2 semantics. All acceptance below is retained design history.

## TASK-029

### Implement external admission, activation and dispatch claims

Requirement coverage: `HP-REQ-063`, `HP-REQ-064`, `HP-REQ-065`.

#### Implement
- [ ] Implement the external schema, owner binding, atomic queued/granted admission, shared occupancy and FIFO, durable key replay and bounded lease reads/waits/events.
- [ ] Bind a reserved grant to one executor and require a durable first-claim-only execution permit. Include configuration identity and family conflicts in request deduplication.
- [ ] Reuse the common scheduler and slot ledger; do not allocate a second controller or queue database.

#### Do not
- [ ] Do not send code/Brief bodies to Haepari, equate accepted/granted with native start, or use try-lock then a separate enqueue.
- [ ] Do not issue a fresh execute permit from a replay or transfer ownership on connection loss.

#### Verify
- [ ] Race mixed managed/external capacity, same/different request keys, different owners, duplicate activation and duplicate begins.
- [ ] Lose replies at admission, grant, activation and begin. Assert stable references, reserved capacity, reconciliation-only replay and no duplicate native start.
- [ ] Prove grant/release blocking waits reconnect without status polling, and cancellation never appears as grant.

#### Done when
- [ ] The contract and focused negative-path tests agree; evidence binds the exact candidate and protocol.
- [ ] No fixture result is reported as live Mulgae acceptance or host deployment.
- [ ] The applicable requirements and external matrix rows have reviewed observations, and the canonical roadmap alone receives lifecycle updates.
- [ ] Documentation, CLI help and examples match. Native product state, credentials and unrelated work remain unchanged.

## TASK-030

### Implement native execution binding, release and reconciliation

Requirement coverage: `HP-REQ-066`, `HP-REQ-067`, `HP-REQ-068`, `HP-REQ-073`, `HP-REQ-075`.

#### Implement
- [ ] Implement opaque scoped native IDs, single open execution intent, owner-attested terminal/no-start settlement, liveness and cancellation/reconciliation.
- [ ] Close further begins before atomic return. Persist idempotent release/mutation receipts, stale generation rejection and minimal durable tombstones.
- [ ] On controller or owner loss preserve uncertain occupancy. Implement explicit current-fenced external recovery without invoking native providers or VM reset.

#### Do not
- [ ] Do not infer termination from a timeout, caller exit, native start acknowledgement, missing run ID, heartbeat expiry or a successful cancellation request.
- [ ] Do not copy ownership material to evidence, treat lease return as review PASS, purge native results or provide an unsafe unlock shortcut.

#### Verify
- [ ] Inject owner death, daemon restart, unknown native start, cancelled observers and concurrent begin/cancel/release.
- [ ] Verify explicit never-started cancellation cannot race a successful execution intent; an unknown active intent retains both slots.
- [ ] Replay old release after a successor is active, test terminal tombstones and missing owner material, and inspect logs/exports for secrets.

#### Done when
- [ ] The contract and focused negative-path tests agree; evidence binds the exact candidate and protocol.
- [ ] No fixture result is reported as live Mulgae acceptance or host deployment.
- [ ] The applicable requirements and external matrix rows have reviewed observations, and the canonical roadmap alone receives lifecycle updates.
- [ ] Documentation, CLI help and examples match. Native product state, credentials and unrelated work remain unchanged.

## TASK-031

### Implement linked segments and single-owner continuation

Requirement coverage: `HP-REQ-069`, `HP-REQ-070`, `HP-REQ-071`, `HP-REQ-072`.

#### Implement
- [ ] Add opaque operation reference and exact released-parent continuation with one live segment and one successor constraint.
- [ ] Support sequential authorized native calls under one lease; maintain fresh execution keys and prohibit recursive acquisition for the same operation.
- [ ] Provide generic client checkpoint/continuation examples: terminal idle work returns before user waiting; later authorized work uses a new key and lease with FIFO.

#### Do not
- [ ] Do not introduce role scheduling, a logical-review status engine, native retry budgets, automatic provider changes or a slot placeholder during user waiting.
- [ ] Do not reuse a released lease/key to resume work, inherit queue priority or silently create a second continuation branch.

#### Verify
- [ ] Use six synthetic role outputs: five accepted, one failed, same-lease immediate recovery, terminal failure, release, unrelated job progress and new-lease one-role recovery.
- [ ] Race two continuation submissions with same/different keys and prove one parent successor and one native dispatch.
- [ ] Show incomplete native review with successful release, no capacity use while waiting, and old-target preservation across a later caller decision.

#### Done when
- [ ] The contract and focused negative-path tests agree; evidence binds the exact candidate and protocol.
- [ ] No fixture result is reported as live Mulgae acceptance or host deployment.
- [ ] The applicable requirements and external matrix rows have reviewed observations, and the canonical roadmap alone receives lifecycle updates.
- [ ] Documentation, CLI help and examples match. Native product state, credentials and unrelated work remain unchanged.

## TASK-032

### Qualify external-client recovery and consumer contracts

Requirement coverage: `HP-REQ-074`, `HP-REQ-076`, `HP-REQ-078`, `HP-REQ-079`.

#### Implement
- [ ] Build an independent fixture client with deterministic per-role results, native-like non-idempotent starts and composite validation. Use no real Mulgae dependency.
- [ ] Run all external verification rows with managed resources on the same controller, and publish the checked consumer protocol and scoped advisory limitation.
- [ ] Publish the Mulgae ownership/request/return contract for later Aquarium:TASK-092; require isolated controller/test resources for VM consumer tests.

#### Do not
- [ ] Do not change use-mulgae in the Haepari repository, invoke live providers by default, fabricate native recovery data or make Aquarium adoption a Haepari release prerequisite.
- [ ] Do not conflate simulator acceptance with installed native skill behavior or create a nested parent-resource dependency.

#### Verify
- [ ] Run the full finite external matrix, including lost native mutation response, optional role coverage, corrupt/missing source, changed target, duplicate resume and stale release.
- [ ] Demonstrate independent control-plane recovery without Aquarium/Mulgae, and prove VM fixtures do not acquire the already held parent/global slot.
- [ ] Complete exact fixture receipts for the post-v0.1.0 release acceptance that re-admits this epic.

#### Done when
- [ ] The contract and focused negative-path tests agree; evidence binds the exact candidate and protocol.
- [ ] No fixture result is reported as live Mulgae acceptance or host deployment.
- [ ] The applicable requirements and external matrix rows have reviewed observations, and the canonical roadmap alone receives lifecycle updates.
- [ ] Documentation, CLI help and examples match. Native product state, credentials and unrelated work remain unchanged.

## Epic acceptance

- [ ] TASK-029 through TASK-032 meet their requirements and all external matrix cases have actual fixture assertions.
- [ ] One daemon enforces independent resources and the shared global occupancy limit for both execution families.
- [ ] Partial failure does not repeat accepted roles, hold an idle user-wait slot or create a second native start on replay.
- [ ] Ownership, outcome and advisory limitations are accurately documented; real Mulgae acceptance remains downstream.
