# TASK-026 operator and developer documentation reconciliation record

Date: 2026-10-18. Documentation-only reconciliation plus a disposable local runbook exercise against the release-candidate package. No runtime code, contract freeze (F1/F2/F3), service, VM, provider or publication change. Requirement coverage: HP-REQ-056, HP-REQ-057. Main accepted the Task under delegated Epic authority after independent Task and Epic review; the canonical lifecycle and requirement checkboxes record that decision. Overall Epic acceptance remains pending.

Baseline: commit `6dc12331ece76fbb26fbf73e400590677fccf039` with only the Main TASK-026 activation delta (`docs/roadmap.md`, `docs/MANIFEST.json`) dirty. Go sources are unchanged by this task; the TASK-025 common-gate pass covers the identical Go code, so verification was proportionate documentation/package/runbook checks (ADR-0020).

Current installation guidance moved to [README.md](../../README.md#install-a-release)
on 2026-10-21. References to `packaging/INSTALL.md` below describe the historical
reconciliation and its original observed package; they are not current paths.

## Reconciled statements (changed)

| Location | Stale claim | Correction |
| --- | --- | --- |
| `docs/operations.md:3` | "Release procedures remain planned; execute them only after their implementation… exist." | `make release` candidate packaging and the explicit user release service are implemented (TASK-025); publication and release-service status/stop remain separate planned authority. |
| `docs/operations.md:27` | "VM state binding remains TASK-013/TASK-015." | TASK-015 implements VM state binding. |
| `docs/contracts/cli-and-jobs.md:5` | "VM resource removal and release commands remain unsupported"; "the general configuration loader remains TASK-023". | VM resource removal remains unsupported; release service status/stop commands remain planned; the configuration loader is implemented by TASK-023. |
| `docs/contracts/cli-and-jobs.md:201` | "VM resource removal and release commands remain planned." | Same narrowing as the CLI family table (rows 16–17). |
| `README.md:3` | Opening narrated pre-implementation history with no release surface. | Names `make release`, the packaged skill/examples/verifier, [packaging/INSTALL.md] and publication-as-separate-authority. |
| `README.md` (Build and check) | No release target mention. | Adds `make release` candidate sentence with the TASK-025 record link. |
| `README.md:83` | "Actual VM observation remains TASK-013/TASK-015 work." | Automatic VM execution and revision-bound recovery are implemented (TASK-015); the exact-release running-VM loop remains TASK-027. |
| `README.md:103` | Borrower validation cited only the internal Go client. | Adds the public `haepari locks list` same-page/server-`now` read-only validation path from the lock contract. |
| `docs/README.md:3` | Status ended before EPIC-007 packaging. | Adds the TASK-025 candidate-package/skill/release-service sentence and states the accepted TASK-012/F1 freeze without provisional candidate wording. |
| `docs/README.md` (Read in this order, item 6) | No packaging/skill/TASK-025 record links. | Links the TASK-025 record, `packaging/INSTALL.md` and the `use-haepari` skill guide (`skill/use-haepari/SKILL.md`). |
| `packaging/INSTALL.md` §1 | "`shasum -a 256 -c` works from any directory." | Checksum entries resolve against the current working directory: run the check from the directory holding archive and sidecar. Observed both directions below. |
| `docs/contracts/configuration.md:60,103` | "…remains TASK-023 work" / "…remain TASK-023." | TASK-023 implements the loader, diagnostic policy and bounded projection pages; queue budgets stay the consumer's choice. |
| `docs/contracts/resource-and-runner.md:83,144` | "the full configuration loader remains TASK-023" / "Real qualification … remains TASK-016." | Implemented by TASK-023 / recorded by TASK-016. |
| `docs/contracts/managed-transitions.md:92` | "TASK-011's current candidate implements…; …remain TASK-012." | TASK-011/TASK-012 implement those slices. |
| `docs/architecture.md:3,100,151` | "The TASK-019 candidate", "qualification remains with TASK-013/TASK-016", "TASK-011's current candidate". | Transitional wording updated to implemented attributions. |

## Audit conclusions (unchanged, with evidence)

- Requirements, including the security rows HP-REQ-047–049 (`docs/requirements.md:213–226`), state current obligations accurately (same-user boundary, secret omission, symlink refusal); no change.
- Architecture/contract boundary phrasing ("not an OS security boundary") is accurate; `docs/architecture.md:202` already covers the development and release LaunchAgents.
- Examples: `docs/examples/` stays marked illustrative; packaged examples keep the two marked placeholders `<BUNDLE_ROOT>`/`<TREE_SHA256>` that validators refuse (observed below).
- Project-owned testing (`TESTING.md`, TASK-025-updated), optional consumer guidance (`integrations/mulgae-review.md`, `to-external.md`, `aquarium-adoption.md` — consumer-owned) and legacy-retirement boundaries (`operations.md` retire section, `INSTALL.md` §6) are accurate; no change.
- Historical records stay historical: `docs/records/*`, `REVISION-NOTES.md`, ADRs (e.g. ADR-0019's decision-time "remains TASK-023" consequence), the EPIC-009 dossier (F23), accepted epic checklists (e.g. EPIC-002's TASK-012 notes), `traceability.md` V32/V33 planned case definitions, and the nine unsent `rollout-requests/` drafts.
- Command surface: every `haepari` verb and `make` target referenced in the edited public documents exists in `help --json` (41 commands) and the Makefiles; no unsupported command appears as available. `lease` and release-service `status/stop` appear only in deferred/planned markings. Remaining `<placeholder>` tokens in active guides are documented argument placeholders, not executable-looking unmarked placeholders.

## Disposable runbook exercise (offline, no VM/service/provider effects)

Environment: fresh `make release RELEASE_OUTPUT=<scratch>/dist` archive `haepari-0.1.0-darwin-arm64.tar.gz` (binary SHA-256 `c3c3f65499f0404738aeae8e95ec2e62ebcb57372d3a0855ccd1ceb0bded34c8`; a documentation build, not the TASK-025 qualification archive). Controller state root and planning `HOME` used a short owner-private canonical `/private/tmp/hp-t026.XXXXXX` exception (socket path 46 bytes, ancestor guards satisfied), distinct so the release plan's `--root` never collided with the `$HOME/.haepari` default. Scratch: `t026-run/` under the assigned epic scratch holds all logs.

Observed, exact outcomes:

1. Sidecar positive: `shasum -a 256 -c` from the archive directory → `OK`, exit 0. Negative (sidecar copied alone elsewhere): `No such file or directory`, `FAILED open or read`, exit 1 — confirming the corrected §1 wording.
2. `verify_tree.py`: package-root default → `tree verification OK: 9 file(s) match`, exit 0; different cwd default (script-directory resolution) → same, exit 0; explicit bad root `/private/tmp` → fail-closed scan error on a foreign entry, exit 1; isolated empty root → `missing inventory: …/SHA256SUMS`, exit 1.
3. Standalone binary (separate `env -i` invocations): `version --json` → 0.1.0 with executable digest; `capabilities --json` → six capabilities, `qualified_backends: []` (truthful, no live qualification claimed); `help --json` → `haepari.help/v1`, 41 commands.
4. Foreground controller `daemon run --backend local` on the short root; `resources list --json` reachable.
5. `service plan --release --instance t026-doc --binary <pkg>/haepari --root <short-root>/svcroot --json` → `haepari.release-service-plan/v1`, label `io.rootkernel.haepari.t026-doc`, pinned binary digest equal to the archive binary digest, plist pinned to `<planning-HOME>/Library/LaunchAgents/<label>.plist`, limits 1/1. No plist or state written. Negatives: explicit default root `$HOME/.haepari` and empty `--root` both exit 2 `INVALID_REQUEST` (observed message preserved in scratch logs).
6. Local auto Job through the localized packaged example: submit → wait exit 0, `PASS`, `cleanup: verified`; same-key replay returned the same Job ID; `export` wrote manifest/artifacts; `purge --expected-revision 19` → `complete: true`. An initial submit whose inventory digest covered more files than the job's selection was refused `CONFLICT` exit 2 (truthful validator). The raw unlocalized example was refused `INVALID_REQUEST` exit 2.
7. Advisory lock: `lock acquire --wait` granted, `locks list` showed the request with server `now`, `lock release` exit 0.
8. Manual flow (synthetic, explicitly labeled): `job-local-manual.json` reached `awaiting_manual`; the event exposed the sealed scenario document, required scenario, manual deadline and workspace; a synthetic pass record (`haepari.manual-result/v1`, observer `t026-doc-writer`, evidence reference marked `synthetic-doc-runbook`) completed with exit 0 and settled `PASS`/`cleanup: verified`. This is a documentation-path exercise only — no human qualification and no TASK-027 release-manual claim.

Controller stop: exact PID terminated, no child processes, no `hp-t026` processes remain, short root deleted. Remaining setup limitations, honestly: this exercise used one fresh local build on one macOS arm64 host with no installed service, no UTM VM and no network; it does not qualify installed-service survival, real VM flows, hardware sleep/power-loss, or `qualified_backends` population.

## Bookkeeping

`python3 docs/tools/validate_docs.py` passed before and after edits; `--write` regenerated `docs/checks.json`/`docs/MANIFEST.json` and the stated link/document counts in `docs/package-validation.md` were synchronized to the computed values. `git diff --check` clean. Main's delegated acceptance updates the canonical roadmap, this Task's checklist, and HP-REQ-056/057; it does not establish overall Epic acceptance.
