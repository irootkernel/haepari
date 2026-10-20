# TASK-028 producer handoff

Date: 2026-10-19. This is the producer completion record for [HP-REQ-060](../requirements.md#hp-req-060) and [HP-REQ-061](../requirements.md#hp-req-061). It follows the packet sections in [Haepari to Aquarium](../handoffs/haepari-to-aquarium.md). The template stays a template.
The canonical roadmap owns lifecycle. This file does not accept TASK-028 or EPIC-007, and it does not close the [verification checklist](../verification.md#v010-release-checklist).

Publication, installation on a real host, and consumer adoption remain separate effects. No new Haepari capability, schema, or package byte is introduced.

## Evidence class

Default adoption is a verified official stable release. That release does not exist. A consumer that starts Aquarium:TASK-086 on the current candidate must explicitly choose the unpublished modified-source evidence class and re-check its bytes.

The candidate's reported source base is `ab5d70193d9cecd2b630bec0b10d166ad5516dd4` with modified source. Commit `f7e3ce299ff1c9eaf353b3dba214df836947fda2` matches the 283 compile and native source inputs. That source map does not contain the paired skill. The packaged skill is a separate one-file inventory entry, `skill/use-haepari/SKILL.md`. Exact archive, binary, guest-runner, and skill digests stay in the private producer packet. This record does not copy those digests, native IDs, raw logs, or private paths.

`compiled_backends` is `["local"]`. `qualified_backends` is empty. The accepted running-VM observation is a separate digest-bound profile. It does not rewrite the frozen capability output. The six capability names are `managed-execution/v1`, `durable-observation/v1`, `advisory-lock/v1`, `managed-recovery/v1`, `artifact-delivery/v1`, and `evidence-retention/v1`.

## Source correction after this handoff

On 2026-10-20, the source corrected the guest-stopped attestation basis from a private five-second clock to the existing shared fifteen-second recovery observation budget. The frozen package and private producer packet identified above are unchanged and exclude that correction. Their source correspondence is to the stated historical commit, not to a later corrected checkout. A build from corrected source has a new identity and needs its own applicable package and local/VM qualification before final release readiness. Existing native observations are not transferred to a different binary. Epic closeout remains on hold; this source correction performs no package publication, service replacement or consumer delivery.

## Frozen contract map

| Gate | Current handoff and accepted deltas |
| --- | --- |
| F1 | [Managed core](TASK-012-managed-core-freeze.md): durable same-key admission/observation, immutable results and protected evidence; job/result v2 absence policy, resource v2 lifecycle ceilings and bounded operator projections. The [TASK-027 delta](TASK-027-release-acceptance.md) adds resource-bound supplementary recovery budgets without changing saved legacy records. |
| F2 | [VM freeze](TASK-016-vm-contract-freeze.md): authenticated persistent-VM execution, owned runner transport and fenced recovery. Current [resource/runner semantics](../contracts/resource-and-runner.md) and TASK-027 preserve operator-only VM power and historical uncertainty. |
| F3 | [Consumer freeze](TASK-020-consumer-freeze.md): project-owned host wrapper, distinct guest entrypoint, immutable selected inputs/profile reference, and general manual completion. |

The [CLI contract](../contracts/cli-and-jobs.md#json-envelopes-and-exit-codes) keeps command success separate from a workload's native exit:

| CLI exit | Meaning |
| --- | --- |
| 0 | Successful command; terminal `run`/`wait` PASS. |
| 1 | Terminal verified FAIL. |
| 2 | Usage/input/contract rejection, conflict or unavailable retained result; no implied workload failure. |
| 3 | Terminal INCOMPLETE with a known result. |
| 4 | Terminal CANCELLED after settlement. |
| 5 | Caller wait expired; the original Job remains alive. |
| 6 | Transport/storage observation uncertain or controller unavailable; reconcile the original request. |

[Lock exits](../contracts/locks.md#exits-and-errors) are operation-specific: a queued acquire without `--wait` can return 0, an ended request can return 4, and wait timeout 5 leaves the request queued. Recover its original request by the same key and explicitly cancel an abandoned request; do not acquire again with a new key. Native workload exit, Make recipe failure and CLI exit are distinct.

## Public help schemas

The frozen `haepari.help/v1` schema list has these 23 names:

`haepari.envelope/v1`, `haepari.job/v1`, `haepari.job/v2`, `haepari.resource/v1`, `haepari.resource/v2`, `haepari.result/v1`, `haepari.result/v2`, `haepari.run-event/v1`, `haepari.recovery-plan/v1`, `haepari.recovery-plan/v2`, `haepari.recovery-plan/v3`, `haepari.recovery-budget/v1`, `haepari.power-recovery-plan/v2`, `haepari.artifact-manifest/v1`, `haepari.artifact-page/v1`, `haepari.purge-receipt/v1`, `haepari.evidence-recovery-receipt/v1`, `haepari.evidence-recovery-receipt/v2`, `haepari.evidence-recovery-receipt/v3`, `haepari.doctor/v1`, `haepari.manual-result/v1`, `haepari.operator-projection/v1`, `haepari.operator-diagnostic/v1`.

`haepari.release-service-plan/v1` and its owner marker `haepari.release-service-owner/v1` are the TASK-025 additive contract, outside that list. Help adds only the `service plan --release` synopsis. See the [packaging record](TASK-025-release-packaging.md) and [CLI contract](../contracts/cli-and-jobs.md).

## Internal and private schemas

These are not public help schemas. Native runner receipts use `haepari.runner-{request,intent,start,outcome,exit,cancel}/v1`, with request, boot/birth identity and fixed-deadline binding as defined in the [runner contract](../contracts/resource-and-runner.md#runner-receipts). Guest transport is `haepari.guest-transport-request/v1`, `haepari.guest-transport-reply/v1`, and `haepari.guest-transport-binding/v1`. Guest execution is `haepari.guest-request/v1` and `v2` with `haepari.guest-reply/v1` and `v2`. Guest recovery is `haepari.guest-recovery-request/v1` and `v2`, `haepari.guest-recovery-reply/v1` and `v2`, and `haepari.guest-attestation/v1` and `v2`. There is no `guest-recovery/v3`.

Private evidence intents are `haepari.evidence-recovery-intent/v1`, `v2`, and `v3`. Intent v3 is separate from public `haepari.recovery-plan/v3` and public `haepari.evidence-recovery-receipt/v3`. v3 plan actions are `recollect` and `dispose`. Storage layout 13 (`evidence-recovery-budget-v13`) adds the resource-budget identity and does not rewrite saved v1 or v2 records. `haepari.purge-intent/v1` remains a private delivery schema outside the 23 public names. Owners are [resource and runner](../contracts/resource-and-runner.md) and [managed transitions](../contracts/managed-transitions.md).

## Operator actions and install

The independent install path is [README.md](../../README.md#install-a-release): verify the archive, run the binary outside the checkout, then `service plan --release` for an isolated user instance. Packaging does not auto-install. The paired guide is [use-haepari](../../skill/use-haepari/SKILL.md).

As of 2026-10-21, the README is the installation guide for rebuilt archives with new artifact identities. The frozen package handed off above retains its original `INSTALL.md` and the same installation steps; its qualification does not transfer to rebuilt archives.

Packaged examples are `haepari.resource/v1` local and lock resources and `haepari.job/v1` local automatic and manual jobs. Files under [docs/examples](../examples/README.md) are design fixtures. The VM templates there are illustrative. The lease templates are deferred external-execution fixtures, not v0.1.0 release tests.

The operator owns VM power. Haepari has no start or stop issuer. `vm.allow_automatic_stop` is accepted and inert, including a stored `true`. The product permits UTM inventory and status. The guest needs existing SSH access and the temporary paired runner. It does not receive a guest toolchain or a permanent guest service. The current scope note on the [F2 record](TASK-016-vm-contract-freeze.md) points here; that record's historical tables stay as written.

For VM readiness, follow [Prepare the macOS VM](../operations.md#prepare-the-macos-vm): one macOS arm64 host/user in its logged-in Aqua session, the installed UTM Apple backend and registered already-running persistent macOS arm64 guest, verified UUID/MAC and current individual DHCP lease, dedicated private SSH mapping/key and host-key pin, and a preprovisioned private guest Job base directory owned by the selected guest user. Remote Login/credential provisioning and any OS access permission are operator setup effects. Read-only `doctor --resource` reports prerequisite gaps; it neither repairs setup nor proves that the guest base exists. Exact observed OS/UTM/boot/profile facts stay in the private packet's evidence references. Other OS/session/backend combinations remain unqualified.

The [runnable project caller](../../examples/project-test/README.md) uses host `make test-vm` with explicit `HAEPARI`, `RESOURCE`, suite/mode and private `RUN_DIR`; its selected `tests/vm/run.sh` is the separate guest entrypoint. Reconnect with the original run directory/key/spec and installation rather than submit a replacement. [Selected-file inputs](../contracts/artifact-and-profile.md#generic-input-helpers-task-018) bind paths, bytes, executable bits, `haepari-tree-sha256/v1`, and opaque `profile_id`/`profile_digest`. Generic automatic work needs no product installation or migration recipe.

For manual work, follow [the operator runbook](../operations.md#run-a-project-test) and [manual completion](../contracts/cli-and-jobs.md#manual-completion): `awaiting_manual` holds capacity until explicit completion or deadline recovery. Read the sealed scenario document; `haepari.manual-result/v1` binds the original installation, Job/profile, observer, required pass/fail/unobserved assertions and current expected revision. Ready and synthetic assertions are not human PASS. Preserve the original record for uncertain-reply replay. Use `inspect`, `events`, `logs`, and explicit `export` to collect results; missing evidence and unknown cleanup remain visible. These references define the formats and commands, not permission to run new workloads.

An acquiring lock owner waits, renews while its work runs, and releases on every completion path. A nested borrower checks the inherited grant with `locks list` and uses it read-only. The borrower does not acquire, renew, release, or cancel. The [lock contract](../contracts/locks.md) owns the commands. Locks grant no execution permit and consume no managed capacity.

[HP-REQ-061](../requirements.md#hp-req-061) is already the install and skill rule: do not delete `~/.aquarium-dev`, foreign Git hooks, Sorage data, or host skills. TASK-027 V32 and V57 record the scoped-host and scoped-HOME observations. This handoff adds no new deletion test.

## Consumer boundaries

Aquarium:TASK-086 can start from the frozen managed contracts and this evidence class. It does not need a future Haepari capability. [External requests](../to-external.md#12-align-aquariumepic-019-with-the-managed-first-scope-required) §1.2 is a consumer prerequisite. The 2026-09-28 alignment note is historical and has not been rechecked. This record does not assert whether that alignment is applied now.

[Mulgae §2.1](../to-external.md#21-queue-reviews-on-haeparis-advisory-lock-plus-rate-limit-safeguards) is the advisory review queue on `mulgae-review`, plus optional rate-limit safeguards. [§2.2](../to-external.md#22-managed-vm-adoption-later-through-aquariumtask-092) is later managed VM rollout through Aquarium:TASK-092. The boundary is also [mulgae-review.md](../integrations/mulgae-review.md). The unsent draft [02-mulgae](../rollout-requests/02-mulgae.md) is not filled or sent. The [producer completion template](../handoffs/producer-completion-template.md) stays unfilled. Product skill, installation, and migration cases V25–V31 are consumer-owned optional material. External cases E01–E20 are deferred and do not block Aquarium:TASK-086.

## Accepted observations and limits

The [TASK-027 acceptance record](TASK-027-release-acceptance.md) maps all 58 mandatory invariants to their actual evidence layers. The private packet binds these outcomes to the exact archive, executable/runner, skill, input and profile identities.

| Evidence | Observed behavior and boundary |
| --- | --- |
| GATE | Local/fake/native-fixture failure coverage, same-key replay/conflicts, nested acquisition refusal, storage compatibility, identity/cleanup fences and recovery. Current affected checks and explicitly reviewed unchanged-stage reuse are recorded separately from failed whole commands. |
| PKG / HOST | Exact standalone package/tree/skill and isolated installed release service; Local auto/manual-interface/cancel/advisory flows, original-runner TERM/KILL reattachment, collection/uninstall, native filesystem/socket checks and bounded host protection. Synthetic manual fixtures are not human observations. |
| RECOVERY | Original retained guest evidence disposed under one authorized, identity-bound supplementary intent and separately checked health. The original INCOMPLETE result, failed recovery receipts and exported host evidence remain unchanged. |
| LIVE | Local progress beside VM occupancy; automatic success, meaningful exit failure, cancellation, timeout, logs/outputs, original-run SSH reconnect with one effect, public F3 replay, actual human offline PASS, verified cleanup and successor reuse on the already-running guest. The owned test service was uninstalled and its state retained; VM power was unchanged. |
| HIST / MACSUP | Historical qualification and supervision supplement only their declared unchanged scope; they do not qualify another executable or unobserved platform. |

Storage layout 13 preserves synthetic prior-schema migration/too-new refusal coverage; there is no prior public Haepari release to migrate. Resource-v2 `recollect`/`dispose` signs stored collection/cleanup ceilings in recovery-plan v3; saved v1/v2 records and original terminal results remain immutable. Per-Job VM recovery observation is bounded by 15 seconds and any earlier saved phase deadline. Follow [recovery operations](../operations.md#current-local-controller-recovery-limit); pause/resume cannot clear unknown execution or protected evidence.

Whole historical `make test` attempts remain exit 2; reviewed stage reuse and corrected affected checks do not turn them into a whole-command pass. The separate original finish tombstone check and close parent-link-count audit failures remain preserved with their bounded continuation/reconciliation evidence in TASK-027. Its older distinct uncertain VM installation/lease also remains protected, not repaired or declared terminated by this handoff.

V25–V31 remain consumer-owned optional cases. External E01–E20 remain deferred. DF-001–004, disruptive VM/session/DHCP/soak/multi-VM campaigns, performance and power-loss durability remain optional unobserved limits under [ADR-0020](../architecture-decision-records/0020-local-development-scope.md). The [verification](../verification.md) definitions remain unmarked; overall checklist closeout and user Epic acceptance are separate from this Task delivery. This handoff changes no external repository, sends no request, removes no legacy state and grants no installation/publication authority.
