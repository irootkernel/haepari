"""Isolated bootstrap/guard, descendant identity and portable inventory regressions."""
import hashlib
import json
import os
import platform
import shlex
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[2]
TC = ROOT / "tools/toolchain"
PINS = (ROOT / "toolchains.env").read_text()
VERSIONS = dict(line.split("=", 1) for line in PINS.splitlines() if line and not line.startswith("#"))
GO_PIN = VERSIONS["GO_VERSION"]
PYTHON_PIN = VERSIONS["PYTHON_VERSION"]
UV_PIN = VERSIONS["UV_VERSION"]
OS = platform.system().lower()
ARCH = {"aarch64": "arm64", "x86_64": "amd64"}.get(platform.machine(), platform.machine())


@pytest.fixture
def scratch():
    with tempfile.TemporaryDirectory(prefix="haepari-toolchain-test-", dir="/tmp") as directory:
        yield Path(directory).resolve()


def run(*args, cwd=ROOT, env=None):
    return subprocess.run([str(a) for a in args], cwd=cwd, env=env, capture_output=True, text=True, timeout=120)


def clean_env(**changes):
    env = os.environ.copy()
    for key in ("UV", "PYTHON", "PYTHON3", "GO_VERSION", "PYTHON_VERSION", "UV_VERSION", "GO", "GOFMT", "GOROOT", "GOENV", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "GOOS", "GOARCH", "GOEXPERIMENT", "PYTHONPATH", "PYTHONHOME", "UV_PYTHON", "UV_PYTHON_DOWNLOADS"):
        env.pop(key, None)
    env.update(changes)
    return env


def launcher(scratch):
    root = scratch / "copy with spaces"
    (root / "tools").mkdir(parents=True)
    shutil.copy2(TC, root / "tools/toolchain")
    (root / "toolchains.env").write_text(PINS)
    return root


def executable(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("#!/bin/sh\n" + text + "\n")
    path.chmod(0o700)


def cache_env(scratch):
    return clean_env(HAEPARI_TOOLCHAIN_CACHE=str(scratch / "owned cache"))


def test_real_exact_versions_and_newer_path_cannot_win(scratch):
    fake = scratch / "system"
    for name in ("go", "gofmt", "python3", "uv"):
        executable(fake / name, "echo unexpected-system >&2; exit 99")
    result = run(TC, "check", env=clean_env(PATH=str(fake) + ":" + os.environ["PATH"]))
    assert result.returncode == 0, result.stderr
    assert all(value in result.stdout for value in VERSIONS.values())
    assert "unexpected-system" not in result.stderr


@pytest.mark.parametrize("manifest", [None, "", "GO_VERSION=1.27.1\n", PINS + "GO_VERSION=1.27.2\n", PINS + "EVIL=$(touch bad)\n", PINS.replace(GO_PIN, "latest"), PINS + "OTHER=1.2.3\n"])
def test_invalid_manifest_has_no_effects(scratch, manifest):
    root = launcher(scratch)
    if manifest is None:
        (root / "toolchains.env").unlink()
    else:
        (root / "toolchains.env").write_text(manifest)
    result = run(root / "tools/toolchain", "python", "-c", "open('bad','w').close()", cwd=root, env=cache_env(scratch))
    assert result.returncode == 2
    assert not (root / "bad").exists()
    assert not (scratch / "owned cache").exists()


@pytest.mark.parametrize("key,value", [("UV", "/tmp/other"), ("PYTHON", "python3"), ("PYTHON3", "python3"), ("GO_VERSION", "0.0.0"), ("PYTHON_VERSION", "0.0.0"), ("UV_VERSION", "0.0.0"), ("GOTOOLCHAIN", "auto"), ("GOENV", "/tmp/other"), ("GOROOT", "/tmp/other"), ("GO", "go"), ("GOFMT", "/tmp/other"), ("GOFLAGS", "-mod=mod"), ("GOWORK", "/tmp/other"), ("GOOS", "other"), ("GOARCH", "other"), ("GOEXPERIMENT", "other"), ("PYTHONHOME", "/tmp/other"), ("PYTHONPATH", "/tmp/other"), ("UV_PYTHON", "python3"), ("UV_PYTHON_INSTALL_DIR", "/tmp/other"), ("UV_PROJECT_ENVIRONMENT", "/tmp/other"), ("UV_PYTHON_DOWNLOADS", "automatic")])
def test_conflicting_overrides_fail_before_commands(scratch, key, value):
    env = clean_env(**{key: value})
    result = run(TC, "python", "-c", "raise AssertionError('executed')", env=env)
    assert result.returncode == 2 and key in result.stderr and "conflicting" in result.stderr
    assert "AssertionError" not in result.stderr


@pytest.mark.parametrize("scope", ["go", "python", "uv"])
def test_missing_tools_never_download_or_fallback(scratch, scope):
    env = cache_env(scratch)
    fake = scratch / "spies"
    executable(fake / "curl", "touch " + shlex.quote(str(scratch / "downloaded")) + "; exit 99")
    env["PATH"] = str(fake) + ":" + env["PATH"]
    result = run(TC, "check", scope, env=env)
    assert result.returncode == 2 and "required=" in result.stderr and "path=" in result.stderr
    assert not (scratch / "downloaded").exists() and not (scratch / "owned cache").exists()


def test_go_mismatch_and_foreign_gofmt_symlink(scratch):
    env = cache_env(scratch)
    home = Path(env["HAEPARI_TOOLCHAIN_CACHE"]) / f"{OS}-{ARCH}/go/{GO_PIN}"
    executable(home / "bin/go", f"echo 'go version go0.0.0 {OS}/{ARCH}'")
    executable(home / "bin/gofmt", "exit 0")
    result = run(TC, "check", "go", env=env)
    assert result.returncode == 2 and "actual=go version go0.0.0" in result.stderr and f"required={GO_PIN}" in result.stderr
    executable(home / "bin/go", f"echo 'go version go{GO_PIN} {OS}/{ARCH}'")
    (home / "bin/gofmt").unlink()
    (home / "bin/gofmt").symlink_to("/bin/sh")
    result = run(TC, "check", "go", env=env)
    assert result.returncode == 2 and "same distribution" in result.stderr


@pytest.mark.parametrize("command", [["make", "fmt"], ["make", "test-prepare"], ["make", "deps"], ["make", "docs-update"], ["sh", "build/build.sh", "out/binary"], ["sh", "build/release.sh", "0.1.1", "out"]])
def test_guard_precedes_format_sync_and_outputs(scratch, command):
    root = launcher(scratch)
    for name in ("Makefile", "go.mod", "pyproject.toml", "uv.lock"):
        shutil.copy2(ROOT / name, root / name)
    for name in ("build", "cmd", "internal"):
        (root / name).mkdir()
    for name in ("build.sh", "release.sh"):
        shutil.copy2(ROOT / "build" / name, root / "build" / name)
    source = root / "cmd/dirty.go"
    source.write_text("package main\nfunc main( ){ }\n")
    before = source.read_bytes()
    result = run(*command, cwd=root, env=cache_env(scratch))
    assert result.returncode != 0
    assert source.read_bytes() == before and not (root / "out").exists()
    assert not (root / ".venv").exists() and not (root / "examples").exists()


def test_nested_go_build_uses_same_distribution(scratch):
    (scratch / "go.mod").write_text("module nested\n\ngo 1.27.1\n")
    (scratch / "child.go").write_text('package main\nimport("os";"os/exec";"fmt")\nfunc main(){fmt.Println(os.Getenv("GOTOOLCHAIN")); c:=exec.Command("go","build","-o",os.Args[1],"./leaf"); c.Stdout=os.Stdout;c.Stderr=os.Stderr;if c.Run()!=nil{os.Exit(1)}; c=exec.Command("go","version","-m",os.Args[1]);c.Stdout=os.Stdout;c.Stderr=os.Stderr;if c.Run()!=nil{os.Exit(1)}}\n')
    (scratch / "leaf").mkdir()
    (scratch / "leaf/main.go").write_text('package main\nfunc main(){}\n')
    result = run(TC, "go", "run", "child.go", scratch / "leaf-bin", cwd=scratch, env=clean_env())
    assert result.returncode == 0, result.stderr
    assert "local\n" in result.stdout and f"go{GO_PIN}" in result.stdout


def test_nested_python_and_python_only_scope(scratch):
    root = launcher(scratch)
    (root / "toolchains.env").write_text(PINS.replace(f"GO_VERSION={GO_PIN}", "GO_VERSION=9.9.9").replace(f"UV_VERSION={UV_PIN}", "UV_VERSION=9.9.9"))
    program = "import subprocess,sys,json; print(json.dumps([sys.executable,subprocess.check_output([sys.executable,'-c','import sys;print(sys.executable)'],text=True).strip()]))"
    result = run(root / "tools/toolchain", "python", "-c", program, env=clean_env())
    assert result.returncode == 0, result.stderr
    parent, child = json.loads(result.stdout)
    assert parent == child and PYTHON_PIN in parent


def test_copied_example_and_version_change_propagation(scratch):
    root = launcher(scratch)
    shutil.copy2(ROOT / "tools/sync_toolchains.py", root / "tools")
    example = root / "examples/project-test"
    shutil.copytree(ROOT / "examples/project-test", example, ignore=shutil.ignore_patterns("__pycache__", ".haepari-runs", "results"))
    result = run(root / "tools/toolchain", "python", root / "tools/sync_toolchains.py", "--check", env=clean_env())
    assert result.returncode == 0, result.stderr
    updated = PINS.replace(f"GO_VERSION={GO_PIN}", "GO_VERSION=9.9.9")
    (root / "toolchains.env").write_text(updated)
    assert run(root / "tools/toolchain", "python", root / "tools/sync_toolchains.py", "--check", env=clean_env()).returncode != 0
    assert run(root / "tools/toolchain", "python", root / "tools/sync_toolchains.py", "--write", env=clean_env()).returncode == 0
    moved = scratch / "relocated example"
    shutil.move(str(example), moved)
    shutil.rmtree(root)
    result = run("make", "toolchain-check", cwd=moved, env=clean_env())
    assert result.returncode == 0, result.stderr
    result = run("make", "test-vm", "RESOURCE=", cwd=moved, env=clean_env())
    assert result.returncode != 0 and "resource" in (result.stdout + result.stderr).lower()
    assert (moved / "toolchains.env").read_text() == updated


def test_generated_python_proxy_handles_runtime_spaces(scratch):
    # Use the same generator as the real E2E proxies/stubs.
    sys.path.insert(0, str(ROOT / "tests/e2e"))
    import test_project_cli
    spaced = scratch / "python path with spaces"
    spaced.symlink_to(sys.executable)
    original = sys.executable
    try:
        sys.executable = str(spaced)
        header = test_project_cli.python_script_header()
    finally:
        sys.executable = original
    proxy = scratch / "proxy with spaces"
    proxy.write_text(header + "import sys; print(sys.version_info[:3])\n")
    proxy.chmod(0o700)
    result = run(proxy)
    assert result.returncode == 0 and str(tuple(map(int, PYTHON_PIN.split(".")))) in result.stdout, result.stderr


def inventory(root):
    entries = sorted(p for p in root.rglob("*") if p.is_file() and p.name != "SHA256SUMS")
    (root / "SHA256SUMS").write_text("".join(f"{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.relative_to(root)}\n" for p in entries))


def test_relocated_verifier_no_tree_writes_missing_added_tampered(scratch):
    tree = launcher(scratch)
    shutil.copy2(ROOT / "packaging/verify_tree.py", tree / "verify_tree.py")
    shutil.copy2(ROOT / "packaging/verify-tree", tree / "verify-tree")
    (tree / "payload").write_text("original")
    inventory(tree)
    moved = scratch / "relocated package"
    tree.rename(moved)
    before = {str(p.relative_to(moved)): p.read_bytes() for p in moved.rglob("*") if p.is_file()}
    result = run(moved / "verify-tree", cwd=scratch, env=clean_env())
    assert result.returncode == 0, result.stderr
    after = {str(p.relative_to(moved)): p.read_bytes() for p in moved.rglob("*") if p.is_file()}
    assert before == after
    (moved / "payload").unlink()
    assert "missing: payload" in run(moved / "verify-tree", env=clean_env()).stderr
    (moved / "payload").write_text("changed")
    assert "tampered: payload" in run(moved / "verify-tree", env=clean_env()).stderr
    (moved / "extra").write_text("added")
    assert "added: extra" in run(moved / "verify-tree", env=clean_env()).stderr
    (moved / "extra").unlink()
    (moved / "extra").symlink_to("payload")
    assert "added: extra (symlink)" in run(moved / "verify-tree", env=clean_env()).stderr
    (moved / "payload").unlink()
    (moved / "payload").symlink_to(moved / "verify_tree.py")
    assert "tampered: payload (symlink" in run(moved / "verify-tree", env=clean_env()).stderr


def test_install_verifies_checksums_without_system_languages(scratch):
    root = launcher(scratch)
    fake = scratch / "bootstrap"
    # Official-download responses are isolated; corrupted bytes must never extract.
    executable(fake / "curl", '''destination=
while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then destination=$2; shift; fi
    shift
done
case "$destination" in
    *.json) printf '%s\\n' '{"filename":"go'''+GO_PIN+'''.'''+OS+'''-'''+ARCH+'''.tar.gz",' '"sha256":"0000000000000000000000000000000000000000000000000000000000000000"}' > "$destination" ;;
    *) echo corrupt > "$destination" ;;
esac''')
    marker = scratch / "unexpected-language-or-extraction"
    for name in ("go", "python", "python3", "tar"):
        executable(fake / name, "touch " + shlex.quote(str(marker)) + "; exit 99")
    env = cache_env(scratch)
    env["PATH"] = str(fake) + ":/usr/bin:/bin"
    result = run(root / "tools/toolchain", "install", env=env)
    assert result.returncode == 2 and "checksum mismatch" in result.stderr
    assert not marker.exists()
    assert not list((scratch / "owned cache").rglob("go"))


@pytest.mark.skipif(OS != "darwin" or ARCH != "arm64", reason="release packaging supports darwin/arm64")
def test_actual_release_relocation_metadata_notices_and_standalone_binary(scratch):
    output = scratch / "package output"
    result = run("sh", "build/release.sh", "0.1.1", output, env=clean_env())
    assert result.returncode == 0, result.stdout + result.stderr
    archive = output / "haepari-0.1.1-darwin-arm64.tar.gz"
    result = run("shasum", "-a", "256", "-c", archive.name + ".sha256", cwd=output)
    assert result.returncode == 0, result.stderr
    moved = scratch / "outside checkout with spaces"
    moved.mkdir()
    result = run("tar", "-xzf", archive, "-C", moved)
    assert result.returncode == 0, result.stderr
    tree = moved / "haepari-0.1.1-darwin-arm64"
    result = run(tree / "verify-tree", cwd=moved, env=clean_env())
    assert result.returncode == 0, result.stderr
    metadata = json.loads((tree / "build-metadata.json").read_text())
    assert metadata["go_version"] == f"go{GO_PIN}"
    assert metadata["toolchains"] == VERSIONS
    actual = run(TC, "go", "version", "-m", tree / "haepari", env=clean_env())
    assert actual.returncode == 0 and f"go{GO_PIN}" in actual.stdout
    notices = (tree / "THIRD_PARTY_NOTICES.txt").read_text()
    assert f"Go {GO_PIN} runtime/standard library" in notices
    for dependency in metadata["modules"]:
        assert f"{dependency['path']} {dependency['version']}" in notices
        assert dependency["path"] in actual.stdout and dependency["version"] in actual.stdout
    minimal = {"PATH": "/usr/bin:/bin", "HOME": str(scratch / "standalone home")}
    result = run(tree / "haepari", "version", "--json", cwd=moved, env=minimal)
    assert result.returncode == 0 and json.loads(result.stdout)["data"]["go_version"] == f"go{GO_PIN}", result.stderr
    for effect, expected in (("missing", "missing: LICENSE"), ("added", "added: injected"), ("tampered", "tampered: LICENSE")):
        bad = scratch / effect
        shutil.copytree(tree, bad)
        if effect == "missing":
            (bad / "LICENSE").unlink()
        elif effect == "added":
            (bad / "injected").write_text("added")
        else:
            (bad / "LICENSE").write_text("tampered")
        result = run(bad / "verify-tree", cwd=moved, env=clean_env())
        assert result.returncode == 1 and expected in result.stderr
    # Exercise packaging's actual metadata producer against incompatible notices.
    root = launcher(scratch)
    shutil.copy2(ROOT / "tools/release_metadata.py", root / "tools")
    (root / "THIRD_PARTY_NOTICES.txt").write_text(notices.replace(f"Go {GO_PIN} runtime", "Go 0.0.0 runtime"))
    result = run(root / "tools/toolchain", "python", root / "tools/release_metadata.py", tree / "haepari", scratch / "invalid-metadata.json", env=clean_env())
    assert result.returncode != 0 and "notices do not match" in result.stderr
    assert not (scratch / "invalid-metadata.json").exists()
    (root / "THIRD_PARTY_NOTICES.txt").write_text(notices)
    env = cache_env(scratch)
    home = Path(env["HAEPARI_TOOLCHAIN_CACHE"]) / f"{OS}-{ARCH}/go/{GO_PIN}"
    executable(home / "bin/go", 'if [ "$#" -eq 1 ]; then echo "go version go'+GO_PIN+' '+OS+'/'+ARCH+'"; else echo "binary: go0.0.0"; fi')
    executable(home / "bin/gofmt", "exit 0")
    # Run with the already-verified pinned Python; only Go's metadata response is faulted.
    result = run(sys.executable, root / "tools/release_metadata.py", tree / "haepari", scratch / "invalid-metadata.json", env=env)
    assert result.returncode != 0 and "release Go mismatch" in result.stderr
    assert not (scratch / "invalid-metadata.json").exists()


@pytest.mark.parametrize("scope", ["python", "uv"])
def test_runtime_mismatch_reports_required_actual_and_path(scratch, scope):
    env = cache_env(scratch)
    base = Path(env["HAEPARI_TOOLCHAIN_CACHE"]) / f"{OS}-{ARCH}"
    if scope == "python":
        executable(base / f"python/{PYTHON_PIN}/cpython-fixture/bin/python3", "echo 'CPython 0.0.0'")
        required, actual = PYTHON_PIN, "CPython 0.0.0"
    else:
        executable(base / f"uv/{UV_PIN}/uv", "echo 'uv 0.0.0'")
        required, actual = UV_PIN, "uv 0.0.0"
    result = run(TC, "check", scope, env=env)
    assert result.returncode == 2
    assert f"required={required}" in result.stderr and f"actual={actual}" in result.stderr and "path=" in result.stderr


def test_missing_or_foreign_go_formatter_is_refused(scratch):
    env = cache_env(scratch)
    home = Path(env["HAEPARI_TOOLCHAIN_CACHE"]) / f"{OS}-{ARCH}/go/{GO_PIN}"
    executable(home / "bin/go", f"echo 'go version go{GO_PIN} {OS}/{ARCH}'")
    result = run(TC, "gofmt", "-w", "anything.go", env=env)
    assert result.returncode == 2 and f"gofmt required={GO_PIN} actual=missing" in result.stderr
    (home / "bin/go").unlink()
    (home / "bin/go").symlink_to("/bin/sh")
    result = run(TC, "go", "build", env=env)
    assert result.returncode == 2 and "Go required=" in result.stderr and "actual=" in result.stderr


@pytest.mark.parametrize("field", ["GO_VERSION", "PYTHON_VERSION", "UV_VERSION"])
def test_each_version_field_propagates_without_manual_snapshot_edits(scratch, field):
    root = launcher(scratch)
    shutil.copy2(ROOT / "tools/sync_toolchains.py", root / "tools")
    script = root / "tools/sync_toolchains.py"
    assert run(sys.executable, script, "--write").returncode == 0
    updated = PINS.replace(f"{field}={VERSIONS[field]}", f"{field}=9.9.9")
    (root / "toolchains.env").write_text(updated)
    assert run(sys.executable, script, "--check").returncode != 0
    assert run(sys.executable, script, "--write").returncode == 0
    assert (root / "examples/project-test/toolchains.env").read_text() == updated
    assert run(sys.executable, script, "--check").returncode == 0



def test_diagnosis_is_read_only_even_with_a_fresh_home(scratch):
    # Keep the verified cache, isolate all ordinary Go/uv user cache locations.
    home = scratch / "pristine home"
    home.mkdir()
    cache_base = Path(os.environ.get("HAEPARI_TOOLCHAIN_CACHE", str(Path(os.environ.get("XDG_CACHE_HOME", str(Path.home() / ".cache"))) / "haepari/toolchains")))
    env = clean_env(HOME=str(home), XDG_CACHE_HOME=str(home / "cache"), XDG_CONFIG_HOME=str(home / "config"), HAEPARI_TOOLCHAIN_CACHE=str(cache_base))
    result = run(TC, "diagnose", env=env)
    assert result.returncode == 0, result.stderr
    assert list(home.iterdir()) == []


def test_prepare_formats_and_creates_locked_environment(scratch):
    root = launcher(scratch)
    # Exercise the real preparation recipes with dirty Go source and no venv.
    # Only documentation generation is isolated from the full design package.
    (root / "Makefile").write_text(
        (ROOT / "Makefile").read_text()
        + "\ndocs-update:\n\t@touch prepared-docs\n"
        + "\ntest-unit test-int test-e2e:\n\t@echo unexpected-test-stage; exit 99\n"
    )
    for name in ("check_format.py", "check_syntax.py"):
        shutil.copy2(ROOT / "tools" / name, root / "tools" / name)
    for name in ("pyproject.toml", "uv.lock"):
        shutil.copy2(ROOT / name, root / name)
    (root / "go.mod").write_text(f"module prepare-fixture\n\ngo {GO_PIN}\n")
    (root / "cmd").mkdir()
    (root / "internal/platform").mkdir(parents=True)
    (root / "internal/platform/platform.go").write_text("package platform\n")
    source = root / "cmd/dirty.go"
    source.write_text("package main\nfunc main( ){ }\n")
    original = source.read_bytes()
    env = clean_env(GOPROXY="off", UV_OFFLINE="1")
    result = run("make", "lint", cwd=root, env=env)
    assert result.returncode != 0 and "make fmt or make test-prepare" in result.stderr
    assert source.read_bytes() == original and not (root / ".venv").exists()
    result = run("make", "test-prepare", cwd=root, env=env)
    assert result.returncode == 0, result.stdout + result.stderr
    assert source.read_bytes() != original
    assert (root / ".venv/bin/python").is_file()
    assert (root / "prepared-docs").is_file()
    assert "unexpected-test-stage" not in result.stdout
    result = run(root / "tools/toolchain", "pytest", "--version", cwd=root, env=env)
    assert result.returncode == 0 and "pytest 9.1.1" in result.stdout
    formatted = source.read_bytes()
    result = run(root / "tools/toolchain", "python", root / "tools/check_format.py", env=env)
    assert result.returncode == 0 and source.read_bytes() == formatted
    (root / "toolchains.env").unlink()
    result = run("make", "help", cwd=root, env=cache_env(scratch))
    assert result.returncode == 0 and "toolchain-install" in result.stdout


@pytest.mark.parametrize("scope", ["go", "python", "uv"])
def test_failed_runtime_probe_keeps_actionable_diagnostics(scratch, scope):
    env = cache_env(scratch)
    base = Path(env["HAEPARI_TOOLCHAIN_CACHE"]) / f"{OS}-{ARCH}"
    if scope == "go":
        path = base / f"go/{GO_PIN}/bin/go"
        executable(path.parent / "gofmt", "exit 0")
    elif scope == "python":
        path = base / f"python/{PYTHON_PIN}/cpython-fixture/bin/python3"
    else:
        path = base / f"uv/{UV_PIN}/uv"
    executable(path, "echo broken-runtime >&2; exit 77")
    result = run(TC, "check", scope, env=env)
    assert result.returncode == 2
    assert "required=" in result.stderr and "actual=broken-runtime" in result.stderr and f"path={path}" in result.stderr


def test_selected_cache_survives_child_home_and_xdg_changes(scratch):
    cache_base = Path(os.environ.get("HAEPARI_TOOLCHAIN_CACHE", str(Path(os.environ.get("XDG_CACHE_HOME", str(Path.home() / ".cache"))) / "haepari/toolchains")))
    xdg = scratch / "initial xdg"
    (xdg / "haepari").mkdir(parents=True)
    (xdg / "haepari/toolchains").symlink_to(cache_base, target_is_directory=True)
    env = clean_env(XDG_CACHE_HOME=str(xdg))
    env.pop("HAEPARI_TOOLCHAIN_CACHE", None)
    program = "import os,subprocess,sys; os.environ['HOME']=sys.argv[1]; os.environ.pop('XDG_CACHE_HOME',None); subprocess.run([sys.argv[2],'python','-c','import sys; print(sys.executable)'],check=True)"
    home = scratch / "isolated home"
    home.mkdir()
    result = run(TC, "python", "-c", program, home, TC, env=env)
    assert result.returncode == 0 and PYTHON_PIN in result.stdout, result.stderr
    assert list(home.iterdir()) == []
