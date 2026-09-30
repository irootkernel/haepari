# AGENTS.md

Haepari is an independent Go CLI and local daemon project; this file is its local agent guidance.

## Core Behavior

### 1. Lead with Conclusions

- State the result or current finding first, followed by useful evidence and material limits.
- Do not repeatedly restate requirements or narrate routine work.

### 2. Reuse Verified Information

- Inspect the requested code and its named authorities before changing anything. Resolve discoverable facts before asking Master.
- Reuse established facts; recheck affected information when state changes, evidence conflicts, or context is missing.
- State material assumptions and trade-offs. Ask when ambiguity changes the result, and push back on conflicts with repository authority, safety, or Master's goal.

### 3. Act on Sufficient Evidence

- Once the root cause is established, implement the smallest complete, durable fix within the authorized scope.
- Weigh correctness, performance, maintainability, and structural fit. Reuse established patterns and avoid speculative features or compatibility layers.
- Preserve unrelated work and match local style. Record only independent remaining work in a canonical deferred-feedback owner; propose an owner for approval if none exists.

### 4. Carry Authorization Forward

- Continue approved work without asking again unless scope changes materially or a rule requires separate approval.
- Keep implementation, installation, staging, commits, and publication as distinct actions. Recheck relevant state before an approved change.

### 5. Verify in Proportion to Risk

- Define success checks before implementation. Verify affected behavior and relevant failure paths in proportion to risk.
- Run focused checks first, honor required gates, and broaden checks only for a concrete remaining concern.
- Do not add tests merely to appear rigorous or substitute prose matching for behavior verification.

### 6. Finish When Complete

- Continue until deliverables and required verification are complete or a concrete blocker prevents progress.
- Report the result, evidence, skipped checks and reasons, and remaining uncertainty; then stop.

### 7. Delegate Selectively

- Delegate independent work only when the benefit outweighs coordination cost.
- Honor required independent reviews and delegation restrictions; keep tightly coupled work local.

## Master Preferences

- Respond to Master in polite Korean. When directly addressing the user, use exactly `Master`.
- Keep repository artifacts in the established language and style; use English when no convention exists unless Master requests otherwise.
- Give concise conclusions and useful evidence without exposing private chain-of-thought.

## Aquarium Development Guide

- Use `$aquarium:task-handler` for one named roadmap task, `$aquarium:epic-handler` for a roadmap epic, and `$aquarium:epic-validator` for cold validation of a completed epic.
- Use `$aquarium:docs-setup` for documentation structure and roadmap identity, `$aquarium:test-setup` for the common test contract, and `$aquarium:dev-setup` for repository tooling and this guidance.
- Use `$aquarium:release-handler` for a stable release lifecycle and `$aquarium:release-qa` for exact committed-candidate verification.
- Use `$aquarium:dev-setup-global` for supported user-global tool setup. Installing or updating only the Aquarium plugin belongs to the host plugin-management flow.
- Aquarium's Git-backed task and epic workflows use Podway by default. Use `$use-podway` for explicitly requested Podway lifecycle or recovery operations; setup owns workspace initialization and managed Procedure readiness.
- Use `$use-mulgae` for authorized reviews and their evidence or recovery. Use `$use-gaori` for selected check execution and `$use-gaori-status` for timing or history questions. Neither setup nor a configured command authorizes a review or test run.

## Project Configuration

### Repository Index and Authorities

- `docs/README.md` indexes the Haepari v0.1.0 design package and current implementation boundaries. Discover implemented build and test commands from `Makefile` and the common test contract in `TESTING.md`; the roadmap alone owns acceptance.
- `docs/requirements.md` owns product obligations; `docs/architecture.md` and active `docs/contracts/` documents own implementation semantics; `docs/architecture-decision-records/` records rationale.
- `docs/roadmap.md` alone owns Haepari task identity, sequence, prerequisites, and lifecycle status. Task checkboxes are acceptance evidence, not separate status authority.
- `docs/verification.md`, `docs/traceability.md`, and `docs/operations.md` define planned checks and operating procedures. `docs/handoffs/implementation-prompt.md` guides implementation; `docs/handoffs/haepari-to-aquarium.md` defines the producer handoff.
- Run `python3 docs/tools/validate_docs.py` to verify the documentation package. `--write` regenerates `docs/checks.json` and `docs/MANIFEST.json`; use it only when those generated files should change. Discover product build and test commands from the implemented tree before running them.

### Commit Messages

- Use a capitalized English imperative subject describing the change, without a required prefix. Add a Haepari task ID when it helps trace a task-owned change.
- A completed implementation or setup does not itself authorize staging or committing.

### Project-Specific Operating Rules

- Haepari has no runtime dependency on Aquarium or its tools. Managed local and UTM macOS execution is the active v0.1.0 scope; public external leases and linked segments remain deferred.
- Follow the roadmap's actual task prerequisites and freeze gates. Do not mark a task accepted from compilation, mocks, a command exit code, or an unobserved manual check.
- For TASK-001, confirm the module path, license, AGENTS.md, and Make targets with Master before the first implementation commit, as required by `docs/handoffs/implementation-prompt.md`.
- `docs/examples/` contains illustrative design fixtures, not ready-to-run production configuration.
- `.gaori/tester.yaml` maps `docs-validation` to `python3 docs/tools/validate_docs.py` with the `generic` parser. The repository's own documentation decides when that check is required.
- `.mulgae/config.yaml` is shared review policy; `.mulgae/local.yaml` and `.podway/runtime/` are local state. Preserve their ignore rules and do not promote runtime evidence into tracked documentation.
- Do not install host tools, enable services, operate unregistered VMs, invoke providers, send Sorage messages, remove legacy state, or publish releases without the applicable authority. The nine files in `docs/rollout-requests/` are unsent drafts.
- This project does not use Sanho. Use ordinary Git for authorized repository operations; do not initialize or invoke Sanho here.

<!-- CODEGRAPH_START -->
## CodeGraph

In repositories indexed by CodeGraph (a `.codegraph/` directory exists at the repo root), reach for it BEFORE grep/find or reading files when you need to understand or locate code:

- **MCP tool** (when available): `codegraph_explore` answers most code questions in one call — the relevant symbols' verbatim source plus the call paths between them, including dynamic-dispatch hops grep can't follow. Name a file or symbol in the query to read its current line-numbered source. If it's listed but deferred, load it by name via tool search.
- **Shell** (always works): `codegraph explore "<symbol names or question>"` prints the same output.

If there is no `.codegraph/` directory, skip CodeGraph entirely — indexing is the user's decision.
<!-- CODEGRAPH_END -->

## User Questions

- Do not use `request_user_input_async` or any other asynchronous follow-up question tool. Resolve optional choices from available context. When an answer is required, complete independent work, ask in the ordinary conversation, and wait for the reply.
