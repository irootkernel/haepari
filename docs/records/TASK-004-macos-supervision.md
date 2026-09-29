# TASK-004 macOS supervision decision record

Observation dates: 2026-09-29 and 2026-09-30. Scope: general host supervision and local storage on the owner-operated development Mac. This is not a product restart-reconciliation, VM, power-loss, or release qualification.

## Environment and method

| Item | Observed value |
| --- | --- |
| Host | macOS 27.0 (26A428), Darwin 27.0.0 arm64, logged-in Aqua `gui/501` session |
| Filesystem | Local APFS data volume; scratch probes used private directories under `/tmp` |
| Go and SQLite | Repository-pinned Go toolchain and `modernc.org/sqlite` v1.59.0, Darwin arm64 implementation |
| Command | `make test-macos-supervision` runs uncached, isolated host probes and SQLite/state tests |

## Exact probe source and outcome

The observed run used base commit `eb9df357277788e0d2665504cf29f05105571bb9` and these exact Git blob identities: `internal/platform/supervision_darwin_test.go` `22723e940bd16557453f0336f6b75ebbdb3d7cba`, `Makefile` `4aef7d6a600991146c27e3ed8e7e3ad65aec79c7`, `internal/store/sqlite/open.go` `32dde1a7d652caeece9978ecc4a01b394ad7d466`, and `internal/state/root.go` `79a51dc09630e0b56705ad56c03e52e6787343d7`. Documentation edits do not change those probe inputs. Re-run the gate if any of them change.

After the SQLite connection-setting correction, the gate was re-run uncached on committed source `4674eb4b20d440b656450ecfb6f569a6f68ea273` on the same macOS 27.0 (26A428) host and exited 0. The current `internal/store/sqlite/open.go` blob is `f57b497d7d0f5e43fe417630d07c8a397d4069a6`; the other three probe blobs listed above are unchanged. The gate also exercised `internal/store/sqlite/open_test.go` blob `13800ee72a1115352d6ad3632dd059c661239940` and the focused controller group test against `internal/controller/local.go` blob `79f1a25d298ce63df9cf18e0f38ed22e73d01dce` and `internal/controller/local_integration_test.go` blob `66bd76e3d9010ff0aae76b2ee9d1c82b8fc60065`. This follow-up supersedes the original `open.go` identity for current TASK-004 consumers; the earlier observation remains historical. The uncached platform, SQLite/state and owned-background-group cases passed again, without extending the qualification boundary below.

After the local execution correction, the same uncached gate was re-run on committed source `95784694c7e2ff11e230e94291f417f913c802cf` on this host and exited 0. The focused controller group case used `internal/controller/local.go` blob `3d5ddfc0e2b9fb71eab59124508e29b2129e3fa1` and `internal/controller/local_integration_test.go` blob `c75ca93e33ee2a66249381008c4794d0ba1629d8`; these supersede their previous blob identities for current consumers. The platform, Makefile, SQLite, and state probe inputs listed above are unchanged. The uncached platform, SQLite/state, and owned-background-group cases passed; this does not extend the qualification boundary below.

On the original TASK-004 candidate, `make test-macos-supervision` exited 0. The uncached platform suite passed the Aqua LaunchAgent survival/restart, 103/104-byte socket, local `F_FULLFSYNC`, and escaped-descendant probes. The uncached SQLite/state suites and the focused owned-background-group integration case also passed. `make check` and `python3 docs/tools/validate_docs.py` passed for that candidate documentation. The generated `docs/checks.json` reports only what its documentation validator executes; it does not report the separate host gate.

### Ginkgo gate requalification (2026-09-30)

The common test-contract commit `dbe740783fe42ac578dd264e0316d47a4620ad21` changed the Make runner and probe tests without re-running this host gate. The first requalification attempt failed: `AquaLaunchAgentLeavesIndependentRunnerAlive` timed out waiting for the restarted controller. A separate bounded helper observation showed that the race-enabled initial controller was still alive when `runner.pid` appeared, and exited about one second later. Calling `launchctl kickstart` while that controller was still running did not establish a restart.

The corrected probe records the initial controller PID, waits for its process group to disappear before kickstart, checks the surviving runner before and after restart, and requires a different controller PID in the restart receipt. It also waits for the restarted controller to exit before the final runner survival and identity checks. Each controller-exit wait is bounded at five seconds and fails closed. It retains the race detector and the existing cleanup checks.

The first corrected host gate passed with `internal/platform/supervision_darwin_test.go` blob `e827502d1064fa89086c1af7448ccccad1cfebd9`. The final successful run, including the restarted-controller exit check, used base commit `dbe740783fe42ac578dd264e0316d47a4620ad21` plus the uncommitted corrected test blob `48ff26dd3540fd98b509dcb7c22608a222ce635f`. All other source and test inputs came from that base commit; documentation changes do not alter them. The following runner and test blobs supersede the corresponding earlier identities:

| Input | Git blob |
| --- | --- |
| `Makefile` | `e9f87f50f09799ab3ea074b1c5d3674501dd3200` |
| `go.mod` | `28ccf2ea94131072eb372295f6fc0c89c7410058` |
| `go.sum` | `c1e1c187dc23a291500c70e68733cab1cebc8ae4` |
| `internal/platform/suite_test.go` | `20daed5e7256d7330fccf46d998fcf891d2e8006` |
| `internal/store/sqlite/open_test.go` | `c3d1bb416821390a681b781cb1d69f0a966db536` |
| `internal/store/sqlite/suite_test.go` | `d95d2725262baf5994b6b15036713653db5d38d5` |
| `internal/state/root_test.go` | `01ddbdd0d29f11d222f7eaf2c7fc6377421b215b` |
| `internal/state/suite_test.go` | `b644ae8600ed5bcec697f322905b6ebe5d73f919` |
| `internal/controller/local_integration_test.go` | `5bdfaa275702398d5925938bb195a9287afec19b` |
| `internal/controller/suite_test.go` | `fa1ab13b78ca9d60c8faa2a25a84b402598842e2` |

The product-source probe inputs are unchanged at this base: `internal/store/sqlite/open.go` blob `f57b497d7d0f5e43fe417630d07c8a397d4069a6`, `internal/state/root.go` blob `79a51dc09630e0b56705ad56c03e52e6787343d7`, and `internal/controller/local.go` blob `3d5ddfc0e2b9fb71eab59124508e29b2129e3fa1` match their previously recorded identities.

On the same macOS 27.0 (26A428), Darwin arm64 host and Aqua `gui/501` session, using Go 1.27.1 and the pinned SQLite v1.59.0, `make test-macos-supervision` exited 0. The uncached, race-enabled Ginkgo stages passed all four selected platform probes, three SQLite integration specs, six state unit specs and the single `OwnedBackgroundGroupIsStopped` controller integration spec. Other specs were excluded by the declared label/focus filters, not used as acceptance evidence. A post-run inspection found no task-namespaced LaunchAgent, platform probe process or retained LaunchAgent/escaped-group scratch directory. `go vet -tags=macos_supervision ./internal/platform` also passed. The common `make test` aggregate was not re-run for this correction; the host gate and documentation checks are the evidence for this bounded refresh. The earlier observations remain historical; qualification still excludes product service restart reconciliation, real UTM and sudden power loss.

The implementing coordinator executed the final gate and inspected its terminal output. Its bounded Ginkgo output projection was:

| Suite / selection | Ran / available | Passed | Failed | Filter-excluded |
| --- | --- | --- | --- | --- |
| Platform / `macos-supervision` | 4 / 5 | 4 | 0 | 1 |
| SQLite / `integration` | 3 / 3 | 3 | 0 | 0 |
| State / `unit` | 6 / 6 | 6 | 0 | 0 |
| Controller / `integration`, `OwnedBackgroundGroupIsStopped` | 1 / 17 | 1 | 0 | 16 |

All four stages reported `PASS` and `Test Suite Passed`; the Make process exited 0. This is coordinator-observed execution evidence, separate from the static reviewer assessment and the documentation validator.

The supervision test bootstraps a one-shot, task-namespaced LaunchAgent into the existing Aqua session. Its plist sets `LimitLoadToSessionType=Aqua` and `AbandonProcessGroup=true`. A test helper stands in for the controller, starts a second helper with `Setsid=true`, and exits. `launchctl kickstart` runs the controller helper again without starting a second runner. Cleanup releases the runner and boots out the LaunchAgent. This test does not install the development service owned by TASK-011.

## Observations and decisions

| Decision | Observation | Result and boundary |
| --- | --- | --- |
| LaunchAgent supervision | After the first controller helper exited, its runner still had its original PID and matching PGID. The restarted helper saw that runner and did not start another. The probe passed under `gui/501`; its service and runner were absent after cleanup. | Use the Aqua LaunchAgent keys above and a separate runner session/group. This proves the generic launchd survival behavior for this host, not Haepari reattachment or crash reconciliation; TASK-011 owns those. |
| SQLite durability settings | `internal/store/sqlite.Open` applies WAL, `synchronous=FULL`, `fullfsync=ON`, and `checkpoint_fullfsync=ON`. The uncached driver test wrote a row and read back `wal`, `2`, `1`, and `1` respectively. The Darwin arm64 driver source registers both PRAGMAs and its Unix sync path calls `fcntl(F_FULLFSYNC)` for full sync, with an `fsync` fallback. A direct `F_FULLFSYNC` call on a local scratch file succeeded. | The pinned driver recognizes the settings and contains the macOS full-sync path; retain them. The probe did not trace each SQLite syscall or simulate sudden power loss. No power-loss durability claim follows from it. Recheck the driver/VFS and filesystem if either changes. |
| Unix socket path | A real Darwin Unix listener accepted a 103-byte path and rejected a 104-byte path. `state.Resolve` rejects a socket path at 104 bytes before bind. | Keep private short state roots and the byte-count guard. Long user-selected roots fail before opening the controller socket. |
| Process groups | The focused controller integration test stopped an owned background process group. A host probe then terminated one group while its descendant, moved into a new session/group, remained alive until probe cleanup. | Terminate and check the known owned group before safe return. A child that escapes that group is not found by a group-ID check; macOS escaped-descendant detection is best effort. Do not infer universal cleanup from `kill(-pgid, 0)` alone. |

The driver source checked for this decision was the pinned module's Darwin build of `lib/sqlite_g_0000000000000003.go` (`SQLITE_CkptFullFSync`, `SQLITE_FullFSync`, `_full_fsync`, and `_unixSync`). The runtime readback and source path together support the driver decision; they do not prove device-level persistence.

## Reproduction and handoff

Run `make test-macos-supervision` in a logged-in Aqua session on macOS arm64. Its preflight checks Darwin/arm64; it does not compare the host to the recorded macOS release or machine. It runs the LaunchAgent and socket/full-sync probes without Go test caching, then runs uncached SQLite and state tests and the owned-process-group integration test. The one-shot service uses a unique label and private scratch directory; a failed probe still attempts bootout and process cleanup. Both detached children have a bounded self-exit interval. If their identity or cleanup cannot be proven, the gate fails and retains the scratch directory for inspection. Inspect any failed cleanup before retrying.

TASK-005 consumes the SQLite and short-root decisions. TASK-009 consumes process-group limits. TASK-011 owns development LaunchAgent installation and actual Haepari restart reconciliation. TASK-016 owns real UTM qualification. Sudden power loss and other macOS releases remain unqualified.
