# Haepari requirements

Target: v0.1.0, Design Revision 3.5. Requirement IDs and historical acceptance evidence are preserved. Only requirements in the Requirements section are mandatory; checkboxes require reviewed evidence. Consumer-owned optional and deferred external requirements have separate owners and do not block release.

## Scope and ownership

The release supports general automatic and manual tests on one local macOS arm64 machine, one OS user and one already-running registered persistent UTM macOS VM. Haepari owns sealed inputs, scheduling, execution/observation, declared evidence collection, manual completion and safe Job cleanup. Consumer recipes own product installation, compatibility, skill discovery/behavior, migrations and their functional assertions. Existing VM power and cross-boot recovery implementation and deterministic fixtures remain; operating those optional paths is not required to develop or release this supported profile.

[ADR-0020](architecture-decision-records/0020-local-development-scope.md) records the scope, evidence and authorization decision. The Requirements section is mandatory; Consumer-owned optional requirements and Deferred external-execution requirements do not count toward v0.1.0. Unobserved optional scenarios are limits, never passing evidence.

## Requirements

### HP-REQ-001
- [ ] Ship an independent Go product named Haepari with CLI `haepari`, a local daemon, and a transient runner.
Acceptance: A release smoke test operates without Aquarium, Podway, Mulgae, Gaori, Sorage, or another scheduler installed.
Implementation owner: `TASK-001`.

### HP-REQ-002
- [ ] Support the local-command controller and UTM macOS adapter on qualified macOS arm64 hosts. Keep interfaces portable; Linux runtime and service qualification are deferred.
Acceptance: Build and execution evidence exists for the claimed macOS target. An unsupported backend/platform fails before mutation; cross-compilation alone is not execution proof.
Implementation owner: `TASK-001`.

### HP-REQ-003
- [ ] Use explicit versioned JSON input and output contracts with strict validation and stable symbolic errors.
Acceptance: Unknown fields, wrong types, unsafe paths, unsupported schema versions, duplicate identifiers, and unresolved placeholders are rejected without enqueueing.
Implementation owner: `TASK-002`.

### HP-REQ-004
- [ ] Use explicit, scoped immutable input bundles containing exact candidate and test bytes for managed Jobs.
Acceptance: Editing or deleting the source checkout after submission does not change a queued job; a missing or corrupt bundled file blocks execution, and the queued job finishes INCOMPLETE without a lease.
Implementation owner: `TASK-003`.

### HP-REQ-005
- [ ] Accept development snapshots from a dirty worktree or a feature branch without altering Git state.
Acceptance: Record the base commit when available plus the actual selected-file digest; release evidence requires the consumer-selected committed/released provenance class.
Implementation owner: `TASK-003`.

### HP-REQ-006
- [ ] Qualify general macOS process supervision and filesystem behavior early; qualify installed UTM automation in TASK-016 before promising VM support.
Acceptance: TASK-004 records the macOS supervision, service-manager and durability decisions with observations. TASK-034 recorded an early UTM probe ([probe record](records/TASK-034-utm-probe.md)). TASK-016 records exact UTM/backend/macOS versions and a real job cycle on the persistent VM. Missing UTM conditions block VM acceptance, not independent local-core development.
Implementation owner: `TASK-004` (macOS decisions) and `TASK-034` (UTM probe).

### HP-REQ-007
- [ ] Persist accepted jobs, immutable request identity, lifecycle events, resource leases, and results in a local SQLite database.
Acceptance: Kill and restart the daemon around each transaction boundary; committed admission is never lost and uncommitted admission is never reported accepted.
Implementation owner: `TASK-005`.

### HP-REQ-008
- [ ] Allow one controller writer per state root, protected by an OS lock as well as database transactions.
Acceptance: Two daemon starts cannot both dispatch. A stale socket is removed only after exclusive ownership is established; a live competing controller is not killed.
Implementation owner: `TASK-005`.

### HP-REQ-009
- [ ] Acknowledge managed submission only after input sealing and durable admission complete.
Acceptance: A lost reply can be recovered with the same request key; the returned job ID and input digest are unchanged. A rejected request does not consume its key.
Implementation owner: `TASK-006`.

### HP-REQ-010
- [ ] Give admission keys durable deduplication semantics, including after result purging.
Acceptance: Same key and same canonical request yields the original admission or a retained unavailable-result tombstone; changed content yields conflict. Neither case launches a second job.
Implementation owner: `TASK-006`.

### HP-REQ-011
- [ ] Schedule FIFO within each resource and enforce resource capacity plus a controller-wide occupancy limit across reserved, active and unresolved managed Jobs.
Acceptance: Competing submitters cannot exceed managed or host VM capacity, including concurrent starts and uncertain power effects. A paused or unavailable resource does not prevent another eligible resource from progressing; a paused VM is not an automatic-stop victim. A caller whose parent context occupies capacity cannot submit a job or block-wait on a non-terminal job; nested acquisition is rejected before admission.
Implementation owner: `TASK-007`.

### HP-REQ-012
- [ ] Hold a managed lease through preparation, execution, manual use, collection, and cleanup.
Acceptance: A second job cannot install or execute while the first job is cleaning or awaiting a human. A clock expiry never grants the next lease by itself.
Implementation owner: `TASK-007`.

### HP-REQ-013
- [ ] Fence every resource mutation with the resource revision, job identity, and lease generation.
Acceptance: A delayed old runner response or stale recovery request cannot release, replace, or act on a resource owned by a later lease.
Implementation owner: `TASK-007`.

### HP-REQ-014
- [ ] Provide durable submission, paged job/resource listing, inspection, event/log reading, blocking wait, and explicit cancellation.
Acceptance: A caller waits on one job without repeated status polling; caller disconnect or wait deadline does not cancel or resubmit the job.
Implementation owner: `TASK-008`.

### HP-REQ-015
- [ ] Separate operation success, workload outcome, cleanup outcome, and final verdict.
Acceptance: The common final-verdict decision table makes missing required evidence or failed cleanup INCOMPLETE even when a workload failure is known; the failure fact is retained. Inspection itself can succeed while reporting a failed job.
Implementation owner: `TASK-008`.

### HP-REQ-016
- [ ] Run local workloads under a per-job supervisor with bounded process-group termination and durable start/exit receipts.
Acceptance: SIGTERM, forced stop, inherited-pipe hang, and caller disconnection are tested. Escaped/unidentifiable descendants cause uncertainty and resource quarantine.
Implementation owner: `TASK-009`.

### HP-REQ-017
- [ ] Keep commands as argv arrays and explicit environments rather than implicit shell strings.
Acceptance: A value containing spaces, quotes, or shell metacharacters remains one argument. Shell execution requires an explicit shell executable and arguments.
Implementation owner: `TASK-009`.

### HP-REQ-018
- [ ] Always attempt bounded collection and cleanup after any effectful preparation or workload failure.
Acceptance: Preparation failure, workload failure, cancellation, and collection failure each reach verified release or explicit quarantine; no path silently frees a resource whose Haepari processes are unconfirmed or deletes required uncollected evidence. Proven termination may settle INCOMPLETE while retained evidence keeps the resource quarantined.
Implementation owner: `TASK-010`.

### HP-REQ-019
- [ ] Keep workload and phase deadlines distinct, configurable, and observable.
Acceptance: A manual inspection is not killed by a short CLI transport deadline. Timed-out cleanup quarantines rather than claiming resource readiness.
Implementation owner: `TASK-010`.

### HP-REQ-020
- [ ] Reconcile interrupted jobs before dispatch after controller restart.
Acceptance: Existing supervisors and receipts are reattached or classified inconclusive. An ambiguous start is never automatically replayed.
Implementation owner: `TASK-011`.

### HP-REQ-021
- [ ] Do not claim exactly-once effects of a managed workload.
Acceptance: Tests cover exit-receipt loss and crash after spawn but before acknowledgement. Results explain uncertainty and preserve the admission without a speculative rerun.
Implementation owner: `TASK-011`.

### HP-REQ-022
- [ ] Collect bounded logs and an artifact manifest to the host before job cleanup.
Acceptance: Every collected artifact has a path, size, and digest. Disk-full, truncated collection, and missing required files produce evidence gaps, not successful empty results. Protected guest evidence survives failed collection and restart until bounded re-collection or explicitly authorized disposal and verified cleanup.
Implementation owner: `TASK-012`.

### HP-REQ-023
- [ ] Support explicit completed-job retention and purge without deleting active resources or deduplication tombstones.
Acceptance: Purging selected finished jobs removes only unprotected owned payloads and logs; unresolved evidence/recovery references and request-key tombstones survive. A repeated old key cannot become a fresh execution.
Implementation owner: `TASK-012`.

### HP-REQ-024
- [ ] Identify a UTM resource by verified backend, VM UUID, MAC address and pinned SSH host key.
Acceptance: Duplicate display names cannot select the wrong VM. An unknown UUID, a UUID or MAC address already registered to another resource, or a changed registration is refused before any VM control. Expired or ambiguous DHCP lease entries fail even if written after VM start; a pre-start unexpired entry requires current guest identity proof.
Implementation owner: `TASK-013`.

### HP-REQ-025
- [ ] Use one persistent VM per resource without resetting it between jobs; after each job, remove only Haepari-owned job files.
Acceptance: After verified cleanup, a second job on the same VM finds none of the first job's Haepari files; protected uncollected evidence instead quarantines the VM and blocks that second job. Workload changes outside the job directory are left for consumer recipes. Haepari never clones, restores or deletes the VM ([ADR-0018](architecture-decision-records/0018-persistent-vm-no-reset.md)).
Implementation owner: `TASK-013`.

### HP-REQ-026
- [ ] Use host-side UTM automation for VM lifecycle and SSH/SFTP for macOS guest operations.
Acceptance: The adapter works without assuming a QEMU Guest Agent or guest IP query is available; it controls UTM only through `utmctl` and treats `Error from event` on stderr as a failure even with exit 0. Unavailable automation reports a prerequisite gap.
Implementation owner: `TASK-014`.

### HP-REQ-027
- [ ] Keep guest supervision alive across SSH disconnection without adding a permanent guest scheduler.
Acceptance: Reconnect by the original run ID and lease token; obtain the same start/exit receipt and original output. Disconnect never implies workload termination.
Implementation owner: `TASK-014`.

### HP-REQ-028
- [ ] Verify SSH host identity and disable agent forwarding and broad host credential inheritance.
Acceptance: Wrong host key, wrong guest identity, wrong VM, and wrong job nonce fail before candidate installation; no StrictHostKeyChecking=no fallback exists.
Implementation owner: `TASK-014`.

### HP-REQ-029
- [ ] Prepare, collect, clean up, and health-check a VM under one lease, quarantining on uncertainty.
Acceptance: A failed cleanup or health check blocks all later jobs for that resource while unrelated resources remain usable; stale recovery cannot act on a later job.
Implementation owner: `TASK-015`.

### HP-REQ-030
- [ ] Keep test host paths separate from guest installation paths.
Acceptance: No VM job mounts the host home, live skills, plugin cache, source checkout, or production runtime writable. Project-controlled guest recipes are never promoted to host hooks.
Implementation owner: `TASK-015`.

### HP-REQ-031
- [ ] Prove the supported already-running UTM Job loop on one real macOS guest before advertising that backend.
Acceptance: Retain observed authenticated success, meaningful failure, cancellation/timeout, logs/outputs, verified cleanup and successor reuse. Record phase timings from the tested candidate. Deterministic fixtures cover identity refusal and power/reboot boundaries; separate live experiments are optional. Historical TASK-016 and TASK-038 observations remain scoped evidence; TASK-027 owns the exact release candidate's small running-VM loop.
Implementation owner: `TASK-016`, `TASK-027`.

### HP-REQ-032
- [ ] Provide a project-owned `make test-vm` integration contract with a separate guest entrypoint.
Acceptance: One small synthetic project submits through the common queue, prints and saves Job IDs, waits and returns truthful results without embedding UTM control. Executing its guest entrypoint on the host is rejected.
Implementation owner: `TASK-017`.

### HP-REQ-033
- [ ] Support consumer-selected suite wrappers, with auto/manual mode independent of suite.
Acceptance: Keep existing suite values compatible; a project may use only regression. An all wrapper runs only that project’s declared suites as distinct Jobs with one frozen candidate and reports all IDs/results. Installation and migration suites are optional consumer recipes.
Implementation owner: `TASK-017`.

### HP-REQ-034
- [ ] Leave ordinary `make test` independent of VM availability and live-service credentials.
Acceptance: VM absence fails only the explicitly selected VM gate. A missing required VM or credential cannot cause a successful skip or host-install fallback.
Implementation owner: `TASK-017`.

### HP-REQ-035
- [ ] Record an immutable consumer environment reference for every managed admission.
Acceptance: Retain profile_id/profile_digest and the sealed selected inputs without resolving latest or inspecting a global installation registry. The small project helper hashes the selected consumer profile bytes; rich installation locks and compatibility checks are optional recipe inputs.
Implementation owner: `TASK-018`.

### HP-REQ-036
- [ ] Transport declared candidate and test files as opaque, explicitly selected payloads.
Acceptance: Arbitrary regular candidate/test files, including a skill-only payload, retain their selected bytes and executable modes. No binary/daemon pairing, skill layout, installation inventory or native discovery rule is required by Haepari.
Implementation owner: `TASK-018`.

### HP-REQ-040
- [ ] Support hands-on jobs that retain exclusive ownership until a recorded outcome and cleanup.
Acceptance: Environment-ready is never PASS. Manual completion includes observer, candidate, scenario outcomes, and omissions; stale revisions or conflicting repeated completion are rejected.
Implementation owner: `TASK-019`.

### HP-REQ-041
- [ ] Do not auto-grade skill behavior or invoke paid providers by default.
Acceptance: Offline, live automatic, and human observations remain distinguishable. Missing human observations or unapproved required live checks result in INCOMPLETE.
Implementation owner: `TASK-019`.

### HP-REQ-045
- [x] Exercise crash, race, repeated request, and resource starvation boundaries with bounded deterministic failure injection.
Acceptance: The published failure matrix is executed and each invariant is tied to a test or observed VM scenario.
Implementation owner: `TASK-021`.

### HP-REQ-046
- [x] Preserve clear cancellation races with final results.
Acceptance: Cancellation before admission, queued cancellation, running cancellation, and cancellation after finish have documented results without overwriting a terminal record.
Implementation owner: `TASK-021`.

### HP-REQ-047
- [x] Restrict control access to the owning user and protect local state and secret references.
Acceptance: Socket/state permissions, path containment, redacted diagnostics, secret omission, and symlink attacks are tested; same-user arbitrary code is explicitly outside the security boundary.
Implementation owner: `TASK-022`.

### HP-REQ-048
- [x] Keep live credentials and external side effects outside the VM test boundary.
Acceptance: No authentication files are bundled from the host. Live scenarios disclose exact endpoints and budget scope; no cleanup is described as undoing network writes or model charges.
Implementation owner: `TASK-022`.

### HP-REQ-049
- [x] Treat queued commands and test recipes as authorized executable code, not automatically safe data.
Acceptance: A local resource is documented as unsandboxed. An untrusted repository cannot change registered host adapter commands, resource identity, or recovery policy through a job.
Implementation owner: `TASK-022`.

### HP-REQ-050
- [ ] Bound orchestration inputs, logs, artifacts and retained events, with streaming delivery.
Acceptance: Configured bounds and visible truncation/gaps are verified with bounded local fixtures. Larger memory/throughput benchmarks are optional unless their measured performance is advertised; an unmeasured target is not a release blocker or a performance claim.
Implementation owner: `TASK-023`.

### HP-REQ-051
- [ ] Support inspection and recovery without Aquarium or any tool under test.
Acceptance: Disable those products and demonstrate Haepari status, cancel, wait and managed fixture recovery through the native CLI. Unknown managed execution retains occupancy until supported termination or no-start evidence exists.
Implementation owner: `TASK-023`.

### HP-REQ-052
- [ ] Version Haepari storage and support safe startup migration and schema-too-new rejection.
Acceptance: A quiesced backup and injected failed upgrade preserve a recoverable prior store; automatic downgrade is refused.
Implementation owner: `TASK-024`.

### HP-REQ-053
- [ ] Keep the production Haepari controller separate from candidate Haepari testing.
Acceptance: Self-tests use an isolated state root or guest, unique socket, and synthetic resources; they cannot replace or stop the stable controller running the campaign.
Implementation owner: `TASK-024`.

### HP-REQ-054
- [ ] Distribute a verified binary and same-release `use-haepari` skill, with explicit daemon service installation.
Acceptance: An isolated installation verifies the complete packaged skill tree, documented CLI flow and explicit service lifecycle without Aquarium or another scheduler. External model/provider skill-behavior evaluation is optional; installation does not auto-enable UTM or modify other products.
Implementation owner: `TASK-025`.

### HP-REQ-055
- [ ] Publish version and capabilities as machine-readable information.
Acceptance: The consumer checks declared schema, backend, wait, manual, and recovery capabilities instead of guessing support from a version string.
Implementation owner: `TASK-025`.

### HP-REQ-056
- [ ] Document the supported VM setup, automatic/manual tests, ordinary recovery and decommissioning.
Acceptance: The runbook supports one already-running registered VM with precise prerequisites and documented current commands. Product-specific skill and migration guidance is clearly optional consumer material; no power/logout/lease-expiry experiment is required for setup.
Implementation owner: `TASK-026`.

### HP-REQ-057
- [ ] Preserve provider and consumer repository ownership.
Acceptance: Sample integration uses project-owned tests; Haepari documentation does not create mandatory product-specific branches or a central integration-test repository.
Implementation owner: `TASK-026`.

### HP-REQ-058
- [ ] Bind release acceptance to exact binaries, skill trees, protocol versions, and observed tests.
Acceptance: A changed artifact digest invalidates the old artifact claim; prerelease local builds are not represented as official distribution evidence.
Implementation owner: `TASK-027`.

### HP-REQ-059
- [ ] Qualify at least two distinct managed resource use cases before calling the queue general purpose.
Acceptance: Run real local-command Jobs and the small already-running single-VM release loop through the same scheduling ledger; record distinct resource occupancy and cleanup evidence. The historical thirty-Job/50 MiB campaign is optional tooling, not a release requirement.
Implementation owner: `TASK-027`.

### HP-REQ-060
- [ ] Deliver a managed consumer handoff sufficient for Aquarium to begin its managed-first pilot.
Acceptance: The handoff includes exact release identities, test evidence, frozen managed schemas and capabilities, protocol examples, known limits, and prerequisite gaps. Haepari implementation completion depends on no Aquarium change. The Aquarium-side plan alignment listed in [external requests](to-external.md) is a named consumer prerequisite, not a Haepari dependency.
Implementation owner: `TASK-028`.

### HP-REQ-061
- [ ] Keep the old Aquarium development-channel cleanup outside Haepari product execution.
Acceptance: No Haepari installer or default job deletes ~/.aquarium-dev, edits foreign Git hooks, removes Sorage data, or replaces host skills.
Implementation owner: `TASK-028`.

### HP-REQ-062
- [ ] Keep managed execution as the supported v0.1.0 request family; reserve a separate future external family without exposing its APIs.
Acceptance: Reject external-mode requests/resources as unsupported; auto/manual stays independent. Preserve frozen F1/F2 and freeze F3 at TASK-020 for the project wrapper, guest entrypoint, consumer environment reference and general manual results. Product installation inventories and migration evidence are outside F3.
Implementation owner: `TASK-002`, with freeze gates in `TASK-012` (F1), `TASK-016` (F2) and `TASK-020` (F3).

### HP-REQ-080
- [ ] Provide advisory named locks with FIFO queuing, blocking wait, explicit release, liveness renewal bounded by a maximum hold, expiry and operator force release.
Acceptance: Two cooperating callers never hold a capacity-one lock at once. A lost acquire reply recovers the same request, and a replay of an ended request reports that it ended. A holder that stops renewing, or a grant nobody claims, ends after one liveness window, and the next waiter is granted in the same transaction. A stale release cannot release a later grant. An inherited grant is verified against the intended installation, lock and current generation, and only its acquiring owner renews or ends it. Forced releases and expiries are recorded.
Implementation owner: `TASK-035`.

### HP-REQ-081
- [ ] Keep locks advisory and separate from managed execution.
Acceptance: Locks consume no managed resource or global capacity, grant no execution permit, and never run or observe the holder's work. A nested managed caller cannot acquire a lock. Documentation states that callers who skip the lock are outside its guarantee.
Implementation owner: `TASK-035`.

## Operational-readiness follow-up ownership

[EPIC-009](roadmap/EPIC-009.md) adds corrective implementation and qualification contributions to existing active requirements. Original implementation owners and accepted evidence remain recorded above; the [traceability table](traceability.md) maps every additional task. No new requirement or implemented behavior is asserted by this plan.

| Contribution | Requirement coverage and boundary |
| --- | --- |
| TASK-036 | Failure/evidence/recovery and frozen semantics: HP-REQ-018–022, HP-REQ-024–025, HP-REQ-027–030, HP-REQ-062. Master resolves missing-output, changed-boot and evidence-loss decisions before affected implementation. |
| TASK-037 | Startup, transfer, runner retention and minimum limits: HP-REQ-011, HP-REQ-019, HP-REQ-022–024, HP-REQ-026–027, HP-REQ-050–051, HP-REQ-062. General configuration and performance qualification stay with TASK-023. |
| TASK-038 | Exact installed-candidate qualification: HP-REQ-011, HP-REQ-018–031, HP-REQ-050–051, HP-REQ-059, HP-REQ-062. Required real campaign rows cannot pass from fixtures or historical results. |
| TASK-039 | Small isolated caller submission/evidence loop: HP-REQ-009–011, HP-REQ-014–015, HP-REQ-018–019, HP-REQ-021–022, HP-REQ-032, HP-REQ-034–035, HP-REQ-059, HP-REQ-062. Generic Make/input/manual integration remains in EPIC-005; product skill/migration recipes are consumer-owned optional scenarios. |

The [dossier decisions](TODO-VM-OPERATIONAL-READINESS.md#decisions-before-affected-implementation) retain the approved TASK-036 choices and historical execution scope. Existing verdicts, protected-evidence rules and F1/F2 shapes remain authoritative until recorded contract/version changes are accepted. DF-001–004 and guest-reboot experiments are optional hardening outside the supported first-release profile; they retain their uncertainty and require an explicit re-entry decision.

## Consumer-owned optional requirements

HP-REQ-037–039 and HP-REQ-042–044 retain recipe guidance and stable IDs. They are optional consumer scenarios, not Haepari feature or release prerequisites. A consumer selects and implements them in its own repository; Haepari runs the declared commands and collects ordinary outputs without interpreting product semantics. No active Haepari task owns their implementation.

### HP-REQ-037
- [ ] Collect complete skill-tree inventories and declared locations, including actual selected home and project discovery roots; consumer recipes verify duplicates and effective discovery.
Acceptance: Added, removed, changed, extra, and shadowing files are exercised; checking only SKILL.md or a minimum file list is insufficient.
Consumer owner: integrating project. Historical Haepari plan owner: `TASK-018`.

### HP-REQ-038
- [ ] Transport native skill version/location declarations, including independently pinned skills and per-CODEX_HOME external skills; consumer recipes enforce compatibility.
Acceptance: A fixture with different binary and skill source revisions can pass when declared compatible; a duplicate shared/home installation is diagnosed rather than silently ignored.
Consumer owner: integrating project. Historical Haepari plan owner: `TASK-018`.

### HP-REQ-039
- [ ] Record consumer-submitted installed-file, fresh-session discovery, behavior, and cleanup results as distinct evidence classes; enforce declared required-result completeness.
Acceptance: A file hash check or a prompt containing copied skill text cannot satisfy installed-skill discovery or behavior acceptance.
Consumer owner: integrating project. Historical Haepari plan owner: `TASK-019`.

### HP-REQ-042
- [ ] Provide a sample migration recipe contract that lets a consumer create meaningful state through an old release before installing the candidate.
Acceptance: Old and new identities, pre-upgrade assertions, preserved state identifiers, post-upgrade read/write behavior, and restart behavior are recorded.
Consumer owner: integrating project. Historical Haepari plan owner: `TASK-020`.

### HP-REQ-043
- [ ] Require consumer-declared tests for configuration, skill/rules, daemon, data, and supported in-progress-work migration policies.
Acceptance: The scenario either proves supported resume or proves safe update refusal while busy; dropping work or inventing seamless upgrade is unacceptable.
Consumer owner: integrating project. Historical Haepari plan owner: `TASK-020`.

### HP-REQ-044
- [ ] Provide sample fault cases and required-result declarations for consumer-owned interrupted installation/migration and supported recovery or explicit unsupported upgrade paths.
Acceptance: A retry does not duplicate logical work; unknown workload effects remain inconclusive. Downgrade support is never inferred from test cleanup.
Consumer owner: integrating project. Historical Haepari plan owner: `TASK-020`.

## Deferred external-execution requirements

The following IDs are retained for post-v0.1.0 design. Their checkboxes and acceptance text do not count toward v0.1.0. B1 and B2 in the deferred lease contract block implementation re-entry.

### HP-REQ-063
- [ ] Admit an external request atomically as queued or granted with a durable owner-bound request key.
Acceptance: Lost reply and identical retry recover the same request; conflicting keys or owners never duplicate a grant. Reserved grants already count against both limits.
Implementation owner: `TASK-029`.

### HP-REQ-064
- [ ] Activate a grant for exactly one executor and require a first-claim native execution intent before effects.
Acceptance: Competing activation fails; identical begin replay is reconciliation-only. A crash after intent commit cannot issue a second execution permit.
Implementation owner: `TASK-029`.

### HP-REQ-065
- [ ] Provide bounded lease listing, inspection, blocking grant/release waits and resumable events.
Acceptance: Observer timeout/disconnect does not cancel, resubmit or free a lease. A terminal cancelled request is not reported as granted.
Implementation owner: `TASK-029`.

### HP-REQ-066
- [ ] Record bounded native execution references and terminal/no-start attestations without invoking the external tool.
Acceptance: Invocation references include native session scope. Binding a run ID does not prove termination; unresolved native starts remain occupied.
Implementation owner: `TASK-030`.

### HP-REQ-067
- [ ] Release external capacity only after all possible protected native executions are settled.
Acceptance: The owner closes new-step admission and frees both slots atomically. Exact release replay cannot change a later lease. Release success never means review PASS.
Implementation owner: `TASK-030`.

### HP-REQ-068
- [ ] Reconcile external owner loss, daemon restart, timeout and active cancellation without time-only reassignment.
Acceptance: Uncertain executions keep capacity; cancellation acknowledgement or a lost observer cannot free it. A never-started revocation proves no execution intent won the race.
Implementation owner: `TASK-030`.

### HP-REQ-069
- [ ] Correlate multiple released execution segments without keeping logical-work state in Haepari.
Acceptance: A new segment has a new request key and lease, same opaque operation reference and exact continuation link; old key replay returns the old released receipt.
Implementation owner: `TASK-031`.

### HP-REQ-070
- [ ] Allow already-authorized immediate native retries under the current lease without recursive acquisition.
Acceptance: A six-role fixture recovers only its failed role under one lease; a new execution key is used for a distinct native call and no extra retry authority is inferred.
Implementation owner: `TASK-031`.

### HP-REQ-071
- [ ] Return a terminally idle review slot before user waiting and require a fresh grant on later continuation.
Acceptance: A five-of-six fixture releases with awaiting_user, another review runs, and later one-role recovery receives ordinary FIFO admission rather than restored priority.
Implementation owner: `TASK-031`.

### HP-REQ-072
- [ ] Prevent concurrent continuation branches and duplicate native dispatch by cooperating clients.
Acceptance: Same operation/resource has at most one live request and one admitted successor per released parent. Duplicate begin returns reconcile_only; caller state binds one user decision to one request key.
Implementation owner: `TASK-031`.

### HP-REQ-073
- [ ] Separate admission truth, scoped owner attestation, native publication/coverage and resource-return outcome.
Acceptance: Tests show released plus incomplete review, successful role coverage plus failed CI, and unknown native execution without a false return or PASS. Native results are never reconstructed from Haepari metadata.
Implementation owner: `TASK-030`.

### HP-REQ-074
- [ ] Keep external capacity advisory and preserve product-native recovery and authorization.
Acceptance: Direct bypass is documented; an unavailable selected queue route fails instead of silently running directly. No provider routing, result composition or automatic retry policy is implemented in Haepari.
Implementation owner: `TASK-032`.

### HP-REQ-075
- [ ] Protect external ownership secrets and preserve minimal request/mutation/continuation tombstones.
Acceptance: No owner secret appears in argv, environment, logs, artifacts or exports. Purge cannot free an active lease, erase unresolved effects or replay an old request as fresh work.
Implementation owner: `TASK-030`.

### HP-REQ-076
- [ ] Qualify external execution through an independent fixture client, including partial failure and resumed one-role recovery.
Acceptance: The finite external matrix passes without installing Mulgae or Aquarium. Native Mulgae compatibility remains a separately reported downstream acceptance.
Implementation owner: `TASK-032`.

### HP-REQ-077
- [ ] Publish exact managed/external, activation, first-claim execution, wait, release and continuation capabilities in the CLI and use-haepari skill.
Acceptance: A consumer rejects a build missing a required capability; the installed skill preserves same-ID observation, fresh continuation identity and truthful native outcome separation.
Implementation owner: deferred with EPIC-008 (post-v0.1.0 re-entry); no active task owns it. The v0.1.0 release publishes managed capabilities plus `advisory-lock/v1`, not external-execution capabilities.

### HP-REQ-078
- [ ] Retain native recovery source ownership and storage outside Haepari.
Acceptance: A consumer fixture rejects missing, corrupted or mismatched target/lineage inputs instead of replaying accepted roles or fabricating composition; Haepari purge never deletes product data.
Implementation owner: `TASK-032`.

### HP-REQ-079
- [ ] Preserve single-controller independence while testing a queue-dependent consumer inside a managed VM.
Acceptance: Guest integration tests use an isolated controller/test resource, not the occupied parent VM/global slot. The external-resource design remains deferred.
Implementation owner: `TASK-032`.

## Explicit exclusions

No distributed controllers, public service, multi-tenant authorization, arbitrary DAG language, multi-resource atomic reservation, Windows backend, recurring scheduler, cloud-provider fleet, automatic AI remediation, automatic provider authentication, or default LLM evaluator is required. VM reset, whether by whole-VM copy, snapshot or disposable start, is not part of v0.1.0 (ADR-0018). No runtime component reads Aquarium roadmap state to decide whether a generic job can run.

## Completion rule

v0.1.0 requires every mandatory requirement in the Requirements section, the mandatory [verification matrix](verification.md) in its declared local/fake/live layers, managed local and small already-running single-VM auto/manual use cases, contracts frozen at F1, F2 and narrowed F3, a verified installation package, and the [Aquarium producer handoff](handoffs/haepari-to-aquarium.md). Missing TASK-027 exact-candidate live evidence leaves release acceptance incomplete; it does not block TASK-017–026 local acceptance or reopen historical qualification. Consumer-owned HP-REQ-037–039 and HP-REQ-042–044, V25–V31 and optional DF-001–004 experiments do not block v0.1.0. Publication and host activation are separate authorized effects.

HP-REQ-063 through HP-REQ-079 are preserved as **deferred** requirements owned by EPIC-008/TASK-029–032 or later consumer integration. HP-REQ-021 refers to managed external side effects of a submitted command, not the deferred public external-lease API. Linux and public lease acceptance require separate re-entry decisions.
