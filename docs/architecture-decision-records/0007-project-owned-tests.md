# ADR-0007: Keep tests in their owning product repositories

Status: accepted for v0.1.0 design; implementation pending.

## Context

The user explicitly prefers each project to own its E2E and hands-on scenarios and expose make test-vm.

## Decision

Define a small common submission/guest-result contract. Project Makefiles package tests and submit to Haepari. Haepari owns VM lifecycle. Aquarium owns the integration behavior it consumes.

## Alternatives considered

A mandatory integration-test repository separates product changes from their acceptance. A Haepari DSL for every test would duplicate existing test frameworks.

## Consequences and revisit condition

A project must implement a host wrapper and a distinct guest entrypoint. Shared infrastructure examples live with Haepari, while application assertions remain with products.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).
