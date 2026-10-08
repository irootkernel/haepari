# Artifact, environment, and migration contract

## Artifact identity

Every admitted input is a sealed, explicit inventory. Each entry has a normalized UTF-8 relative path, regular-file type, size, executable/non-executable mode, and SHA-256. Reject absolute paths, traversal, duplicate paths, case-folding collisions on the selected guest filesystem, symlinks, hardlinks, devices, sockets, and files that change while being copied. Preserve required executable bits. A project needing generated or linked content must materialize a declared regular-file payload before submission.

For a directory digest, sort by the UTF-8 bytes of normalized POSIX paths. Hash length-prefixed path bytes, normalized executable-mode value, file size, and raw content SHA-256 for each entry. Version the digest algorithm. Do not rely on ZIP timestamps, directory modification times, or an archive filename for identity. Include the archive digest separately when a release archive itself is under test.

The TASK-003 internal `haepari.snapshot/v1` manifest uses `haepari-tree-sha256/v1`: hash the algorithm label and a NUL byte, then for each byte-sorted entry hash an unsigned big-endian 64-bit path-byte length, path bytes, one mode byte (`0` or `1` for non-executable or executable), an unsigned big-endian 64-bit size, and the raw 32-byte SHA-256 content digest. The object ID is SHA-256 of the canonical JSON manifest, so a different base-commit or evidence-class claim does not masquerade as a different selected-file tree. Selected path components use clean POSIX relative syntax; case-folded component collisions, links, non-regular files, known credential path forms in both the source root and selected paths, and concurrent source changes are rejected. The credential-path check is a guard for common host authentication locations, not a content classifier: consumers must select non-secret inputs.

Admission stages files below an owned temporary path, hashes and verifies every entry, atomically seals the object, and only then commits the request. If the caller changes selected input bytes during this process, reject admission. Build caches and non-selected files do not join the bundle automatically.

TASK-003 seals and re-verifies selected bytes under the private state root. TASK-033's local auto-Job submission requires `input.selected_files`, compares the submitted lowercase tree digest, and binds the snapshot object to admission in SQLite. A missing or corrupt owned object blocks dispatch without consuming a lease. The current controller supports `development` evidence class only; later production paths must supply and verify the required base and release provenance for other classes. Before accepting a new seal, startup serializes with active sealing; orphan staging deletion has a synced intent and a synced completion in the cleanup journal, and a pending intent is finished on restart. Published objects are never removed by that recovery. The store makes no Git calls and does not infer release provenance. `development` permits an optional supplied base commit; `candidate-integration` requires one; `release-distribution` additionally requires an explicit release reference and archive SHA-256. The consumer remains responsible for proving those supplied provenance claims.

A development bundle may contain exact selected dirty-worktree bytes and tests. Record the Git base commit and dirty/snapshot provenance separately. A released artifact claim additionally records release/tag/source identity and the published archive checksum. Neither case authorizes Git changes, network publication, or installation on the host.

## Implemented delivery and retention (TASK-012)

Finished local Jobs expose `haepari.artifact-manifest/v1`: `job_id`, the original immutable `result`, `files` containing relative path/size/lowercase SHA-256 plus transient `page_token`, and separate `supplementary` recovery outcomes. Required results stream through 64 KiB copy buffers; a collection admits at most 1000 files and 1 GiB total. Missing, partial, oversized or changed files are never silently accepted. A failed partial target is removed while the source workspace remains protected. Declared missing results appear in `evidence_gaps` with `required_output_uncollected`; collection phase/protected references retain the reason and exact owner.

The local supervisor drains combined stdout/stderr through its own bounded pipe into `output.log`. The default combined limit is 256 MiB. After the bound or a write failure, it continues draining and records the actual retained prefix size/hash, dropped bytes and `log_limit` or `log_write_failed` gap. A pipe read failure records `log_read_failed` unless another log gap is already present. This evidence failure does not replace a native workload exit with SIGPIPE. A proven exit requires completion of both process observation and the owned log drainer. Terminal `result.logs` and `result.evidence_gaps` preserve those facts; a gap prevents complete evidence. Older exact runner receipts without a log manifest remain readable, without manufacturing a historical checksum.

Artifact pages use `haepari.artifact-page/v1` with exact `offset`, `next_offset`, base64 JSON `data`, `more` and the manifest's `file` identity. A signed token binds installation, Job, path, size/hash and native file incarnation; it expires after one hour or controller key rotation. A manifest rechecks full hashes; pages recheck incarnation facts and export independently verifies the whole streamed hash. Re-fetch the manifest after a stale token. The manifest refuses more than 3000 collected result files plus one log, or more than 1000 resource-scoped supplementary operations; it never truncates a history to claim success.

`export <job-id> --to <absolute-directory>` creates a new owner-private destination exclusively. It writes `result.json`, streamed files under their manifest paths, then `manifest.json` as the completion receipt, without transient tokens. Existing destinations are refused. Failed exports retain their partial destination for inspection and produce no successful completion receipt. Export is a snapshot of verified retained evidence, not a new verdict.

`purge <job-id> --expected-revision <n> --json` is explicit and finished-only. It refuses active workers, unresolved evidence, recovery/power intents, unknown files, links, foreign ownership and missing positive-identity roots. Before deletion, schema 9 records exact native directory/file identities and atomically marks the admission unavailable. Each unlink rechecks that incarnation and its recorded parents, including the sealed object’s internal paths retained in the durable intent across partial deletion/restart. A retry uses the saved original expected revision and immutable intent, tolerates already deleted paths, and refuses replacements. Each explicit attempt uses one fixed minute for evidence-fence waiting, chunked input verification, inventory planning and deletion; retries do not renew Job or evidence-recovery budgets. Shared sealed objects remain until no available admission references them; original inventory is verified and bound before tombstoning a legacy admission. Object-less ledger fixtures are refused, not silently made replayable.

Purge deletes unprotected artifacts, logs, supplementary files, eligible sealed object bytes and old event payloads. Permanent admission, cancellation and Job recovery replay identities, cursor ownership/floor, small terminal result, run/phase/ownership and retention receipts remain private reconciliation authority. The deleted event prefix comes from permanent cursor ownership, independent of the public unavailable receipt. Small `recovery_intent` and `phase_reconciled` bindings remain below the public floor: exact recovery apply returns its original receipt after purge and restart, while a changed token or evidence under that key conflicts. Public result/events/log/artifact reads report `RESULT_UNAVAILABLE`; submit returns the original unavailable admission and cannot execute again. Resource recovery snapshots omit a purged Job's original result and expose `result_status=unavailable` with the resolved-evidence projection so later health verification remains possible. No automatic expiry, background GC or operator configuration parser is implemented in this slice; TASK-023 owns configuration/performance qualification.

Protected evidence can be re-collected or explicitly disposed of through a revision-bound resource recovery plan. Durable intent binds original results, protected paths, required outputs, native owner/root and independent collection/cleanup deadlines derived once from admitted budgets. Disposal lists evidence loss before apply and records `request:<permanent-mutation-key>` authority before deletion. Recollection records a separate `recovery-<key>` inventory before cleanup and rechecks it on restart. The complete revalidation pass consumes the original cleanup deadline, with one tracked durable reader owner; no file or restart receives a renewed allowance. A stopped expired attempt records failure without reading supplementary bytes or deleting protected source, then returns to quarantine for a new explicit recovery plan. A live old controller or worker blocks resumption; an expired or unproven collector preserves source bytes and quarantine. Completed recovery remains supplementary, never alters the original verdict, and requires separate observed resource-health recovery before dispatch or purge. Production selects separate local and UTM/SSH evidence backends. TASK-015 implements VM collection and revision-bound recovery, with bounded fake/native guest coverage; TASK-016 records the bounded real backend qualification and F2 decision.

## Environment lock

The consumer owns `haepari.environment-lock/v1` documents as reproducible test inputs. Haepari validates the envelope and seals bytes; the product-specific expected installation inventory belongs to the consumer. This is not an authoritative registry of what happens to be installed on the host.

An environment lock records:

| Field | Meaning |
| --- | --- |
| `profile_id` and `profile_revision` | Stable consumer-owned identity; revisions are immutable. |
| `owner` | Repository or delivery scope that defines compatibility. |
| `platform` | Guest OS/architecture and relevant host/runtime qualification identity. |
| `components[]` | Exact selected installation units and their binary/plugin/skill/configuration references. |
| `host_runtime` | Required Codex/client/runner identities, tested capability expectations, and model/provider configuration labels where relevant. |
| `starting_profile` | Prior supported profile for migration, or product-absent profile for clean installation. |
| `expected_inventory` | Reference to the consumer's expected paths/digests and effective connection checks. |
| `recipes` | Digests of installer, state-creation, assertions, and manual scenario documents. |
| `evidence_class` | Development, candidate integration, or release distribution. |

The full lock digest binds all fields. Artifact source locations may be informational, but actual job execution uses already verified bytes or exact content-addressed inputs. No mutable tag, branch, or latest alias remains unresolved at admission.

Haepari does not bind a profile to a VM. The VM resource definition holds the machine-local fields (VM UUID, MAC address, SSH mapping), and they are not committed into a shared product profile. Credentials and absolute personal homes are excluded from shared profile files.

## Installation units and skills

Each component can identify a CLI binary, sibling daemon, application bundle, plugin package, paired skill trees, external skill/rules trees, and connection configuration. Only actual native components are declared. A product without an agent skill records not_applicable with its source evidence rather than inventing `use-<name>`.

The exact binary version and skill source ref are separate fields. They normally move together when the native contract requires it, but an independently pinned skill remains independent. A skill-only candidate is an explicit override of the skill tree with the binary unchanged. A project candidate may replace its entire declared unit; it does not upgrade other products implicitly.

Consumer installers stage and verify complete incoming trees before replacement, remove obsolete owned files, and avoid merging new content into an unexamined old directory. Expected and observed inventories include all regular files, executable bits, frontmatter identity, configuration scope, and enabled/effective integration. Compare the complete tree, not a fixed minimum list.

Locations follow the upstream/native contract. For Aquarium adoption this includes shared `~/.agents/skills`, the active `<CODEX_HOME>/skills` (commonly `~/.codex/skills`), plugin-owned assets, rules and MCP configuration, and project-local discovery roots. Verify duplicate and shadowing behavior in the selected home. Do not copy or reconfigure the host's actual homes to prepare the VM.

Install identity, discovery in a fresh product process/session, observed skill behavior, and cleanup are distinct assertions. A deterministic fixture or pasted skill prompt cannot stand in for actual installed discovery. Capture a bounded observation record with the native runtime/model settings; do not claim probabilistic model behavior is proven for every possible execution.

## Profile selection policy

Normal project development uses the most recent explicitly accepted stable profile and overrides only the target project's declared candidate unit. The profile is pinned at admission. A changed integration can select a named next-release profile; it must not silently rewrite the stable profile.

Aquarium's final VM gate uses its intended complete release combination, including exact tool binaries, paired skills, external global skills/rules, and effective configuration. Components that do not change remain pinned. Candidate integration and final release distribution are separate evidence classes; a digest change requires new affected evidence.

A latest-compatibility run is an explicit maintenance action: resolve supported releases, prepare a new immutable profile, verify it, and then select it as the future default. It never modifies already accepted job inputs. Old profiles required by supported migrations and retained reproductions remain available.

## Suite execution

Each product owns its tests under a project path such as `tests/vm/`. The host-side Make target submits a bundle and waits; the guest entrypoint performs the actual product work. Common resource control stays in Haepari.

A clean-install scenario first removes any earlier installation of the target product, including a candidate left by an earlier job, then verifies that no existing CLI, service, skill, or registration satisfies the test. Other stable dependencies may remain installed. The VM is not reset between jobs ([ADR-0018](../architecture-decision-records/0018-persistent-vm-no-reset.md)).

A migration scenario first establishes the exact prior installation unit, removing newer versions left by earlier jobs. It invokes prior-version public interfaces to create synthetic but meaningful settings and data, verifies the pre-upgrade behavior, preserves that state, installs the candidate through the intended upgrade path, and runs post-upgrade assertions. The starting and target identities are both retained. The candidate installer may not quietly erase the state that the scenario is meant to migrate.

A regression scenario exercises supported behavior on an installed candidate. Expected results come from explicit product invariants and approved behavior changes, not from automatically accepting whatever the candidate emits.

## Required migration assertions

1. Preserve declared user data, IDs, associations, custom configuration, and referenced files.
2. Read prior data and create/read new data after migration, then restart and repeat essential checks.
3. Verify configuration defaults and renamed/removed fields without silently overwriting custom values.
4. Replace the complete skill/rules/configuration unit, remove obsolete owned files, and preserve unrelated installation content according to the native contract.
5. Verify the intended CLI/daemon/MCP pairing and fresh-session skill discovery after upgrade.
6. Apply the product's explicit busy-work policy: supported resume or safe refusal/drain before upgrade. Never infer a promise of uninterrupted migration.
7. Inject at least one interruption in the actual persistent migration path and verify documented recovery or a safe stop without duplicate logical effects.
8. Reject unsupported source versions before destructive change, or test every advertised supported direct/stepped path.
9. Distinguish product downgrade/rollback from test cleanup. Removing a test installation does not prove a product can downgrade its data.

For v0.1.0 adoption, the immediately preceding accepted profile to candidate is mandatory. Wider upgrade paths are mandatory only when advertised by the product. A first-release product records that no predecessor exists; it must not fabricate a migration pass. Its installation and forward state-persistence tests still apply, and the first subsequent supported upgrade becomes a real migration case.

## Result inventories

Record before and candidate installation inventories, and an after-cleanup inventory when the recipe cleans up. Each inventory references the profile and recipe digests and contains only non-sensitive identity fields. Collect needed logs to the host before deleting their guest paths. If required evidence remains recoverable but cannot be exported, stop owned processes as necessary, durably record the protected paths, owner, collection gaps and retention reason, and quarantine the VM; do not delete that evidence or assert it passed. A proven-terminated Job may settle INCOMPLETE with `cleanup_outcome=failed`/`evidence_retained` while the resource remains quarantined. Unproven workload or collector termination keeps occupancy in reconciliation. Recovery may re-collect or explicitly authorize disposal with the evidence loss identified in advance; purge and retention expiry cannot erase unresolved references. Supplementary recovery evidence never rewrites the immutable terminal verdict. See [evidence-preserving settlement](managed-transitions.md#evidence-preserving-settlement).

The queue's stored result includes workload outcome and cleanup status independently. A product can fail with a successful job cleanup; a product can appear to pass while the resource remains unusable. Only the latter resource's verified recovery permits another job.
