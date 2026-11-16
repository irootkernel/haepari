# ADR-0023: Separate development state and move one preserved installation

Status: Selected for EPIC-010 TASK-040. TASK-041 supplies development isolation and TASK-042 implements release v2/default-home support; TASK-043 implements offline relocation and startup fencing. Exact-candidate qualification and consumer handoff remain with TASK-044 and TASK-045.

## Context

The shared installation uses a dev-named root while native release-service planning rejects the default home. Copying the directory would break inode-bound evidence and could introduce a second installation. Renaming it without a contract would leave service ownership, recovery and consumer references ambiguous. Haepari's own candidates also need a controller independent of the installation used by participating projects.

## Decision

Use an initially empty explicit development home, immutable candidate binaries and exact saved development plans. Keep the existing shared installation selected explicitly until the later Aquarium v0.1.24 official adoption operation. Neither Epic completion nor Haepari publication triggers host cutover.

Select release-service plan v2 for newly planned release services, allowing the native default home while retaining all private-root, owner, binary, Aqua/plist and state-preservation guards. Preserve development v1/v2 and non-default release v1 validation and wire bytes. A directory name does not determine service family. Relocation preserves a release instance's label and requires release-family source/target plans; it does not convert development services.

Move the original installation with an offline, same-filesystem, no-replace rename. Bind an exact plan to installation, logical history, complete retained-file inventory, persistent filesystem identity, both saved plans/binaries and a separately verified backup. Hold the original controller and store ownership through effects. Block busy, uncertain and unresolved state instead of clearing it. Change only explicit service-root metadata; immutable DB payloads, runner requests, results, receipt bindings and historical failures/gaps stay unchanged.

Use a root-local durable journal and identity-based reconciliation across backup, rename, metadata update and reverse move. A supported daemon refuses partial relocation before touching the database or socket. Apply never starts a service or edits consumers. Rollback is a reverse move before consumer resumption and new work, not automatic downgrade or copied-backup restoration. See the complete [home and relocation contract](../contracts/home-and-relocation.md).

Preserve terminal journals as immutable root-local snapshots before an exact-predecessor-bound successor replaces the active journal. This permits another relocation after rollback while fencing all stale mutations. Publish the prepared journal before creating a backup directory, then bind the created directory's persistent identity durably before copying. The mkdir-to-binding interval is not atomic: a directory left without that binding stays unproven and untouched. The supported pre-move recovery is verified rollback of the unchanged source followed, when needed, by a new plan with a different absent backup path; never adopt an unknown partial directory.

Stage every journal/snapshot/owner-marker publication in a fresh private sibling attempt directory on the destination filesystem, outside the installation inventory. Only the live creator's held descriptors authorize staging; fully written and synced bytes enter the guarded destination by atomic rename. A crash leaves unpublished staging outside the root, so a new same-plan attempt can proceed from the unchanged durable journal without adopting or deleting that unknown staging. Keep full root/history inventory validation: there is no temporary-file prefix exception or recursive temporary-ownership journal. Cleanup removes only descriptor-proven current-creator outputs; lost-creator siblings stay preserved for their separate owner.

## Alternatives and consequences

- Directory copy or destination merge would create replacement file identities and ambiguous histories; neither is supported.
- A symlink alias or historical-payload path rewrite would weaken existing private-path and immutable binding guards. Retained evidence is resolved through the current verified root; historical execution paths remain historical.
- A second installation-local queue cannot exclude shared use of a physical VM. Explicit drain, pause, verified development cleanup and handback are required; no second scheduler is introduced.
- Automatic service restart, consumer rewrites or schema downgrade would exceed the mover's authority and make rollback ambiguous. Their owners retain separate actions.

Qualification must include preserved PASS/FAIL/INCOMPLETE, missing and supplementary evidence, same-key replay without workload execution, interruption at durable boundaries, busy/uncertain/foreign/replaced input refusals, and exact-package service and small already-running VM evidence. Local fixtures do not establish those live observations. Existing accepted v0.1.0 evidence and frozen contracts retain their original scope.

The implementation observes original DB/WAL through unchanged descriptor inventories and gives SQLite only private disposable copies. A read-only engine transaction supplies both logical inventory and engine backup; this avoids SQLite sidecar writes to the source while retaining committed WAL state. Darwin stale-socket identity uses native descriptor-relative `getattrlistat` file-ID/creation attributes and the held parent's verified volume capability, rather than opening the socket vnode. Read-only archived status is separate from exact-target-binary mutation authority and follows the verified current predecessor chain.
