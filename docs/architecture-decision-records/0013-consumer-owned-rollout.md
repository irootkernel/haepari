# ADR-0013: Keep adoption and legacy retirement outside Haepari core

Status: accepted for v0.1.0 design; implementation pending.

## Context

Aquarium will coordinate adoption through Sorage, but the old development channel has hooks, workers, services and live users that may outlast individual source changes.

## Decision

Haepari ships an exact producer handoff. Aquarium adopts itself first, then delegates each named producer in order through Sorage, accepts returned evidence, and finally reconciles the combination and retires shared legacy state.

## Alternatives considered

Haepari installation must not delete Aquarium state. Aquarium must not implement foreign repositories directly or treat a request acknowledgement as completed work.

## Consequences and revisit condition

Producer source cleanup and host decommissioning are distinct. Stable Haepari and Sorage remain the control plane while their candidates are tested. The final destructive cleanup requires an explicit scope and verified prerequisites.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).
