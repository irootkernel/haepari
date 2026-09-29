# ADR-0001: Independent Go CLI and daemon

Status: accepted for v0.1.0 design; TASK-001 bootstrap accepted.

## Context

The controller must remain usable while Aquarium and its tools are being changed or tested. Python, Go, and TypeScript were considered.

## Decision

Implement the controller, CLI and transient runner in Go. Distribute an independent stable executable and paired use-haepari skill. Scripts used by project recipes may still be shell or Python.

The bootstrap pins Go 1.27.1, pure-Go `modernc.org/sqlite` v1.59.0, and `golang.org/x/sys` v0.47.0 for the macOS arm64 target. `go.mod` and `go.sum` pin the dependency graph. The SQLite module is BSD-3-Clause and carries additional embedded-component notices; `x/sys` is BSD-3-Clause. Release packaging must retain applicable third-party notices. TASK-001 verifies the build, while TASK-004 checks the installed driver's macOS durability settings. Neither check alone qualifies a public release.

## Alternatives considered

Embedding the queue in Aquarium would couple recovery to plugin deployment. Python and TypeScript remain viable languages, but their ordinary runtime/packaging choices do not offer a needed advantage for this controller.

## Consequences and revisit condition

The product has its own release, installation and storage lifecycle. A single binary does not eliminate external UTM/SSH dependencies, and a pure-Go dependency set must be verified. Revisit only if a required platform/runtime capability outweighs this operational boundary.

The [architecture](../architecture.md), [contracts](../contracts/cli-and-jobs.md), and [requirements](../requirements.md) define the executable acceptance boundary. External platform facts are listed in [sources](../sources.md).
