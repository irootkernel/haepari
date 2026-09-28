# Producer completion report: Haepari VM adoption

Status: TEMPLATE. Replace all bracketed fields with observed values or explicit gaps. Send this as a producer-to-Aquarium Handoff under the authorized rollout request; do not edit the sender-owned original request or the managed Vault.

## Correlation and ownership

Original logical request: [key and request digest]. Native request Handoff: [actual reference and revision]. Producer: [resolved project identity and repository]. Producer work: [producer-owned epic/task IDs]. This response has its own native Handoff identity.

## Candidate and environment

Source: [exact commit/snapshot and provenance class]. Haepari: [release, executable/runner/capability identities]. Input/test bundle: [digest]. Starting and target profile: [IDs and digests]. Candidate installation unit: [binary/application/daemon/plugin/skills/rules/configuration identities]. Native skill location/pin decisions: [evidence or not_applicable].

## Per-suite observations

| Suite | Job reference | Work outcome | Evidence | Cleanup | Gaps |
| --- | --- | --- | --- | --- | --- |
| Clean install | [observed] | [observed] | [bounded reference] | [observed] | [explicit] |
| Migration from prior accepted version | [observed or justified N/A] | [observed] | [old-state and post-upgrade invariants] | [observed] | [explicit] |
| Existing-behavior regression | [observed] | [observed] | [bounded reference] | [observed] | [explicit] |
| Installed-skill / hands-on | [observed or justified N/A] | [observer and outcomes] | [discovery/behavior references] | [observed] | [explicit] |
| Failure / recovery | [observed] | [observed] | [bounded reference] | [observed] | [explicit] |

## Legacy retirement

List exact removed producer/launcher/hook/controller/source references and the evidence that they were aquarium-dev-specific. List native use-* skills and generic dev features intentionally preserved. Separate source changes from live host detachment. For host changes, state scope, actual authority, stopped workers/services, exact owned marker removal, stable command resolution and remaining references. Do not claim the shared manager/root has been removed unless that separate final Aquarium operation occurred.

## Delivery and limits

Changed paths, exact producer commits, tests performed, tests not performed, known compatibility constraints, released/unreleased status and required follow-up. Do not include secrets, raw account stores, full provider transcripts or unreviewed private runtime data. Completion means the stated evidence exists; Aquarium's acceptance remains a separate review.

## Mulgae native review evidence when applicable

Report the installed `use-mulgae` tree, native review/recovery behavior, exact rerun lineage and composition according to Mulgae's own accepted scope. Ordinary-review Haepari lease identities are not required in v0.1.0. Review queuing on Haepari's advisory lock and any rate-limit safeguards are a separate Mulgae change (Haepari `docs/to-external.md` §2.1). They are not reported in this VM adoption packet. The managed VM installation, migration, regression, skill and cleanup packet remains mandatory.
