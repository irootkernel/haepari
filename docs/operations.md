# Operator runbook

This is the planned operational contract. Commands are the proposed Haepari surface; UTM-specific control arguments must come from the installed version's qualified help/dictionary. Do not execute examples before the implementation and operator configuration exist.

## Provision the controller

Install the verified Haepari release and its complete `use-haepari` tree through an explicit installation action. Select a private local state root. Run version/capabilities and read-only doctor. Start the daemon through the approved foreground or user-service path. Do not initialize by copying another user's state database.

Register a local command resource first and execute harmless deterministic work. Verify submit, wait, output collection, cancellation, and restart before registering destructive VM reset capability. The local resource runs commands on the host and is not a sandbox.

## Provision the macOS baseline

Create one test-only macOS VM using UTM's supported path. Record host/guest OS, UTM/backend identity, CPU/memory/disk allocation, test user, GUI login/service requirements, and automation permissions. Run the TASK-034 probe first when it has not been run on this host. Control UTM only from the Haepari LaunchAgent in the logged-in Aqua session; `utmctl` does not work from SSH sessions or before login. Enable Remote Login only for the required test user and provision a dedicated SSH credential/known-hosts mapping. This is a separate setup effect.

Install the chosen stable product combination and native skill locations using approved exact artifacts. External global skills and per-home rules are part of the combination. Establish new-session discovery and service/MCP readiness. Keep real production accounts and data out of the image. Provider authentication uses a separately approved test setup; document expiration and refresh rather than copying host authentication files.

Shut down cleanly and create the complete immutable baseline. Qualify copying, UTM registration, SSH identity, guest job nonce, candidate installation, and whole-environment restoration. A whole-package backup is not automatically a working restore method until that cycle passes. Do not assume snapshot support; a beta UTM snapshot or disposable mode is not a v0.1.0 reset strategy.

Register the baseline/profile digest and working-VM ownership in Haepari. Bind each baseline to one resource; use separate resources for the product-absent install profile and each prior-version migration profile. When one baseline is derived by copying another, regenerate its MAC address, machine identity and guest SSH host keys before registration; registration refuses colliding identities. Keep template and working VM clearly separated. VM address and SSH mapping follow the method recorded by TASK-034 (by default the host DHCP lease for the working copy's MAC, checked against an explicit mapping); no QEMU-agent-only address query is assumed. Never disable host-key checking to make a clone connect.

## Run a project test

From the project, use its documented `make test-vm` with a named suite, mode, and baseline. The wrapper freezes candidate/test input, displays its identity, submits once, and prints the job ID. It waits on the same ID. Continue host development without installing that candidate into the stable host toolchain.

To inspect a dropped session, use `inspect`, `events`, or `wait` on the original job. Do not submit a replacement merely because the caller lost its result. An explicit new run after a settled failure uses a new key and retains the link to the old result.

For hands-on, wait for awaiting_manual, open the exact owned UTM guest, and follow the project checklist. Do not install unrelated tools while another project's lease is active. Complete the result with observed and unobserved items. The VM remains occupied through collection/reset.

## Diagnose failures

| Observation | Action |
| --- | --- |
| Job queued | Inspect resource availability, FIFO predecessors, and controller-wide capacity. |
| Missing VM, automation permission, SSH or native login | Report prerequisite gap; do not install on the host instead. |
| Host macOS guest limit reached | The affected job finishes INCOMPLETE with a temporary prerequisite gap. The controller pauses every `utm-macos` resource on this host with reason `host_vm_limit`; queued VM jobs wait. Stop an unrelated VM, then `resource resume` them. Nothing is quarantined. |
| `RECOVERY_BLOCKED` on resource recovery | Recover the occupying job first with `job recover`, then recover the resource. |
| Workload failed, cleanup verified | Read the retained product evidence; a new run is a separate submission. |
| Connection lost | Reattach the existing run/job; do not re-execute the original command. |
| Resource quarantined | Inspect the exact failed phase and request a current recovery plan. |
| Reset would discard uncollected evidence | Disclose that loss and obtain the applicable recovery authority. |
| State database is newer than executable | Install a compatible stable controller; never downgrade the schema blindly. |

Use `job recover --plan` and `resource recover --plan` to inspect exact job/run, resource revision, generation, observations, uncertainty and proposed effects. Apply with a still-current token and provenance-bearing evidence only after authorized operator review. An attestation stays labeled attested; unsupported or unknown execution remains unresolved. A mutation receipt records the exact outcome. Confirm identity and reusable state afterward. A successful reset changes resource readiness, not the failed/inconclusive workload verdict.

## Update a baseline

Resolve new supported release identities in an explicit maintenance action. Create a new immutable consumer profile and qualify it, including relevant skill changes and upgrade paths. Publish a new baseline revision and then select it as the default for future admissions. Do not edit an old profile or mutate the baseline of a queued request. Retain old baselines required by supported migration paths and incident reproduction.

## Update Haepari itself

Pause admission or resources and drain/reconcile active work. Take a quiesced, consistent SQLite backup including the required WAL state through a supported backup method. Verify the new release's storage compatibility and installation identity. Apply the update without running two controllers. Startup migrations run under the exclusive controller lock. Preserve the prior backup and executable for documented recovery; a schema downgrade is not automatically supported.

Haepari development uses a separate `HAEPARI_HOME`, socket, artifact area, and synthetic resources or a guest installation. Never test a candidate by replacing the stable controller managing its own VM campaign.

## Retire Aquarium's old development channel

Haepari never performs this as an installation side effect. The consumer rollout first proves Aquarium and each producer's new VM path, then detaches exact owned post-commit markers, stops/retires producer-owned development services, checks open workers/sessions, and settles preservation needs. Remove the shared launcher/MCP references/runtime only after all consumers no longer need them. The final removal of `~/.aquarium-dev/` has a separate explicit destructive-action boundary.

Preserve stable host tools, production state, Sorage control-plane data and Vault, unrelated Git hooks, native skill installations still needed on the host, and current user authentication. Do not delete every `use-*` skill or every path containing the word `dev`. Repository source cleanup and live host cleanup are different deliverables.

## Deferred external resource

The R2 external operator path is preserved in [the deferred contract](contracts/external-leases.md). It has no v0.1.0 command or release gate. B1/B2 must close before operators receive a supported runbook.


## Advisory locks

Register the `mulgae-review` lock resource (`kind: lock`, capacity one) before Mulgae's integration uses it; see the [lock resource example](examples/resource-lock.template.json).

- **Inspect.** `haepari locks list` shows request and grant IDs, the holder, waiters in FIFO order, and every deadline.
- **Abandoned grant.** A grant whose holder process died stops being renewed and ends after one liveness window. To end it sooner, use `haepari lock release <grant-id> --force --reason <text>`.
- **Stale queued request.** Remove it with `haepari lock cancel <request-id> --force --reason <text>`.
- **Limits of the lock.** Locks are advisory. A review started without the lock is not coordinated.

## Configuration and changes

The [configuration contract](contracts/configuration.md) owns the paths, precedence, `haepari.config/v1` fields, defaults and change rules, and gives the development-loop profile for 10–30 VM jobs a day.

**Changing global configuration.**

1. Stop the controller.
2. Edit the private configuration file.
3. Start the controller again. It validates the file and records an audit receipt before dispatch.

Running local jobs survive the restart and are reattached.

**Changing resources.** Use `resource update`, `pause`, `resume` and `remove` with expected revisions.

**Queue deadlines on a capacity-one VM resource.** Set `queue_seconds` to at least (jobs ahead + 1) × the maximum job duration. `haepari doctor` warns when a default queue deadline is too short.

Queue deadlines use persisted UTC instants; after sleep/wake/restart, queue expiry is evaluated before reservation. Running/manual deadlines request cancellation and reconciliation, never automatic release. Cleanup deadline leaves quarantine if restoration is unproven. A CLI wait timeout does not change a Job. See the [transition table](contracts/managed-transitions.md).
