# ADR-0005: Resource reuse requires verified cleanup

Status: accepted for v0.1.0 design; implementation pending.

## Context

A failed install or disconnected shell can leave a VM running with modified services, skills or data.

## Decision

Keep managed occupancy through preparation, work, collection and cleanup until owned workload/collector termination is proven. Verified cleanup permits resource reuse. Proven termination with failed cleanup or protected guest evidence may settle the Job INCOMPLETE and return occupancy while quarantining the resource; unproven termination keeps occupancy and reconciliation. Separate workload outcome from evidence, cleanup and final verdict.

## Alternatives considered

Releasing on process exit, SSH disconnect or lease timeout permits cross-test contamination. Treating cleanup failure as a warning can create misleading passes.

## Consequences and revisit condition

Some failed jobs stop that resource until operator recovery. Other resources remain available. A recovery operation cannot rewrite a product result to PASS.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).

## Amendment (Design Revision 3.4)

The original "release only after verified reuse" wording conflated managed occupancy with resource availability. The decision above now follows the [evidence-preserving settlement](../contracts/managed-transitions.md#evidence-preserving-settlement): a proven-terminated Job may release capacity while its VM remains quarantined with protected required evidence. Cleanup cannot be reported verified until all required owned files are removed. Re-collection or authorized disposal and health verification restore availability without changing the terminal verdict.

## Amendment (Design Revision 3.3)

[ADR-0018](0018-persistent-vm-no-reset.md) keeps one persistent VM per resource. For a VM resource, verified reuse now means that Haepari's own processes have stopped and its job files are gone. Services, skills and data that a workload changes persist by design; the consumer's recipe removes or restores them, not Haepari.

## Deferred external extension (ADR-0016)

ADR-0014/0015 preserve the R2 external design, whose application is deferred by [ADR-0016](0016-managed-first-scope.md). Managed ownership and cleanup remain active.
