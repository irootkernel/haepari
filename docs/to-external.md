# External change requests

Status: request list for other repositories, Design Revision 3.2 (2026-09-28). Nothing here has been applied to those repositories. Each owner reviews the request, allocates its own IDs and records accepted work in its own roadmap. This file records what Haepari's managed-first design needs from other projects and why; it is not an authority over them.

Order:

1. The Mulgae request (§2.1) has two parts. Queuing reviews on Haepari's advisory lock needs the Haepari v0.1.0 lock capability. The measurement and the optional safeguards can start at any time.
2. The Aquarium baseline and alignment (§1.1 and §1.2) must be complete before Aquarium:TASK-086 accepts a Haepari handoff.
3. Everything marked advisory is a proposal for the owner's decision.

Cross-project IDs are always qualified: `Haepari:TASK-0nn`, `Aquarium:TASK-0nn`, `Aquarium:EPIC-002`. Aquarium:EPIC-002 (Cancelled development environment) and Haepari:EPIC-002 (Durable queue) are unrelated.

## 1. Aquarium

**Observed state (read-only, 2026-09-28).**

- HEAD `f987ada` ("[DOC] Mark TASK-085 completed") does not contain Aquarium:EPIC-019.
- The Aquarium:EPIC-019 plan exists only as uncommitted working-tree edits:
  - modified `docs/roadmap/README.md`
  - modified `docs/todo/README.md`
  - modified `docs/todo/TODO-AQUARIUM-DEV.md`
  - untracked `docs/todo/TODO-HAEPARI-VM-ADOPTION.md`
- These edits still describe the Haepari Design Revision 2 scope.

### 1.1 Commit a planning baseline (required)

Review and commit the Aquarium:EPIC-019 planning edits, preferably together with §1.2. The Haepari handoff and the Sorage packets can then cite an exact Aquarium revision.

### 1.2 Align Aquarium:EPIC-019 with the managed-first scope (required)

**What changed on the Haepari side.** Haepari ADR-0016 defers the following:

- public external leases
- linked continuation segments
- E01–E20

Haepari v0.1.0 supplies managed local and UTM macOS Jobs and the advisory lock `mulgae-review` (Haepari ADR-0017), on which ordinary Mulgae reviews queue.

**Why this blocks adoption.** Aquarium:EPIC-019 still requires the deferred capabilities. A Haepari v0.1.0 handoff cannot satisfy that plan, so Aquarium:TASK-086 would stay blocked.

**How to read the tables.** Line numbers refer to the uncommitted working tree observed above.

- **Replace** swaps the quoted text for the proposed text.
- **Remove** deletes the quoted text only.
- **Keep** marks nearby text that stays because it is managed-scope content.

#### `docs/roadmap/README.md`

| Line | Current text | Request |
| --- | --- | --- |
| 471 (Aquarium:EPIC-019 blocker) | "…including managed UTM reset, independent local-resource evidence and design-revision-2 external lease/continuation capabilities with independent fixture evidence." | **Replace** with: "…including managed UTM reset and independent local-resource evidence. Public external leases are deferred by Haepari ADR-0016 and are not a blocker." |
| 479 (Aquarium:TASK-086) | "Verify exact release, runner, skill, managed/external capabilities, first-claim execution and continuation fixtures plus real backend evidence; freeze consumer and authority boundaries." | **Replace** with: "Verify exact release, runner, skill and managed capabilities plus real local and UTM backend evidence; freeze consumer and authority boundaries." |
| 483 (Aquarium:TASK-090) | "…freeze Sorage packets including Mulgae VM plus external lease/partial-recovery scope…" | **Replace** with: "…freeze managed-first Sorage packets; the Mulgae packet covers managed VM, skill, migration, regression and legacy detachment only…" |
| 485 (Aquarium:TASK-092) | "Through Sorage accept producer-owned VM/skill/migration tests and use-mulgae external leases, same-lease recovery, release-before-user-wait, fresh-lease continuation and native compose; reconcile Aquarium references and legacy detachment." | **Replace** with: "Through Sorage accept producer-owned VM, skill, migration and regression tests and legacy detachment; Mulgae's native partial-role recovery and composition stay Mulgae-owned; reconcile Aquarium references." |
| 493 (Aquarium:TASK-100) | "…managed/external occupancy and native Mulgae partial-recovery continuation without a second lease wrapper." | **Replace** with: "…managed occupancy and restoration evidence." |
| 495 (Aquarium:TASK-102) | "Verify final managed/external behavior and review-recovery acceptance…" | **Replace** with: "Verify final managed behavior…" |

#### `docs/todo/README.md`

| Line | Current text | Request |
| --- | --- | --- |
| 14 | "…Haepari external-lease capability acceptance, Mulgae-owned `use-mulgae` partial-failure continuation…" | **Replace** with: "…Haepari managed capability acceptance…" |

#### `docs/todo/TODO-HAEPARI-VM-ADOPTION.md`

| Line | Current text | Request |
| --- | --- | --- |
| 9 | "This epic adopts project-owned VM tests, delegates external review-lease integration to Mulgae, and retires the old development channel in stages. Haepari design revision 2 supplies both managed and external execution contracts; the product target remains v0.1.0." | **Replace** those two sentences with: "This epic adopts project-owned VM tests and retires the old development channel in stages. Haepari Design Revision 3.2 supplies the managed execution contract and an advisory lock on which Mulgae queues ordinary reviews; the R2 external lease is deferred by Haepari ADR-0016, and the product target remains v0.1.0." **Keep** the rest of the paragraph. |
| 41 | "### Managed VM execution and external review leases" | **Replace** with: "### Managed VM execution" |
| 43 | First sentence: "UTM tests use Haepari-managed execution, including manual hands-on ownership." The remaining sentences cover the `mulgae-review` external resource, managed/external naming, shared occupancy and review segments. | **Keep** the first sentence. **Replace** the rest with: "Ordinary Mulgae reviews queue on Haepari's advisory lock `mulgae-review` (Haepari ADR-0017); the R2 external lease stays deferred." |
| 45, 47, 49 | Atomic grant or queue, first-claim begin, same-lease retry, new-lease continuation, stale-release fencing and owner material | **Remove** these three paragraphs. |
| 51 | Six-role recovery guidance ending "…complete coverage, publication, findings/CI and returned capacity remain separate facts…" | **Keep** it as Mulgae-owned native recovery guidance. **Remove** "and returned capacity". |
| 53 | "TASK-092 delegates this implementation to the Mulgae project, including its owned `use-mulgae` skill and any minimal required native helper. … Haepari's standalone fixture evidence does not prove native Mulgae behavior." | **Replace** the first sentence with: "TASK-092 delegates Mulgae's managed VM adoption to the Mulgae project." **Remove** the sentence about Haepari's standalone fixture evidence. **Keep** the isolated test instance rule and the pilot-independence sentence. |
| 109 | "The Mulgae return additionally identifies the external request/lease generations, operation/continuation references and native root/rerun/attempt/composite results… VM-only success cannot complete TASK-092." | **Replace** with: "The Mulgae return reports managed VM evidence and Mulgae's own native review evidence (installed `use-mulgae` behavior, rerun lineage, composition). Other producers acquire no external-review obligation." |
| 133 | "Require exact managed/external resource capabilities, atomic grant/queue, single-executor activation and first-claim dispatch… Accept Haepari's independent E01 through E20 fixture evidence…" | **Replace** with: "Require exact managed capabilities and real local and UTM evidence. E01–E20 are deferred by Haepari ADR-0016." |
| 142 | "Parse valid and invalid managed and external input/result fixtures… an uncertain native start cannot be re-dispatched from a replay." | **Replace** with: "Parse valid and invalid managed input/result fixtures and demonstrate supported wait/reconnect through the exact installed test release. Verify that a stale lease cannot return a successor's capacity and that an uncertain managed start is not rerun from a replay." |
| 200 | "…The revised Mulgae packet must cover both managed VM adoption and external `use-mulgae` leases, partial-failure recovery, native compose and explicit advisory limits." | **Replace** the second sentence with: "The revised Mulgae packet covers managed VM adoption only." |
| 233 | "…Send the revised two-part request through Sorage…" | **Replace** "revised two-part request" with "managed VM adoption request". The Mulgae rate-limit work is a separate request (§2.1), not a Sorage rollout part. |
| 235, 236, 238, 240 | Queue-backed ordinary review, ownership and dispatch checkpoint, lease return before user waiting, and capacity retention under observer timeout | **Remove** these checklist items. |
| 237 | "Verify the six-role partial-failure case: five accepted results remain unchanged; authorized immediate exact recovery uses the same lease, with no recursive acquisition or repeated successful role." | **Replace** with: "Verify natively that five accepted results remain unchanged and that recovery reruns only the failed role." This is optional Mulgae-owned evidence. |
| 239 | "Verify later authorized one-role recovery under a new FIFO request/lease linked to the released segment, exact native source/attempt lineage and immutable compose. Reject…" | **Replace** with: "Verify exact native source/attempt lineage and immutable compose; reject mixed targets, missing or corrupt recovery sources and selected-role omissions." |
| 241, 243, 244 | Isolated test instance in the VM; align only Aquarium-owned references; remove old development references | **Keep.** |
| 242 | "…whole-environment restoration and the documented advisory limitation of direct unqueued calls." | **Keep.** The lock is advisory, and Haepari HP-REQ-081 requires that limitation to be documented. |
| 302 | "Exercise the accepted queue-backed Mulgae skill alongside managed VM ownership: independent resource queues, shared global occupancy, same-lease failed-role recovery, slot return during user waiting, fresh-lease continuation…" | **Replace** with: "Exercise Mulgae's installed skill alongside managed VM ownership and record actual native/installed-skill evidence separately." |
| 336 | "…Include managed/external capacity and accepted Mulgae partial-failure continuation, preserving incomplete native reviews as distinct from released leases." | **Replace** that sentence with: "Include managed capacity and Mulgae's accepted native review behavior." |
| 351 | "Haepari is independently implemented and its exact managed/external native handoff and independent fixture evidence are accepted." | **Replace** with: "Haepari is independently implemented and its exact managed handoff is accepted." |
| 354 | "…including the producer-owned external `use-mulgae` integration and partial-failure continuation contract." | **Remove** this clause. |

Use qualified cross-project IDs in both repositories. Replace any unqualified "EPIC-002" wording that could be read as Haepari:EPIC-002.

### 1.3 Resolve the Cancelled-status inspector warning now (recommended)

**The problem.** The R2 planning check reported `epic_lifecycle_unverifiable` for the roadmap-permitted `Cancelled` status of Aquarium:EPIC-002. The fix is scheduled inside Aquarium:TASK-101, near the end of the rollout. Until then, the warning can mask new inspector regressions.

**The request.** Handle it as a small independent task now. This revision did not recheck the warning.

### 1.4 Proposals for Aquarium's decision (advisory)

These come from the Haepari design review. They are not Haepari requirements.

**Rollout scope.** The verified gaps that host-isolated tests cannot close are concentrated in Aquarium, Podway and Sorage:

- Podway `56c8421`, Not-tested: "actual user launchd GUI-domain installation or restart"
- Podway `1e81399`, Not-tested: "production LaunchAgent upgrade"
- Sorage `7e3a98a`, Not-tested: "A literal fresh macOS login/logout LaunchAgent journey"
- Sorage `fc41df8`: explains that release tests must intercept `launchctl` because "Test homes do not namespace launchd labels"
- Aquarium `cc114b2` and `0a0fff5`, Not-tested: "fresh installed-session discovery"

Consider making Podway and Sorage the required producers after the Aquarium pilot. The other seven producers (Mulgae, Gaori, Sanho, Dolgorae, Gul, Seongge, Gaebokchi) would then be opt-in.

**Legacy retirement.** `~/.aquarium-dev/bin` currently contains only `dolgorae`, `gaori`, `podway` and `sanho`. Consider retiring the shared channel once those four producers detach, instead of after all nine VM adoptions.

**Pilot value metrics.** In Aquarium:TASK-090, record:

- defects found only in the VM
- the change in VM-related Not-tested notes
- manual verification time per release
- VM job duration and VM cycle phases
- queue wait p50/p95
- flake rate

VM tests run in the development loop, and the owner expects 10–30 jobs a day. Plan for the development-loop profile in the [configuration contract](contracts/configuration.md#development-loop-profile): two VM resources and a host guest limit of two.

A suggested go/no-go for wider rollout has two parts:

- at least one defect found only in the VM, or at least a 50% reduction in VM-related Not-tested notes
- a flake rate of 5% or less

**Queue deadlines.** A UTM resource has capacity one. Set `queue_seconds` to at least (jobs ahead, including the running one, + 1) × the maximum job duration; see the [configuration contract](contracts/configuration.md#queue-deadlines-for-capacity-one-resources).

### 1.5 What Haepari supplies

The [producer completion packet](handoffs/haepari-to-aquarium.md) supplies:

- the exact managed schemas and capabilities, frozen at gates F1–F3
- the advisory lock contract and the `advisory-lock/v1` capability
- real local and UTM evidence
- the V-case results
- known limits

It does not supply the deferred external-lease capabilities.

## 2. Mulgae

### 2.1 Queue reviews on Haepari's advisory lock, plus rate-limit safeguards

**Why.**
- **Observation (owner, 2026-09-28).** One review with six or seven concurrent roles works. Two reviews at once sometimes fail.
- **Decision (owner).** Each ordinary review queues in Haepari and runs when its turn comes. See Haepari [ADR-0017](architecture-decision-records/0017-advisory-named-locks.md) and the [lock contract](contracts/locks.md).

**Verified facts (checked 2026-09-28).**

- The [Z.AI usage policy](https://docs.z.ai/devpack/usage-policy) says: "Rate (concurrency) limits are tied to your plan tier. The platform dynamically adjusts these limits based on resource availability…"
  - Lite is recommended for one project at a time.
  - Pro is recommended for one or two projects.
  - Max is recommended for two or more projects.
- Mulgae maps the rate-limit attempt condition to `transitionActionFailClosed` (`internal/app/review/policy.go:318-322`).
- The MCP error for a rate-limited review is `provider_rate_limited` with `Retryable: false` (`internal/entrypoint/mcp/tools.go:516`).
- Commit `f5cf064` (2026-08-12) removed the OS-user-global provider-role flock. Its trailers include:
  - "Rejected: Scope the provider lock by project or run | no shared provider resource requires hidden coordination"
  - "Directive: Do not reintroduce OS-user-global provider-role lock roots; serialize only genuinely shared mutable state"
  - "Not-tested: Live concurrent ZCode and AGY provider execution"

  The provider policy above treats the account's concurrency limit as a shared resource. The owner's observation points the same way.

**Observations to re-measure.** The design review aggregated these from local `.mulgae` records across 24 roots. They are correlations only, and Haepari did not re-verify them.

- Of 310 ZCode review runs with a reconstructable start time, 51 (16%) had a rate-limited role. All 82 rate-limited role failures were ZCode.
- In the last three days the rate-limited runs were 45 of 241: 7/70 (09-25), 16/83 (09-26) and 22/88 (09-27).
- Rate-limited share by the number of ZCode roles already running at run start:

  | Concurrent ZCode roles | Rate-limited share |
  | --- | --- |
  | 6 or fewer | 7% (11/166) |
  | 7–12 | 18% (16/88) |
  | more than 12 | 43% (24/56) |

- 81 of the 82 rate-limited invocations carried `retry-after` (3–13 s) and `x-should-retry: true`. The ZCode client retried with backoff (median 13.5 attempts over 116 s) before failing. Every failure ended at the initial attempt, because Mulgae does not rerun rate-limited roles.

**Requested work.** Mulgae decides the details and allocates the IDs.

1. **L1 — queue each review on Haepari's lock (primary).** This needs the Haepari v0.1.0 capability `advisory-lock/v1`; see the [lock contract](contracts/locks.md).

   **Where to acquire.** MCP `RunReview` reuses the CLI argument parsing (`internal/composition/mcp_backend.go:117-137`), so reviews start through one shared path. Acquire `mulgae-review` exactly once there:
   - Persist the request key and payload.
   - Acquire and wait inside the review run.
   - Renew the grant from the running review process at least every liveness window (120 s by default).
   - Release on every path: success, failure and cancellation.

   **Avoid double acquisition.** `use-mulgae` does not acquire a separate lock. A caller that already holds a grant passes it through `HAEPARI_LOCK_GRANT`, and Mulgae then skips acquisition.

   **Hold bound.** Set the lock's `max_hold_seconds` from the run deadline Mulgae's preflight reports. That deadline grows when roles exceed lanes. With 60-minute role timeouts:

   | Configuration | Run deadline |
   | --- | --- |
   | lanes ≥ roles | about 7,207 s |
   | seven roles on six lanes | about 14,409 s |
   | seven roles on three lanes | about 21,611 s |

   The example lock allows 21,600 s; raise it when needed.

   **If Haepari is unavailable,** fail with a clear message by default and allow an explicit user override. Mulgae decides this behavior.

   **Until L1 ships,** run reviews one at a time, or adopt M1 below.
2. **M0 — measure (no code, about 7 days, before and after L1).** Record the rate-limited ZCode run ratio, p95 run duration and lock queue wait. The baseline is above: 16% overall, 25% on 2026-09-27.
3. **M1 — account-scoped cross-process slots (optional).** This covers calls that skip the lock. Use a counting semaphore keyed by provider account, for example N flock-held slot files; set N to one review's role count (6–7). If adopted, record a decision that supersedes the `f5cf064` directive.
4. **M2 — one retry after a rate limit (optional).** Retry once after waiting for capacity. Add no further time-based backoff layer, because the client already honors `retry-after`.

**Suggested acceptance.** Over seven days of comparable load after L1:

- concurrent-review rate-limit failures stop
- the rate-limited ZCode run ratio is 5% or less
- review queue waits are recorded

**Boundaries.**

- Haepari never starts, observes or interprets a review.
- Mulgae keeps native partial-role recovery, rerun lineage, retained results and composition.
- The lock is advisory: a review started without it is not coordinated.

### 2.2 Managed VM adoption (later, through Aquarium:TASK-092)

- The Sorage draft [02-mulgae](rollout-requests/02-mulgae.md) covers managed VM adoption only.
- It is sent after the Haepari release and after the Aquarium pilot is accepted.
- It no longer carries the rate-limit investigation; that is §2.1.

## 3. Not requested

- Haepari requests no direct change from the other producers. Their VM adoption drafts are sent by Aquarium in its own order.
- Haepari does not schedule individual provider calls. It only offers the review-level advisory lock.
