# VM operational readiness execution dossier

Status: EPIC-009 core delivery is Accepted in the canonical roadmap. This shared
dossier is retained because active EPIC-006/007 still reference its feedback
disposition. Canonical operating outcomes and the small caller handoff are linked
from the roadmap; excluded experiments do not become later closeout obligations.


Consumer Epic: [EPIC-009](roadmap/EPIC-009.md). This is its consolidated execution SOT for epic-handler, not a second lifecycle authority. The [roadmap](roadmap.md) owns identity, status, order and prerequisites; the Epic file owns task acceptance checklists.

Design Revision 3.5 supersession: EPIC-009 is Accepted and its campaign instructions/evidence below retain historical scope. Ordinary development and TASK-027 follow [ADR-0020](architecture-decision-records/0020-local-development-scope.md), not another full campaign. DF-001–004 and product-specific skill/migration qualification are optional; their uncertainty is preserved.

## Goal and reason for the work

An ordinary project must be able to submit a sealed VM workload, obtain truthful diagnostics and evidence, and run a successor after bounded failure handling. Unknown workload or filesystem-worker lifetime must remain fenced and occupied until resolved. Establish this operating loop before expanding project, skill, manual and migration integration.

The 2026-10-09 interim feedback was assessed against clean source revision 5011bab1c8cc5863ca7b30409627f50cb0b0b290. It motivated this adopted plan, not a new product acceptance. The high-impact source mechanisms are:

- A watch error ends the active observer; failed discards its cause, and startup reconciliation alone does not revisit a transient failure.
- Power advancement runs in fresh execution, while recovery refuses unresolved adapter intents.
- Cold preparation probes identity once after power reconciliation. The accepted TASK-016 record explicitly covers an already-running VM.
- Each guest exchange tars the full executable; a normal Call has identity and operation exchanges. Collection uses small pages. Per-Job host runner copies are not in the current purge payload list.
- The daemon creates the ledger with global limit one, despite the documented two-VM/global-three development profile.
- The real-UTM Make target now invokes the tracked [campaign driver](../tests/qualification/README.md). Later corrections still lack actual installed-candidate real-UTM qualification, and the historical campaign cannot establish their installed behavior.

These are entry-point facts to recheck at execution, not a claim about a currently running service. Transfer volume, cold-start latency, reboot behavior and failure frequency require measurement. Missing-output quarantine is also recorded in current contracts and accepted history; changing it requires a decision rather than treating it as an unauthorized implementation deviation.

## Authority and document map

| Owner | Purpose in this Epic |
| --- | --- |
| [Requirements](requirements.md) and [traceability](traceability.md) | Product obligations and corrective-task coverage; original owners remain recorded. |
| [Managed transitions](contracts/managed-transitions.md) | Verdict derivation, capacity return, uncertainty and resource availability. |
| [Resource and runner](contracts/resource-and-runner.md) | Power/guest identity, pinned transport, native proof and recovery semantics. |
| [Artifact contract](contracts/artifact-and-profile.md) | Existing versus missing evidence, delivery, retained runners and explicit purge. |
| [Configuration](contracts/configuration.md) | Distinct global and host guest limits; minimum startup selection versus later general configuration. |
| [F1](records/TASK-012-managed-core-freeze.md) and [F2](records/TASK-016-vm-contract-freeze.md) | Recorded frozen boundaries and change/version decisions. |
| [Testing](../TESTING.md), [verification](verification.md) and [operations](operations.md) | Product checks, scenario assertions and current versus planned operating procedures. |
| [External requests](to-external.md) and [deferred feedback](deferred-feedback.md) | Consumer-owned alignment and optional DF-001–004 uncertainty; no v0.1.0 release deadline. |

If a planned policy conflicts with a current contract, resolve and update that canonical owner before implementing it. This dossier links those decisions and integrates acceptance; it does not override them.

## Scope, sequence and delivery

The approved documentation plan adds TASK-036–039 and places EPIC-009 before EPIC-005. The canonical prerequisite table controls execution: TASK-036 consumes TASK-012 and TASK-015; TASK-037 follows TASK-036; TASK-038 also requires TASK-016; TASK-039 follows TASK-038; TASK-017 then consumes TASK-039.

TASK-036–037 can develop against isolated fixtures without real UTM. TASK-038 requires bounded live authority and actual VM observation; TASK-039 requires an actual isolated public-CLI e2e observation. Do not bypass their gates to start TASK-017. Master revised the goal on 2026-10-10: qualify the operating loop using one existing registered VM, with global limit one and host guest limit one. Two-VM/global-three qualification moves to [DF-004](deferred-feedback.md#df-004-two-vm-and-local-concurrency) for a separate later campaign and does not block TASK-038, TASK-039 or this Epic. TASK-037 acceptance and its implemented concurrency controls and fixture coverage remain intact. No third-VM framework-limit probe is required by this Epic.

| Task | Implementation direction | Required delivery and promotion |
| --- | --- | --- |
| TASK-036 | Separate absence, failed collection and unknown effects; retry safe reads under saved deadlines; revisit recoverable Jobs; resolve power/reboot ownership; retain bounded diagnostic causes. | Approved D1–D3, F1/F2 delta and versions, focused failure evidence, current recovery/diagnostic runbook. |
| TASK-037 | Wait for cold readiness; reuse digest-bound runner bytes; improve bounded collection; safely dispose finished runner payloads; expose minimal global-limit selection. | Measured call/byte baseline and candidate comparison, transfer/retention contracts, startup/service options and operating limits. |
| TASK-038 | Publish a CLI campaign, preflight exact candidates/resources, exercise the matrix and preserve restartable campaign evidence. | Tracked campaign and instructions, exact installed-source/executable/runner binding, scenario results and measured bounds in the VM qualification record. |
| TASK-039 | Freeze and submit a small test-owned offline input; use saved keys and public observations; test failure, successor reuse and queued input changes. | Reused minimal helpers/runnable e2e quickstart, native local observations and TASK-017 caller-contract lessons. |

For each task, implement the smallest complete change, run focused checks before the common gate, update the dossier's decision/handoff summaries, and promote durable behavior to the canonical owners. Use the repository's ordinary Git and the current epic-handler review/acceptance contract; this dossier starts no workflow, provider review or commit by itself.

## Decisions before affected implementation

Master approved D1–D3 for EPIC-009 execution: positively absent outputs may permit INCOMPLETE with verified cleanup and reuse; same-resource cross-boot recovery uses a separate fenced capability; no new evidence-loss escape is added. TASK-036 implements and reviews these decisions under the owning F1/F2 deltas. Existing v1 admissions and terminal history remain authoritative for their original policy.

| Decision | Proposed question and constraint | Work blocked until resolved |
| --- | --- | --- |
| D1: Missing output | Should proven workload termination plus positively proven file absence permit a gap-bearing INCOMPLETE result, verified cleanup and resource reuse? Decide separately from an existing but uncollected file, identity conflict or transport failure. Cancellation does not become CANCELLED without the required evidence facts. | New absence classification, result/cleanup semantics and automatic reuse. Common local/VM behavior belongs to F1; guest error transport also affects F2. |
| D2: Changed guest boot | Define when the same registered UUID/MAC/pin and a fresh boot prove the old execution stopped, how the original root/evidence can be recovered, and which foreign/unknown cases stay blocked. Host logout is a separate scenario. | Cross-boot recovery, any VM registration removal needed for recovery, and changes to guest proof/receipt interpretation. |
| D3: Evidence-loss escape | Decide whether a scoped development-only disposal path is necessary, what exact owned evidence may be lost and what attestation/provenance is required. Prefer a verified evidence-preserving path when it is possible. | Any additional break-glass disposal or destructive recovery beyond current exact authorization. No broad state-root or VM deletion. |

Also record the safe read-operation allowlist, error wire classification and F1/F2 version handling before implementation. A read failure is not proof that the workload ended. Unknown start/stop effects are never retried merely because the observer recovered. New schema versions need an explicit decision; neither a SQLite version nor this plan changes the frozen wire identity.

TASK-037 chooses its transport change using measured calls/bytes and the accepted identity/deadline constraints, documenting the concrete F2 delta before applying it. The scope does not preselect a new guest daemon, universal backend abstraction or compatibility layer.

## Campaign and acceptance matrix

TASK-038 provides the reproducible backend campaign on one existing registered VM; TASK-039 supplies the isolated small caller-e2e row under the 2026-10-12 scope revision. Every required row below needs candidate-bound observations. The deferred two-VM/local concurrency row belongs to DF-004 and is excluded from this acceptance matrix. Master removed VM power-on/off, cold-start, guest-reboot and power-intent scenario qualification from this Epic on 2026-10-12. Reuse the already-running registered VM; do not operate its power for this campaign. The removed scenarios are outside acceptance, not observed passes. Existing recovery implementation, fixture checks and historical evidence remain intact. The declared use case is one machine, one user and that user's already-running VM. Separate identity-refusal fault experiments and transfer/network benchmarking are not acceptance requirements; implemented VM/Job identity fences remain. Reproducibility is the tracked runnable small e2e command and its assertions, not another live VM campaign. A missing environment or unexplained core Job result blocks completion; a local result does not replace the existing VM execution evidence.

| Scenario | Required observation and success condition | Existing verification cases |
| --- | --- | --- |
| Success and successor | Exact collected bytes/log, verified owned-root cleanup, one return and next-Job reuse; outside-root guest changes persist. | V08, V18, V20, V21 |
| Missing versus uncollected output | Exercise D1 separately for confirmed absence, an existing unavailable file and a broken transport; preserve the workload result and the approved gap/retention behavior. | V20, V21, V53 |
| Failure, cancellation and timeout | Preserve native outcomes and cancellation races, original deadlines and one settlement; reuse only after approved collection/cleanup or recovery. | V14, V19, V20, V21, V40 |
| Temporary SSH observation loss | Recover the original run with bounded safe-read retries/reconciliation; once marker and receipts show no duplicate workload. | V15, V17, V42 |
| Controller restart | Reattach or safely reconcile original identities, workers and receipts without changing budgets or launching again. | V10, V11, V42, V47 |
| 50 MiB log | Verify the complete declared bytes/hash within the admitted collection budget; retain already-observed call count and elapsed time as supporting data. No hidden truncation or successful partial collection; no additional transfer benchmark is required. | V20, V65 |
| 30-Job soak | Interleave success and intentional failure/recovery with distinct saved keys; inspect occupancy, successor reuse and runner storage before/after explicit cleanup or purge. | V05, V08, V18, V21, V53, V65 |
| Small isolated caller e2e | A test-owned offline shell recipe runs through the actual public CLI/controller/runner, queued source changes cannot alter sealed inputs, saved-key reconnect never repeats execution, useful failure evidence and verified successor reuse are observed. No external project adoption is required. | V03, V06, V12, V18, V20, V36, V54 |

Set finite per-phase and aggregate campaign budgets before live execution. Readiness/retry/reconciliation consume saved phase deadlines. Logs and selected outputs declare collection bounds. Existing timing/application-byte measurements are supporting evidence; packet/protocol overhead is optional and must not block this Epic or trigger an additional campaign.

## Live entry and authority boundaries

Before TASK-038, disclose and approve the exact candidate/service plan, the existing registered VM identity, permitted workload/observation-interruption effects, budgets and restoration procedure. VM start/stop/reboot and power-intent scenario qualification are excluded by the 2026-10-12 scope revision. Require a reachable controller and a successful read-only public observation; LaunchAgent registration or running status alone is insufficient. Never relocate an executable or bypass host permissions as an automatic recovery.

Master revised TASK-039 on 2026-10-12 to a small repository-owned e2e caller test instead of external real-project application. Use the actual built product and an isolated foreground controller/local resource with selected test-owned files, a bounded offline recipe, meaningful failure and finite budgets. Reuse test-owned digest/spec helpers. No external project selection, installed-service modification or VM operation is required. Keep the local-e2e limit explicit and preserve TASK-038's separate real-VM prerequisite.

The implementation envelope must distinguish repository edits/checks, service replacement, VM effects, reviewed candidate acceptance, staging/commits and final Epic acceptance. No dossier text grants installation, network/provider effects, publication, external messages or changes to other repositories.

DF-001 actual DHCP expiry, DF-002 host logout/post-login recovery, DF-003 multi-day lease currency and DF-004 multi-VM concurrency remain unobserved optional hardening; Design Revision 3.5 removes their first-release deadline. The bounded same-boot or guest-reboot scenarios do not close them. Release services, manual/skill acceptance and Aquarium adoption remain later work.

## Feedback disposition

The IDs below identify the supplied interim feedback. Source-confirmed mechanisms, supplied observations and hypotheses retain distinct assurance. Assignment is a work owner, not a claim that all 37 findings are proven defects.

| ID | Assessment and disposition | Owner / reason |
| --- | --- | --- |
| F01 | Adopt earlier real use. | TASK-039 small caller cycle precedes TASK-017; external real-project adoption remains consumer-owned. |
| F02 | Consumer wording conflict confirmed. | Aquarium owner via external requests §1.2 before Aquarium:TASK-086; Mulgae owner decides lock adoption. No cross-repository edits here. |
| F03 | Consumer-specific shape merits review; requirements already leave compatibility decisions to recipes. | TASK-018 and TASK-020; use pilot feedback, do not automatically shrink scope. |
| F04 | Complexity/effort metrics are review judgments. | TASK-021 and existing structural TODO candidates; do not use line counts alone to reject safety mechanisms. |
| F05 | Missing-output quarantine confirmed and already contractual. | TASK-036/D1, then TASK-038; not an unapproved implementation deviation. |
| F06 | Observer abandonment and absent periodic recovery confirmed; holding unknown execution is correct. | TASK-036, TASK-038; recover observation without speculative release. |
| F07 | Full-runner retransmission confirmed; throughput estimates are unmeasured. | TASK-037 baseline/change, TASK-038 real measurements. |
| F08 | Single-shot cold readiness confirmed. | TASK-037 readiness implementation; extra TASK-038 cold-start observation is excluded from the revised core scope. |
| F09 | Same-boot recovery limitation confirmed; DF-002 impossibility is not established. | TASK-036/D2 implementation; guest-reboot experiments are excluded from TASK-038 and host logout remains DF-002. |
| F10 | Recovery friction is partly policy. | TASK-036 decisions and runbook; preserve exact ownership/provenance. |
| F11 | Global limit one and missing general loader/doctor confirmed; configuration contract already names TASK-023. | TASK-037 minimum limit; TASK-023 explicit general loader/doctor completion. |
| F12 | Freeze inventory/version consistency needs review. | TASK-036 F1/F2 delta; TASK-020 consumes pilot evidence. No blanket additive-compatible waiver. |
| F13 | Public events also carry private replay authority. | TASK-022 audits separation and access; do not call same-user fencing metadata an authentication secret. |
| F14 | Pre-release migration complexity is confirmed; removal is not adopted. | TASK-024 retains existing-state safety and evaluates the need for any change. |
| F15 | Automatic retention is absent by the current implementation boundary. | TASK-023 general retention policy; TASK-037 fixes runner payload disposal first. |
| F16 | Effort inversion is a policy judgment. | TASK-036/D1/D3; persistent VM scope stays intact. |
| F17 | Repeated identity proof contributes to transfer cost. | TASK-037; preserve identity checks when eliminating redundant transport. |
| F18 | Repeated state-machine/import paths need scoped review. | TASK-021 and structural TODO; no mandatory universal jobBackend refactor. |
| F19 | Running-VM log usability requires behavior verification. | TASK-023 log streaming/diagnostics; pilot records whether it blocks meaningful use. |
| F20 | Tracked qualification stub and corrected-candidate evidence gap confirmed. | TASK-038; publish and observe the exact campaign. |
| F21 | One already-running VM is narrower than the development profile. | TASK-038 qualifies the corrected operating loop on one VM; DF-004 retains unobserved two-VM/global-three qualification for a separate campaign. |
| F22 | Test-duration history is supplied evidence. | TASK-023 measurement; do not remove failure coverage merely to shorten tests. |
| F23 | State wording drift merits cleanup. | This plan synchronizes affected references; TASK-026 owns wider documentation cleanup. |
| F24 | First-use friction has source/supplied walkthrough support. | TASK-039 test-owned helpers/runnable e2e quickstart; no claim all supplied examples were rerun. |
| F25 | Formal project VM wrappers are not yet delivered. | TASK-039 minimum CLI recipe; TASK-017 owns the full Make contract. |
| F26 | Idle lock sweep commits are source-supported; cost requires measurement. | TASK-023; preserve high-water/wake safety before changing writes. |
| F27 | Census cost and PID-reuse impact are hypotheses requiring measurement. | TASK-023; retain native process-identity proof. |
| F28 | MAC parsing is restrictive; actual bootpd representation/frequency is uncertain. | TASK-037 verify supported forms, TASK-038 real lease observation; no probability claim. |
| F29 | Filesystem primitive/durability drift needs targeted comparison. | TASK-021 and structural TODO; preserve platform-qualified durability. |
| F30 | Migration authority duplication is source-supported. | TASK-024; consolidate only with retained replay/rollback proofs. |
| F31 | Test-only exports and weak substitutes are lower-priority review claims. | TASK-021 evaluates meaningful coverage before cleanup. |
| F32 | Error classification/atomicity claims need a concrete reproducer. | TASK-022 audit; no unverified broad rewrite. |
| M1 | Per-Job runner payload persists outside purge. | TASK-037, TASK-038 soak; retain active/unresolved runners. |
| M2 | Cold-start prerequisite lacks observation or an explicit matching waiver. | Cold-start qualification is excluded from the revised TASK-038 scope; preserve historical TASK-016 acceptance with its stated scope. |
| M3 | Guest/controller error causes are lost or opaque. | TASK-036 bounded durable diagnostics. |
| M4 | Unresolved power intent blocks recovery paths. | TASK-036 resolution implementation; the extra TASK-038 power-interruption campaign is excluded from the revised core scope. |
| M5 | Executable is read for transport construction; optimization benefit needs comparison. | TASK-037 runner identity/reuse design, without mutable caches. |

These assignments are summarized in the existing task and external-request owners. Newly discovered independent work must name a canonical owner before being deferred; it must not silently expand this Epic.

## Evidence, handoff and closeout

For each accepted candidate retain source revision (or exact isolated candidate digest before commit), executable and guest-runner digests, platform/service/resource qualification scope, commands, scenario outcomes, phase/call timings, byte/storage measurements and skipped checks with reasons. Never present historical receipts as fresh evidence. Private run IDs, local state paths, tokens, SSH material and raw workflow logs stay local; promote only bounded non-secret summaries.

TASK-039 hands TASK-017 the small offline recipe, declared inputs/outputs, helper invocation, candidate-bound result summary, local-e2e limits and open consumer questions. It does not deliver a release or freeze the generic project Make/guest entrypoint, opaque profile reference and general manual-result contract. ADR-0020 narrows that later F3 boundary; rich consumer environment locks remain optional.

At final Epic assessment, repeat affected integration scenarios on the final candidate, reconcile the task checklists with this matrix, and state every environmental limit. Promote behavior to contracts, measurements/qualification to the VM record, operating procedures to operations, developer commands to TESTING/implementation guidance, and consumer alignment to external requests.

After explicit Epic acceptance, replace this Epic's Detailed SOT links with Canonical Outcomes and inspect all dossier references. Delete the temporary dossier and its index entry only under approved closeout authority after no active consumer needs it. This adoption neither deletes the dossier nor pre-authorizes its later removal.


## TASK-037 candidate handoff

The before-protocol cost observation and selected capability are promoted to
[ADR-0019](architecture-decision-records/0019-owned-runner-transport.md). The
[runner contract](contracts/resource-and-runner.md#task-037-readiness-and-owned-transport-capability)
owns readiness, immutable admission/binding, historical wire, completed-cleanup
verifier and explicit payload purge semantics. The
[configuration contract](contracts/configuration.md#task-037-startup-limits)
owns global/host startup limits and explicit v2 development-service plans.
[Operating controls](operations.md#task-037-operating-controls-and-task-038-handoff)
define the measurable TASK-038 candidate and separate live approval boundary.
The source-backed MAC representation change leaves registration and guest
identity strict; actual installed DHCP observation remains TASK-038.
