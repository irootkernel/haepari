# ADR-0016: Managed-first v0.1.0 scope

Status: Accepted for Design Revision 3; implementation pending.

## Context

R2 added public external leases and linked recovery segments under ADR-0014/0015. B1 can strand capacity after intent settlement and owner loss; B2 can dead-end cancelled queued continuations or admit duplicate roots. These safety questions are unresolved. The first release must provide credible managed local and UTM execution without making those external APIs a prerequisite.

## Decision

v0.1.0 supports managed local and UTM macOS Jobs only. Internal managed ownership, generations, fencing, capacity accounting, restoration and quarantine remain mandatory. EPIC-008/TASK-029–032 and HP-REQ-063–079/E01–E20 are deferred. Linux runtime qualification and actual upgrade from a nonexistent public Haepari predecessor are outside the release gate. Ordinary Mulgae reviews do not require Haepari admission; Mulgae owns its provider/account investigation and native partial recovery.

## Alternatives considered

**Keep external leases in v0.1.0, or deliver them first.** Rejected.

- B1 (stranded capacity) and B2 (continuation dead end and duplicate roots) are unresolved.
- The purpose of ordinary-review capacity was avoiding provider rate limits. A review-segment lease cannot enforce a provider's account concurrency limit, and it would serialize reviews that use other providers.
- That work belongs in Mulgae; see [external requests](../to-external.md) §2.1.

**A thin lock-and-script VM runner first, with a pilot deciding whether to build the daemon.** Not adopted as a replacement.

- The owner kept the durable managed scheduler direction. The managed VM path needs durable admission, reconnectable waits, fenced cleanup, quarantine and restart reconciliation (HP-REQ-007 through HP-REQ-021). A lock-and-script runner would have to reimplement these without the shared ledger.
- VM value metrics and a go/no-go signal are instead requested from the Aquarium pilot ([external requests](../to-external.md) §1.4).
- TASK-034 probes UTM feasibility early, before the VM tasks depend on it.

**A different VM backend (tart), or a beta UTM snapshot/disposable mode.** Not selected for v0.1.0.

- ADR-0006 keeps UTM, and `reset_strategy` keeps the backend boundary replaceable.
- Revisit after the TASK-034 and TASK-016 evidence.

## Amendment (Design Revision 3.2)

[ADR-0017](0017-advisory-named-locks.md) supersedes two parts of the Decision above: the sentence "Ordinary Mulgae reviews do not require Haepari admission", and the "managed local and UTM macOS Jobs only" scope, which now also includes advisory named locks.

- **What changes.** Ordinary Mulgae reviews queue on Haepari's advisory lock `mulgae-review` and run when granted.
- **What stays.** The R2 external lease, the linked segments, EPIC-008 and E01–E20 remain deferred, under the re-entry conditions below.

## Consequences and re-entry

ADR-0014/0015 remain design history rather than being erased or represented as implemented. External work may re-enter post-v0.1.0 only after B1/B2 have complete transition, uniqueness, fencing and recovery evidence as stated in the deferred contract, plus a separate accepted consumer need. No version or date is promised. UTM implementation and Aquarium managed adoption do not depend on EPIC-008.
