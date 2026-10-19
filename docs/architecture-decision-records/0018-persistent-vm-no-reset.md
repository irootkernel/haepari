# ADR-0018: One persistent VM per resource, without per-job reset

Status: Accepted for v0.1.0 design (Design Revision 3.3); implementation pending. Amends [ADR-0006](0006-utm-ssh.md).

## Context

**Earlier design.** Revisions up to 3.2 ran every VM job on a working copy restored from a powered-off baseline, so each job started from the same state.

**What the owner does.** The owner uses each macOS VM continuously for end-to-end tests and relies on the queue to take turns. A fresh copy per job is not wanted.

**What the TASK-034 probe showed** ([record](../records/TASK-034-utm-probe.md)):
- `utmctl` works from a LaunchAgent without a signing identity or an Automation prompt.
- A UTM clone keeps its source's MAC address.
- UTM's data container cannot be read from a terminal or from a LaunchAgent.

## Decision

- **Resource.** A `utm-macos` resource is one persistent, operator-owned UTM VM. It is named by its UTM UUID, MAC address and pinned SSH host key.
- **Job cycle.** The operator starts and stops the persistent VM. Haepari runs one Job at a time in an already-running registered VM, stops owned Job processes, collects evidence and removes only its owned Job directory when required evidence is no longer protected. It leaves the VM running or the resource quarantined as appropriate.
- **Host guest limit.** `host_macos_guest_limit` and `vm.allow_automatic_stop` remain accepted legacy configuration fields. They grant no power-control authority. Managed global/per-resource occupancy and historical unresolved power claims remain fenced; an unstarted selected VM stays queued with `vm_not_started`.
- **No reset.** Haepari never clones, restores, resets, deletes or edits the VM. Changes a workload makes elsewhere in the guest persist. Consumer recipes set up their own starting state and clean up after themselves.
- **Control.** Haepari uses `utmctl list` and `status` for identity and readiness observation from the Aqua LaunchAgent. The product issues no VM start, stop, clone or delete command. It uses neither AppleScript nor UTM’s data container.
- **Quarantine.** If Haepari cannot confirm that its own processes stopped, or must retain required evidence instead of removing job files, it quarantines the resource. An operator then inspects the VM and uses the documented recovery path.

## Design Revision 3.4 clarification

Historical clarification (superseded by the operator-only power decision above): The capacity stop described above is conditional on operator-owned `vm.allow_automatic_stop`; it is disabled by default because an empty Haepari Job slot does not show whether persistent guest services or maintenance can be interrupted. Pausing protects maintenance. The controller durably reserves and reconciles victim stop and target start claims before power effects, and a target remains queued if no eligible victim exists. This clarification does not change the persistent-VM or no-reset decision. Required uncollected evidence keeps the VM quarantined until re-collection or explicitly authorized disposal and verified cleanup.

## Alternatives considered

**A working copy per job from a powered-off baseline (Revision 3.2).** Rejected. It would add baseline registration, clone and delete recovery, identity-collision rules and a restore phase to every job. The owner does not want per-job reset.

**UTM snapshots or a disposable start (UTM 5 beta).** Not adopted. Both are beta features, and they would bring back reset semantics that nobody has asked for. A later reset strategy needs its own decision.

**Stopping the VM after every job.** Rejected. It adds boot time to every job and gains no isolation, because the disk keeps its changes either way.

## Consequences and revisit condition

- **Tests are not isolated from earlier tests.** A clean-install test must first remove earlier installations itself. A failed test can leave state behind that affects the next one. Results report what the consumer's inventory observed; Haepari does not claim a clean machine.
- **Removed from the design.** Baseline registration, `reset_strategy`, `PROFILE_MISMATCH` and the clone-identity rules. A job's profile digest is now recorded as consumer evidence only.
- **Backups are the operator's job.** The operator keeps any backup of the VM outside Haepari and restores it by hand if the VM breaks. A backup copy keeps the VM's MAC address, so it must never run alongside the resource VM; after a restore that changes the VM UUID, the operator updates the resource.
- **Revisit condition.** Revisit this decision if contamination between jobs causes false results often enough to justify a reset strategy.
