# Developer toolchains

`../toolchains.env` is the sole manually edited version authority for Go,
CPython and uv. The Go module's minimum remains 1.27.1;
`pyproject.toml` continues to support Python >=3.10. Runtime pins select the
verified development environment, independently of minimum support policy.
No Rust, TypeScript or Ruby toolchain is part of this project.

## Preparation and execution

From the checkout root:

```sh
make help
make toolchain-install
make toolchain-check
make test-prepare
make test-toolchain
make test
make docs-check
```

The POSIX `tools/toolchain` bootstrap needs shell, curl, tar, shasum and ordinary
POSIX utilities, but no installed Python or Go. Explicit installation downloads
a versioned Go archive from Google's distribution endpoint, verifies its SHA-256
against the official go.dev JSON catalog, and downloads a versioned uv archive
and its published SHA-256 from astral-sh/uv releases. That exact uv installs
exact CPython with its managed-download integrity checks. Nothing modifies
Homebrew, the global PATH, login configuration or default host commands.

The cache is `${XDG_CACHE_HOME:-$HOME/.cache}/haepari/toolchains/<os>-<arch>/`,
with version-specific `go/<version>`, `uv/<version>` and `python/<version>`
subtrees. `HAEPARI_TOOLCHAIN_CACHE` may select another absolute Haepari-owned
cache base, including paths containing spaces. Guarded processes export the
resolved selection so children that isolate HOME or XDG locations keep the same
pins and prepared cache. Keep it outside the checkout and
extracted release trees. Installation is explicit and idempotent; existing
incorrect toolchains fail validation rather than being silently replaced.

`tools/toolchain diagnose` and `check` are read-only. Optional `go`, `python`,
`uv` or `all` scopes select prerequisites. Runtime failures identify required
version, observed value and executable path. Missing/malformed/duplicate pins,
foreign executable symlinks and conflicting Go/Python/uv selectors (including
`GO`, `PYTHON`, `UV` and environment version overrides) fail before
commands run. Unset conflicting shell selectors to use the manifest. Exact `GOTOOLCHAIN=local` and `GOENV=off` are compatible; auto or
path overrides that redirect execution are refused. Go/gofmt must resolve to
the same pinned distribution. Go children inherit its bin directory first in
PATH and `GOTOOLCHAIN=local`, with persistent Go environment configuration off. Go telemetry is directed to
`/dev/null` through its process-local telemetry-directory override, preventing
counter writes or uploader activity without changing the user telemetry mode.
Scoped Python stdlib commands do not require Go, uv or Go module dependencies.

Use `tools/toolchain go <args>`, `tools/toolchain python <args>` and
`tools/toolchain pytest <args>` for focused checks. Python children use
`sys.executable`; executable test proxies quote that runtime through a portable
shell/Python header, including paths containing spaces. `make deps`, also called by `make test-prepare`,
prepares Go dependencies and runs exact-Python `uv sync --locked --group dev`.
Normal commands never download languages or fall back to system interpreters.
Pytest validates the existing locked environment without synchronizing it.
`make test-prepare` checks all required toolchains before changes, prepares locked
dependencies, applies Go formatting, runs static/build checks, and regenerates
and validates snapshots and documentation. It runs no unit, integration or E2E
tests. `make fmt`, `make deps` and `make docs-update` remain available individually;
`make lint` and `make docs-check` retain their non-rewriting checks.
Python syntax checks compile in memory and docs checks do not write manifests.

## Generated and release boundaries

`make docs-update`, also called by `make test-prepare`, copies the root manifest and common launcher into the runnable
example, generates documentation manifests and checks both. `make docs-check`
refuses stale snapshots. The copied example uses only its local snapshot and
the selected user's toolchain cache; no original checkout absolute path is
embedded. Its shell-only test and guest entrypoint remain runtime-independent.

Direct build/release scripts guard runtimes before creating output directories.
Release packaging compares actual `go version -m` runtime and dependency
versions with root pins, generated `build-metadata.json` and distributed
`THIRD_PARTY_NOTICES.txt`, refusing mismatches. Archives carry the manifest,
common launcher and `verify-tree` entrypoint. After relocation, explicitly
prepare its runtime with `./tools/toolchain install` when necessary, then run
`./verify-tree`. Verification writes no bytecode or cache into the inventory
tree, and reports missing, added and tampered entries, including symlink
replacements. The product `haepari` binary itself needs no Go or Python runtime.
A local packaging smoke is not publication or installed-service/VM qualification.

## Upgrades

Edit only root `toolchains.env`, then run explicit `make toolchain-install`
and `make test`; its preparation stage handles dependencies, formatting and
generated documentation before the test stages. For focused work, run
`make test-prepare` before the selected tests. Review `go version -m` and
notices before packaging. Change
minimum support, dependency locks or licenses only when the upgrade actually
requires it; do not bulk-upgrade dependencies. Preserve historical candidate
and runtime observations in records. Never hand-edit derived example pins or
its launcher. This sequence does not authorize installation of the product,
services, VM operations, commits or publication.
