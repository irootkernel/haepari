# Home separation and installation-preserving relocation

Detailed SOT for [EPIC-010](roadmap/EPIC-010.md), targeting Haepari v0.1.1 readiness and later Aquarium v0.1.24 official adoption. The [roadmap](roadmap.md) alone owns Task membership, prerequisites, order and lifecycle status. This dossier integrates the approved execution decisions; it does not announce implemented commands or authorize execution.

## Goal and delivery boundary

| Stage | Haepari development | Participating projects |
| --- | --- | --- |
| After TASK-041 | Independent `~/.haepari-dev/` | Existing shared controller selected explicitly |
| Epic complete and Haepari v0.1.1 published | Keep the development home | Preserve the existing shared installation |
| Aquarium v0.1.24 official adoption | Keep the development home | Relocate that same installation to `~/.haepari/` |

The existing shared root is currently the dev-named `~/.haepari-aquarium-dev/`. Its name does not make it a development candidate. Keep it until the verified released Haepari/Aquarium combination is officially adopted. Waiting for Aquarium v0.1.24 is Master's selected rollout policy, not an inferred technical version requirement. TASK-045 and Epic completion stop at compatibility, rehearsal and handoff readiness. Actual host migration is a later adoption operation; neither Haepari publication alone nor this registration triggers it.

The deleted old `~/.haepari-release` history is not a migration input and must not be recreated. Historical negative outcomes are never reclassified as clean or PASS.

## Authorities and Task ownership

- [Architecture](architecture.md), [configuration](contracts/configuration.md) and [CLI/jobs](contracts/cli-and-jobs.md) own current controller, home-selection and service semantics. TASK-040 owns their selected versioned changes and requirement/case mappings.
- [Artifact/profile](contracts/artifact-and-profile.md), [managed transitions](contracts/managed-transitions.md), [locks](contracts/locks.md) and [operations](operations.md) own existing identity, evidence, occupancy and safe-return rules. TASK-043 must preserve them through relocation.
- [Testing](../TESTING.md), [verification](verification.md) and [toolchain guide](../tools/README.md) own executable checks and evidence boundaries; TASK-044 owns the candidate qualification.
- [Producer packet](handoffs/haepari-to-aquarium.md) owns handoff contents; TASK-045 prepares the candidate packet and later official-combination requirements. Aquarium owns its consumer changes, combination acceptance and adoption operation. No Aquarium repository change is part of this Epic.

Use the six Task definitions in the Epic as the work breakdown. Recommended order is TASK-040 through TASK-045; actual dependencies remain on the roadmap. Existing product requirements and historical acceptance remain unchanged until the relevant Task delivers and reviews its canonical updates.

## Development-home contract

TASK-041 uses the existing native development service with instance `main`, initial limits 1/1, and an explicit absolute development root. Do not initialize it from shared data. Store immutable candidates under ignored `bin/dev/<binary-sha256>/haepari` and retain exact saved service plans. Drain work and locks before uninstalling the old exact development service and installing its next candidate; retain development state and never overwrite a running binary.

Select the development root only for those commands and services. Do not change shell profiles, general-project selection or Mulgae MCP. General consumption from the Haepari repository still uses the shared controller where appropriate. Automatic tests retain unique canonical `/tmp` state; qualification services retain their scoped disposable HOME. No host candidate service installation occurs as an ordinary test side effect.

Use local/fake-native fixtures first. Before an authorized development VM observation, stop new shared VM work, prove it drained, and pause that resource. Resume shared use only after the development workload, consumer/native cleanup and development-resource pause are verified. Two installation-local queues or locks cannot coordinate the same physical VM; add no second scheduler or assumed cross-installation exclusion. No VM power action is implied.

## Planned service and relocation interfaces

TASK-040 defines the selected [home and relocation contract](contracts/home-and-relocation.md) and [ADR-0023](architecture-decision-records/0023-home-separation-and-relocation.md). TASK-042/043 still own implementation; none of these interfaces is a current native capability.

New release plans use `haepari.release-service-plan/v2`, retaining the existing field structure and allowing the native default home. Development plans retain default-root refusal. Non-default release v1 and development v1/v2 remain supported with their original validation. Private-root, exact ownership, binary and plist fences remain mandatory.

```text
haepari service relocate plan --source-plan <old-service-plan> --target-plan <new-service-plan> --backup-dir <private-backup-directory> --json
haepari service relocate apply --plan-file <relocation-plan> --json
haepari service relocate status --plan-file <relocation-plan> --json
haepari service relocate rollback --plan-file <relocation-plan> --json
```

Plan is read-only and emits `haepari.service-relocation-plan/v1`. The selected contract defines `haepari.service-relocation-receipt/v1` and `haepari.service-relocation-journal/v1`, exact closed fields, canonical digests and phase reconciliation. Bind installation ID, source/target plans, binary digests, root/file identities, logical ledger and evidence inventories, backup location and one relocation identity. Recheck those observations before applying; drift refuses instead of silently refreshing the plan.

Apply requires the old service stopped, exclusive controller ownership, no active or queued work/current grants, no unresolved recovery, unknown occupancy, pending native effects or unfinished cleanup. An unreachable controller is not drain proof. Preserve unresolved state and report the blocker; never manufacture eligibility through disposal, SQL edits or forced release.

Use an absent, private, non-symlink target on the same local filesystem. Verify a consistent DB backup with required WAL state, the full retained-file inventory, original plans and executable before a no-replace directory rename. Retain original file identities. Cross-filesystem copy, destination merging and symlink aliases are unsupported.

Move the same database, objects and Job evidence; preserve installation/Job IDs, request keys, resource revisions/configuration, results, receipts, digests and historical failures/gaps. Existing old absolute work paths remain historical records, not permission to rerun completed Jobs. Update only authorized mutable service-root metadata; keep the existing service instance/label. Unknown live references into the old root block relocation until handled through their supported owner. Consumers' configuration is outside the native mover.

Journal durable boundaries within the moving root and sync publication/rename metadata. Same-plan apply reconciles that journal with actual identities and continues or returns the recorded result. A conflicting plan or replaced object refuses. Partial relocation blocks ordinary daemon startup. Apply itself starts no service and edits no consumer/MCP configuration.

Terminal-journal succession preserves an immutable snapshot before an exact-predecessor-bound new plan becomes current; old-plan mutations then refuse while retained status receipts remain readable where locatable. Backup creation follows a durable prepared journal, and copying follows a durable binding to the created directory's persistent identity. An interruption between mkdir and that binding leaves an unproven directory: preserve it, refuse adoption, and allow only verified pre-move rollback of the unchanged source. A later plan may use a different absent backup path. Full-inventory checks account only for the exact declared journal/history/owner metadata delta, never a blanket namespace exclusion.

Publication staging is a fresh private sibling on the verified destination filesystem, outside both installation roots/history and backup payload. Only the live creator's descriptors authorize it; a fully synced complete record is atomically renamed into its guarded destination. A crashed creator's incomplete sibling remains untouched and is never adopted, while same-plan recovery derives a fresh attempt from the unchanged durable record. Unexpected files inside the root/history still refuse; no prefix-based inventory bypass or recursive staging-ownership journal is permitted.

Rollback requires both services stopped and consumers not yet resumed; validate the same journal and identities, then reverse the directory move and restore root metadata. After a v0.1.1 startup, use v0.1.1 at the original path too: no automatic schema downgrade. A copied backup preserves data but is not automatically a runnable restoration because inode-bound evidence may differ. Never restore it over newer work or claim copied identity bindings remain valid.

## Acceptance matrix

| Owner | Required observations |
| --- | --- |
| TASK-041 | Separate root/socket/installation identities, harmless local execution, drained exact candidate replacement, unchanged shared-state sentinels |
| TASK-042 | Disposable default/non-default release plan/install/uninstall and state retention; development-default, foreign owner, modified binary and mismatched plan/plist refusals |
| TASK-043 | Same-installation move and reverse move; unchanged immutable payloads, keys, evidence hashes and file identities; query/log/export/replay without duplicate workload |
| TASK-043/044 | Busy or uncertain state, existing target, foreign filesystem, symlink, changed inventory/plan, interrupted backup/rename/owner update, same-plan continuation and conflicting retry |
| TASK-044 | Local and fake-VM terminal history, manual records, PASS/FAIL/INCOMPLETE and supplementary/missing evidence; existing common checks, extracted-package checks and small exact-candidate running-VM release loop |
| TASK-045 | Current Aquarium consumer-contract compatibility and disposable relocation rehearsal; handoff identifies evidence class, future official-combination verification, limitations and operational owner |

Use pinned toolchains, focused checks first and the existing common gate. Keep controller restart, same-key replay, workload, evidence, cleanup, provider execution and actual consumer acceptance separate. No broad soak, extra VM, power/reboot campaign or live provider review is added. Required physical observations need their own authorized envelope; missing evidence blocks only its dependent acceptance.

## Later Aquarium v0.1.24 adoption operation

The operator coordinating official Aquarium v0.1.24 adoption owns actual host cutover after Haepari v0.1.1 and the verified released combination are available. TASK-045 hands over the following procedure; it does not execute it or certify a future Aquarium release.

1. Verify official archive, binary and paired-skill identities and the tested Haepari/Aquarium combination. Bind future official artifacts separately from candidate evidence.
2. Suspend participating consumers, drain Jobs and locks, and capture installation/history/evidence baselines and the exact external-reference inventory, including Mulgae MCP.
3. Remove the old exact service while preserving state. Prepare the fixed official v0.1.1 target service plan and relocation plan; retain verified backup under `~/.haepari-backups/<relocation-id>/` outside both roots.
4. Apply the native relocation, then separately install the target service. Verify old history/evidence, resources including `macos-main` and `mulgae-review`, limits 2/1, a bounded local Job and lock acquisition/return.
5. Update only inventoried live consumer home references to the explicit absolute shared root, including Mulgae MCP. Preserve optional parent-context/borrowed-grant forwarding. Restart the necessary clients and resume consumers only after verification.
6. Record actual cutover results and keep backups; do not delete them automatically. Before consumer resumption, use the validated rollback on failure. Uncertain new work must be reconciled before any reverse move.

Publication, host installation, runtime updates, actual migration and Sorage transmission retain separate authority. This dossier and its unchecked criteria grant none of those effects. TASK-040 supplies contract and mapping decisions only; downstream implementation, service/VM observations and final acceptance remain separately evidenced under their execution envelope.
