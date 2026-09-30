# Testing

## Contract

Contract: aquarium-test-contract/v1
Profile: make
Enrollment: configured for the current local Go product slice. The executable Makefile is authoritative; a passing common gate does not qualify unimplemented UTM, installed-service, or release behavior.

## Canonical Commands

| Command | Handler |
| --- | --- |
| `make test` | Serial, fail-fast prepare → unit → integration → local E2E. |
| `make test-prepare` | Formatting and deterministic static/build checks. |
| `make test-unit` | Go unit specs. |
| `make test-int` | Self-contained Go integration specs. |
| `make test-e2e` | Built local CLI and foreground-controller black-box scenario. |

The E2E handler uses `uv run --locked --group dev` to sync the pinned pytest environment before the scenario; its first run may contact the Python package index. Missing dependencies fail the gate if they cannot be installed. Go build and Ginkgo CLI compilation may fetch pinned public Go modules when the module cache is empty; `go mod download` is the explicit offline preparation step. `make check` delegates to `make test`. `make test-integration` delegates to `make test-int`. `make test-macos-supervision` remains an explicit logged-in macOS host probe; `make test-utm` fails with the TASK-016 prerequisite gap and is outside the local aggregate.

## Stage Mapping

- Prepare runs `gofmt -w` on Go files under `cmd/` and `internal/`, the existing `lint` target (`gofmt` verification and `go vet ./...`), integration-tagged and supervision-tagged vet, `go build ./...`, an offline `uv.lock` consistency check, Python syntax compilation, and the existing documentation validator. It can rewrite Go source and create build/cache artifacts. It starts no controller, database, container, VM, or provider.
- Unit runs pinned Ginkgo v2 through `go run` with `--race`, `--fail-on-empty`, `--fail-on-pending`, and the `unit` label. Specs exercise isolated CLI decoding, contract logic, client transport fake, snapshot/state logic, and platform validation without a separately managed resource.
- Integration uses the `integration` build tag and label with the same race and empty/pending failure checks. It exercises private embedded SQLite, Unix-socket client/controller cooperation, owned local subprocesses, and bounded local fixtures. It requires no externally managed database, container, network service, or live provider. Unit specs are excluded by the label filter.
- E2E builds `bin/haepari` and runs pytest against that executable through documented CLI commands only. It registers one local resource, exercises fenced pause/resume with exact replay and stale-revision rejection, submits and awaits a harmless selected-file Job, checks the result and artifact, reconnects with the same key, and rejects a changed-payload replay without another execution.

## Test Frameworks

| Language and layer | Framework and evidence | Runner |
| --- | --- | --- |
| Go unit and integration | Ginkgo v2.32.1 and Gomega v1.41.0, pinned in `go.mod` and `go.sum`; specs import Ginkgo, representative assertions use Gomega, and suite bootstraps register its failure handler. Preserved assertion branches use GinkgoT within Ginkgo specs. | `go run github.com/onsi/ginkgo/v2/ginkgo` with stage-specific flags and package lists. |
| Python E2E | pytest 9.1.1, declared in `pyproject.toml` and resolved in `uv.lock`; `tests/e2e/` contains public CLI assertions. | `uv run --locked --group dev python -m pytest -q tests/e2e`. |

The Python documentation validator is a repository check, not a Python product test layer. No legacy framework waiver is in force.

## Gaori Mapping

`.gaori/tester.yaml` maps `docs-validation`, `test`, `test-prepare`, `test-unit`, `test-int`, and `test-e2e` to `generic`. Make and `go run` can emit mixed build, vet, runner, and failure output, so a specialized parser is not claimed. The wrapped command's exit code remains authoritative. Gaori is optional and does not install dependencies or create test authority.

## E2E Environment

The artifact is the locally built `bin/haepari`; public interfaces exercised are `daemon run`, `resource register`, `resource pause`, `resource resume`, `submit`, `wait`, and `inspect`. Before starting the controller, the harness requires macOS arm64, a regular executable, and a newly created owner-private short state parent under canonical `/tmp`. It sets `HAEPARI_HOME`, `HOME`, `TMPDIR`, and a minimal `PATH` for the child processes. No production state root, existing daemon, TCP port, VM, account, provider, or external endpoint is selected. Missing prerequisites fail the test rather than skip it.

The harness starts one foreground controller and waits for a successful public protocol response before registering a test-only local resource. It seeds one non-secret selected file, computes the documented tree digest independently, captures bounded CLI results and a local daemon log, then rechecks every admitted Job for verified cleanup, terminates the exact process it started, and removes only its own scratch tree after its identity and shutdown are proven. If cleanup is uncertain, it fails and retains the tree for inspection. The only database is SQLite under that private scratch tree; the only socket is its Unix control socket. The product scenario uses no credential variables, external sandbox, container, volume, paid request, or network service. Dependency preparation may contact the public Go module proxy or Python package index; it is separate from the isolated product scenario.

## Language Diagnostics

Go unit and integration use the race detector without test-result caching; prepare runs `go vet` for default, integration, and macOS supervision build modes and builds the product. Gofmt applies deterministic meaning-preserving formatting first. Python E2E is syntax-compiled in prepare and uses pytest strict configuration and marker checks. There is no established Python formatter, linter, or type checker for the documentation utility or the new test-only harness. Go's race detector is the applicable concurrency diagnostic; no unsafe-code sanitizer or separate runtime diagnostic is configured for this Go/Python scope.

## Legacy Waivers

None.
