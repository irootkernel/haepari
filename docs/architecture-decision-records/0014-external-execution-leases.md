# ADR-0014: Managed execution and external execution leases

Status: R2 design decision retained; application deferred by ADR-0016. B1/B2 re-entry blockers remain unresolved.

## Context

UTM tests need Haepari to prepare and restore a shared environment. Ordinary Mulgae reviews already have native CLI/MCP execution and recovery. Wrapping both as Haepari subprocess Jobs would duplicate the review transport and incorrectly make a short start response look like completion.

## Decision

Use two explicit execution families under one admission sequence, scheduler and capacity ledger. Managed Jobs use Haepari runners and resource adapters. External requests reserve capacity for one cooperating client executor and expose request/wait/activate/begin/bind/settle/release. Every native begin has a first-claim-only execution permit; replay is reconciliation-only. UTM hands-on remains managed manual work. Haepari does not invoke or interpret external product work.

## Alternatives

A separate queue per family duplicates scheduling and global-limit semantics. A mandatory subprocess wrapper cannot preserve all native MCP lifetimes. A plain try-lock followed by enqueue creates a race and duplicate-admission risk. Lease expiry alone cannot prove external work stopped.

## Consequences

The core adds owner-bound external states and opaque execution receipts. External safety depends on truthful cooperating clients and explicit native reconciliation. Direct calls remain outside this advisory boundary. Unknown execution can retain capacity and require operator recovery. No exactly-once native-effects guarantee is made.

## Validation and reconsideration

The external failure matrix and independent fixture caller must pass before handoff. Reconsider enforcement only if native Mulgae admission later validates leases; do not claim that phase-one skill changes already enforce every direct call.

See [external lease contract](../contracts/external-leases.md).
