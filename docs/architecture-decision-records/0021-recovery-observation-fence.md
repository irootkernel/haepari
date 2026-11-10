# ADR-0021: Keep VM recovery observations outside the mutation fence

Status: Selected for the Master-approved recovery correction, 2026-11-10; implementation verification is recorded separately.

## Context

Mulgae reported Sorage Handoff `a511b8ae-cfed-4f0c-a765-8f9238a2b543`, Revision 1. VM Job recovery captures its ledger snapshot before SSH observation. SSH measurement callbacks append audit events without changing Job revision. Hashing every event therefore makes the plan invalidate itself during subsequent observation. Repeating plan/apply or exhausting the measurement limit is not a repair.

## Decision

New VM Job plans emit `haepari.recovery-plan/v4`. Keep the original event stream and bounded measurement policy. Exclude only known observation-only SSH telemetry from the recovery snapshot event digest, as specified in the CLI contract. Preserve recorded effectful exchanges within the existing measurement limit, unrecognized/malformed records, durable native evidence, identity, ownership, phase, resource and revision fences. The recovery-protocol `probe` is observation-only too; `inspect` may refresh control observation metadata without granting workload authority. Measurement overflow still records a gap, not unlimited exchange detail. Canonical complete exchange records share one ledger/transport wire type; malformed, missing, null and noncanonical fields are never exempt. Apply still re-observes and checks the exact snapshot transactionally.

The frozen `haepari.help/v1` supported-schema list adds only `haepari.recovery-plan/v4`; its structure and existing entries are unchanged. This is the current source delta, not a rewrite of the dated TASK-028 producer package or its historical 23-name inventory.

Local Job and resource plan schemas are unchanged. Private schema 13 and saved requests/receipts need no migration. Existing committed requests replay their original receipts. Unapplied tokens retain their two-minute expiry and controller-instance binding; this change does not carry old tokens across controller replacement. Operator attestation keeps its provenance and cannot establish PASS or rewrite original native evidence.

## Consequences and verification

Fixtures must enable actual transport measurement callbacks, retain audit growth while proving stable observations and successful public apply, and verify exact replay, immutable false native proof and INCOMPLETE attested settlement. Ledger checks cover observation events between snapshot and apply, effectful/unknown records and unchanged revision/identity guards. Run focused race checks followed by the common test gate. These isolated fixtures do not qualify the installed controller or repair Mulgae's occupied resource.

The original `process_census` / `process_incarnation` / `identity_changed` stop remains an observed safety stop with unknown cause. Track its independent diagnosis in the canonical TODO; do not label it a false positive or weaken native supervision.
