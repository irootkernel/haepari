# ADR-0008: Use frozen installation combinations including skills

Status: accepted for v0.1.0 design; implementation pending.

## Context

Binary-only tests miss paired skills, external global skills, rules, plugin payloads and effective connections. Latest is not a reproducible target.

## Decision

Consumers define immutable environment locks. Default development replaces one declared installation unit of an accepted profile. Release integration applies the intended full combination. Binary and skill identities remain separate.

## Alternatives considered

Resolving latest at dispatch changes queued work. A central Haepari registry of compatible tool versions would duplicate Aquarium authority. Assuming every skill shares a binary tag violates independently pinned native contracts.

## Consequences and revisit condition

Profile selection and component verification are consumer responsibilities. Haepari freezes the input and transports evidence. Profile maintenance is explicit and cannot mutate accepted jobs.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).
