# Haepari

Haepari is an independent Go CLI and local controller for managed command jobs. The TASK-001 bootstrap can report its identity and start an isolated foreground controller. TASK-002 adds provisional internal managed schemas and a durable state ledger. The CLI cannot yet submit or run a Job. The [development package](docs/README.md) owns the planned contracts and roadmap.

## Build and check

Use Go 1.27.1 on macOS arm64:

```sh
make build
make check
./bin/haepari version --json
```

`make test-macos-supervision` and `make test-utm` are explicit real-environment gates. They fail until their owning tasks provide observed evidence; `make check` does not run them. No host service is installed by these targets.

## Isolated foreground controller

Choose a short, private state root; macOS Unix socket paths have a 104-byte `sun_path` limit:

```sh
HAEPARI_HOME=/tmp/hp-example ./bin/haepari daemon run --backend local
```

The controller creates a private SQLite state file and Unix socket, then waits for a termination signal. It currently accepts no job protocol. The local backend executes commands on the host once later tasks implement submission; it is not a sandbox. Do not use a production state root for development tests. `HAEPARI_HOME` must be an absolute path on a local filesystem with an existing parent directory; the program creates only the final state-root directory. It refuses a non-private root, untrusted or shared writable ancestor, or symlinked ancestor. macOS system links `/tmp` and `/var` are resolved to their canonical paths. After an abnormal exit, confirm that no controller is using the state root before removing a stale `control.sock` and restarting.

Haepari has no runtime dependency on Aquarium or its tools. UTM jobs, service installation, advisory locks, and public external leases are not available in this bootstrap. The [roadmap](docs/roadmap.md) names the owning tasks. Haepari is licensed under [MIT](LICENSE).
