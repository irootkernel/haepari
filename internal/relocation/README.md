# Offline installation relocation

This package owns strict relocation plan/journal/receipt decoding, read-only
eligibility, complete identity inventories, backup, durable publication,
same-filesystem no-replace moves, exact-plan continuation and reverse moves.
The [canonical contract](../../docs/contracts/home-and-relocation.md) owns the
public semantics; the roadmap alone owns acceptance.

`NewPlan` holds existing controller/store locks without creating them and copies
original DB/WAL bytes into a private SQLite observation. It never invokes the
normal migrate/create opener on the installation. `sqlite.Observation` pins a
read-only engine transaction for logical inventory and engine backup; before and
after inventories preserve source DB/WAL/SHM absence, bytes and identities.
`managed.RelocationObservation` classifies current holds through existing domain
contracts without settling or renewing anything. Terminal negative/gap history
is retained separately from unresolved current lifecycle state.

`Operate` holds the original locks across moves. The prepared journal precedes
backup mkdir; a separate durable directory binding precedes payload copying.
Unbound backup paths remain unproven and untouched. Journal, owner, manifest and
terminal-snapshot records are written and synced in fresh descriptor-owned
external sibling staging, then guarded atomic publication syncs both sides.
Crash-orphan staging is never adopted or swept. Recovery finishes both planned
rename-parent syncs even when the move is already visible. Existing snapshots
are synced with their history directory before succession; backup payload files
and child directories are synced before the manifest and the backup/parent
barrier before verified state. Visible current journals are synced before
mutation/terminal receipt reuse and before startup activation is trusted. Terminal history succession
binds the exact predecessor before replacing its current journal. After owner
publication, apply revalidates the final root, complete retained inventory and
logical ledger, successfully closes that observation, and verifies the backup
before publishing terminal completion. Drift leaves `owner_pending` recoverable
and ordinary startup blocked. Read-only
status verifies the current installation/history chain without requiring an
obsolete mover or granting stale plans mutation authority.

`AcquireController` opens without creation when a relocation journal exists and
binds the held private lock to its planned persistent identity and current path.
Every durable plan decoder also checks supported service schemas and pair
relationships independently of executable/service observations. Missing or replaced locks refuse even if another process still holds the original
inode. Ordinary no-journal acquisition retains new-home creation. Direct
`Activate` independently repeats that binding, including activated journals,
before ordinary SQLite, config commits and socket repair. It refuses nonterminal moves and records
terminal activation once; later legitimate history does not reactivate or
restore rollback authority. Darwin stale socket identity uses no-follow native
`getattrlistat` attributes relative to the held parent, together with that
parent's persistent-object capability and volume UUID.

The unit suite covers move/reverse identity, closed fields, kernel-lock
continuity, typed reference refusal, eligibility classes, backup replacement,
partial external publications, unbound mkdir/binding windows, successor
history, stale mutations, post-owner retained-file/addition drift and
later-inspector status. Startup fixtures exercise actual `RunWithOptions` and
direct activation with missing/replaced original locks, partial journals and
normal new homes. Recovery tests inject failures immediately before real held-
descriptor directory Sync calls, including distinct rename parents and existing
payload/manifest/snapshot/journal entries. Subprocess faults exit at actual
durable/effect boundaries. The SQLite integration suite proves committed
WAL observation and backup without source writes. Controller integration tests
compare retained local/fake-guest/manual history through forward and legal
pre-activation reverse moves, then restored-root public query/log/export and
same-key replay without duplicate dispatch. A fresh predecessor-bound forward
plan retains the forward public observations after the restored-root checks. Commands and qualification limits
are in [TESTING.md](../../TESTING.md#offline-relocation-source-checks).

No code here starts/stops services, runs providers, rewrites consumers or
historical payloads, adopts unknown backup state, or disposes retained evidence.


Relocation plan files keep the complete generated inventory and use a separate
regular-file reader from the managed Job/spec 1 MiB cap. Strict decoding remains
required. Admission manifests are decoded against their object IDs even after a
completed purge; missing purged bulk files are allowed without restoring them.
The same reserved-history namespace validator runs before an initial usable plan,
again during first-apply preflight and before first-journal publication. Missing,
empty and recognized history retain existing semantics; a recognized snapshot is
not adopted as a new current predecessor. Unknown bytes remain untouched.
