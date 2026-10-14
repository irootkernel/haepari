# Deferred feedback

This document owns small follow-up checks intentionally postponed from current work. Entries do not allocate roadmap tasks or change lifecycle status; [the roadmap](roadmap.md) remains the sole lifecycle authority.

## DF-005: Shared diagnostic connection and lease-source policy

- **Disposition:** Small independent maintenance follow-up under the EPIC-005 delegated candidate; no current correctness or release obligation is deferred.
- **Owner:** Haepari maintainer, with TASK-017 owning the current selected-resource diagnostic behavior.
- **Reason:** Doctor deliberately uses a finite connection deadline and an actionable JSON prerequisite envelope, while existing CLI commands retain their current connection/error policy. All still use the same native client transport. Native DHCP selection in doctor, VM dispatch and guest attestation currently uses the same injected source or read-only no-follow file open. The doctor CLI, controller and pinned-SSH probes also use separate finite caps of 20, 15 and 15 seconds. Each child inherits the earlier parent deadline; the current caps and cancellation fixtures agree. These repeated policy boundaries agree today; changing only one later can cause drift.
- **Re-entry condition:** Before changing diagnostic deadline values or common CLI connection/context handling, the native DHCP path/open guards or shared Aqua/lean pinned-SSH diagnostic policy, consider one shared policy helper and qualify every caller. No new roadmap identity, loader policy, host effect or compatibility surface is adopted by this entry.
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

## DF-008: Manual preparation step vocabulary

- **Disposition:** Small independent future maintenance under the EPIC-005 delegated candidate; no current acceptance or lifecycle obligation is deferred.
- **Owner:** Haepari maintainer, with TASK-019 owning the current manual preparation observation contract.
- **Reason:** The completed local-phase and explicit manual-observation validators both accept only `vm_start`, `ssh_ready` and `transfer`, at most three unique steps with nonnegative durations. They agree today. Their separate phase, backend, identity and replay guards remain intentional; a later vocabulary or bound change can make the shared part drift.
- **Re-entry condition:** Before changing the preparation-step vocabulary, count or duration rules, consider one shared validator for those rules and qualify both callers. Preserve their distinct lifecycle fences; this entry adopts no public schema, deadline renewal or workload effect.
- **Verification if selected:** Preserve original preparation and await deadlines, exact replay and stale/conflicting observation refusal, and durable retirement before owned collection or cleanup. Run the affected phase and manual-ledger fixtures.

## DF-009: Wait and saved-submission recovery guidance

- **Disposition:** Small independent future guidance follow-up under the EPIC-005 delegated candidate; no current identity, admission or verdict obligation is deferred.
- **Owner:** Haepari maintainer, with TASK-006 owning admission and replay semantics, TASK-008 owning bounded wait and TASK-017 owning the project wrapper's saved-request recovery guidance.
- **Reason:** A wait timeout ends observation with exit 5 while the same Job continues; the contract already directs callers to wait again using its printed ID, and the wrapper retains a PENDING observation. The short error could state that continuation and connection-failure distinction more directly. The wrapper also preserves its original key after a refusal; its CONFLICT guidance could distinguish a definitive pre-admission profile/digest refusal from uncertain or already-bound admission. Current refusal cannot create aggregate PASS or silently replace a Job.
- **Re-entry condition:** Before changing wait error wording, caller timeout defaults or saved-submission refusal/reconnect guidance, consider explicit same-ID continuation and definitive-refusal explanations. Keep the original run for inspection; uncertain or committed admission must retain its original spec and key. Do not infer permission to resubmit with a new key.
- **Verification if selected:** Preserve exit 5/PENDING, exit 6 uncertainty, terminal verdicts, saved spec/key/Job identity and exact replay. Exercise current wait-again and pre-admission versus uncertain-response paths without duplicate workload execution or automatic cancellation.

## DF-010: Remaining project aggregate verdict cases

- **Disposition:** Small independent future regression coverage; no current required observation or truthful-reporting obligation is deferred.
- **Owner:** Haepari maintainer, with TASK-017 owning the project caller's aggregate projection.
- **Reason:** The wrapper currently maps INCOMPLETE to exit 3 and CANCELLED to exit 4 and refuses a verdict/exit mismatch. Project-caller assertions cover PASS, FAIL, PENDING and UNKNOWN; native and public CLI manual assertions cover INCOMPLETE and CANCELLED. A future change to the wrapper map could drift without an explicit project aggregate case for those last two verdicts.
- **Re-entry condition:** Before changing the wrapper verdict/exit map or extending its manual aggregate fixtures, consider one INCOMPLETE and one CANCELLED project-caller case. This entry does not weaken the current truthful aggregate or original-identity contract.
- **Verification if selected:** Assert aggregate status, exit 3 or 4, the original saved Job ID and absence of aggregate PASS, without another workload admission or automatic cancellation.

## DF-011: Manual evidence labels after retained reboot recovery

- **Disposition:** Small independent future regression coverage; current manual evidence projection and optional reboot qualification remain separate.
- **Owner:** Haepari maintainer, with TASK-019 owning manual observation projection and TASK-036 owning operational recovery.
- **Reason:** Reboot-retained collection currently passes its existing `guest_reboot_evidence_retained` gap through `manualCollectionPhase`, which adds the applicable `manual_observation_unobserved` gap without erasing physical evidence. Ordinary manual physical-evidence fixtures cover that distinction, while the reboot-retained fixtures use automatic Jobs. A future edit of this recovery hook could lose the manual label without changing the existing automatic cases.
- **Re-entry condition:** Before changing retained reboot collection or selecting a focused manual recovery fixture, consider an isolated fake-native manual case. A real VM reboot campaign remains optional under ADR-0020 and needs its own applicable operating authority; this entry creates no release gate or VM effect.
- **Verification if selected:** Preserve both applicable evidence gaps, a verdict other than PASS, the original Job/run identities and quarantine, and no second guest execution. Keep original deadlines and safe cleanup guards.

## DF-012: First-submit inventory-mismatch assertion

- **Disposition:** Small independent future regression coverage; the current pre-admission comparison and no-key-consumption rule remain implemented and qualified.
- **Owner:** Haepari maintainer, with TASK-006 owning durable admission and input identity.
- **Reason:** The controller currently compares the newly sealed tree with the declared inventory digest before creating a Job ID or admitting the key. Existing fixtures cover changed survivors, sealing changes, error mapping and other pre-admission failures, while their ordinary submit inputs use matching digests. A focused new-key mismatch assertion would make a future change to this separate comparison easier to detect.
- **Re-entry condition:** Before changing the first-submit inventory comparison or its error/admission ordering, consider one otherwise valid Unix submit with a different 64-character inventory digest. This entry does not defer a current code correction, required operating-loop observation or documentation check.
- **Verification if selected:** Expect public CONFLICT, no admission row for the key and no workload execution. Preserve the separate invalid first-input, changed-survivor and corrupt-retained-inventory classifications.

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
