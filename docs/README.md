# Haepari v0.1.0 development package

Status: implementation plan. No software, installed service, accepted test result, or published release is supplied by this package.

Haepari is an independent Go CLI and local daemon for durable resource scheduling. Managed execution covers local commands and UTM macOS VM tests through one durable controller, capacity ledger, and fenced safe-return path. Cooperating tools, such as Mulgae reviews, can queue on advisory named locks. Public external leases are deferred. Aquarium is the first planned consumer; Haepari has no runtime dependency on Aquarium or its tools.

Design revision: 3.2, dated 2026-09-28. Revision 3 adopted a managed-first scope; external leases and linked segments remain preserved deferred designs. Revision 3.1 corrected roadmap prerequisites, completed the managed transition and configuration contracts, fixed the macOS service and durability decisions, and moved the changes needed from Aquarium and Mulgae into [external requests](to-external.md). Revision 3.2 records the owner's decisions: advisory named locks for Mulgae reviews, VM tests in the development loop, and the MIT license. [Revision notes](REVISION-NOTES.md) and [review disposition](review-disposition.md) describe the changes. The product target remains v0.1.0.

## Read in this order

1. [Requirements](requirements.md) defines the release scope and observable acceptance criteria.
2. [Architecture](architecture.md) defines ownership, persistence, scheduling, and recovery.
3. [Architecture decision records](architecture-decision-records/README.md) records the selected trade-offs.
4. [Roadmap](roadmap.md) orders seven active epics and 31 active implementation tasks (35 in total); one epic and four tasks are deferred, with stable existing IDs.
5. [CLI and job contract](contracts/cli-and-jobs.md), [resource and runner contract](contracts/resource-and-runner.md), [artifact and environment contract](contracts/artifact-and-profile.md), [configuration contract](contracts/configuration.md) and [lock contract](contracts/locks.md) define the supported boundary; the [external lease contract](contracts/external-leases.md) is deferred. The [managed transition table](contracts/managed-transitions.md) defines lifecycle and capacity changes.
6. [Verification](verification.md), [traceability](traceability.md), and [operations](operations.md) define test evidence and operator procedures.
7. [Aquarium adoption plan](aquarium-adoption.md) describes the separate consumer rollout. [External requests](to-external.md) lists the changes Haepari needs from Aquarium and Mulgae. [Handoffs](handoffs/README.md) provides development and producer request templates.

## Settled decisions

- Product and executable name: Haepari / `haepari`. Initial target: v0.1.0.
- Go core, CLI, transient runner, and local daemon; SQLite on a local filesystem.
- One controller per OS user and Haepari state root, with a private Unix socket. No public HTTP service.
- One named resource per managed Job. Different resources may run concurrently under one global occupancy limit; a UTM resource has capacity one.
- UTM uses managed execution for VM end-to-end tests (`make test-vm`), which run in the development loop; the owner expects 10–30 VM jobs a day. Local tests stay with each project and Gaori.
- Ordinary Mulgae reviews queue on the advisory lock `mulgae-review` and run when granted (ADR-0017). Locks grant no execution permit and count toward no managed capacity.
- License: MIT. Go module path: `github.com/irootkernel/haepari`.
- Linked external continuation is a deferred design with unresolved safety blockers.
- Managed admission, reservation, execution, collection, restoration and return are distinct. Replay never launches a second uncertain workload.
- Project repositories own tests, installation recipes, migration fixtures, and hands-on checklists. There is no mandatory integration-test repository.
- `make test-vm` submits and awaits a job; guest execution uses a different entrypoint. UTM control belongs to the common adapter.
- A fixed, validated environment combination is the baseline. Admission resolves aliases to immutable identities. Jobs never resolve moving `latest` references.
- Candidate deployment covers the declared installation unit, including binaries, skills, plugin resources, and connection settings. Binary and skill identities may differ where their native contract requires it.
- A migration scenario creates state with the old version and preserves it while applying the candidate. VM reset happens after the scenario, not between old-state creation and migration assertions.
- Haepari schedules and records manual verification but does not infer a human pass or act as an LLM evaluator.
- Existing `aquarium-dev` state is retired through a staged consumer rollout, never by deleting its root before dependents are detached.
- The consumer contract freezes at gate F1 (managed core, TASK-012), gate F2 (VM fields, TASK-016) and gate F3 (consumer integration, TASK-020); TASK-002 is provisional.
- The macOS controller runs as a `gui/<uid>` LaunchAgent in the Aqua session with `AbandonProcessGroup=true`; runners use their own session; darwin SQLite uses `fullfsync` and `checkpoint_fullfsync`; service builds use a stable signing identity.
- Each UTM baseline is bound to one resource, the guest runner travels with each job, and `host_macos_guest_limit` bounds Haepari-managed macOS guests.

## Document authority

Requirements own product obligations. The architecture and active managed contracts own implementation semantics. ADRs own rationale. The roadmap alone owns Haepari task identity, sequence, and lifecycle status. Task checkboxes are implementation acceptance evidence, not independent lifecycle records.

The Aquarium repository's `docs/roadmap/README.md` owns Aquarium IDs and status. The adoption copy in this package is a handoff aid, not a second roadmap authority. The planned Aquarium epic is Aquarium:EPIC-019, with Aquarium:TASK-086 through Aquarium:TASK-102. Inside this package an unqualified EPIC or TASK ID is a Haepari ID; an ID from another repository is always qualified, as in Aquarium:TASK-092. [External requests](to-external.md) lists requested changes to other repositories; it is not their authority.

Examples are illustrative design fixtures. They are not working scripts or production-ready configuration. Placeholder identities must be rejected by the implemented validators. No real credentials or current private runtime data are included.

External facts are identified in [sources](sources.md). All architecture choices and acceptance criteria are proposed product contracts, not claims that existing software already implements them. The package preserves the user's settled direction; any later material scope change needs a recorded decision.

Documentation check results are recorded in [package validation](package-validation.md) and reproducible with `docs/tools/validate_docs.py`. The Aquarium-owned inspector warning is tracked in [external requests](to-external.md) §1.3.

The [Mulgae integration contract](integrations/mulgae-review.md) defines the managed VM adoption boundary and how ordinary reviews queue on the advisory lock. It is planned downstream behavior, not a Haepari dependency or a claim about the installed Mulgae release.
