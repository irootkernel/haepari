# ADR-0020: Local development scope and proportionate verification

Status: Accepted for v0.1.0 design by Master, 2026-10-12 (Design Revision 3.5).

## Context

Haepari exists to make local VM testing easy. The plan mixed generic execution guarantees with product-specific skill/migration acceptance and disruptive environment experiments. EPIC-009 already reduced its campaign, but remaining release and operator guidance still required DHCP expiry, logout and multi-day experiments. Exact-candidate evidence became coupled to repeated per-run approval and installed-service campaigns.

## Decision

VM power is exclusively operator-owned. Haepari has no VM start/stop issuer, including capacity management and recovery. Legacy uncertain power claims remain protected without reissue. An unstarted selected VM waits for the operator.

Support one macOS arm64 host, one OS user and one already-running registered persistent UTM macOS VM. General automatic and manual Jobs, sealed candidate/test inputs, collection, truthful verdicts, cleanup, reuse and original-run reconnection are the first-release product. Consumer recipes own installation/compatibility policy, skill discovery/behavior and product migration assertions.

HP-REQ-037–039 and HP-REQ-042–044, and V25–V31, are consumer-owned optional material. Their IDs/history remain; active Haepari tasks do not implement them. TASK-017–020 retain IDs/prerequisites with generic project inputs, manual completion and integration responsibilities. F3 freezes only the wrapper/guest entrypoint, immutable consumer environment reference and general manual results. Rich installation locks, inventories and migration evidence do not join F3. This scope change neither changes F1/F2 wire/runtime semantics nor removes existing recovery code or regression fixtures.

Mandatory fault invariants use bounded local or fake/native guest fixtures. The exact release candidate also needs a small running-VM cycle: success, meaningful failure, cancellation, timeout, declared logs/outputs, SSH observation reconnection, verified cleanup and successor reuse, plus one ordinary offline manual scenario. Local E2E uses an isolated foreground controller and test-owned process restarts. Installed-service interruption is requalified at packaging or when its supervision changes, not every task. Existing Accepted backend/operating evidence is not reopened by a temporary missing VM.

Operator-managed VM power on/off, guest reboot, host logout, actual DHCP expiry, multi-day uptime, multiple VMs, adversarial live identity experiments and separate performance campaigns are optional hardening. DF-001–004 retain uncertainty and scoped re-entry procedures but no v0.1.0 release deadline. The historical TASK-038 thirty-Job/50 MiB campaign remains optional tooling and historical evidence; it is not a recurring task or release gate. Performance targets require measurement only if advertised.

An established, authorized development scope names the repository/test commands, selected controller/resource, offline workload effects and finite budgets. It covers ordinary submission, blocking observation, original-run reconnection, semantic cancellation and owned Job cleanup. Reuse that authority for normal source/candidate changes inside the scope; do not require a new question or approval file for every Job or binary hash. Record actual candidate/input/result identities automatically. Existing effects.authorization_reference may identify this scope; it is provenance, not independent authorization. Registration alone grants no workload authority.

Operator VM power operations, installed-service replacement, host installation, evidence-loss disposal, providers, publication and effects outside the scope retain their separate authority. The full optional campaign tool keeps its existing exact manifest/authority-file interface. No approval is fabricated by writing a file. A missing selected live prerequisite fails that gate; independent local work continues.

Transient reads/connections retry and reattach within saved deadlines. A known test failure with collected evidence and verified cleanup returns the resource for another Job. Positive required-file absence follows the existing v2 INCOMPLETE-and-reuse policy. Unknown execution, changed target/root identity and existing uncollected evidence preserve the affected resource; timeout never proves termination. Local/VM independence is recommended with global_limit=2 and host_macos_guest_limit=1; installation defaults remain unchanged.

## Consequences and supersession

This decision narrows the Haepari release obligations of ADR-0008/0009 and the F3 portion of ADR-0016. Their consumer recipe rationale remains useful. It supersedes the former TASK-027 deadlines in DF-001–003 without retroactively changing TASK-016 acceptance or the F1/F2 decisions. Optional scenarios are unobserved, not passed.

Haepari still verifies its own packaged skill files/documented CLI flow, service lifecycle, storage compatibility and isolated self-tests. Default model/provider grading is not required. Aquarium and other consumers may select broader native recipe acceptance under their own authority; it is not a Haepari producer prerequisite.

## Verification

The documentation validator distinguishes mandatory, consumer-owned optional and deferred sets, preserving stable non-contiguous IDs and rejecting invalid ownership/references. The current new live gate is TASK-027; its prerequisite closure includes TASK-028 for final handoff. Accepted historical VM evidence does not require a new live campaign for TASK-017–026. Per-requirement case ownership and duplicate traceability rows are checked independently of global case coverage. The [review disposition](../review-disposition.md#local-development-scope-agreement-design-revision-35) records semantic review and the limits of automated checks. Runtime changes remain with the revised roadmap tasks. This ADR and its documentation checks assert no new live product observations.
