# Mulgae adoption and review capacity boundary

Status: Design Revision 3.4 handoff. Ordinary Mulgae reviews queue on Haepari's advisory lock `mulgae-review` and run when granted ([ADR-0017](../architecture-decision-records/0017-advisory-named-locks.md)). Haepari is not a provider-call scheduler, and the R2 external lease stays deferred. The prior R2 external design remains in [ADR-0014](../architecture-decision-records/0014-external-execution-leases.md), [ADR-0015](../architecture-decision-records/0015-linked-recovery-segments.md) and the [deferred contract](../contracts/external-leases.md), subject to B1/B2 re-entry. Its six-role lease examples are not current consumer acceptance.

## Managed VM adoption

Aquarium:TASK-092 later asks Mulgae to own `make test-vm` and a distinct guest entrypoint, sealed candidate/test bytes, fixed profile, installation unit, complete `use-mulgae` tree and effective shared/selected-CODEX_HOME locations. Mulgae verifies clean install, meaningful old-version state and migration, regression, fresh-session discovery and observed behavior, manual checks where applicable, cleanup of its test installation and legacy-detachment evidence. Haepari collects integrity and declared required-result completeness; Mulgae supplies installers, fixtures, assertions, native service behavior and product-specific recovery promises. Test cleanup in the VM is not proof of Mulgae downgrade support. Native partial-role recovery, exact rerun lineage and composition remain Mulgae responsibilities.

## Review queuing through the advisory lock

**Why.** One review with six or seven concurrent roles works, but two concurrent reviews on the same provider account sometimes fail. The owner therefore decided that each ordinary review waits its turn in Haepari.

**Where Mulgae acquires the lock.** MCP and the CLI start reviews through one shared path in Mulgae, so Mulgae acquires `mulgae-review` exactly once, at that shared start:

1. Persist the key and payload.
2. Acquire and wait. The wait happens inside the review run, so the agent's tool calls stay short.
3. Renew the grant from the running review process.
4. As the acquiring owner, release on every path after the protected work finishes.

A caller that acquired a grant owns renewal and release through the end of its protected work. It may pass `HAEPARI_LOCK_GRANT` to a nested Mulgae invocation; the child first checks the selected Haepari installation, intended `mulgae-review` lock and current grant ID/generation/state. A valid child borrows the grant without acquisition, renewal, cancellation or release, including on failure. An invalid or stale reference is returned to the owner, never silently reacquired. `use-mulgae` does not acquire a separate lock.

The [lock contract](../contracts/locks.md) defines the commands, expiry and exits.

**Limits.** The lock is advisory:

- Haepari never starts, observes or interprets the review.
- A review started without the lock is not coordinated.
- A grant whose holder process died ends after one liveness window (120 seconds by default).

**What stays with Mulgae:**

- **Haepari unavailable.** Mulgae decides the behavior. The recommendation is a clear failure with an explicit user override.
- **Optional safeguards.** Mulgae-internal account slots and a retry after a rate limit remain optional; they cover direct calls.

The requested Mulgae changes are in [external requests](../to-external.md) §2.1.

## Acceptance separation

A Mulgae VM Job result reports workload outcome, submitted evidence, cleanup and verdict. Mulgae's native review result separately reports role coverage, retained sources, rerun lineage, composition, findings and CI. One cannot substitute for the other. A producer completion packet states exact observed values and gaps; no ordinary-review lease ID is required.
