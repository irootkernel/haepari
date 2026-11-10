# Changelog

## v0.1.1 - Unreleased

### Fixed

- Keep VM Job recovery plans stable across observation-only SSH measurements while retaining audit history and revision, lease, ownership and native-evidence fences. New VM Job plans use `haepari.recovery-plan/v4`; operator attestation still cannot produce PASS.

## v0.1.0 - 2026-10-22

### Added

- Support automatic Jobs and manual Jobs with recorded human observations on macOS arm64 local resources and one already-running registered persistent UTM macOS VM, without issuing VM power commands.
- Provide closed, versioned JSON contracts and explicit workload, evidence, cleanup and final-verdict reporting for CLI integrations.
- Seal selected-file inputs and raw environment profiles, with reusable project-test helpers and a separate guarded guest entrypoint.
- Persist FIFO queues, capacity leases, immutable request keys and terminal results, with same-key replay, blocking observation and explicit cancellation.
- Supervise workloads independently of callers, reconnect to original executions after SSH or controller interruption, and require proof-bound recovery before reusing uncertain resources.
- Collect bounded logs and hashed artifacts with paged reads, explicit evidence gaps, verified export and retention/purge guards that preserve protected evidence and admission identities.
- Coordinate cooperating tools with durable advisory locks, bounded renewal and expiry, and read-only nested-borrower validation.
- Expose bounded controller configuration, resource-readiness checks and queue/retention diagnostics while preserving occupied or quarantined resources when execution or cleanup is uncertain.
- Ship a checksummed macOS arm64 archive with a standalone ad-hoc-signed executable, the `use-haepari` skill, portable examples, a complete-tree verifier, the MIT license, applicable third-party notices and explicit per-user release-service setup.
