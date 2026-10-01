# EPIC-003 execution dossier

Consumer epic: [EPIC-003](roadmap/EPIC-003.md). Planning basis: committed source `a195301fbe9de5bf9ef00bed89620fa99c03c16b`.

This temporary dossier connects existing requirement owners, task dependencies, acceptance and handoffs. The [canonical roadmap](roadmap.md) alone owns identity, ordering, prerequisites and lifecycle status. The [epic checklist](roadmap/EPIC-003.md) owns implementation acceptance; this dossier records neither a second status nor new product requirements. Re-read the current authorities before execution.

## Goal and boundaries

Complete the existing local managed Job path with an independent per-job supervisor, safe phase settlement, restart reconciliation, explicit recovery and retained evidence, then freeze the managed core contract at F1. Keep one controller and scheduler; the runner supervises one exact run and does not admit Jobs or reserve capacity.

TASK-009 through TASK-012 are the complete membership. Preserve sealed inputs, permanent admission and cancellation keys, revisioned resource ownership, inherited parent guards, advisory-lock separation and immutable terminal results. Process exit, cancellation acknowledgement and a successful command are not proof of safe reuse or acceptance.

Actual UTM/SSH execution and backend qualification belong to TASK-013 through TASK-016; the common transition and evidence contracts needed by those tasks are part of this epic. Manual completion and session details belong to TASK-019, with their format frozen at F3 by TASK-020. F2, F3, release qualification, downstream Aquarium adoption, Linux runtime and public external execution remain outside this epic; public external execution is deferred. No VM reset or speculative workload replay is permitted.

## Execution authorities

| Responsibility | Canonical owner |
| --- | --- |
| Goal, membership, prerequisites, sequence and lifecycle | [Roadmap](roadmap.md), with [EPIC-003 acceptance](roadmap/EPIC-003.md) |
| Product obligations | [Requirements](requirements.md), HP-REQ-016 through HP-REQ-023 and HP-REQ-062/F1 |
| Components, persistence, supervision and recovery boundaries | [Architecture](architecture.md) |
| Phase transitions, capacity, verdicts and recovery ordering | [Managed transitions](contracts/managed-transitions.md) |
| Job/result/envelope schemas, CLI errors, exits and cursors | [CLI and Jobs](contracts/cli-and-jobs.md) |
| Runner identity, receipts, local reuse and adapter fencing | [Resource and runner contract](contracts/resource-and-runner.md) |
| Owned artifacts, integrity and protected evidence | [Artifact contract](contracts/artifact-and-profile.md) |
| Operator configuration, budgets and limits | [Configuration contract](contracts/configuration.md) |
| Advisory locks included in F1 | [Lock contract](contracts/locks.md) and [ADR-0017](architecture-decision-records/0017-advisory-named-locks.md) |
| Acceptance scenarios and requirement-to-case mapping | [Verification](verification.md) and [Traceability](traceability.md) |
| Development guidance and actual check commands | [Implementation prompt](handoffs/implementation-prompt.md), repository Makefile and TESTING.md |
| Operator recovery and service procedures | [Operations](operations.md) and [TASK-004 supervision record](records/TASK-004-macos-supervision.md) |
| Current user-facing behavior | Repository-root README.md |
| Existing future-work candidates | [TODO index](TODO.md); recheck relevant entries at their named task boundary before adoption |

Use the [ADR index](architecture-decision-records/README.md) for rationale, especially ADR-0002, ADR-0004, ADR-0005, ADR-0011, ADR-0012, ADR-0016, ADR-0017 and ADR-0018. Contracts and requirements own behavior; ADRs do not replace acceptance. This dossier does not redefine an authority when the implementation changes: promote the change to its owner and update the integration map.

## Prerequisites and order

The following committed prerequisites form the planning basis. Their current successful lifecycle and required evidence must be rechecked in the canonical roadmap and Git before the dependent task starts; these source references are not new acceptance claims.

| Pre-epic prerequisite | Committed source | Consumed outcome and evidence boundary |
| --- | --- | --- |
| TASK-033 | `eb9df357277788e0d2665504cf29f05105571bb9` | Existing production-path local admission, execution and observation, including subsequent accepted corrections |
| TASK-007 | `7ab3b90896813994bf0026d558f5784f113eb45f` | Fenced resource/global scheduling, retained occupancy and nested guards |
| TASK-004 | `2a3894f99f51436d3cd2eda843f4fc6c79d2f683` | Qualified host supervision decisions and their recorded limits; not product service reconciliation |
| TASK-035 | `de31633e9b3614d068ad4e5c62e8b6dd08ee2d4b` | Implemented advisory lock contract, included in F1 without managed occupancy |

| Member task | Actual prerequisites | Next member |
| --- | --- | --- |
| [TASK-009](roadmap/EPIC-003.md#task-009) | TASK-033, TASK-007, TASK-004 | TASK-010 |
| [TASK-010](roadmap/EPIC-003.md#task-010) | TASK-009 | TASK-011 |
| [TASK-011](roadmap/EPIC-003.md#task-011) | TASK-010, TASK-004 | TASK-012 |
| [TASK-012](roadmap/EPIC-003.md#task-012) | TASK-010, TASK-011, TASK-035 | Whole-epic audit and F1 handoff |

The member DAG has no cycle. An unfinished member determines order; it is not an additional pre-epic blocker. There is no foreign-repository prerequisite for this epic. Missing real UTM conditions do not block the independent local core; the roadmap retains the later hardware gates.

## Task outcome and verification map

This map supplements, without narrowing, every Implement, Do not, Verify and Done when criterion in the [epic checklist](roadmap/EPIC-003.md).

| Task / requirements | Required outcome and seams | Core acceptance cases |
| --- | --- | --- |
| TASK-009 / HP-REQ-016, HP-REQ-017 | Independent local runner; durable run claim and request/spawn/start/exit receipts; explicit argv/environment; identity-bound process-group supervision, streaming output and bounded termination/pipe cleanup | V10, V11, V13, V44, V46; fork/output-heavy/inherited-pipe fixtures, supervisor death, PID/boot identity and ambiguous spawn |
| TASK-010 / HP-REQ-018, HP-REQ-019 | Common prepare/run/collect/cleanup/verify and terminal-verdict path; independent budgets; effect classification; safe capacity return or reconciliation/quarantine | V14, V19, V20, V21, V40, V51, V53; partial phase failures, preparation/ready cancellation, deadlines, late cancellation and terminal-commit replay |
| TASK-011 / HP-REQ-020, HP-REQ-021 | Reattach proven runs and receipts; resume durable cleaning/settling; revision-bound recovery with provenance; generic adapter-intent/evidence recovery; isolated development LaunchAgent | V10, V11, V22, V42, V46; restart around spawn/exit receipts, stale plans, conflicting observations, unidentified descendants and actual service restart with a surviving runner |
| TASK-012 / HP-REQ-022, HP-REQ-023, HP-REQ-062/F1 | Bounded streaming delivery, size/digest manifests, exact offsets and explicit export; protected-evidence references and safe retention/purge; managed core freeze | V05, V20, V21, V43, V53; missing/partial/corrupt collection, disk exhaustion, unproven collector, protected guest-evidence restart/disposal and purge/retry; carry TASK-035 lock acceptance into F1 |

Common guest-evidence and power-transition cases may use the roadmap-authorized bounded fake backend before real VM qualification. Those observations do not qualify UTM or satisfy F2.

## Cross-task acceptance and failure paths

- Commit the run claim and spawn intent before workload effects. Import only receipts with the exact run/resource/lease/nonce and boot/process identity. Ambiguous spawning or an unidentifiable descendant cannot authorize another start or signaling a historical bare PID.
- Preserve occupancy through every possible effect and unproven termination. Attempt bounded collection and cleanup after effectful failure. Keep workload outcome, evidence completeness, cleanup outcome and final verdict separate; derive terminal verdicts through the authoritative decision table.
- Queued cancellation changes no occupancy. A reserved proven no-effect Job returns one `(-1,-1)` through atomic result/lease/capacity/event settlement with `cleanup_outcome=not_needed`; uncertain process or power effects cannot use it. Preserve pause/quarantine. The VM-local prerequisite path binds its pause receipt and successor resource revision before settlement, including restart between those commits.
- Startup reconciles before dispatch. Proven runners are reattached; unresolved Jobs stay reconciling and occupied. Independent resources progress only when their readiness and remaining global capacity permit it. Job recovery precedes resource recovery; occupied resource recovery returns `RECOVERY_BLOCKED`.
- Protect required recoverable evidence before cleanup can delete it. Proven termination may return Job occupancy while the resource remains quarantined; unproven workload or collector termination retains occupancy. Re-collection adds supplementary evidence without rewriting the terminal result. Explicit disposal names its evidence loss in a current revision-bound plan.
- Retention never deletes active/foreign paths, unresolved evidence/recovery references or permanent request-key identity. Same-key replay after purge cannot launch a new workload. Keep cancellation replay identity and observer cursor ownership safe through retention.
- Demonstrate the integrated local path with success, a relevant failure, restart/recovery and same-key observation, then audit every member criterion and integration seam. No compilation, mock, exit code or unchecked manual claim substitutes for its required evidence.

## Verification and authority boundaries

Select focused unit/integration and local CLI checks from the executable Makefile and TESTING.md, then run the required common gate on the exact candidate. `make test` runs prepare, unit, integration and local E2E; prepare formats Go source, so isolate and preserve unrelated work first. Documentation changes require `python3 docs/tools/validate_docs.py --write` followed by the ordinary validator and diff integrity checks.

TASK-011 needs a separately authorized, isolated development LaunchAgent in the logged-in Aqua `gui/<uid>` domain, with `LimitLoadToSessionType=Aqua` and `AbandonProcessGroup=true`. Observe the actual product runner in its own session across controller exit/restart. The TASK-004 helper probe alone is insufficient. A missing required environment or unproven cleanup leaves acceptance incomplete.

Implementation, check execution, selected-route review, exact-candidate acceptance, staging, commits, service installation and publication are separate authorities. A later approved epic envelope selects the review route, bounded source transmission, required checks, managed sessions and planned member-task commit boundaries. This dossier grants none of those effects. Real host tool installation, enabling services, VM operation, provider calls, Sorage messages, legacy removal and publication require their applicable authority.

Keep check/reviewer provenance, exact command/target and local workflow identities in native runtime evidence and bounded handoffs. Do not copy provider reports, transcripts, runtime paths or session/run IDs here. Promote durable evidence only when downstream correctness requires it and the approved evidence-residency contract permits it. The repository has no declared release-note owner.

## Documentation promotion and downstream handoffs

| Owner | Durable information and downstream boundary |
| --- | --- |
| TASK-009 | Runner/receipt and process-identity semantics to resource-and-runner and architecture; implemented commands to CLI contract and root README; focused check guidance to TESTING.md as needed. TASK-014 consumes the exact runner boundary. |
| TASK-010 | Phase/effect/verdict/capacity semantics to managed-transitions and affected contracts; failure/recovery guidance to operations. TASK-015 and TASK-019 consume the common settlement path without an invented cleanup guarantee. |
| TASK-011 | Reconciliation and recovery commands to architecture, CLI contract and operations; development service and actual observation boundary to current operating/check guidance. TASK-015 consumes generic recovery; TASK-016 uses the development service. |
| TASK-012 | Artifact/retention behavior to artifact and CLI contracts and operations; record all managed core changes since TASK-002 and F1's frozen contract/decision in their canonical owners. TASK-015 consumes evidence protection and TASK-017 builds on F1. |

F1 covers Job, resource, result, event and envelope schemas; lifecycle/outcome enums; error codes and CLI exits; canonicalization, cursor semantics, machine-readable help and TASK-035's advisory locks. Manual result format remains for F3; VM fields remain for F2. Changes after a freeze need the roadmap-required recorded contract decision and schema version change.

Each member handoff identifies its exact candidate/commit when authorized, actual checks and environment, criterion coverage, remaining gaps and next canonical task. Preserve existing future-work entries unless their named concern has been checked and resolved or deliberately adopted under the approved scope. No external producer request is sent by this epic.

## Dossier maintenance and closeout

Maintain only the execution decisions, owner links, current integration constraints and actionable handoffs needed by EPIC-003. Put accepted product behavior, architecture, rationale, usage and operating guidance in their canonical owners; Git preserves implementation history. The roadmap alone records lifecycle transitions.

After successful whole-epic validation, inventory every canonical reference and consumer of this exact dossier. Replace EPIC-003's Detailed SOT links with valid Canonical Outcomes links and remove its adopted TODO entry only at the final consumer closeout. Retain the dossier while another non-terminal consumer or canonical reference remains. Delete it only under explicit dossier-deletion authority in the epic closeout envelope, then synchronize the documentation counts and generated projections.

The creation of this dossier leaves all Epic/Task lifecycle states and acceptance checkboxes unchanged. Its next handoff is the dossier-based EPIC-003 execution plan and approval envelope.
