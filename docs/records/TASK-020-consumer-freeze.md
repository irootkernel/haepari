# TASK-020 consumer integration freeze decision

Effective when TASK-020 becomes Accepted in the [canonical roadmap](../roadmap.md). This record defines F3 under HP-REQ-062; it does not accept the Task or Epic. The accepted Task commit and its review and verification evidence identify the frozen bytes.

## Surface and owners

| Surface | Frozen convention | Owner |
| --- | --- | --- |
| Host Make caller | `test-vm` invokes the host wrapper with `HAEPARI`, `RESOURCE` and a private `RUN_DIR`. `SUITE` defaults to `regression`; `MODE` defaults to `auto`. Suite selection and execution mode are independent; `all` contains only declared suites. | [Runnable project](../../examples/project-test/README.md), [TASK-017](TASK-017-project-integration.md) |
| Guest entrypoint | A distinct selected guest script runs in the managed Job context. Host Make orchestration is not the guest workload. Local and guest execution use the same declared argv, cwd and output recipe. | [Resource and runner](../contracts/resource-and-runner.md), runnable project |
| Consumer input identity | Regular non-secret selected bytes form the public tree inventory; explicit candidate/test subsets preserve paths, bytes and executable bits. `profile_id` and `profile_digest` refer to opaque consumer bytes. Saved run/spec/key files preserve original admission even when current inputs, profile or resource revision change. | [Artifact and profile](../contracts/artifact-and-profile.md), [TASK-018](TASK-018-opaque-inputs.md) |
| Manual completion | Job v2 manual declarations identify a selected scenario document and exact required scenarios. `haepari.manual-result/v1` binds Job, installation, profile and expected revision, observer and pass/fail/unobserved scenario assertions with evidence references. | [CLI and Jobs](../contracts/cli-and-jobs.md), [TASK-019](TASK-019-manual-jobs.md) |
| Manual lifetime | Successful owned preparation remains occupied in `awaiting_manual`; it is not a human PASS. Exact canonical completion replays its original receipt across settlement, restart and retention. Original deadlines, cancellation and physical cleanup fences govern retirement and one capacity return. | [Managed transitions](../contracts/managed-transitions.md), TASK-019 |

Managed execution remains the supported v0.1.0 request family; external-mode requests and resources remain unsupported. F3 freezes these implemented conventions and existing shapes. It does not re-freeze F1/F2 or turn private SQLite migration numbers into public schema versions. Installed-skill inventories, rich installation locks, product-specific evaluation and migration protocols remain optional consumer design. Evidence references record consumer assertions; they neither authorize filesystem effects nor make Haepari interpret product behavior.

## Verification and limits

The candidate must demonstrate one combined project loop through automatic success, meaningful native exit failure, occupied manual preparation, explicit synthetic completion, original saved-key replay and verified cleanup before a successor Job. Local public-CLI and fake SSH/native guest fixtures own that evidence; reproducible commands belong in [TESTING.md](../../TESTING.md).

The fake guest is an isolated native execution fixture. Scripted scenario records are synthetic assertions, not observed human product tests. No live VM, installed service, provider workload, host installation or release is qualified here. TASK-027 owns the exact packaged candidate's ordinary running-VM acceptance; accepted TASK-038 qualification is not repeated.
