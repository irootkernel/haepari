# Haepari architecture

Target: v0.1.0. This document specifies intended behavior, not an implemented system.

## System boundary

```text
project Makefile -> Haepari CLI -> private Unix socket -> controller
                                               | SQLite + sealed objects
                                               | managed resource scheduler
                                               +-> local runner
                                               +-> UTM host adapter -> SSH -> guest runner -> project recipe
```

The controller owns input validation and sealing, admission, resource reservation, execution lifecycle, evidence integrity/completeness, restoration and truthful results. Consumer projects own installers, migrations, compatibility, assertions, native services, installed-skill behavior and product-specific recovery. The CLI is a client; the transient runner is not a second scheduler. A disconnect never releases capacity. Public external callers and their lease API are deferred. Cooperating tools such as Mulgae may queue on an advisory named lock; Haepari never runs or observes their work (ADR-0017).

## Execution modes

Local and UTM macOS Jobs use managed execution. Automatic and manual are Job modes under the same ownership and safe-return path. Public external lease and linked continuation designs are retained in the [deferred contract](contracts/external-leases.md) and ADR-0014/0015; they are not v0.1.0 capabilities. Internal managed leases, generations, fencing and quarantine remain mandatory.

Advisory named locks are a separate, simpler coordination primitive, defined in the [lock contract](contracts/locks.md). They are FIFO grants that stay alive while the holder renews them, bounded by a maximum hold. They carry no execution permit and count toward no managed capacity or the global limit. Ordinary Mulgae reviews queue on `mulgae-review`.

## Modules and dependency direction

Use a small Go module with these conceptual packages. Directory names may change if boundaries remain intact.

| Package | Responsibility | May depend on |
| --- | --- | --- |
| `domain` | Job/resource state, identity, outcome and transition rules | Standard library |
| `application` | Managed admission, one scheduler, reserve/run/collect/restore, cancel/wait/recover | Domain and narrow ports |
| `store/sqlite` | Transactions, migrations, event cursors, admission keys | Application ports, database/sql, selected driver |
| `artifact` | Contained immutable bundles and collection | Domain, filesystem ports |
| `runner` | Per-run process supervision and receipts | Domain, OS process interfaces |
| `adapter/local` | Local resource preparation and execution | Runner and resource ports |
| `adapter/utm` | UTM identity, qualified cold restore, SSH operations | Resource ports, bounded subprocess execution |
| `transport/local` | Unix-socket API and blocking event delivery | Application services |
| `cmd/haepari` | CLI, daemon, runner entrypoints | Application wiring |

Dependencies point toward domain and application contracts. UTM names, AppleScript, SSH, skill paths, and Aquarium release rules do not enter the scheduler package. Avoid an in-process plugin ABI or a plugin marketplace in v0.1.0. Registered adapter commands are trusted local configuration, not executable hooks supplied by a submitted repository.

## Process and installation model

One Go executable provides the CLI, `daemon run`, and transient `runner` subcommands. A stable installed release runs the controller. The guest runner is the same tested release or an explicitly compatible, checksum-bound runner artifact; it travels with each job's sealed bundle instead of being baked into the baseline, so a Haepari update does not require re-qualifying baselines. Its identity is recorded in the result.

Default private state is `~/.haepari/`; `HAEPARI_HOME` may select an explicit absolute local path for an isolated instance. The root contains `control.sock`, `controller.lock`, `state.sqlite`, immutable `objects/`, owned `jobs/`, baseline registrations, and private diagnostics. Use mode 0700 for owned directories and 0600 for control/socket/database files where applicable. Output exports require an explicit destination. Do not place the database on a shared network filesystem.

An explicitly installed per-user launchd service may supervise the macOS controller. Linux service packaging is deferred. Foreground `daemon run` remains available for development. Service installation does not enroll a VM or copy credentials. Stable controller updates require a drained or reconciled instance; installing a candidate must not replace the controller currently orchestrating its test.

A process-level exclusive lock spans daemon lifetime. The daemon acquires it before opening or repairing the control socket and before dispatch. It does not kill a competing daemon. Socket peer ownership must be checked where supported; filesystem permissions remain mandatory. Same-user code execution is outside the isolation guarantee.

## Persistence model

Use SQLite with a pinned pure-Go driver, explicit transactions, foreign keys, a bounded busy timeout, WAL on a supported local filesystem, and FULL synchronous durability for control state. The exact Go toolchain and dependency versions are selected and committed during TASK-001; no document claims a current package version. SQLite permits one writer at a time; keep transactions short and never hold one across a subprocess or network call. See [S4 and S5](sources.md).

| Logical table | Essential fields |
| --- | --- |
| `jobs` | ID, admission sequence, immutable spec/input/profile digests, mode, resource ID, lifecycle state, phase deadlines, workload outcome, terminal verdict, revision |
| `admissions` | Global admission sequence, request key, canonical managed digest, job ID, durable tombstone |
| `external_requests` (deferred) | Not created by the v0.1.0 storage contract |
| `external_executions` (deferred) | Not created by the v0.1.0 storage contract |
| `mutation_receipts` | Request-scoped operation key, input digest and original response; terminal replay without renewed authority |
| `resources` | Stable ID, kind, execution mode, adapter identity if managed, configuration digest, optional baseline identity, capacity, availability, revision |
| `leases` | Resource ID, slot, managed job reference, generation, nonce, acquisition and settlement state |
| `runs` | Job ID, phase/run ID, runner protocol/artifact identity, boot identity, durable intent/start/exit references |
| `events` | Monotonic cursor, job/resource ID, transition type, bounded structured data |
| `artifacts` | Job ID, relative path, size, content digest, collection/completeness status |
| `manual_results` | Job ID, expected revision, observation identity, scenario outcomes, evidence references |
| `schema_migrations` | Applied schema version and migration identity |
| `lock_requests` | Lock ID, request key, payload digest, holder label, requested queue seconds, FIFO sequence, state, queue deadline, and created, granted and ended timestamps |
| `lock_grants` | Request ID, grant ID, slot generation, liveness deadline, maximum-hold deadline, and the release, cancellation, expiry or forced-release receipt |

Indexes enforce unique managed request keys and one active occupant per resource slot. Mutation keys prevent conflicting replay. A resource generation advances when a new ownership grant is created, not merely when a client reconnects. The daemon can reattach the existing generation after restart. Row revisions protect configuration, manual completion, cancellation, and recovery races.

Managed admission references only already-sealed objects. If required input writing fails, there is no accepted request. If the transaction commits but the reply is lost, the request key recovers the same job. Garbage collection may remove an unreferenced abandoned staging object but must not remove objects reachable from accepted jobs or retained baselines.

## Scheduling

Each managed Job consumes one named resource unit. Local resources may expose several slots; UTM resources have capacity one. The controller-wide limit counts every reserved, active and unresolved managed occupation. Lock grants are scheduled FIFO per lock and never count toward it.

Within a resource select the earliest eligible admission. Across resources select the oldest eligible head under the global limit. Paused/quarantined resources do not block unrelated eligible heads, but unresolved occupants still consume their resource and global slots. Only managed resources and `kind: lock` resources are registrable in v0.1.0.

Managed lease acquisition and `queued -> preparing` commit atomically before any adapter effect. Replayed reservation returns its existing receipt.

Managed leases span prepare, auto/manual execution, collection and reset. A job holding capacity can neither submit another job to the same installation nor block on a non-terminal job's progress (`wait`, `run`, or a followed event or log stream), including a job on another resource. See the [nested acquisition rule](contracts/managed-transitions.md#nested-acquisition).

There is no TTL takeover of active or uncertain managed work. Advisory lock grants are the only exception: they expire because they protect no Haepari-owned work. A timeout starts reconciliation. A never-started managed reservation can be cancelled only by proving no runner effect and invalidating its start authority.

Pausing prevents new dispatch and leaves current work under its existing policy. An explicit revision-bound resume re-enables only a paused, verified resource; it cannot clear quarantine. Changing a resource's baseline, identity, or executable adapter definition requires it to be idle and updates its revision. Previously accepted requests retain their resolved configuration identity; incompatible reconfiguration blocks them instead of silently selecting a new environment.

## Lifecycle and outcome model

The managed Job lifecycle is:

```text
queued -> preparing -> ready -> running ----------> collecting -> cleaning -> settling -> finished
                         |        |                     ^
                         |        +-> cancelling -------+
                         +-> awaiting_manual -----------+
queued -> finished                     (cancel, queue deadline, lost input or prerequisite)
preparing/ready -> collecting|settling (prepare failure, deadline or cancel)
any occupied phase -> reconciling      (interruption or unknown termination)
```

The [managed transition table](contracts/managed-transitions.md) is the complete, normative set; this diagram is a summary.

Manual mode performs preparation and declared setup/smoke commands before entering `awaiting_manual`. Every effectful failure moves toward collection and cleanup. A queued cancellation can finish without preparing the resource. An immutable prerequisite that is unavailable at admission rejects the request without admission and without consuming its key. A prerequisite lost, or sealed input found corrupt, after admission but before reservation finishes the job INCOMPLETE without a lease. Once effects are possible, uncertainty is retained until safe cleanup or quarantine is established.

Resource availability is `available`, `paused`, `recovering`, or `quarantined`; occupancy is recorded separately and results report the field as `resource_availability`. A job can finish INCOMPLETE with its resource quarantined. A successor cannot run until a separately requested recovery verifies the resource and restores availability.

Keep four facts separate:

- Workload outcome: pending, pass, fail, cancelled, timed_out, or inconclusive.
- Evidence completeness: complete, partial, or unavailable.
- Cleanup outcome: not_needed, verified, failed, or unknown.
- Final verdict: PASS, FAIL, CANCELLED, or INCOMPLETE.

PASS requires the workload's required assertions, complete required evidence, and verified cleanup. FAIL means a verified product/assertion failure with enough evidence and safe resource settlement. CANCELLED means cancellation completed with safe resource settlement. Missing evidence, failed reset, or required unobserved manual work makes the verdict INCOMPLETE while preserving a known product failure as a separate finding. Unresolved process state gets no verdict: the job stays `reconciling` with its occupancy retained until recovery settles it, and then it is INCOMPLETE. A result is never rewritten to erase a previously observed failure.

The [managed transition table](contracts/managed-transitions.md) specifies every active admission, phase, interruption, recovery, settlement and capacity mutation.

## Process and failure supervision

The runner records an exclusive run claim and durable spawn intent before invoking the workload, then records actual process/boot identity and an exit receipt. A repeated start with identical identity returns the original run; conflicting input fails. A crash in the intent-to-spawn window is ambiguous unless observed process/receipt evidence resolves it. Do not automatically re-execute it.

Launch a process group and implement bounded graceful termination, escalation, pipe closure, and receipt collection. A child that escapes the group cannot be assumed dead. Inspect the supported OS evidence and quarantine an affected resource if cleanup cannot establish safety. `exec.CommandContext` by itself does not provide this process-tree contract; Go's default cancellation targets its associated process. See [S6](sources.md).

For guest workloads, SSH starts or reattaches a transient supervisor whose lifetime is independent of the SSH connection. It is not another scheduler. Use a qualified detached execution mechanism, record the mechanism and boot identity, and persist receipts in a contained guest run directory. Startup/read/cancel commands verify the job and resource generation. A cancelled SSH client never serves as proof that the remote workload stopped.

On daemon startup, block affected managed resources, inspect unfinished managed runs, reconcile known completion, collect evidence, and clean up.  Resume observation of an existing running supervisor when its identity is proven. Unknown state keeps the job `reconciling` with its occupancy retained and its resource quarantined; a verdict (INCOMPLETE) follows only recovery settlement, never a repeated workload. Independent healthy resources may resume after their own recovery checks.

## Immutable artifacts and profiles

A submitted bundle explicitly lists its files and executable modes. Snapshot creation never follows unlisted host paths, reads credentials, or silently copies the complete home. A caller may package a selected dirty worktree, but the result binds actual bytes rather than claiming the base commit is the candidate.

An environment profile is a consumer-owned, frozen selection of components and expected inventory. A baseline registration maps that profile digest to a qualified VM image. An alias such as a consumer's default baseline may be resolved during admission, but the accepted request stores the resulting immutable ID. Deduplication uses the submitted request's canonical digest, so an identical replay after an alias moves returns the original admission. No queue execution downloads or resolves `latest`.

The base VM is never a workload target. A qualified cold restore materializes an owned working VM from a powered-off baseline, preserving all required package metadata and disk/configuration relationships. The UTM adapter owns the working VM's UUID and lifecycle. It must reconcile UTM registration with package state, not replace arbitrary `.utm` files while they remain running or ambiguously registered. Concrete commands are probed during TASK-034 and qualified against the installed backend during TASK-016; documentation support for copy/export is not proof of the full recovery procedure. See [S1, S2, S3](sources.md).

During a migration scenario, the starting profile is the prior supported environment. Product recipes create prior-version state, apply the target installation unit, and verify preserved behavior. Haepari does not reset between those actions. A whole-system release profile may override several tools together; the default product job overrides only its selected component unit.

## Skill and configuration boundary

A skill is an identified complete directory, not just SKILL.md. A consumer lock records upstream identity, full tree digest, expected target, selected home, and relevant effective configuration. Paired skills can share the binary release or have an independent source pin. Consumer recipes enforce the appropriate native rule.

The queue does not become a global skill installer. It transports frozen payloads and invokes the consumer's guest installation/inspection recipes. The guest result includes before/candidate/restored inventories. Those recipes verify declared global and project discovery locations, stale files, duplicate names, and connection identities, then open a fresh product session for installed-behavior observation. Hash equality alone is installation evidence, not behavior evidence. See [S7](sources.md) for the external skill-directory concept.

## Security and authority

Haepari is a single-user scheduler, not an OS sandbox. Local jobs execute authorized commands with their configured privileges. UTM jobs gain isolation only to the extent that shared folders, networking, credentials, and host-side adapters preserve it.

Host-side adapter programs are registered by the operator, pinned, and outside project payload control. VM profiles default to no writable host home/source/skill mounts, no SSH agent forwarding, no inherited SSH configuration surprises, and an explicit known-hosts identity. Pass argument data via structured input files or a fixed validated runner protocol, never interpolate user strings into remote shell commands.

No job bundle contains host authentication files. Test credentials are separately provisioned inside the guest or supplied through a narrowly approved secret reference. Reusable images containing test authentication require explicit approval, private storage, and an expiration/reprovisioning policy. Secrets must not enter logs, evidence manifests, exported images, request digests, or this design package. Offline tests are default; live endpoints and cost authority remain a consumer request property.

A VM reset cannot undo messages, repository pushes, external databases, or model charges. Consumers must use disposable endpoints and synthetic data or declare and authorize the external effects. SSH identity checks and local state isolation do not confer automatic network egress isolation. See [S8](sources.md).

## Result delivery and integration

The CLI submits once and waits on a blocking event stream with a durable cursor. Reconnection observes the same job. `make test-vm` maps the final verdict to a nonzero/zero gate result; inspect commands return successfully even when reporting failure. It prints the job ID early enough for a caller to reconnect.

Project-owned guest entrypoints never call `make test-vm` or re-enqueue themselves. The all suite is an ordinary host wrapper that submits distinct install and migration jobs in order using the same frozen candidate; there is no Haepari DAG language.

Aquarium consumes a release/capability handoff and owns its rollout. The controller never reads Sorage's database or Vault. Sorage transports implementation requests and returned evidence between project owners; it does not schedule VM work.

## Deferred external execution

ADR-0014 and ADR-0015 preserve the R2 proposal. It is post-v0.1.0, subject to B1/B2 re-entry criteria in the [deferred contract](contracts/external-leases.md). Ordinary Mulgae reviews queue on the advisory lock instead (ADR-0017). Provider-call rate limiting is not part of the active architecture.


## Durability and service supervision

Control records use SQLite WAL with `synchronous=FULL` on a qualified local filesystem, explicit transactions, and a tested driver/VFS. Seal external objects through private temporary files, content hash, file sync, atomic rename and parent-directory sync before committing references; record cleanup of orphan staging files. On darwin, open the store with `PRAGMA fullfsync=ON` and `PRAGMA checkpoint_fullfsync=ON`; TASK-004 records whether the pinned driver honors them, and if it does not, the power-loss durability claim is blocked. Kill tests do not prove power-loss durability. Backup only a quiesced consistent store. Unix socket paths use a bounded private root and startup checks for platform path limits; a long `HAEPARI_HOME` is rejected before bind.

**LaunchAgent configuration.** The development and release LaunchAgents run in the user's `gui/<uid>` domain with:

- `LimitLoadToSessionType=Aqua`
- `AbandonProcessGroup=true`

**Runner survival.**

- Runners start in their own session and process group, so launchd does not kill them when the controller exits.
- TASK-004 observes this behavior and TASK-011 verifies it.
- `setsid` alone is not proof of survival from service-manager cleanup.

**Reattachment after restart.**

- The contract may reattach only a surviving runner with matching durable run/boot identity.
- Otherwise it records an interrupted execution, retains occupancy, and reconciles or quarantines.
- Transparent continuation is not promised.
- Because runners survive, stopping the controller to change global configuration does not by itself end local jobs.

**UTM host control.**

- It needs the logged-in Aqua session and Automation approval for the exact controlling executable.
- Every build that controls UTM through a LaunchAgent uses a stable signing identity so that approval survives rebuilds and updates: the TASK-034 probe agent, the TASK-011 development service and the release. Ad-hoc signing is only for builds that never control UTM.
- Local scheduling and SSH guest execution have independent prerequisites.

**Host capacity.**

- A missing session or permission, or exhausted host VM capacity, is a temporary host-capability block. The cause is host-wide, so the controller pauses every `utm-macos` resource on this host with the reason recorded; queued jobs stay queued. The operator resumes them after restoring the capability.
- A contaminated working VM is quarantined.
