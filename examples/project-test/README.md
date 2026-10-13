# Small project caller

This runnable reference project uses the public Haepari CLI. Its default suite
checks a small candidate input. The host submits and observes; the managed runner
executes `tests/vm/run.sh` in its sealed workspace. The wrapper performs no UTM
power operation, host installation or environment repair.

Select an explicitly authorized local or already-running registered VM resource
and a compatible Haepari CLI:

```sh
make test
make test-vm HAEPARI=/absolute/path/to/haepari RESOURCE=selected-resource
```

`make test` checks only this project's local source. It needs neither a controller
nor a VM. `make test-vm-guest` refuses direct host execution without the managed
runner's Job/lease context. The guest script uses macOS's built-in `plutil` to
read that context and never submits another Job.

The host prints the private run directory before submission and saves every
request key and exact spec before using it. Reconnect with the printed directory:

```sh
make test-vm HAEPARI=/absolute/path/to/haepari RESOURCE=selected-resource \
    RUN_DIR=/absolute/path/to/the/printed/run-directory
```

Keep the original resource, suite and mode. The saved installation ID prevents
replay into a different controller installation. The bundle retains selected
candidate/test/profile/scenario bytes even when the project's source changes.
A lost reply or wait timeout keeps the same request key and Job; it never starts
a replacement. A new run after a known failure requires a new run directory.
Default `.haepari-runs/` directories remain for inspection and are ignored by the example's tracked `.gitignore`. Keep explicitly selected `RUN_DIR` destinations outside version control. A caller-created run directory must be owned by the current user with mode `0700`.
The saved declared-suite list must match every saved Job entry. Empty, truncated
or inconsistent run records are refused before another submission or aggregate
PASS. Retain the original directory and inspect its keys and specs when refused.

`project.json` declares the selected files, candidate/test subsets, profile path
and ID, and suite recipes with argv, explicit relative cwd, offline authorization
reference, manual scenario (for manual mode), outputs and optional non-secret
environment. The sibling stdlib `haepari_inputs.py` module supplies reusable
`capture_inputs`, `tree_digest` and `job_spec` helpers; keep it beside the caller. `SUITE=all` runs only those declared suites, serially, on the same
frozen bundle and reports skipped suites after a non-passing result. The sample
declares only `regression`; installation and migration recipes are not required.
`MODE=auto|manual` is independent of the suite. Manual Jobs run the declared
optional setup and workload smoke within the prepare budget, then hold capacity
in `awaiting_manual`. Smoke success never supplies a human PASS. The wrapper may
report PENDING while the original Job remains occupied. Follow the sealed
scenario document and submit the closed `haepari.manual-result/v1` record with
`haepari manual complete <job-id> --input <record.json> --expected-revision <n> --json`.
The `manual_preparation_stopped` event's `workspace` identifies the sealed
workspace for the hands-on check: a host path for local Jobs and a guest path
for VM Jobs. Use `haepari events <job-id> --json` to read it.
Use `installation_id` from doctor. Map the admission/inspection receipt's
`environment_profile_id` and `environment_profile_digest` to the record's
`profile_id` and `profile_digest`, respectively. Use
the current awaiting Job revision and every required scenario ID. Each pass/fail
needs consumer evidence references; `unobserved` is explicit. Haepari records
these assertions without interpreting product behavior. Unobserved checks yield
INCOMPLETE. A fully observed failed check yields FAIL when required evidence is
complete and cleanup is verified; missing evidence or unverified cleanup yields
INCOMPLETE while retaining the failure fact.

The exact same record and expected revision replay the original completion
receipt, even after settlement/restart. A changed record conflicts. Reconnect
with the saved run directory to report the original result; do not resubmit as a
new Job. Caller exit and wait expiry do not abandon a manual Job. Explicit
cancellation or the original manual deadline retires completion authority and
starts owned collection/cleanup. Restart never renews that deadline or repeats
setup. Unknown native/filesystem termination keeps reconciliation and occupancy.

`doctor --resource <id> --json` observes the selected installation and resource.
For a VM it checks the Aqua domain, existing running identity, individual DHCP
lease and pinned SSH access with a read-only guest probe. It transfers no runner
and stages no Job files. A missing prerequisite returns an actionable gap and
exit 2. This observation is not a lease or cleanup proof; normal admission,
dispatch and recovery still enforce the resource's current fences.

The wrapper reports Haepari's final verdict and native workload exit separately.
PASS/FAIL/INCOMPLETE/CANCELLED use exits 0/1/3/4; wait expiry uses 5 and leaves the
Job alive. A non-verdict observation error uses aggregate `UNKNOWN` and preserves
its nonzero CLI exit; it never claims an INCOMPLETE terminal verdict. Outputs and logs remain available through normal `inspect`, `logs`
and `export`. These are the Python wrapper's exits; Make reports a recipe failure
with its own nonzero exit. This example's finite queue/prepare/run/collect/cleanup budgets are
30/10/10/5/5 seconds; manual specs use 60 seconds instead of the run budget.
Each caller wait is 15 seconds. A larger inspection budget must be chosen before
admission, not silently extended while reconnecting.

Profile bytes are consumer data, not an installation inventory. The example
hashes its selected `profile.json` bytes into the existing environment reference.
The profile need not be JSON: Haepari does not interpret its contents. Candidate
and test subset digests use the public `haepari-tree-sha256/v1` algorithm, including
paths, content and executable bits. Every subset and the profile path must be in
`selected_files`. A skill-only Markdown payload with its test script and profile
is sufficient; there is no required binary, daemon or installation inventory.
Capture streams regular files in 64 KiB chunks, refuses links and unsafe or
known credential path names, and preserves executable/non-executable modes. Missing inputs
or mutation during capture leave an unfinished run for inspection, with no Job
admission. A bundle digest mismatch is refused by the controller before admission.
Reconnection uses only saved original specs and keys, even if the current input
policy/profile changes or the registered resource gains a successor revision.
Only declared files and environment are copied; no ambient credentials or host
installation tree is collected.

The recipe declares its own layout and authorization; the reusable spec helper
has no default source directory or scenario path. Invalid profile IDs, recipe
shapes, escaping cwd or a cwd without selected child files are refused locally.
Case and Unicode normalization aliases are rejected without renaming selected
paths. A permanent INVALID_REQUEST response requires correcting the declaration
and choosing a new empty run directory; keep the refused run for inspection.

Standalone `capture_inputs` callers must provide a trusted, caller-owned private
bundle parent. The wrapper supplies its locked 0700 run directory. Credential
name guards apply to selected inputs; generated outputs may use such names.

The [F3 decision](../../docs/records/TASK-020-consumer-freeze.md) records the host,
guest, saved-input and manual-result conventions demonstrated by this project.
It takes effect with canonical TASK-020 acceptance; the sample does not define
a separate lifecycle or require a richer consumer installation inventory.
