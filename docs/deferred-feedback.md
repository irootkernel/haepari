# Deferred feedback

This document owns small follow-up checks intentionally postponed from current work. Entries do not allocate roadmap tasks or change lifecycle status; [the roadmap](roadmap.md) remains the sole lifecycle authority.

The owner approved the two TASK-016 qualification deferrals below on 2026-10-05. They are waived for the current TASK-016 campaign so work can proceed, and must be verified later, before TASK-027 release acceptance. A deferred check is unobserved, not passed, and is not permanently removed from backend qualification.

## DF-001: Real DHCP lease-expiry rejection

- **Disposition:** Deferred; owner-approved waiver for the current TASK-016 campaign only.
- **Requirement owners:** [TASK-016](roadmap/EPIC-004.md#task-016), [lease validity and guest identity](contracts/resource-and-runner.md#host-session-and-vm-identity).
- **Reason:** A running guest renews its lease; deliberately producing an expired lease would require a separately approved VM networking procedure and a bounded observation window. The owner chose to perform this verification later.
- **Remaining uncertainty:** Rejection of an actually expired lease for the registered MAC has not been observed on the real backend. Renewal and pinned guest identity observations, and automated expiry coverage, do not prove that omitted real failure path.
- **Re-entry condition:** Before TASK-027 release acceptance, or earlier when changing lease resolution or guest identity handling, arrange a bounded owner-approved expiry and restoration procedure for the designated VM. Do not edit the shared host lease file or substitute a synthetic fixture for real evidence.
- **Required verification:** Bind the installed candidate and registered VM identity; observe the individual lease expire; show that preparation refuses guest dispatch without another address-discovery path and safely returns capacity. Restore networking, re-observe a valid lease and pinned guest identity, and verify revision-bound resource resume and a subsequent successful Job.
- **Owner:** Haepari maintainer, with the operator approving VM network effects and restoration.

## DF-002: Host logout and post-login recovery

- **Disposition:** Deferred; owner-approved waiver for the current TASK-016 campaign only.
- **Requirement owners:** [TASK-016](roadmap/EPIC-004.md#task-016), [TASK-034 prerequisites](records/TASK-034-utm-probe.md#task-016-prerequisites-not-observed-here), [host-session handling](contracts/resource-and-runner.md#host-session-and-vm-identity).
- **Reason:** Logging out closes the owner's other applications. The owner chose to reserve this check for a later coordinated window. Screen-lock qualification is separate and does not substitute for logout.
- **Remaining uncertainty:** Actual Aqua-session loss during logout, logged-out UTM availability, and recovery after a new login have not been qualified on the real backend. An isolated controller restart is not an actual logout.
- **Re-entry condition:** Before TASK-027 release acceptance, or earlier when changing the LaunchAgent or host-session recovery boundary, arrange an owner-coordinated logout and same-account login window. Disclose any UTM shutdown prompt and obtain the applicable authority before VM power effects; do not force-close applications or discard unsaved work.
- **Required verification:** Bind the installed candidate, native host session, LaunchAgent and VM identity; observe actual logout and the resulting service/control availability; confirm safe handling of uncertain work and cleanup. After login, verify the new native session and exact service candidate, re-establish guest identity, recover only original uncertain executions, and verify a subsequent complete VM Job without duplicate execution.
- **Owner:** Haepari maintainer, with the operator controlling logout and login.
