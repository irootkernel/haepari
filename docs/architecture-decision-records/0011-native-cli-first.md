# ADR-0011: Use a thin native integration before adding MCP

Status: accepted for v0.1.0 design; implementation pending.

## Context

The user needs a reusable tool callable from Make, terminals and multiple AI environments.

## Decision

Provide a versioned CLI, private local API, blocking wait/events and a same-release use-haepari skill. Do not require an MCP server in v0.1.0.

## Alternatives considered

Starting with multiple transports increases protocol and lifecycle surface. A script that repeatedly polls status loses the supported waiting semantics.

## Consequences and revisit condition

Agents use the CLI once and await the same job. A future MCP adapter may reuse the application boundary without introducing another scheduler or new state authority.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).
