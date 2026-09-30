# CLI and job contract

Contract family: `haepari.* / v1`. Proposed public surface for v0.1.0. TASK-002 defines provisional schemas and examples; local and failure evidence refines them. Freeze gate F1 (TASK-012) freezes the managed core contract and machine-readable help, gate F2 (TASK-016) the VM fields, and gate F3 (TASK-020) the consumer integration contract. See the [roadmap freeze gates](../roadmap.md#contract-freeze-gates).

This contract describes managed Jobs. TASK-033 implements `resource register`, `submit`, `run`, `inspect`, and `wait` for a single capacity-one local auto-Job resource. Queued input loss and queue expiry settle INCOMPLETE without a lease; incomplete output collection retains the owned workspace and quarantines the resource. TASK-007 extends local registration to multiple resources and configured capacities, enforces per-resource FIFO and the controller's current global limit, and implements `resource inspect` plus fenced `resource pause`/`resume` with JSON output. These commands interpret `--expected-revision` as the availability revision and optionally accept `--request-key`. Omitting the key generates a fresh random mutation identity for that invocation; supply and persist an explicit key for exact replay after an uncertain reply. `--reason` applies to pause and must contain 1–256 bytes (default `operator_pause`); resume rejects that flag. Read the current fences with `resource inspect <id> --json`. By default each invocation observes the current configuration/lease epoch. For exact CLI replay across intervening reservations or configuration changes, persist the key and explicitly supply both `--expected-config-revision` and `--expected-lease-generation` from the first observation, with the original availability revision. Explicit configuration revisions must be at least 1 and lease generations at least 0; supplying only one pin or an invalid explicit value is rejected before connection or mutation. Repeat those identical flags after an uncertain reply. A changed epoch in a new request under the same key remains `CONFLICT`; the replay path does not weaken any fence or apply the mutation twice. A local resume records controller-observed filesystem readiness and cannot clear quarantine. An exact committed resume replay returns the stored mutation identity and current resource state without taking a new readiness observation. The observation records facts at its stated observation time; it does not lock filesystem state through the SQLite commit. An omitted `idle_since` means the historical idle time is unknown. The configuration digest returned by resource inspect is the SHA-256 of the canonical stored registration JSON. The resume readiness reference contains the installation, resource, relative jobs path, permission mode, modification and observation times, outcome and fresh reference; its SHA-256 digest binds those exact recorded facts. Pause clears client-supplied readiness fields. Quarantine changes are also published to sibling queued Jobs as availability events. The default foreground controller still uses global limit 1; the ledger accepts the configured limit for later configuration wiring. VM host guards and power-intent reconciliation are internal adapter APIs consumed by TASK-013/TASK-016; the public local controller still rejects VM registration and never invokes `utmctl`. The remaining command families below are planned and are not implied by those slices. External requests are a [deferred design](external-leases.md), not a v0.1.0 CLI family. Do not use `manual` Jobs to emulate external execution.

## CLI families

| Command | Contract |
| --- | --- |
| `haepari version --json` | Report product version, source identity, and executable identity without starting a daemon. |
| `haepari capabilities --json` | Report protocol versions and compiled/backend-qualified capabilities separately, including `managed-execution/v1` and `advisory-lock/v1`. |
| `haepari doctor --json` | Read-only configuration/service/prerequisite inspection; never boot or repair a VM. |
| `haepari daemon run` | Run the exclusive local controller in the foreground. |
| `haepari service install`, `status`, `stop`, `uninstall` | Explicit per-user service lifecycle. No implicit installation during submit. |
| `haepari resource register --file <json>` | Validate and register one operator-owned resource. For a `utm-macos` resource this checks the VM UUID, MAC address and SSH host key pin; it never creates, clones or boots a VM. |
| `haepari resources list --json` | List registered resources, availability, occupancy and configuration revisions. |
| `haepari resource inspect <id> --json` | Return revision, identity, configuration digest, availability, and occupancy. |
| `haepari resource update --file <json> --expected-revision <n>` | Validate a new revision; capacity/identity changes obey the configuration contract. |
| `haepari resource remove <id> --expected-revision <n>` | Remove only an idle, unreferenced resource; no implicit VM-package deletion. |
| `haepari resource pause <id> --expected-revision <n>` | Stop future dispatch without killing active work. |
| `haepari resource resume <id> --expected-revision <n>` | Resume a paused resource after current readiness verification; cannot clear quarantine or recover unknown effects. |
| `haepari resource recover <id> --plan` | Read-only exact resource/job/lease/run identity, observed evidence and proposed effects; return a fenced token. |
| `haepari resource recover <id> --plan-token <token> --evidence <json>` | Apply a still-current recovery with machine-observed or operator-attested evidence; return a mutation receipt. |
| `haepari submit --spec <json> --request-key <key> --json` | Seal referenced input bytes and durably admit or recover one identical request. |
| `haepari run --spec <json> --request-key <key>` | Submit using the same contract, print the ID, then wait; does not add retry authority. |
| `haepari jobs list [--resource <id>] [--state <state>] [--cursor <cursor>] --json` | Return bounded stable-order job pages including queue position and retained result summaries. |
| `haepari inspect <job-id> --json` | Observe current state/result without changing it. |
| `haepari wait <job-id> [--timeout <duration>] --json` | Block until finished or the caller's waiting deadline. |
| `haepari events <job-id> [--after <cursor>] [--follow] --json` | Read/reconnect to bounded event pages or a stream. |
| `haepari logs <job-id> [--after <offset>] [--follow]` | Read exact byte-offset log pages; report truncation explicitly. |
| `haepari cancel <job-id> --expected-revision <n>` | Request cancellation. An acknowledged request is not a cleanup result. |
| `haepari manual complete <job-id> --input <json> --expected-revision <n>` | Record a human result and begin collection/cleanup. |
| `haepari export <job-id> --to <directory>` | Export reviewed result files to an explicit destination, without overwriting unrelated files. |
| `haepari job recover <job-id> --plan` | Read-only exact run, resource generation, receipts, unknowns and proposed reconciliation path. |
| `haepari job recover <job-id> --plan-token <token> --evidence <json>` | Fenced recovery mutation; cannot mark unknown execution terminated without supported proof. |
| `haepari purge <job-id> --expected-revision <n>` | Remove a finished job's unprotected bulk data; refuse deletion of evidence reachable from unresolved retention/recovery references and keep a request-key tombstone. |
| `haepari lock acquire`, `wait`, `renew`, `keepalive`, `release`, `cancel`; `haepari locks list` | Advisory named locks for cooperating tools; see the [lock contract](locks.md). Lock commands use the lock contract's exit table. |

A mutating command is an explicit user/agent action. The paired skill must carry the actual authorization context and scope. Merely having the CLI, an existing job, or an inbox item is not authorization to install, execute providers, reset foreign state, or publish a release.

## JSON envelopes and exit codes

Every JSON response is one closed object with `schema`, `operation`, `ok`, and either `data` or `error`. Errors include a stable `code`, a bounded explanation, a retry/reconciliation classification, and an optional job/resource reference. Never embed secrets or unrestricted child stderr.

The provisional internal envelope identifier is `haepari.envelope/v1`. `ok=true` requires `data`; `ok=false` requires `error` with exactly one of `none`, `retry_same_key` or `reconcile` as its retry classification. Both branches reject unknown top-level fields, duplicate keys and unresolved placeholders. Nested operation-specific `data` is validated by that operation's schema before use.

| CLI exit | Meaning |
| --- | --- |
| 0 | Command succeeded; for `run`/terminal `wait`, final verdict PASS. |
| 1 | Terminal `run`/`wait` has a verified FAIL. |
| 2 | Invalid usage, rejected input, conflict, unsupported contract, or a known admission whose retained result is unavailable. No implied workload failure. |
| 3 | Terminal INCOMPLETE with a known result envelope. |
| 4 | Terminal job is CANCELLED after safe settlement. |
| 5 | Caller wait deadline elapsed; the job is not cancelled. |
| 6 | Transport outcome uncertain or controller unavailable; reconcile the same request key before another submit. |

Each machine error code maps to one CLI exit:

| Error code | Meaning | CLI exit |
| --- | --- | --- |
| `INVALID_REQUEST` | Malformed, unsafe or placeholder input | 2 |
| `CONFLICT` | Same request key with a different submitted digest, or a conflicting mutation replay | 2 |
| `UNSUPPORTED` | Unsupported mode, schema version, platform or backend, including external execution | 2 |
| `PREREQUISITE_UNAVAILABLE` | An immutable prerequisite is unavailable at admission; no admission, key not consumed | 2 |
| `NESTED_ACQUISITION_REJECTED` | Submission or advisory lock acquisition from a caller whose parent context occupies capacity | 2 |
| `NESTED_WAIT_REJECTED` | Blocking observation (`wait`, `run`, `events --follow`, `logs --follow`) of a non-terminal job from such a caller | 2 |
| `NOT_FOUND` | The job, resource or recovery plan does not exist | 2 |
| `RESULT_UNAVAILABLE` | The admission is known but its retained result is unavailable; no new job is launched | 2 |
| `RESOURCE_BUSY` | The resource change requires an idle or unreferenced resource | 2 |
| `LOCK_EXPIRED` | A lock grant expired or was released before this renewal | 2 |
| `PROTOCOL_UNSUPPORTED` | CLI and controller share no protocol version; no mutation | 2 |
| `STALE_REVISION` | The expected revision is no longer current | 2 |
| `RECOVERY_BLOCKED` | Recovery refused (occupied resource or stale/unsafe plan), or pause/resume refused (wrong availability or failed local readiness) | 2 |
| `WAIT_TIMEOUT` | The caller's wait deadline elapsed; the job continues | 5 |
| `TRANSPORT_UNCERTAIN` | Controller unavailable or reply lost; reconcile the same request key | 6 |
| `STORAGE_UNAVAILABLE` | The controller could not commit durable state; no mutation is acknowledged | 6 |

`inspect`, `events`, and `logs` return 0 when their own read succeeds, regardless of workload verdict. Preserve a native command's exact exit code/signal inside the run result; do not substitute it for the CLI operation code. `run --json` may stream versioned admission/event/final objects as NDJSON, while ordinary command responses remain one JSON object. The schema must distinguish those modes.

In the TASK-033 local slice, `run` waits for at most 24 hours after admission, and each `wait` call accepts at most a 24-hour timeout. A caller wait deadline returns exit 5 while the Job continues; use its printed Job ID to wait again. This bound is separate from the admitted Job's run deadline.

## Job input

`haepari.job/v1` contains the following closed fields (the example is provisional until freeze gate F1, and its VM-specific fields until F2):

- `schema`: exactly `haepari.job/v1`.
- `execution_mode`: exactly `managed`; the selected resource must admit managed execution.
- `resource_id`: exactly one registered resource; no list in v0.1.0.
- `mode`: `auto` or `manual`.
- `suite`: a consumer label such as `install`, `migration`, or `regression`.
- `input`: a contained bundle root, an explicit `selected_files` list and an inventory digest. TASK-003 provides the internal selected-file sealer; TASK-033 local admission copies, verifies and binds the object before accepting the request. The submitted digest is the lowercase 64-character hex portion of the manifest's `tree_sha256` value.
- `environment`: the consumer profile reference and its expected digest, recorded as evidence for VM jobs; an explicit local environment reference for local jobs.
- `workload`: argv, bundle-relative working directory, non-secret environment, and required output declarations. Local execution resolves a bare `argv[0]` against the workload's `PATH` (default `/usr/bin:/bin`, overridable in the workload environment); a relative path containing `/` resolves against the declared working directory.
- `setup`: optional bounded argv for manual-mode preparation; ordinary install recipes may be the workload for auto mode.
- `manual`: required scenario document and completion schema only when mode is manual.
- `deadlines`: positive finite queue, preparation, run/manual, collection, and cleanup budgets.
- `effects`: offline/live class and declared authorization/effect reference, containing no credential values.
- `provenance`: candidate identity, test identity, requested compatibility/profile owner, and development/release evidence class.

The provisional decoder rejects duplicate JSON keys at any depth, unknown fields, wrong types, unresolved placeholders, unsafe or case-colliding output paths, and empty required-output declarations. It requires an absolute clean bundle root, lower-case 64-digit SHA-256 digests and positive phase deadlines no longer than 366 days. Auto Jobs require a run deadline and omit `manual`; manual Jobs require a manual deadline, a contained scenario document and unique required scenario IDs, and omit the run deadline. These validation rules precede admission. TASK-033 seals local auto-Job input before admission and checks local resource readiness; later tasks own the remaining resource and execution paths.

No field lets a project replace the host UTM control program, VM identity, SSH identity, resource capacity, or cleanup policy. Those belong to operator-controlled resource configuration.

The request key is required for mutating submission. It is 1–128 ASCII characters, begins with an alphanumeric character, and contains only alphanumeric characters, `.`, `_` and `-`. Invalid keys are rejected before sealing or admission. A managed runner propagates a controller-verifiable parent execution context. A child holding capacity in the same installation cannot submit another Job or block on a non-terminal job's progress (`wait`, `run`, `events --follow`, `logs --follow`); non-blocking reads and observation of terminal jobs remain allowed. See [nested acquisition](managed-transitions.md#nested-acquisition). A project wrapper persists that key and the frozen input identity in ignored/private run metadata before its first submission, so an uncertain transport response can be reconciled without generating another key. Its scope is one Haepari installation, not the working directory; the R2 shared external namespace is deferred. The deduplication digest is the canonical digest of the submitted request: the validated typed structure, including alias names exactly as submitted, plus the sealed input digest. Resolved environment and resource-configuration identities are recorded with the admission but are not part of the deduplication digest, so an identical replay after a resource update or an alias move returns the original admission. No raw secret values participate. Same key/digest recovers an existing admission; a different digest conflicts. A rejection does not consume the key. A new run after a known terminal failure requires a new key and a new explicit submission linked to the prior job.

Admission tombstones survive ordinary purging. A tombstone returns the known admission and an unavailable-result status without executing. There is no short timer after which an old request key silently becomes reusable.

The TASK-006 internal receipt uses `admission` for immutable sequence, request/submitted/input/object identities, original resource configuration revision, environment profile identity and UTC admission time. These are separate from the current resource revision and occupancy fields. A retained tombstone has `result_status: unavailable` with the known Job/admission identity, and omits verdict, capacity and lease fields. `submit` returns that known admission successfully; `run` emits it and exits 2 without waiting, and `wait` reports `RESULT_UNAVAILABLE` with exit 2. This is unavailable retained evidence, not a Job verdict or a rejected/new submission. `inspect` exposes the tombstone without a fabricated result. Bulk retention remains TASK-012; missing accepted payloads without an explicit retention receipt are storage uncertainty.

## Event and wait semantics

Use HTTP over the private Unix socket for request/response operations and a documented streamed response for events. Every connection starts with a protocol handshake: the client sends its supported protocol version range, and the controller selects one and returns its capability set plus the stable installation ID used to check inherited advisory grants. If they share no version, the controller returns `PROTOCOL_UNSUPPORTED` before any mutation. There is no TCP listener by default or in the v0.1.0 public surface. An event cursor is monotonic and resumable. The server verifies cursor scope; if older events were explicitly purged, return a resynchronization response with the current snapshot, never a silent empty stream interpreted as success.

The provisional internal event page carries a Job-scoped `current_cursor` and a `next_cursor` for the returned page. A resynchronization response carries the Job snapshot and a retained Job-owned checkpoint cursor. Prefix retention preserves cursor ownership tombstones and the purge floor; a client resumes from the checkpoint after applying the snapshot. Cursor scope and the page are read from one SQLite snapshot. A cursor whose ownership was never recorded is rejected rather than attributed to a Job by guesswork.

The implementation may perform bounded internal liveness checks. TASK-033 uses a blocking HTTP wait over the private socket and in-process notification after durable terminal settlement; streamed events and cursor reconnect remain planned. User-facing skills and wrappers must use blocking wait/events rather than a status polling loop. The daemon remains the owner when a CLI process exits. A heartbeat indicates only connection liveness, not test progress or renewed execution authority.

## Manual completion

A manual result binds the job, exact installation/profile identity, expected current revision, observer label, each required scenario's `pass`, `fail`, or `unobserved` status, and bounded evidence references. It contains no authentication values. `unobserved` prevents PASS. Conflicting repeated completion fails; an exact retry recovers the original completion receipt.

The provisional `haepari.manual-result/v1` shape has `job_id`, `installation_id`, `profile_id`, `profile_digest`, `expected_revision`, `observer` and `scenarios[]`. Each scenario has an `id`, `status` and `evidence_references[]`; passing or failing observations require at least one reference. The exact required scenario set comes from the admitted Job. The decoder is implemented internally during TASK-002; completion over the CLI and its durable receipt belong to later execution tasks.

An execution deadline can request cancellation/recovery of an abandoned manual session, but expiry does not declare it passed, prove the human disconnected, or release the VM. For known long inspections the operator supplies an appropriate deadline before admission. Extensions, if implemented, require a current revision and explicit authorization; automatic indefinite extensions are out of scope.

## Consumer Make contract

`make test-vm SUITE=install|migration|regression|all MODE=auto|manual` is the proposed convention. A project host wrapper assembles immutable inputs, checks the installed Haepari capability contract, submits, prints the job ID, waits, and returns the gate result. It does not run UTM commands itself.

The VM executes a distinct entrypoint, conventionally `make test-vm-guest` or `tests/vm/run.sh`. It requires the job/lease context that the guest runner provides before installation. The host wrapper must not be copied into an execution path that recursively submits another job. Ordinary `make test` remains unchanged unless a separately adopted repository policy deliberately adds an external gate.

`SUITE=all` is a serial host wrapper over distinct suite jobs, all bound to the same candidate/test bundle. It retains every job ID and reports skipped suites after a failure. Do not claim aggregate PASS unless every required suite and manual check completed.

## Deferred external request routing

No `haepari lease` command, external mode, or lease purge appears in v0.1.0 help/capabilities. Advisory locks are not leases: they grant no execution permit and count toward no managed capacity. The [R2 proposal](external-leases.md) is retained for post-v0.1.0 re-entry.


## Machine outcomes and glossary

The proposed `haepari.result/v1` fields are `job_id`, `resource_id`, `resource_revision`, `lease_generation`, `job_state`, `resource_availability`, `workload_outcome`, `evidence_completeness`, `cleanup_outcome`, `final_verdict`, `timings`, artifact inventory and observation provenance. `timings` holds UTC timestamps for admission, reservation and the start and end of each phase. It also holds adapter step durations reported by the adapter: for UTM these are VM start (when needed), SSH ready, transfer, collection and cleanup. Queue wait and VM cycle phases are derived from it. See the [local result example](../examples/result-local.template.json).

The provisional result type has separate optional `native_exit_code` and `native_signal` fields so an observed command exit is retained without becoming the CLI exit. A complete PASS or FAIL requires the artifacts declared by the admitted Job; unknown outcome values and contradictory terminal verdicts are rejected. A queued or reserved no-effect result records `cleanup_outcome=not_needed` without inventing a workload output. The internal ledger binds a no-effect machine observation's reference, mechanism and digest to the exact Job, resource revision, slot and lease before classification, and refuses settlement while a VM power transition is pending or uncertain. TASK-033 and TASK-016 supply and qualify the actual local and VM observations; this provisional record alone is not runtime qualification.

A **request key** deduplicates admission; a **job ID** identifies the accepted managed Job; a **lease generation** fences one resource-slot ownership; a **run ID** binds the single supervised workload; a **resource revision** fences configuration/recovery; a **profile** is a consumer-owned frozen environment selection that Haepari records but does not apply to the VM. A **wait timeout** ends only observation. **INCOMPLETE** is a known terminal verdict with missing proof, while **FAIL** is a known workload/assertion failure. A rejected input has no admission. A transport uncertainty requires inspection or resubmission with the same key, never a new speculative job.

Machine error codes and their exits are listed under [JSON envelopes and exit codes](#json-envelopes-and-exit-codes). Terminal result envelopes carry `PASS`, `FAIL`, `CANCELLED` or `INCOMPLETE` separately from `ok` for the command. The [authoritative decision table](managed-transitions.md#authoritative-final-verdict-decision-table) controls all combinations: a known workload failure with complete evidence and verified cleanup is FAIL; missing required evidence, unverified cleanup or required unobserved manual work makes the final verdict INCOMPLETE while preserving the failure fact. A reserved proven no-effect Job may finish with `cleanup_outcome=not_needed` and one capacity return; queued cancellation uses the same cleanup value without a reservation or capacity return. A VM-local `vm_unavailable` pause commits before its reserved Job settles and does not pause other resources. A queued Job blocked by host VM policy or capacity exposes its current actionable blocking reason through inspection and events without changing its queue deadline. Terminal results are immutable; later recovery evidence has separate receipts.

## Recovery mutation contract

`job recover --plan` and `resource recover --plan` return exact installation, job, resource, config revision, slot/generation, run/boot/nonce, last durable intent and receipts, current observations, unknowns, proposed effects, evidence loss and a short-lived token bound to that snapshot. The operator has local control authority over the owned resource and explicitly invokes apply; the token alone is no substitute for authorization. Apply checks the entire tuple and expected revisions transactionally before effects and records mutation intent and receipt. Evidence has source, observer, observation time, mechanism and digest/reference. Machine-observed termination can permit normal collection and cleanup. Operator-attested termination or non-execution is labeled attested, can only resolve the stated resource/job under the declared policy, never upgrades workload outcome to PASS, and remains visible in result provenance. An unverified timeout, lost CLI, stale PID, or controller restart is insufficient. Failed cleanup quarantines; proven cleanup returns readiness. Replayed apply returns the original receipt; stale or changed plans reject without effects.
