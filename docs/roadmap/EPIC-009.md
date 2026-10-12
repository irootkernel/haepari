# EPIC-009: VM operational readiness

Make ordinary managed VM work usable before expanding project, skill and manual integration. A recoverable failure must have a bounded route back to observation, settlement and reuse; unknown execution must retain its fences and occupancy.

Identity, status, order and actual prerequisites belong to the [canonical roadmap](../roadmap.md). Checked criteria record acceptance of the owner-approved core scope; excluded experiments are not asserted passed.

Canonical Outcomes: [owned runner and collection contract](../architecture-decision-records/0019-owned-runner-transport.md), [core VM operating observations](../records/TASK-038-operating-campaign.md), and [small caller e2e and TASK-017 handoff](../records/TASK-039-small-e2e.md). The [shared feedback dossier](../TODO-VM-OPERATIONAL-READINESS.md#feedback-disposition) remains for later active consumers.

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

- [x] Poll started state, the valid registered-MAC lease and pinned guest readiness within the admitted prepare deadline; preserve host-wide denial and no-effect pause behavior.
- [x] Measure the existing call/byte cost, then remove repeated complete-runner transfer using a digest-bound owned runner and bounded observation/collection operations. Record affected F2 decisions before changing the protocol.
- [x] Include runner payloads in safe finished-Job retention/purge or verified cleanup. Active and unresolved recovery must retain the exact runner; old request keys and required receipts must remain usable.
- [x] Expose a validated positive global execution limit at controller startup, default one, through the CLI and isolated development-service plan as needed. Keep host guest capacity separate and leave the general configuration loader and doctor to TASK-023.
- [x] Verify supported DHCP MAC representations before relaxing parsing; preserve unique-entry, individual-expiry and identity checks.

#### Do not

- [x] Do not remove a pin or identity fence to save a call, delete unknown files, share mutable runner bytes, or introduce automatic evidence expiry.
- [x] Do not reset the VM, add a permanent guest scheduler or silently change the operator's running service.

#### Verify

- [x] Use controlled readiness delays, expiry, permission denial and deadline boundaries; verify no guest staging before readiness.
- [x] Compare call counts, bytes and bounded collection before/after; reject changed runner/root/file identity and exercise interrupted transfer and purge.
- [x] Prove independent progress at global limits two and three, one slot per VM, unchanged default-one behavior and protection of unresolved runner payloads.

#### Done when

- [x] Focused and common checks pass for the recorded protocol, startup and retention changes.
- [x] Repeated calls no longer require complete-runner retransmission, finished runner storage has an explicit safe disposal path, and limits are selectable without a general configuration subsystem.
- [x] TASK-038 has a measurable candidate and documented operating controls; no real-VM throughput claim is inferred from fixtures.

## TASK-038

### Publish and execute repeatable real-VM qualification

Requirement coverage: HP-REQ-011, HP-REQ-018, HP-REQ-019, HP-REQ-020, HP-REQ-021, HP-REQ-022, HP-REQ-023, HP-REQ-024, HP-REQ-025, HP-REQ-026, HP-REQ-027, HP-REQ-028, HP-REQ-029, HP-REQ-030, HP-REQ-031, HP-REQ-050, HP-REQ-051, HP-REQ-059, HP-REQ-062.

Why: TASK-016's bounded historical campaign does not qualify the later corrected candidate, large output or repeated failure/recovery on the existing VM. The tracked public-CLI observation driver is implemented; bounded installed-candidate Job observations are recorded in the [campaign record](../records/TASK-038-operating-campaign.md), under the accepted reduced core scope. Master revised this campaign on 2026-10-10 to reuse one registered VM; [DF-004](../deferred-feedback.md#df-004-two-vm-and-local-concurrency) owns later two-VM/global-three qualification outside this Epic's acceptance. Master removed VM power-on/off, cold-start, guest-reboot and power-intent scenario qualification on 2026-10-12; this task reuses the already-running VM. That removal is a scope decision, not a claim that those scenarios passed. The same-machine, same-user, owner-started VM scope also excludes separate adversarial identity-refusal experiments and transport/network benchmarking. Reproducibility means a tracked runnable core e2e command, not a second live VM campaign.

#### Implement

- [x] Replace the real-UTM stub with a repository-owned public-CLI campaign and explicit preflight, selected-candidate, resource, authority and finite-budget inputs.
- [x] Execute the required dossier campaign matrix on the exact installed development-service candidate and one existing authorized registered VM at global limit one and host guest limit one; retain original keys/results after interruption.
- [x] Record core Job outcomes, complete collected output/log evidence, verified cleanup and successor reuse with the selected executable/runner. Retain already-observed timing/transfer/storage data as supporting measurements; no separate network benchmark is required.

#### Do not

- [x] Do not treat missing resources as a successful skip, substitute host results for VM execution, or change VMs/services without live authority. Keep the core e2e command and public caller recipe in the repository.
- [x] Do not relabel the host-logout deferral as guest reboot or claim DF-001–003 passed from this campaign.

#### Verify

- [x] Observe success/reuse, missing and uncollected output, failure, cancellation, timeout, SSH interruption and controller restart on the already-running VM. Do not run VM start/stop/reboot or power-intent scenario qualification.
- [x] Collect and verify a 50 MiB log and complete a bounded 30-Job soak with failure/recovery interleaving and sequential reuse of the same VM. Two-VM/local concurrency remains unobserved under DF-004 and is not a required campaign row.
- [x] Demonstrate no duplicate workload and no lost capacity across saved-key replay and successor reuse. Preserve the implemented VM/Job identity checks; separate wrong-key/lease/incarnation fault experiments are outside this Epic acceptance.

#### Done when

- [x] Required real scenarios are observed against the same candidate and accepted decisions; a missing environment or observation leaves this task incomplete.
- [x] A maintainer can run the tracked small core e2e command and inspect its public-CLI assertions without copied private runtime state. No separate live-campaign reproduction experiment is required.
- [x] Observed core results and their limits are recorded, F1/F2 changes are reviewed, and TASK-016 history and DF-001–003 remain intact. Optional transport data must not become a new completion gate.

## TASK-039

### Demonstrate a small isolated e2e caller cycle

Requirement coverage: HP-REQ-009, HP-REQ-010, HP-REQ-011, HP-REQ-014, HP-REQ-015, HP-REQ-018, HP-REQ-019, HP-REQ-021, HP-REQ-022, HP-REQ-032, HP-REQ-034, HP-REQ-035, HP-REQ-059, HP-REQ-062.

Why: the public caller path must freeze selected inputs, submit, obtain useful evidence and run again after a failure before the wider consumer contract in TASK-017–020. Master revised this task on 2026-10-12 to use a small repository-owned isolated e2e test; no external real-project selection or adoption is required. Its actual CLI/controller/runner observations are recorded in the [small e2e record](../records/TASK-039-small-e2e.md).

#### Implement

- [x] Reuse minimal test-owned digest/spec submission helpers and provide a runnable small e2e quickstart using sealed selected files, saved keys and truthful wait/result handling.
- [x] Use a bounded offline shell recipe with an isolated foreground controller and owned local resource. Do not require another project or change the installed service or VM.
- [x] Run success, meaningful failure, recovery/reuse and queued-input-change cases, recording usability gaps and minimum required fields.
- [x] Hand off caller-contract lessons and explicit local-e2e limits to TASK-017; external adoption and alignment remain with their consumer owners.

#### Do not

- [x] Do not invent the full Make/environment-lock/manual integration ahead of EPIC-005 or equate a candidate pilot with release or Aquarium adoption.
- [x] Do not install the candidate on the production host, invoke live providers, weaken verdicts or duplicate a Job after a lost reply.

#### Verify

- [x] The small test runs the actual product CLI/controller/runner through the public surface; evidence binds the executable and sealed test inputs. Local e2e is not real-VM qualification.
- [x] A caller can recover its saved key, inspect the actual failure and execute a distinct authorized successor after verified cleanup/recovery.
- [x] Helpers reject invalid inputs and report missing observations without a false aggregate pass; ordinary local tests remain VM-independent.

#### Done when

- [x] The observed small e2e cycle proves useful evidence, once-only execution, bounded failure handling and resource reuse.
- [x] TASK-017 receives the small caller cycle's exact limitations and contract inputs; remaining work has a named owner.
- [x] The dossier and canonical documentation distinguish implementation checks, real qualification, project observations and release/adoption.

## Epic acceptance

- [x] TASK-036–039 meet their individual criteria and the dossier acceptance matrix on the final candidate.
- [x] Original admissions, evidence, frozen runner bindings and terminal results remain protected; uncertainty never authorizes duplicate execution or speculative release.
- [x] Required contract decisions, measured operating bounds and current runbooks are promoted to their canonical owners.
- [x] The qualified small caller cycle precedes EPIC-005; external project adoption, deferred release checks and consumer-owned work retain their own acceptance.
- [x] Explicit Epic acceptance updates the canonical roadmap. Prepare dossier closeout only after promoting durable outcomes and checking all remaining references.
