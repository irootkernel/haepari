# Testing

## Contract

Contract: aquarium-test-contract/v1
Profile: make
Enrollment: configured for the local Go product slice, fake UTM control and scripted native guest execution. The executable Makefile is authoritative; a passing common gate does not qualify unimplemented UTM, installed-service, or release behavior.

## Canonical Commands

| Command | Handler |
| --- | --- |
| `make test` | Serial, fail-fast prepare → unit → integration → local E2E. |
| `make test-prepare` | Formatting and deterministic static/build checks. |
| `make test-unit` | Go unit specs and offline qualification-driver checks. |
| `make test-qualification` | Offline campaign checks and documentation scope/traceability regressions; no VM or service. |
| `make test-utm` | Optional full single-VM public-CLI campaign with its existing exact manifest/authority interface; outside the local aggregate. |
| `make test-int` | Self-contained Go integration specs. |
| `make test-e2e` | Built local CLI and foreground-controller black-box scenario. |

The E2E handler uses `uv run --locked --group dev` to sync the pinned pytest environment before the scenario; its first run may contact the Python package index. Missing dependencies fail the gate if they cannot be installed. Go build and Ginkgo CLI compilation may fetch pinned public Go modules when the module cache is empty; `go mod download` is the explicit offline preparation step. `make check` delegates to `make test`. `make test-integration` delegates to `make test-int`. `make test-macos-supervision` remains an explicit logged-in macOS host probe; `make test-utm` requires the exact private campaign, actual authority and evidence paths documented in [the campaign instructions](tests/qualification/README.md). Missing selected prerequisites fail; the optional live campaign is outside the local aggregate and is not a recurring task or release requirement.

## Development and release selection

Design Revision 3.5 follows [ADR-0020](docs/architecture-decision-records/0020-local-development-scope.md). Run focused local checks for changed behavior, then the common gate at the applicable task/package boundary. Fault injection, identity refusal, boot/PID changes and controller restarts use isolated local/fake-native fixtures. No normal gate logs out the host, reboots a VM, expires DHCP or replaces the installed controller.

The runnable [project-owned wrapper](examples/project-test/README.md) provides `make test-vm` with read-only selected-resource prerequisites. Focused checks are `go run github.com/onsi/ginkgo/v2/ginkgo --race --fail-on-empty --label-filter=unit ./cmd/haepari`, `go run github.com/onsi/ginkgo/v2/ginkgo --race --fail-on-empty --label-filter=unit --focus="Task017|Task014SSHBoundary" ./internal/utm`, `go run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration --fail-on-empty --label-filter=integration --focus="Task017ResourceDoctor|Task017NativeGuestProject" ./internal/controller`, and `uv run --locked --group dev python -m pytest -q tests/e2e/test_project_cli.py tests/e2e/test_project_inputs.py`. They use isolated local/fake-native resources, never a real VM. E2E controller state retains its existing short `/tmp` private-root fixture because the mounted external volume has a shared ancestor rejected by product guards; project payloads are test-owned scratch. TASK-027 will observe success, meaningful failure, cancellation, timeout, logs/outputs, original-run SSH reconnection, verified cleanup, successor reuse and one ordinary offline manual scenario with the exact packaged candidate. The historical thirty-Job/50 MiB campaign and DF-001–004 experiments are optional hardening. Runtime guards and existing fixture coverage remain.

`Task017ResourceDoctor` asserts a ready registered running-VM report and stalled Aqua/SSH probe cancellation through the inherited caller context, with unchanged resource/Job snapshots, no guest staging and no power effect. The native transport is a fixture, not a live VM observation.

Reuse established authorization for the same repository/test/resource/effect scope. Automatically record candidate/input/result identities; do not require a fresh approval file or question for ordinary Job execution, cancellation or owned cleanup. Optional `make test-utm` retains its existing campaign/authority files; service replacement, power operations, protected-evidence disposal and providers remain distinct effects. A temporarily missing VM blocks only a selected new live observation; it does not reopen accepted evidence or block independent local checks.

TASK-023 M2a focused checks cover closed `haepari.resource/v2` lifecycle budgets, the accepted-configuration projection, native fill of omitted active phase budgets, private schema 12 job-bound pins, and the job-spec request cap. They do not replace the common gate. At that milestone, output enforcement, `doctor --projection`, and queue and retention projection were still open. The current candidate implements those runtime bounds and diagnostics, including visible streaming and truncation and conservative unknown occupancy. The historical focused counts in the F2 and correction paragraphs stay as recorded.

TASK-023 M2b-2 F2 ran these focused checks on 2026-10-17. `gofmt -l` on the changed Go files printed no names. `go test -tags=integration -count=1 -v -race ./internal/managed/ -ginkgo.focus=OperatorDiagnostic` ran 7 of 288 specs: 7 passed, 0 failed, 281 skipped. `go test -tags=integration -count=1 -v -race ./internal/controller/ -ginkgo.focus='OperatorDiagnostic|OperatorProjection'` ran 5 of 352 specs: 5 passed, 0 failed, 347 skipped. `go test -count=1 -v ./internal/client/ -ginkgo.focus='OperatorDiagnostic|AcceptedConfiguration' -ginkgo.label-filter=unit` ran 3 of 36 specs: 3 passed, 0 failed, 33 skipped. `go test -count=1 -v ./cmd/haepari/ -ginkgo.focus='OperatorDiagnostic|AcceptedConfiguration|Task017DoctorCLI|Capabilities|CLIContractErrorAndVerdictExits' -ginkgo.label-filter=unit` ran 6 of 30 specs: 6 passed, 0 failed, 24 skipped. Those specs cover the diagnostic page, a malformed percent and a semicolon, config-view behavior, a bad cursor and id shape, a filtered upper mismatch, an upper bound below the current max, a missing section, limits outside 1..20 including a negative limit that is not silently defaulted, an explicit missing resource, the whole-body cap with a withheld row and its resume, sum overflow without a partial sum, an absent or corrupt header that stays readable, a closed database staying off `INVALID_REQUEST`, the historical pin, the nine-kind `permanent_replay_count` including 0, windows and clock overflow, a missing payload above the public floor, protected-evidence release by an exact resolved receipt, and a `recovery_intent` that stays protected after a later `phase_reconciled`. Documentation validation for this wording is `python3 docs/tools/validate_docs.py --write` followed by `python3 docs/tools/validate_docs.py`. Those focused checks did not measure a fresh VM, the full UTM campaign, or a performance benchmark.

TASK-023 M2b-2 correction ran these focused checks on 2026-10-17. `gofmt -l` on the changed Go files printed no names. `go test -tags=integration -count=1 -v -race ./internal/managed/ -ginkgo.focus=OperatorDiagnostic` ran 11 of 292 specs: 11 passed, 0 failed, 281 skipped. `go test -tags=integration -count=1 -v -race ./internal/controller/ -ginkgo.focus='OperatorDiagnostic|OperatorProjection'` ran 6 of 353 specs: 6 passed, 0 failed, 347 skipped. `go test -count=1 -v ./internal/client/ -ginkgo.focus='OperatorDiagnostic|AcceptedConfiguration' -ginkgo.label-filter=unit` ran 3 of 36 specs: 3 passed, 0 failed, 33 skipped. `go test -count=1 -v ./cmd/haepari/ -ginkgo.focus='OperatorDiagnostic|AcceptedConfiguration|Task017DoctorCLI|Capabilities|CLIContractErrorAndVerdictExits' -ginkgo.label-filter=unit` ran 6 of 30 specs: 6 passed, 0 failed, 24 skipped. The semicolon selector It also ran alone as 1 of 353. Those specs keep a zero-list failed cleanup protected, keep one settled recovery on its accepted historical pin with both windows before a full purge, and after that purge keep the row unavailable with permanent replay retained and both windows absent. One exact guest-completed-cleanup intent releases only that intent and leaves the stored result unchanged. Unrelated, malformed, partial, and post-finish unresolved intents stay held. An earlier explicit `view=config`, empty `view=`, or bare `view` key stays on the legacy first-value config path. Standalone `view=diagnostic;section=queue` and `section=queue;view=diagnostic` stay 400 `INVALID_REQUEST`, retry `none`. Documentation validation for this wording is `python3 docs/tools/validate_docs.py --write` followed by `python3 docs/tools/validate_docs.py`. Those focused checks did not measure a fresh VM, the full UTM campaign, or a performance benchmark.

The TASK-023 harness correction compares the complete process identity and uses one fixture-shared 2s predicate wait with a 25ms poll. A same-incarnation zombie stays pending. An uncertain observation, a timeout, or a malformed present record fails and retains the fixture. The cleanup does not signal. It does not weaken `-race` or a runtime budget. The focused command was `go run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration --fail-on-empty --fail-on-pending --label-filter=integration --focus=Task023M2a ./cmd/haepari`. It ran 11 of 55 specs: 11 passed, 0 failed, 44 skipped, seed 1792204479. Nine of those passes are the new helper specs and two are the existing CLI specs.

The first common `make test` stays a separate result. It stopped in `test-int` on the Task023M2a DeferCleanup owned-process check, and `test-e2e` did not run. The exact incarnation and any race-delay cause stay unknown.

The later common `make test` passed by stage. `test-prepare` passed. Its documentation validation reported no failure, schema validation did not run because there is no standalone JSON Schema suite, and 11 collision lines were informational. `test-unit` seed 1792204843: 9 suites, 187 passed of 189, 2 skipped. Qualification: 84 passed in 2.92s. `test-int` seed 1792204893: 6 suites, 700 passed of 838, 138 skipped, in 24m41.136079958s. That duration is suite wall time, not a product performance result. The integration counts were Haepari 25/55, Controller 338/353, Managed 273/292, Runner 56/83, SQLite 3/3, UTM 5/52. `test-e2e`: 79 passed in 175.00s after a fresh `make build` of `bin/haepari`. That binary was not installed.

Historical TASK-038 log usability belongs to that old exact candidate. Current TASK-023 checks are the changed fake and native bounds. This task did not run a fresh VM. Packaged candidate observation remains TASK-027 and TASK-028. The full UTM campaign and extra performance checks stay optional under [ADR-0020](docs/architecture-decision-records/0020-local-development-scope.md) and the hardware and environment gates in [the roadmap](docs/roadmap.md#hardware-and-environment-gates), lines 105-109. This record claims no Linux support, no unmeasured platform, and no benchmark.

Documentation tooling regression tests run offline with `uv run --locked --group dev python -m pytest -q tests/qualification/test_validate_docs.py` through the existing `test-qualification` stage. They test scope classification, per-requirement case ownership, duplicate traceability rows and historical/current VM-gate dependency closure, not arbitrary prose semantics or product/VM acceptance.

## Stage Mapping

- Prepare runs `gofmt -w` on Go files under `cmd/` and `internal/`, the existing `lint` target (`gofmt` verification and `go vet ./...`), integration-tagged and supervision-tagged vet, `go build ./...`, an offline `uv.lock` consistency check, Python syntax compilation (including the explicit service qualification harness), and the existing documentation validator. It can rewrite Go source and create build/cache artifacts. It starts no controller, database, container, VM, or provider.
- Unit runs pinned Ginkgo v2 through `go run` with `--race`, `--fail-on-empty`, `--fail-on-pending`, and the `unit` label. The stage requires macOS arm64 and fails, rather than skips, on an unsupported host. Specs exercise isolated CLI decoding, closed configuration documents and private configuration files, including a relative file resolved from the working directory and refusal of a non-regular file, contract logic, client transport fake, snapshot/state logic, runner receipt/input validation, and platform validation without a separately managed resource. The unit target includes `./internal/config` with the existing eight packages. It then runs the offline Python campaign-driver checks through `make test-qualification`, using the same pinned pytest environment as E2E.
- Integration uses the `integration` build tag and label with the same race and empty/pending failure checks. It exercises private embedded SQLite, Unix-socket client/controller cooperation, owned local subprocesses, and bounded local fixtures. TASK-009 adds actual same-binary supervisor/trampoline fixtures for fast exec, literal arguments/environment, failed exec versus exit 126, output-heavy inherited writers, TERM/KILL escalation, discovered descendants changing session/reparenting, supervisor death around receipt boundaries, prior-boot rejection and exact ledger import fencing. Additional checks cover active/completed start replay without effects, a present unproven exit retaining controller occupancy/quarantine, injected census failure and termination-budget expiry, pre-exec crash effect absence, descriptor-less trampoline refusal, output-identity mismatch, cancellation contention/exhaustion and discriminating declared PATH wiring. A short-lived caller exits before workload completion; the orphaned runner completes and replay preserves effects. An expired gate produces a proven no-start outcome that the controller can import. Cancellation before gate release produces a proven no-start cancelled outcome without effects or replay. These qualify caller independence; controller restart/reattachment remains TASK-011's portion of V46. Census fault injection tests conservative failure handling; it does not qualify an actual kernel observation failure. Those observations do not qualify the later installed LaunchAgent or universal escaped-process detection. It requires no externally managed database, container, network service, or live provider. Unit specs are excluded by the label filter.
- TASK-010 integration adds native phase failure/deadline scenarios, stopped preparation with owned files and no applicable workload outputs, protected evidence before cleanup, timely missing-output and cleanup-deadline reasons, expired-phase worker refusal, bounded supervisor-observer uncertainty, final-proof loss, foreign-path retention, exact phase identity/budget/deadline fencing and reopen evidence, missing active event refusal, preparation-stop refusals for an existing run claim, invalid identity/reason/state and missing cancellation, queued cancellation/expiry timing shape, ordered entered-phase timestamps and empty adapter-step arrays. A stopped-worker deadline fault creates real partial input files, then exercises collection/verified cleanup, no run claim and one terminal return; the injected copy error does not qualify kernel-call timing. Existing finish-time and blocked-worker fixtures separately cover late versus uncertain observation. Negative receipt checks cover oversized deadlines, foreign protected paths, non-collection inventories and invalid phase states without writes. Negative settlement checks refuse an incomplete run, completed-unproven run/collect/cleanup/verify phases, contradictions with collection/cleanup/protected evidence facts and forged no-start reasons for an entered workload. Reserved no-effect replay coverage now asserts admission/reservation timestamps and empty phase/adapter-step arrays; controller no-effect cancellation asserts the exact prepare/verify phase order. The compiled CLI E2E checks the complete local phase timing shape. Existing before/after-commit crash probes now carry separate outcome/termination proof and committed collection/cleanup/verification observations, assert their exact timing projection and one terminal return on replay. VM no-effect pause/replay/uncertain-power guards remain ledger fixtures; these do not qualify real VM execution.
- E2E builds `bin/haepari` and runs pytest against that executable through documented CLI commands only. It registers one local resource, exercises fenced pause/resume with exact replay and stale-revision rejection, submits and awaits a harmless selected-file Job, checks the result and artifact, reconnects with the same key, and rejects a changed-payload replay without another execution. It also reads filtered resource/Job pages, drains event pages and a terminal follow stream, observes a known failing workload through successful inspection and exact log bytes, and cancels a paused queued Job with an exact-key replay and terminal wait.

## Test Frameworks

TASK-011's current candidate adds read-only and stale recovery plans, scoped attestation, permanent recovery replay after controller key rotation, occupied-resource refusal, original phase deadlines, retained observer workers and adapter intent/progress across database reopening. Native recovery tests refuse attestation of a live exact supervisor, including missing native start/intent files with retained ledger ownership, without imports or capacity return. Child-controller death during collecting and cleaning verifies immutable original intents, compensation and protected evidence, the original cleanup deadline and one terminal capacity return; a live filesystem owner remains occupied and refuses attestation. A deterministic resumed-worker deadline barrier also verifies that another machine or attested recovery stays blocked until the tracked worker actually stops. Native runner tests detach and reattach the same incarnation; durable settlement consumes completed collection/cleanup, commits one capacity return, and resumes an interrupted resource health-check intent. The local CLI E2E kills and normally terminates its own foreground controller while an actual runner waits on a harmless release barrier, then verifies unchanged supervisor/run/deadline, one execution and immutable terminal replay. These observations qualify foreground restart only.

The installed development-service check is explicit and excluded from `make test`. After separately authorized installation of a saved private candidate plan, run `uv run --locked --group dev python tests/qualification/development_service.py --plan-file <saved-plan.json>`. It verifies the exact binary digest, instance root/marker and Aqua plist, registers one synthetic local resource, and checks that occupied uninstall, existing-plist install and mismatched-plan uninstall refuse without changing the controller or plist. It then asks launchd to terminate that exact service with SIGTERM and SIGKILL. It checks the surviving actual product runner's own session, matching native reattachment receipts, unchanged deadline, no rerun, final verified cleanup and idle availability. It leaves the service installed and retained state intact and operates no VM. Missing prerequisites fail. TASK-011's current candidate completed this separately authorized observation on macOS arm64: all three service guards refused, both controller restarts reattached the same runner, the workload executed once, and the Job finished PASS with verified cleanup and zero occupancy. Its exact candidate digest and JSON output remain local runtime evidence and bounded handoff facts; they must not be copied into tracked documentation. The scenario is exclusive; an existing scenario directory is preserved and refuses reuse of that name. Any later explicitly authorized observation supplies a fresh `--scenario <id>`, resource and request key after the earlier Job has settled.

| Language and layer | Framework and evidence | Runner |
| --- | --- | --- |
| Go unit and integration | Ginkgo v2.32.1 and Gomega v1.41.0, pinned in `go.mod` and `go.sum`; specs import Ginkgo, representative assertions use Gomega, and suite bootstraps register its failure handler. Preserved assertion branches use GinkgoT within Ginkgo specs. | `go run github.com/onsi/ginkgo/v2/ginkgo` with stage-specific flags and package lists. |
| Python E2E | pytest 9.1.1, declared in `pyproject.toml` and resolved in `uv.lock`; `tests/e2e/` contains public CLI assertions. | `uv run --locked --group dev python -m pytest -q tests/e2e`. |

The Python documentation validator is a repository check, not a Python product test layer. No legacy framework waiver is in force.

## Gaori Mapping

`.gaori/tester.yaml` maps `docs-validation`, `test`, `test-prepare`, `test-unit`, `test-int`, and `test-e2e` to `generic`. Make and `go run` can emit mixed build, vet, runner, and failure output, so a specialized parser is not claimed. The wrapped command's exit code remains authoritative. Gaori is optional and does not install dependencies or create test authority.

## E2E Environment

The artifact is the locally built `bin/haepari`; public interfaces exercised include `daemon run`, `runner inspect --request <absolute-request.json> --json`, `resource register`, `resource pause`, `resource resume`, `resources list`, `jobs list`, `submit`, `run`, `wait`, `inspect`, `events`, `logs`, `cancel`, `doctor`, `export`, `purge`, advisory acquire/wait/renew/keepalive/release/cancel/list and lock resource remove/recover. Resource update is exercised through the typed Unix client/controller integration suite. Before starting the controller, the harness requires macOS arm64, a regular executable, and a newly created owner-private short state parent under canonical `/tmp`. It sets `HAEPARI_HOME`, `HOME`, `TMPDIR`, and a minimal `PATH` for the child processes. No production state root, existing daemon, TCP port, VM, account, provider, or external endpoint is selected. Missing prerequisites fail the test rather than skip it.

Each scenario starts one foreground controller and waits for a successful public protocol response before registering a test-only local resource. It seeds one non-secret selected file, computes the documented tree digest independently, captures bounded CLI results and a local daemon log, then rechecks every admitted Job for verified cleanup or a terminal queued cancellation with no lease or workload execution. It terminates the exact process it started and removes only its own scratch tree after its identity and shutdown are proven. If cleanup is uncertain, it fails and retains the tree for inspection. The only database is SQLite under that private scratch tree; the only socket is its Unix control socket. The product scenario uses no credential variables, external sandbox, container, volume, paid request, or network service. Dependency preparation may contact the public Go module proxy or Python package index; it is separate from the isolated product scenario.

## Language Diagnostics

Go unit and integration use the race detector without test-result caching; prepare runs `go vet` for default, integration, and macOS supervision build modes and builds the product. Gofmt applies deterministic meaning-preserving formatting first. Python E2E is syntax-compiled in prepare and uses pytest strict configuration and marker checks. There is no established Python formatter, linter, or type checker for the documentation utility or the new test-only harness. Go's race detector is the applicable concurrency diagnostic; no unsafe-code sanitizer or separate runtime diagnostic is configured for this Go/Python scope.

## Legacy Waivers

None.

## Advisory lock evidence

TASK-035 maps V59–V64 to durable ledger transactions, real Unix runtime/client checks and a separate compiled CLI lifecycle scenario. It exercises FIFO handoff, replay and ended history, liveness/max-hold and queue expiry, force reasons, pause/resume/update/remove, lock/managed capacity separation, inherited read-only validation and parent mutation refusal. An actual managed workload invokes the compiled CLI with its runner-exported context and attempts fresh and replay acquisition with spare lock capacity. Owned borrower subprocesses validate through the typed Unix client and attempt wait, renewal, normal/forced release and cancellation before success, failure or cancellation. Duration-conversion overflow and accepted long-policy calculations after UTC advancement receive regression checks, including FIFO expiry, renewal, reopen/startup and observed wake. Short maximum hold and keepalive interruption also receive regression checks. Controlled startup/wake/UTC fixtures exercise logical discontinuity and backward-clock behavior. A Darwin unit check reads the native wake marker without forcing sleep. The common gate qualifies neither real VM execution nor installed-service/hardware sleep/power-loss behavior. Keep those later qualification boundaries explicit.

## TASK-012 evidence delivery and retention

`GuestFileCollectionPages` reads and hashes a real 50 MiB local file through the guest file reader: v1 requires 800 bounded 64 KiB pages and v2 requires 100 bounded 512 KiB pages. It checks the encoded reply limit and refuses changed file metadata. Whole-envelope fixtures include large admitted argv/environment in ordinary, owned and attested responses, require shorter pages within 1 MiB including the final newline, and reconstruct exact bytes/hash without dropping proof fields. Metadata that cannot fit refuses rather than returning a zero-progress page. Authenticated SSH boundary fixtures accept exactly the versioned file limit, reject an oversized page and retain the 64 KiB live-output limit. `Task014NativeSSHProtocol` runs the actual product executable through the constant owned-runner bootstrap for both versions, verifies multi-page bytes and unchanged file incarnation fences. Its large-request v2 cases execute both ordinary and owned command encoders; a separate native attested fixture executes the recovery encoder with a large environment and validates every complete envelope and resumed offset. These are local protocol fixtures; the separate TASK-038 campaign record contains the corrected candidate's observed real-VM large-log collection.

Controller integration covers exact 64 KiB artifact offsets, full streamed export hashes, exclusive destinations, changed files, collection count/byte bounds and an actual short write followed by injected ENOSPC. It verifies owned bulk deletion, shared sealed object survival, foreign/replaced path refusal and permanent admission/cancellation replay. `Task012RecoveryReplayRetention` executes a native local workload before interrupted settlement, applies public machine or attested Job recovery, verifies separate resource health where required, and performs event-prefix/public bulk purge followed by full SQLite close/reopen. It checks actual old payload removal, private replay bindings, unchanged original receipt/result/capacity/mutation/event/run counts, changed-token/evidence conflicts and unavailable public bulk views. These in-process runtime fixtures qualify retention/reopen behavior; actual controller death remains covered by the separate native child fixtures. A reopened sealed-object purge retains recorded internal parent incarnations even when the original leaf was moved beneath a replacement parent; partial deletion and exact replay preserve those fences. Immutable receipt publication uses the shared Darwin exclusive rename with one link, exact-byte replay and symlink/conflict refusal. Native child fixtures stop after a durable collection or disposal intent and after a partial purge: the parent observes live-owner refusal, kills and reaps that exact child, and resumes only the original fixed-budget intent. An unproven collector remains fenced while alive and retains source bytes after its original deadline expires.

The separate filesystem-backed fake guest fixture binds synthetic VM UUID/boot and full native run identity; its workload and native termination use the real product runner, and collection/cleanup/health use a distinct guest source. It observes tokenless unsupported VM inspection, protected guest restart, blocked successor reservation, re-collection, explicit loss before disposal and immutable results. It invokes neither UTM nor SSH and does not qualify F2.

Compiled CLI E2E adds large export/no-overwrite, public purge/retry after restart, unavailable submit/run/wait/events/logs and exact cancel replay. Public recollection of genuinely missing required bytes fails while retaining the source; a separate explicit disposal and health plan safely resolves the fixture without rewriting its result. The fixture removes a retained Job from cleanup tracking only after observed workspace absence and public resolved-recovery/available-zero-occupancy proof, or after its original verified cleanup and completed purge proof. Machine help exposes implemented commands and defaults; CLI and hello advertise the same six independently asserted capabilities.

## TASK-013 UTM host adapter

The unit stage includes `internal/utm`. `UTMCommandBoundary`, `IndividualDHCPLease` and `RegisteredHostPin` exercise bounded argv, forbidden operations, exit-zero Automation failures, UUID-only selection, lease expiry/missing/ambiguity/renewal, pre-start identity proof including a wrong guest OS, and exact pin-file bytes with symlink refusal and allowed comments. `Task013UTMController` exercises real Unix registration and revision-fenced updates against a fake UTM command boundary, duplicate UUID/MAC, unknown VM and missing pin, storage uncertainty, maintenance pause, policy/operator exclusion, durable transfer claims and observation-only uncertainty. It builds and invokes the real CLI to verify missing/zero/negative VM availability revisions, refusal of the flag on a non-VM update, and a successful VM update preserving pause and idle history. Changed/missing successor pins and unknown UUIDs leave the original registration unchanged. Unchanged and skipped successor revisions are refused at both controller and ledger fences. Concurrent transfer calls issue at most one start; delayed stop/start commands cannot release protection or be bypassed through ledger confirmation. Successful return proof survives SQLite close/reopen and is reset before the next effect; command errors and unrecorded returns remain protected after reopen despite desired status. Failed inventory status is conservatively uncertain and failed list refuses the guard. The native command runner drains and reaps oversized output while refusing it. Existing ledger and scheduling integration checks retain VM-local pause, quarantine, stale observation, one settlement return, host-place and crash/reopen proofs. These checks operate no UTM VM and do not qualify SSH, guest cleanup or F2.


## TASK-014 authenticated guest continuity

`Task014SSHBoundary` unit checks fixed argv and encoded request data, dedicated pin and authentication references, foreign VM/MAC/boot/challenge/platform refusal, lease expiry and one uncertain issued operation without retry. `Task014GuestContinuity` integration uses the real native supervisor with a private filesystem guest fixture: completed prepare replay, original start/output and one effect across reconnect/start replay, wrong nonce and reboot refusal, corrupt inventory/runner refusal before Job creation, cancellation and an ambiguous launch claim that never spawns again.

The native VM controller fixture also checks a delayed start against the saved absolute run deadline with both a connected observer and a lost start reply followed by controller restart. It asserts a timed-out outcome, unchanged prepared request bytes and one workload execution. A mixed-case UUID and uppercase MAC update exercises verified registration, successful execution, VM-local pause and explicit readiness-verified resume while preserving stored spelling. Guest start replay rejects a changed effective deadline. These are isolated native-process and fake UTM/SSH observations; they do not qualify an installed candidate on a real VM.

`Task014NativeSSHProtocol` builds a private product binary through `make build OUTPUT=<private-path>` and verifies its ad-hoc signature and embedded identifier after a standalone copy. It runs those same bytes through the exact constant three-file bootstrap using a local subprocess transport, with actual interface/boot observations and synthetic UTM status. It transfers the pinned runner and sealed input, observes original receipts after the transport returns, cancels a transport observer while the workload survives, then proves semantic cancellation and one execution. It operates no SSH server or UTM VM. These fixtures establish the portable executable, protocol and native detach boundary; host-key authentication is enforced by the inspected OpenSSH argv and fake rejection cases, not claimed as a real network observation. Actual Remote Login, UTM/SSH smoke and installed-service backend qualification remain separately authorized observations in TASK-016; power-loss durability remains later scope. The integration stage includes `internal/utm` and requires these fixtures to pass. The macOS distribution build requires the native Apple linker and `codesign`; no signing certificate is required.

## TASK-015 VM phases and recovery

`Task015VMJobs` runs the production VM admission/scheduler and execute/collect/cleanup/recovery ports against fake UTM status and a scripted SSH endpoint. The endpoint decodes the actual transfer archive and invokes the real guest entrypoint in a separate filesystem; native workload supervision uses the product runner. It checks successive Jobs after verified cleanup, persistent state outside the Job directory, ENOSPC retention with bounded recollection/disposal and unchanged original results, partial preparation (including the empty mkdir-before-root-binding interruption, live-worker refusal and nonempty-root retention) and lost-start restart with original identity/deadline and no duplicate workload, retirement of a delayed unobserved start, unknown workload/collector occupancy, native cancellation, cleanup-failure quarantine, VM-local pause with explicit readiness-verified resume, and returned framework-limit rejection without preparation. It also rejects a replaced guest root before recovery and dispatches independent local work while a VM retains its capacity blocker and original queue deadline. Existing TASK-013 controller and managed scheduling checks cover stale pause/lease proofs, quarantine preservation, one capacity return, eligible victims and B/local isolation. The common integration stage runs these checks with the race detector. Host-denial regressions inject returned UTM `OSStatus -1743` at membership and status checks after reservation, require host-wide pause before no-effect capacity return, preserve an operator pause and local availability, and prove no SSH, power or guest-root effect. Unit checks preserve typed host denial before and after identity proof and refuse permission classification for unrelated status text or an unknown command return.

These fixtures operate no actual SSH server or UTM VM, configure no service and qualify neither Remote Login/authentication nor TASK-016/F2. `make test-utm` is the separate approved live observation gate; offline fixtures do not satisfy it. Operational run identities and exact candidate/check bindings stay in private workflow evidence.

`Task015GuestCleanup` injects a directory replacement after original-root validation and immediately before deletion for both a completed native workload and partial preparation. It requires a conflict, preserved replacement sentinel and original bytes, and ordinary guest health refusal of reuse. The partial case directly exercises the retired-preparation deletion helper; existing VM recovery fixtures cover its retirement/control dispatch. The replacement is a controlled local filesystem race, not a live VM observation.

## TASK-016 VM timing observations

`Task016VMAdapterTimings` exercises both a running VM and a required fenced start through the scripted native guest fixture. It checks measured authentication/transfer latency, durable result projection, collection/cleanup phase elapsed durations and immutable result replay. `VMPreparationStepValidation` rejects unobserved, foreign, repeated and negative preparation durations; the local phase integration fixture rejects VM-only observations on a local resource. These checks operate no real VM and do not freeze F2. The separately authorized installed-service campaign must bind the exact deployed executable and environment before backend acceptance.

## TASK-018 opaque inputs

`uv run --locked --group dev python -m pytest -q tests/e2e/test_project_cli.py tests/e2e/test_project_inputs.py`
checks the reference caller and a skill-only candidate with arbitrary raw profile
bytes, independently reproduced inventory/subset digests across multiple 64 KiB chunks, actual executable modes,
queued source stability, missing inputs, unsafe/colliding/credential selections,
corrupt-bundle refusal and restoration under the original key, malformed input
policies/recipes, Unicode aliases, profile-ID/count/path bounds and cwd refusal.
Local negatives include manual recipe validation, malformed project JSON,
permanent-request guidance and intermediate-directory links; a generated
credential-named output proves input-selection rules do not over-restrict results. The integration
focus `Task017NativeGuestProject|Task018` also updates the fake/native VM resource,
then proves the original saved spec, Job ID and admission/profile survive replay
without another workload. These use isolated local/fake-native resources; they
perform no host installation, live VM or provider action.

## TASK-036 recovery and evidence policy

`Task036OperationalRecovery` exercises v2 missing-output settlement and successor reuse, v1 retained-history policy, mixed absent and existing inaccessible evidence, moved/replaced local workspace protection, semantic cancellation with incomplete evidence, transient watch loss and bounded read retries. Native controller interruption before cleanup covers unchanged, moved and replaced v2 workspaces under the original deadline; a missing original pathname cannot become verified cleanup. Event-prefix and completed-payload purge preserve the original local workspace binding. Retry exhaustion preserves guest-effect uncertainty so a deadline cannot substitute for remote worker cessation. `GuestWorkspaceIdentity` checks the corresponding prepared guest inode fence, positive absence and recovery refusal after replacement, plus v1 static error envelopes and classification-only native stderr for both versions.

`Task036PeriodicRecovery` uses the actual native guest fixture to lose a start reply, reconcile the original identity and deadline without another start, settle once and run a successor. Prior operator pause and intervening availability revisions prevent automatic restoration. An undelivered start is retired and cleaned under the original identity/deadline without a native exit or automatic availability restoration. `Task036PowerRecovery` and its ledger checks cover exact orphaned-power cessation authority, stale/repeated requests, no command reissue, pre-guest no-start settlement and separate resource health. `GuestRebootRecoveryNative` and `GuestRebootControllerRecovery` exercise changed-boot recovery with immutable original requests and native receipts, retained INCOMPLETE evidence, separately verified recollection/cleanup/health, and conflicting-root/boot/ownership refusal.

`GuestRebootControllerRecovery` also projects the original pass/fail outcome and exit code for both Job versions while retaining INCOMPLETE evidence. It loses a completed native attestation reply, waits for the original effect deadline, and distinguishes durable retirement from an unissued effect or publication alone. Controlled controller detachment after the host recovery commit, collection record and cleanup record checks exact-authority resumption, unchanged saved intent and phase deadlines, one workload execution and one capacity return. These interruption fixtures detach and reopen the runtime; they do not kill an installed controller process or reboot a VM.

Supplementary absence fixtures combine a positively absent required output with a real existing output, refuse uncollected existing bytes and retain a file appearing at the durable collection/cleanup boundary. They exercise actual private receipt recording, disjoint inventories, timestamp/schema/replay refusals and separate health without rewriting the original result. Native wire negatives retain genuine pinned identity proof and reject absence for legacy, log/receipt, offset, expected-file, contradictory bytes/file and changed workspace/basis requests. Completed ordinary cleanup fixtures execute real native collection/deletion, lose the host reply and model prior guest/controller boots in the remaining receipts. Missing, incomplete, failed or foreign control, false native proof, replacement root and missing/corrupt host evidence refuse recovery. The v1 and deliberately late v2 cases preserve original deadlines, one terminal return and no second start/cleanup; late v2 positive absence remains INCOMPLETE through separately verified reuse. Prior-boot metadata is synthetic fixture state, not an observed host or VM reboot.

These checks are enrolled in the common integration gate and operate isolated native processes with fake UTM/SSH. They qualify neither an actual VM reboot nor the installed development-service candidate. VM power/reboot scenario qualification was removed from EPIC-009 on 2026-10-12; the existing fixture contract remains unchanged.

## Planned operational qualification

EPIC-009 is defined in [the roadmap](docs/roadmap.md); its [execution dossier](docs/TODO-VM-OPERATIONAL-READINESS.md#campaign-and-acceptance-matrix) owns the required corrected-candidate and isolated caller observations. TASK-036–037 use focused checks and the existing common gate. TASK-038 now provides a tracked public-CLI observation driver and explicit input gate; bounded installed-candidate observations are retained in the [campaign record](docs/records/TASK-038-operating-campaign.md), within the accepted reduced core scope. TASK-039 reuses test-owned submission helpers and demonstrates one small isolated caller cycle, while TASK-017 retains the full project Make contract.

The campaign requires exact candidate/resource inputs, finite budgets, success/reuse, failure/cancel/timeout, SSH observation loss, controller restart, large output and a 30-Job soak using one existing registered VM at global limit one and host guest limit one. Master removed VM start/stop, cold-start, guest-reboot and power-intent scenario qualification on 2026-10-12; this gate uses the already-running VM. The two-VM/global-three profile is deferred to [DF-004](docs/deferred-feedback.md#df-004-two-vm-and-local-concurrency) for a separate later campaign; its absence does not block this campaign or EPIC-009 acceptance. Missing prerequisites must fail that selected gate, without a successful skip, private-helper dependency or host fallback. The same-machine, same-user scope does not require separate identity-refusal fault experiments or transport/network benchmarking. Reproducibility means the tracked small core e2e command below, not another live VM campaign. The driver preserves exact submitted spec bytes and permanent keys across resume, rejects uncertain control reissue, drains full logs/events and retains original deadlines. Its report always leaves native qualification unverified for assessment against the dossier. Offline driver checks are enrolled in the unit stage; successful collection alone does not accept TASK-038.

## TASK-037 fixture coverage and live boundary

`Task037TransportCost` compares the same three inspect calls at the actual
SSHRunner archive seam. `Task037SSHMeasurements` verifies interrupted transport,
start/completion storage failure without retry and full drained-byte accounting.
`Task037MixedExecutionLimits` covers mixed VM/local reservation limits one/two/
three, VM capacity one, one return and bounded measurement detail gaps.
`Task037ControllerStartupLimits` runs actual independent native workers through
the Unix controller, including default-one dispatch, selected two/three limits
and progress after a slot return. Invalid startup limits create no state.

`Task037VMOperationalBounds` uses fake UTM and the native guest endpoint to cover
cold status/lease/proof delays, original deadline/no early staging, cancellation
and host-denial ordering, protected active/unresolved runners, exact known runner
purge, unknown-file refusal, interrupted purge/replaced-inode refusal, old keys
and post-purge health, plus historical full-wire compatibility. The owned member
of `Task014NativeSSHProtocol` builds the actual signed product binary and runs
its constant compact bootstrap through a non-POSIX `/bin/csh` remote-command
fixture and an explicitly quoted `/bin/sh` interpreter, using local subprocesses. It verifies no full
runner retransmission, reaped interrupted transport, complete 64 KiB file pages
and oversized/replacement root/runner/file refusal. Existing attested-recovery
coverage proves that interrupted-publication temporary bytes remain unchanged
when known final runner payloads and artifacts are purged. `Task037TransportVerifier` checks normal
and attested completed-cleanup health with different executable bytes while
preserving native control/attestation and refusing effects.

These tests are included in the existing common `make test` gate. They operate
no real SSH server, UTM VM or installed service and establish no real throughput,
reboot, 50 MiB campaign or two-VM concurrency observation. TASK-038 owns the exact
installed-candidate single-VM campaign and its measurements; DF-004 owns the
separate later two-VM concurrency observation.

## Small isolated caller e2e

Master revised TASK-039 on 2026-10-12 to a small repository-owned native CLI
e2e instead of applying Haepari to another real project. After `make build`, run:

```sh
uv run --locked --group dev python -m pytest -q \
  tests/e2e/test_local_cli.py::test_small_offline_caller_cycle
```

The test uses the actual binary and a temporary foreground local controller.
Its four offline Jobs cover success, meaningful native exit-7 failure, a distinct
successful successor and queued sealed-input preservation after source changes.
It verifies artifact bytes/hash, failure logs, same-key replay, exactly one
effect per admission, verified cleanup and zero final occupancy. The test-owned
`_tree_digest` and spec helper expose the minimum public caller inputs without
requiring a consumer integration. Job queue/prepare/run/collect/cleanup budgets
are 30/10/10/5/5 seconds; each terminal wait is at most 15 seconds.

The ordinary `make test-e2e` gate includes this test. It changes no installed
service, operates no VM and edits no other project. Its local result does not
replace TASK-038 real-VM qualification or release/adoption acceptance. See the
[small e2e record](docs/records/TASK-039-small-e2e.md) for the bounded observation
and TASK-017 handoff.

## General manual Job checks

TASK-019 uses `tests/e2e/test_manual_cli.py` against the actual local CLI and
foreground controller, and `Task019NativeManual` integration fixtures against
scripted SSH with the native guest runner. Records are synthetic consumer
assertions, not observed human product passes. They cover setup/smoke separation,
exclusive awaiting capacity, pass/fail/unobserved completion, exact and conflicting
replay, invalid identity/scenario records, caller timeout, cancellation, original
deadline, controller crash and successor reuse. TASK-027 owns real running-VM
manual release observation. These tests are enrolled in `make test`.

`Task019ManualDeadlineCommitRestart` submits a valid-format synthetic PASS record while the
original worker is detached, before abandonment commits, and again after
settlement. Both refuse without recording completion or renewing the deadline.
`Task019ManualPhysicalEvidence` couples an unobserved assertion with actual native
guest collection: known positive absence remains cleanup-eligible, uncollected
bytes remain protected with failed cleanup and quarantine, and unproven collection
holds occupancy until original-proof recovery. `Task019ManualGuestRecovery`
refuses unknown, live-worker and partial-preparation observations, then restores
the original guest response and resumes the same preparation and awaiting deadline
without a second setup/smoke. These are isolated native/fake-transport faults.
The public CLI also names unsealed scenario and unsupported result-schema fields
without echoing their values, and an unsealed refusal leaves the request key free.
The reusable input helper rejects a manual scenario outside its selected files
before admission.

`Task019ActualSchema9Migration` imports the SQL dump produced by the actual
schema-9 binary built from `249c82dc4b9385f84962191b846cddce952526c4` (development
build, not a packaged release). `internal/managed/testdata/schema9.json` binds its
binary and SQL digests and original receipts. Migration preserves the original
installation/admission/cancellation identities without fabricating preparation
or human observations. Supplementary downgraded-layout tests remain separate.

The integration gate emits a Ginkgo progress report for a test that runs longer
than one minute, then once per minute. Reports add diagnostic stacks to retained
check evidence; they do not alter test deadlines, runtime guards or acceptance.

`Task019ManualRemotePreparationRecovery` uses native guest preparation with a
scripted remote supervisor identity absent from the host. Authenticated guest
liveness permits original-run reattachment; false or unknown proof preserves the
entire recovery snapshot. It retains the original preparation deadline and one
prepare/start attempt without operating a real VM or native remote supervisor.
`Task019ManualPublicRecoveryReplay` applies the actual HTTP recovery plan for
local and fake/native VM manual Jobs. It checks the original receipt after human
completion and controller-key rotation, conflicting replay, unchanged mutation,
event and run counts, one preparation and verified cleanup.

## Combined consumer loop and F3

`Task020CombinedConsumer` extends the existing native guest project fixture
through automatic success, native exit-7 failure, occupied manual preparation,
synthetic completion and exact saved-key replay before a successful next Job.
It checks caller aggregate PASS/FAIL/PENDING, the corresponding CLI exits and original Job IDs independently of Make’s recipe-failure exit 2; retained manual and successor output after cleanup, zero final
occupancy and no VM power effect. Both this fixture and the local public-CLI
project test count actual executions to refuse repeated preparation on replay.

```sh
go run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration \
  --fail-on-empty --fail-on-pending --label-filter=integration \
  --focus=Task020CombinedConsumer ./internal/controller
make build
uv run --locked --group dev python -m pytest -q tests/e2e/test_project_cli.py
```

These checks are enrolled in `make test`. They qualify the small synthetic
project through the local controller and fake SSH/native guest execution,
including saved original admission after profile and resource changes.
They do not observe a human product pass or qualify a live or installed VM.
TASK-027 retains that packaged running-VM release check; TASK-038 is not repeated.
The [F3 decision](docs/records/TASK-020-consumer-freeze.md) binds the surface
only when TASK-020 becomes Accepted in the canonical roadmap.
