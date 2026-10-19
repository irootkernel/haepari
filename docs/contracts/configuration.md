# Controller configuration contract

Design Revision 3.5. This contract owns the operator configuration schema `haepari.config/v1`. It does not own resource definitions or transitions:

- Resource definitions (`haepari.resource/v1`) are in the [resource contract](resource-and-runner.md).
- Lifecycle effects are in the [managed transition table](managed-transitions.md).

## Location and precedence

Default macOS paths live under the owning user's private Haepari root:

| Path | Purpose |
| --- | --- |
| `config.json` | Operator configuration |
| `control.sock`, `controller.lock` | Control |
| `state.sqlite` plus WAL files | State |
| `objects/` | Sealed input |
| `jobs/` | Evidence |

**Selecting the root and config file.**

- `HAEPARI_HOME` may select an absolute private root before startup.
- An explicit `--config` path overrides the root's configuration file. A relative filename is resolved against the process working directory. The resulting file is a private regular file and may sit outside the selected root. The root itself must be absolute.
- Precedence: CLI root selection first, then the environment root, then the default root. Explicit configuration does not select a different root.
- Config file values do not silently override root selection.
- No personal workstation path is normative.

**Rejected before any mutation.** Roots that are relative, shared or network-mounted, symlink-escaping, world-writable, or whose socket path would exceed the platform limit (104 bytes for `sun_path` on macOS). A configuration filename is not refused merely for being relative. After resolution it is refused when it is a symlink, hard link, socket, named pipe, group- or world-writable, or not on a local filesystem.

## Schema

`haepari.config/v1` is a closed object; unknown fields are rejected.

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `schema` | Exactly `haepari.config/v1` | — | Schema identity. |
| `revision` | Positive integer | 1 | Each accepted change increments it. |
| `global_limit` | Positive integer | 1 | Controller-wide limit on reserved, preparing, running, manual, collecting, cleaning and uncertain managed occupations. See the note below the table. |
| `host_macos_guest_limit` | Integer 1–2 | 1 | Accepted compatibility setting. Haepari does not enforce physical running-VM capacity or issue VM start/stop commands. Operators manage VM power and host constraints. Managed occupancy is governed by `global_limit` and each resource capacity. Selected-VM readiness and historical power-claim fences remain mandatory. |
| `default_deadlines` | Object of positive integers: `queue_seconds`, `prepare_seconds`, `run_seconds`, `manual_seconds`, `collect_seconds`, `cleanup_seconds` | 14400, 900, 3600, 7200, 300, 900 | Defaults for an active phase key that native `submit` or `run` omits. They are not resource ceilings and not job maxima. A v2 resource supplies explicit lifecycle ceilings. A v1 resource uses the 366-day legacy product ceiling. |
| `limits` | Object of positive integers: `request_bytes`, `log_bytes_per_stream`, `artifact_bytes`, `artifact_count`, `event_retention_days`, `result_retention_days` | 1 MiB, 256 MiB, 1 GiB, 1000, 30, 90 | Resource-protection limits. Truncation and omissions appear in results; required evidence is never silently discarded. |

**Why `global_limit` defaults to 1, and what that costs.** A fresh single-user installation runs one managed occupation at a time. That keeps host load predictable and makes contention visible instead of surprising. The cost is that a single uncertain VM job blocks every other resource. Operators who use local and VM resources together should set it to 2. Case V09 runs with 2.

The [configuration example](../examples/config.template.json) shows every field.

## Development-loop profile

Use one already-running registered `utm-macos` resource (capacity one) and one local resource, on the same machine and user. The recommended development limits are `host_macos_guest_limit=1` and `global_limit=2`, so uncertain VM work does not consume the only global slot needed by independent local work. Fresh installation defaults remain one; the existing startup flags implement these explicit limits.

Use the persistent VM without per-Job reset. Local/fake concurrency fixtures verify managed limit enforcement and independent progress. Two-VM/global-three operation remains optional DF-004 hardening. The operator starts and stops the VM outside Haepari; the product has no VM power issuer. An unstarted selected VM waits in the queue rather than triggering a start or a capacity-driven stop.

## Queue deadlines for capacity-one resources

A queued job on a capacity-one resource waits for every job ahead of it.

- **Rule:** set `queue_seconds` to at least (jobs ahead + 1) × the resource's maximum job duration. "Jobs ahead" includes the job currently running.
- **Maximum job duration:** prepare + run + collect + cleanup for automatic jobs. For manual jobs use the manual deadline in place of the run budget; a manual job ahead can hold the resource far longer than the example below.
- **Example:** the [VM job example](../examples/job-vm.template.json) uses 14,400 seconds. That leaves room for one automatic job ahead with a 95-minute maximum duration.
- **Budget responsibility:** The consumer chooses a queue deadline before admission. The implemented selected-resource `doctor` does not calculate queue budgets; TASK-023 implements the bounded queue/retention projection pages, not budget advice.

## Changes

**Global configuration.**

1. Edit the private file while the controller is stopped.
2. At startup, the controller validates it and compares its digest and revision with the stored effective configuration.
3. It commits the new values and an audit receipt before dispatch.

A failed validation keeps the old effective configuration and refuses startup. Runners run in their own session and process group, and the LaunchAgent sets `AbandonProcessGroup=true`, so a controller stop does not end local jobs. On restart, the controller reattaches them or reconciles them.

**Resource changes** use expected revisions and durable mutation receipts.

- **Raising limits** takes effect for future dispatch.
- **Lowering a limit below current occupancy** never evicts jobs; dispatch pauses until occupancy is below the new limit.
- **Lowering resource capacity** also preserves existing reservations and pauses further dispatch.
- **Changing VM identity or adapter** requires zero active reservations. Queued requests pinned to the old revision need explicit handling: keep the old immutable definition until they drain, or cancel them with explicit receipts. Never retarget silently.
- **Removal** requires an idle resource, no queued or pinned requests and no retained recovery reference. Removing a registration never deletes a VM package.
- **VM registration** checks that UTM lists the UUID, that no other resource names the same UUID or MAC address, and that the SSH host key pin is present. It never creates, clones, boots or deletes a VM.

## TASK-012 implementation boundary

The current local controller uses the default 256 MiB combined stdout/stderr log prefix, 1 GiB and 1000 required outputs per collection, and 64 KiB delivery pages. Truncation and write failures become explicit immutable evidence gaps. Required outputs exceeding a bound remain protected and cannot produce complete evidence. This implements default resource protection, not the full operator configuration parser. The table's configurable retention days do not authorize automatic deletion: only explicit revision-bound purge and protected-evidence recovery are implemented. TASK-023 owns effective configuration and performance qualification; changing a frozen managed schema requires the F1 contract decision process.

## TASK-015 implementation boundary

The foreground controller accepts `daemon run --host-macos-guest-limit 1|2`, default 1, before opening state. The field remains a compatibility setting. A new VM reservation requires the selected registered VM observed exactly started; other states wait under `vm_not_started` and the original queue deadline. There is no stop-victim selection or physical running-VM count gate. Global/per-resource occupancy, full inventory fences and historical unresolved claims still apply. Automatic development VM input is bounded to 48 MiB payload and 96 MiB encoded guest request before preparation; output and log defaults retain the TASK-012 bounds above.

## TASK-037 startup limits

`daemon run --global-limit <positive-int>` selects the ledger's controller-wide
limit, default 1. Zero, negative, malformed and overflowing values refuse before
opening state or issuing effects. `--host-macos-guest-limit 1|2` remains separate
and defaults to 1. Every VM registration still has capacity one. Startup applies
the selected global limit to local and VM reservations, including unresolved
occupancy; restarting with a lower limit never erases existing reservations.

`service plan` accepts the same optional flags. An explicitly selected limit
produces `haepari.development-service-plan/v2`, binding both positive global and
bounded host limits into the exact plist argv. A v1 plan keeps its original argv
and refuses new fields, including null values. Install/uninstall reverify the
saved plan; neither replaces a running service or changes limits by implication.
TASK-023 implements the general configuration loader, configuration-aware diagnostic policy and the bounded operator projection pages; TASK-017 implements the bounded selected-resource prerequisite doctor.

## TASK-023 M1 implementation boundary

M1 loads closed `haepari.config/v1` before creating a state root. CLI `--root` selects the root before `HAEPARI_HOME` and the default, and a relative root is refused. Explicit `--config` overrides only the configuration file. A relative filename is resolved from the process working directory; an absolute private file may sit outside the root. Neither changes the root. Symlink components, hard links, sockets, named pipes, group or world permissions, and non-local filesystems are refused. A missing default `config.json` uses the validated defaults plus legacy CLI limits. An explicit missing, unsafe, or invalid file is refused with no fallback. An explicit capacity flag that disagrees with a present file value is refused; an omitted flag is not an explicit value.

The controller stores the canonical document, its digest, the values digest, the revision, and one audit row before socket publication or dispatch. The same explicit revision and digest replays without a second audit. The same revision with a changed digest, a lower revision, or a skipped revision keeps the stored row and refuses startup. Implicit no-file startup reuses the accepted revision when the effective values match and otherwise takes the next revision. Lowering a limit does not evict reserved, uncertain, or pinned jobs.

Supported limit fields stay positive and at most the frozen caps: request 1 MiB, one shared combined stdout/stderr log bucket of 256 MiB, artifacts 1 GiB, and 1000 artifacts. A larger value is an error before effects. It is not clamped. `haepari.runner-request/v1` is unchanged: omitted or zero `log_limit_bytes` remains the 256 MiB combined cap. Retention days are stored and are not an automatic purge. Private SQLite schema 11 is the storage identity for this document and is not a public wire version. M3 is authorized for the bounded local, fake, and native CLI checks, the historical TASK-038 carry, one common gate, and the final task review, with no fresh live-VM, provider-workload, or benchmark campaign.

## TASK-023 M2a accepted configuration and admission bounds

M2a reads the accepted row. It does not read the mutable file for admission or for CLI defaults. `GET /v1/operator/projection?view=config` returns closed `haepari.operator-projection/v1`: `schema`, `view` exactly `config`, `installation_id`, `observed_at`, the full accepted document, `document_digest`, and `values_digest`. The default view is `config`. A missing or corrupt accepted row is an error. The handler does not substitute defaults. The response is bounded and contains no secrets or private paths. Native `submit` and `run` call this view only to fill absent active phase keys, before strict decode and the canonical hash. A fully explicit spec does not call it. The server never fills budgets. The same request key with a changed materialized spec conflicts and creates no Job. An unaccepted file edit changes neither the view nor a saved job.

A new production admission requires a valid accepted row. NewLedger fails closed: a missing accepted row refuses a new admission. Only an explicit UseLegacyFixtureBounds call, or an equally explicit fixture option, selects `policy_source=legacy_defaults`. RequireAcceptedConfiguration does not define that constructor default. The job and its immutable bounds row commit together. The row records `request_bytes`, `log_bytes`, `artifact_bytes`, `artifact_count`, `event_retention_days`, `result_retention_days`, `policy_source=accepted_configuration`, and the accepted revision and document digest. On each later effect, that pin is checked against the historical audit document for its revision, not against the latest accepted row. The audit document must be canonical, the pin digest must match it, and the six limit fields must equal the audited limits. A later accepted revision does not replace an existing pin. `request_bytes` limits the job spec JSON after replay and before Seal. It does not limit the sum of selected files. M2a enforces that request bound and the resource phase ceilings. It stores the log, artifact, and retention values for a later milestone. A missing bounds row on a current job fails closed and keeps occupancy and protected evidence. Existing jobs migrated to schema 12 receive the historical fixed caps, advisory 30 and 90 days, `policy_source=legacy_defaults`, and a NULL configuration reference. That backfill does not invent an accepted revision or audit.

## TASK-023 M2b-1 runtime bounds

M2b-1 uses that same immutable validated row at runtime. It does not read the mutable configuration file and does not substitute a default. A later accepted revision still does not replace the pin. A missing, malformed, or historical-audit mismatch fails closed before request publication and leaves occupancy and protected evidence unchanged. The local and VM request builders read the pin before the first marshal, receipt, and publication. `log_limit_bytes` is set only when the pin is below 256 MiB. Zero or an omitted `log_limit_bytes` remains one combined stdout/stderr bucket of 256 MiB. An already saved request, spec, receipt, digest, or clock is not rewritten. Collection and recovery use the minimum of the saved request's effective log limit and the pin. Effective zero is 256 MiB. The admitted artifact bytes and count govern required whole files. An exact fit is accepted. One byte or one file over the pin is not copied. The log is a separate bucket. A saved request wider than the pin stays byte-identical. A retained log larger than that minimum is omitted from the accepted result logs, the on-disk file is kept, and the result records `pinned_log_exceeded` with the retained size as the offset and `dropped_bytes` 0. That reason is not a native log-manifest gap. Ordinary truncation keeps the readable prefix, the native `log_limit` gap, the native process outcome, and incomplete protected settlement. A log with no saved request fails closed. An event whose encoding at the largest cursor exceeds 2 MiB is refused before either event insert, and that transaction rolls back. Phase budgets stay the admitted spec. There is no second phase cap. This slice adds no diagnostic projection, CLI flag, or report.

M2b-2 is not part of this slice and is not waived. It owns legacy `doctor --resource`, a new versioned `doctor --projection`, and bounded paged queue and retention projections. A queue figure must use explicit resource ceilings and known admitted budgets, must treat unknown occupied execution as unknown rather than a finite guarantee, and must check overflow. Configuration defaults are not job maxima. Unknown, protected, permanent, and unavailable identities are not a deletion list. There is no automatic deletion and no new purge. A read-only diagnosis does not publish `eligible=true` and is not mutation permission. Existing revision-bound purge still revalidates its authority. Omitted or zero runner `log_limit_bytes` remains the single 256 MiB combined cap.

## TASK-023 M2b-2 F1 diagnostic contract

F2 implements this diagnostic contract. `GET /v1/operator/projection?view=diagnostic&section=queue|retention` returns `haepari.operator-diagnostic/v1`. Help lists that schema and the second doctor usage, `doctor --projection --section queue|retention [--resource <id>] [--cursor <cursor>] [--limit <1-20>] --json`, and keeps `doctor --resource <id> --json`. The implemented config view, `haepari.doctor/v1`, `GET /v1/resources/{id}/doctor`, and legacy `doctor --resource` stay as they are.

`haepari.operator-diagnostic/v1` is the body for the existing `GET /v1/operator/projection?view=diagnostic&section=queue|retention`. An empty view and `view=config` stay `haepari.operator-projection/v1`. Config GET stays fail-closed: an absent accepted row is `NOT_FOUND`, a corrupt row is `INVALID_REQUEST`, and there is no `Defaults()` document. A queue page uses a `resources` array only. A retention page uses a `jobs` array only. Absent optional fields are omitted. Arrays are non-null.

Header fields are `schema`, `view`, `section`, `installation_id`, `observed_at`, `any_unknown_occupancy`, `current_config`, and `page`. `observed_at` is the server snapshot UTC and is not a query parameter. `page` has `limit`, `more`, and `next_cursor` only when `more` is true. Allowed query keys are only `view`, `section`, `limit`, `cursor`, and `resource`. A diagnostic raw query that `url.ParseQuery` rejects, including a malformed percent or a semicolon, is 400 `INVALID_REQUEST`, retry `none`, and does not drop that filter into the installation page. An earlier explicit `view=config`, an empty `view=`, or a bare `view` key with no `=` wins and stays on the legacy first-value config path. An empty view and `view=config` keep the legacy first-value parse. Duplicate keys, an unknown key, a cursor whose kind, installation, filter, shape, or upper bound does not match, a missing `section`, and a request that names both `queue` and `retention` are 400 `INVALID_REQUEST`, retry `none`, with no repair and no rewind. `limit` is 1 through 20 and defaults to 10. The handler does not clamp it. An explicit `resource` that is absent is 404 `NOT_FOUND`. An omitted `resource` is the installation page. A page empty because of the upper bound is 200 with an empty array.

The cursor reuses the existing `pageCursor` codec. A queue cursor carries `LastID` and `UpperID`, both resource ids, and its kind is `operator-diagnostic-queue`. A retention cursor carries `Last` and `Upper`, both admission sequences, and its kind is `operator-diagnostic-retention`. The filter is the requested resource selection: empty for the installation page, or that resource id when the query names one. Queue shape is `LastID` and `UpperID`, and both use the existing resource id shape. A filtered queue `UpperID` matches the requested resource, and the first filtered page sets that upper. The initial installation upper is not required to equal a later `MAX`. Retention shape is `Last` and `Upper`. The handler enforces kind, installation, filter, shape, and last less than or equal to upper. The cursor has no MAC. There is no new cursor store and no repair. `next_cursor` is the last returned row, never the withheld next row. `more=true` advances. There is no row crop and no second progress, resync, or `purged_through` cursor. The serialized diagnostic body maximum is 65536 bytes, including the header, `current_config`, the section array, and `page.more` / `next_cursor`. Cursor size changes with the last returned row. The handler measures each whole-row prefix and shrinks without skips. A row that does not fit is withheld and retried as the first row of the next page. A first row whose full body exceeds 65536 bytes is 400 `INVALID_REQUEST`.

The snapshot is one read-only database transaction. Occupancy proof is the durable ledger plus the in-process controller worker table. Missing worker proof is unknown. There is no caller `known_unoccupied`, no host or provider probe, and no `Defaults()`. `current_config` is unknown only for validation absence or corruption, with reason `absent` or `corrupt`. A real database or snapshot error stays the existing `STORAGE_UNAVAILABLE`, HTTP 503, retry `reconcile`. That error is not hidden as unknown and there is no fallback. When the diagnostic header is absent or corrupt, the diagnostic HTTP status stays 200 so historical pins are not hidden or marked invalid. A present header has only `status=present`, `revision`, `document_digest`, `values_digest`, `global_limit`, and `host_macos_guest_limit`. The header has no day counts. `global_limit` is not a divisor.

A queue row has `resource_id`, `kind`, `availability`, `status` of `bounded`, `unknown`, or `not_applicable`, `reason` only when `status` is not `bounded`, and, only when `bounded`, `ceiling_source` of `lifecycle_budgets` or `legacy_product_ceiling`, `ceilings` for all six of `queue_seconds`, `prepare_seconds`, `run_seconds`, `manual_seconds`, `collect_seconds`, and `cleanup_seconds`, `included_jobs`, `backlog_seconds`, `prospective_ceiling_seconds`, and `total_seconds`. v2 ceilings are the stored lifecycle budgets. v1 ceilings are the product ceiling `366*24*60*60` for all six. Defaults are not maxima. `queue_seconds` is shown in `ceilings` and excluded from the sum. `included_jobs` counts every nonfinished admitted job, including queued jobs. `backlog_seconds` sums those jobs' mode-active execution budgets from the admitted budgets, not the ceilings. Auto is prepare plus run plus collect plus cleanup. Manual is prepare plus manual plus collect plus cleanup. The sum excludes `queue_seconds` and the other mode's phase. Queued jobs stay in the sum. `prospective_ceiling_seconds` is the maximum of the auto ceiling sum and the manual ceiling sum, once. `total_seconds` is backlog plus prospective, overflow-checked. An idle bounded resource still has `included_jobs` 0 and `backlog_seconds` 0, present rather than omitted. `sum_overflow` omits the three sums, `included_jobs`, and `ceilings`. The row does not use MaxInt64 or a partial sum. Reason priority is `resource_spec_unreadable`, then `unknown_occupied_execution` for reconciling, pending or uncertain power, occupancy mismatch, or missing worker proof, then `unreadable_admitted_budget`, then `sum_overflow`, then `lock_resource` with status `not_applicable`. There is no capacity division, no `active_auto`, no `active_manual`, and no `summed_active_seconds`. `any_unknown_occupancy` does not erase another resource's bounded total and is not a global SLA. Queue applies per resource across supported local, VM, and lock resources. A local budget is not a global SLA, and the projection is not limited to the local kind.

A retention row has `job_id`, `resource_id`, `state`, `mode` of `auto`, `manual`, or `unknown`, `policy_class` of `accepted`, `advisory_default`, or `unknown`, `policy_revision` only when `accepted`, `event_retention_days`, `result_retention_days`, `retention_class`, `event_window`, `result_window`, and `permanent_replay_count`. `accepted` is the historical validated pin. A later accepted revision does not replace that revision or those days. `advisory_default` is the exact legacy 30 and 90 labels, with no windows and no due. Unknown policy is missing, corrupt, audit mismatch, noncanonical, digest drift, or any other source, with no 30/90 fallback. Unknown policy omits `event_retention_days`, `result_retention_days`, and `policy_revision`. `retention_class` is `unknown`, `active`, `occupied`, `protected`, `unavailable`, or `clear`, in that priority. There is no permanent class. `permanent_replay_count` is the count of the nine existing private-event binding kinds on the retention allowlist: `recovery_intent`, `phase_reconciled`, `vm_prepared`, `vm_retained`, `vm_transport_binding`, `vm_transport_admission`, `vm_retire_intent`, `power_cessation`, and `local_workspace`. It is not every permanent database identity and not every payload. The row emits a computed count, including 0. A missing event payload above the public floor omits it. A count above 0 does not force payload rows to permanent and does not disable windows. The row does not emit paths, payloads, or permanent-event cursors. The age overrides that omit both windows are `active`, `occupied`, `protected`, `unavailable`, and `unknown`. Windows exist only when `policy_class` is `accepted` and `retention_class` is `clear`. `event_window` is `within` or `warning` only. It is an admission-age upper bound, `admitted_at` plus `event_retention_days`. It is never an event past, a due time, or a per-event age. `result_window` is `within` or `past` only when `verify.ended_at` is valid and `admitted_at` is at or before `ended_at` and `ended_at` is at or before `observed_at`. Otherwise the row omits `result_window`. Days stay positive int64. If days exceed MaxInt64/86400, or `admitted_at` or `ended_at` plus days times 86400 overflows, the row omits the windows only. The calculation does not multiply `time.Duration` and does not replace the pin days. A missing event payload above the public floor marks that job `retention_class=unknown` and omits its counts and windows. That row does not stay `clear`. Other rows are unaffected. Computable counts, and unknown for other reasons, can still appear. Protected includes quarantine, recovering, pending or uncertain power, a current unresolved protected-evidence hold, unresolved recovery, and incomplete purge. Nonempty historical protected evidence is a current hold only while it stays unresolved. The raw result list stays immutable. A later complete exact resolved supplementary receipt releases that current hold. Unreadable proof is `unknown`. Unresolved, mismatched, partial, or corrupt receipts stay `protected` or `unknown`. A later `phase_reconciled` event is not completion proof for a `recovery_intent`, and an unrelated later phase event does not clear an unresolved intent.

Errors add no ErrorCode. A bad query is 400 `INVALID_REQUEST`, retry `none`. An explicit absent resource is 404 `NOT_FOUND`. An unreadable database snapshot is 503 `STORAGE_UNAVAILABLE`. This surface has no eligible flag and no GC. It adds no deletion list, no purge command, no diagnostic storage migration, no `haepari.retention-projection/v1`, and no constant checklist booleans.
