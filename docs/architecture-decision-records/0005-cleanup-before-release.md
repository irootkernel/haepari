# ADR-0005: Resource release requires verified cleanup

Status: accepted for v0.1.0 design; implementation pending.

## Context

A failed install or disconnected shell can leave a VM running with modified services, skills or data.

## Decision

Keep the lease until preparation/work/collection/cleanup settle. Release only after verified reuse; otherwise quarantine. Separate workload outcome from evidence, cleanup and final verdict.

## Alternatives considered

Releasing on process exit, SSH disconnect or lease timeout permits cross-test contamination. Treating cleanup failure as a warning can create misleading passes.

## Consequences and revisit condition

Some failed jobs stop that resource until operator recovery. Other resources remain available. A recovery operation cannot rewrite a product result to PASS.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).

## Deferred external extension (ADR-0016)

ADR-0014/0015 preserve the R2 external design, whose application is deferred by [ADR-0016](0016-managed-first-scope.md). Managed ownership and cleanup remain active.
