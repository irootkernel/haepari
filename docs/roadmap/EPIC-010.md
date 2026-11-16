# EPIC-010: Home separation and installation-preserving relocation

Prepare Haepari v0.1.1 to separate its own development controller from the shared installation and relocate that shared installation without losing identity or history.

Identity, lifecycle status, order and actual prerequisites belong to the [canonical roadmap](../roadmap.md). Detailed SOT: [Home separation and installation-preserving relocation](../TODO-HOME-SEPARATION-AND-RELOCATION.md). Unchecked boxes remain acceptance obligations, not execution claims. TASK-040 defines the selected [home and relocation contract](../contracts/home-and-relocation.md), HP-REQ-082–087 and V66–V72 without changing historical traceability or accepted evidence.

## TASK-040

### Define home roles and the relocation contract

Requirement coverage: HP-REQ-082, HP-REQ-083, HP-REQ-084, HP-REQ-085, HP-REQ-086, HP-REQ-087.

Why: release services currently reject the native default home, while the shared controller uses a dev-named root. Moving a directory alone does not resolve ownership, immutable records or consumer references.

#### Implement

- [x] Record the selected home policy, preservation guarantees, version treatment and unsupported states in an ADR and the affected canonical contracts.
- [x] Define release-service plan v2 and relocation plan/receipt v1, exact ownership and binary fences, durable recovery, refusal semantics and rollback boundaries.
- [x] Reconcile requirement and verification mappings before implementation; preserve historical contract bytes and accepted evidence.

#### Do not

- [x] Do not call a planned interface implemented, authorize host cutover, or rewrite historical results.

#### Verify

- [x] Resolve the contract against current service, installation, runner, evidence and recovery authorities and the Detailed SOT acceptance matrix.

#### Done when

- [x] The contract, compatibility treatment, migration limitations and Task-owned acceptance evidence are decision complete and reviewed.

## TASK-041

### Establish the isolated development home

Requirement coverage: HP-REQ-082.

#### Implement

- [x] Prepare the independent development home, immutable candidate binaries and exact saved service plans using the existing native development lifecycle.
- [x] Make that home usable for subsequent EPIC-010 development without changing the current shared controller or consumer configuration.
- [x] Document drained candidate replacement and the single-VM ownership handover procedure.

#### Do not

- [x] Do not copy the shared database, replace a running candidate file, change global shell defaults, or let two controllers operate the same VM concurrently.

#### Verify

- [x] Observe separate installation/socket/state identities, a harmless local Job and exact owned service replacement, preserving shared-state sentinels. Use the Task's separately authorized development-service envelope.

#### Done when

- [x] The development home is usable and its isolation and replacement procedure have original evidence. Actual setup is future Task work, not part of this registration.

## TASK-042

### Support the default home in release services

Requirement coverage: HP-REQ-083.

#### Implement

- [x] Allow the native default home in new release-service plans while retaining development-home isolation, private-path checks and ownership fences.
- [x] Preserve non-default release v1 and development v1/v2 behavior; implement the selected release v2 contract and affected guidance.

#### Verify

- [x] In a disposable HOME, verify default/non-default planning, install/uninstall, state preservation, development-default refusal, conflicting ownership, changed binary and mismatched plist/plan refusals.

#### Done when

- [x] The supported default-root lifecycle and legacy compatibility pass focused checks without touching the host shared installation.

## TASK-043

### Implement installation-preserving relocation and rollback

Requirement coverage: HP-REQ-084, HP-REQ-085.

#### Implement

- [ ] Deliver the planned native plan/apply/status/rollback surface, offline exclusive ownership, verified backup, no-replace same-filesystem rename and durable recovery journal.
- [ ] Preserve installation/Job/request identities, resource configuration, immutable results, receipt bindings and retained evidence; update only the authorized mutable service-root metadata.

#### Do not

- [ ] Do not merge roots, use a copy fallback, edit historical SQLite payloads or runner requests, discard unresolved evidence, start services automatically, or rewrite consumer configuration.

#### Verify

- [ ] Verify same-plan continuation and rollback after interruption, original file identities and history, and refusal of busy, uncertain, conflicting, replaced or unsupported inputs.

#### Done when

- [ ] Relocation and recovery obey the complete dossier contract and preserve query/export/replay behavior without duplicate workload execution.

## TASK-044

### Qualify preservation, recovery and release packaging

Requirement coverage: HP-REQ-086.

#### Implement

- [ ] Integrate the selected preservation, crash-boundary and refusal fixtures into the existing test layers and exercise the packaged binary outside its checkout.
- [ ] Finish affected operator, installation and paired-skill documentation before candidate review.

#### Verify

- [ ] Compare immutable history and evidence before/after relocation, including deliberate FAIL/INCOMPLETE and missing-evidence cases; run the focused and required common gates.
- [ ] Qualify the exact package's small already-running single-VM release loop under its own execution envelope. Do not repeat the historical full campaign or introduce VM power experiments.

#### Done when

- [ ] Package, relocation/recovery, compatibility and required local/VM observations are bound to the exact candidate, with remaining limits explicit.

## TASK-045

### Prepare Aquarium adoption evidence and the cutover handoff

Requirement coverage: HP-REQ-087.

#### Implement

- [ ] Verify the candidate against the current Aquarium consumer contract and rehearse the preserved-installation upgrade/relocation with disposable state.
- [ ] Prepare the producer packet, immutable identities, combination-verification requirements, external-reference inventory and release-time cutover/rollback runbook for Aquarium v0.1.24 adoption.
- [ ] Separate candidate evidence from the official artifact/combination checks still due after publication. Identify any consumer change as Aquarium-owned work.

#### Do not

- [ ] Do not relocate the shared host, change global MCP or consumer settings, send a message without instruction, or claim Aquarium acceptance/publication from producer evidence.

#### Done when

- [ ] Compatibility, rehearsal and a complete cutover handoff are ready before Haepari v0.1.1 publication. Actual host cutover is not a Task or Epic completion criterion.

## Epic acceptance

- [ ] All six Tasks meet their criteria with original, applicable evidence and affected documentation complete.
- [ ] The development home is usable independently; the current shared installation is preserved.
- [ ] The installation-preserving relocation capability is qualified and its unsupported cases refuse safely.
- [ ] Aquarium v0.1.24 adoption has a complete handoff and an explicit later operational owner; no host migration is claimed.
- [ ] Final Epic validation and Master's overall acceptance are recorded through the canonical roadmap. Commit, release and installation remain separate effects.
