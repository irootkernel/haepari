# Package validation: Design Revision 3.5

Date: 2026-10-12. These are documentation-only checks, not product acceptance or new host/VM observations. [Revision notes](REVISION-NOTES.md) record the local-development scope change. Product evidence remains with its owning tasks and historical records.

The standard-library [`tools/validate_docs.py`](tools/validate_docs.py) checks the package. `python3 docs/tools/validate_docs.py --write` regenerates checks.json and MANIFEST.json; a second non-writing run verifies their contents. Offline regression checks run through `make test-qualification` alongside the existing campaign-driver fixtures.

## Current checks

| Check | Result |
| --- | --- |
| UTF-8, code fences and JSON syntax | Passed |
| Relative links and anchors (653 links, GitHub-style anchors) | Passed |
| Roadmap DAG and task titles | Passed; IDs, prerequisites and lifecycle statuses preserved |
| Requirement–task–case traceability | Passed; mandatory/consumer-owned/deferred scopes are distinct |
| Scope parser and invalid-reference regression tests | Passed through the offline qualification stage |
| Qualified cross-project IDs and deferred terms | Passed; lexical limits below apply |
| Stated counts | Passed; computed sets, not highest ID suffixes |
| git diff --check | Passed |
| MANIFEST.json | Regenerated; non-writing recheck verifies file sizes/hashes |
| Standalone JSON Schema validation | Not run; product Go decoders are separate |
| Product/VM tests and installed-service experiments | Not run by this documentation change |
| Git staging, commit and publication | Not performed |

**Roadmap DAG.** The graph is acyclic, no active task depends on a deferred task, and TASK-028 remains the only active sink. The longest prerequisite chain has 25 tasks. Historical real-UTM qualification (TASK-016/038) is distinct from the current live gate (TASK-027). The validator computes only the current gate's prerequisite closure: TASK-027 and its final-handoff successor TASK-028. TASK-017–026 can be implemented and accepted locally without a new VM campaign. Accepted evidence, task IDs, actual prerequisites and statuses remain unchanged.

**Traceability.** Every mandatory requirement has a mapped implementation owner and at least one mandatory case of its own; complete aggregate case coverage cannot hide an empty row. All 58 mandatory managed cases (V01–V24 and V32–V65) are referenced. Six consumer-owned requirements and seven consumer cases retain their IDs outside mandatory acceptance. Consumer-owned/deferred requirements cannot be owned by active Haepari tasks. Unclassified or duplicate IDs, duplicate traceability rows, missing requirement mappings and optional/deferred/unknown case references fail validation. Scope sections may be reordered and IDs need not be contiguous.

**Deferred terms.** EPIC-008, TASK-029–032, E01–E20, HP-REQ-063–079 and lease commands appear in active documents only inside deferred sections or on lines that mark them deferred. Consumer scopes are classified structurally; product behavior is not inferred from words.

**Counts.**

| Item | Total | Active | Consumer-owned optional | Deferred |
| --- | --- | --- | --- | --- |
| Requirements | 81 | 58 | 6 | 17 |
| Epics | 9 | 8 | 0 | 1 |
| Tasks | 39 | 35 | 0 | 4 |
| ADRs | 20 | 18 | 0 | 2 |
| Verification cases | 85 | 58 | 7 | 20 |

The package also holds 77 Markdown documents, 12 JSON design templates, nine unsent producer request drafts and one validation script. No runtime acceptance is inferred from these counts.

## Limits of these checks

Automated checks cover syntax, links, counts, declared scope/ownership, per-requirement case references and the historical/current DAG distinction. They do not interpret arbitrary release or approval prose. Semantic review separately compares those statements with the approved plan; its disposition is recorded in [review disposition](review-disposition.md#local-development-scope-agreement-design-revision-35). Historical observations remain unchanged, and optional skill/migration or DF-001–004 limitations do not become passing evidence. Go/runtime tests, actual VM/service observations, providers, external messages and the Aquarium inspector are outside this change. The broader Design Revision 3.2 comparison below remains historical.

## Design-package changes since Design Revision 3.2

This historical comparison is against commit `91a874d` (Design Revision 3.2). It records the Design Revision 3.3 and 3.4 documentation changes before the staged TASK-001 bootstrap; it does not inventory the current implementation candidate. Earlier revisions are summarized in [revision notes](REVISION-NOTES.md).

**Added (2):**

- `docs/architecture-decision-records/0018-persistent-vm-no-reset.md`
- `docs/records/TASK-034-utm-probe.md`

**Deleted (1):**

- `docs/examples/baseline-registration.template.json`

**Modified (52):**

- `docs/aquarium-adoption.md`
- `docs/architecture-decision-records/0005-cleanup-before-release.md`
- `docs/architecture-decision-records/0006-utm-ssh.md`
- `docs/architecture-decision-records/0007-project-owned-tests.md`
- `docs/architecture-decision-records/0008-installation-combinations.md`
- `docs/architecture-decision-records/0009-real-migration-state.md`
- `docs/architecture-decision-records/0012-isolation-and-effects.md`
- `docs/architecture-decision-records/0016-managed-first-scope.md`
- `docs/architecture-decision-records/README.md`
- `docs/architecture.md`
- `docs/checks.json`
- `docs/contracts/artifact-and-profile.md`
- `docs/contracts/cli-and-jobs.md`
- `docs/contracts/configuration.md`
- `docs/contracts/locks.md`
- `docs/contracts/managed-transitions.md`
- `docs/contracts/resource-and-runner.md`
- `docs/examples/README.md`
- `docs/examples/resource-lock.template.json`
- `docs/examples/resource-macos.template.json`
- `docs/handoffs/haepari-to-aquarium.md`
- `docs/handoffs/implementation-prompt.md`
- `docs/handoffs/producer-completion-template.md`
- `docs/integrations/mulgae-review.md`
- `docs/MANIFEST.json`
- `docs/operations.md`
- `docs/package-validation.md`
- `docs/README.md`
- `docs/requirements.md`
- `docs/review-disposition.md`
- `docs/REVISION-NOTES.md`
- `docs/roadmap.md`
- `docs/roadmap/EPIC-001.md`
- `docs/roadmap/EPIC-002.md`
- `docs/roadmap/EPIC-003.md`
- `docs/roadmap/EPIC-004.md`
- `docs/roadmap/EPIC-005.md`
- `docs/roadmap/EPIC-007.md`
- `docs/rollout-requests/01-podway.md`
- `docs/rollout-requests/02-mulgae.md`
- `docs/rollout-requests/03-gaori.md`
- `docs/rollout-requests/04-sorage.md`
- `docs/rollout-requests/05-sanho.md`
- `docs/rollout-requests/06-dolgorae.md`
- `docs/rollout-requests/07-gul.md`
- `docs/rollout-requests/08-seongge.md`
- `docs/rollout-requests/09-gaebokchi.md`
- `docs/sources.md`
- `docs/to-external.md`
- `docs/tools/validate_docs.py`
- `docs/traceability.md`
- `docs/verification.md`

## Remaining gaps

**Bounded host observations and remaining gaps.** TASK-004 observed the Aqua LaunchAgent keys, SQLite `fullfsync` driver setting/readback, socket path limit, and owned process-group behavior on one macOS 27.0 arm64 host; see [its decision record](records/TASK-004-macos-supervision.md). TASK-016 and TASK-038 retain accepted candidate-bound backend and operating observations, including the stated screen-lock/SSH/cleanup limits; TASK-034 retains its host probe. Actual DHCP expiry, logout/post-login recovery, multi-day lease currency and two-VM qualification remain unobserved optional DF-001–004 hardening, not release gaps. Power-loss durability and other macOS releases remain unqualified limits. TASK-027 still needs its exact packaged candidate's small running-VM loop; historical evidence does not supply that new observation.

**Owned by Mulgae.** The lock integration and the behavior when Haepari is unavailable are Mulgae decisions; see [external requests](to-external.md) §2.1.

**Consumer behavior** remains product-owned. Without a VM reset, each consumer recipe sets up and cleans up its own state.

**Not applicable yet.** Migration from a prior public Haepari release has no predecessor to test.

**Owner decisions** are recorded in [review disposition](review-disposition.md#owner-decisions-2026-09-28) and [its Revision 3.3 section](review-disposition.md#owner-decisions-2026-09-28-revision-33).

**Out of this revision's scope:**

- The Aquarium inspector warning was not rechecked; it is Aquarium-owned ([external requests](to-external.md) §1.3).
- No Aquarium, Mulgae or other repository was changed.
- No Sorage message was sent.
- No installation, commit, publication or release was performed.
