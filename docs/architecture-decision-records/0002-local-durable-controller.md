# ADR-0002: Single local controller with durable SQLite state

Status: accepted for v0.1.0 design; implementation pending.

## Context

Jobs must survive the submitter closing a terminal or an AI session. A shared VM needs one owner, not several competing clients.

## Decision

Use one daemon per OS user/state root, a private Unix socket, an OS ownership lock, and a SQLite store. Only the daemon admits jobs and assigns leases. Store data on a local filesystem.

## Alternatives considered

Per-command file locks alone do not provide durable job recovery. Redis, a remote database and distributed workers add deployment and failure modes that are not required for v0.1.0.

## Consequences and revisit condition

The design is local-first and single-user, with explicit recovery after crashes. SQLite is not an external-effects transaction manager. Public/multi-host operation requires a separate design.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).
