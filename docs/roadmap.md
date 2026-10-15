# Haepari roadmap

Target: v0.1.0, Design Revision 3.5. This file alone owns Haepari task identity, lifecycle status, recommended sequence and actual prerequisites. The tables below record current task and epic lifecycle states. The TASK-001 bootstrap is not a managed Job implementation or release acceptance.

## Execution policy

The task table gives the recommended epic-handler order and, separately, each task's actual implementation prerequisites. Within an epic, use task order. A task may start earlier than its epic when its actual prerequisites are met; TASK-034 ran this way, before TASK-001. A task is done only with its focused implementation, failure evidence, documentation and review. Missing evidence required by that task blocks its acceptance; accepted historical evidence is not reopened by a temporarily unavailable VM. When an epic waits only on a hardware gate, the handler may continue with later tasks whose actual prerequisites are met; the waiting epic is accepted after its gated task is. Commit and release authority are separate.

Lifecycle status vocabulary:

| Status | Meaning |
| --- | --- |
| Planned | In scope, not started. |
| In progress | Implementation has started. |
| Blocked | Waiting on a recorded external or real-environment prerequisite. |
| In review | Implementation and evidence are submitted for review. |
| Accepted | Reviewed acceptance evidence is recorded; only this file records it. |
| Deferred | Outside the current release; re-entry needs a recorded decision. |
| Cancelled | Withdrawn; history is retained. |

`Scope` separately says whether a task counts toward v0.1.0 (`Active`) or not (`Deferred`).

## Local development scope (Design Revision 3.5)

[ADR-0020](architecture-decision-records/0020-local-development-scope.md) narrows v0.1.0 to one machine, one user and one already-running registered VM. TASK-017–020 keep their IDs/prerequisites with generic project/manual responsibilities. Product-specific skill and migration requirements are consumer-owned optional scenarios. Existing F1/F2 contracts, runtime recovery and Accepted status are preserved; F3 is narrowed before its acceptance. The mandatory release matrix uses bounded local failure fixtures plus a small exact-candidate real-VM Job loop, not another full TASK-038 campaign. VM power/reboot/logout/DHCP-expiry/multi-day/two-VM experiments and performance benchmarks do not block release. Ordinary same-scope authorized development checks do not require a new approval per Job or binary digest.

## Epic sequence

| Epic | Goal | Tasks | Scope | Status |
| --- | --- | --- | --- | --- |
| [EPIC-001](roadmap/EPIC-001.md) | Contract, first local slice and OS decisions | TASK-001 through TASK-004 plus TASK-033 | Active | Accepted |
| [EPIC-002](roadmap/EPIC-002.md) | Durable queue, ownership and advisory locks | TASK-005 through TASK-008 plus TASK-035 | Active | Accepted |
| [EPIC-003](roadmap/EPIC-003.md) | Local execution, recovery and managed core freeze | TASK-009 through TASK-012 | Active | Accepted |
| [EPIC-004](roadmap/EPIC-004.md) | UTM macOS and SSH | TASK-034 plus TASK-013 through TASK-016 | Active | Accepted |
| [EPIC-009](roadmap/EPIC-009.md) | VM operational readiness before consumer integration | TASK-036 through TASK-039 | Active | Accepted |
| [EPIC-005](roadmap/EPIC-005.md) | Generic project and manual test integration; consumer integration freeze | TASK-017 through TASK-020 | Active | Accepted |
| [EPIC-006](roadmap/EPIC-006.md) | Failure and platform hardening | TASK-021 through TASK-024 | Active | In progress |
| [EPIC-007](roadmap/EPIC-007.md) | Release and Aquarium handoff | TASK-025 through TASK-028 | Active | Planned |
| [EPIC-008](roadmap/EPIC-008.md) | Public external leases and linked segments | TASK-029 through TASK-032 | Deferred | Deferred |

EPIC-003 Canonical Outcomes: [managed core freeze](records/TASK-012-managed-core-freeze.md), [managed transitions](contracts/managed-transitions.md), [artifact delivery and retention](contracts/artifact-and-profile.md), and [operations](operations.md).

EPIC-009 Canonical Outcomes: [owned runner and collection contract](architecture-decision-records/0019-owned-runner-transport.md), [core VM operating observations](records/TASK-038-operating-campaign.md), and [small caller e2e and TASK-017 handoff](records/TASK-039-small-e2e.md). The [shared feedback dossier](TODO-VM-OPERATIONAL-READINESS.md#feedback-disposition) remains for active EPIC-006/007 consumers. Epic/task identity does not encode execution order; existing IDs and accepted history are preserved.

## Recommended task order and actual prerequisites

| Task | Title | Actual prerequisites | Scope | Status |
| --- | --- | --- | --- | --- |
| [TASK-001](roadmap/EPIC-001.md#task-001) | Bootstrap the independent Go product | None | Active | Accepted |
| [TASK-002](roadmap/EPIC-001.md#task-002) | Define provisional managed schemas and state invariants | TASK-001 | Active | Accepted |
| [TASK-003](roadmap/EPIC-001.md#task-003) | Implement contained snapshots and identity | TASK-002 | Active | Accepted |
| [TASK-033](roadmap/EPIC-001.md#task-033) | Deliver the first production-path local walking skeleton | TASK-001, TASK-002, TASK-003 | Active | Accepted |
| [TASK-004](roadmap/EPIC-001.md#task-004) | Qualify general macOS OS supervision | TASK-001 | Active | Accepted |
| [TASK-034](roadmap/EPIC-004.md#task-034) | Probe UTM host automation non-destructively | None | Active | Accepted |
| [TASK-005](roadmap/EPIC-002.md#task-005) | Implement durable storage and controller ownership | TASK-033, TASK-004 | Active | Accepted |
| [TASK-006](roadmap/EPIC-002.md#task-006) | Implement admission and durable deduplication | TASK-005, TASK-003 | Active | Accepted |
| [TASK-007](roadmap/EPIC-002.md#task-007) | Implement resource scheduling and fenced leases | TASK-006 | Active | Accepted |
| [TASK-008](roadmap/EPIC-002.md#task-008) | Implement client waits, events and cancellation admission | TASK-007 | Active | Accepted |
| [TASK-035](roadmap/EPIC-002.md#task-035) | Implement advisory named locks | TASK-008 | Active | Accepted |
| [TASK-009](roadmap/EPIC-003.md#task-009) | Implement the local per-job supervisor | TASK-033, TASK-007, TASK-004 | Active | Accepted |
| [TASK-010](roadmap/EPIC-003.md#task-010) | Implement phase settlement and safe resource return | TASK-009 | Active | Accepted |
| [TASK-011](roadmap/EPIC-003.md#task-011) | Implement crash reconciliation, explicit recovery and the development service | TASK-010, TASK-004 | Active | Accepted |
| [TASK-012](roadmap/EPIC-003.md#task-012) | Implement artifact delivery and retention; freeze the managed core contract | TASK-010, TASK-011, TASK-035 | Active | Accepted |
| [TASK-013](roadmap/EPIC-004.md#task-013) | Implement persistent VM registration and UTM control | TASK-007, TASK-003 | Active | Accepted |
| [TASK-014](roadmap/EPIC-004.md#task-014) | Implement authenticated SSH and guest runner continuity | TASK-013, TASK-009 | Active | Accepted |
| [TASK-015](roadmap/EPIC-004.md#task-015) | Integrate VM collection, job cleanup and recovery | TASK-014, TASK-010, TASK-011, TASK-012 | Active | Accepted |
| [TASK-016](roadmap/EPIC-004.md#task-016) | Qualify the real macOS VM backend; freeze VM contract fields | TASK-015, TASK-034 | Active | Accepted |
| [TASK-036](roadmap/EPIC-009.md#task-036) | Restore recoverable VM failure handling | TASK-012, TASK-015 | Active | Accepted |
| [TASK-037](roadmap/EPIC-009.md#task-037) | Bound VM startup, transport, and retained runner storage | TASK-036 | Active | Accepted |
| [TASK-038](roadmap/EPIC-009.md#task-038) | Publish and execute repeatable real-VM qualification | TASK-016, TASK-037 | Active | Accepted |
| [TASK-039](roadmap/EPIC-009.md#task-039) | Demonstrate a small isolated e2e caller cycle | TASK-038 | Active | Accepted |
| [TASK-017](roadmap/EPIC-005.md#task-017) | Publish the project Make and guest integration contract | TASK-008, TASK-012, TASK-015, TASK-039 | Active | Accepted |
| [TASK-018](roadmap/EPIC-005.md#task-018) | Implement generic candidate and environment input helpers | TASK-017 | Active | Accepted |
| [TASK-019](roadmap/EPIC-005.md#task-019) | Implement general manual jobs and recorded observations | TASK-018, TASK-010 | Active | Accepted |
| [TASK-020](roadmap/EPIC-005.md#task-020) | Validate the generic consumer loop; freeze the integration contract | TASK-018, TASK-019 | Active | Accepted |
| [TASK-021](roadmap/EPIC-006.md#task-021) | Complete concurrency and crash qualification | TASK-011, TASK-019, TASK-035 | Active | Accepted |
| [TASK-022](roadmap/EPIC-006.md#task-022) | Harden trust, privacy and external-effect boundaries | TASK-021 | Active | Planned |
| [TASK-023](roadmap/EPIC-006.md#task-023) | Measure bounds and qualify supported platforms | TASK-021 | Active | Planned |
| [TASK-024](roadmap/EPIC-006.md#task-024) | Qualify storage compatibility and self-test isolation | TASK-005, TASK-021 | Active | Planned |
| [TASK-025](roadmap/EPIC-007.md#task-025) | Package the release, user services and paired skill | TASK-020, TASK-022, TASK-023, TASK-024 | Active | Planned |
| [TASK-026](roadmap/EPIC-007.md#task-026) | Finalize operator and developer documentation | TASK-025 | Active | Planned |
| [TASK-027](roadmap/EPIC-007.md#task-027) | Run exact release acceptance | TASK-016, TASK-019, TASK-020, TASK-025, TASK-026 | Active | Planned |
| [TASK-028](roadmap/EPIC-007.md#task-028) | Deliver the Aquarium consumer handoff | TASK-027 | Active | Planned |
| [TASK-029](roadmap/EPIC-008.md#task-029) | Implement external admission, activation and dispatch claims | Post-v0.1.0 re-entry; not an active predecessor | Deferred | Deferred |
| [TASK-030](roadmap/EPIC-008.md#task-030) | Implement native execution binding, release and reconciliation | Post-v0.1.0 re-entry; not an active predecessor | Deferred | Deferred |
| [TASK-031](roadmap/EPIC-008.md#task-031) | Implement linked segments and single-owner continuation | Post-v0.1.0 re-entry; not an active predecessor | Deferred | Deferred |
| [TASK-032](roadmap/EPIC-008.md#task-032) | Qualify external-client recovery and consumer contracts | Post-v0.1.0 re-entry; not an active predecessor | Deferred | Deferred |

Every active task other than TASK-028 is an actual prerequisite of a later active task, so release acceptance cannot bypass it.

## Contract freeze gates

TASK-002 defines provisional schemas; it does not freeze them. Three gates freeze the consumer contract, each after the evidence it depends on:

- **F1, managed core contract (TASK-012).** Covers job, resource, result, event and envelope schemas; lifecycle/outcome enums; error codes; CLI exit codes; canonicalization; cursor semantics; and the advisory lock contract. It freezes after local failure and crash evidence (TASK-010, TASK-011, TASK-012), as defined in the [F1 decision record](records/TASK-012-managed-core-freeze.md); canonical TASK-012 acceptance makes that decision effective. TASK-017 builds the project Make contract on F1.
- **F2, VM contract fields (TASK-016).** Covers `utm-macos` resource fields, guest runner protocol, host-session identity fields and the VM-specific job and result fields. The [F2 decision record](records/TASK-016-vm-contract-freeze.md) defines the candidate inventory and qualified scope, including the owner-approved later checks in [deferred feedback](deferred-feedback.md). Canonical TASK-016 acceptance makes the freeze effective.
- **F3, consumer integration contract (TASK-020).** Covers the project Make and guest entrypoint conventions, immutable consumer environment references and the general manual result format. The [F3 decision record](records/TASK-020-consumer-freeze.md) defines the implemented surface; canonical TASK-020 acceptance makes that decision effective after TASK-017 through TASK-020. Rich installation locks, installed-skill evaluation and migration evidence are optional consumer material outside F3.

F3 follows the real-VM qualification and isolated caller cycle in TASK-038–039 through TASK-017. EPIC-009 must resolve its affected F1/F2 contract decisions and version changes before implementing the new semantics; it neither reopens historical acceptance by implication nor grants a blanket additive-change waiver. TASK-020 uses the pilot lessons before freezing the consumer integration contract.

After a gate, a change needs a recorded contract decision and a schema version change. TASK-025 packages capabilities after F3; TASK-027 re-verifies all three gates; TASK-028 hands off only frozen contracts.

## Hardware and environment gates

Historical real-UTM qualification tasks: TASK-016, TASK-038. These Accepted records retain their candidate-bound backend and operating observations. TASK-034's Accepted host probe and TASK-039's Accepted native local caller cycle also remain historical; TASK-039 is not real-VM evidence.

Current real-VM acceptance tasks: TASK-027. It observes the exact packaged candidate's small already-running single-VM auto/manual loop. Current real-VM prerequisite closure: TASK-027, TASK-028. Missing that new observation blocks release acceptance and the subsequent final producer handoff, not independent local development. TASK-028 may prepare its handoff locally but cannot deliver an accepted release before TASK-027.

TASK-017–026 implement and verify their changed behavior locally or with fake/native guest fixtures; their acceptance requires no new live campaign. The accepted VM prerequisites remain valid while a VM is temporarily unavailable. An optional TASK-014 smoke or selected `make test-utm` rerun needs its own actual observations and cannot substitute for TASK-027. No task ID, actual prerequisite or lifecycle status changes with this distinction.

TASK-004 needs a macOS host but no VM. TASK-011 implements the development LaunchAgent used for qualification; operating or replacing an exact service and registered VM still requires a bounded execution envelope. TASK-038 observes core Job execution, collected evidence, large logs and repeated reuse on one already-running registered VM. Master removed VM power-on/off, cold-start, guest-reboot and power-intent scenario qualification from EPIC-009 on 2026-10-12; these are no longer acceptance requirements and are not recorded as passed. The same-machine, same-user scope also excludes separate identity-refusal fault experiments and transport/network benchmarking. Reproducibility is the tracked small core e2e command, not an additional live VM campaign. The two-VM/global-three profile is deferred to a separately authorized campaign in [DF-004](deferred-feedback.md#df-004-two-vm-and-local-concurrency); it does not block TASK-038, TASK-039 or EPIC-009 acceptance. DF-001–004 are optional hardening outside v0.1.0 acceptance under ADR-0020, with uncertainty retained. Linux runtime, public external leases and an upgrade from a nonexistent public Haepari predecessor remain outside the release gate.

## Milestones and preservation

TASK-033 proves a production-path submit/wait/result/reconnect local slice inside EPIC-001. TASK-035 adds advisory named locks on the same store and wait path. EPIC-003 hardens its scheduling, supervision and recovery without a second scheduler and ends at freeze gate F1. TASK-034 probed UTM automation before bootstrap; EPIC-004 then qualifies the VM adapter independently of EPIC-008 and ends at freeze gate F2. EPIC-009 restores and measures the ordinary VM loop and proves a small isolated caller cycle before EPIC-005, which delivers generic automatic/manual integration and ends at freeze gate F3. EPIC-007 supplies the exact Aquarium handoff; the Aquarium-side prerequisites are listed in [external requests](to-external.md). EPIC-008, TASK-029–032 and E01–E20 retain their IDs and design history for post-v0.1.0 re-entry, subject to B1/B2 closure.

Totals: nine epics (eight active, one deferred); 39 tasks (35 active, four deferred).
