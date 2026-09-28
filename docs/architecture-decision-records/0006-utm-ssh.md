# ADR-0006: UTM lifecycle on the host, SSH supervision in the guest

Status: accepted for v0.1.0 design; implementation pending. Amended by [ADR-0018](0018-persistent-vm-no-reset.md) in Design Revision 3.3.

## Context

UTM automation is backend/version dependent, while the macOS guest can expose a controlled SSH entrypoint.

## Decision

Use a qualified host-side UTM adapter, one persistent VM per resource ([ADR-0018](0018-persistent-vm-no-reset.md)), and SSH/SFTP to an identity-checked guest. A transient runner persists across SSH disconnects. Concrete UTM commands are discovered and qualified, not assumed.

## Alternatives considered

Project-owned AppleScript duplicates destructive control logic. QEMU guest-agent operations cannot be assumed for a macOS Apple-backend guest. Snapshot/disposable optimization is not necessary for the first release. Design Revision 3.1 considered a beta UTM snapshot/disposable mode and a different VM manager (tart); neither is adopted for v0.1.0 (see [ADR-0016](0016-managed-first-scope.md)). Design Revision 3.3 drops per-job reset altogether ([ADR-0018](0018-persistent-vm-no-reset.md)).

## Consequences and revisit condition

Qualification requires real capability evidence. The operator owns the VM; the adapter binds its UUID and owns only its host and guest job paths. Project payloads cannot redefine them.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).
