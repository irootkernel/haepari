# ADR-0010: Manual results are explicit observations

Status: accepted for v0.1.0 design; implementation pending.

## Context

Skill discovery and behavior need installed-session evidence, and some GUI/workflow checks require a human.

## Decision

Manual jobs retain the same resource lease and wait for a revision-bound observation record. Installation checks, discovery, behavior, live-provider execution and human results remain separate evidence classes.

## Alternatives considered

Environment-ready, a file checksum, or an agent pasting a skill prompt cannot imply hands-on completion. Default automated LLM grading adds cost and evidence ambiguity.

## Consequences and revisit condition

Unobserved required scenarios produce INCOMPLETE. Explicitly authorized live tests can run, but Haepari does not decide product semantics or certify all possible model behavior.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).
