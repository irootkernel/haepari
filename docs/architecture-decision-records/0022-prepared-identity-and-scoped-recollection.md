# ADR-0022: Bind prepared identity and separate scoped evidence collection

Status: Selected for the Master-approved recovery correction, 2026-11-13; implementation verification and publication remain separate.

## Context

A mount device number can change after a reboot without replacing a retained Job directory. Device/inode alone cannot safely distinguish that case from replacement. A resource-wide recollection also stops at an unrelated unresolved historical Job, obscures the failing validation, and can prevent delivery of an immutable result whose original guest log was explicitly never collected.

The original workload outcome, evidence completeness, native termination, cleanup and resource reuse remain separate facts. Neither a new executable nor an operator assertion rewrites an original INCOMPLETE result or false native termination receipt. A transfer timeout is not evidence of a transport implementation defect.

## Decision

New guest preparations record descriptor-observed local volume UUID, 64-bit file ID and creation time for root, workspace, control, runner and log. The filesystem must advertise persistent object IDs. The host retains the original preparation binding. Within a boot, device/inode checks remain mandatory. Across proven different boots, recovery additionally requires the original persistent identities, request and receipt digests, runner digest, stopped owners and the ordinary revision, lease, deadline and ownership fences. Unsupported attributes, missing original identity, replaced objects and live owners refuse without adopting or deleting evidence.

Use new outer wire versions, rather than adding preparation authority to frozen v1/v2 messages:

| Family | Historical versions | New versions |
| --- | --- | --- |
| Guest request/reply and control | v1 ordinary; v2 absence-aware | v3 ordinary; v4 absence-aware, both with prepared identity |
| Native runner request | v1 local and historical guest | v2 prepared guest log identity |
| Guest recovery request/reply and attestation | v1 same-boot; v2 reboot | v3 same-boot; v4 reboot with prepared identity |
| Guest transport binding | v1 historical | v2 prepared identity |
| Attested cleanup | v1 ordinary; v2 absent outputs | v3 ordinary; v4 absent outputs with new attestation authority |
| Host evidence recovery intent/receipt | v1-v3 historical | v4 permits prepared roots and preserves optional resource-owned budgets |

Historical absent fields serialize identically, and original bytes and binding digests remain unchanged. Old guest/control/recovery versions reject new identity fields. Replaying a completed preparation never adds identity. A legacy prior-boot orphan is not automatically migrated or marked verified. Exact permanent-key replay returns its original saved receipt and deadlines.

The `job-evidence-recovery/v1` and `persistent-guest-identity/v1` capabilities distinguish the correction. A selected `resource recover --plan --action recollect --job-id <id>` binds one retained owned Job into its signed plan, durable intent and finite deadlines. Resource-global occupancy and adapter fences still apply. Omitting the target keeps resource-wide behavior; selected dispose and target overrides on apply are rejected. Other unresolved Jobs remain unresolved and health continues to block reuse. Plans carrying prepared identity use `haepari.recovery-plan/v5`; historical plans keep their original version selection.

Recovery refusal diagnostics expose only a validated Job ID, fixed stage and fixed cause category. Existing HTTP/error/exit/retry classification and the tokenless native-termination special case remain intact. Raw backend errors and private paths or credentials are not rendered.

Partial export may omit the original host log only when the immutable result explicitly records `guest_log_uncollected` and both the log and durable host binding are absent under a verified private Job directory. Export retains the original result, log metadata and gaps and verifies every delivered file. Missing or changed previously bound evidence, links, permissions failures and unexplained absence still refuse. Supplementary collection does not impersonate the original host log.

## Consequences and verification

Use isolated native and modeled-boot fixtures to verify device-only renumbering, replacement negatives, original receipt/digest guards, immutable false proof and INCOMPLETE, scoped A recovery with B held, stale plans/configuration/leases, restart/replay and partial export. Run focused race checks, then the assembled common test and documentation gates. These checks do not qualify a real reboot or change the installed controller.

The current v0.1.0 Aquarium development instance, prior ledger and archived orphan remain untouched. Installation, a correction release and consumer adoption are separate actions.
