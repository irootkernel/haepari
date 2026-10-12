# TASK-017 project Make and guest integration

## Delivered boundary

The [reference project](../../examples/project-test/README.md) owns its small
regression recipe and declared suites. `make test` remains independent of a
controller, VM and credentials. `make test-vm` invokes the public CLI and retains
a private selected-file bundle, installation identity, exact specs, request keys
and returned Job IDs. Reconnection uses the same saved requests; suite aggregation
stops after a non-pass and reports skipped suites. The distinct guest script
requires the managed runner's Job/lease context and never submits a Job.

The selected-resource `doctor` observes prerequisites without availability,
capacity or power mutations. The VM probe reads Aqua/running identity, DHCP and
pinned SSH/boot/platform/MAC facts; it stages no guest runner or files. Readiness
is an observation, not a lease, cleanup proof or guarantee of future dispatch.
F1/F2 admission, ownership, deduplication and cleanup guards remain authoritative.
Diagnostic failures distinguish prerequisite stages without exposing private
paths or raw SSH output. A missing controller retains its actionable explanation.
Non-verdict observation errors use `UNKNOWN`; wait expiry uses `PENDING`. Neither
claims a terminal INCOMPLETE result or cancels the original Job.

Mode is independent of suite. This candidate constructs manual specs using the
existing format, but does not claim manual controller admission; TASK-019 owns
that implementation. TASK-018 owns reusable generic payload helpers and expanded
opaque-profile verification. No installation inventory or host deployment is
required by this sample.

## Verification

Observed on macOS arm64 during implementation from baseline
`249c82dc4b9385f84962191b846cddce952526c4`, with the task-owned working diff:

| Check | Observation |
| --- | --- |
| Unit suites in `internal/utm` and `cmd/haepari`, race enabled | Ready, stopped VM, expired individual lease, wrong challenge/MAC/boot/platform, transport loss, bounded deadline and stopped-during-probe; strict SSH pins, malformed/oversized identity and SSH stderr refusal, invalid specs/missing pins without guest access; direct doctor usage, absent-controller envelope without state creation and help/schema advertisement. |
| Integration-tagged `Task017ResourceDoctor|Task017NativeGuestProject` in `internal/controller`, race enabled | Local/missing prerequisite projections without ledger mutation; unsafe local root, advisory resource and configuration-changing-during-probe refusal; unavailable Aqua/private mapping, UTM status and DHCP read refusal; failed post-probe storage read remains distinct from configuration change; actual host wrapper and guest script through the fake UTM/SSH adapter and native guest command, PASS/native 0 and meaningful FAIL/native 7, same IDs on replay, verified owned-directory cleanup with fenced control metadata retained. |
| `uv run --locked --group dev python -m pytest -q tests/e2e/test_project_cli.py` | Fourteen public CLI scenarios: success/failure/all aggregate and exact replay, queued sealed-input stability, ordinary test independence/guest refusal, missing controller/resource refusal, lost suite membership refuses PASS, manual spec plus truthful unsupported admission, tampered spec/foreign installation refusal, undeclared/nested/private/concurrent caller refusal, actual wait expiry then original-Job completion, and test-owned peer observation faults that preserve identity and report UNKNOWN without rewriting the native PASS; actual Make default/explicit mode-suite and spaced-path forwarding, lost admission reply then deduplicated original-key recovery with one workload, malformed config/peer refusal, selected symlink/hardlink/FIFO rejection and malformed guest-context refusal; invalid JSON and unsupported-shape run records retain explicit recovery guidance, actual 25-second caller expiry recovers the original admission, actual 20-second doctor connection deadline bounds an unresponsive private fault peer, and a coordinated real file write triggers the metadata-change capture refusal. |

The focused checks are reproducible from [TESTING.md](../../TESTING.md). Native
Gaori results remain private; they are not promoted into tracked runtime logs.
Initial fixture failures were corrected: a negative MAC case reused its normal
fixture MAC; preflight wrongly required exactly one matching interface instead
of registered-MAC presence; the cleanup assertion wrongly rejected retained
control metadata; external-volume controller state correctly failed private
ancestor checks, so E2E retains the existing short private state fixture.
An additional direct public-CLI reproduction exposed aggregate PASS from a
corrupt saved record containing no Jobs. The caller now persists declared suites
and verifies complete unique suite/Job/key/spec bindings before dispatch or
aggregation. Its regression covers both empty and partially lost records,
missing membership metadata and restored-record replay without another workload.
The CLI missing-controller unit fixture uses a short private root so the macOS
Unix socket limit is exercised correctly; product ancestor and path-length
protections were not weakened. Small future shared-policy consolidation remains
with [the canonical feedback owner](../deferred-feedback.md#df-005-shared-diagnostic-connection-and-lease-source-policy).

The host-platform refusal is enforced from native `runtime.GOOS/GOARCH`; this macOS arm64 run did not observe that branch on another host. It is a source-inspected limit, not cross-platform qualification. Re-enter native host coverage if the supported host scope expands.

The optional generic fallback, temporary pin-snapshot failure and corrupt registered-configuration diagnostic paths remain source-inspected; this record does not claim execution of every guard. Future diagnostic changes re-enter [the named maintenance owner](../deferred-feedback.md#df-006-diagnostic-stage-naming-and-residual-coverage).

No real VM, installed service, host installation, power experiment, provider
workload or release observation was performed. TASK-027 owns the exact packaged
candidate's running-VM loop. This record and task checkboxes do not independently
accept lifecycle; [the roadmap](../roadmap.md) alone owns that decision.
