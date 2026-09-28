# Verification and release acceptance

No case in this document is marked executed. Each implementation task supplies focused evidence; the final release combines the exact artifact identities and results. Passing unit tests does not prove real UTM control, installed skill behavior, or human observations.

## Layers

| Layer | Scope | Environment |
| --- | --- | --- |
| Unit | State transitions, schemas, digests, queues, path validation, verdict derivation | Deterministic isolated fixtures |
| Integration | SQLite transactions, Unix API, process supervisor, event streaming, repeated requests, fake UTM/SSH | Local temporary state with controlled subprocesses |
| Real local E2E | Installed CLI and service controlling independent command resources | Isolated Haepari state root |
| Deferred external client E2E | R2 proposal, subject to B1/B2 redesign | Not a v0.1.0 release gate |
| Real UTM E2E | Actual VM ownership, preparation, SSH, detached execution, collection and cold restore | Explicitly registered test VM |
| Installed skills/manual | Discovery, behavior and operator observations | Fresh guest product session and approved provider scope |
| Migration | Old state, upgrade, regression, interruption/recovery | Exact prior and candidate installation units |

## Mandatory failure matrix

| Case | Scenario | Required invariant |
| --- | --- | --- |
| V01 | Two daemons start concurrently | One controller owns the state; the other cannot dispatch. |
| V02 | Two callers submit the same key concurrently | One admission and one workload; both callers see its ID. |
| V03 | Reply lost after durable admission | Same-key recovery returns the original job. |
| V04 | Same key with changed payload | Conflict before execution. |
| V05 | Purge an old finished job, then retry its key | Tombstone prevents a new workload. |
| V06 | Change/delete source bytes while queued | Sealed inputs are used; changed source is not reopened. |
| V07 | Traverse paths, inject links, duplicate case-folded paths | Rejection without writing outside owned roots. |
| V08 | Multiple jobs target one VM | At most one lease from prepare through reset. |
| V09 | Another independent resource has work (`global_limit` of at least 2) | It can progress while the VM is occupied or quarantined. |
| V10 | Daemon crashes after lease commit, before spawn | Reconcile; no blind redispatch. |
| V11 | Runner crashes during spawn acknowledgement | Inconclusive until actual state is established; no automatic repeat. |
| V12 | CLI wait ends or its connection closes | Job persists and can be reattached. |
| V13 | Workload forks a child or leaves stdout open | Bounded group/pipe handling; uncertainty is explicit. |
| V14 | Cancel races with a terminal exit | One consistent result and no stale resource release. |
| V15 | SSH disconnects while guest work runs | Same run is reattached; no duplicate install/test. |
| V16 | Guest reboots and an old PID is reused | Boot identity blocks incorrect process attachment. |
| V17 | Wrong SSH key, wrong VM UUID, wrong baseline | No candidate installation or cleanup of foreign state. |
| V18 | Clone succeeds but registration recording fails | Recover exact owned partial state; no name-based broad deletion. |
| V19 | Preparation fails after partially changing guest | Cleanup still executes under the lease. |
| V20 | Required logs are missing or host disk fills | No PASS; preserve evidence status and safe settlement. |
| V21 | VM reset fails | Resource is quarantined, next VM job cannot start. |
| V22 | Stale recovery token arrives after a new lease | Reject without touching the later job. |
| V23 | Manual environment is ready but no result exists | It remains awaiting_manual or finishes INCOMPLETE after recovery. |
| V24 | Two conflicting manual completions | Revision/conflict checks prevent overwritten outcomes. |
| V25 | Candidate skill has missing/extra/stale files | Installation verification fails; full reset restores baseline. |
| V26 | Shared/home/project duplicate skill exists | The consumer identifies the effective discovery conflict. |
| V27 | Skill bytes match but the native session cannot discover it | Discovery remains failed/unverified; file checks do not mask it. |
| V28 | Binary version unchanged, skill-only candidate | Correct candidate identity and behavior/restoration evidence are recorded. |
| V29 | Old version creates state; candidate upgrades | Data/configuration/identity invariants and post-upgrade writes hold. |
| V30 | Migration is interrupted or retried | Native recovery is safe; no duplicate logical work. |
| V31 | Busy work exists during update | The declared resume or refuse/drain policy is observed. |
| V32 | Candidate Haepari is tested | Stable controller and its real state/socket remain unchanged. |
| V33 | Haepari store is too new or migration fails | Refuse unsafe startup; retain recoverable state. |
| V34 | Live endpoint/credential prerequisites missing | INCOMPLETE, with no silent skip, authentication, or production fallback. |
| V35 | Resource baseline changes after request admission | The job retains its resolved identity or blocks explicitly. |
| V36 | Ordinary tests run without VM | Standard local gate works; explicit VM gate fails with a prerequisite gap. |

For Haepari release evidence, V25–V31 may use a small sample consumer with real prior sample-version state; each producer later supplies its own native migration and skill behavior observations. A test may cover several cases, but each case must map to an actual assertion. Fault injection stays bounded and private to tests. Production code cannot enable arbitrary failure injection by an untrusted job.

## Resource and performance checks

Design targets, to be measured rather than claimed: with 1,000 queued jobs and four active local jobs, controller memory excluding workload processes and OS file cache remains below 256 MiB. Stream a 1 GiB workload log while controller growth remains below 32 MiB after warm-up. Run a local-only submit/inspect/wait scenario without an unbounded polling loop and record latency, machine, build, load, and storage conditions; no universal latency promise is made. VM tests run in the development loop, and the owner expects 10–30 VM jobs a day. TASK-016 and TASK-027 therefore record VM cycle phase durations (restore, boot-to-SSH, collection, reset) and queue wait p50/p95, derived from result `timings`.

Set explicit request-size, per-stream log, artifact-count/size, and per-phase time limits in operator configuration. Required evidence may not be silently discarded to satisfy a limit. Limits and truncation must appear in the result. Limits are resource-protection settings, not a new restriction on an integrated tool's native semantic contract.

## Migration evidence

Retain source/target profile and artifact digests, exact old-state creation commands, pre-upgrade assertions, expected invariants, actual installation/migration commands, post-upgrade checks, restart checks, and interruption recovery. All state is synthetic or explicitly approved test data. Do not copy production databases or account stores into the VM.

Where prior fixtures are generated by an old binary, that artifact must be verified. Handwritten old schemas alone are supplementary fixtures, not the sole real migration evidence. Downgrade testing is separate and only required when supported. An unsupported migration must leave the prior state intact enough for its documented recovery.

## Manual and live evidence

Record observer, exact job/profile/candidate/test identity, scenario, observed action/result, and any missing checks. A default suite never starts a paid provider. Explicit live tests declare endpoints and spending/time boundaries. The queue reports their completion state without pretending to verify a model's internal decisions.

The native `use-haepari` skill is verified for managed submit-once, wait, same-ID observation, cancellation and recovery. Verify no nested capacity acquisition, no nested blocking wait and no automatic rerun of uncertain work. Explicit cancel and honest cleanup/manual/native-outcome reporting remain required. Run it against the installed stable CLI in a fresh test session. The test controller remains independent of the candidate skill and binary under evaluation.

## v0.1.0 release checklist

- [ ] Every active requirement has reviewed evidence and no unsupported PASS claim.
- [ ] Unit/integration tests and the mandatory failure matrix are complete.
- [ ] Actual execution is recorded for each advertised platform/backend.
- [ ] Real local and macOS UTM managed use cases pass. E01 through E20 remain deferred.
- [ ] Advisory lock cases V59–V64 pass, and `advisory-lock/v1` appears in the capability output.
- [ ] Contracts are frozen at gates F1 (TASK-012), F2 (TASK-016) and F3 (TASK-020), and the TASK-004 and TASK-034 decision records are reviewed.
- [ ] Installed CLI, daemon service and full same-release skill are verified.
- [ ] Cold reset returns the full environment, including skills and settings, to its declared baseline.
- [ ] Initial Haepari schema creation, too-new rejection, synthetic migration-framework fixtures and supported recovery are verified. Consumer projects still run meaningful product old-state migration. Actual Haepari prior-public-release migration is N/A until a predecessor exists.
- [ ] Required manual observations are complete and independently distinguishable from deterministic tests.
- [ ] Exact archive/executable/skill identities and capabilities are frozen in the producer handoff.
- [ ] Publication, installation on the real host, and Aquarium adoption remain separate actions.

## Additional active managed cases

| Case | Scenario | Required invariant and evidence class |
| --- | --- | --- |
| V37 | EPIC-001 local walking skeleton | Isolated controller, harmless command, committed ID, blocking wait, truthful result and same-key reconnect with one execution; local integration. |
| V38 | Parent holds global limit one and requests another resource | Reject nested acquisition before admission; existing-job status/wait allowed; local concurrency. |
| V39 | Several occupied slots and a parent requests another resource | Reject nested acquisition independently of spare resource capacity; local concurrency. |
| V40 | Sleep/wake/restart crosses queue, execution and manual deadlines | Queue expiry without lease; execution/manual cancellation with proof; no monotonic serialization or wait-cancel confusion; local integration. |
| V41 | Resource/global limit updates, baseline pin and removal | Existing reservations survive; queued revision binding, drain/cancel receipts and stale mutation fences hold; local integration plus UTM for baseline. |
| V42 | Machine-observed versus operator-attested recovery | Exact plan/receipt identity, provenance and stale-token rejection; unknown execution stays occupied; local integration plus UTM recovery. |
| V43 | A request or resource definition uses external execution mode | Rejected as `UNSUPPORTED` without admission; no lease command or external capability is exposed; local integration. |
| V44 | argv values contain spaces, quotes and shell metacharacters | Each value stays one argument; shell execution happens only through an explicit shell executable and arguments; fixture/unit. |
| V45 | A caller with an occupying parent context blocks on a queued job through `wait`, `run`, `events --follow` or `logs --follow` | `NESTED_WAIT_REJECTED` for each; non-blocking reads and observation of terminal jobs still work; a context chain crossing two installations is checked per installation; local concurrency. |
| V46 | The controller exits or restarts under the development LaunchAgent while a local runner works | The runner survives in its own session and process group; the controller reattaches it or records an interrupted execution with retained occupancy; no rerun; macOS supervision. |
| V47 | UTM is controlled from the service context, with and without Automation permission | Probe and adapter operations work from the LaunchAgent in the Aqua session; a missing permission or session is a prerequisite gap, never a host fallback; real UTM (TASK-034, TASK-016). |
| V48 | `HAEPARI_HOME` yields a socket path over the platform limit | Rejected before bind or any mutation; tests use short socket roots; local integration. |
| V49 | The host macOS guest limit is reached by Haepari or by a framework limit error | No reservation beyond `host_macos_guest_limit`; a framework limit error without a guest start finishes that job INCOMPLETE with a temporary gap and no quarantine, pauses every `utm-macos` resource on this host with reason `host_vm_limit`, and leaves later VM jobs queued instead of failing them; fake backend plus real UTM. |
| V50 | A baseline is registered to a second resource, or a job's profile differs from its resource baseline | Registration fails; the job is rejected with `PROFILE_MISMATCH` without admission; fixture/unit. |
| V51 | Cancel during preparing or ready; interruption during cleaning or settling; resource recovery while a job occupies the resource; one job settles on a multi-slot resource that another job has quarantined | Cancellation classifies effects and settles safely; cleanup or settlement resumes from its durable intent; `resource recover` returns `RECOVERY_BLOCKED` until job recovery settles; settlement never clears `paused` or `quarantined`; local integration plus fake UTM. |
| V52 | Sealed input is corrupted, or a pinned prerequisite disappears, while a job is queued | The job finishes INCOMPLETE without a lease; a prerequisite unavailable at admission is rejected without consuming the key; local integration. |
| V53 | Collection reaches its deadline, or the collector cannot be stopped | Evidence is marked partial and cleaning proceeds; an unstoppable collector leads to reconciliation and quarantine; no PASS; local integration plus fake UTM. |
| V54 | The same request key and payload are resent after a resource update or alias move | The original admission is returned with the identities resolved at admission; local integration. |
| V55 | CLI and controller share no protocol version | `PROTOCOL_UNSUPPORTED` before any mutation; local integration. |
| V56 | The store is opened on darwin | `fullfsync` and `checkpoint_fullfsync` are on; if the driver ignores them, the power-loss durability claim is recorded as blocked; macOS supervision (TASK-004). |
| V57 | Haepari installation, service lifecycle and default jobs run on a host that has legacy development state | No deletion of `~/.aquarium-dev`, foreign Git hooks, Sorage data or host skills; a before/after file-system audit shows only Haepari-owned paths changed; installed release observation. |
| V58 | A working VM's DHCP-discovered address disagrees with the explicit mapping, or a baseline's MAC, machine identity or SSH host key collides with another registered baseline | No connection or installation on a mismatch, which is reported as a prerequisite gap; colliding registration is refused; fixture plus real UTM (TASK-034, TASK-016). |
| V59 | Two cooperating callers acquire the capacity-one `mulgae-review` lock | FIFO grant and never two holders. Release, cancellation, force release and expiry each hand the slot to the next waiter in the same transaction. Local integration. |
| V60 | An acquire reply is lost and resent; the key is reused with a different payload; an ended request is replayed | The original request is recovered with its current state and no second queue entry. A changed payload is rejected with `CONFLICT`. A replay of an ended request exits 4 with its state, and a new acquisition needs a new key. Local integration. |
| V61 | A holder stops renewing; a waiter leaves before its grant; a holder runs `keepalive` until the maximum hold; an operator forces a release or cancellation | The stopped holder's grant and the unclaimed grant each end as `hold_expired` after one liveness window. `keepalive` renews until the maximum hold ends the grant. Forced actions are recorded with their reasons. Local integration. |
| V62 | Locks are held while managed local and VM jobs run; a managed job's workload acquires a lock; the lock resource is paused, recovered or removed | Locks consume no managed or global capacity. The nested acquisition is rejected with `NESTED_ACQUISITION_REJECTED`. Pausing stops new grants only, recovery returns `UNSUPPORTED`, and removal is refused while requests exist. Local integration. |
| V63 | The controller restarts, or the host sleeps, while a grant is held and callers wait | Grants, FIFO order and deadlines survive. On startup and wake, each live grant first gets a fresh liveness window (never past its maximum hold), and then deadlines are applied before any new grant. A holder that keeps renewing never loses its grant because of the stop or sleep. A holder that died ends one window later. Nothing is extended past its maximum hold by a clock jump. Local integration. |
| V64 | Stale IDs and exits: release or renew after a grant ended and the slot was granted again; `--wait` times out; a queued request's deadline passes | Release returns the old grant's terminal receipt and leaves the new grant untouched. Renew fails with `LOCK_EXPIRED`. The wait timeout exits 5 and the request stays queued. The queue expiry ends the request as `queue_expired` with exit 4. Fixture/unit. |
| V65 | Finished local and VM jobs report `timings` | Admission, reservation and phase timestamps are present, and UTM restore, boot-to-SSH, collection and reset step durations are present, so queue wait and VM cycle phases can be derived. Fixture plus real UTM (TASK-016). |

## External execution and continuation matrix

These cases preserve design-revision-2 proposals and are **deferred**; B1/B2 are unresolved. They are not v0.1.0 acceptance. The independent fixture client in TASK-032 models native mutation uncertainty and six-role recovery without requiring Mulgae. TASK-027 does not require them. Actual Mulgae/use-mulgae checks remain downstream and cannot be claimed from these fixtures.

| Case | Scenario | Required invariant |
| --- | --- | --- |
| E01 | Managed and external resources compete for global capacity | Reserved grants and uncertain active leases count; neither mode exceeds the shared limit. |
| E02 | An external caller requests with free capacity or while queued | One atomic operation grants or queues; no separate failed try-lock admission race. |
| E03 | Admission/grant reply is lost, then the same key is resent | One request and reserved slot; changed content/owner conflicts and old terminal keys stay terminal. |
| E04 | Two executors activate or begin the same execution intent | Only one binding and first-claim dispatch; all begin replays are reconcile_only. |
| E05 | Client crashes after begin commit before native-start acknowledgement | No automatic native restart or slot release; resolve no-start through checkpoint evidence or retain uncertainty. |
| E06 | Five of six roles succeed and one recoverable role fails | Preserve five native results; authorized exact recovery uses the same lease and no recursive acquisition. |
| E07 | Immediate rerun fails terminally and requires user input | Settle native work, release awaiting_user/incomplete and allow an unrelated queued review to run. |
| E08 | User later authorizes the failed role only | New request key/lease with same operation and exact released parent enters FIFO; no restored priority. |
| E09 | Two callers resume the same logical recovery | Same-key replay or single-successor/live-operation conflict prevents duplicate admission; dispatch remains single-owner. |
| E10 | A stale L1 release arrives while L2 is active | Original receipt or stale rejection; L2 occupancy and owner are unchanged. |
| E11 | Await times out, caller disconnects or native cancel is merely acknowledged | Native work remains occupied until true terminal/no-start evidence; observer state is not execution state. |
| E12 | Daemon restarts or owner liveness is lost | Unproven external work becomes reconciling and retains both slots; owner recovery cannot repeat native start. |
| E13 | Release races a new execution begin | One transaction wins; released requests cannot acquire new begin permits and unresolved intents block release. |
| E14 | Current source or native lineage differs from the root target | Consumer fixture rejects mixed-target composition; Haepari does not merge or reinterpret role results. |
| E15 | Recovered role succeeds but compose is absent or CI fails | Return success remains separate from native publication, coverage and CI. |
| E16 | An optional selected role is missing, or native sources are purged/corrupt | Native-like consumer rejects incomplete recovery; no reconstructed reports, fake role success or silent omission. |
| E17 | External resource permits two segments or its caller bypasses the queue | Capacity is segments, not provider invocations; direct bypass is explicitly outside advisory accounting. |
| E18 | Owner secret, run refs, tombstones or continuation metadata are exported/purged | Secret never leaves private owner state; active/unresolved/tombstone retention prevents unsafe reuse and no native files are deleted. |
| E19 | Queue-backed native capability is missing or a VM test would request its parent slot | Fail the selected path; use isolated fixture controller/resources, no silent direct path or self-deadlock. |
| E20 | Queued/unactivated work is cancelled while activation/begin races | Free capacity only after atomic no-intent proof and authority invalidation; live/uncertain effects stay occupied. |

External owner attestations are evidence about resource return, not independent proof of native product outcomes. Preserve native references and the exact fixture assertion for each row. V01 through V65 are the active managed matrix; V59–V64 cover advisory locks and V65 covers result timings. E01 through E20 remain deferred and need revision for B1/B2 before re-entry.

## Storage verification classes

Initial schema creation, unsupported newer schema rejection and synthetic migration-framework fault tests are active Haepari tests. Actual migration from a prior public Haepari release is not applicable until one exists. This does not weaken product-owned old-version-state creation and migration tests in consumer VM recipes.
