# Artifact, environment, and migration contract

## Artifact identity

Every admitted input is a sealed, explicit inventory. Each entry has a normalized UTF-8 relative path, regular-file type, size, executable/non-executable mode, and SHA-256. Reject absolute paths, traversal, duplicate paths, case-folding collisions on the selected guest filesystem, symlinks, hardlinks, devices, sockets, and files that change while being copied. Preserve required executable bits. A project needing generated or linked content must materialize a declared regular-file payload before submission.

For a directory digest, sort by the UTF-8 bytes of normalized POSIX paths. Hash length-prefixed path bytes, normalized executable-mode value, file size, and raw content SHA-256 for each entry. Version the digest algorithm. Do not rely on ZIP timestamps, directory modification times, or an archive filename for identity. Include the archive digest separately when a release archive itself is under test.

Admission stages files below an owned temporary path, hashes and verifies every entry, atomically seals the object, and only then commits the request. If the caller changes selected input bytes during this process, reject admission. Build caches and non-selected files do not join the bundle automatically.

A development bundle may contain exact selected dirty-worktree bytes and tests. Record the Git base commit and dirty/snapshot provenance separately. A released artifact claim additionally records release/tag/source identity and the published archive checksum. Neither case authorizes Git changes, network publication, or installation on the host.

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

The operator separately binds a profile to a qualified local baseline. Baseline registration names actual VM ownership and reset details. Those machine-local fields are not committed into a shared product profile. Credentials and absolute personal homes are excluded from shared profile files.

## Installation units and skills

Each component can identify a CLI binary, sibling daemon, application bundle, plugin package, paired skill trees, external skill/rules trees, and connection configuration. Only actual native components are declared. A product without an agent skill records not_applicable with its source evidence rather than inventing `use-<name>`.

The exact binary version and skill source ref are separate fields. They normally move together when the native contract requires it, but an independently pinned skill remains independent. A skill-only candidate is an explicit override of the skill tree with the binary unchanged. A project candidate may replace its entire declared unit; it does not upgrade other products implicitly.

Consumer installers stage and verify complete incoming trees before replacement, remove obsolete owned files, and avoid merging new content into an unexamined old directory. Expected and observed inventories include all regular files, executable bits, frontmatter identity, configuration scope, and enabled/effective integration. Compare the complete tree, not a fixed minimum list.

Locations follow the upstream/native contract. For Aquarium adoption this includes shared `~/.agents/skills`, the active `<CODEX_HOME>/skills` (commonly `~/.codex/skills`), plugin-owned assets, rules and MCP configuration, and project-local discovery roots. Verify duplicate and shadowing behavior in the selected home. Do not copy or reconfigure the host's actual homes to prepare the VM.

Install identity, discovery in a fresh product process/session, observed skill behavior, and restoration are distinct assertions. A deterministic fixture or pasted skill prompt cannot stand in for actual installed discovery. Capture a bounded observation record with the native runtime/model settings; do not claim probabilistic model behavior is proven for every possible execution.

## Baseline selection policy

Normal project development uses the most recent explicitly accepted stable profile and overrides only the target project's declared candidate unit. The profile is pinned at admission. A changed integration can select a named next-release profile; it must not silently rewrite the stable profile.

Aquarium's final VM gate uses its intended complete release combination, including exact tool binaries, paired skills, external global skills/rules, and effective configuration. Components that do not change remain pinned. Candidate integration and final release distribution are separate evidence classes; a digest change requires new affected evidence.

A latest-compatibility run is an explicit maintenance action: resolve supported releases, prepare a new immutable profile, verify it, and then select it as the future default. It never modifies already accepted job inputs. Old profiles required by supported migrations and retained reproductions remain available.

## Suite execution

Each product owns its tests under a project path such as `tests/vm/`. The host-side Make target submits a bundle and waits; the guest entrypoint performs the actual product work. Common resource control stays in Haepari.

A clean-install scenario starts from a declared profile without the target product and verifies no accidental existing CLI, service, skill, or registration satisfies the test. Other stable dependencies may remain installed.

A migration scenario starts with the exact prior installation unit. It invokes prior-version public interfaces to create synthetic but meaningful settings and data, verifies the pre-upgrade behavior, preserves that state, installs the candidate through the intended upgrade path, and runs post-upgrade assertions. The starting and target identities are both retained. The candidate installer may not quietly erase the state that the scenario is meant to migrate.

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
9. Distinguish product downgrade/rollback from testbed restoration. Successful VM restore does not prove a product can downgrade its data.

For v0.1.0 adoption, the immediately preceding accepted profile to candidate is mandatory. Wider upgrade paths are mandatory only when advertised by the product. A first-release product records that no predecessor exists; it must not fabricate a migration pass. Its installation and forward state-persistence tests still apply, and the first subsequent supported upgrade becomes a real migration case.

## Result and restoration inventories

Record before, candidate, and restored installation inventories when applicable. Each inventory references the profile and recipe digests and contains only non-sensitive identity fields. Preserve needed logs on the host before resetting the VM. If evidence cannot be exported, record the gap and retain/quarantine the VM under an explicit inspection policy; never reset it and assert that missing evidence passed.

The queue's stored result includes workload outcome and cleanup status independently. A product can fail with a successfully restored environment; a product can appear to pass while the resource remains unusable. Only the latter resource's verified recovery permits another job.
