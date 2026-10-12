# Aquarium adoption handoff copy

The actual Aquarium repository is the authority for this rollout. Its planned Aquarium:EPIC-019 and Aquarium:TASK-086 through Aquarium:TASK-102 are defined in `docs/roadmap/README.md` and detailed in `docs/todo/TODO-HAEPARI-VM-ADOPTION.md`. This package copy is a portable briefing, not a second lifecycle database.

Design Revision 3.5 boundary: the broader product skill/installation/migration policies below are consumer-owned rollout guidance, not Haepari v0.1.0 producer prerequisites. Haepari supplies generic automatic/manual execution and evidence; each consumer decides its native recipe acceptance. This copy changes no external repository or rollout authority.

## Objective and external dependency

After Haepari's independent v0.1.0 handoff is verified, adopt project-owned VM installation, skill, hands-on, migration and regression tests. Aquarium is the first implementation. Then Aquarium sends one explicitly authorized Sorage request to each project in order and verifies the returned implementation evidence. Aquarium implements no foreign repository as part of these coordination tasks.

The dependency is Haepari's exact installed-release/capability/runner/skill handoff, not a claim that planning documents exist. Haepari must qualify its local and UTM backends without requiring Aquarium adoption first. The initial Aquarium epic remains Blocked on that external implementation. The default is a verified official Haepari release; an explicitly selected exact candidate needs the same capability/artifact evidence and retains its candidate evidence class.

## Proposed sequence (managed-first, pending Aquarium adoption)

This table describes the sequence that Haepari's managed-first scope needs. It is not yet Aquarium's plan. Aquarium's uncommitted Aquarium:EPIC-019 plan still requires the R2 external lease capabilities. The required Aquarium changes, with exact locations, are in [external requests](to-external.md) §1.1 and §1.2; this package does not apply them.

| Aquarium task | Delivery |
| --- | --- |
| Aquarium:TASK-086 | Accept Haepari's exact managed local/UTM capabilities and evidence; external leases no longer block v0.1.0 acceptance. |
| Aquarium:TASK-087 | Define profile, complete skill/configuration inventory, VM preparation and migration contracts. |
| Aquarium:TASK-088 | Implement Aquarium-owned `make test-vm` and guest installation/migration/regression suites. |
| Aquarium:TASK-089 | Implement manual/installed-skill evidence and a separate VM release gate. |
| Aquarium:TASK-090 | Qualify Aquarium's pilot and freeze managed-first producer request/return packets. |
| Aquarium:TASK-091 | Request and accept Podway adoption through Sorage. |
| Aquarium:TASK-092 | Request and accept Mulgae managed VM, installed skill, migration, regression and legacy-detachment adoption through Sorage; ordinary-review external leases are not mandatory. |
| Aquarium:TASK-093 | Request and accept Gaori adoption through Sorage. |
| Aquarium:TASK-094 | Request and accept Sorage adoption through the stable host broker. |
| Aquarium:TASK-095 | Request and accept Sanho adoption through Sorage. |
| Aquarium:TASK-096 | Request and accept Dolgorae adoption through Sorage. |
| Aquarium:TASK-097 | Request and accept Gul adoption through Sorage. |
| Aquarium:TASK-098 | Request and accept Seongge adoption through Sorage. |
| Aquarium:TASK-099 | Request and accept Gaebokchi adoption through Sorage. |
| Aquarium:TASK-100 | Reconcile the full managed VM combination and cross-product upgrade/regression evidence; deferred external execution is not required. |
| Aquarium:TASK-101 | Retire shared Aquarium development-channel source and approved host runtime after every producer is detached. |
| Aquarium:TASK-102 | Cold-validate the final arrangement, promote documentation and complete Aquarium closeout. |

Each task depends on its preceding task; Aquarium:TASK-086 additionally requires the Haepari handoff. A producer implementation is an external dependency of its corresponding Aquarium task. Do not send the next producer's request until the preceding producer's returned work has been accepted against the common contract.

## Installation and evidence contract

Every project owns its test recipes, host wrapper, guest entrypoint, installation-unit declaration, migration fixtures and manual checklists. The queue owns UTM/SSH and resource lifecycle. Normal development uses an accepted fixed profile with the target component overridden, on persistent VMs that Haepari never resets. Each job returns its product to the stable version when it finishes. Aquarium's final release gate uses the complete intended fixed combination.

Full skill trees, location/native pin rules, external global skills, rules, effective CLI/MCP/plugin configuration and fresh-session discovery are covered. Skill-only changes are valid candidates. Installation correctness and observed behavior are distinct checks. Actual source versions are resolved during execution; none are invented here.

Migration creates synthetic state using the exact prior supported version, checks it before update, applies the candidate without resetting that state, verifies preserved data/configuration/skills and post-upgrade read/write/restart behavior, and tests declared interruption and busy-work policies. Clean installation, migration, regression and manual results are separately identified.

Current Aquarium `release-qa` remains an offline disposable-scenario contract. The new installed VM gate is a separate explicitly authorized release step with results bound to the same candidate and environment. Do not call an existing automated test a replacement for the existing release-delta assessment or silently broaden global/live authority.

## Sorage exchange

The source and destination project identities must be resolved through the current native Sorage contract before sending. Do not guess slugs from product names. A request has a stable logical correlation key and immutable request-document digest; the actual broker receipt and current revision are read from Sorage.

The request asks the producer to allocate its own local roadmap IDs and implement its own source. The producer may accept the request or ask for clarification using native review semantics. That acknowledgement is not completion. Once work is implemented and verified, the producer sends a separate completion-report Handoff back to Aquarium, naming the original request and exact evidence.

Aquarium reads that current returned document, reviews candidate/artifact/skill/profile identities and actual test/migration results, requests corrections through native review/revision when needed, and accepts the response only when the task's acceptance is met. An uncertain send is reconciled against native receipts before any resend. Do not edit the Vault, query broker storage directly, or run an unrelated inbox poller. Raw IDs, paths and transcripts remain runtime evidence unless the repository's evidence-residency contract permits a reviewed bounded promotion.

The included request files are drafts, not sent messages. This planning work authorizes no broker operation.

## Legacy retirement

Aquarium:EPIC-002's unfinished legacy expansion is withdrawn in the Aquarium plan while its completed history is preserved. The old detailed dossier remains temporarily available as a reference until final retirement. Planning cancellation does not remove a running service or installed file.

Each producer inventories actual enrollment/hook/service use and retires its own obsolete integration only after its new path passes and the corresponding host change is approved. Detach owned post-commit markers before deleting targets they invoke. Stop/drain producer development processes before deleting their selected artifacts. Preserve stable tools, generic developer capabilities and native use-* skills.

Aquarium retains the minimal old manager/launcher until all producers are detached. Only then remove shared CLI/MCP/setup/status dependencies and explicitly approved host state. The final `~/.aquarium-dev/` deletion is separate from source cleanup and requires a current ownership/process/reference check plus the chosen preservation policy. Keep production ~/.aquarium state, active Codex homes, stable native skills, Sorage Vault and unrelated hooks untouched.

## Completion receipt requirements

The returned report identifies the producer repository and its own work IDs; source commit or exact development snapshot; candidate archives/executables and skill-tree identities; profile and test identities; Haepari release and Job references; install/migration/regression/manual outcomes; cleanup results and inventories; legacy source changes; host detachment status; and outstanding gaps. An acknowledgement, commit list or producer prose saying done is insufficient without those observations.

A producer with no previous release or no shipped skill uses an evidence-backed not_applicable declaration for that dimension. It cannot omit the overall install, state-persistence, guest ownership and cleanup contract.

## Planning validation note

The R2 planning record reported that the Aquarium documentation inspector recognized the new IDs and references but returned `epic_lifecycle_unverifiable` for the roadmap-permitted `Cancelled` state on Aquarium:EPIC-002. Aquarium:TASK-101 currently includes reconciling this objective lifecycle check before final closeout; [external requests](to-external.md) §1.3 recommends fixing it now instead. That historical observation has not been rechecked in this task. This documentation revision does not modify the inspector implementation.

## Aquarium follow-up

The required Aquarium changes and the advisory proposals are listed in [external requests](to-external.md) §1:

- **Required:** alignment of Aquarium:EPIC-019 with the managed-first scope and the persistent-VM model. The planning baseline is already committed (Aquarium `910ef29`).
- **Recommended:** fixing the inspector warning now.
- **Advisory:** rollout scope, legacy-retirement timing and pilot value metrics.

This package does not apply them. The execution order stays:

1. Aquarium pilot
2. Podway, Mulgae, Gaori, Sorage, Sanho, Dolgorae, Gul, Seongge, Gaebokchi
3. Aquarium reconciliation and closeout

Aquarium may change that order by adopting the advisory rollout proposal. Mulgae retains native partial-role recovery, exact rerun lineage and composition. Use qualified cross-project IDs, such as Haepari:TASK-011 and Aquarium:TASK-092.
