# Managed execution transitions (v0.1.0 design)

This is the normative managed transition and capacity ledger (Design Revision 3.2). A Job has lifecycle state, workload outcome, evidence completeness, cleanup outcome and final verdict as separate fields. Resource availability is a separate resource field.

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
- **When capacity returns:** a terminal settlement returns `(-1,-1)` only after termination is proven. A verified restoration leaves the resource's availability as it was; a failed restoration after proven termination quarantines the resource.
- **Uncertain termination:** never returns capacity. The job stays `reconciling` and its resource stays `quarantined` with the occupancy retained.
- **Availability is preserved:** a job's settlement never clears `paused` or `quarantined`. Only the resource transitions below change availability, so another job on a multi-slot resource cannot un-quarantine it.

## Job transitions

| Previous state | Event | Guard and identity/revision | Next state | Delta | Durable record | Replay |
| --- | --- | --- | --- | --- | --- | --- |
| absent | submit | Valid schema, sealed input, unique request key and canonical submitted digest; profile matches the resource baseline; no occupying parent context | queued | (0,0) | Admission, input manifest, key/digest, resolved identities, deadline, event | Same key and submitted digest returns the original ID; a different digest conflicts |
| absent | reject | Invalid input, unsupported mode, `PROFILE_MISMATCH`, nested parent context or an immutable prerequisite unavailable at admission | absent | (0,0) | Bounded diagnostic only; no admission; the request key is not consumed | A retry with the same key is validated again and can be admitted once the cause is gone |
| queued | cancel | J; cancellation wins before reservation | finished | (0,0) | CANCELLED result and event | Terminal receipt |
| queued | queue deadline | J; persisted UTC deadline reached, no reservation | finished | (0,0) | INCOMPLETE result, reason `queue_expired` | Terminal receipt |
| queued | input corruption or lost prerequisite | J; sealed input digest mismatch, or a pinned baseline/profile is no longer available, before reservation | finished | (0,0) | INCOMPLETE result with an integrity or prerequisite gap; no lease | Terminal receipt |
| queued | reserve | J/R; resource `available`, queue head, free slot and global limit, adapter reservation guards (such as the host macOS guest limit) satisfied, input intact | preparing | (+1,+1) | Lease generation, slot, preparation intent, event | Same lease receipt; no second slot |
| preparing | prepare succeeds | J/R; adapter nonce and identity match | ready | (0,0) | Prepared identity and phase receipt | Existing phase receipt |
| preparing or ready | prepare fails, prepare deadline or host VM limit | J/R; effects classified | collecting if effects are possible; settling if no effect is proven | (0,0) | Failure, effects classification; a host VM limit without a guest start is a temporary prerequisite gap, not quarantine, and pauses VM dispatch (see resource availability) | Existing receipt; never repeat effects blindly |
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
| collecting | collection completes, fails or reaches its deadline | J/R; manifest and required-result checks attempted; collector stopped | cleaning | (0,0) | Artifact inventory, integrity, completeness and omissions; a deadline marks evidence partial | Existing manifest/receipt |
| collecting | collector cannot be proven stopped | J/R | reconciling | (0,0) | Unknown collector state | Current reconciliation state |
| collecting, cleaning, settling or reconciling | cancel | J; cancellation intent current | unchanged | (0,0) | Sticky cancel intent. After the workload has ended (collecting, cleaning, settling) it is recorded without changing the workload outcome or verdict. During reconciling it applies only if reconciliation proves the workload still running, which is then terminated under this intent; otherwise the proven outcome stands | Same intent |
| cleaning | cleanup/restoration succeeds | J/R; termination proven and declared reset/health checks pass | settling | (0,0) | Cleanup proof, baseline/health identity | Existing proof |
| cleaning | cleanup/restoration fails or its deadline passes | J/R; termination proven; exact resource identity retained | settling | (0,0) | Failure and quarantine reason | Existing failure receipt |
| settling | terminal commit, resource restored | J/R; final outcome fields and required evidence recorded | finished; resource availability unchanged (`available` stays `available`; `paused` and `quarantined` are not cleared) | (-1,-1) | Immutable result, verdict, lease return and capacity event atomically | Terminal receipt |
| settling | terminal commit, restoration failed | J/R; termination proven, restoration failed | finished; resource `quarantined` | (-1,-1) | INCOMPLETE or FAIL result, quarantine and capacity event atomically | Terminal receipt |

**Verdict rules.**

- **PASS** requires all of the following:
  - a successful workload
  - complete and intact required evidence
  - successful cleanup and restoration
  - an eligible manual observation, when one is requested
- **FAIL:** a known workload failure.
- **INCOMPLETE:**
  - absent proof or missing required evidence
  - a successful workload whose restoration failed; its resource is quarantined
- **CANCELLED:** only after safe settlement.
- **Unknown termination:** stays `reconciling`; it gets no verdict yet.
- Resource availability never substitutes for a job verdict.

## Resource availability

Resource availability is exactly `available`, `paused`, `recovering` or `quarantined`. Occupancy is recorded separately. Results report the field as `resource_availability`.

| Previous availability | Event | Guard | Next availability | Durable record |
| --- | --- | --- | --- | --- |
| available | pause | R; expected revision | paused | Pause receipt; current work continues under its policy |
| available | host capability lost | Adapter reports a framework VM-limit error without a guest start, or a missing session or Automation permission | paused, reason recorded; the cause is host-wide, so every `utm-macos` resource on this host is paused | Pause receipt with the reason; queued jobs stay queued under their own deadlines |
| paused | resume | R; expected revision; readiness verified, including host guest capacity and session for `utm-macos` | available | Resume receipt |
| available or paused | quarantine | Job settlement or reconciliation row above | quarantined | Quarantine reason and job reference |
| quarantined | apply recovery plan | R and exact plan token; no job occupies the resource | recovering | Recovery mutation receipt and evidence provenance |
| recovering | restoration and health proven | R | available | New resource revision and observation provenance |
| recovering | restoration fails or cannot be proven | R | quarantined | Failure receipt |

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
- The context is a chain. Each entry holds installation ID, job ID, lease generation and a controller-verifiable nonce.
- A runner appends its own entry and never replaces an inherited one. Entries from other installations are preserved but ignored by this controller.

**What the controller rejects.** A caller is nested when its chain holds an entry for a job that currently occupies capacity in this installation. For a nested caller, the controller rejects:

- **Submission or lock acquisition** (`NESTED_ACQUISITION_REJECTED`): rejected before admission, whatever resource or [advisory lock](locks.md) is requested and however much capacity is free.
- **Blocking observation of a non-terminal job** (`NESTED_WAIT_REJECTED`): `wait`, `run`, `events --follow` and `logs --follow`. Blocking on another job's progress while holding capacity can deadlock at the global limit.

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
