# Haepari

Haepari is an independent Go CLI and local controller for managed command jobs and advisory named locks. TASK-033 connects the provisional managed ledger and selected-file store to a foreground local Job path; TASK-009 moves workload supervision into an independent per-run process. The [development package](docs/README.md) owns the planned contracts and roadmap.

## Build and check

Use Go 1.27.1 on macOS arm64:

```sh
make build
make test
./bin/haepari version --json
```

`make test` is the serial prepare, unit, integration, and isolated local CLI E2E gate; `make check` is its alias. E2E automatically syncs the pinned pytest harness and may contact the Python package index on first use. Prepare formats Go source, so review any unrelated dirty Go files before running it. [TESTING.md](TESTING.md) documents every stage, the private E2E state root, and failure behavior.

`make test-macos-supervision` is the separate macOS arm64 host probe for the bounded TASK-004 decisions recorded in [its evidence](docs/records/TASK-004-macos-supervision.md). `make test-utm` remains the later real-VM gate and fails until TASK-016. The common gate does not run either host/VM gate or install a host service.

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

Persist the request key before submission. Reusing it with the same sealed input recovers the existing Job; any changed selected file still available conflicts, including when another selected file has been removed. `haepari run --spec local-job.json --request-key saved-key` submits and blocks through the same client path. `--json` emits admission and final NDJSON objects. The current local controller accepts multiple local resources with configured capacities and only auto Jobs with `development` evidence class. Its global active limit remains one, so raising resource capacity alone does not enable parallel execution. `resource inspect <id> --json` exposes availability/configuration revisions and the lease epoch; fenced `resource pause`/`resume` controls future dispatch. It retains and quarantines uncertain work and its slot for reconciliation after interruption. With the current global limit, an interrupted occupying Job blocks dispatch on all resources. Startup reconciles before dispatch, reattaches a proven supervisor and consumes durable stopped phases without another workload start. Unknown execution retains its slot; use the recovery plans below and preserve owned evidence. See the [current local recovery limit](docs/operations.md#current-local-controller-recovery-limit). Missing required output is preserved in the owned workspace and quarantines the resource until recovery. Native cancellation admission, bounded local cancellation, paged lists and event/log observers are available. Explicit local reconciliation and development-service planning are implemented in TASK-011's current candidate. Manual Jobs and UTM execution remain later work. The foreground restart checks do not qualify installed-service survival or host power-loss durability.

Each local workload now has an independent supervisor in its own session. Its private request, spawn/start/outcome/exit receipts bind the exact run and native boot/birth identities. Explicit argv and declared environment reach the workload without implicit shell expansion. Output streams to the owned file; bounded TERM/KILL handles the group and discovered descendants. Native discovery remains best effort and the local resource is not a sandbox. A missing termination receipt retains occupancy and quarantine. For read-only diagnosis, `bin/haepari runner inspect --request <absolute-owned-job-path>/request.json --json` reads those receipts; it does not recover or release the Job. See the [runner contract](docs/contracts/resource-and-runner.md#runner-receipts).

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

Admission receipts include a separate immutable `admission` identity: request key, canonical submitted digest, sealed input/object identity, global sequence, original resource configuration revision, frozen environment profile identity and UTC admission time. Current resource fences remain separate. Same-key replay compares surviving selected source files without recreating sealed payloads. An explicitly retained tombstone returns the original admission with `result_status: unavailable`, without a verdict or current capacity/lease claims; `run` emits that admission and exits 2, and `wait` reports `RESULT_UNAVAILABLE` with exit 2. Neither creates another Job. Unexpected missing accepted rows or objects remain storage uncertainty, not ordinary retention. Production bulk retention and purge are owned by TASK-012 and are not exposed by this slice.

## Recovery and isolated development service

Inspect a reconciling Job with `haepari job recover <id> --plan --json`. The read-only plan includes exact ownership, revisions, durable adapter intents, observations, uncertainty and protected evidence. A supported plan carries a controller-bound token valid for two minutes. Apply it with `--plan-token <token> --request-key <saved-key> --json`; changed revisions, lease or native observations invalidate an unapplied plan. Retry the identical saved request after an uncertain reply, even after controller restart: its permanent receipt is observed without repeating effects.

For an otherwise unproven local stop, plan with `--action attest-stopped` or `attest-no-start` and apply with `--evidence-file <json>`. The scoped evidence contains source `operator-attested`, observer, nonzero UTC observed time (`Z` or `+00:00`), mechanism, digest and reference. It stays labeled attested and cannot yield PASS. A positively observed live supervisor or incomplete filesystem owner blocks attestation; first establish its termination. Do not assert a stop that you have not established. An occupied resource rejects `resource recover` with `RECOVERY_BLOCKED`; recover the Job first. Idle local resource recovery verifies readiness and workspace absence without modifying a terminal Job result. Protected evidence or unresolved adapter effects block reuse; bounded re-collection/disposal belongs to TASK-012 and VM observation to TASK-013/TASK-015.

`haepari service plan --instance <id> --binary <absolute-candidate> --root <private-development-root> --json` prepares an isolated Aqua LaunchAgent and pins the executable digest without installing it. Save the response's `data` object as a plan file. Installation is a separate authorized command: `haepari service install --plan-file <json> --json`. It refuses the default state root, existing foreign state and an existing plist. The exact instance owns its private root, `gui/<uid>` label and `AbandonProcessGroup=true` plist. `service uninstall --plan-file <json> --json` removes only that exact installed plist after stopping the service; it preserves state, runner receipts and artifacts. The explicit [installed-service check](TESTING.md) is required before claiming service survival.

## Advisory named locks

Register a `kind: lock` document such as [the lock example](docs/examples/resource-lock.template.json). Omitted capacity, revision, liveness and maximum hold default to 1, 1, 120 s and 21600 s; explicit zero is invalid. A lock has no adapter or execution mode.

```sh
bin/haepari resource register --file lock-resource.json --json
bin/haepari lock acquire mulgae-review --request-key saved-lock-key --holder review-run --wait --timeout 5m --json
bin/haepari locks list --lock mulgae-review --limit 50 --json
bin/haepari lock keepalive <grant-id>
bin/haepari lock release <grant-id> --json
```

Save the key and payload before acquisition. A lost reply replays the same request and reports its current state; a granted replay claims it by renewal. A caller wait timeout exits 5 and leaves the request queued. Grants need renewal within their original liveness window and never extend beyond their original maximum hold. `keepalive` renews every third of that window; interruption stops renewal without releasing the grant. Its eventual `LOCK_EXPIRED` error exits 2. An ended acquire/wait exits 4. Exact own release replay exits 0; a differently ended grant returns its immutable terminal receipt and exits 4.

Pause stops new grants and resume hands free slots to FIFO waiters. Existing grants remain renewable. Updates change future grants only; shrinking capacity does not revoke grants. Removal requires no queued or granted requests. Lock recovery returns `UNSUPPORTED`. `resource inspect` reports `advisory_grants` and `advisory_queued` separately from managed occupancy. Lock keys, terminal history and slot generations survive removal and re-registration. Follow `more`/`next_cursor` to inspect every bounded list page.

An integration may pass `HAEPARI_LOCK_GRANT` containing installation ID, lock ID, grant ID and generation to a child. `internal/client.CheckLockGrant` validates it read-only on the selected installation and intended lock; the environment alone is not proof. A borrower must not renew, wait-claim, release, cancel or force-end the parent's grant. Managed parent contexts also reject literal lock acquisition. The owner retains renewal and release duties when a child succeeds, fails or is cancelled.

Schema 7 persists lock deadlines, UTC highwater and discontinuities. Startup and observed macOS wake extend each still-live grant's liveness before expiry, capped by its original maximum hold. A backward clock jump cannot extend a window. Local injected restart/wake checks and native marker reading do not qualify hardware sleep or power-loss survival. Locks grant no execution permit, consume no managed or global capacity, and never observe holder work. Callers who skip the lock remain outside its guarantee. See the [lock contract](docs/contracts/locks.md).

Haepari has no runtime dependency on Aquarium or its tools. UTM jobs and public external leases remain unavailable. Development-service installation is a separate explicit action; its real qualification is separate from the common gate. The [roadmap](docs/roadmap.md) names the owning tasks. Haepari is licensed under [MIT](LICENSE).
