# ADR-0015: Logical recovery spans distinct capacity leases

Status: R2 design decision retained; application deferred by ADR-0016. B1/B2 re-entry blockers remain unresolved.

## Context

A six-role review can retain five accepted results while a failed role needs recovery. It may recover immediately or stop for a user decision. Retaining capacity throughout an idle conversation blocks unrelated reviews; discarding the logical review repeats accepted work.

## Decision

Keep logical review state in Mulgae and its caller. Authorized immediate native retries use the active lease. Once native execution and cleanup end, release before user waiting. A later authorized segment uses a new request key and lease, the same opaque operation reference and an exact released-parent link. It enters normal FIFO order. Protect one live segment and one successor admission atomically; do not reactivate returned leases or recursively acquire within a held segment.

Mulgae owns exact target/attempt lineage, retention and final compose. Haepari stores correlation only and cannot infer review PASS from return success.

## Alternatives

A single lease for the entire logical review wastes capacity during user delays. A queue per role expands Haepari into a review scheduler. A new root review repeats successful roles. Reusing the original request key conflicts with durable deduplication and could reopen a released lease.

## Consequences

The caller must keep a minimal durable recovery/dispatch checkpoint and preserve native sources. Native commands without idempotency support remain unsafe to repeat after uncertain execution. Exact recovery refers to the original target, not an edited current checkout. Immediate retries remain bounded by existing native/user authority and the segment budget, not a new Haepari retry policy.

## Validation and reconsideration

Prove five-of-six recovery, return-before-user-wait, unrelated progress, new-lease continuation, duplicate resume rejection and target/lineage mismatch. Reconsider multi-resource or role-level scheduling only for a separately demonstrated requirement.

See [Mulgae integration](../integrations/mulgae-review.md).
