# Sources and planning provenance

The R2 references were recorded as checked on 2026-09-27. Selected primary pages were checked again for Design Revision 3 on 2026-09-28; installed-version behavior remains unqualified. Their statements support the limited platform facts below, not the proposed Haepari contracts. Installed-version qualification remains mandatory. Do not infer that a changing documentation site guarantees the user's local environment.

| Ref | Primary source | Fact used |
| --- | --- | --- |
| S1 | https://docs.getutm.app/scripting/scripting/ | UTM exposes AppleScript and a CLI wrapper; automation does not cover every feature. |
| S2 | https://docs.getutm.app/scripting/cheat-sheet/ | Examples identify VMs by ID; guest file/command and IP examples depend on QEMU Guest Agent. |
| S3 | https://docs.getutm.app/basics/actions/ | UTM exposes copying/export of a complete VM and its data. This alone is not a qualified restore procedure. |
| S4 | https://sqlite.org/wal.html | WAL has local-filesystem/shared-memory requirements and one writer at a time. |
| S5 | https://www.sqlite.org/lang_transaction.html | SQLite transaction boundaries and concurrent writer constraints. |
| S6 | https://pkg.go.dev/os/exec | CommandContext's default cancellation targets its process; custom cancellation and bounded waits are possible. |
| S7 | https://developers.openai.com/plugins/concepts/skills | Skills are directories with instructions and optional scripts, references, templates, and assets. |
| S8 | https://man.openbsd.org/ssh_config | SSH host identity, forwarding and connection configuration options. |
| S9 | https://github.com/utmapp/UTM/releases/tag/v5.0.6 | UTM v5.0.6 is a beta pre-release (2026-09-24). It adds snapshots for Apple Virtualization VMs on macOS 27, a run-without-saving (disposable) mode, and `utmctl snapshot` and `discard-state`. Not adopted for v0.1.0. |
| S10 | https://github.com/utmapp/UTM/blob/main/utmctl/UTMCtl.swift | `utmctl` prints "NOTE: utmctl does not work from SSH sessions or before logging in." It lists `clone`, `delete`, `ip-address`, `exec` and `file`; the guest operations need a guest agent. |
| S11 | https://www.apple.com/legal/sla/docs/macOSSequoia.pdf and https://developer.apple.com/documentation/virtualization/vzerror/code/virtualmachinelimitexceeded | The macOS license (§2B(iii), checked in the macOS Sequoia license; re-check the installed release's license) permits up to two additional macOS instances in virtual machines per Mac for development and testing. The Virtualization framework defines a `virtualMachineLimitExceeded` error. |
| S12 | https://keith.github.io/xcode-man-pages/launchd.plist.5.html | launchd kills the remaining processes in a job's process group when the job exits, unless `AbandonProcessGroup` is true. `LimitLoadToSessionType` selects the session type, such as `Aqua`. |
| S13 | https://sqlite.org/pragma.html | `PRAGMA fullfsync` and `checkpoint_fullfsync` default to off and use `F_FULLFSYNC` on macOS. |
| S14 | https://go.dev/dl/ | Go 1.27.1 has an official macOS arm64 distribution. |
| S15 | https://pkg.go.dev/modernc.org/sqlite@v1.59.0 | The pinned SQLite driver is pure Go, supports darwin/arm64, and lists BSD-3-Clause licensing. Its installed behavior still requires TASK-004 qualification. |

## User decisions

The user selected the name Haepari, independent tool ownership, project-owned VM tests, the Aquarium-first adoption order, stable installation combinations, full skill/configuration coverage, migration/regression verification, and Sorage-based producer delegation. Go and the bounded v0.1.0 scope carry forward the agreed recommendation. The initial package invented no license, public repository URL, installed baseline version, or provider account; the owner selected MIT in Design Revision 3.2.

## Design Revision 3 source and evidence classes

Documented behavior: [UTM scripting](https://docs.getutm.app/scripting/scripting/) exposes AppleScript and a CLI wrapper with incomplete automation coverage; it does not prove headless/session behavior for the installed release. [Apple launchd guidance](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html) covers per-user jobs and warns against daemonizing; it does not prove runner survival under Haepari's future service configuration. [SQLite WAL](https://www.sqlite.org/wal.html) and [PRAGMA](https://sqlite.org/pragma.html) document WAL sync and macOS `fullfsync` settings; driver/VFS, filesystem and power-loss behavior still need qualification. [Apple Virtualization framework](https://developer.apple.com/documentation/virtualization) describes VM APIs and a VM-limit error; the two-instance licensing limit comes from the macOS license (S11), and no Haepari/UTM-specific bound is documented.

Design choices: managed-first scope, deadlines, private paths, cold whole-package reset and the operator recovery protocol in this package. Report observations: review statistics come from the external design review summarized in [review disposition](review-disposition.md); Haepari did not remeasure them. Unperformed qualification: installed UTM session/Automation behavior, launchd runner-exit behavior, exact VM concurrency and licensing, cold restore, fullfsync driver behavior and real product migration. No platform experiment was run for this documentation revision.

## Aquarium repository context

The initial package creation inspected the actual Aquarium checkout in its own checkout, whose HEAD was `f987ada2de755fa78975d12a66b7838b38662ac7` before this task's planning edits. Its working tree was clean at that initial inspection. The canonical documentation/roadmap, AGENTS.md, epic-execution SOT, current TODO index and old development dossier were read. Earlier turns in this conversation inspected the development manager, test contract, tool catalog, and release-QA contracts; adoption tasks must re-read their current versions rather than treating historical excerpts as immutable consumer contracts.

The installed `use-sorage` skill was read for the handoff model. It defines a sender-owned current document, recipient review and explicit accept/decline; acceptance of a request is not proof of product implementation. Completion results therefore use an explicitly correlated producer-to-Aquarium handoff. No broker command was run and no message was sent while preparing this package.

The Haepari design uses no live credentials, actual private job database, or copied production data. Project-specific rollout focus is proposed acceptance scope; each producer must inspect its current repository and native contracts before implementation.

## Design revision 2 provenance

Revision date: 2026-09-28. The external lease and continuation contracts follow the user's decisions in this conversation. They are prospective product design, not newly verified vendor capabilities. No external platform behavior was changed or freshly asserted for this revision.

Mulgae source inspected in the preceding design discussion was `8f5a583`, especially `skills/use-mulgae/references/recovery.md`, `skills/use-mulgae/SKILL.md` and the native rerun guards. It describes exact failed-attempt recovery, retained successful roles, immutable compose, process-local MCP invocation handles and non-idempotent review-like mutations. It is not evidence of the user's installed binary/skill version. The producer must re-read its current native contracts before implementing Aquarium:TASK-092.

The R2 account above is historical. Design Revision 3 did not read or edit the Aquarium or Mulgae repository. The Aquarium follow-up is specified in this package only; the Cancelled-status inspector issue remains Aquarium-owned.

## Design Revision 3.1 provenance

Date: 2026-09-28. S9–S13 were checked on this date.

**Aquarium (read-only).** HEAD `f987ada` does not contain Aquarium:EPIC-019. The Aquarium:EPIC-019 plan exists only as uncommitted edits that still describe the R2 scope.

**Mulgae (read-only).**
- The rate-limit handling was read at `internal/app/review/policy.go:318-322` and `internal/entrypoint/mcp/tools.go:516`.
- Commit `f5cf064` was read.

**Other producer repositories (read-only).** The commits cited in [external requests](to-external.md) were read.

**Repository changes.** None of these repositories were changed. The requested changes are listed in [external requests](to-external.md).

**Decisions made in this revision.** It fixes the launchd keys, the SQLite `fullfsync` settings, the one-to-one baseline binding and per-job runner transport as design decisions. It does not claim that they were observed; TASK-004, TASK-034 and TASK-016 record the observations.

## Design Revision 3.2 provenance

Date: 2026-09-28.

**Owner decisions:**
- the MIT license
- VM tests in the development loop, with 10–30 jobs a day
- advisory named locks for Mulgae reviews
- Gaori keeping local tests

**Owner observation.** One Mulgae review with six or seven roles works, and two concurrent reviews sometimes fail. This is an owner report, not a remeasurement.

**Sources and repositories.** No new external source was consulted, and no other repository was read or changed.

## Design Revision 3.3 provenance

Date: 2026-09-28.

**Owner decisions:**
- one persistent VM per resource, without per-job reset
- no code-signing requirement for local services

**Observation.** The TASK-034 probe ran on the owner's host and VM; its [record](records/TASK-034-utm-probe.md) lists the host and UTM versions and the results.

**Sources and repositories.** No new external source was consulted, and no other repository was read or changed.
