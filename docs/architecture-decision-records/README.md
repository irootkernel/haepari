# Architecture decision records

ADR-0001 through ADR-0013 apply to the active managed design. ADR-0014/0015 preserve R2 external design history; ADR-0016 defers their application. All are pending implementation. They do not claim deployed behavior. Changes require a new record or an explicit superseding decision.

| ADR | Decision | Status |
| --- | --- | --- |
| ADR-0001 | [Independent Go CLI and daemon](0001-go-independent-cli.md) | Accepted for v0.1.0 design |
| ADR-0002 | [Single local controller with durable SQLite state](0002-local-durable-controller.md) | Accepted for v0.1.0 design |
| ADR-0003 | [Schedule resources rather than application workflows](0003-resource-scheduling.md) | Accepted for v0.1.0 design |
| ADR-0004 | [Freeze input and deduplicate admission before execution](0004-immutable-admission.md) | Accepted for v0.1.0 design |
| ADR-0005 | [Resource release requires verified cleanup](0005-cleanup-before-release.md) | Accepted for v0.1.0 design |
| ADR-0006 | [UTM lifecycle on the host, SSH supervision in the guest](0006-utm-ssh.md) | Accepted for v0.1.0 design |
| ADR-0007 | [Keep tests in their owning product repositories](0007-project-owned-tests.md) | Accepted for v0.1.0 design |
| ADR-0008 | [Use frozen installation combinations including skills](0008-installation-combinations.md) | Accepted for v0.1.0 design |
| ADR-0009 | [Test migration with prior-version state](0009-real-migration-state.md) | Accepted for v0.1.0 design |
| ADR-0010 | [Manual results are explicit observations](0010-manual-evidence.md) | Accepted for v0.1.0 design |
| ADR-0011 | [Use a thin native integration before adding MCP](0011-native-cli-first.md) | Accepted for v0.1.0 design |
| ADR-0012 | [Separate runtime isolation from authorization and external effects](0012-isolation-and-effects.md) | Accepted for v0.1.0 design |
| ADR-0013 | [Keep adoption and legacy retirement outside Haepari core](0013-consumer-owned-rollout.md) | Accepted for v0.1.0 design |
| ADR-0014 | [Managed and external execution leases](0014-external-execution-leases.md) | R2 design retained; deferred by ADR-0016 (B1 open) |
| ADR-0015 | [Logical recovery spans distinct capacity leases](0015-linked-recovery-segments.md) | R2 design retained; deferred by ADR-0016 (B2 open) |
| ADR-0016 | [Managed-first v0.1.0 scope](0016-managed-first-scope.md) | Accepted for v0.1.0 design (Design Revision 3; alternatives added in 3.1; amended by ADR-0017 in 3.2) |
| ADR-0017 | [Advisory named locks for cooperating tools](0017-advisory-named-locks.md) | Accepted for v0.1.0 design (Design Revision 3.2) |

ADR-0014 and ADR-0015 record the Design Revision 2 external-lease and linked-segment decisions. ADR-0016 (Design Revision 3) defers their application; re-entry requires B1/B2 closure in the [deferred contract](../contracts/external-leases.md) and a separate accepted consumer need. Existing decision IDs are preserved.
