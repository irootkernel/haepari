# Haepari producer completion packet

This is a completion template, not completed evidence. Aquarium:TASK-086 accepts the exact packet before implementation begins.

## Product and interface identity

- Product version, source commit, release state and supported platform executions.
- Archive and executable SHA-256 for the host CLI/controller and guest runner.
- Complete use-haepari source/tree identity and native installation guidance.
- Supported job/resource/result/event/runner/manual schemas and exact CLI exit semantics.
- Capability output distinguishing compiled features from actually qualified UTM capability.
- Storage schema compatibility and stable-controller update/recovery policy.

## Observed acceptance

- Requirements coverage and the finite verification matrix with actual outcomes.
- Independent local-command use case plus a real UTM persistent-VM use case.
- Lost reply, restart, SSH disconnection, cancellation, manual ownership and quarantined-resource recovery.
- Immutable candidate/test transfer, declared outputs and owned cleanup.
- Haepari storage compatibility and test-owned controller recovery fixture results; product migration is optional consumer evidence.
- Protected host state and independent controller self-test evidence.
- Exact environment/UTM/OS prerequisites, permissions, runner identity and the persistent-VM model (no reset, ADR-0018).

## Consumer assets

- Versioned schemas and marked examples that the consumer can validate locally.
- Project Make wrapper and separate guest entrypoint reference implementation.
- Result collection/export guidance, profile and VM resource registration interface and manual completion format.
- Operator runbook, known limits and evidence that remains unobserved.

## Acceptance boundary

Do not populate fields with assumed values. A local build is not an official release; a test result is not host installation or publication authority. The packet may be produced before publication, but the consumer must explicitly choose and verify the exact evidence class it accepts. Default stable adoption requires the verified official Haepari release. No part of this packet removes the old Aquarium development channel.

## Design Revision 3 managed-first packet

Include exact managed schemas/capabilities frozen at gates F1, F2 and F3, the advisory lock contract and `advisory-lock/v1`, local and real UTM execution evidence, the 58 mandatory V-case results in their declared layers (V25–V31 are optional consumer cases), nested acquisition and recovery cases, controller/runner exit behavior, job cleanup, packaged skill files and documented CLI flow, storage schema classes, and known gaps. External lease activation, dispatch, settlement, continuation and E01–E20 are deferred and cannot block Aquarium:TASK-086. State any unqualified macOS session, Automation permission, guest identity or cleanup prerequisite explicitly. State the Aquarium-side plan alignment from [external requests](../to-external.md) §1.2 as a consumer prerequisite. No packet content implies Aquarium acceptance or publication.

## Design Revision 3.5 boundary

The producer qualifies one machine/user/already-running VM with generic automatic/manual Jobs. F3 covers wrapper/guest entrypoint, immutable consumer environment references and general manual results. Product-specific skill discovery/behavior, installation compatibility and migration recipes belong to consumers, not Haepari completion. Power/logout/DHCP-expiry/multi-day/two-VM and performance campaigns remain optional unobserved limits. Ordinary execution records exact identities under an established scope without per-Job approval files. Existing F1/F2, packaged-service/storage checks and safe recovery remain. See [ADR-0020](../architecture-decision-records/0020-local-development-scope.md).
