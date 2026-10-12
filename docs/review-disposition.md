# Design review disposition: Revision 3.5


## Local development scope agreement (Design Revision 3.5)

Status: Agreed after correction and focused Grok re-review on 2026-10-12. Master authorized the scope revision and iterative review. Grok reported “AGREED” with no remaining actionable contradictions or defects in the revised documentation scope. This agreement claims no new runtime observation or task acceptance.

[ADR-0020](architecture-decision-records/0020-local-development-scope.md) owns the scope decision. The [roadmap](roadmap.md#hardware-and-environment-gates) retains all task IDs, actual prerequisites and lifecycle states. TASK-016/TASK-038 are accepted historical real-UTM qualification, not recurring live prerequisites for TASK-017–026. TASK-027 alone owns the new exact-packaged-candidate small running-VM auto/manual observation. TASK-028 retains its actual dependency on TASK-027: local handoff preparation may continue, but final producer handoff cannot precede release acceptance. A missing VM blocks that selected new live gate and final handoff, not independent local development.

The supported target is one macOS arm64 machine, one user and one already-running registered persistent VM. Normal authorized Jobs reuse the established finite development scope across candidate changes. Sealed inputs, truthful results, required evidence retention, unknown-execution fencing, cleanup and original-run reconnection remain mandatory under F1/F2. No public profile decoder, runtime recovery relaxation or mandatory per-Job approval file is introduced. TASK-017–020 still own the planned generic integration and narrowed F3; editing this design does not implement those features.

The initial Grok review used two completed specialist reviews (scope/authority and roadmap/contracts). Its validator specialist did not complete; Grok's parent performed a static validator review, and the implementation agent independently reproduced and corrected a traceability defect. The verification below is offline documentation/tooling evidence, not VM acceptance. Historical review sections below retain their original revisions.

| Finding | Disposition and concrete correction |
| --- | --- |
| G-01: Historical VM gates block local successors | Resolved. Separate accepted historical tasks from the current live gate and compute only TASK-027/TASK-028's current dependency closure. Regression fixtures reject local tasks added to that closure or the final handoff omitted. TASK-028's actual prerequisite is preserved. |
| G-02: Old DF release deadlines survive in records/gaps | Resolved. Add current-scope annotations to the F2 freeze, TASK-038 record, operating guidance and dossier. DF-001–004 remain uncertain optional hardening without a v0.1.0 deadline; historical observations are unchanged. |
| G-03: Completion scope is ambiguous | Clarified. “Every mandatory requirement” refers to the Requirements section, excluding consumer-owned/deferred sets. Required new live evidence blocks its own gate; unrelated local tasks continue. |
| G-04: Optional inventory guidance hides required settlement; envelope decoder implied | Resolved. Separate mandatory managed evidence retention/settlement into its own section. Consumer environment-lock bytes are opaque selected files, with format/compatibility validation owned by the consumer. |
| G-05: TASK-039/dossier imply a full rich environment-lock F3 | Resolved. Annotate the historical handoff with generic Make/guest entrypoint, opaque profile reference and general manual results; keep the original local observation and limits. |
| G-06: Mulgae completion packet appears to gate Haepari | Clarified. The packet applies only to a consumer-selected Mulgae rollout under its policy; Haepari producer completion follows its own handoff. No external consumer policy is changed. |
| G-07: Roadmap revision label | Resolved. Target is Design Revision 3.5. |
| G-08: Generic Make requirements classified as real UTM | Resolved. HP-REQ-032/033 use fixture/unit and local integration evidence. |
| G-09: V58 implies live expiry/two-VM fault campaigns | Clarified. Fault invariants and logical VM isolation use bounded local/fake-native fixtures. Historical TASK-016 evidence remains; actual expiry/second-VM experiments stay optional DF hardening. |
| G-10: Guest context requires installation | Resolved. Job context is required before executing the generic guest entrypoint. |
| G-11: Power-loss experiments remain a later gate | Resolved. Historical UTM observations and the current small release loop are distinguished; power-loss durability remains unqualified, with optional experiments unless advertised. |
| G-12: VM setup requires product candidate installation | Resolved. Setup uses read-only existing-target preflight; selected generic Jobs check execution/cleanup within their scope. Installation is a consumer recipe choice. |
| G-13: New small release criterion lacks an owner | Resolved. HP-REQ-031, traceability and TASK-027 coverage explicitly assign new exact-release evidence to TASK-027 while preserving historical owners. |
| G-14: Haepari self-test implies the large VM campaign | Clarified. HP-REQ-059 uses the small running-VM release loop; the thirty-Job/50 MiB campaign is optional historical tooling. |
| G-15: Old ADR statuses still imply release implementation | Resolved. ADR-0008/0009 retain their original rationale as consumer guidance with release obligations superseded by ADR-0020; no historical F1/F2 observations are invented. |
| G-16: Scope tests do not catch semantic contradictions | Partly adopted. Add structural dependency-closure regressions and document their limit. Arbitrary prose semantics require review; literal phrase-matching tests would not establish correctness and are not added. |
| Additional validator defect | Resolved. Each mandatory requirement must own at least one verifying case, even if other rows cover the global case union. Duplicate traceability rows fail rather than hiding an invalid earlier row. Both have behavioral regressions. |

The implementation prompt now separates generic core invariants from optional consumer installation/skill/migration assertions. This is a documentation and documentation-validator correction; it changes no runtime Go code, service, VM, external repository, task status, staging or release publication.

Verification: `UV_OFFLINE=1 make test-qualification` passed all 84 offline checks; documentation validation and relative-link checks passed. Task IDs, actual prerequisites and lifecycle states match HEAD; the index is empty. Automated checks cover structural rules, not arbitrary prose. Grok read the corrected source and delta without rerunning these checks, and explicitly agreed with the TASK-027/TASK-028 distinction and partial adoption of G-16. Its agreement is a documentation/tooling review, not independent product or VM verification. The synthetic dependency tests check structural behavior; non-write documentation validation also checks the actual roadmap. No further corrective review round was needed after the first re-review. Product/VM checks are outside this documentation-only change; TASK-027's future required live evidence remains unobserved.

## Pre-implementation correction (Revision 3.4)

The staged Revision 3.3 package was re-read before correction. Five contract gaps were present: automatic VM stops could select a paused maintenance VM or race reservations; failed collection could delete required guest evidence; verdict rules disagreed; lease freshness used an unsafe OR; and borrowed advisory grants lacked ownership rules. Revision 3.4 resolves them in the [resource](contracts/resource-and-runner.md), [managed transition](contracts/managed-transitions.md), [artifact](contracts/artifact-and-profile.md) and [lock](contracts/locks.md) contracts, with Task and V-case ownership in [traceability](traceability.md). TASK-014 gains an optional early observation; TASK-016/F2 and the accepted TASK-034 record are unchanged. Older review dispositions below remain historical accounts of their own drafts, not current permission to stop an unprotected VM.

This register records two external design reviews, both dated 2026-09-28:

- **R-A:** review of Design Revision 2.
- **R-B:** re-review of Design Revision 3.

The owner holds the full review report outside this repository. Revision 3 was written without that report; Revision 3.1 used it. This register is self-contained: it summarizes each finding and records its disposition, so it can be checked without the report.

Dispositions:

| Disposition | Meaning |
| --- | --- |
| Resolved (3) or (3.1) | Addressed in that revision. |
| Deferred | Kept as a re-entry condition for deferred work. |
| External request | Requested from another repository in [external requests](to-external.md). |
| Not adopted | Considered and recorded, but not taken. |
| Open (owner) | Waits for an owner decision. |

## R-A: review of Design Revision 2

### Blockers

| ID | Finding | Disposition | Evidence |
| --- | --- | --- | --- |
| A-B1 | External capacity strands after owner loss; no recovery path | Deferred; re-entry blocker | [External lease contract](contracts/external-leases.md#re-entry-blockers-b1-and-b2) |
| A-B2 | Continuation dead end after a cancelled or expired successor; duplicate roots | Deferred; re-entry blocker | Same section |
| A-B3 | Hold-and-wait deadlock from nested acquisition under a shared global limit | Resolved (3) for submission. Resolved (3.1) for nested blocking waits and context chains | [Nested acquisition](contracts/managed-transitions.md#nested-acquisition); V38, V39, V45 |

### Platform facts missing from the design

| ID | Finding | Disposition | Evidence |
| --- | --- | --- | --- |
| A-P1 | `utmctl` needs a logged-in GUI session, Automation (TCC) approval and stable code signing | Resolved (3.1): Aqua LaunchAgent. Revised (3.3): the TASK-034 probe needed no signing identity, and `utmctl` raised no Automation prompt, so the signing requirement was dropped; qualified in TASK-016 | [Architecture](architecture.md#durability-and-service-supervision); [resource contract](contracts/resource-and-runner.md#host-session-and-vm-identity); V47 |
| A-P2 | At most two concurrent macOS guests per host | Resolved (3.1): `host_macos_guest_limit`; a framework limit error is a temporary gap | [Configuration](contracts/configuration.md); V49 |
| A-P3 | Apple Virtualization has no guest agent; address discovery; clones keep MAC and serial | Resolved (3.1); revised (3.3): DHCP lease by the registered MAC, boot nonce, and refusal of a duplicate UUID or MAC; the persistent-VM design ([ADR-0018](architecture-decision-records/0018-persistent-vm-no-reset.md)) no longer clones, so clone identities do not arise | [Resource contract](contracts/resource-and-runner.md); V50, V58 |
| A-P4 | The service manager kills runners together with the controller | Resolved (3.1): `AbandonProcessGroup=true` and runners in their own session; verified in TASK-011 | [Architecture](architecture.md#durability-and-service-supervision); V46 |
| A-P5 | Sleep/wake clock semantics undefined | Resolved (3) | [Deadlines](contracts/managed-transitions.md#deadlines-and-restart); V40 |
| A-P6 | macOS fsync does not guarantee power-loss durability | Resolved (3.1): `fullfsync` and `checkpoint_fullfsync`; the claim is blocked if the driver ignores them | [Architecture](architecture.md#durability-and-service-supervision); V56 |
| A-P7 | A beta UTM snapshot/disposable mode makes the no-snapshot premise stale | Resolved (3) with `reset_strategy`. Not adopted for v0.1.0 (3.1) | [ADR-0016](architecture-decision-records/0016-managed-first-scope.md) |

### Plan and document quality

| ID | Finding | Disposition | Evidence |
| --- | --- | --- | --- |
| A-1 | Fully serial 32-step roadmap | Resolved (3.1): actual-prerequisite DAG; real UTM gates only TASK-034, TASK-016, TASK-027 and TASK-028 | [Roadmap](roadmap.md#hardware-and-environment-gates) |
| A-2 | No walking skeleton; schemas frozen before evidence | Resolved (3) with TASK-033. Resolved (3.1) with freeze gates F1, F2 and F3 | [Roadmap](roadmap.md#contract-freeze-gates) |
| A-3 | No external lease transition table | Deferred with EPIC-008 | [External lease contract](contracts/external-leases.md) |
| A-4 | Operator recovery, automatic reclaim and nesting rules | Resolved (3) for managed recovery. Deferred for external leases | [Recovery contract](contracts/cli-and-jobs.md#recovery-mutation-contract) |
| A-5 | Task and epic IDs collide with Aquarium IDs | Resolved (3.1): unqualified IDs are Haepari IDs; other repositories are always qualified | [README](README.md#document-authority) |
| A-6 | Verification cases not traced to requirements | Resolved (3) with the traceability table. Resolved (3.1) with semantic fixes and V43–V58 | [Traceability](traceability.md) |
| A-7 | Skill and migration verification owned by both Haepari and consumers | Resolved (3) | [Requirements](requirements.md) HP-REQ-036–044 |
| A-8 | No committed cross-repository baseline | External request for Aquarium. The Haepari commit is the owner's decision | [External requests](to-external.md) §1.1 |
| A-9 | No glossary | Resolved (3) for the managed scope | [Glossary](contracts/cli-and-jobs.md#machine-outcomes-and-glossary) |
| A-10 | Configuration and command surface missing | Resolved (3). The configuration contract moved into contracts in 3.1 | [Configuration](contracts/configuration.md) |
| A-11 | Examples disagree with contracts | Resolved (3.1): `resource_availability`, complete config fields, VM queue deadline, guest suite check | [Examples](examples/README.md) |
| A-12 | Bootstrap information missing | Resolved (3.1) as proposed defaults; the owner confirmed MIT in 3.2 | [TASK-001](roadmap/EPIC-001.md#task-001) |
| A-13 | Managed state model incomplete | Resolved (3.1) | [Transition table](contracts/managed-transitions.md) |
| A-14 | Exit code 3 has two meanings | Resolved (3). The deferred contract was aligned in 3.1 | [CLI contract](contracts/cli-and-jobs.md#json-envelopes-and-exit-codes) |
| A-15 | No Must/Should marking; first-release storage migration contradiction | Resolved (3.1): every active requirement is mandatory. Resolved (3): storage verification classes | [Requirements](requirements.md); [storage classes](verification.md#storage-verification-classes) |
| A-16 | Aquarium inspector warning for the Cancelled status | External request | [External requests](to-external.md) §1.3 |
| A-L1 | ADR index missing ADR-0014 and ADR-0015 | Resolved (3.1) | [ADR index](architecture-decision-records/README.md) |
| A-L2 | Rollout drafts are nearly identical | Not adopted: the drafts are Aquarium-owned. The rollout-scope proposal is an external request | [External requests](to-external.md) §1.4 |
| A-L3 | Personal absolute paths in shared documents | Resolved (3) | [Sources](sources.md) |
| A-L4 | Makefile has no `PROFILE` variable | Resolved (3) | [Makefile example](examples/Makefile.example) |

### Usefulness

| ID | Finding | Disposition | Evidence |
| --- | --- | --- | --- |
| A-U1 | External leases do not address the measured pain (provider rate limits) | Resolved (3): external leases deferred. Resolved (3.2): reviews queue on the advisory lock (owner decision); Mulgae safeguards are optional | [ADR-0017](architecture-decision-records/0017-advisory-named-locks.md); [external requests](to-external.md) §2.1 |
| A-U2 | Conflicts with the Mulgae `f5cf064` directive | Resolved (3) on the Haepari side. The re-decision is requested from Mulgae | [External requests](to-external.md) §2.1 |
| A-U3 | Local command resource overlaps Gaori | Resolved (3.2) by owner decision: Gaori runs local tests, `make test-vm` is a separate VM path, and the general resource model stays for later resources | [README](README.md#settled-decisions) |
| A-U4 | VM value unproven; pilot before building | Not adopted as a replacement. Pilot metrics are requested from Aquarium, and TASK-034 probes early | [ADR-0016](architecture-decision-records/0016-managed-first-scope.md); [external requests](to-external.md) §1.4 |

## R-B: re-review of Design Revision 3

### High

| ID | Finding | Disposition | Evidence |
| --- | --- | --- | --- |
| B-H1 | The Aquarium plan conflicts with the managed-first scope | External request (§1.1–1.2). Haepari side Resolved (3.1): proposed sequence, HP-REQ-060, TASK-028 | [Adoption copy](aquarium-adoption.md); [requirements](requirements.md) HP-REQ-060 |
| B-H2 | DAG errors: sink tasks, a UTM gate over local work, a freeze completion cycle | Resolved (3.1) | [Roadmap](roadmap.md); [package validation](package-validation.md) |
| B-H3 | Untrue "passed" claims and remaining scope remnants | Resolved (3.1): EPIC-008 wording, qualified IDs, HP-REQ-077 owner, counts; checks regenerated by script | [Package validation](package-validation.md) |

### Medium

| ID | Finding | Disposition | Evidence |
| --- | --- | --- | --- |
| B-M1 | Transition table gaps and contradictions | Resolved (3.1) | [Transition table](contracts/managed-transitions.md); V51, V53 |
| B-M2 | Prerequisite handling contradicts itself; "rejection key" undefined; queued input corruption missing | Resolved (3.1) | [Architecture](architecture.md); [transition table](contracts/managed-transitions.md); V52 |
| B-M3 | Nested-acquisition gaps: blocking wait, context overwrite, stripped environment | Resolved (3.1) for waits and chains. Environment stripping stays a documented limit | [Nested acquisition](contracts/managed-transitions.md#nested-acquisition); V45 |
| B-M4 | Platform decisions still deferred as qualification points | Resolved (3.1) | [Architecture](architecture.md#durability-and-service-supervision); [TASK-004](roadmap/EPIC-001.md#task-004); [TASK-011](roadmap/EPIC-003.md#task-011) |
| B-M5 | Early UTM spike removed | Resolved (3.1) with TASK-034 | [TASK-034](roadmap/EPIC-004.md#task-034) |
| B-M6 | VM identity enforcement and the host guest limit | Resolved (3.1) for identity enforcement and the host limit. The suggested automatic requeue is Not adopted (decision 4); VM resources pause instead, so queued jobs wait | [Resource contract](contracts/resource-and-runner.md); V49, V50, V58 |
| B-M7 | No rule for choosing a baseline for a profile | Resolved (3.1): one baseline per resource, `PROFILE_MISMATCH` | [Resource contract](contracts/resource-and-runner.md#resource-definition) |
| B-M8 | Mixed resource state vocabulary | Resolved (3.1) | [Resource availability](contracts/managed-transitions.md#resource-availability) |
| B-M9 | Traceability links that verify nothing; missing cases | Resolved (3.1) | [Traceability](traceability.md); V43–V58 |
| B-M10 | VM job time budget exceeds the queue deadline | Resolved (3.1) | [Configuration](contracts/configuration.md#queue-deadlines-for-capacity-one-resources) |
| B-M11 | TASK-033 scope unclear | Resolved (3.1) | [TASK-033](roadmap/EPIC-001.md#task-033) |
| B-M12 | Bootstrap decisions unresolved | Resolved (3.1) as proposals; the owner confirmed MIT in 3.2 | [TASK-001](roadmap/EPIC-001.md#task-001) |
| B-M13 | Alias replay, protocol negotiation, receipt protocol and guest runner delivery undefined | Resolved (3.1) | [CLI contract](contracts/cli-and-jobs.md); [runner receipts](contracts/resource-and-runner.md#runner-receipts) |
| B-M14 | Incomplete disposition; ADR-0016 has no alternatives | Resolved (3.1) | This register; [ADR-0016](architecture-decision-records/0016-managed-first-scope.md) |

### Low

| ID | Finding | Disposition | Evidence |
| --- | --- | --- | --- |
| B-L | Status column missing; mismatched titles; stale ADR headings; deferred exit code; missing config limits; default global limit versus V09 | Resolved (3.1) | [Roadmap](roadmap.md); [ADR index](architecture-decision-records/README.md); [configuration](contracts/configuration.md); [verification](verification.md) |

### Usefulness

| ID | Finding | Disposition | Evidence |
| --- | --- | --- | --- |
| B-U1 | The Mulgae rate-limit handover is not actionable and is gated late | Resolved (3.1): an independent request with evidence and acceptance criteria | [External requests](to-external.md) §2.1 |
| B-U2 | Rollout breadth; aquarium-dev retirement tied to all producers | External request (advisory) | [External requests](to-external.md) §1.4 |
| B-U3 | Infrastructure built before VM value is validated | Not adopted as a replacement. Pilot metrics requested; TASK-034 moved early | [ADR-0016](architecture-decision-records/0016-managed-first-scope.md) |

## R-C: independent review of the Revision 3.1 draft

An independent read-only review of the draft returned REVISE. After the findings were fixed, a follow-up check by the same reviewer returned ACCEPT-WITH-RESERVATIONS. Its six remaining wording notes were then fixed, and the validation script was rerun:

- the capacity-rule wording
- a host-wide pause scope
- the verdict effect of a late cancel
- the order of F3 and F2
- VM fields that stay provisional until F2
- the rationale for the `global_limit` default

| ID | Finding | Disposition | Evidence |
| --- | --- | --- | --- |
| C-H1 | Successful settlement set the resource to `available` unconditionally, which cleared a pause or another job's quarantine | Resolved (3.1): settlement never clears `paused` or `quarantined` | [Capacity rule](contracts/managed-transitions.md#capacity-rule); V51 |
| C-M1 | ADR-0004 still bound the request key to resolved identities | Resolved (3.1) with an explicit amendment | [ADR-0004](architecture-decision-records/0004-immutable-admission.md) |
| C-M2 | The architecture still said unknown state yields INCOMPLETE | Resolved (3.1): it stays `reconciling` until recovery settles it | [Architecture](architecture.md) |
| C-M3 | Followed events or logs bypassed the nested-wait rejection | Resolved (3.1): every blocking observation is rejected | [Nested acquisition](contracts/managed-transitions.md#nested-acquisition); V45 |
| C-M4 | The host-capability block was undefined, so a VM-limit error could drain the queue | Resolved (3.1): VM resources pause with a recorded reason | [Resource availability](contracts/managed-transitions.md#resource-availability); V49 |
| C-M5 | F1 froze the manual result format before TASK-019; the environment lock was in no gate | Resolved (3.1): new gate F3 in TASK-020; TASK-020 follows TASK-019 | [Freeze gates](roadmap.md#contract-freeze-gates) |
| C-M6 | TASK-025 cited TASK-016 qualification without that prerequisite | Resolved (3.1) | [TASK-025](roadmap/EPIC-007.md#task-025) |
| C-M7 | This register overstated two resolutions | Resolved (3.1): the V04 link was removed and B-M6 was corrected | [Traceability](traceability.md) |
| C-M8 | Requested Aquarium deletions were too broad, and Aquarium TODO `:233` was missing | Resolved (3.1): line-exact requests | [External requests](to-external.md) §1.2 |
| C-M9 | The validation script did not cross-check counts, could pass on zero parsed items, used a hard-coded gate set, and ignored colliding IDs | Resolved (3.1): count cross-checks, minimum-count guards, and a check of the stated gate set against the DAG were added. Colliding IDs rely on the qualification convention and a printed review list | [Validation script](tools/validate_docs.py) |
| C-M10 | Two derived baselines could share a MAC address | Resolved (3.1); superseded (3.3): baselines are gone, and registration refuses a duplicate UUID or MAC | [Resource contract](contracts/resource-and-runner.md); V50 |
| C-L | Probe context and VM count; development signing; templates; config maximum, defaults and key names; external request counts and quotes; cancel rows for late phases; missing error codes; inspector location; source conflict; ADR-0016 rationale; wording | Resolved (3.1); the signing part was superseded in 3.3 | See the linked files |

**Answers to the review's open questions.**
- Recovery verification boots count toward the host guest limit.
- In the queue-deadline rule, "jobs ahead" includes the running job, and manual jobs use their manual deadline.
- S11 was checked against the macOS Sequoia license, so the installed release's license must be rechecked.
- `PROFILE_MISMATCH` applies only to `utm-macos` resources.

## R-E: independent review of the Revision 3.2 draft

The review returned REVISE: the direction, counts and traceability were correct, but there were contract defects. After the fixes, a follow-up check by the same reviewer returned ACCEPT-WITH-RESERVATIONS and raised seven minor notes. All seven were fixed before Revision 3.2 was completed, and the validation script was rerun:

- live grants get a fresh liveness window on startup and wake
- returned grants are renewed so they count as claimed
- Mulgae's run deadline grows with roles per lane
- the V-case range in the handoff
- the exit code of a replayed release
- acquisition while a lock is paused
- the reason for rejecting connection-bound locks

| ID | Finding | Disposition | Evidence |
| --- | --- | --- | --- |
| E-1 | rev3.1 wording contradicted the lock: "Only managed resources are registrable", "Mulgae review scheduling is not part of the architecture", "publishes only managed capabilities", "Jobs only" | Resolved (3.2) | [Architecture](architecture.md); [requirements](requirements.md) HP-REQ-077; [ADR-0016](architecture-decision-records/0016-managed-first-scope.md) amendment |
| E-2 | External requests still said reviews need no Haepari admission and asked to delete the direct-call limitation | Resolved (3.2): row 242 is now Keep | [External requests](to-external.md) §1.2; [02-mulgae](rollout-requests/02-mulgae.md) |
| E-3 | The maximum hold (7,200 s) was below Mulgae's run deadline; the hold deadline doubled as the liveness timeout | Resolved (3.2): 120 s liveness renewal and a 6 h maximum hold | [Lock contract](contracts/locks.md#lock-resource) |
| E-4 | A grant nobody claims could be held idle; cancelling a granted request was undefined; operators could not see IDs | Resolved (3.2): liveness ends unclaimed grants; cancel releases a grant; `locks list` shows IDs | [Lock contract](contracts/locks.md#transitions) |
| E-5 | Replaying an expired grant exited 0; expiry states were ambiguous | Resolved (3.2): replay reports the current state, ended requests exit 4, and `queue_expired` and `hold_expired` are separate | [Lock contract](contracts/locks.md#exits-and-errors) |
| E-6 | `use-mulgae` and the Mulgae CLI could both acquire | Resolved (3.2): acquisition happens once, at Mulgae's shared review start, and `HAEPARI_LOCK_GRANT` is reused | [Lock contract](contracts/locks.md#typical-use); [external requests](to-external.md) §2.1 |
| E-7 | Resource commands were undefined for locks | Resolved (3.2) | [Lock contract](contracts/locks.md#lock-resource); V62 |
| E-8 | Force release did not hand off atomically; deadline order was unclear; exits clashed with the job exit table; locks were missing from the nested rule | Resolved (3.2) | [Lock contract](contracts/locks.md); [nested acquisition](contracts/managed-transitions.md#nested-acquisition) |
| E-9 | Key namespace, stored payload and lock timestamps were undefined | Resolved (3.2) | [Lock contract](contracts/locks.md#commands); [architecture](architecture.md) tables |
| E-10 | The no-takeover principle stated no lock exception; the provider-lock alternative was not answered | Resolved (3.2) | [Architecture](architecture.md); [ADR-0017](architecture-decision-records/0017-advisory-named-locks.md) |
| E-11 | `timings` could not separate restore from boot-to-SSH and had no case | Resolved (3.2): adapter steps and V65 | [CLI contract](contracts/cli-and-jobs.md); V65 |
| E-12 | The lock contract sat in no freeze gate; interim guidance for Mulgae was dropped | Resolved (3.2): the lock contract is part of F1 (TASK-012 now follows TASK-035), and "until L1 ships" guidance was restored | [Freeze gates](roadmap.md#contract-freeze-gates); [external requests](to-external.md) §2.1 |
| E-13 | Sleep overlap, missing exit cases, stale revision labels, per-profile throughput | Resolved (3.2) | [Lock contract](contracts/locks.md#typical-use); V64; [development-loop profile](contracts/configuration.md#development-loop-profile) |

## Owner decisions (2026-09-28)

The open decisions left by Revision 3.1 were answered by the owner and applied in Revision 3.2.

| Decision | Answer | Where recorded |
| --- | --- | --- |
| License | MIT; Go module path `github.com/irootkernel/haepari` | [TASK-001](roadmap/EPIC-001.md#task-001); [README](README.md#settled-decisions) |
| VM test cadence | Part of the development loop, with 10–30 VM jobs a day expected | [Development-loop profile](contracts/configuration.md#development-loop-profile); [verification](verification.md#resource-and-performance-checks) |
| Local-command resource versus Gaori | No overlap in intent: Gaori runs local tests; Haepari's UTM use is VM end-to-end testing through `make test-vm`; the general resource model stays so that other resources can be added later | [README](README.md#settled-decisions) |
| Mulgae reviews | Queue in Haepari and run when granted, through the advisory lock `mulgae-review` | [ADR-0017](architecture-decision-records/0017-advisory-named-locks.md); [lock contract](contracts/locks.md); [external requests](to-external.md) §2.1 |
| Z.AI plan tier | Not stated. The owner reports that one review with six or seven roles works and two concurrent reviews sometimes fail | [External requests](to-external.md) §2.1 |

**Still open, and owned by Mulgae:** the behavior when Haepari is unavailable, and whether to add account slots.

## R-F: independent review of the Revision 3.3 draft

One read-only reviewer returned REVISE. Most of the rewrite was consistent; the findings below were all applied.

| ID | Finding | Disposition | Where |
| --- | --- | --- | --- |
| F-1 | V18 still tested a clone/registration failure, and HP-REQ-025 lost its only normal case | Resolved (3.3): V18 now runs a second job on the persistent VM | [Verification](verification.md) |
| F-2 | Leftover reset wording: implementation prompt, "reusable images", "Baseline selection policy", "dirty resource", VM rollback, image and dictionary wording | Resolved (3.3) | [Handoffs](handoffs/implementation-prompt.md); [architecture](architecture.md); [artifact contract](contracts/artifact-and-profile.md); [requirements](requirements.md); [operations](operations.md) |
| F-3 | ADR-0005 treated persisting guest changes as contamination; ADR-0006 and ADR-0007 said Haepari owns the VM | Resolved (3.3): ADR-0005 amended; the operator owns the VM | [ADR-0005](architecture-decision-records/0005-cleanup-before-release.md); [ADR-0006](architecture-decision-records/0006-utm-ssh.md); [ADR-0007](architecture-decision-records/0007-project-owned-tests.md) |
| F-4 | `host_macos_guest_limit` had three meanings and no rule for a second resource | Resolved (3.3): at the limit, Haepari force-stops an idle registered VM (owner decision); a job waits only while every running registered VM holds a job | [Resource contract](contracts/resource-and-runner.md#host-session-and-vm-identity); [configuration](contracts/configuration.md); V49 |
| F-5 | A persistent VM fault failed every queued job; statuses other than `stopped` were undefined | Resolved (3.3): `vm_unavailable` pause; any status other than `started` or `stopped` is unavailable | [Resource contract](contracts/resource-and-runner.md); V58; [operations](operations.md) |
| F-6 | Probe overclaims: guest OS, session requirement, "every operation", Automation approval, `stop --request`, "exact versions", approving identity | Resolved (3.3): wording narrowed to what the record shows; the record now names the responsible executable | [Probe record](records/TASK-034-utm-probe.md) |
| F-7 | Aliases resolved by Haepari at admission, with no profile registry left | Resolved (3.3): consumers resolve aliases before submitting; V54 reworded | [Architecture](architecture.md); [README](README.md#settled-decisions) |
| F-8 | The UUID↔MAC binding was never verified | Resolved (3.3): the MAC is operator-asserted and confirmed at every Prepare through the lease and the pinned host key | [Resource contract](contracts/resource-and-runner.md) |
| F-9 | Backups keep the MAC; product daemons in a persistent guest looked like escaped processes; SSH shutdown and the guest marker had no configuration | Resolved (3.3): backup warning; Haepari owns only the runner's session and process group; no shutdown in normal operation; the guest runner provides the job context | [Operations](operations.md); [architecture](architecture.md); [CLI contract](contracts/cli-and-jobs.md) |
| F-10 | Aquarium-facing text: two VM resources in §1.4, missing TODO rows, stale revision label, shared-VM expectations for producers | Resolved (3.3); Aquarium's plan was also found committed (`910ef29`), and line numbers now refer to `0c5330b`. The owner then kept the two-VM development-loop profile, so §1.4 states two VMs again | [External requests](to-external.md); [rollout requests](rollout-requests/01-podway.md) |
| F-11 | Roadmap and status statements still said TASK-034 was blocked by a missing UTM environment and that nothing was accepted | Resolved (3.3) | [Roadmap](roadmap.md); [README](README.md) |
| F-12 | HP-REQ-035 traced to V50, which now tests registration | Resolved (3.3): V35 and V54 | [Traceability](traceability.md) |

**Open question from the review.** Observing Apple's two-guest limit needs three macOS VMs. TASK-016 records the limit as unobserved if they are not available.

## Owner decisions (2026-09-28, Revision 3.3)

After the TASK-034 probe, the owner decided how VMs are used. Revision 3.3 applies these decisions.

| Decision | Answer | Where recorded |
| --- | --- | --- |
| VM state between jobs | No reset. One persistent VM per resource is used continuously in queue order; consumer recipes set up and clean up their own state | [ADR-0018](architecture-decision-records/0018-persistent-vm-no-reset.md); [resource contract](contracts/resource-and-runner.md#utm-control); [EPIC-004](roadmap/EPIC-004.md) |
| Code signing for local services | Not required; ad-hoc builds are enough, because `utmctl` raised no Automation prompt | [Architecture](architecture.md#durability-and-service-supervision); [probe record](records/TASK-034-utm-probe.md) |
| TASK-034 | Accepted from the 2026-09-28 probe on the owner's VM; the clone step in the old task text is no longer needed | [Roadmap](roadmap.md); [probe record](records/TASK-034-utm-probe.md) |
| Test state on a shared VM | Each job first removes its product's earlier test state and finishes by returning its product to the stable version | [Rollout requests](rollout-requests/01-podway.md); [external requests](to-external.md) §1.2 |
| Development-loop profile | Keep Revision 3.2's two VM resources, host guest limit two and global limit three; each VM is persistent | [Development-loop profile](contracts/configuration.md#development-loop-profile) |
| Profile aliases | The submitting project resolves them before submitting; Haepari keeps no alias table | [Architecture](architecture.md); [README](README.md#settled-decisions) |

**Functional-change audit (2026-09-28).** Two read-only reviewers compared the owner's Revision 3 with the Revision 3.3 draft. They listed each functional change with its approval source: an explicit answer, an approved plan, a consequence of an approved decision, a deviation from a plan, or no approval. Twelve items needed the owner's decision. The answers are below.

| Item | Before | Owner decision |
| --- | --- | --- |
| Lock expiry | The 3.2 plan: hold deadline 3,600 s default, 7,200 s maximum; the draft: 120 s liveness, 6 h maximum, 2 h default queue deadline | Keep liveness and the maximum hold; no default queue deadline |
| Where Mulgae takes the lock | The 3.2 plan: `use-mulgae`; the draft: Mulgae's shared review start | Mulgae's shared review start |
| Automatic pauses | The 3.1 plan: INCOMPLETE without automatic requeue; the draft: `host_vm_limit` and `vm_unavailable` pauses | Keep both pauses, with manual resume |
| VM power at the host guest limit | The draft: never stop an idle VM | VMs stay on; at the limit Haepari force-stops an idle registered VM |
| Freeze gates | The 3.1 plan: F1 and F2; the draft: F1, F2 and F3 | Keep three gates |
| Host guest limit in TASK-016 | The draft: may be recorded as unobserved | Approved |
| Release service signing | EPIC-007 still required it | Removed |
| MAC rules, dropped Z.AI plan-tier check, TASK-001 bootstrap list, assistant-chosen values | Added or chosen by the assistant | Approved as written |
