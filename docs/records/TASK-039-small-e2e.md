# TASK-039 small isolated caller e2e

## Scope revision

Master replaced the external real-project application requirement on 2026-10-12
with a small repository-owned e2e test. No external repository selection,
integration, provider invocation or adoption is required. The caller cycle uses
a temporary foreground controller and a local managed resource. TASK-038 retains
its separate already-running single-VM qualification scope and prerequisite role.
The [roadmap](../roadmap.md) alone owns lifecycle; this record does not accept the
Task or Epic.

## Native observation

The tracked test is
[`test_small_offline_caller_cycle`](../../tests/e2e/test_local_cli.py), invoked by:

```sh
uv run --locked --group dev python -m pytest -q \
  tests/e2e/test_local_cli.py::test_small_offline_caller_cycle
```

It passed on 2026-10-12: one test, 1.71 seconds, four actual managed Jobs through
the public CLI and native controller/runner. The executable SHA-256 was
`af41a4e72d5ede7edc11f473e6bac32505fbd35f962b6857c34d03af167a18ee`,
based on modified-source commit
`f91a166e5276226b240f490b45a131b4d8ba3a2f`. The test records actual executable and
test-source digests in each Job's provenance; this is not an installed-service or
real-VM observation.

| Step | Required native observation |
| --- | --- |
| Success | Selected `ready` input, PASS/native exit 0, complete artifacts, verified cleanup. |
| Meaningful failure | Selected `wrong` input fails the recipe's expected-input assertion, FAIL/native exit 7, exact output and useful `expected ready input` diagnostics collected. |
| Distinct successor | A new key and restored `ready` input produce PASS with collected evidence and verified cleanup after the failure. |
| Queued input change | The owned local resource is paused, the `ready` input is sealed at admission, and the original file is changed. After resume the Job produces the original `ready` output and PASS; the host file still has the changed content. |

The test exports and verifies every output's bytes/hash and result. Replaying the
first three saved keys returns the original Job IDs without another workload.
The single-byte execution counter has exactly four bytes after four admissions.
All four Jobs finish; resource availability is available and occupancy zero.
No controller restart, installed-service change, VM power operation, external
project edit or release action is performed.

## Minimal caller recipe and TASK-017 handoff

Reuse the existing test-owned selected-file digest and spec helpers. The selected
file is `source/input.txt`; the independent `haepari-tree-sha256/v1` digest binds
its path, executable bit, size and content hash. A `haepari.job/v2` spec declares a
resource/profile, offline shell argv, bundle-relative cwd, selected input digest,
required `results/out.txt`, finite deadlines and candidate/test provenance.
Submission saves a distinct request key and returned Job ID before observing it.
Terminal waits check the CLI exit separately from the native workload exit;
inspection and export can succeed for a useful FAIL result.

The queue/prepare/run/collect/cleanup budgets are 30/10/10/5/5 seconds. Each caller
wait is limited to 15 seconds. A quarantined resource and unknown cleanup still
require their public recovery rules; this small cycle exercises a known native
failure followed by verified ordinary cleanup, not an evidence-loss escape.

Under Design Revision 3.5, TASK-017 owns the generic project Make and guest
entrypoint contract; TASK-018 owns opaque consumer profile references and
TASK-019–020 own general manual results and the narrowed F3 boundary
([ADR-0020](../architecture-decision-records/0020-local-development-scope.md)).
Preserve sealed queued inputs, permanent request keys, truthful caller/native
exit distinctions, selected output declarations and separate collection/cleanup
evidence. Rich environment-lock decoding and real-project installation/migration
are optional consumer work. This historical local result does not replace
TASK-027's exact-release running-VM and manual observations or required runtime
readiness/recovery checks.
