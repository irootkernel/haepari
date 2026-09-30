# ADR-0017: Advisory named locks for cooperating tools

Status: Accepted for v0.1.0 design (Design Revision 3.2); implemented by TASK-035 with bounded local verification.

## Context

The owner wants each ordinary Mulgae review to queue in Haepari and run only when its turn comes.

**The problem it solves.** One review with six or seven concurrent roles works. Two reviews at once on the same provider account sometimes fail.

**Why the existing design cannot be used.** The R2 external lease (ADR-0014 and ADR-0015) would provide this queuing. ADR-0016 deferred it for two reasons:

- It carries safety blockers B1 and B2.
- Its protocol is heavy: activation, execution intents, settlement and continuation.

None of those guarantees are needed just to stop two reviews from running at once.

## Decision

Add advisory named locks, specified in the [lock contract](../contracts/locks.md).

**What a lock provides:**
- `kind: lock` resources
- FIFO queues and a blocking wait
- explicit release
- operator force release

**How a grant stays alive:**
- A grant lives while its holder renews it within a liveness window, which defaults to 120 seconds.
- The maximum hold bounds the grant even with renewals. Its default of six hours covers current Mulgae configurations.
- Mulgae's run deadline grows when roles exceed lanes. With 60-minute role timeouts it is about 7,207 s when lanes ≥ roles, about 14,409 s for seven roles on six lanes, and about 21,611 s for seven roles on three lanes. The lock's maximum hold is set from the run deadline Mulgae's preflight reports.
- If the holder stops renewing, the grant expires and the next waiter is granted in the same transaction.

**What a lock does not do:**
- grant an execution permit
- protect state
- count toward managed resource occupancy or the global limit
- serve callers inside a managed job; they cannot acquire locks

**First use.** The first lock is `mulgae-review`, with capacity one. Mulgae acquires it once, at the review start that its MCP server and CLI share. It renews the grant from the running review and releases it on every path.

## Alternatives considered

**Restore the R2 external lease.** Kept deferred. Its guarantees target non-idempotent native execution and linked recovery, not turn-taking.

**Use the hold deadline alone as the timeout.** Rejected. It must cover a run of more than two hours, so a crashed session would block the queue for that long. A short liveness window lets waiters proceed within minutes.

**A connection-bound lock that releases when the holder's connection drops.** Rejected. The holder is now Mulgae's review process, which could keep a connection open. But a connection-bound grant would end on any transient disconnect or controller restart and release the lock under a review that is still running. Liveness renewal survives both, because startup and wake give live grants a fresh window.

**Acquire in `use-mulgae` or in each agent.** Rejected. The MCP and CLI paths share one review start, so acquiring in more than one layer would make a review wait behind its own grant. Acquisition therefore sits in Mulgae's shared review start, and nested tools reuse the grant.

**Per-provider or per-account locks.** Not adopted for now.
- The owner wants the review to be the queued unit.
- One lock also serializes reviews whose providers have no rate problem; the owner accepted that for simplicity.
- Per-provider locks can be added later as more locks.

**Mulgae-internal account slots only.** Kept as an optional Mulgae safeguard for calls that skip the lock. The owner chose a Haepari queue so that reviews wait visibly in the same controller as VM jobs.

## Consequences and revisit condition

- **A holder that stops** releases its turn one liveness window later. An operator can release it sooner.
- **Overlap.** Work that outlives the maximum hold can overlap the next holder, and so can work whose renewals stop while it keeps running. The worst case is the same as having no lock.
- **Direct calls that skip the lock** are not coordinated.
- **B1 and B2 do not apply.** Grants expire, and there is no continuation.
- **The no-takeover principle.** It still governs managed work. Advisory grants are the only exception, because they protect no Haepari-owned work.
- **ADR-0016 is amended.** Its statement that ordinary Mulgae reviews need no Haepari admission, and its "managed Jobs only" scope, no longer hold; the full external lease stays deferred.

**Revisit condition.** Revisit this decision if a consumer needs execution-level guarantees rather than turn-taking.
