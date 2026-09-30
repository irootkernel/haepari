# Haepari

Haepari is an independent Go CLI and local controller for managed command jobs. TASK-033 connects the provisional managed ledger and selected-file store to a foreground local Job path. The [development package](docs/README.md) owns the planned contracts and roadmap.

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

Persist the request key before submission. Reusing it with the same sealed input recovers the existing Job; any changed selected file still available conflicts, including when another selected file has been removed. `haepari run --spec local-job.json --request-key saved-key` submits and blocks through the same client path. `--json` emits admission and final NDJSON objects. The current walking skeleton accepts one local resource of capacity one and only auto Jobs with `development` evidence class. It retains and quarantines uncertain work and its slot for reconciliation after interruption. Missing required output is preserved in the owned workspace and quarantines the resource until recovery. Automated reattachment, cancellation, manual Jobs, service supervision, and UTM execution remain with later tasks. Host power-loss durability and macOS service survival are not qualified by this slice.

Haepari has no runtime dependency on Aquarium or its tools. UTM jobs, service installation, advisory locks, and public external leases are not available in this slice. The [roadmap](docs/roadmap.md) names the owning tasks. Haepari is licensed under [MIT](LICENSE).
