# ADR-0003: Schedule resources rather than application workflows

Status: accepted for v0.1.0 design; implementation pending.

## Context

The first resource is a UTM VM, but ordinary build slots and test devices need the same admission and ownership semantics.

## Decision

Give each job exactly one named resource and one slot. Serialize the UTM resource; permit declared local capacities and independent resources. Keep product workflows in project recipes.

## Alternatives considered

A single global FIFO would unnecessarily block unrelated resources. A general DAG, dynamic dependency resolver, or Podway replacement would exceed the current need.

## Consequences and revisit condition

The queue is reusable without learning product names. Multi-resource reservation, distributed placement and complex priorities are deferred; capacity is a concurrency bound rather than an OS sandbox.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).

## Deferred external extension (ADR-0016)

ADR-0014/0015 preserve the R2 external design, whose application is deferred by [ADR-0016](0016-managed-first-scope.md). Managed ownership and cleanup remain active.
