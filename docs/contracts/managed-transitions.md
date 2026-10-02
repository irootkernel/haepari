# Managed execution transitions (v0.1.0 design)

This is the normative managed transition and capacity ledger (Design Revision 3.4). A Job has lifecycle state, workload outcome, evidence completeness, cleanup outcome and final verdict as separate fields. Resource availability is a separate resource field.

Every mutation is a single durable transaction with an event and a revision increment. Guards use these fences:

- `J`: the exact job ID and expected job revision.
- `R`: resource ID, configuration revision, slot and lease generation.
- `run`: run ID, boot identity and nonce.
- `J/R`: all of `J` and `R`.

An exact mutation replay returns the stored receipt and current state, never a second effect. A changed payload with the same mutation key conflicts.

## Capacity rule

The capacity delta is `(resource occupancy, global occupancy)`.

- **What counts as occupancy:** reservation, preparation, execution, manual hold, collection, cleanup and uncertain execution.
- **Admission:** does not change limits or occupancy.
- **When capacity returns:** a terminal settlement of a reserved Job returns `(-1,-1)` only after termination or no-start is proven. Verified or legitimately `not_needed` cleanup leaves the resource's availability as it was; failed cleanup after proven termination quarantines the resource. Queued cancellation before reservation remains `(0,0)`.
- **Uncertain termination:** never returns capacity. The job stays `reconciling` and its resource stays `quarantined` with the occupancy retained.
- **Availability is preserved:** a job's settlement never clears `paused` or `quarantined`. Only the resource transitions below change availability, so another job on a multi-slot resource cannot un-quarantine it.

## Job transitions

| Previous state | Event | Guard and identity/revision | Next state | Delta | Durable record | Replay |
| --- | --- | --- | --- | --- | --- | --- |
| absent | submit | Valid schema, sealed input, unique request key and canonical submitted digest; no occupying parent context | queued | (0,0) | Admission, input manifest, key/digest, resolved identities, deadline, event | Same key and submitted digest returns the original ID; a different digest conflicts |
| absent | reject | Invalid input, unsupported mode, nested parent context or an immutable prerequisite unavailable at admission | absent | (0,0) | Bounded diagnostic only; no admission; the request key is not consumed | A retry with the same key is validated again and can be admitted once the cause is gone |
| queued | cancel | J; cancellation wins before reservation | finished | (0,0) | CANCELLED result and event | Terminal receipt |
| queued | queue deadline | J; persisted UTC deadline reached, no reservation | finished | (0,0) | INCOMPLETE result, reason `queue_expired` | Terminal receipt |
| queued | input corruption or lost prerequisite | J; sealed input digest mismatch, or the pinned resource revision is no longer available, before reservation | finished | (0,0) | INCOMPLETE result with an integrity or prerequisite gap; no lease | Terminal receipt |
| queued | reserve | J/R; resource `available`, queue head, free slot and global limit, adapter reservation guards (including a free host VM place or a fenced victim-to-target transfer claim) satisfied, input intact | preparing | (+1,+1) | Lease generation, slot, preparation intent, event | Same lease receipt; no second slot |
| preparing | prepare succeeds | J/R; adapter nonce and identity match | ready | (0,0) | Prepared identity and phase receipt | Existing phase receipt |
| preparing or ready | prepare fails, prepare deadline or host VM limit | J/R; effects classified | collecting if effects are possible; settling if no effect is proven | (0,0) | Failure, effects classification; for a VM-local proven no-effect prerequisite failure, the `vm_unavailable` availability row commits in this same transaction; a host VM limit without a guest start is a temporary prerequisite gap, not quarantine, and pauses VM dispatch (see resource availability) | Existing receipt; never repeat effects blindly |
| preparing or ready | cancel | J/R; cancellation intent current | collecting if effects are possible; settling if no effect is proven | (0,0) | Cancel intent and effects classification | Same intent |
| ready | start automatic run | J/R/run; durable spawn intent and single run claim | running | (0,0) | Spawn intent before the effect, then the start receipt when observed | Existing run, or reconcile an ambiguous spawn |
| ready | manual setup complete | J/R; setup and environment identity observed | awaiting_manual | (0,0) | Ready receipt, observer requirements, manual deadline | Current session; never PASS |
| running | exit or workload failure | J/R/run; exact exit/termination evidence | collecting | (0,0) | Exit receipt, workload outcome, event | Existing exit receipt |
| running | cancel or execution deadline | J/R/run; request is current | cancelling | (0,0) | Cancel intent and bounded termination policy | Same intent; acknowledgement is not termination |
| awaiting_manual | manual completion | J/R; expected revision, observer and required scenario entries | collecting | (0,0) | Scoped observation receipt and omissions | Exact receipt; conflict rejected |
| awaiting_manual | cancel or manual deadline | J/R; deadline or cancel intent | cancelling | (0,0) | INCOMPLETE or cancellation intent | Existing intent; no automatic PASS |
| cancelling | termination established | J/R/run; owned process tree stopped or no-start proven | collecting | (0,0) | Termination/no-start proof | Existing proof |
| cancelling | termination deadline without proof | J/R/run; bounded termination policy exhausted | reconciling | (0,0) | Termination attempts and unknowns | Current reconciliation state |
| preparing, ready, running, awaiting_manual, cancelling, collecting, cleaning or settling | controller or runner interruption | J/R; lost observation, no terminal proof | reconciling | (0,0) | Last intent/receipt, boot and process identity, uncertainty event | Current reconciliation state |
| reconciling | reattach | J/R/run; surviving runner and exact nonce/boot identity proven | prior observed phase | (0,0) | Reattachment observation and revision | Existing observation |
| reconciling | resume cleanup or settlement | J/R; termination already proven; durable cleanup or settlement intent and exact resource identity proven | cleaning or settling | (0,0) | Resumed intent and observation | Existing intent; cleanup steps are idempotent or re-verified |
| reconciling | prove stop/no-start | J/R/run; machine observation or scoped operator attestation with provenance | collecting | (0,0) | Recovery evidence and provenance; attestation alone never yields PASS | Existing recovery receipt |
| reconciling | evidence insufficient | J/R; execution unknown | reconciling; resource quarantined | (0,0) | Updated observations, blocked reason and quarantine receipt retaining the lease | Current state; no takeover on TTL |
| collecting | collection completes, fails or reaches its deadline | J/R; manifest and required-result checks attempted; collector stopped | cleaning | (0,0) | Artifact inventory, integrity, completeness and omissions; a deadline marks evidence partial; required recoverable uncollected paths are protected from deletion | Existing manifest/receipt |
| collecting | collector cannot be proven stopped | J/R | reconciling | (0,0) | Unknown collector state | Current reconciliation state |
| collecting, cleaning, settling or reconciling | cancel | J; cancellation intent current | unchanged | (0,0) | Sticky cancel intent. After the workload has ended (collecting, cleaning, settling) it is recorded without changing the workload outcome or verdict. During reconciling it applies only if reconciliation proves the workload still running, which is then terminated under this intent; otherwise the proven outcome stands | Same intent |
| cleaning | cleanup succeeds | J/R; termination proven, all required owned paths removed and declared cleanup/health checks pass | settling | (0,0) | Cleanup proof, VM/health identity | Existing proof |
| cleaning | cleanup fails, required evidence is intentionally retained, or its deadline passes | J/R; termination proven; exact resource identity and retained evidence references recorded | settling | (0,0) | `cleanup_outcome=failed`, failure or `evidence_retained` reason, quarantine reference | Existing failure receipt |
| settling | terminal commit, cleanup not needed | J/R; exact Job, pinned resource configuration, current resource revision, slot and lease generation; no owned Job effect requiring cleanup, unproven workload/collector/other owned process, protected owned path or unresolved evidence-retention obligation; any VM power operation fully reconciled | finished; resource availability unchanged, including `paused` or `quarantined` | (-1,-1) | Derive verdict through the authoritative table; atomically commit immutable result with `cleanup_outcome=not_needed`, lease return, capacity change and terminal event | Terminal receipt; exact replay never returns capacity twice |
| settling | terminal commit, cleanup verified | J/R; final outcome fields and required evidence recorded | finished; resource availability unchanged (`available` stays `available`; `paused` and `quarantined` are not cleared) | (-1,-1) | Immutable result, verdict, lease return and capacity event atomically | Terminal receipt |
| settling | terminal commit, cleanup failed or evidence retained | J/R; termination proven, cleanup not verified | finished; resource `quarantined` | (-1,-1) | INCOMPLETE result, quarantine and capacity event atomically; known workload failure remains in `workload_outcome` | Terminal receipt |

## Authoritative final-verdict decision table

TASK-010 derives every terminal verdict through one common path from the separate recorded fields. Apply the rows in order. Evidence requirements are evaluated for the path that actually ran: queued cancellation and proven no-start do not require outputs from an unstarted workload, while a cancelled active run requires termination and cleanup evidence. `verified` cleanup includes required health checks; `not_needed` is valid only when no owned Job effect requiring cleanup occurred, even if a resource slot and lease were reserved or a VM start was confirmed and reconciled before guest staging. Successful cleanup of an effectful path remains `verified`. An uncertain VM start or stop cannot be classified `not_needed` merely because no files were staged. An eligible manual observation is required only for a declared manual result. Resource availability never substitutes for a Job verdict.

| Recorded condition | Final verdict and preserved facts |
| --- | --- |
| After possible effects, owned workload or collector termination is unproven | None: remain `reconciling`, retain occupancy and quarantine; a timeout or cancel acknowledgement is not proof. |
| Queued cancellation before resource acquisition | CANCELLED with `cleanup_outcome=not_needed` and no occupancy delta. |
| Rejection before admission | No Job or verdict; return the appropriate CLI error without consuming the request key. |
| Applicable required evidence is missing, partial, corrupt or unobserved; required results for a completed manual path are absent; or cleanup is `failed`/`unknown` | INCOMPLETE, even if workload failed; retain the observed `workload_outcome` and quarantine when cleanup/reuse is unproven. |
| Explicit cancellation wins before a known workload outcome, termination is proven, required cancellation evidence is complete and cleanup is `verified` or legitimately `not_needed` | CANCELLED with `workload_outcome=cancelled`. |
| Workload passes, required evidence is complete/intact, applicable manual observations are eligible and cleanup is `verified` | PASS. |
| Workload or a required manual assertion fails, required evidence is complete/intact and cleanup is `verified` | FAIL; keep the exact workload failure and native exit/signal. |
| Execution or manual deadline, preparation failure, missing result, queue expiry or lost prerequisite without a verified workload conclusion | INCOMPLETE; use `workload_outcome=timed_out` for an execution deadline and `inconclusive` where execution did not establish an outcome. |

A cancellation received after workload completion does not change a known workload failure or the verdict derived from it; a cancellation after terminal commit returns the existing terminal result. A timed-out or explicitly cancelled run with a prior proven failure retains that failure. Proven no-effect paths may settle INCOMPLETE with `cleanup_outcome=not_needed`. The CLI maps terminal PASS/FAIL/INCOMPLETE/CANCELLED to exits 0/1/3/4.

## Evidence-preserving settlement

Stopping an owned workload or collector and deleting its files are separate effects. If required evidence remains recoverable after collection fails or times out, record its guest-relative owned paths, Job/run and resource identities, collection gaps, retention reason and recovery reference durably before any cleanup can delete it. Stop owned processes as needed, but do not delete the referenced files. With proven termination, settle the Job INCOMPLETE, return its managed occupancy and leave the resource quarantined with `cleanup_outcome=failed` and reason `evidence_retained`; required owned paths prevent verified cleanup. If workload or collector termination is unproven, stay reconciling with occupancy retained.

After a terminal settlement, `resource recover --plan` identifies protected evidence, exact resource revision and proposed bounded re-collection or explicit disposal. Applying a disposal plan requires authorization that names the evidence loss; record the authority, effects and outcome. Re-collection adds supplementary evidence receipts, never edits an immutable terminal result or upgrades it to PASS. Once evidence is recovered or explicitly disposed, remove remaining Haepari-owned Job files, verify process absence and VM health, then return the resource to `available`; failures leave it quarantined. If the Job still occupies capacity, `job recover` establishes termination and settles it before resource recovery. Ordinary retention expiry, purge and garbage collection must honor every unresolved evidence/recovery reference.

## Implemented local phase receipts (TASK-010)

Local preparation failure, cancellation, proven no-start and completed workload paths share verification, verdict derivation and atomic terminal commit. Effectful paths use bounded collection and cleanup; a proven no-effect path moves directly to settling and omits unneeded phases. Preparation records a stopped-worker proof only after the worker completes. An expired preparation before worker authorization additionally requires the owned Job path to be absent; it produces INCOMPLETE with `not_needed`, without creating filesystem receipts under the owned Job path or claiming a run. An existing unowned Job path blocks this shortcut. Owned preparation files still need cleanup even when no workload starts; outputs declared for an unentered workload are inapplicable.

The observable `preparation_stopped` event records the exact lease-bound `identity`, classification `reason` (`prepare_failed`, `prepare_deadline` or `cancel_before_start`), frozen `outcome` and `no_owned_effect` proof flag. It is committed only after controller-observed worker completion, before the common collection/cleanup path or proven no-effect settling. The outcome preserves cancellation ordered before preparation completion; the event never authorizes restarting the worker.

The active SQLite event stream stores `phase_intent` and `phase_observed` with `haepari.phase-receipt/v1`: exact Job/resource/configuration/slot/lease/run/nonce identity, phase, configured `budget_seconds`, UTC `started_at`, absolute `deadline`, latest `observed_at`, `completed` and observed `outcome`. Phases are `prepare`, `run`, `collect`, `cleanup` and `verify`. Verification is a ledger decision with no separate filesystem budget; its budget is zero and deadline is the zero timestamp. Other phase budgets match the admitted Job, and a completion cannot change its original start or deadline. These records contain no portable monotonic reading and never authorize repeating an interrupted effect. Missing active payloads, conflicting receipts or a backward observation block progress. TASK-011 consumes these facts for restart reconciliation. New phase intents also record the controller's boot/birth/process identity as `controller_owner`. An incomplete or unproven filesystem phase cannot resume until its old worker incarnation is proven stopped or a scoped operator attestation is recorded. Attestation cannot override a positively observed matching live filesystem owner or an execution supervisor without terminal proof; planning withholds a token and apply retains occupancy and evidence. Recovery appends `phase_reconciled` compensation with the original intent digest, exact identity, evidence and unchanged deadline; it never edits the original phase observation. A resumed cleaner remains tracked under its current controller until its worker actually stops, even after an observation deadline. While that worker remains active, recovery plans withhold tokens and apply retains occupancy; the controller keeps its database and ownership lock through listener shutdown. For the `run` phase, `completed=true` means supervision or reconciliation has completed; it does not establish that the workload started or succeeded. Native `started`, outcome and termination evidence remain separate, and attestation cannot establish PASS. A completed trustworthy phase is consumed without repeating its effect. Backward phase timestamps record `clock_discontinuity` and block progress; persisted UTC deadlines never gain a fresh phase budget after restart.

A collection observation also stores the size/digest artifact inventory, complete/partial evidence and protected Job-relative paths. It commits before cleanup intent or deletion. A stopped collector with recoverable incomplete evidence settles INCOMPLETE with `reason=evidence_retained`; protected-path reasons distinguish missing output from `collection_deadline`. A blocked collector or cleaner records `unproven`, retains occupancy and enters quarantine/reconciliation. No filesystem worker is started after its phase deadline. Local settlement checks observed collection/cleanup facts against the final result and requires the current separate termination or preparation-stop proof; historical ledger-only fixtures without phase records cannot bypass the proof. Immutable result `timings` projects admission, reservation and the start/end of entered phases, including verification. Queued cancellation has admission timing and no reservation or phase timings.

This slice preserves the existing revision-bound VM no-effect classification, pause and settlement transactions; it does not operate a VM. TASK-011's current candidate implements explicit local reconciliation and the isolated development-service lifecycle. Protected-evidence re-collection/disposal and production retention remain TASK-012; manual completion remains TASK-019. Actual installed-service restart requires its separately authorized observation.

## Internal VM power-transition ledger

A victim-to-target transfer is part of the existing controller scheduler, not a second scheduler or a multi-resource Job. Before any `utmctl` effect, atomically record the victim stop intent and the target's future start claim with exact Job/lease, victim/target resource revisions, VM UUIDs and operation generation. Victim selection and operator pause/policy update are serialized by the same revisioned transaction: a pause committed first excludes the VM; a pause/update racing a committed transition conflicts until reconciliation. The victim remains protected from Job reservation, reconfiguration and another power operation until observed state reconciles the intent. It still counts as running until stop is confirmed; a pending/uncertain start counts toward the host limit. The target starts only after the victim stop is confirmed. Subprocesses run outside SQLite transactions. On restart or stale responses, inspect current state and match the recorded generation; never infer that a revision failure cancelled an issued command. If no eligible victim exists before reservation, retain the queued Job and its deadline with an actionable blocker. A proven no-start prerequisite failure follows the normal INCOMPLETE/no-effect path; uncertain power effects retain protection until reconciled.

## Resource availability

Resource availability is exactly `available`, `paused`, `recovering` or `quarantined`. Occupancy is recorded separately. Results report the field as `resource_availability`.

| Previous availability | Event | Guard | Next availability | Durable record |
| --- | --- | --- | --- | --- |
| available | pause | R; expected revision; no pending/uncertain VM power transition | paused | Pause receipt; current work continues under its policy; a committed pause excludes automatic stop selection |
| available | VM-local prerequisite unavailable | Exact registered VM/resource and current configuration and availability revisions; the reserved Job's lease/slot and proven no-effect preparation failure identified; no guest staging, workload, collector or other owned Job effect requiring cleanup; any VM power operation has a known reconciled outcome | paused, reason `vm_unavailable` for this resource only | In the same transaction as that Job's `preparing or ready -> settling` failure classification, record scoped cause, observations, identities, predecessor/successor resource revisions and pause event; block dispatch before the affected Job's reservation is returned |
| available | host capability lost | Adapter reports a framework VM-limit error without a guest start, or a missing session or Automation permission | paused, reason recorded; the cause is host-wide, so every `utm-macos` resource on this host is paused | Pause receipt with the reason; queued jobs stay queued under their own deadlines |
| paused | resume | R; expected revision; no pending/uncertain VM power transition; readiness verified, including host guest capacity and session for `utm-macos` | available | Resume receipt |
| available or paused | quarantine | Job settlement or reconciliation row above | quarantined | Quarantine reason and job reference |
| quarantined | apply recovery plan | R and exact plan token; no job occupies the resource | recovering | Recovery mutation receipt and evidence provenance |
| recovering | protected evidence re-collected or explicitly disposed, owned files removed, and cleanup and health proven | R | available | New resource revision, evidence recovery/disposal receipt and observation provenance |
| recovering | cleanup fails or cannot be proven | R | quarantined | Failure receipt |

For a VM-local prerequisite failure, classify the reserved Job's effects and move it to `settling` in the same durable transaction that pauses an available resource; then settle that Job through the `not_needed` INCOMPLETE row. The pause receipt links the reservation's pinned configuration revision and lease generation to the successor resource availability revision; the Job settlement checks that link and the current revision. A crash between pause and settlement leaves occupancy reserved for recovery. An exact replay returns the pause or terminal receipt without repeating either change. If the resource was already `paused` or `quarantined`, classify the failure and preserve that availability and its reason in the same transaction; its failure receipt binds the unchanged availability revision and original lease for settlement. Do not downgrade quarantine or overwrite an operator pause. A stale response for an older registration/revision cannot pause a newer one or change a terminal result. Host-wide session, permission and framework-limit failures keep the host-wide row; uncertain power effects remain protected for reconciliation, and failures after possible guest effects use the effectful cleanup path. Resume is the explicit revision-bound, readiness-verified row above.

**Order of recovery.**

1. **Refused while occupied.** `resource recover` returns `RECOVERY_BLOCKED` while any job still occupies the resource.
2. **Job first.** `job recover` settles the reconciling job first, using the "prove stop/no-start" row and then normal settlement. That returns its occupancy, and the resource stays `quarantined`.
3. **Then the resource.** Resource recovery restores availability.

A recovery never rewrites a job verdict.

## Deadlines and restart

**What is stored.** The controller stores:

- wall-clock UTC creation time and absolute deadlines
- the configured duration
- the last observation

A process-local monotonic clock may measure a current interval, but its reading is never persisted as portable time.

**On wake or restart:**

1. Compare persisted deadlines with the current wall clock.
2. Record any clock discontinuity.
3. Reconcile execution before applying deadline effects.

**What each deadline does when it expires:**

| Deadline | Effect |
| --- | --- |
| Queue | Finishes the job; needs no resource. |
| Execution or manual | Requests cancellation and proof of termination. |
| Collection | Marks evidence partial and proceeds to cleaning once the collector is stopped. |
| Cleanup | Quarantines the resource. |
| Wait timeout | Affects only the client call. |

A backward clock jump cannot extend a previously observed deadline without an explicit revisioned extension.

## Nested acquisition

**The parent context chain.**

- Managed runners export a parent context to their descendants, including the guest entrypoint. The host wrapper propagates it when it invokes child tools.
- The context is a chain. Each entry holds installation ID, job ID, lease generation and a controller-verifiable nonce. The local provisional transport uses bounded JSON in `HAEPARI_PARENT_CONTEXT` and `X-Haepari-Parent-Context`: at most 32 entries and 8192 encoded bytes, with exact lowercase JSON field names. Admission reserves room for the runner's own entry. The controller stores the inherited chain at admission and issues a fresh nonce in the lease reservation transaction; declared workload environment cannot replace that exported chain.
- A runner appends its own entry and never replaces an inherited one. Entries from other installations are preserved but ignored by this controller.

**What the controller rejects.** A caller is nested when its chain holds an entry for a job that currently occupies capacity in this installation. For a nested caller, the controller rejects:

- **Submission (including `run` admission) or lock acquisition** (`NESTED_ACQUISITION_REJECTED`): rejected before admission, whatever resource or [advisory lock](locks.md) is requested and however much capacity is free.
- **Blocking observation of a non-terminal job** (`NESTED_WAIT_REJECTED`): `wait`, `events --follow` and `logs --follow`. `run` first submits a Job, so a nested invocation is rejected at admission with `NESTED_ACQUISITION_REJECTED` before any wait. Blocking on another job's progress while holding capacity can deadlock at the global limit.

**What stays allowed.** Non-blocking `inspect`, `events` and `logs` pages on any job, and any observation of a terminal job. To run more work, either execute inside the held Job, or settle and release it before submitting an independent Job. No recursive slot borrowing or queue bypass exists.

**Limits of the check.**

- As a best-effort second signal, the controller may compare the socket peer's process group and session with active runners.
- A caller that strips the environment, or uses a pre-existing process without the inherited context, can still evade this cooperative check. It is an integration safeguard, not a same-user security boundary.

**Required tests:**

- a parent holding the only global slot
- parents occupying multiple slots while each requests another resource
- a nested blocking wait, and a nested `events --follow`, on a queued job
- a chain that crosses two installations
- a nested caller acquiring an advisory lock
- a multi-slot resource quarantined by one job, while another job on it settles successfully

### Current manual accounting foundation

TASK-007 implements the internal `HoldManualCapacity` ready-to-awaiting_manual transaction. It requires the exact Job/configuration/availability revisions, occupied slot and lease, admitted manual mode and environment digest, and bounded adapter readiness evidence. It records the admitted required scenarios, scenario document and UTC manual deadline in the durable event and changes no resource/global capacity. Same-key exact replay returns the original receipt without extending that deadline. Time passage does not return a managed slot; interruption retains it for reconciliation. This is an adapter accounting seam, not public manual-session qualification. TASK-019 owns runtime session discovery, human completion and abandonment handling.
