# ADR-0012: Separate runtime isolation from authorization and external effects

Status: accepted for v0.1.0 design; implementation pending.

## Context

VMs can still reach host services or external accounts, and local resources deliberately execute on the host.

## Decision

Keep host adapter configuration trusted, disable broad sharing/agent forwarding, pin guest identity, and exclude credentials from bundles. Product recipes declare live endpoints and effect/cost boundaries. Treat same-user code as trusted within the controller account.

## Alternatives considered

A resource name or token is not an OS sandbox. No VM cleanup can undo remote writes or model charges. Automatic authentication copying conflicts with the isolation objective.

## Consequences and revisit condition

Some live tests remain blocked until test credentials and endpoints are provisioned. Those gaps cannot be converted into successful skips.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).
