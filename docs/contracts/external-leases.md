# External execution lease contract

Status: **deferred design**, post-v0.1.0 subject to re-entry criteria. The R2 schema names and command sketches below preserve proposal identity; none is a v0.1.0 supported API, CLI help example, required test or consumer prerequisite. ADR-0014/0015 remain historical decisions whose application is deferred by ADR-0016. EPIC-008/TASK-029–032 remain deferred.

## Ownership and scheduling

The R2 proposal describes `execution_mode: managed` and `execution_mode: external`; only managed is supported in v0.1.0. A managed Job uses Haepari's runner and adapter. An external request reserves capacity for a cooperating client that executes through its own native CLI or MCP. The same controller, durable admission order, resource capacity and global occupancy limit govern both paths. There is no second scheduler for external requests.

In this deferred proposal, a resource declares one execution mode: `local` and `utm-macos` are managed; `external` is externally executed. The first examples are `macos-e2e` (managed, capacity one) and `mulgae-review` (external, positive configured capacity). Product names are configuration labels, never scheduler branches. Reject the wrong request family before admission. One request consumes one resource unit, including a one-role Mulgae recovery. Capacity counts execution segments, not role or provider-call counts.

`auto` and `manual` describe a managed Job's test mode. They are not synonyms for managed and external execution. VM hands-on retains its managed lease while the person uses the environment. A Mulgae review awaiting a user's decision, with no live execution, releases its external lease.

`accepted` means a request was durably recorded. `granted` means one resource slot and one global slot were reserved. Neither means human consent, review PASS, or permission to repeat a native mutation. The caller still follows its existing execution, transmission, provider and cost authority.

## Request identity

`haepari.lease-request/v1` has these fields; unknown fields are rejected:

| Field | Meaning |
| --- | --- |
| `schema` | `haepari.lease-request/v1` |
| `execution_mode` | Exactly `external` |
| `resource_id` | One registered external resource |
| `operation_ref` | Stable opaque, caller-namespaced logical-work reference |
| `intent_fingerprint` | SHA-256 of the caller's frozen execution intent; Haepari does not parse the target or Brief |
| `continuation_of` | Null for a root, or the exact previous released request ID |
| `caller_ref` | Bounded non-secret attribution label; not authentication |
| `deadlines` | Positive finite queue, segment and liveness budgets in seconds |
| `effects` | Declared effect class and non-secret authority reference; not credentials |

The request key is supplied separately to `lease request`, and its namespace is shared with managed admissions. Canonical identity includes this validated request, the resolved resource configuration digest and the authenticated owner binding. An external request does not require a source bundle, VM baseline, test profile or command argv. Haepari stores no review code, prompt, findings or role-result body. The caller owns immutable target capture and native provenance checks.

Before the first request, the client saves the request key, operation reference, intent fingerprint and a random ownership secret in private local state. `--owner-file` points to a regular 0600 file in the caller's approved private state area. The file is never an artifact or template input. Do not put the secret on argv, in environment variables, output, logs, a source repository or an export. The controller stores a one-way verifier and never returns the secret. Lost ownership material requires explicit recovery; read access to an ID is not ownership. This is a cooperating same-user protocol, not an OS security boundary.

Same key and identical request/owner returns the existing request and state, including after purge. Changed content, owner or execution family conflicts. A replay never creates another grant. The implementation retains minimal durable tombstones and lineage needed for deduplication; it does not expire keys into new admissions.

For an admitted replay, compare against its already-resolved resource definition instead of silently re-resolving current configuration. Changed resource definitions prevent a new start when incompatible; they do not change the historical receipt.

## Proposed CLI

All JSON responses use the envelope in [CLI and jobs](cli-and-jobs.md). Every mutating operation has a durable operation key or a documented intrinsic key, expected revision where noted, and the current ownership binding. `--input` files carry only bounded structured fields and references.

| Command | Result |
| --- | --- |
| `haepari lease request --spec <json> --request-key <key> --owner-file <path> --json` | One atomic admit-and-schedule operation; returns `queued` or `granted` and the stable request ID. Never fail a try-lock and then separately enqueue. |
| `haepari leases list [--resource <id>] [--operation-ref <ref>] [--cursor <cursor>] --json` | Bounded pages without ownership secrets. |
| `haepari lease inspect <request-id> --json` | Read request, occupancy, revision and bounded execution references. |
| `haepari lease wait <request-id> --until granted\|released [--timeout <duration>] --json` | Block on the same request. Terminal rejection/cancellation is also returned; it is not a grant. |
| `haepari lease events <request-id> [--after <cursor>] [--follow] --json` | Same durable cursor semantics as managed Jobs. |
| `haepari lease activate <request-id> --executor-id <id> --operation-key <key> --expected-revision <n> --owner-file <path> --json` | Bind a reserved grant to one client executor. Returns lease ID and generation; no native execution has started. |
| `haepari lease begin <request-id> --input <json> --owner-file <path> --json` | Record one opaque native execution intent before calling the external tool. Input binds lease/generation, executor, expected revision, execution key and intent digest. |
| `haepari lease bind <request-id> --input <json> --owner-file <path> --json` | Attach returned native invocation/run references to that execution key. This never dispatches work or proves completion. |
| `haepari lease settle <request-id> --input <json> --owner-file <path> --json` | Record the owner's terminal/no-start attestation and bounded native evidence reference for that execution. Does not decide review coverage. |
| `haepari lease reconcile <request-id> --input <json> --owner-file <path> --json` | Record supported native running/terminal/no-start/unknown observation under current fences; resume observation or settlement without a new native dispatch. |
| `haepari lease touch <request-id> --input <json> --owner-file <path> --json` | Liveness only. Cannot extend authority, change the segment deadline or free capacity. |
| `haepari lease cancel <request-id> --expected-revision <n> --operation-key <key> --owner-file <path> --json` | Cancel queued work or request active cancellation; an active request still requires owner drain/settlement. |
| `haepari lease release <request-id> --input <json> --owner-file <path> --json` | Close admission for further steps and atomically return capacity after the declared boundary is settled. |

`begin` input uses a stable `execution_key`, fixed intent digest and current fencing tuple. Only the transaction that first creates that intent returns `dispatch: execute_once`. Identical replay returns the existing intent with `dispatch: reconcile_only`, never a new execute permit. A caller that lost the first reply must not assume it can now dispatch. This deliberately prefers an explicit unresolved-start state over a duplicate native mutation. The caller may attest `not_started` only with evidence from its single-owner execution journal that it never received/used the permit; otherwise keep it unknown.

Mutating `bind`, `settle`, `reconcile`, `touch` and `release` inputs include `operation_key`, expected revision, lease ID/generation and executor ID. A successful replay is matched against its original payload before checking a now-advanced revision. Conflicting replay fails. Token ownership and original identity are always checked, even for a terminal replay. `cancel` and `activate` follow the same rule. Capability output must identify these exact semantics, not just a version string.

Operation exit 0 means the lease command succeeded, including queued admission or released capacity. It never means the external review passed. Invalid/conflicting input uses 2; unavailable/uncertain infrastructure uses 6, matching the active managed contract; a wait ending in cancellation uses 4; observer-only wait timeout uses 5. No lease operation uses the managed Job final-verdict exit mapping. Human waiting is represented by a released receipt with `release_reason: awaiting_user`, not a passing Job.

## State and capacity model

| State | Occupies capacity? | External execution allowed? |
| --- | --- | --- |
| `queued` | No | No |
| `granted` | Resource and global slot reserved | No, activation and begin are still required |
| `active` | Yes | Only the bound executor with a fresh per-execution permit |
| `cancel_requested` | Yes | No new execution; owner drains existing work |
| `reconciling` | Yes | No new execution until current native state is reconciled |
| `released` | No | Never; continuation is a new request |
| `cancelled` | No | Never; only after no effects or safe terminal settlement is proven |
| `rejected` | No | Never; no execution was admitted |

The scheduler inserts the request and checks FIFO/capacity in one admission transaction. It may grant immediately only if that request is the eligible resource head and both limits permit it. A later request cannot bypass an earlier eligible head merely because it used the external API. Paused/unavailable resources do not create a global head-of-line block; occupied or uncertain leases still count toward the global limit.

A grant reserves capacity before a reply is sent. Activation binds one `executor_id` for its lifetime. A different executor cannot adopt the same grant or native intent just by knowing the request key. Identical activation replay recovers the binding, not permission to repeat work. The client must have its own durable single-owner dispatch checkpoint; copying owner material between independent agents is unsupported.

`begin` changes state before the external call can occur. At most one native execution intent may be open or uncertain within a lease. That intent may be a native multi-role review that starts several provider calls internally. Once it settles, an authorized immediate rerun can begin under the same lease with a new execution key. Haepari does not inspect role counts or impose a native retry count.

A valid release closes further `begin` admission atomically. It requires all possible native executions settled, no unresolved start or cancellation, and an owner attestation that no protected activity remains. Input contains a bounded evidence reference, `release_reason` (`completed`, `failed`, `awaiting_user`, `deferred`, or `cancelled`), and `external_outcome` (`succeeded`, `failed`, `incomplete`, or `cancelled`). These are caller-reported metadata, not Haepari's review verdict. Default external resources trust this scoped owner attestation; Haepari does not invent a native status verifier. A falsely reporting same-user client can violate an advisory limit.

If no execution intent ever existed, the controller can settle a cancellation or abandoned grant only by transactionally invalidating activation/begin authority and proving no intent won the race. An active or uncertain intent cannot be released from elapsed time, heartbeat loss, a CLI exit, an empty process list, cancel acknowledgement, or a network error.

The transition to `released` and freeing both slots is one transaction. Exact release replay returns the original receipt without touching any successor. No released lease is revived. An execution deadline stops new begins and requests cancellation/reconciliation; it does not kill an unknown process or free capacity on its own. A native operation already running must drain or be explicitly recovered.

## Continuation without reserving an idle slot

A logical review and its resource occupation have different lifetimes. Keep `operation_ref` stable across execution segments. Within a segment, authorized immediate retries use the same lease and new execution keys. Before waiting for a person's decision or a long external condition, confirm native termination, preserve the native recovery references, release with `awaiting_user` or `deferred`, and stop submitting work. There is no queued placeholder while permission is unresolved.

After a new decision authorizes execution, create a new request key and intent fingerprint with the same `operation_ref` and `continuation_of` naming the previous released request. Receive an ordinary FIFO place; do not inherit queue position, capacity or approval. Store the new key durably before sending it. A replay of the old key returns the old released receipt and never creates a new lease.

Continuations must belong to the same resource and logical reference. The scheduler permits at most one non-terminal request for a `(resource_id, operation_ref)` and one admitted direct successor of a released request. Enforce both constraints transactionally. This prevents two resume callers from creating parallel branches with different keys; the loser receives the existing reference without ownership or dispatch authority. An intended later recovery continues from the latest released segment. Correlation carries no role state, workflow step, native result, provider routing or final operation verdict.

The client maps one user-authorized execution segment to one stable request key and owns single dispatch. Haepari guards admission and execution intents, but cannot guarantee exactly-once native effects. A client bypassing the protocol, changing its logical identity or sharing execution permits is outside that guarantee. Preserve native reconciliation on uncertain mutation results even when the Haepari admission itself is certain.

## Failure and owner loss

External execution is not supervised by Haepari's runner. The client must use its native start/await/cancel/result contract and preserve native IDs as they become available. A process-local invocation ID is tagged with its native server/session scope; it is not a durable restart handle. Bind a durable native run ID separately when the tool returns one. No ID may be invented by Haepari.

After client death, liveness loss or daemon restart, unproven active external work moves to `reconciling` and retains both occupied slots. Block new begins. Reconnecting the same authenticated executor may submit native terminal evidence and settle, or explicitly reconcile a proven continuing execution and resume observation. It cannot automatically restart the original command. A current owner uses `lease reconcile` with `observation: running|terminal|not_started|unknown`, its evidence reference and expected revision. Proven running work resumes observation under the existing open intent; it does not receive a new execution permit. Proven terminal/no-start work can settle. Unknown work stays occupied. A pending cancellation remains sticky even after a running observation and prohibits further begins.

Resource recovery plans identify the exact request, generation, executor and unresolved intent. A current operator action can establish supported native termination or a verified no-start boundary; it cannot manufacture completion by renaming a state.

For capacity greater than one, the uncertain lease consumes its slot; other free slots can continue only when the resource remains healthy. The operator may pause/quarantine the whole resource when uncertainty affects shared safety. A quarantined resource blocks all new admissions to execution. Managed recovery can perform a VM reset; external recovery never assumes authority to reset a VM, kill arbitrary providers or delete native data.

No forced unsafe unlock, TTL reassignment, process scanning as a substitute for missing native identity, new daemon session to recover a process-local handle, or silent direct-execution fallback is part of v0.1.0. Unknown work may require operator intervention and can block progress. Report that limitation explicitly.

## Data, limits and conformance

External requests retain their small identity, owner verifier, intent/settlement log and deduplication tombstones outside the VM. They do not export ownership secrets or purge Mulgae's source/result storage. Native role results must remain available under the owning product's retention rules for later exact recovery and composition. A Haepari reference is not a backup of those results.

Use bounded opaque references and explicit metadata/event limits with backpressure. Reaching a storage or reference limit rejects another begin before effects; it does not free a current lease or delete unresolved evidence. Do not set an Aquarium-only semantic retry cap to solve metadata growth.

The R2 proposal would have qualified both modes through a fixture caller; this is not v0.1.0 release qualification. See [external verification](../verification.md#external-execution-and-continuation-matrix) and the [Mulgae integration contract](../integrations/mulgae-review.md). Real Mulgae adaptation is downstream work, not a prerequisite for Haepari's standalone implementation.

## Capability identifiers

The planned capability identifiers are `managed-execution/v1`, `external-execution/v1`, `external-first-claim/v1`, `external-blocking-wait/v1`, `external-safe-release/v1` and `external-continuation/v1`. A later re-entry must first resolve B1/B2, then freeze any supported machine representation. A future external consumer would require compatible identifiers and schemas. Compiled protocol support, fixture qualification and observed real consumer integration remain separate fields. A version string or working managed path cannot substitute for a missing external capability.

Caller integrations may use a small local helper to maintain the owner checkpoint and liveness while awaiting the native tool. It is not another scheduler and must not fabricate native progress. The model itself uses blocking awaits rather than a heartbeat/status polling loop. A liveness receipt only records owner contact; it never proves execution ended, overrides cancellation or extends a segment deadline.

## Re-entry blockers: B1 and B2

**B1, stranded capacity:** All dispatch intents can be settled while the owner vanishes or loses its private owner file, leaving capacity stranded. Re-entry requires owner-file-independent recovery, an atomic closure of the execution-claim gate, proof that no unresolved **or executable** intent remains, fencing of late owner mutations before capacity release, explicit activation/abandonment semantics, and operator recovery with machine-observed versus attested evidence provenance. Zero open intents alone is insufficient if another intent can still be claimed. Unknown execution is never reclaimed by TTL.

**B2, continuation dead end and duplicate roots:** A queued successor can be cancelled or expire after its released parent has consumed its single successor allowance. Re-entry requires a persistent reservation of one pending successor and safe reservation release before grant/execution rights. Granted or uncertain successors cannot be reopened casually. Define root uniqueness within an exact installation/resource/operation namespace, a new request for a later continuation versus replay of the old request, durable uniqueness constraints, and transition/race tests. Consuming successor only at grant does not resolve duplicate queued successors or execution uncertainty.

EPIC-008 must revise these prospective contracts and E01–E20 before implementation. No future version or date is promised.
