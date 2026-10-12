# Deferred feedback

This document owns small follow-up checks intentionally postponed from current work. Entries do not allocate roadmap tasks or change lifecycle status; [the roadmap](roadmap.md) remains the sole lifecycle authority.

## DF-005: Shared diagnostic connection and lease-source policy

- **Disposition:** Small independent maintenance follow-up under the EPIC-005 delegated candidate; no current correctness or release obligation is deferred.
- **Owner:** Haepari maintainer, with TASK-017 owning the current selected-resource diagnostic behavior.
- **Reason:** Doctor deliberately uses a finite connection deadline and an actionable JSON prerequisite envelope, while existing CLI commands retain their current connection/error policy. All still use the same native client transport. Native DHCP selection in doctor, VM dispatch and guest attestation currently uses the same injected source or read-only no-follow file open. These small repeated policy boundaries agree today; changing only one later can cause drift.
- **Re-entry condition:** Before changing common CLI connection/context handling, the native DHCP path/open guards or shared Aqua/lean pinned-SSH diagnostic policy, consider one shared policy helper and qualify every caller. No new roadmap identity, loader policy, host effect or compatibility surface is adopted by this entry.
- **Verification if selected:** Preserve direct doctor usage/error/gap exits and actionable envelopes, existing CLI context errors and timeout semantics, no-follow lease reads, dispatch/attestation identity guards and no-effect diagnostic observations. Run affected CLI, UTM and controller fixtures; no live VM campaign is implied.

## DF-006: Diagnostic stage naming and residual coverage

- **Disposition:** Independent future maintenance; current doctor behavior and required observations are not deferred.
- **Owner:** Haepari maintainer.
- **Reason:** UTM prerequisite stage strings and controller messages currently agree. Future renaming can degrade a specific diagnostic to the safe generic fallback. The unused invalid-spec and rare temporary-storage/fallback paths merit coordinated coverage when their producers change.
- **Re-entry condition:** Before changing prerequisite stage producers, gap mappings or supported diagnostic paths, consider shared named constants and qualify every affected caller. Current status-read and lease-read failures have focused fixtures; source inspection does not claim execution of every generic fallback.
- **Verification if selected:** Preserve unchanged dispatch errors, actionable bounded gap messages, no private diagnostic disclosure and read-only ledger/power behavior. Use isolated unit/controller fault fixtures.

## DF-007: Reusing the trusted Make example

- **Disposition:** Independent future template hardening outside the current same-user caller scope; no current acceptance or security obligation is deferred.
- **Owner:** Haepari maintainer and any consumer adopting a broader invocation model.
- **Reason:** The small Make recipe interpolates caller-owned variables into shell text. The same user already owns the shell and recipes; this is not a multi-user or untrusted-CI guarantee.
- **Re-entry condition:** Before deriving Make variables from lower-trust or machine-supplied input, define the supported literal-value/escaping contract and consider passing values through exported environment variables. This entry adopts no CI service or new backend scope.
- **Verification if selected:** Exercise unusual literal path/variable characters as data without unintended shell evaluation, preserving the public CLI flow and original keys. Continue selecting the documented explicit absolute CLI path.

Master approved the original TASK-016 deferrals on 2026-10-05. On 2026-10-12, Design Revision 3.5 removed DF-001–003 from TASK-027 obligations and kept DF-004 optional under [ADR-0020](architecture-decision-records/0020-local-development-scope.md). All four are unobserved optional hardening, not passing evidence or v0.1.0 blockers. Their historical TASK-016 acceptance and uncertainty are unchanged.

## DF-001: Real DHCP lease-expiry rejection

- **Disposition:** Optional hardening; removed from v0.1.0 release obligations by Master on 2026-10-12. Original TASK-016 waiver/history is preserved.
- **Requirement owners:** [TASK-016](roadmap/EPIC-004.md#task-016), [lease validity and guest identity](contracts/resource-and-runner.md#host-session-and-vm-identity).
- **Reason:** A running guest renews its lease; deliberately producing an expired lease would require a separately approved VM networking procedure and a bounded observation window. The owner chose to perform this verification later.
- **Remaining uncertainty:** Rejection of an actually expired lease for the registered MAC has not been observed on the real backend. Renewal and pinned guest identity observations, and automated expiry coverage, do not prove that omitted real failure path.
- **Re-entry condition:** When explicitly expanding the qualified scope or investigating a concrete regression involving lease resolution or guest identity handling, arrange a bounded owner-approved expiry and restoration procedure for the designated VM. Do not edit the shared host lease file or substitute a synthetic fixture for real evidence.
- **Verification if selected:** Bind the installed candidate and registered VM identity; observe the individual lease expire; show that preparation refuses guest dispatch without another address-discovery path and safely returns capacity. Restore networking, re-observe a valid lease and pinned guest identity, and verify revision-bound resource resume and a subsequent successful Job.
- **Owner:** Haepari maintainer, with the operator approving VM network effects and restoration.

## DF-002: Host logout and post-login recovery

- **Disposition:** Optional hardening; removed from v0.1.0 release obligations by Master on 2026-10-12. Original TASK-016 waiver/history is preserved.
- **Requirement owners:** [TASK-016](roadmap/EPIC-004.md#task-016), [TASK-034 prerequisites](records/TASK-034-utm-probe.md#task-016-prerequisites-not-observed-here), [host-session handling](contracts/resource-and-runner.md#host-session-and-vm-identity).
- **Reason:** Logging out closes the owner's other applications. The owner chose to reserve this check for a later coordinated window. Screen-lock qualification is separate and does not substitute for logout.
- **Remaining uncertainty:** Actual Aqua-session loss during logout, logged-out UTM availability, and recovery after a new login have not been qualified on the real backend. An isolated controller restart is not an actual logout.
- **Re-entry condition:** When explicitly expanding the qualified scope or investigating a concrete regression involving the LaunchAgent or host-session recovery boundary, arrange an owner-coordinated logout and same-account login window. Disclose any UTM shutdown prompt and obtain the applicable authority before VM power effects; do not force-close applications or discard unsaved work.
- **Verification if selected:** Bind the installed candidate, native host session, LaunchAgent and VM identity; observe actual logout and the resulting service/control availability; confirm safe handling of uncertain work and cleanup. After login, verify the new native session and exact service candidate, re-establish guest identity, recover only original uncertain executions, and verify a subsequent complete VM Job without duplicate execution.
- **Owner:** Haepari maintainer, with the operator controlling logout and login.

## DF-003: Multi-day DHCP lease currency

- **Disposition:** Optional hardening; removed from v0.1.0 release obligations by Master on 2026-10-12. Original TASK-016 waiver/history is preserved.
- **Requirement owners:** [TASK-016](roadmap/EPIC-004.md#task-016), [TASK-034 prerequisites](records/TASK-034-utm-probe.md#task-016-prerequisites-not-observed-here), [lease validity and guest identity](contracts/resource-and-runner.md#host-session-and-vm-identity).
- **Reason:** The current bounded campaign observed renewal and authenticated identity on the same guest boot over approximately two hours. Continuous operation for days was not demonstrated; the owner chose to reserve the longer observation window for later verification.
- **Remaining uncertainty:** Currency of the registered MAC's individual DHCP lease after days of uninterrupted VM operation remains unobserved. The shorter renewal campaign and automated renewed-lease tests do not prove this prerequisite. This is separate from DF-001's expired-lease rejection.
- **Re-entry condition:** When explicitly expanding the qualified scope or investigating a concrete regression involving lease selection or expiry/identity validation, arrange an owner-approved multi-day observation of the designated persistent VM. Record the start and end native guest boot/uptime, registered UUID/MAC, and actual lease renewal; restarting the VM resets the continuous observation window.
- **Verification if selected:** Bind the installed candidate and registered VM; prove the same native boot across the multi-day window, a unique individually unexpired registered-MAC lease with observed renewal, pinned SSH and current guest MAC/platform/boot identity, and no alternate address-discovery path. Complete a bounded VM Job with verified collection/cleanup and a subsequent successful Job. Preserve uncertainty and block dispatch if lease or identity proof is unavailable.
- **Owner:** Haepari maintainer, with the operator authorizing the long-running VM window.

## DF-004: Two-VM and local concurrency

- **Disposition:** Deferred by Master on 2026-10-10 to a separate later campaign; excluded from TASK-038, TASK-039 and EPIC-009 acceptance. Unobserved, not passed.
- **Requirement owners:** [TASK-038 qualification scope](roadmap/EPIC-009.md#task-038), [TASK-037 startup limits](contracts/configuration.md#task-037-startup-limits), V08, V09, V41 and V49. Implemented concurrency controls and accepted fixture coverage remain intact.
- **Reason:** Master chose to qualify the corrected operating loop by reusing one existing registered VM and to verify two VMs later.
- **Remaining uncertainty:** Independent real progress of two VM Jobs and local work at global limit three and host guest limit two remains unobserved. Sequential reuse of one VM and fixture results do not prove this profile.
- **Policy consistency follow-up:** The CLI, controller startup and v2 development-service plan each validate the same execution-limit bounds. They agree and have fixture coverage today. When selecting this campaign or changing those bounds, review their shared policy ownership and verify that all three boundaries still accept and refuse the same limits; separate predicates can drift during a later policy change. This does not authorize raising the current ceiling.
- **Re-entry condition:** Arrange a separately approved campaign when two distinct authorized registered VM resources and an independent local resource are available. Disclose the exact candidate/service plan, resource identities, finite budgets, effects and restoration procedure before execution. No DF-001–004 entry has a TASK-027 deadline after Design Revision 3.5.
- **Verification if selected:** Bind the installed source/executable/runner identities and both UUID/MAC/pin mappings; run two VM Jobs plus independent local work at global limit three and host guest limit two. Observe one slot per VM, independent progress, stale-return refusal, once-only settlement and reuse without lost capacity. Preserve unresolved occupancy and evidence. This is not a three-VM framework-limit probe.
- **Owner:** Haepari maintainer, with the operator authorizing resources, service and VM effects and restoration.
