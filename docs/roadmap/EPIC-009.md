# EPIC-009: VM operational readiness

Make ordinary managed VM work usable before expanding project, skill and manual integration. A recoverable failure must have a bounded route back to observation, settlement and reuse; unknown execution must retain its fences and occupancy.

Identity, status, order and actual prerequisites belong to the [canonical roadmap](../roadmap.md). These unchecked criteria define planned work, not implementation or qualification.

Detailed SOT: [VM operational readiness execution dossier](../TODO-VM-OPERATIONAL-READINESS.md).

## TASK-036

### Restore recoverable VM failure handling

Requirement coverage: HP-REQ-018, HP-REQ-019, HP-REQ-020, HP-REQ-021, HP-REQ-022, HP-REQ-024, HP-REQ-025, HP-REQ-027, HP-REQ-028, HP-REQ-029, HP-REQ-030, HP-REQ-062.

Why: a failed read-only observation currently abandons the observer, startup reconciliation runs only once, and unresolved power intents prevent the Job and resource recovery paths from progressing. Missing outputs and changed guest boots also need explicit policy decisions rather than implicit extensions of the current contract.

#### Implement

- [x] Resolve dossier decisions D1–D3 with Master before changing the affected behavior. Record the F1/F2 delta, schema/version treatment and retained-history behavior in the owning contracts and freeze records.
- [x] Distinguish positively observed absence from existing uncollected evidence and transport/identity uncertainty. Preserve the native workload outcome and derive the verdict from the approved evidence/cleanup policy.
- [x] Retry safe observations with backoff inside their original deadlines; periodically reconcile eligible interrupted Jobs without another prepare/start, a new run identity or an extended budget.
- [x] Add a fenced way to resolve orphaned power intents and the approved same-resource reboot recovery path, including unsupported/unknown cases and stale observations.
- [x] Preserve bounded diagnostic causes through guest replies, controller failure handling and durable inspection; redact secrets and separate diagnostics from termination proof.

#### Do not

- [x] Do not infer termination from a failed watch, a missing PID or an SSH error; do not return unproven occupancy or retry an uncertain effect.
- [x] Do not silently discard existing required evidence, replace the original runner or rewrite historical terminal results.

#### Verify

- [x] Cover missing versus uncollected outputs, late cancellation, transient observation loss, original-run reattachment, changed boot, orphaned power intent, conflicting identity and stale recovery.
- [x] Prove one capacity return after valid settlement and retained occupancy while termination is unknown. Read-only retry must never duplicate the workload.
- [x] Run focused and common product checks under the repository test contract. These fixtures do not complete the real-VM qualification in TASK-038.

#### Done when

- [x] Decisions D1–D3 and their contract/version implications are resolved, implemented and reviewed, with unsupported cases stated.
- [x] Recoverable failures have bounded observation/recovery paths; uncertainty preserves the required ownership and evidence.
- [x] Changed contracts, diagnostics and operating guidance agree with the exact tested candidate.

## TASK-037

### Bound VM startup, transport, and retained runner storage

Requirement coverage: HP-REQ-011, HP-REQ-019, HP-REQ-022, HP-REQ-023, HP-REQ-024, HP-REQ-026, HP-REQ-027, HP-REQ-050, HP-REQ-051, HP-REQ-062.

Why: one immediate readiness check does not cover a cold VM boot. Each guest exchange currently transfers the complete executable, and each Job leaves a host runner copy outside the purge inventory. A fixed global limit of one also prevents the documented mixed-resource development profile.

#### Implement

- [ ] Poll started state, the valid registered-MAC lease and pinned guest readiness within the admitted prepare deadline; preserve host-wide denial and no-effect pause behavior.
- [ ] Measure the existing call/byte cost, then remove repeated complete-runner transfer using a digest-bound owned runner and bounded observation/collection operations. Record affected F2 decisions before changing the protocol.
- [ ] Include runner payloads in safe finished-Job retention/purge or verified cleanup. Active and unresolved recovery must retain the exact runner; old request keys and required receipts must remain usable.
- [ ] Expose a validated positive global execution limit at controller startup, default one, through the CLI and isolated development-service plan as needed. Keep host guest capacity separate and leave the general configuration loader and doctor to TASK-023.
- [ ] Verify supported DHCP MAC representations before relaxing parsing; preserve unique-entry, individual-expiry and identity checks.

#### Do not

- [ ] Do not remove a pin or identity fence to save a call, delete unknown files, share mutable runner bytes, or introduce automatic evidence expiry.
- [ ] Do not reset the VM, add a permanent guest scheduler or silently change the operator's running service.

#### Verify

- [ ] Use controlled readiness delays, expiry, permission denial and deadline boundaries; verify no guest staging before readiness.
- [ ] Compare call counts, bytes and bounded collection before/after; reject changed runner/root/file identity and exercise interrupted transfer and purge.
- [ ] Prove independent progress at global limits two and three, one slot per VM, unchanged default-one behavior and protection of unresolved runner payloads.

#### Done when

- [ ] Focused and common checks pass for the recorded protocol, startup and retention changes.
- [ ] Repeated calls no longer require complete-runner retransmission, finished runner storage has an explicit safe disposal path, and limits are selectable without a general configuration subsystem.
- [ ] TASK-038 has a measurable candidate and documented operating controls; no real-VM throughput claim is inferred from fixtures.

## TASK-038

### Publish and execute repeatable real-VM qualification

Requirement coverage: HP-REQ-011, HP-REQ-018, HP-REQ-019, HP-REQ-020, HP-REQ-021, HP-REQ-022, HP-REQ-023, HP-REQ-024, HP-REQ-025, HP-REQ-026, HP-REQ-027, HP-REQ-028, HP-REQ-029, HP-REQ-030, HP-REQ-031, HP-REQ-050, HP-REQ-051, HP-REQ-059, HP-REQ-062.

Why: TASK-016's bounded historical campaign does not qualify the later corrected HEAD, a cold start, large output or the two-VM/global-three profile. The tracked real-UTM entrypoint is still a stub.

#### Implement

- [ ] Replace the real-UTM stub with a repository-owned public-CLI campaign and explicit preflight, selected-candidate, resource, authority and finite-budget inputs.
- [ ] Execute the dossier campaign matrix on the exact installed development-service candidate and authorized registered resources; retain original keys/results after interruption.
- [ ] Record per-phase and per-call times, transfer bytes/calls, output sizes, host runner storage, occupancy and post-cleanup reuse. Bind source/executable/runner identities.

#### Do not

- [ ] Do not treat missing resources as a successful skip, fall back to the host, use a private helper as the only reproducibility source, or change VMs/services without live authority.
- [ ] Do not relabel the host-logout deferral as guest reboot or claim DF-001–003 passed from this campaign.

#### Verify

- [ ] Observe cold start, success/reuse, missing output, failure, cancellation, SSH interruption, controller restart, guest reboot and power-intent recovery.
- [ ] Collect and verify a 50 MiB log, complete a bounded 30-Job soak with failure/recovery interleaving, and run two VM Jobs plus independent local work at global limit three.
- [ ] Demonstrate wrong-key/identity refusal, no duplicate workload, no lost capacity and safe recovery from an interrupted campaign.

#### Done when

- [ ] Required real scenarios are observed against the same candidate and accepted decisions; a missing environment or observation leaves this task incomplete.
- [ ] A maintainer can reproduce the campaign using tracked instructions and public commands without copied private runtime state.
- [ ] Measured results and limits are recorded, F1/F2 changes are reviewed, and TASK-016 history and DF-001–003 remain intact.

## TASK-039

### Demonstrate one real project through the development service

Requirement coverage: HP-REQ-009, HP-REQ-010, HP-REQ-011, HP-REQ-014, HP-REQ-015, HP-REQ-018, HP-REQ-019, HP-REQ-021, HP-REQ-022, HP-REQ-032, HP-REQ-034, HP-REQ-035, HP-REQ-059, HP-REQ-062.

Why: integration fixtures alone cannot show that an ordinary project can freeze inputs, submit, obtain useful evidence and run again after a failure. That feedback must precede the wider consumer contract in TASK-017–020.

#### Implement

- [ ] Provide minimal digest/spec submission helpers and an executable development quickstart using sealed selected files, saved keys and truthful wait/result handling.
- [ ] Select one real project and bounded offline recipe under the dossier selection gate; exercise the approved development service without editing another repository by implication.
- [ ] Run success, meaningful failure, recovery/reuse and queued-input-change cases, recording usability gaps and minimum required fields.
- [ ] Hand off consumer-contract lessons to TASK-017 and external alignment requests to their owning repositories.

#### Do not

- [ ] Do not invent the full Make/environment-lock/manual integration ahead of EPIC-005 or equate a candidate pilot with release or Aquarium adoption.
- [ ] Do not install the candidate on the production host, invoke live providers, weaken verdicts or duplicate a Job after a lost reply.

#### Verify

- [ ] The real project runs through the public Haepari surface; evidence binds sealed candidate/test inputs and the qualified controller/runner.
- [ ] A caller can recover its saved key, inspect the actual failure and execute a distinct authorized successor after verified cleanup/recovery.
- [ ] Helpers reject invalid inputs and report missing observations without a false aggregate pass; ordinary local tests remain VM-independent.

#### Done when

- [ ] The observed project cycle proves useful evidence, once-only execution, bounded failure handling and resource reuse.
- [ ] TASK-017 receives the pilot's exact limitations and contract inputs; remaining work has a named owner.
- [ ] The dossier and canonical documentation distinguish implementation checks, real qualification, project observations and release/adoption.

## Epic acceptance

- [ ] TASK-036–039 meet their individual criteria and the dossier acceptance matrix on the final candidate.
- [ ] Original admissions, evidence, frozen runner bindings and terminal results remain protected; uncertainty never authorizes duplicate execution or speculative release.
- [ ] Required contract decisions, measured operating bounds and current runbooks are promoted to their canonical owners.
- [ ] The qualified project cycle precedes EPIC-005; deferred release checks and consumer-owned work retain their own acceptance.
- [ ] Explicit Epic acceptance updates the canonical roadmap. Prepare dossier closeout only after promoting durable outcomes and checking all remaining references.
