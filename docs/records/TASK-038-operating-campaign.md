# TASK-038 operating campaign observations

## Scope and candidate

Master narrowed EPIC-009 to one existing registered VM on 2026-10-10 and removed
VM power-on/off, cold-start, guest-reboot and power-intent scenario qualification
on 2026-10-12. The remaining campaign reuses the already-running VM with global
execution limit one and host macOS guest limit one. Removed scenarios are outside
acceptance; they are not passing or skipped rows. Earlier captured evidence and
TASK-016 history remain preserved.

The observations below belong to the modified-source development candidate based
on commit `f91a166e5276226b240f490b45a131b4d8ba3a2f`, executable SHA-256
`af41a4e72d5ede7edc11f473e6bac32505fbd35f962b6857c34d03af167a18ee`.
The exact source inventory, installed service/runner bindings, original spec/key
bytes, native observations, CLI replies and exports remain private execution
evidence. The scope revision changes campaign tooling and documentation, not the
qualified Go implementation or installed binary. It does not qualify revised
campaign-driver bytes from the older native execution.

The [roadmap](../roadmap.md) alone owns lifecycle. This record promotes bounded
measurements and operating limits; it does not accept TASK-038 or EPIC-009.

## Observed core results

| Scenario | Observation |
| --- | --- |
| Core batch | 34 distinct Jobs: 25 PASS, six intentional FAIL with native exit 7, two INCOMPLETE and one CANCELLED. Every executed Job had one run intent, one native start and one effect marker. |
| Thirty-Job soak | Exactly 30 sequential admissions: 24 PASS and six intentional FAIL, with one controller SIGTERM during an original running Job. The successor controller retained the original Job/key/run/deadlines and receipts; no workload was relaunched. |
| Missing output | Native exit 0 and workload pass remained INCOMPLETE/partial for the absent required output. Cleanup was verified; the next Job reused the same VM. |
| Run timeout | Original admitted run deadline produced timed_out and INCOMPLETE with complete evidence and verified cleanup. The budget was not reset. |
| Cancellation | Original running Job settled CANCELLED with native termination evidence, complete collection and verified cleanup. |
| Existing uncollected output | An existing unreadable required output retained its owned root and quarantine. One scoped restoration preceded public supplementary recollection and separate health. The original INCOMPLETE result remained unchanged. |
| Outside-root persistence and successor | The private marker survived ordinary cleanup and was byte-verified by later Jobs. A distinct post-recovery successor returned PASS/exit 0 with complete collection and verified cleanup; its exact recipe verified and removed the marker. No separate out-of-band absence query is claimed. |
| SSH observer loss | One host SSH watch client was interrupted. Bounded read retry and one live reconciliation recovered the same original run under the same absolute deadline. The original Job and distinct successor both returned PASS/exit 0 with complete collection and verified cleanup. |
| Saved-key payload conflict | The installed controller rejected a changed valid spec under an existing permanent key with CONFLICT, before new admission or guest work. All 42 existing finished receipts remained unchanged; resource occupancy stayed zero. |
| Retention and reuse | Core Job results, paginated events, complete logs and verified exports were captured before explicit revision-bound purge of only the new resolved Jobs. The retained source keys still returned their original admissions. |

The 30-Job soak's median prepare/run/collect/cleanup times were
1.951/3.238/4.923/4.789 seconds. These are measurements of this serial development
campaign, not portable latency or throughput guarantees.

## Large-log and transport measurements

The 50 MiB log contained exactly 52,428,800 bytes, SHA-256
`8565a714dca840f8652c5bae9249ab05f5fb5a4f9f13fbe23304b10f68252da2`.
Full public log delivery and export checks verified those bytes. Collection took
178.469 seconds within the admitted 900-second collect budget and used 218
complete SSH measurement pairs. Its prepare/run/cleanup times were
2.968/4.694/4.367 seconds; final verdict PASS, evidence complete, cleanup verified.

The large-log exchanges reported 162,667,008 archive bytes, including 161,011,584
runner bytes, and 71,405,490 response bytes. These are application byte counters;
network/protocol overhead was not measured. Compact-owned-transport fixture results in
[ADR-0019](../architecture-decision-records/0019-owned-runner-transport.md) do not
establish a compact-path real-VM throughput result.

Each batch Job retained a 20,126,448-byte host runner before explicit purge and
zero runner payload bytes after it. One incomplete SSH measurement pair across
the intentional controller restart remains a visible measurement gap; no missing
exchange is imputed as zero bytes or a successful completion.

On 2026-10-12, a read-only audit rechecked 40 retained core export manifests,
122 delivered files and 52,432,267 delivered bytes against their recorded sizes
and SHA-256 hashes. This audit read existing evidence; it admitted no Job and
performed no VM power operation.

## Limits and remaining criteria

The uncollected-output slice also admitted a queued successor before the resource
was reusable. It expired without reservation or workload. This shows that
quarantine blocks execution, not necessarily admission; callers should wait for
available/occupancy zero before a bounded successor. Its original key, spec,
deadlines and INCOMPLETE result remain preserved.

A resource-health caller timed out at 120 seconds while its original server
request continued and completed after 229.946 seconds. Durable completion and
public availability were confirmed before an exact saved-token/key cached reply;
no fresh health effect or deadline was introduced. Health planning scanned prior
Jobs and took 113.047 seconds. The SSH slice's coordinator also initially compared
equivalent UTC and local-offset deadline strings; later assessment compared their
absolute instants without changing the original native records or repeating the
signal. These caller failures are not hidden by the successful native outcomes.

Master clarified the use case on 2026-10-12: one machine, one user and that
user's already-running VM. Separate wrong SSH key/pin, lease and runner/root/file
incarnation fault experiments and packet/protocol overhead benchmarks are outside
this Epic's acceptance. They remain unobserved, not failed or passed gates. The
implemented identity fences and existing fixture checks remain intact.

Reproducibility means the tracked small core e2e command in
[TESTING.md](../../TESTING.md#small-isolated-caller-e2e), whose public-CLI assertions
already ran successfully; no separate live VM reproduction campaign is required.
The [campaign instructions](../../tests/qualification/README.md) remain available
for later explicitly selected execution. Canonical roadmap acceptance closes the revised core scope while preserving
these observations and their limits. None of the excluded experiments should be
proposed again as a completion condition.

Master revised TASK-039 on 2026-10-12 to the repository-owned small native
caller e2e instead of external real-project selection or application. Its
[observation and limits](TASK-039-small-e2e.md) are separate from this VM campaign. DF-001–003 release obligations and
DF-004 two-VM concurrency remain unchanged. VM power scenario qualification is
excluded from this Epic and must not be proposed again as its completion gate.
