# Follow-up candidates

These items came from EPIC-001 cold validation. They are candidates, not accepted work or roadmap status changes. Recheck the current implementation and tests at the named re-entry point before adopting an item. `docs/roadmap.md` alone owns task identity and lifecycle.

## Structural TODO candidates

- At TASK-005/007/009, decide whether the current `internal/managed` ledger schema and queries, `internal/store/sqlite` connection setup, and `internal/controller` bootstrap/scheduler/local execution need explicit application, store, and executor seams before adding another backend. Remove the unused `Ledger.Input` API if no owner emerges. The current consolidation makes later backend changes touch transport and persistence together.
- At TASK-010/012, centralize terminal settlement choreography, lifecycle vocabulary, error-to-exit mapping, and envelope validation so the controller, CLI, and managed contract cannot drift as more result/error paths are added. Keep externally visible behavior and existing error codes stable during that refactor.
- At TASK-009/012, consolidate contained no-follow file traversal and define the snapshot-to-managed digest representation at one boundary. The current duplicated traversal and prefix conversion require coordinated changes when new artifact paths are added.

## Deferred verification and diagnostics

- Before TASK-006/008 accepts concurrent clients, add deterministic same-key concurrent admission and wait-registration race coverage; sequential tests cannot prove the intended single-admission and no-lost-wakeup invariants.
- Before TASK-008/012 freezes protocol behavior, assert both sides of unsupported-version handshake and malformed/missing protocol-header rejection. Current positive traffic does not prove rejection before mutation.
- Before TASK-009/010 expands local execution, assert the deadline-boundary and SIGTERM-accepted paths with deterministic timing/termination fixtures. Existing timeout tests cover escalation but do not cover every classification branch.
- Before TASK-009 relies on workload PATH resolution for more workloads, drive a bare-name command through `executeLocal`, including declared PATH wiring, relative slash resolution, and non-executable first candidates. Direct resolver coverage alone cannot detect controller wiring drift.
- Before TASK-009 extends source admission, assert permission-denied source-root and selected-file classification as `PREREQUISITE_UNAVAILABLE` on a supported non-root fixture. The existing missing-source case covers only ENOENT.
- Before TASK-009 exposes start failures as operator evidence, distinguish an unresolved workload executable from other process-start failures in the retained result or receipt without exposing unrestricted OS error text. The current generic `process_start_failed` reason slows diagnosis of workload PATH mistakes.
