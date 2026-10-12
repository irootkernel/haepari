# Single-VM operational campaign

`utm_campaign.py` is the TASK-038 observation driver. It uses public Haepari
commands against one existing registered UTM macOS VM and an exact installed
development service with global limit one and host guest limit one. It does not
install, register, reset or clone anything. Two-VM/local concurrency is DF-004.

The campaign requires a reviewed private manifest, a separate actual execution
approval, and a private evidence directory. A missing input fails. The ordinary
`make test` gate runs only the offline driver tests through `test-qualification`.
The historical `development_service.py` remains its original local-service
qualification; it is not imported by this VM driver.

## Approval and reproducibility

Before execution, disclose the exact source/executable/runner candidate, saved
v2 service plan, registered UUID/MAC/mapping/pin, all workload specs and permanent
keys, control commands, per-step and Job phase budgets, aggregate budget and
restoration procedure. The maintainer reviews the scenario assertions against
the dossier matrix. The operator approves effects; writing an authority JSON
file is not itself approval. Installation or replacement is a separate operation.

Retain the original registration's canonical JSON bytes in a private input file.
Their SHA-256 must equal the public resource `configuration_digest`; pretty
printing or adding a newline changes that digest. This binds the declared
UUID/MAC/SSH mapping to the registered configuration without inventing a public
resource-description command. Use the same registration document when preparing
a new registration under its separate authority. Do not substitute a list-only
resource receipt for the full binding.

Use canonical absolute paths with no symlinked parents. Input/config/evidence
files must be owned by the caller with mode 0600; directories are 0700 and
executables are 0700. Production state-root restrictions still apply. Prepare
agent scratch under the mounted RootKernel temporary directory when available;
do not weaken state-root restrictions to use that directory for a service.

## Manifest

The closed `haepari.utm-campaign/v1` document requires these fields:

| Field | Value |
| --- | --- |
| `schema`, `campaign_id` | Schema above and a unique request-key-compatible name |
| `repository`, `source_revision` | Canonical checkout and exact 40-character product-source HEAD |
| `source_modified` | Optional boolean, default `false`; `true` requires the exact candidate inventory below |
| `candidate_inventory_file`, `candidate_inventory_sha256` | Required only for modified source: private inventory file and exact prefixed SHA-256 |
| `binary`, `binary_sha256` | Absolute installed executable and `sha256:<64 lowercase hex>` |
| `driver_sha256` | SHA-256 of the exact reviewed `utm_campaign.py` bytes, with the same prefix |
| `plan_file`, `plan_sha256` | Saved v2 service plan and its exact prefixed digest |
| `resource` | `id`, bare-hex `configuration_digest`, positive `config_revision`, `uuid`, `mac`, `registration_file`, `connection_reference`, `known_hosts_reference` |
| `aggregate_seconds`, `command_seconds` | Positive integer budgets; aggregate at most 86400, each CLI call at most 600 |
| `controls` | Named exact control command definitions, described below |
| `rows` | All ten named rows below, in the driver's fixed order |

The driver binds public version source revision, the selected modified-source flag and executable
digest to the manifest, then checks the service plist/owner document, actual LaunchAgent PID,
running executable text inode, native connected socket peer PID, public resource digest and
revision, mapping/pin bytes, and public observations. An unavailable native
process observation blocks preflight; it is not replaced with LaunchAgent
registration or a compilation result. Other unfinished Jobs block entry.
Resume permits only the original campaign's admissions, including an admission
found by its durable key after a lost submit reply. Issued submissions use their
exact frozen spec bytes and the controller's sealed inputs; removal or changes to
the original input files do not authorize a new admission or prevent exact replay.
Not-yet-issued submissions still require the selected original input inventory.

For an uncommitted candidate, retain the build's source/executable binding and
create the inventory using the tracked helper. Run from the selected checkout,
with an existing private output parent and the actual executable digest:

```python
import importlib.util, json, os
from pathlib import Path

spec = importlib.util.spec_from_file_location("campaign", "tests/qualification/utm_campaign.py")
campaign = importlib.util.module_from_spec(spec)
spec.loader.exec_module(campaign)
inventory = campaign.capture_candidate(Path.cwd(), "sha256:<actual executable digest>")
path = Path("/absolute/private/candidate.json")
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, "w") as output:
    json.dump(inventory, output, sort_keys=True, indent=2)
```

Set `source_modified: true` and bind that file's exact bytes in the manifest.
The inventory records HEAD, the complete tracked binary diff, all changed and
nonignored untracked file hashes, executable bits and the selected executable
digest. Preflight, resume and effect calls reject drift. Symlinked source paths
and nonregular files are refused. This is a mutable-source observation, not an
immutable capture or a proof that arbitrary source produced an executable;
retain the actual build and reviewed-candidate evidence too. Source changes
require a new reviewed inventory, never replacement of an active checkpoint.

Every step has a globally unique `id`, an `action` and positive integer `seconds`
(at most 3600). References point to earlier steps. Steps are executed in order:

| Action | Additional fields and behavior |
| --- | --- |
| `submit` | `spec_file`, `spec_sha256`, `request_key`; private offline managed `haepari.job/v2` spec, exact bytes/hash and distinct saved key |
| `replay` | `job`: earlier submit ID; submits its exact frozen spec/key and requires the original Job ID |
| `wait` | `job`; optional `expected_exit`, `expected_error`; retains native result and caller-only timeout |
| `inspect` | `job`; optional `until`: JSON checks repeatedly observed under the original step deadline |
| `resource` | Read the selected resource; optional `until` checks |
| `cancel` | `job`; saves actual expected Job revision and one mutation key before the request |
| `recover_plan` | `kind` (`job` or `resource`), `plan_action`, and `job` for a Job plan; resource `health` selects the default plan without `--action`; other supported actions are explicit |
| `recover_apply` | `plan`: earlier plan step, `request_key`, optionally `evidence_file` and `evidence_sha256`; applies the saved token/key, never refreshes it implicitly |
| `events` | `job`; drains all pages, rejects resync/foreign cursors and records SSH measurement pairs and gaps |
| `logs` | `job`, expected `size` and prefixed `sha256`; drains every byte and verifies exact full size/hash and one file incarnation |
| `export` | `job`; public verified export to a new exclusive step directory; retains partial destinations on error |
| `purge` | `job`; saves the exact finished Job revision and invokes public explicit purge; preserves all captured campaign evidence |
| `control` | `control`: one approved named command; optional `expected_exit`; uncertain return blocks reissue |
| `check` | `record`: earlier step ID, `checks`: nonempty exact JSON observations |

`cancel`, `purge`, `wait` and `recover_apply` accept an explicitly expected
nonzero exit. To test a public refusal also specify `expected_error`; the driver
requires both the exit and the exact error code. Checks use a JSON path array
and an `equals` value. For example, after a successful `wait`:

```json
{
  "id": "success-result-check", "action": "check", "seconds": 10,
  "record": "success-wait",
  "checks": [
    {"path": ["final_verdict"], "equals": "PASS"},
    {"path": ["cleanup_outcome"], "equals": "verified"}
  ]
}
```

This is a step fragment, not a complete campaign. Each row needs assertions
covering its actual requirement, not merely an exit-code or occupancy check.
The driver never infers row qualification from a row name or a control label.
Use observed native result field names and actual CLI verdict exit codes.

## Required rows and evidence

| Row | Required scenario and evidence to assess |
| --- | --- |
| `success_reuse` | Complete bytes/hash, verified cleanup and one capacity return; distinct successor on the same VM; approved outside-root marker survives |
| `missing_output` | Proven native termination and positive required-file absence; truthful gap-bearing INCOMPLETE, cleanup and successor reuse |
| `uncollected_output` | An existing unavailable required file or failed collection remains protected; no speculative cleanup/reuse; original evidence recovered through public plans |
| `native_failure` | Meaningful nonzero native exit with collected diagnostics, truthful FAIL and verified successor reuse |
| `cancel` | Running original Job, revision-bound cancellation and native termination facts; outcome-race semantics and once-only settlement |
| `timeout` | Original admitted run deadline, native timed-out result and bounded safe settlement; no reset budget |
| `ssh_interruption` | Scoped temporary observation loss and explicit restoration; original-run reattachment, once marker and no second workload |
| `controller_restart` | Exact installed controller interruption and new native incarnation; same guest run/deadlines/receipts and saved-key reattachment |
| `large_log` | Exactly 52,428,800 bytes with full hash verified by `logs`, admitted collection budget, retained timing/application-byte data; no additional network benchmark |
| `soak` | Exactly thirty distinct persisted `submit` steps, intentional failure/recovery interleaving, explicit safe purge, old-key replay, occupancy and runner storage before/after |

Master removed VM start/stop, cold-start, guest-reboot and power-intent scenario
qualification from EPIC-009 on 2026-10-12. Reuse the already-running registered
VM. These scenarios are excluded requirements, not passing or skipped campaign
rows; previously captured observations remain historical evidence. Do not add
controls that switch VM power as part of this campaign. The product recovery
contracts and their local fixture tests remain unchanged. Master also clarified
the use case as one machine, one user and that user's already-running VM: separate
identity-refusal experiments and transport/network benchmarks are outside Epic
acceptance. Existing identity fences remain in the product. A maintainer running
the tracked small core e2e command satisfies reproducibility; no second live VM
campaign is required for that purpose.

The script requires every remaining row, thirty soak submissions and the exact 50 MiB log
assertion before entry. It does not automatically prove native scenario
semantics from arbitrary user assertions. Capture all public events/logs/results
before any purge. No missing observation is a successful skip. DF-001 DHCP
expiry, DF-002 host logout, DF-003 multi-day currency and DF-004 two-VM concurrency
remain outside this campaign.

## Controls and restoration

Each named control has `argv` (an exact argument array starting with an absolute
private owned executable), `executable_sha256`, positive `seconds` (at most 600),
`purpose` and `restoration`. A repository-owned or explicitly reviewed operator
control must be reproducible with its source and exact dependencies retained
with the approved inputs. A script path or prose declaration alone is not proof.
The driver does not use a shell to reinterpret the argument array. It adds no
dynamic argument substitution. Selected Job IDs can be read from saved campaign
checkpoints by the approved control; its scoped target checks remain required.

Controls emit a JSON `haepari.utm-control-observation/v1` with the exact `vm_uuid`
and their actual native observations. The complete stdout/stderr and command
result are retained. Row checks can refer to `observation` within the control's
result. Retain first-hand command/native proof for once markers and
restoration; a freely supplied boolean is insufficient.

On timeout, the driver kills only its own command process group and records an
uncertain return. That does not prove the remote workload or power operation
stopped. It never repeats that control, cancels its Job, stops a service/VM,
deletes protected evidence or executes a guessed restoration. Inspect and carry
out the specifically approved restoration before arranging a bounded next
operation. An interrupted export is likewise retained for inspection. Safe
submit/cancel/recovery/purge replay retains its original spec/key/token/revision
and deadline. Expired or stale plans require explicit fresh operator direction;
the driver does not replace authority or extend admission deadlines.

## Authority and commands

The authority has exactly `schema` (`haepari.utm-campaign-authority/v1`),
`campaign_sha256` (exact complete manifest bytes), `approval_reference` (actual
operator decision), `expires_at` (finite UTC Unix seconds), `effects` and
`restoration`. Currently the explicit effect inventory is `submit`, `cancel`,
`purge`, `export`, `recover_apply` plus every named control. Installation and
registration are absent; approve and perform them separately. A changed manifest
or authority cannot replace an existing evidence checkpoint.

After approval, create a new private empty evidence directory, then use:

```sh
HAEPARI_UTM_MODE=preflight \
HAEPARI_UTM_CAMPAIGN=/absolute/private/campaign.json \
HAEPARI_UTM_AUTHORITY=/absolute/private/authority.json \
HAEPARI_UTM_EVIDENCE=/absolute/private/evidence \
make test-utm
```

Select `run` for first execution, `resume` for the original saved campaign, or
`report` for a local checkpoint report. Run/resume both recheck exact installed
bindings. Before issuing any resumed effect, the driver verifies every completed
step's retained result digest; missing or changed evidence blocks even purge.
After recollection or attestation, use a separate resource `health` plan and
apply its saved token/key before successor reuse. A modified-source binary needs
the explicitly selected candidate inventory; no intermediate commit is required.
The aggregate deadline starts before the first scenario and survives
resume. Per-step deadlines are persisted before their first call; attempts do
not extend them. Backward wall-clock movement blocks resume, and monotonic time
prevents a clock adjustment from extending a running operation.

The evidence directory holds immutable input/key copies, attempt start/return
records and private stdout/stderr, a durable checkpoint, native service binding
observations, digest-bound step result artifacts and `report.json`. The checkpoint
keeps bounded references rather than duplicating complete event histories. Each
step result is limited to 32 MiB; the checkpoint writer stops before 16 MiB and
the reader permits 32 MiB. Exceeding a bound fails with retained original
control/key/deadline state, so divide large observations into explicit bounded
steps. Existing result bytes are never replaced with a different observation. A passing preflight means readiness only. A zero
run exit means all selected commands/assertions returned and observations were
collected. **It does not accept TASK-038:** the report keeps qualification
`unverified` until the required candidate-bound native assertions and limits are
assessed by the owning workflow's Independent Review and acceptance gates.
The report verifies every completed step's result hash; missing or changed
observations produce an explicit gap and an incomplete report. Incomplete rows
or commands return 2 and preserve the original evidence.

SSH exchange totals are application archive/response bytes. Missing pairs and
detail-limit gaps remain explicit. Packet/protocol overhead is optional supporting
data and is outside this Epic completion gate. If explicitly selected later, it
requires a bounded capture; an unavailable capture remains unobserved,
not zero or a theoretical estimate. Record host retained runner bytes using the
approved exact Job inventory, not recursive payload deletion. Promote only
bounded non-secret measurements to the qualification record after assessment;
private runtime evidence stays private.
