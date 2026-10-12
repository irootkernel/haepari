# TASK-018 generic opaque inputs

The [runnable caller](../../examples/project-test/README.md) uses its sibling
stdlib module to select and capture arbitrary non-secret regular files, compute
public inventory/subset digests, and construct existing Job v2 fields. Candidate
and test subsets include paths, content and executable bits. The profile reference
binds the exact consumer bytes, without JSON parsing or mandatory inventories.
The example profile declares only the purpose of the small offline loop.
Recipes explicitly declare their cwd, offline authorization and manual scenario
references. The reusable constructor validates recipe and identifier shapes,
refuses cwd paths without selected child files, and carries no project-layout
defaults. Selection compares case and Unicode normalization aliases without
renaming or interpreting payload bytes. Diagnostics name refused paths and
distinguish permanent invalid declarations from retained-key retry cases.

The immutable run retains captured bytes, exact specs and keys. Reconnect does
not read the current project policy or profile. Native admission remains the
sealing authority; a resource revision update cannot replace original admission
identity. No registry, latest resolution, host installation scan or F1/F2 schema
change is introduced.

Focused local and fake/native verification is reproducible from
[TESTING.md](../../TESTING.md). The skill-only flow executes a selected script and
exports the selected Markdown bytes across multiple 64 KiB capture/hash chunks. Independent digest encoding checks the
profile, full payload and candidate/test subsets. Negative cases reject missing,
unsafe, credential, colliding and corrupt inputs; restoring a corrupt bundle
reuses the original spec/key. Policy/recipe negatives also cover missing input
policy, non-selected/empty groups, invalid profile references/IDs, count/path
bounds, malformed environment/argv/outputs and escaping or absent cwd paths.
Additional local checks cover invalid manual recipes before admission, malformed
project JSON, permanent-request guidance, intermediate-directory links, valid
generated credential-named outputs, invalid UTF-8 path declarations and field
labels for missing groups. Generated output names use path-shape validation;
credential-name screening applies to selected inputs. Standalone capture callers
provide a trusted private bundle parent, as the wrapper does. The existing queued-source fixture and fake/native
resource-update flow preserve original sealed content and admission metadata.
Native check results and exact candidate identities remain private workflow
evidence. Lifecycle acceptance belongs only to [the roadmap](../roadmap.md).

No live VM, human manual observation, installed service, host installation,
provider or release qualification is claimed. TASK-019 supplies manual execution;
TASK-020 combines the generic loop and freezes F3. TASK-027 owns the exact release
candidate's ordinary running-VM observation.
