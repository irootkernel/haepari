# ADR-0004: Freeze input and deduplicate admission before execution

Status: accepted for v0.1.0 design; implementation pending.

## Context

Source trees change while jobs wait. Transport replies can be lost even after a request was accepted.

## Decision

Seal candidate and test bytes before durable admission. Require a request key bound to the canonical submitted request: the validated typed structure with alias names as submitted, plus the sealed payload digest. Record the resolved resource and profile identities with the admission, outside the deduplication digest. Retain admission tombstones after ordinary purge.

## Alternatives considered

Re-reading a source path at dispatch loses candidate identity. Time-limited duplicate suppression can re-execute an old uncertain request.

## Consequences and revisit condition

Snapshots consume storage and need reference-aware collection. Same-key retries recover admission, not arbitrary external effects. An explicit new run uses a new key.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).

## Amendment (Design Revision 3.1)

This amendment supersedes the original wording "request key bound to the validated spec, sealed payload and resolved resource/profile identities".

**Why.** Binding the key to resolved identities made an identical replay conflict after a resource update or an alias move, although nothing the caller submitted had changed.

**What stays the same.**
- Resolved identities are still recorded at admission and still govern execution.
- A replay returns the original admission with those identities.

See the [CLI contract](../contracts/cli-and-jobs.md) and V54.

## Deferred external extension (ADR-0016)

ADR-0014/0015 preserve the R2 external design, whose application is deferred by [ADR-0016](0016-managed-first-scope.md). Managed ownership and cleanup remain active.
