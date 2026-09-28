# Design revision 3.2

Date: 2026-09-28. The product target remains Haepari v0.1.0, and every implementation task remains planned. This revision applies the owner's answers to the decisions Revision 3.1 left open.

**Advisory named locks ([ADR-0017](architecture-decision-records/0017-advisory-named-locks.md), [lock contract](contracts/locks.md)).**
- **Why.** Ordinary Mulgae reviews should queue in Haepari and run when granted, so two reviews no longer overlap on one provider account.
- **What they are.** Locks are FIFO grants with durable state and force release. A grant stays alive while its holder renews it within a liveness window (120 s by default), bounded by a maximum hold (6 h by default, covering current Mulgae configurations; set from the run deadline Mulgae's preflight reports). Startup and wake give live grants a fresh liveness window.
- **Where Mulgae acquires.** Mulgae acquires the lock once, at the review start its MCP server and CLI share. `use-mulgae` does not acquire separately.
- **What they are not.** They grant no execution permit and count toward no managed capacity; nested managed callers cannot acquire them.
- **Relation to deferred work.** ADR-0016 is amended accordingly. The R2 external lease stays deferred, and B1 and B2 do not apply to locks.
- **Plan and verification.** New HP-REQ-080 and HP-REQ-081, new TASK-035 in EPIC-002 (frozen with the managed core at F1), and new cases V59–V64.

**Development loop.**
- VM tests run in the development loop, with 10–30 jobs a day expected.
- The configuration contract adds a development-loop profile: two VM resources, a host guest limit of two, and a global limit of three.
- Results carry `timings`, including UTM step durations, and TASK-016 and TASK-027 record VM cycle phases and queue waits. Case V65 checks them.

**Other decisions.**
- The license is MIT.
- Gaori keeps local tests; Haepari's UTM use is VM end-to-end testing.

**External requests.** [External requests](to-external.md) §2.1 now asks Mulgae to queue reviews on the lock and keeps the account slots and retry as optional safeguards. The Aquarium TODO wording follows the lock decision.

**Totals.**

| Item | Total | Active |
| --- | --- | --- |
| Requirements | 81 | 64 |
| Tasks | 35 | 31 |
| Cases | 85 | 65 |
| ADRs | 17 | — |

**Review.** An independent review of the draft (R-E in [review disposition](review-disposition.md)) returned REVISE. Its findings were fixed before completion:

- rev3.1 wording that contradicted the lock
- hold-time arithmetic against Mulgae's run deadline
- unclaimed grants
- replay exits
- double acquisition
- resource-command effects
- the freeze gate
- `timings` detail

**Scope.** No other repository was changed. No code, experiment, installation, commit or publication was performed.

# Design revision 3.1

Date: 2026-09-28. Product target remains Haepari v0.1.0. This is a documentation revision that keeps the Revision 3 managed-first scope. Every implementation task remains planned. It answers the re-review of Revision 3; the [review disposition](review-disposition.md) lists every finding from both reviews with its outcome.

## Roadmap

- **Dependencies.** The roadmap's actual prerequisites now form a checked DAG. Every active task except TASK-028 gates a later task.
- **Real-UTM gates.** Real UTM evidence is confined to TASK-034, TASK-016 and TASK-027, plus TASK-028 after them. Local hardening no longer waits for a VM.
- **Freeze gates.** TASK-002 stays provisional. The consumer contract freezes at gate F1 (managed core, TASK-012), gate F2 (VM fields, TASK-016) and gate F3 (consumer integration, TASK-020). This removes the completion cycle between TASK-002 and TASK-033. TASK-020 now follows TASK-019, and packaging (TASK-025) follows F3.
- **TASK-004** records the macOS service and durability decisions. **TASK-011** installs the development LaunchAgent that TASK-016 runs under.
- **New TASK-034** probes UTM automation non-destructively as soon as access exists.
- **Totals:** 34 tasks (30 active).

## Contracts

- **Transition table.** It adds rows for:
  - cancellation and deadlines during preparation
  - resumed cleanup and settlement
  - termination-proof failure
  - queued input corruption
  - collection deadlines
- **Capacity and state rules.** The table also:
  - corrects when capacity returns
  - defines resource availability transitions
  - makes `resource recover` wait for job recovery
- **Rejections** no longer consume request keys.
- **Settlement** never clears `paused` or `quarantined`. A host VM-limit error or a lost session pauses the VM resources, so queued VM jobs wait instead of failing one after another.
- **Nested acquisition** uses a context chain and also rejects blocking observation of non-terminal jobs (`wait`, `run` and followed events or logs).
- **CLI contract.** It maps error codes to exits, adds a protocol handshake, and deduplicates on the submitted request digest so that a replay survives alias moves.
- **Resource contract.** It binds each baseline to one resource and refuses colliding MAC, machine and host-key identities. It adds the host macOS guest limit and defines the receipt file protocol. The guest runner now travels with each job.
- **ADR-0004** is amended: deduplication uses the submitted request.
- **New configuration contract.** [configuration.md](contracts/configuration.md) now owns `haepari.config/v1`. It adds `host_macos_guest_limit`, default deadlines, limits and queue-deadline guidance.

## Platform decisions

The following are now fixed as design decisions:

- LaunchAgents run in `gui/<uid>` with `LimitLoadToSessionType=Aqua` and `AbandonProcessGroup=true`.
- Runners run in their own session.
- darwin SQLite uses `fullfsync` and `checkpoint_fullfsync`.
- Service builds use a stable signing identity.

Their observation remains TASK-004, TASK-034 and TASK-016 work.

## External requests and records

- **[External requests](to-external.md)** replaces the in-package follow-up lists. It covers:
  - the Aquarium baseline commit and Aquarium:EPIC-019 alignment, with exact locations
  - the inspector warning
  - advisory rollout, retirement and pilot-metric proposals
  - an independent Mulgae rate-limit request with verified evidence
- **Records.** Requirements mark every active requirement as mandatory. Verification adds V43–V58, and traceability maps every active case. ADR-0016 records the alternatives it rejected. The ADR index lists ADR-0014 through ADR-0016.
- **Validation.** An independent review of the draft found issues that were fixed before this revision was completed; see [review disposition](review-disposition.md) (R-C).

## Correction

Revision 3 stated that the review report was not present. A copy existed outside the inspected repository; Revision 3.1 used the full report.

## Scope

No Aquarium, Mulgae or other repository was changed. No code, platform experiment, VM operation, provider call, installation, commit or publication was performed.

# Design revision 3

Date: 2026-09-28. Product target remains Haepari v0.1.0. This is a documentation revision; every implementation task remains planned.

**Release scope.** Managed local and UTM macOS execution, durable admission, capacity fencing, manual sessions, collection, restoration and exact Aquarium handoff.

**Outside this release gate:**
- public external leases and linked continuation
- ordinary Mulgae review admission
- provider/account limits
- Linux runtime qualification
- an actual upgrade from a nonexistent public predecessor

ADR-0014 and ADR-0015 and their IDs remain as deferred design history with B1/B2 re-entry blockers.

**Roadmap changes.**
- The first epic now includes a production-path local walking skeleton.
- TASK-002 defines provisional schemas and invariants.
- TASK-004 owns general OS supervision qualification; UTM qualification remains TASK-016.
- TASK-010 owns common phase settlement while TASK-019 owns manual-session details.
- EPIC-008 and TASK-029–032 remain deferred without blocking TASK-013.
- Existing IDs are retained.

**Additions.** This revision adds the managed transition ledger, recovery and configuration contracts, active requirement/case traceability, and revised producer handoffs. Earlier R2 validation and Aquarium repository observations are historical only. No Aquarium or Mulgae repository was changed.
