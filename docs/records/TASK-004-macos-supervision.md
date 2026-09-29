# TASK-004 macOS supervision decision record

Observation date: 2026-09-29. Scope: general host supervision and local storage on the owner-operated development Mac. This is not a product restart-reconciliation, VM, power-loss, or release qualification.

## Environment and method

| Item | Observed value |
| --- | --- |
| Host | macOS 27.0 (26A428), Darwin 27.0.0 arm64, logged-in Aqua `gui/501` session |
| Filesystem | Local APFS data volume; scratch probes used private directories under `/tmp` |
| Go and SQLite | Repository-pinned Go toolchain and `modernc.org/sqlite` v1.59.0, Darwin arm64 implementation |
| Command | `make test-macos-supervision` runs uncached, isolated host probes and SQLite/state tests |

## Exact probe source and outcome

The observed run used base commit `eb9df357277788e0d2665504cf29f05105571bb9` and these exact Git blob identities: `internal/platform/supervision_darwin_test.go` `22723e940bd16557453f0336f6b75ebbdb3d7cba`, `Makefile` `4aef7d6a600991146c27e3ed8e7e3ad65aec79c7`, `internal/store/sqlite/open.go` `32dde1a7d652caeece9978ecc4a01b394ad7d466`, and `internal/state/root.go` `79a51dc09630e0b56705ad56c03e52e6787343d7`. Documentation edits do not change those probe inputs. Re-run the gate if any of them change.

On this candidate, `make test-macos-supervision` exited 0. The uncached platform suite passed the Aqua LaunchAgent survival/restart, 103/104-byte socket, local `F_FULLFSYNC`, and escaped-descendant probes. The uncached SQLite/state suites and the focused owned-background-group integration case also passed. `make check` and `python3 docs/tools/validate_docs.py` passed for the candidate documentation. The generated `docs/checks.json` reports only what its documentation validator executes; it does not report the separate host gate.

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
