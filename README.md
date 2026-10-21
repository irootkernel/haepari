# Haepari

Haepari is an independent Go CLI and local controller for managed command jobs and advisory named locks. `make release` assembles a versioned release package — a standalone darwin/arm64 binary, the paired `use-haepari` skill tree, exercised portable examples and the packaged tree verifier. See [Install a release](#install-a-release) for binary and controller setup and [the agent setup below](#optional-configure-an-ai-coding-agent) for the paired skill. Published archives are identified by their [GitHub Release](https://github.com/irootkernel/haepari/releases). The [development package](https://github.com/irootkernel/haepari/blob/main/docs/README.md) owns the planned contracts and roadmap.

## Supported development scope

The first release targets one macOS arm64 machine, one user and one already-running registered persistent UTM VM, with general automatic/manual tests. Haepari owns input transfer, execution, results and Job cleanup; product-specific skill and migration assertions are optional consumer recipes. Ordinary approved development work reuses its scope without per-Job approval files. Power/logout/network experiments and separate long campaigns are optional. See [the scope decision](https://github.com/irootkernel/haepari/blob/main/docs/architecture-decision-records/0020-local-development-scope.md) and [operator guidance](https://github.com/irootkernel/haepari/blob/main/docs/operations.md#ordinary-development-scope). The [runnable project wrapper](https://github.com/irootkernel/haepari/blob/main/examples/project-test/README.md) provides automatic/manual local and fake-guest integration with read-only VM prerequisites. Manual smoke readiness holds capacity until recorded observations and owned settlement.

## Install a release

Select a versioned archive and its matching checksum sidecar from
[GitHub Releases](https://github.com/irootkernel/haepari/releases). The archive
contains the standalone `haepari` CLI/controller, this `README.md`, the complete
`skill/use-haepari/` tree, portable `examples/`, `verify_tree.py`, `SHA256SUMS`,
and the MIT `LICENSE`. Installation is explicit; the archive does not install
itself or enroll a VM.

Replace the angle-bracket placeholders below with the selected version, paths
and IDs before running commands. Run the package commands from the extracted
`haepari-<version>-darwin-arm64/` directory except for the archive-sidecar check,
which runs from the directory holding the archive and sidecar. Source-build
instructions later in this README require a source checkout; source-document
links open the repository on GitHub.

### Verify the package

The sidecar `<archive>.tar.gz.sha256` records the archive's bare file name:
keep the archive and sidecar together and run
`shasum -a 256 -c` from the directory holding both — checksum entries
resolve against the current working directory, not the sidecar's own path.
It binds the whole archive (including `SHA256SUMS`). `SHA256SUMS` inside
the archive lists every file **except itself**.

```sh
shasum -a 256 -c haepari-<version>-darwin-arm64.tar.gz.sha256   # archive digest
tar -xzf haepari-<version>-darwin-arm64.tar.gz
cd haepari-<version>-darwin-arm64 && python3 verify_tree.py
```

`verify_tree.py` (packaged, standard library only, independent of the haepari
binary) compares the complete unpacked tree against the inventory and reports
each **missing**, **added** or **tampered** file by name. It never follows
symlinks: an unlisted symlink or special file is reported as added, a recorded
regular file replaced by a symlink, special file or directory is reported as
tampered even when the target bytes are identical, and `SHA256SUMS` itself must
be a regular file. Ordinary structural directories that the inventory does not
list are fine. Plain `shasum -a 256 -c SHA256SUMS` only covers recorded files
and cannot detect extra files; use the packaged helper for complete-tree
verification. A failed check leaves all files in place for inspection.

### Run the binary standalone

The binary needs no source checkout or plugin cache:

```sh
env -i PATH=/usr/bin:/bin HOME="$PWD/home" ./haepari version --json
env -i PATH=/usr/bin:/bin HOME="$PWD/home" ./haepari capabilities --json
env -i PATH=/usr/bin:/bin HOME="$PWD/home" ./haepari help --json
```

`version` reports the stamped release version, platform, VCS identity, and the
executable SHA-256. `capabilities` reports the frozen protocol capability set
(`managed-execution/v1`, `durable-observation/v1`, `advisory-lock/v1`,
`managed-recovery/v1`, `artifact-delivery/v1`, `evidence-retention/v1`), the
compiled bootstrap backend, and `qualified_backends` separately. Compiled
support is not live readiness: `qualified_backends` stays empty until real
backend qualification is recorded as a separate, digest-bound fact. Consumers
check these declared schemas/backends/capabilities instead of guessing from
the version string.

### Start a controller

Foreground (private root; keep socket path short):

```sh
mkdir -p "$PWD/home/hp" && chmod 700 "$PWD/home/hp"
export HAEPARI_HOME="$PWD/home/hp"
./haepari daemon run --backend local
```

Set the same absolute `HAEPARI_HOME` in the client terminal before running
the examples below. For a service installation, use its planned state root.

Or install the explicit user release service (user LaunchAgent, Aqua session,
no root): plan first, review the pinned digest/root/label, then install as a
separate action. `service install` refuses an existing plist; `uninstall`
refuses while any resource is occupied and removes only its exact plist,
preserving state and evidence.

```sh
./haepari service plan --release --instance <unique-id> \
  --binary "$PWD/haepari" --root <absolute-private-root> --json > plan.envelope.json
# Save the plan's data object; install/uninstall consume it, not the envelope.
python3 -c 'import json,sys; json.dump(json.load(open("plan.envelope.json"))["data"], open("plan.json","w"), indent=2)'
# review plan.json (label io.rootkernel.haepari.<unique-id>, digest, plist)
./haepari service install --plan-file plan.json --json
# Select this service for subsequent resource/job/lock client commands.
export HAEPARI_HOME=<same-absolute-private-root>
# Run the examples, wait for all work to settle, then remove the service:
./haepari service uninstall --plan-file plan.json --json
```

### Localize and run the packaged examples

The resource examples are ready as-is. The job examples carry two clearly
marked placeholders that the validators reject until localized:
`<BUNDLE_ROOT>` and `<TREE_SHA256>`.

1. Create the selected files, e.g. `bundle/source/input.txt` (and
   `bundle/source/scenarios.md` for the manual example).
2. Compute the public `haepari-tree-sha256/v1` digest over the sorted
   selected files (this is the documented algorithm, shown as a Python
   snippet — run it from `bundle/`):

   ```python
   import hashlib, struct, pathlib
   digest = hashlib.sha256(b"haepari-tree-sha256/v1\0")
   for name in ["source/input.txt"]:  # sorted UTF-8; list every selected file
       data = pathlib.Path(name).read_bytes()
       encoded = name.encode()
       executable = bool(pathlib.Path(name).stat().st_mode & 0o111)
       digest.update(struct.pack(">Q", len(encoded)) + encoded + bytes([executable]))
       digest.update(struct.pack(">Q", len(data)) + hashlib.sha256(data).digest())
   print(digest.hexdigest())
   ```

3. Replace `<BUNDLE_ROOT>` with the absolute bundle path and `<TREE_SHA256>`
   with the printed digest. Then register the resource and run:

   ```sh
   ./haepari resource register --file examples/resource-local.json --json
   ./haepari submit --spec examples/job-local-auto.json --request-key install-check-1 --json
   ./haepari wait <job-id> --timeout 60s --json   # exit 0 PASS / 1 FAIL / 3 INCOMPLETE / 4 CANCELLED
   ```

   For an uncertain transport reply, resend the **same** key and spec; never
   a new key. Read `error.code` and `error.retry` first: exit 6 also covers
   storage failures. The paired skill (`skill/use-haepari/SKILL.md`) documents the full
   submit-once/wait/reconnect/cancel/recovery behavior.

### Advisory lock example

```sh
./haepari resource register --file examples/resource-lock.json --json
./haepari lock acquire pkg-review --request-key review-1 --holder my-tool --wait --json
./haepari lock keepalive <grant-id> --json &   # while the protected work runs
./haepari lock release <grant-id> --json       # on every completion path
```

A nested invocation receiving `HAEPARI_LOCK_GRANT` validates the inherited
grant read-only and uses it; it never renews, releases, or cancels the
parent's grant. Locks grant no execution permit and count toward no managed
capacity.

### Operating boundaries

- Unpublished candidate: a local build/test is not distribution, host
  installation, or publication authority.
- No auto-install, no UTM auto-enrollment, no required MCP transport, no
  external lease commands in v0.1.0.
- Never delete `~/.aquarium-dev`, foreign Git hooks, Sorage data, or host
  skills; Haepari owns only its own state root and job workspaces.
- `qualified_backends` emptiness is truthful until real backend
  qualification is recorded separately.

## Build and check (source checkout)

Use Go 1.27.1 on macOS arm64:

```sh
make build
make test
./bin/haepari version --json
```

`make test` is the serial prepare, unit, integration, and isolated local CLI E2E gate; `make check` is its alias. E2E automatically syncs the pinned pytest harness and may contact the Python package index on first use. Prepare formats Go source, so review any unrelated dirty Go files before running it. [TESTING.md](https://github.com/irootkernel/haepari/blob/main/TESTING.md) documents every stage, the private E2E state root, and failure behavior.

`make test-macos-supervision` is the separate macOS arm64 host probe for the bounded TASK-004 decisions recorded in [its evidence](https://github.com/irootkernel/haepari/blob/main/docs/records/TASK-004-macos-supervision.md). `make test-utm` is the optional full single-VM campaign, requiring its reviewed manifest/authority inputs. Use the project-owned `make test-vm` target in [examples/project-test](https://github.com/irootkernel/haepari/blob/main/examples/project-test/README.md) with an explicit CLI and resource. The common gate does not run either host/VM gate or install a host service.

`make release` assembles the unpublished release-candidate archive into a fresh `dist/` directory (see [Install a release](#install-a-release) and the [TASK-025 record](https://github.com/irootkernel/haepari/blob/main/docs/records/TASK-025-release-packaging.md)); a local build or package is a candidate, not official distribution.

## Optional: configure an AI coding agent

For the reusable agent workflow, install the complete
[`use-haepari` skill directory](skill/use-haepari/) under
`~/.agents/skills/use-haepari/`, separately from the Haepari binary.
The release archive also includes this directory as `skill/use-haepari/`.
The current skill is one self-contained `SKILL.md`; install any additional
files with it if a later version expands the tree.

The default `main` reference selects development guidance. Select the matching
release tag for guidance paired with an installed release. For an exact
unpublished candidate, use the skill tree from its verified package instead of
`main`.

The following procedure downloads the skill before replacing an existing
installation and restores the previous directory if the replacement fails:

```bash
(
set -eu

agent_skills_dir="$HOME/.agents/skills"
haepari_skill_dir="$agent_skills_dir/use-haepari"
haepari_ref=main

mkdir -p "$agent_skills_dir"
haepari_stage_dir="$(mktemp -d "$agent_skills_dir/.use-haepari.install.XXXXXX")"
haepari_staged_skill="$haepari_stage_dir/use-haepari"
haepari_previous_skill="$haepari_stage_dir/previous"
haepari_installed=false

cleanup_haepari_skill_install() {
  haepari_install_status=$?
  if [ "$haepari_installed" != true ] && \
    [ -e "$haepari_previous_skill" ] && [ ! -e "$haepari_skill_dir" ]; then
    mv "$haepari_previous_skill" "$haepari_skill_dir" || true
  fi
  if [ "$haepari_installed" = true ] || [ ! -e "$haepari_previous_skill" ]; then
    rm -rf "$haepari_stage_dir"
  else
    echo "Haepari skill backup preserved at $haepari_previous_skill" >&2
  fi
  return "$haepari_install_status"
}
trap cleanup_haepari_skill_install EXIT

mkdir -p "$haepari_staged_skill"
curl -fsSLo "$haepari_staged_skill/SKILL.md" \
  "https://raw.githubusercontent.com/irootkernel/haepari/$haepari_ref/skill/use-haepari/SKILL.md"
# For a verified extracted package, replace the curl command above with:
# cp -R "/absolute/path/to/haepari-<version>-darwin-arm64/skill/use-haepari/." "$haepari_staged_skill/"

if [ -e "$haepari_skill_dir" ]; then
  mv "$haepari_skill_dir" "$haepari_previous_skill"
fi
mv "$haepari_staged_skill" "$haepari_skill_dir"
haepari_installed=true
rm -rf "$haepari_stage_dir"
trap - EXIT
)
```

Repeat the procedure to update the complete skill directory. Skill installation
does not install the binary, start a controller, register a VM, or authorize
workloads. The skill covers Job submission, blocking waits, reconnection,
cancellation, recovery and advisory-lock ownership; a controller and resources
still need their own setup.

## Isolated foreground controller

Choose a short, private state root; macOS Unix socket paths have a 104-byte `sun_path` limit:

```sh
HAEPARI_HOME=/tmp/hp-example ./bin/haepari daemon run --backend local
```

The controller creates a private SQLite state file, `objects/` input store, `jobs/` root, and Unix socket. It serves HTTP over that socket with a protocol handshake. The local backend executes commands on the host; it is not a sandbox. Use a disposable state root and harmless input for development. `HAEPARI_HOME` must be an absolute path on a local filesystem with an existing parent directory; the program creates only the final state-root directory. It refuses a non-private root, untrusted or shared writable ancestor, or symlinked ancestor. macOS system links `/tmp` and `/var` are resolved to their canonical paths. The controller holds a private `controller.lock` kernel lock until exit. Before publication, it syncs the bound socket identity into the locked file. Restart recovers a refused stale `control.sock` only under the lock and with that identity; reachable sockets, unknown legacy sockets and foreign paths are preserved. For an unknown socket, confirm that the prior controller is stopped and no controller holds the kernel lock, remove only `control.sock` from that state root, then restart the controller. Never delete the lock file or replace an active controller's socket.

In another shell, register one local resource with capacity one, then submit a validated `haepari.job/v1` auto Job. The Job's `input.selected_files` explicitly lists regular files under `input.root`; `input.inventory_digest` is the lowercase 64-character tree digest from the selected-file manifest. These commands do not install or start a service:

```sh
HAEPARI_HOME=/tmp/hp-example ./bin/haepari resource register --file local-resource.json --json
HAEPARI_HOME=/tmp/hp-example ./bin/haepari submit --spec local-job.json --request-key saved-key --json
HAEPARI_HOME=/tmp/hp-example ./bin/haepari wait job-id --timeout 5m --json
HAEPARI_HOME=/tmp/hp-example ./bin/haepari inspect job-id --json
```

Persist the request key before submission. Reusing it with the same sealed input recovers the existing Job; any changed selected file still available conflicts, including when another selected file has been removed. `haepari run --spec local-job.json --request-key saved-key` submits and blocks through the same client path. `--json` emits admission and final NDJSON objects. The current local controller accepts multiple local resources with configured capacities and auto/manual Jobs with `development` evidence class. Its global active limit defaults to one; select `--global-limit 2` for mixed local/VM development so independent local work can continue when a VM holds its slot. `resource inspect <id> --json` exposes availability/configuration revisions and the lease epoch; fenced `resource pause`/`resume` controls future dispatch. It retains and quarantines uncertain work and its slot for reconciliation after interruption. With the current global limit, an interrupted occupying Job blocks dispatch on all resources. Startup reconciles before dispatch, reattaches a proven supervisor and consumes durable stopped phases without another workload start. Unknown execution retains its slot; use the recovery plans below and preserve owned evidence. See the [current local recovery limit](https://github.com/irootkernel/haepari/blob/main/docs/operations.md#current-local-controller-recovery-limit). For v1, missing required output retains the workspace and quarantines the resource. V2 permits reuse only after positive absence in the bound workspace and verified cleanup; existing uncollected evidence stays protected. See the [approved recovery policy](https://github.com/irootkernel/haepari/blob/main/docs/contracts/managed-transitions.md#epic-009-approved-recovery-delta-task-036). Native cancellation admission, bounded local cancellation, paged lists and event/log observers are available. Explicit local reconciliation and isolated development-service planning are implemented. General local/development UTM manual completion is implemented by TASK-019 alongside automatic Jobs; exact running-VM release observation remains TASK-027. The foreground restart checks do not qualify installed-service survival or host power-loss durability.

Each local workload now has an independent supervisor in its own session. Its private request, spawn/start/outcome/exit receipts bind the exact run and native boot/birth identities. Explicit argv and declared environment reach the workload without implicit shell expansion. Output streams through a bounded collector to the owned file; log gaps are recorded without interrupting workload output, and bounded TERM/KILL handles the group and discovered descendants. Native discovery remains best effort and the local resource is not a sandbox. A missing termination receipt retains occupancy and quarantine. For read-only diagnosis, `bin/haepari runner inspect --request <absolute-owned-job-path>/request.json --json` reads those receipts; it does not recover or release the Job. See the [runner contract](https://github.com/irootkernel/haepari/blob/main/docs/contracts/resource-and-runner.md#runner-receipts).

Native observation and cancellation:

```sh
bin/haepari jobs list --resource local-dev --limit 50 --json
bin/haepari resources list --json
bin/haepari events <job-id> --after <cursor> --follow --json
bin/haepari logs <job-id> --after <offset> --identity <log-identity> --follow --json
bin/haepari cancel <job-id> --expected-revision <job-revision> --request-key saved-cancel-key --json
```

Persist page cursors, event cursors and log identity/offsets from successful responses. Apply an event resynchronization snapshot before its checkpoint. Log JSON pages preserve arbitrary bytes as base64 and explicitly report a gap; plain `logs` writes one bounded byte page (or a blocking stream with `--follow`) and reports gaps and any remaining-page resume checkpoint on stderr. Follow streams end only with a versioned `end` record; an unexpected transport end is uncertain. The caller timeout on `wait` and `run` stops observation without cancelling the Job. Interruption and disconnect also stop observers without cancelling it. Cancellation returns a durable intent and current snapshot; observe `wait` for final settlement. A queued cancellation wins atomically against reservation. An active cancellation cannot release capacity without termination and cleanup proof, and a cancellation received after a known workload outcome preserves that outcome. Keep the explicit cancellation key and original expected revision for an uncertain-reply retry.

Every Unix request carries a supported protocol range. The handshake returns selected version, capabilities and the installation ID persisted in schema 6. Moving a bound state database preserves this ID; the first binding preserves the earlier root-derived identity. Unsupported or malformed ranges reject before mutation.

Admission receipts include a separate immutable `admission` identity: request key, canonical submitted digest, sealed input/object identity, global sequence, original resource configuration revision, frozen environment profile identity and UTC admission time. Current resource fences remain separate. Same-key replay compares surviving selected source files without recreating sealed payloads. An explicitly retained tombstone returns the original admission with `result_status: unavailable`, without a verdict or current capacity/lease claims; `run` emits that admission and exits 2, and `wait` reports `RESULT_UNAVAILABLE` with exit 2. Neither creates another Job. Unexpected missing accepted rows or objects remain storage uncertainty, not ordinary retention. Finished bulk data can be purged explicitly under its saved Job revision; permanent request identities prevent re-execution. There is no automatic expiry or background garbage collection.

## Export and retention

Inspect machine-readable command support without connecting to a controller:

```sh
bin/haepari help --json
```

Export a finished Job to a new absolute directory. The export verifies each file's full size and SHA-256 and writes `manifest.json` last; an existing destination is refused. A failed export leaves its partial directory for inspection.

```sh
bin/haepari export <job-id> --to /absolute/new-directory --json
bin/haepari purge <job-id> --expected-revision <job-revision> --json
```

Save the purge revision for an uncertain-reply retry. Purge makes results unavailable before removing owned artifacts, logs, supplementary files and unshared sealed input bytes. It refuses active Jobs, unresolved evidence, unknown paths and replaced file identities. Permanent admissions and cancellation keys survive; repeating submission cannot rerun the purged Job. Required evidence that could not be collected remains protected until bounded re-collection or explicit disposal. See [artifact delivery and retention](https://github.com/irootkernel/haepari/blob/main/docs/contracts/artifact-and-profile.md) for limits and recovery behavior.

The operator alone starts and stops the VM. Haepari observes `utmctl list/status`, waits for the selected VM to be started, and runs owned Jobs over SSH. It has no VM power-control command.

## Recovery and isolated development service

Inspect a reconciling Job with `haepari job recover <id> --plan --json`. The read-only plan includes exact ownership, revisions, durable adapter intents, observations, uncertainty and protected evidence. A supported plan carries a controller-bound token valid for two minutes. Apply it with `--plan-token <token> --request-key <saved-key> --json`; changed revisions, lease or native observations invalidate an unapplied plan. Retry the identical saved request after an uncertain reply, even after controller restart: its permanent receipt is observed without repeating effects.

For an otherwise unproven local stop, plan with `--action attest-stopped` or `attest-no-start` and apply with `--evidence-file <json>`. The scoped evidence contains source `operator-attested`, observer, nonzero UTC observed time (`Z` or `+00:00`), mechanism, digest and reference. It stays labeled attested and cannot yield PASS. A positively observed live supervisor or incomplete filesystem owner blocks attestation; first establish its termination. Do not assert a stop that you have not established. An occupied resource rejects `resource recover` with `RECOVERY_BLOCKED`; recover the Job first. Idle local resource recovery verifies readiness and workspace absence without modifying a terminal Job result. Protected evidence or unresolved adapter effects block reuse. For an idle quarantined resource, use `resource recover <id> --plan --action recollect --json` to collect required outputs into a separate supplementary inventory. A disposal plan uses `--action dispose` and lists the evidence loss; applying its token authorizes that loss. These operations preserve the original terminal result. For resource v2, new recollect/dispose plans sign the registered finite collection and cleanup ceilings; saved recovery deadlines and resource-v1 behavior remain unchanged. After evidence is resolved, a separate resource recovery plan verifies health before reuse. Automatic VM execution and revision-bound recovery are implemented (TASK-015); the exact-release running-VM loop remains TASK-027.

`haepari service plan --instance <id> --binary <absolute-candidate> --root <private-development-root> --json` prepares an isolated Aqua LaunchAgent and pins the executable digest without installing it. Save the response's `data` object as a plan file. Installation is a separate authorized command: `haepari service install --plan-file <json> --json`. It refuses the default state root, existing foreign state and an existing plist. The exact instance owns its private root, `gui/<uid>` label and `AbandonProcessGroup=true` plist. `service uninstall --plan-file <json> --json` removes only that exact installed plist after stopping the service; it preserves state, runner receipts and artifacts. The explicit [installed-service check](https://github.com/irootkernel/haepari/blob/main/TESTING.md) is required before claiming service survival.

## Advisory named locks

Register a `kind: lock` document such as [the lock example](https://github.com/irootkernel/haepari/blob/main/docs/examples/resource-lock.template.json). Omitted capacity, revision, liveness and maximum hold default to 1, 1, 120 s and 21600 s; explicit zero is invalid. A lock has no adapter or execution mode.

```sh
bin/haepari resource register --file lock-resource.json --json
bin/haepari lock acquire mulgae-review --request-key saved-lock-key --holder review-run --wait --timeout 5m --json
bin/haepari locks list --lock mulgae-review --limit 50 --json
bin/haepari lock keepalive <grant-id>
bin/haepari lock release <grant-id> --json
```

Save the key and payload before acquisition. A lost reply replays the same request and reports its current state; a granted replay claims it by renewal. A caller wait timeout exits 5 and leaves the request queued. Grants need renewal within their original liveness window and never extend beyond their original maximum hold. `keepalive` renews every third of that window; interruption stops renewal without releasing the grant. Its eventual `LOCK_EXPIRED` error exits 2. An ended acquire/wait exits 4. Exact own release replay exits 0; a differently ended grant returns its immutable terminal receipt and exits 4.

Pause stops new grants and resume hands free slots to FIFO waiters. Existing grants remain renewable. Updates change future grants only; shrinking capacity does not revoke grants. Removal requires no queued or granted requests. Lock recovery returns `UNSUPPORTED`. `resource inspect` reports `advisory_grants` and `advisory_queued` separately from managed occupancy. Lock keys, terminal history and slot generations survive removal and re-registration. Follow `more`/`next_cursor` to inspect every bounded list page.

An integration may pass `HAEPARI_LOCK_GRANT` containing installation ID, lock ID, grant ID and generation to a child. `internal/client.CheckLockGrant` validates it read-only on the selected installation and intended lock; the environment alone is not proof. A borrower without the Go client performs the same read-only check through `haepari locks list --lock <intended-lock> --json` pages, matching the installation, lock and grant identities with the page's server `now` before use ([lock contract](https://github.com/irootkernel/haepari/blob/main/docs/contracts/locks.md)). A borrower must not renew, wait-claim, release, cancel or force-end the parent's grant. Managed parent contexts also reject literal lock acquisition. The owner retains renewal and release duties when a child succeeds, fails or is cancelled.

Schema 7 persists lock deadlines, UTC highwater and discontinuities. Startup and observed macOS wake extend each still-live grant's liveness before expiry, capped by its original maximum hold. A backward clock jump cannot extend a window. Local injected restart/wake checks and native marker reading do not qualify hardware sleep or power-loss survival. Locks grant no execution permit, consume no managed or global capacity, and never observe holder work. Callers who skip the lock remain outside its guarantee. See the [lock contract](https://github.com/irootkernel/haepari/blob/main/docs/contracts/locks.md).

Haepari has no runtime dependency on Aquarium or its tools. Automatic development UTM Jobs are implemented; the [F2 decision](https://github.com/irootkernel/haepari/blob/main/docs/records/TASK-016-vm-contract-freeze.md) records the qualified persistent-VM combination and remaining verification limits. Public external leases remain unavailable. Development-service installation is a separate explicit action; its real qualification is separate from the common gate. The [roadmap](https://github.com/irootkernel/haepari/blob/main/docs/roadmap.md) names the owning tasks. Haepari is licensed under [MIT](LICENSE).

## UTM host registration

TASK-013 provides the host portion of the persistent VM adapter. `resource register --file <resource.json> --json` checks UTM UUID membership and a digest-bound SSH host-key pin before registration. UUID and MAC uniqueness are durable constraints; VM names are never selectors. The [resource example](https://github.com/irootkernel/haepari/blob/main/docs/examples/resource-macos.template.json) remains illustrative. Supply an operator-provisioned dedicated known-hosts file containing one `ssh-ed25519` key for `haepari-vm-<lowercase-VM-UUID>`, and set `guest.known_hosts_reference` to `sha256:<lowercase-file-digest>:<absolute-file-path>`. No credentials are copied and registration performs no power operation.

An idle VM configuration update uses `resource update --file <revision-plus-one.json> --expected-revision <configuration-revision> --expected-availability-revision <availability-revision> --json`. A stale, occupied or power-protected resource refuses the update; maintenance pause is preserved. `vm.allow_automatic_stop` defaults to false. The legacy automatic-stop field is inert. Current reservations create no power intent and issue no VM start or stop. Historical claims still require exact durable return/cessation proof and fresh identity-bound observation; failed or unrecorded returns stay protected without reissue. DHCP uses an individual unexpired MAC lease and current authenticated guest identity rather than the lease file modification time. Automatic development VM Jobs, explicit readiness-verified resume and revision-bound Job/resource recovery are implemented by TASK-015. VM resource removal remains unavailable. TASK-016 has qualified the bounded real backend and frozen F2; its [decision record](https://github.com/irootkernel/haepari/blob/main/docs/records/TASK-016-vm-contract-freeze.md) defines the supported combination. See the [host-control boundary](https://github.com/irootkernel/haepari/blob/main/docs/records/TASK-013-utm-host-control.md).


TASK-014 provides the authenticated guest transport and transient runner used by the next integration step. It transfers the digest-pinned runner with the sealed input, verifies the registered host key, current MAC lease and boot identity, and reconnects to the original run receipts and output. A disconnected SSH observer does not stop or restart the workload. TASK-015 connects public automatic development VM Jobs to bounded collection, owned Job cleanup, quarantine and recovery. Provision guest authentication separately; see the [SSH mapping and protocol](https://github.com/irootkernel/haepari/blob/main/docs/contracts/resource-and-runner.md#ssh-and-guest-supervision). The accepted [TASK-016 qualification](https://github.com/irootkernel/haepari/blob/main/docs/records/TASK-016-vm-contract-freeze.md) covers one already-running registered macOS VM. Actual DHCP expiry, logout/post-login recovery, multi-day uptime and two-VM operation remain [optional hardening](https://github.com/irootkernel/haepari/blob/main/docs/deferred-feedback.md), outside v0.1.0 release obligations.

The foreground daemon accepts `--host-macos-guest-limit 1|2` (default 1). It counts observed running VMs and protected power claims before reservation. At capacity, only an explicitly eligible registered idle VM may be stopped; otherwise the Job stays queued under its original deadline. Set 2 only for an independently qualified operator configuration.

VM results are immutable after settlement. Failed required collection retains the exact guest Job directory and quarantines the VM; unknown workload or collector termination also retains occupancy. Use the original Job's revision-bound recovery plan before idle resource recollection or explicit disposal, then a separate health plan before reuse. See [TASK-015's boundary](https://github.com/irootkernel/haepari/blob/main/docs/records/TASK-015-vm-job-recovery.md). Neither implemented dispatch nor fake/native fixtures qualify a real UTM installation.
