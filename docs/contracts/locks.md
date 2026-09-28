# Advisory lock contract

Design Revision 3.2. This contract owns `kind: lock` resources and the `haepari lock` command family. [ADR-0017](../architecture-decision-records/0017-advisory-named-locks.md) records the decision.

## Purpose and limits

**What a lock does.** Cooperating tools queue for a named lock, wait until it is their turn, run their own work, then release the lock. Ordinary Mulgae reviews use the `mulgae-review` lock with capacity one.

**What a lock does not do.** A lock is advisory:

- It coordinates the callers that use it.
- It protects no Haepari-owned state and grants no execution permit.
- Haepari never runs, observes or interprets the holder's work.
- A caller that skips the lock is outside its guarantee.

**How it differs from the deferred external lease** ([ADR-0014](../architecture-decision-records/0014-external-execution-leases.md) and [ADR-0015](../architecture-decision-records/0015-linked-recovery-segments.md)):

- A lock has no activation, execution intent, native-reference binding, settlement or continuation.
- A grant ends when its holder stops renewing it, or at its maximum hold.
- A lock never counts toward managed resource occupancy or the controller-wide `global_limit`.

**Why expiry is allowed here.** Managed work is never taken over on a timer. Advisory grants are the one exception, because they protect no Haepari-owned work.

**The deferred lease's blockers do not apply.** A grant whose holder vanished expires after its liveness window, so no capacity is stranded for long (B1). There are no continuation links, so none can dead-end (B2).

## Lock resource

A lock is registered like any resource, with `haepari.resource/v1` and `kind: lock`.

| Field | Meaning | Default |
| --- | --- | --- |
| `id` | Lock name, such as `mulgae-review` | — |
| `capacity` | Grants that may be held at once | 1 |
| `liveness_seconds` | A grant must be renewed within this window, or it ends | 120 |
| `max_hold_seconds` | Upper bound from grant time, including every renewal. Set it at or above the holder tool's maximum run time. | 21600 |
| `default_queue_seconds` | Queue deadline when `acquire` omits `--queue-seconds` | 7200 |
| `revision` | Resource revision for updates | 1 |

A lock resource has no adapter, baseline or execution mode. See the [lock resource example](../examples/resource-lock.template.json).

**Availability.** A lock uses only `available` and `paused`.

**Resource commands:**

| Command | Effect on a lock |
| --- | --- |
| `resource pause` | Stops new grants. Existing grants continue until they end. |
| `resource resume` | Re-enables grants. |
| `resource recover` | Returns `UNSUPPORTED`. |
| `resource remove` | Requires no queued or granted request. |
| `resource update` | Applies to grants made afterwards. Lowering capacity never revokes a current grant. |

## Commands

| Command | Contract |
| --- | --- |
| `haepari lock acquire <lock> --request-key <key> --holder <label> [--queue-seconds <n>] [--wait] [--timeout <duration>] --json` | Atomically queue the request, or grant it when a slot is free and nobody is waiting ahead. See the replay and exit rules below. |
| `haepari lock wait <request-id> [--timeout <duration>] --json` | Block until the request is granted or ends. Like `acquire --wait`, it renews the grant it returns, so the caller gets a full liveness window to start renewing. |
| `haepari lock renew <grant-id> --json` | Move the liveness deadline to `now + liveness_seconds`, never past grant time plus `max_hold_seconds`. After the grant ends it fails with `LOCK_EXPIRED`. |
| `haepari lock keepalive <grant-id>` | Renew every `liveness_seconds / 3` until the grant ends, then exit. Stopping the process stops the renewals. |
| `haepari lock release <grant-id> --json` | Release the grant. An exact replay of the caller's own release returns the original receipt and exits 0. Releasing a grant that ended some other way returns its terminal receipt and never touches a later grant. |
| `haepari lock cancel <request-id> [--force --reason <text>] --json` | Remove a queued request. If the request is granted, release the grant in the same transaction. `--force` records an operator action and its reason. |
| `haepari locks list [--lock <id>] --json` | Show each request ID, grant ID, holder label and state, with waiters in FIFO order and every deadline. |
| `haepari lock release <grant-id> --force --reason <text> --json` | Operator release. The receipt records the reason. |

**Replay.** Resending `acquire` with the same key and payload returns the stored receipt together with the request's current state. It never creates a second request. A request that has ended cannot be revived; a new acquisition needs a new key.

**Request keys.** Lock request keys are scoped to the lock within one installation. They are separate from job request keys. Callers persist the key and the full payload before sending, so a replay sends identical content. The holder label is part of the payload.

**Grants and identity.** Every grant starts a new generation of its slot. The grant ID is a random identifier. `locks list` shows it, because same-user code is outside the security boundary anyway. The holder label is a non-secret description, such as the tool and run.

**Nested callers.** A caller whose parent context occupies managed capacity cannot acquire a lock; it gets `NESTED_ACQUISITION_REJECTED`. The [nested acquisition rule](managed-transitions.md#nested-acquisition) covers locks.

## Transitions

Every lock mutation first applies the lock's due deadlines, in the same transaction. After a restart or wake, deadlines are applied before any new grant.

| Previous state | Event | Guard | Next state | Durable record |
| --- | --- | --- | --- | --- |
| absent | acquire | Valid lock and payload; new key; caller not nested | granted if the lock is `available`, a slot is free and no earlier request waits; otherwise queued, also while the lock is `paused` | Request with payload digest, FIFO sequence and queue deadline; on grant, the grant ID, generation, liveness deadline and maximum-hold deadline |
| any existing | acquire replay | Same key and payload | unchanged | Stored receipt plus the current state |
| any existing | acquire with a reused key | Same key, different payload | unchanged | `CONFLICT` |
| queued | a slot frees, or the lock is resumed | Request is the FIFO head and the lock is `available` | granted | Grant fields as above. Recorded in the transaction that freed the slot or resumed the lock. |
| queued | cancel | — | cancelled | Cancellation receipt, forced or not |
| queued | queue deadline passes | Persisted UTC deadline | queue_expired | Expiry receipt |
| granted | renew | Before the liveness and maximum-hold deadlines | granted | New liveness deadline, capped at the maximum hold |
| granted | release, or cancel of the request | Grant ID matches the current generation | released | Release receipt. The next waiter is granted in the same transaction. |
| granted | force release | Operator action with a reason | released | Forced receipt with the reason. The next waiter is granted in the same transaction. |
| granted | liveness or maximum-hold deadline passes | Persisted UTC deadlines | hold_expired | Expiry receipt with the deadline that passed. The next waiter is granted in the same transaction. |
| released, cancelled, queue_expired or hold_expired | release or renew | Stale grant or request | unchanged | Release returns the terminal receipt; renew returns `LOCK_EXPIRED` |

**Unclaimed grants.** A waiter can disappear before it is granted, for example when a tool call's time limit ends a `--wait`. Such a grant is never renewed. It ends as `hold_expired` after one liveness window, and the next waiter is granted. `acquire --wait`, `lock wait` and an acquire replay each renew the grant they return, which counts as claiming it.

**Durability.** Requests, grants, FIFO order and every deadline live in the controller's SQLite store and survive restarts. Deadlines are persisted UTC instants under the [managed clock rules](managed-transitions.md#deadlines-and-restart). A backward clock jump cannot extend a deadline.

**Startup and wake.** Holders cannot renew while the controller is stopped or the host sleeps. So on startup and on wake, before applying deadlines, the controller moves each live grant's liveness deadline to at least `now + liveness_seconds`, never past its maximum hold, and records the discontinuity. A holder that is still running then renews normally. A holder that died ends one window later. A restart or wake therefore never releases a grant that is still being renewed.

## Exits and errors

| Result | CLI exit |
| --- | --- |
| The command succeeded. The request is queued (no `--wait`) or granted, a release, renew or cancel took effect, or the caller's own release was replayed | 0 |
| Invalid input, `CONFLICT`, `NESTED_ACQUISITION_REJECTED`, `NOT_FOUND`, `LOCK_EXPIRED`, `UNSUPPORTED` | 2 |
| The request has ended without a current grant: cancelled, queue_expired, hold_expired or released. This includes a replay of an ended request. | 4 |
| The caller's wait timeout passed; the request stays queued | 5 |
| Transport uncertain or controller unavailable; resend the same key | 6 |

With `--wait`, `acquire` exits 0 only while the request is granted.

The capability identifier is `advisory-lock/v1`.

## Typical use

**Mulgae (recommended integration).** MCP and the CLI start reviews through one shared path in Mulgae. Mulgae acquires `mulgae-review` exactly once, at that shared start:

1. Persist the key and payload.
2. Acquire and wait for the grant. The queue wait happens inside the review run, so the agent's own tool calls are not held open.
3. Renew the grant from inside the running review process.
4. Release on every path: success, failure and cancellation.

**Nested tools.** A tool that already holds the grant passes it to nested tools through `HAEPARI_LOCK_GRANT`. A tool that sees a live grant for the same lock does not acquire again. `use-mulgae` does not acquire a separate lock.

**A caller without an in-process holder.**

1. Run `acquire --wait`.
2. If a tool-call time limit ends the wait, resend the same `acquire` with the same key and payload. This works even if the request ID was never printed. `lock wait <request-id>` also works when the ID is known.
3. Once granted, keep `haepari lock keepalive <grant-id>` running in the background while the work runs.
4. Release at the end.

**Limits of the lock:**

- **Stopped holder.** If the holder process stops, renewals stop and the grant ends after one liveness window.
- **Overlap.** Two holders can briefly overlap in two cases:
  - work outlives `max_hold_seconds`;
  - a holder's renewals stop while its work keeps running, for example when its renewal thread hangs.

  The lock is advisory, so the worst case is the same as having no lock.
