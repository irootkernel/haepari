# ADR-0009: Test migration with prior-version state

Status: accepted for v0.1.0 design; implementation pending.

## Context

Clean installation cannot establish whether existing configuration, data, skills and work survive an upgrade.

## Decision

Create meaningful synthetic state through a verified old release, record pre-upgrade invariants, upgrade in place, and verify preserved behavior, new writes, restart, and supported interruption recovery before the recipe cleans up.

## Alternatives considered

Recreating fixtures with the new version or wiping state before assertions tests installation only. Removing a test installation does not prove product downgrade.

## Consequences and revisit condition

Supported source versions and busy-work policy must be declared. The immediate prior accepted profile is the default required path; wider promises require wider evidence.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).
