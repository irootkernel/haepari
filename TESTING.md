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
- Unit runs pinned Ginkgo v2 through `go run` with `--race`, `--fail-on-empty`, `--fail-on-pending`, and the `unit` label. Specs exercise isolated CLI decoding, contract logic, client transport fake, snapshot/state logic, runner receipt/input validation, and platform validation without a separately managed resource.
- Integration uses the `integration` build tag and label with the same race and empty/pending failure checks. It exercises private embedded SQLite, Unix-socket client/controller cooperation, owned local subprocesses, and bounded local fixtures. TASK-009 adds actual same-binary supervisor/trampoline fixtures for fast exec, literal arguments/environment, failed exec versus exit 126, output-heavy inherited writers, TERM/KILL escalation, discovered descendants changing session/reparenting, supervisor death around receipt boundaries, prior-boot rejection and exact ledger import fencing. Additional checks cover active/completed start replay without effects, a present unproven exit retaining controller occupancy/quarantine, injected census failure and termination-budget expiry, pre-exec crash effect absence, descriptor-less trampoline refusal, output-identity mismatch, cancellation contention/exhaustion and discriminating declared PATH wiring. A short-lived caller exits before workload completion; the orphaned runner completes and replay preserves effects. An expired gate produces a proven no-start outcome that the controller can import. Cancellation before gate release produces a proven no-start cancelled outcome without effects or replay. These qualify caller independence; controller restart/reattachment remains TASK-011's portion of V46. Census fault injection tests conservative failure handling; it does not qualify an actual kernel observation failure. Those observations do not qualify the later installed LaunchAgent or universal escaped-process detection. It requires no externally managed database, container, network service, or live provider. Unit specs are excluded by the label filter.
- TASK-010 integration adds native phase failure/deadline scenarios, stopped preparation with owned files and no applicable workload outputs, protected evidence before cleanup, timely missing-output and cleanup-deadline reasons, expired-phase worker refusal, bounded supervisor-observer uncertainty, final-proof loss, foreign-path retention, exact phase identity/budget/deadline fencing and reopen evidence, missing active event refusal, preparation-stop refusals for an existing run claim, invalid identity/reason/state and missing cancellation, queued cancellation/expiry timing shape, ordered entered-phase timestamps and empty adapter-step arrays. A stopped-worker deadline fault creates real partial input files, then exercises collection/verified cleanup, no run claim and one terminal return; the injected copy error does not qualify kernel-call timing. Existing finish-time and blocked-worker fixtures separately cover late versus uncertain observation. Negative receipt checks cover oversized deadlines, foreign protected paths, non-collection inventories and invalid phase states without writes. Negative settlement checks refuse an incomplete run, completed-unproven run/collect/cleanup/verify phases, contradictions with collection/cleanup/protected evidence facts and forged no-start reasons for an entered workload. Reserved no-effect replay coverage now asserts admission/reservation timestamps and empty phase/adapter-step arrays; controller no-effect cancellation asserts the exact prepare/verify phase order. The compiled CLI E2E checks the complete local phase timing shape. Existing before/after-commit crash probes now carry separate outcome/termination proof and committed collection/cleanup/verification observations, assert their exact timing projection and one terminal return on replay. VM no-effect pause/replay/uncertain-power guards remain ledger fixtures; these do not qualify real VM execution.
- E2E builds `bin/haepari` and runs pytest against that executable through documented CLI commands only. It registers one local resource, exercises fenced pause/resume with exact replay and stale-revision rejection, submits and awaits a harmless selected-file Job, checks the result and artifact, reconnects with the same key, and rejects a changed-payload replay without another execution. It also reads filtered resource/Job pages, drains event pages and a terminal follow stream, observes a known failing workload through successful inspection and exact log bytes, and cancels a paused queued Job with an exact-key replay and terminal wait.

## Test Frameworks

| Language and layer | Framework and evidence | Runner |
| --- | --- | --- |
| Go unit and integration | Ginkgo v2.32.1 and Gomega v1.41.0, pinned in `go.mod` and `go.sum`; specs import Ginkgo, representative assertions use Gomega, and suite bootstraps register its failure handler. Preserved assertion branches use GinkgoT within Ginkgo specs. | `go run github.com/onsi/ginkgo/v2/ginkgo` with stage-specific flags and package lists. |
| Python E2E | pytest 9.1.1, declared in `pyproject.toml` and resolved in `uv.lock`; `tests/e2e/` contains public CLI assertions. | `uv run --locked --group dev python -m pytest -q tests/e2e`. |

The Python documentation validator is a repository check, not a Python product test layer. No legacy framework waiver is in force.

## Gaori Mapping

`.gaori/tester.yaml` maps `docs-validation`, `test`, `test-prepare`, `test-unit`, `test-int`, and `test-e2e` to `generic`. Make and `go run` can emit mixed build, vet, runner, and failure output, so a specialized parser is not claimed. The wrapped command's exit code remains authoritative. Gaori is optional and does not install dependencies or create test authority.

## E2E Environment

The artifact is the locally built `bin/haepari`; public interfaces exercised are `daemon run`, `resource register`, `resource pause`, `resource resume`, `resources list`, `jobs list`, `submit`, `run`, `wait`, `inspect`, `events`, `logs`, `cancel`, advisory acquire/wait/renew/keepalive/release/cancel/list and lock resource remove/recover. Resource update is exercised through the typed Unix client/controller integration suite. Before starting the controller, the harness requires macOS arm64, a regular executable, and a newly created owner-private short state parent under canonical `/tmp`. It sets `HAEPARI_HOME`, `HOME`, `TMPDIR`, and a minimal `PATH` for the child processes. No production state root, existing daemon, TCP port, VM, account, provider, or external endpoint is selected. Missing prerequisites fail the test rather than skip it.

Each scenario starts one foreground controller and waits for a successful public protocol response before registering a test-only local resource. It seeds one non-secret selected file, computes the documented tree digest independently, captures bounded CLI results and a local daemon log, then rechecks every admitted Job for verified cleanup or a terminal queued cancellation with no lease or workload execution. It terminates the exact process it started and removes only its own scratch tree after its identity and shutdown are proven. If cleanup is uncertain, it fails and retains the tree for inspection. The only database is SQLite under that private scratch tree; the only socket is its Unix control socket. The product scenario uses no credential variables, external sandbox, container, volume, paid request, or network service. Dependency preparation may contact the public Go module proxy or Python package index; it is separate from the isolated product scenario.

## Language Diagnostics

Go unit and integration use the race detector without test-result caching; prepare runs `go vet` for default, integration, and macOS supervision build modes and builds the product. Gofmt applies deterministic meaning-preserving formatting first. Python E2E is syntax-compiled in prepare and uses pytest strict configuration and marker checks. There is no established Python formatter, linter, or type checker for the documentation utility or the new test-only harness. Go's race detector is the applicable concurrency diagnostic; no unsafe-code sanitizer or separate runtime diagnostic is configured for this Go/Python scope.

## Legacy Waivers

None.

## Advisory lock evidence

TASK-035 maps V59–V64 to durable ledger transactions, real Unix runtime/client checks and a separate compiled CLI lifecycle scenario. It exercises FIFO handoff, replay and ended history, liveness/max-hold and queue expiry, force reasons, pause/resume/update/remove, lock/managed capacity separation, inherited read-only validation and parent mutation refusal. An actual managed workload invokes the compiled CLI with its runner-exported context and attempts fresh and replay acquisition with spare lock capacity. Owned borrower subprocesses validate through the typed Unix client and attempt wait, renewal, normal/forced release and cancellation before success, failure or cancellation. Duration-conversion overflow and accepted long-policy calculations after UTC advancement receive regression checks, including FIFO expiry, renewal, reopen/startup and observed wake. Short maximum hold and keepalive interruption also receive regression checks. Controlled startup/wake/UTC fixtures exercise logical discontinuity and backward-clock behavior. A Darwin unit check reads the native wake marker without forcing sleep. The common gate qualifies neither real VM execution nor installed-service/hardware sleep/power-loss behavior. Keep those later qualification boundaries explicit.
