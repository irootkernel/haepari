---
name: use-haepari
description: Operate the installed Haepari release CLI for durable local/UTM macOS jobs and advisory locks — submit once with a persisted request key, wait and reconnect by job ID, cancel, recover, and follow the lock owner/borrower flow. Use when asked to run, inspect, wait for, cancel, or queue work through the installed haepari binary or mulgae-review lock; not for installing, publishing, or VM setup.
---

# Use Haepari

Haepari runs one durable per-user controller behind a private Unix socket. The `haepari` CLI submits sealed jobs, blocks on results, and coordinates cooperating tools with advisory locks. Work survives your process exiting; the controller owns execution.

## Before first use

1. Confirm the installed binary and its capabilities instead of guessing from a version string:

   ```sh
   haepari version --json
   haepari capabilities --json
   ```

   `capabilities` lists frozen protocol capabilities (`managed-execution/v1`, `durable-observation/v1`, `advisory-lock/v1`, `managed-recovery/v1`, `artifact-delivery/v1`, `evidence-retention/v1`), the compiled bootstrap backend, and — separately — backends qualified for live use. Compiled support is not live readiness; `qualified_backends` is empty until real qualification is recorded.

2. Confirm the controller is reachable (`HAEPARI_HOME` selects the installation): `haepari resources list --json`. A connection failure is a prerequisite gap; do not start a second controller against the same root.

3. Work only inside the established authorization scope for this installation (repository, resource, offline effects, budgets). Having the CLI, a job ID, or an inbox item is not authorization to install tools, run providers, reset foreign state, or publish.

## Submit once; never resubmit speculatively

- Persist the request key and the exact spec bytes **before** the first `submit`. Use a descriptive unique key (1–128 chars: `A-Za-z0-9._-`, starts alphanumeric).
- After an uncertain reply (timeout, exit 6 transport-uncertain), resend the **same key and same spec**; the controller returns the original admission. Never generate a new key to "retry" — that can launch a second workload.
- The same key with a changed payload is rejected as `CONFLICT`.
- `run` = submit + wait. `submit` returns immediately with the job ID.
- Deadlines: submit fills omitted active phase budgets from the accepted configuration; explicit deadlines stay exact.

## Observe truthfully

- Block with `wait <job-id> --timeout <duration> --json` (max 24h). Do not poll `inspect` in a loop.
- Reconnect after any disconnect by re-running `wait`/`inspect`/`events`/`logs` on the **same job ID**. A caller wait timeout (exit 5) does not cancel or change the job.
- Job observation exit codes are the contract: 0 PASS (or command OK), 1 FAIL, 3 INCOMPLETE, 4 CANCELLED, 5 wait timeout, 2 invalid/conflict/`RESULT_UNAVAILABLE`, 6 uncertain storage/transport. On any failure, read the envelope `error.code` and its `retry` classification (`none`, `retry_same_key`, `reconcile`) — exit 6 alone does not tell you which condition applied.
- Report results exactly as returned. Never synthesize PASS, rerun uncertain work, or treat a missing result as failure. A known admission whose retained result is unavailable (`RESULT_UNAVAILABLE`) is recovered by the same request key, not a new submission.
- `logs` uses byte offsets and a file identity; resume a reported gap from its stated offset/identity.

## Cancel and recover

- `cancel <job-id> --expected-revision <n> --request-key <saved-key> --json`: inspect first for the current revision. An acknowledged cancel is a request, not termination proof — wait for and report the **actual immutable final verdict**: usually CANCELLED (exit 4), but an already-known earlier FAIL or INCOMPLETE stays recorded and is never rewritten by a late cancel (exit 1/3 accordingly).
- For stuck/uncertain jobs: `inspect`, then read-only `job recover <id> --plan --json` / `resource recover <id> --plan --json`. Apply only with the returned token and the saved key. Attestation never yields PASS; unknown execution stays occupied — never kill an arbitrary PID or delete state to "free" a resource.

## Advisory locks (e.g. `mulgae-review`)

A lock coordinates callers; it grants no execution permit and counts toward no managed capacity.

**Acquiring owner** (the one process that called `acquire`):

1. Persist key + payload, then `haepari lock acquire <lock-id> --request-key <key> --holder <label> [--wait] --json`. A timeout (exit 5) does not cancel the request; resend the same key or `lock wait <request-id> --json`. Lock exit 4 means the request ended without a current grant; read `current.state` instead of applying Job verdict meanings.
2. Abandoning while queued: a wait or observation timeout is **not** cancellation, and a queued request has no default deadline. Cancel your own queued request with `lock cancel <request-id> --json`; if the grant raced and was already issued, that same cancel releases it. If you lost the request ID, recover it by resending the identical `acquire` — same key and payload, without `--wait` — which replays the stored request and its current state, then cancel that ID. Request cancellation is an owner action; a borrower never cancels.
3. Keep the grant alive while your work runs: renew via `lock renew <grant-id> --json`, or run `lock keepalive <grant-id> --json` alongside.
4. **Release on every completion path** — success, failure, and cancellation: `lock release <grant-id> --json`. Stopped renewals end the grant after one liveness window; every grant ends at its max hold.

**Nested borrower** (received `HAEPARI_LOCK_GRANT` with installation/lock/grant IDs and generation):

Connecting or listing successfully is **not** proof that the grant is valid — the CLI only checks the reference's syntax. Validate read-only yourself before using it:

1. Page through `haepari locks list --lock <your intended lock id> --limit 100 [--cursor <next_cursor>] --json` until you find the referenced `grant_id` or the pages are exhausted. Fail closed — do not conclude the grant is valid — when a page has `more=true` but no `next_cursor`, repeats a cursor you already used, or has a missing/unparsable server `now`.
2. Require the reference's `lock_id` to equal your caller-known intended lock and each page's `data.installation_id` to equal the reference's installation. On the **same page** that matched, also require: the matched request's `installation_id` equals that installation; its `lock_id` equals both the reference and intended lock; `grant_id` and `generation` equal the reference's; `state` is `granted`; and that page's server `now` is before both non-missing `liveness_deadline` and `max_hold_deadline`. Missing or invalid time values cannot establish validity.
3. Classify failures yourself and return them to the owning integration — they are validation outcomes, not automatic CLI errors (a successful list may still exit 0 for an invalid reference): wrong installation or wrong lock → `CONFLICT`; `grant_id` never found after exhausting the pages of the intended lock → `NOT_FOUND`; wrong `generation`, a non-`granted` (including ended) state, or a passed deadline → `LOCK_EXPIRED`.

Then **use** the grant only — for example, skip acquiring that lock and proceed with your work. Never renew, release, cancel, or force-release the parent's request/grant — including on your own failure or cancellation — and never reacquire or silently acquire under a new key.

## Hard boundaries

- A caller whose parent context occupies capacity cannot submit/acquire (`NESTED_ACQUISITION_REJECTED`) or block on non-terminal jobs (`wait`, `events --follow`, `logs --follow` → `NESTED_WAIT_REJECTED`). Non-blocking reads and terminal observation remain allowed.
- Supported scope: one macOS arm64 machine, one user, already-running registered VMs (persistent; never reset). No MCP transport is required; external leases are not exposed in v0.1.0.
- Never: install into production during packaging, auto-enroll UTM VMs, bypass permissions, disable SSH host-key checking, or delete `~/.aquarium-dev`, foreign Git hooks, Sorage data, or host skills.
