# Controller configuration contract

Design Revision 3.4. This contract owns the operator configuration schema `haepari.config/v1`. It does not own resource definitions or transitions:

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
- An explicit `--config` path overrides the root's configuration file.
- Precedence: CLI root/config selection first, then the environment root, then the default root.
- Config file values do not silently override root selection.
- No personal workstation path is normative.

**Rejected before any mutation.** Roots that are relative, shared or network-mounted, symlink-escaping, world-writable, or whose socket path would exceed the platform limit (104 bytes for `sun_path` on macOS).

## Schema

`haepari.config/v1` is a closed object; unknown fields are rejected.

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `schema` | Exactly `haepari.config/v1` | — | Schema identity. |
| `revision` | Positive integer | 1 | Each accepted change increments it. |
| `global_limit` | Positive integer | 1 | Controller-wide limit on reserved, preparing, running, manual, collecting, cleaning and uncertain managed occupations. See the note below the table. |
| `host_macos_guest_limit` | Integer 1–2 | 1 | Registered running, pending-start and uncertain-power VMs counted conservatively. At the limit, Haepari may stop only an eligible registered VM whose operator-owned resource explicitly permits automatic stop; otherwise the requesting Job waits under its queue deadline. Raise the limit only to the bound observed in TASK-016. Apple's license allows at most two additional macOS instances per Mac, and the operator's own VMs count too ([S11](../sources.md)). |
| `default_deadlines` | Object of positive integers: `queue_seconds`, `prepare_seconds`, `run_seconds`, `manual_seconds`, `collect_seconds`, `cleanup_seconds` | 14400, 900, 3600, 7200, 300, 900 | Defaults when a job omits a phase budget; the key names match the job schema. A job's budgets cannot exceed its resource's lifecycle budgets. |
| `limits` | Object of positive integers: `request_bytes`, `log_bytes_per_stream`, `artifact_bytes`, `artifact_count`, `event_retention_days`, `result_retention_days` | 1 MiB, 256 MiB, 1 GiB, 1000, 30, 90 | Resource-protection limits. Truncation and omissions appear in results; required evidence is never silently discarded. |

**Why `global_limit` defaults to 1, and what that costs.** A fresh single-user installation runs one managed occupation at a time. That keeps host load predictable and makes contention visible instead of surprising. The cost is that a single uncertain VM job blocks every other resource. Operators who use local and VM resources together should set it to 2. Case V09 runs with 2.

The [configuration example](../examples/config.template.json) shows every field.

## Development-loop profile

VM tests run in the development loop, and the owner expects 10–30 VM jobs a day. The defaults above stay conservative. For this load, the recommended setup is:

- **Two `utm-macos` resources.** Each is its own persistent VM with its own MAC address, used without reset in queue order ([ADR-0018](../architecture-decision-records/0018-persistent-vm-no-reset.md)). A job names its resource, for example one VM for install tests and one for migration tests.
- **`host_macos_guest_limit` 2,** when the operator runs no other macOS VM on the host.
- **`global_limit` 3:** two VM jobs plus one local job. Advisory locks never count toward it.

**Measure before adding capacity.** TASK-016 records the VM cycle phases, and result `timings` give queue wait p50/p95. The host allows at most two macOS guests. If waits stay high with two VMs, shorten the job cycle instead of adding VMs.

## Queue deadlines for capacity-one resources

A queued job on a capacity-one resource waits for every job ahead of it.

- **Rule:** set `queue_seconds` to at least (jobs ahead + 1) × the resource's maximum job duration. "Jobs ahead" includes the job currently running.
- **Maximum job duration:** prepare + run + collect + cleanup for automatic jobs. For manual jobs use the manual deadline in place of the run budget; a manual job ahead can hold the resource far longer than the example below.
- **Example:** the [VM job example](../examples/job-vm.template.json) uses 14,400 seconds. That leaves room for one automatic job ahead with a 95-minute maximum duration.
- **Warning:** `haepari doctor` warns when a resource's default queue deadline is shorter than one maximum job duration.

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

The foreground controller accepts `daemon run --host-macos-guest-limit 1|2`, default 1, before opening its state or issuing effects. Scheduling counts observed running VMs and pending/uncertain start claims, uses only eligible operator-authorized registered victims, and preserves blocked queue deadlines. This explicit option implements the host limit without claiming the TASK-023 general configuration loader or TASK-016 real limit qualification. Automatic development VM input is bounded to 48 MiB payload and 96 MiB encoded guest request before preparation; output and log defaults retain the TASK-012 bounds above.

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
The general configuration loader, doctor and operator policy remain TASK-023.
