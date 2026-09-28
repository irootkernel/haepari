# Contract examples

These examples are design fixtures, not runnable installation scripts. Values in angle brackets are unresolved placeholders and must be rejected at admission. JSON parsing alone does not make a spec executable. The implemented CLI schemas become authoritative once frozen at gate F1 (TASK-012) for the managed core, gate F2 (TASK-016) for VM fields and gate F3 (TASK-020) for the consumer integration contract, including the environment lock.

The resource definition is operator-owned and separate from a project job. The environment lock is consumer-owned test input, not host inventory. Examples omit private local VM package paths, real SSH identity material, credentials and actual release hashes.

A host Make wrapper should prepare its immutable bundle and resolved JSON before calling `haepari run`. The guest entrypoint is different and must reject execution without verified guest/job lease context. The Make example deliberately refers to project-owned packaging code that the adopting producer must implement.

## Deferred external execution templates

`resource-external.template.json` declares a generic caller-executed resource. `lease-request.template.json` and `lease-continuation.template.json` contain bounded non-secret intent metadata. Request keys and private owner-file material are deliberately not included. Replace placeholders and resolve native capabilities before use. These preserve the R2 proposal for post-v0.1.0 re-entry, subject to B1/B2. They are not v0.1.0 examples, CLI support or release tests. See [external leases](../contracts/external-leases.md) and [Mulgae recovery](../integrations/mulgae-review.md).

The [local Job](job-local.template.json) and [local resource](resource-local.template.json) illustrate managed execution. All examples are provisional design fixtures until the consumer contract freeze.

The [controller configuration](config.template.json) demonstrates every field of the closed `haepari.config/v1` shape defined by the [configuration contract](../contracts/configuration.md), including the default global limit of one and host macOS guest limit of one. Resources are registered separately. Placeholder paths and identities are not executable defaults.

The [local result](result-local.template.json) shows distinct job, resource availability, workload, evidence, cleanup, verdict and `timings` fields. The [VM job](job-vm.template.json) uses a queue deadline that leaves room for one maximal job ahead on a capacity-one resource. It is illustrative; no result schema implementation exists yet.

The [baseline registration](baseline-registration.template.json) binds an operator-owned powered-off package, observed VM UUID, SSH identity and real restore qualification receipt. It is not a command to clone or operate a VM.

The [lock resource](resource-lock.template.json) defines the advisory `mulgae-review` lock from the [lock contract](../contracts/locks.md): capacity one, default and maximum hold times, and a default queue deadline. It has no adapter or execution mode.
