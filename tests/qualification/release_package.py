"""Explicit installed release-package qualification; never run by the common gate.

Unpacks the built release archive, verifies it with the packaged complete-tree
helper plus an independent inline inventory (missing/added/tampered negatives
plus dangling-symlink-added and recorded-file symlink-replacement negatives on
scratch copies), exercises the standalone binary outside the checkout, then
installs ONLY one unique release LaunchAgent instance (``h7-t25-rel-<scenario>``)
with its own private state root. Service plan/install/uninstall AND the job
clients all run against ONE scoped test-owned child HOME, so the generated
plist lives inside the audited test tree (launchctl bootstraps an owned plist
from any path; see internal/platform/supervision_darwin_test.go). Native flows:
submit/wait PASS, same-key replay and changed-payload conflict, wait-timeout
reconnect, manual pass and unobserved-INCOMPLETE, queued cancel with terminal
exit 4, package lock lifecycle, documented read-only borrower validation through public `locks list` paging (valid reference, foreign installation, wrong intended lock, stale generation, unknown grant, ended reference; consumer classifications distinct from CLI exits; parent grant untouched),
installed-service TERM/KILL reattachment with one workload effect, and the
release-service refusal guards. A bounded filesystem audit snapshots the fully
owned test HOME BEFORE install and AFTER uninstall; the expected Haepari-owned
empty ``Library/LaunchAgents`` parents are explicitly allowlisted and reported.
Real-host observation is limited to read-only non-secret sentinels (existing
dev plist digest/mode, the io.rootkernel.haepari* plist name set, selected
top-level metadata). No stable/default service, VM, credential, provider or
host toolchain is touched; no whole-host write tracing is claimed.
"""

import argparse
from datetime import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import shutil
import stat
import struct
import subprocess
import sys
import tarfile
import tempfile
import time

DEV_PLIST = "io.rootkernel.haepari.dev.epic004-task016.plist"
REAL_SENTINEL_TOPS = [".aquarium-dev"]
REAL_PLIST_GLOB = "io.rootkernel.haepari*.plist"
ALLOWED_NEW_DIRS = {"Library", "Library/LaunchAgents"}


def _digest_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _tree_digest(root: Path, names: list[str]) -> str:
    """Independent rolling haepari-tree-sha256/v1 over the sorted selection."""
    digest = hashlib.sha256(b"haepari-tree-sha256/v1\0")
    for name in sorted(names, key=lambda value: value.encode("utf-8")):
        path = root / name
        data = path.read_bytes()
        encoded = name.encode("utf-8")
        executable = bool(path.stat().st_mode & 0o111)
        digest.update(struct.pack(">Q", len(encoded)) + encoded + bytes([executable]))
        digest.update(struct.pack(">Q", len(data)) + hashlib.sha256(data).digest())
    return digest.hexdigest()


def _snapshot(path: Path) -> dict | None:
    """Non-secret file metadata: existence, mode, size, digest. No credentials."""
    try:
        info = path.lstat()
    except FileNotFoundError:
        return None
    entry: dict = {"mode": stat.S_IMODE(info.st_mode), "directory": stat.S_ISDIR(info.st_mode)}
    if stat.S_ISREG(info.st_mode):
        entry["size"] = info.st_size
        entry["sha256"] = _digest_bytes(path.read_bytes())
    return entry


def _snapshot_tree(root: Path) -> dict[str, dict]:
    captured: dict[str, dict] = {}
    for path in sorted(root.rglob("*")):
        captured[str(path.relative_to(root))] = _snapshot(path)  # type: ignore[index]
    return captured


def _diff_trees(before: dict[str, dict], after: dict[str, dict]) -> list[str]:
    changes = []
    for name in sorted(set(before) | set(after)):
        if name not in before:
            changes.append(f"extra: {name} ({after[name]})")
        elif name not in after:
            changes.append(f"missing: {name} ({before[name]})")
        elif before[name] != after[name]:
            changes.append(f"changed: {name} ({before[name]} -> {after[name]})")
    return changes


def _verify_with_packaged_helper(package: Path) -> None:
    """Run the packaged complete-tree helper; it must pass on the pristine tree."""
    completed = subprocess.run([sys.executable, str(package / "verify_tree.py"), str(package)],
                               text=True, capture_output=True, timeout=60, check=False)
    assert completed.returncode == 0, (completed.stdout, completed.stderr)


def _inventory_mismatches(root: Path) -> list[str]:
    """Independent lstat-based inventory: never follows symlinks.

    Reports missing/added/tampered entries, treating unlisted symlinks and
    special files as added and a recorded regular file replaced by a symlink,
    special file or directory as tampered even with identical target bytes.
    Ordinary structural directories stay unlisted without complaint.
    """
    inventory = root / "SHA256SUMS"
    if not stat.S_ISREG(inventory.lstat().st_mode):
        return ["tampered: SHA256SUMS (not a regular file)"]
    recorded = {}
    for line in inventory.read_text().splitlines():
        value, _, name = line.partition(" ")
        name = name.strip()
        if not name:
            value, _, name = line.partition("*")
        recorded[name.strip()] = value.strip()
    kinds: dict[str, str] = {}
    def scan_error(error: OSError) -> None:
        raise error

    for current, dirnames, filenames in os.walk(root, followlinks=False, onerror=scan_error):
        for name in list(dirnames) + filenames:
            full = os.path.join(current, name)
            rel = os.path.relpath(full, root)
            mode = os.lstat(full).st_mode
            if stat.S_ISLNK(mode):
                kinds[rel] = "symlink"
            elif stat.S_ISDIR(mode):
                kinds[rel] = "directory"
            elif stat.S_ISREG(mode):
                kinds[rel] = "regular"
            else:
                kinds[rel] = "special"
    mismatches = []
    for name in sorted(set(recorded) | set(kinds)):
        kind = kinds.get(name)
        if kind is None:
            mismatches.append(f"missing: {name}")
        elif kind in ("symlink", "special"):
            if name == "SHA256SUMS" or name in recorded:
                where = "SHA256SUMS" if name == "SHA256SUMS" else "recorded regular file"
                mismatches.append(f"tampered: {name} ({kind} replaces {where})")
            else:
                mismatches.append(f"added: {name} ({kind})")
        elif kind == "directory":
            if name in recorded:
                mismatches.append(f"tampered: {name} (directory replaces recorded regular file)")
        elif name == "SHA256SUMS":
            continue
        elif name not in recorded:
            mismatches.append(f"added: {name}")
        elif _digest_bytes((root / name).read_bytes()) != recorded[name]:
            mismatches.append(f"tampered: {name}")
    return mismatches


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--scenario", default="release-package", help="unique owned observation name")
    parser.add_argument("--version", default="0.1.0")
    args = parser.parse_args()
    assert re.fullmatch(r"[a-z][a-z0-9-]{0,31}", args.scenario), "scenario must be a short lowercase slug"
    instance = f"h7-t25-rel-{args.scenario}"
    assert re.fullmatch(r"[a-z0-9][a-z0-9-]{0,47}", instance)
    if platform.system() != "Darwin" or platform.machine() != "arm64":
        sys.exit("release-package qualification requires a macOS arm64 host")
    if not args.archive.is_file():
        sys.exit(f"missing archive: {args.archive}")

    spec = importlib.util.spec_from_file_location(
        "haepari_local_fixture", Path(__file__).resolve().parents[1] / "e2e" / "test_local_cli.py")
    fixture = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(fixture)

    parent = Path(tempfile.mkdtemp(prefix=f"hp-relpkg-{args.scenario}-", dir="/tmp")).resolve()
    evidence: dict[str, object] = {"scenario": args.scenario, "instance": instance, "scratch": str(parent)}
    print(json.dumps({"qualification_scratch": str(parent), "instance": instance}), file=sys.stderr, flush=True)
    completed = False
    service_installed = False
    scoped: dict[str, str] = {}
    binary: Path | None = None
    plan_path: Path | None = None
    try:
        # ---- 1. Unpack and verify the archive inventory -------------------
        sidecar = args.archive.with_name(args.archive.name + ".sha256")
        assert sidecar.is_file(), f"missing sidecar: {sidecar}"
        expected_archive = sidecar.read_text().split()[0]
        assert _digest_bytes(args.archive.read_bytes()) == expected_archive, "archive digest mismatch"
        tree_dir = parent / "tree"
        tree_dir.mkdir()
        with tarfile.open(args.archive) as archive:
            names = sorted({member.name.split("/")[0] for member in archive.getmembers()})
            assert len(names) == 1, f"archive must have exactly one top-level directory: {names}"
            archive.extractall(tree_dir, filter="data")
        package = tree_dir / names[0]
        binary = package / "haepari"
        def run(env: dict, *cmd: str, timeout: int = 60) -> subprocess.CompletedProcess:
            return subprocess.run([str(binary), *cmd], env=env, cwd=package,
                                  text=True, capture_output=True, timeout=timeout, check=False)
        respond = lambda env, *cmd, expected=0, timeout=60: fixture._response(run(env, *cmd, timeout=timeout), expected)

        _verify_with_packaged_helper(package)
        assert not _inventory_mismatches(package), _inventory_mismatches(package)
        # Scratch-copy negatives: missing, added and tampered are each detected
        # by BOTH the packaged helper and the independent inline inventory.
        for mutation, victim in (("missing", "README.md"), ("added", "extra/SHA256SUMS"), ("tampered", "LICENSE")):
            copy = parent / f"negative-{mutation}"
            copy.mkdir()
            shutil.copytree(package, copy / names[0])
            target = copy / names[0]
            if mutation == "missing":
                (target / victim).unlink()
            elif mutation == "added":
                (target / victim).parent.mkdir(parents=True, exist_ok=True)
                (target / victim).write_text("foreign addition\n")
            else:
                (target / victim).write_bytes((target / victim).read_bytes() + b"x")
            helper = subprocess.run([sys.executable, str(target / "verify_tree.py"), str(target)],
                                    text=True, capture_output=True, timeout=60, check=False)
            assert helper.returncode == 1 and any(
                line.startswith(f"{mutation}: ") for line in helper.stderr.splitlines()), (mutation, helper.stderr)
            found = _inventory_mismatches(target)
            assert found and any(mutation in item for item in found), (mutation, found)

        # Symlink negatives: an unlisted dangling link is added; a recorded
        # regular file replaced by a symlink with identical owned non-secret
        # target bytes is tampered. Neither checker follows links.
        link_added = parent / "negative-link-added"
        link_added.mkdir()
        shutil.copytree(package, link_added / names[0])
        os.symlink("dangling-missing-target", link_added / names[0] / "link-dangling")
        helper = subprocess.run([sys.executable, str(link_added / names[0] / "verify_tree.py"),
                                 str(link_added / names[0])], text=True, capture_output=True, timeout=60, check=False)
        assert helper.returncode == 1 and any(
            line.startswith("added: link-dangling (symlink") for line in helper.stderr.splitlines()), helper.stderr
        found = _inventory_mismatches(link_added / names[0])
        assert "added: link-dangling (symlink)" in found, found

        link_replaced = parent / "negative-link-replacement"
        link_replaced.mkdir()
        shutil.copytree(package, link_replaced / names[0])
        identical = link_replaced / "license-target.bin"
        shutil.copyfile(link_replaced / names[0] / "LICENSE", identical)
        (link_replaced / names[0] / "LICENSE").unlink()
        os.symlink("../license-target.bin", link_replaced / names[0] / "LICENSE")
        helper = subprocess.run([sys.executable, str(link_replaced / names[0] / "verify_tree.py"),
                                 str(link_replaced / names[0])], text=True, capture_output=True, timeout=60, check=False)
        assert helper.returncode == 1 and any(
            line.startswith("tampered: LICENSE (symlink") for line in helper.stderr.splitlines()), helper.stderr
        found = _inventory_mismatches(link_replaced / names[0])
        assert "tampered: LICENSE (symlink replaces recorded regular file)" in found, found

        evidence["package_inventory"] = ("packaged helper + independent inventory (pristine pass; missing/added/tampered "
                                         "plus dangling-symlink-added and recorded-file symlink-replacement negatives)")

        # ---- 2. Standalone binary outside the checkout ---------------------
        standalone_home = parent / "standalone-home"
        standalone_home.mkdir(mode=0o700)
        minimal = {"PATH": "/usr/bin:/bin", "HOME": str(standalone_home), "LANG": "C"}
        version = respond(minimal, "version", "--json")["data"]
        assert version["version"] == args.version, version
        assert version["platform"] == "darwin/arm64" and version["executable_sha256"].startswith("sha256:")
        caps = respond(minimal, "capabilities", "--json")["data"]
        assert caps["compiled_backends"] == ["local"] and caps["qualified_backends"] == []
        assert caps["protocol_versions"] == ["managed-execution/v1"]
        assert set(caps["capabilities"]) == {"managed-execution/v1", "durable-observation/v1", "advisory-lock/v1",
                                             "managed-recovery/v1", "artifact-delivery/v1", "evidence-retention/v1"}
        machine_help = respond(minimal, "help", "--json")["data"]
        evidence["frozen_outputs"] = {"version": version, "capabilities": caps, "help": machine_help}
        usages = {entry["name"] + " " + entry["usage"] for entry in machine_help["commands"]}
        assert any(u.startswith("service plan --release ") for u in usages), "release service usage missing from help"
        evidence["standalone_binary"] = {"version": version["version"], "executable_sha256": version["executable_sha256"],
                                         "source_revision": version.get("source_revision", ""),
                                         "source_modified": version.get("source_modified", False)}
        evidence["standalone_working_directory"] = str(package)

        # ---- 3. Baseline bounded filesystem audit (BEFORE install) ---------
        scoped_home = parent / "scoped-home"
        sentinels = {
            ".aquarium-dev/keep.bin": b"legacy dev channel bytes\n",
            "git-hooks/pre-commit": b"#!/bin/sh\n# foreign hook\n",
            "sorage/state.json": b'{"schema": "foreign.control-plane/v1"}\n',
            ".config/skills/use-foreign/SKILL.md": b"# foreign host skill\n",
        }
        for relative, content in sentinels.items():
            target = scoped_home / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
        scoped_home.chmod(0o700)
        baseline_scoped = _snapshot_tree(scoped_home)
        launch_agents = Path.home() / "Library" / "LaunchAgents"
        baseline_real = {
            "dev-plist": _snapshot(launch_agents / DEV_PLIST),
            "plist-names": sorted(p.name for p in launch_agents.glob(REAL_PLIST_GLOB)),
            "tops": {name: _snapshot(Path.home() / name) for name in REAL_SENTINEL_TOPS},
        }
        evidence["baseline_real_sentinels"] = baseline_real
        # One scoped child HOME for service plan/install/uninstall and job clients.
        scoped = {"PATH": "/usr/bin:/bin", "HOME": str(scoped_home), "LANG": "C"}

        # ---- 4. Plan and install the unique release service ----------------
        service_root = parent / "release-state"
        service_env = dict(scoped, HAEPARI_HOME=str(service_root))
        plan_data = respond(scoped, "service", "plan", "--release", "--instance", instance,
                            "--binary", str(binary), "--root", str(service_root), "--json")["data"]
        assert plan_data["schema"] == "haepari.release-service-plan/v1"
        assert plan_data["label"] == f"io.rootkernel.haepari.{instance}"
        assert plan_data["global_limit"] == 1 and plan_data["host_macos_guest_limit"] == 1
        assert "sha256:" + _digest_bytes(binary.read_bytes()) == plan_data["binary_sha256"]
        assert plan_data["root"] == str(service_root) and not service_root.exists()
        plist_text = plan_data["plist"]
        for needle in ("<key>LimitLoadToSessionType</key><string>Aqua</string>",
                       "<key>AbandonProcessGroup</key><true/>", "<key>RunAtLoad</key><true/>",
                       "<key>KeepAlive</key><true/>", "<string>--global-limit</string>",
                       "<string>--host-macos-guest-limit</string>"):
            assert needle in plist_text, needle
        plan_path = parent / "release-plan.json"
        plan_path.write_text(json.dumps(plan_data))
        plan_path.chmod(0o600)
        installed = respond(scoped, "service", "install", "--plan-file", str(plan_path), "--json")
        assert installed["ok"] and installed["data"]["label"] == plan_data["label"]
        service_installed = True
        plist_path = Path(plan_data["plist_path"])
        assert plist_path.is_relative_to(scoped_home), "planned plist must live inside the scoped test HOME"
        owned = lambda p: p.lstat().st_uid == os.geteuid() and stat.S_IMODE(p.lstat().st_mode) & 0o077 == 0
        assert owned(plist_path) and plist_path.read_text() == plist_text
        marker = json.loads((service_root / ".development-service.json").read_text())
        assert marker["schema"] == "haepari.release-service-owner/v1" and marker["instance"] == instance
        target = f"gui/{os.geteuid()}/{plan_data['label']}"

        def service_pid() -> int:
            output = subprocess.run(["/bin/launchctl", "print", target], text=True,
                                    capture_output=True, timeout=15, check=False)
            if output.returncode != 0:
                return 0
            match = re.search(r"^\s*pid = (\d+)\s*$", output.stdout, re.MULTILINE)
            return int(match.group(1)) if match else 0

        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if service_pid() > 0 and run(service_env, "resources", "list", "--json").returncode == 0:
                break
            time.sleep(0.1)
        else:
            raise AssertionError("installed release service did not become ready")
        evidence["service"] = {"label": plan_data["label"], "root": str(service_root), "pid": service_pid(),
                               "plist": str(plist_path), "plist_inside_scoped_home": True,
                               "binary_sha256": plan_data["binary_sha256"],
                               "daemon_env": {"HAEPARI_HOME": str(service_root), "PATH": "/usr/bin:/bin"}}

        # Existing-plist install refuses without changing the service.
        controller_pid = service_pid()
        repeated = run(scoped, "service", "install", "--plan-file", str(plan_path), "--json")
        assert repeated.returncode == 6 and json.loads(repeated.stdout)["error"]["code"] == "STORAGE_UNAVAILABLE"
        assert service_pid() == controller_pid and plist_path.read_text() == plist_text

        # ---- 5. Documented native flows through the installed service ------
        respond(service_env, "resource", "register", "--file", str(package / "examples/resource-local.json"), "--json")
        # Submit connects before validating a spec; observe placeholder refusal
        # through the isolated reachable controller rather than a missing daemon.
        refused = run(service_env, "submit", "--spec", str(package / "examples/job-local-auto.json"),
                      "--request-key", "placeholder-check", "--json")
        assert refused.returncode == 2 and json.loads(refused.stdout)["error"]["code"] == "INVALID_REQUEST", \
            (refused.stdout, refused.stderr)
        bundle = parent / "bundle"
        (bundle / "source").mkdir(parents=True)
        (bundle / "source/input.txt").write_bytes(b"package check\n")

        def localize(example: str, extra_files: dict[str, bytes] | None = None) -> Path:
            selected = {"source/input.txt": b"package check\n"}
            if extra_files:
                selected.update(extra_files)
                for name, content in extra_files.items():
                    target_file = bundle / name
                    target_file.parent.mkdir(parents=True, exist_ok=True)
                    target_file.write_bytes(content)
            names = list(selected)
            document = json.loads((package / "examples" / example).read_text())
            document["input"]["root"] = str(bundle)
            document["input"]["inventory_digest"] = _tree_digest(bundle, names)
            path = parent / f"localized-{example}"
            path.write_text(json.dumps(document))
            return path

        # 5a. Auto PASS from the localized shipped example.
        auto_path = localize("job-local-auto.json")
        receipt = respond(service_env, "submit", "--spec", str(auto_path),
                          "--request-key", f"{args.scenario}-auto", "--json")["data"]
        job_id = receipt["job_id"]
        result = respond(service_env, "wait", job_id, "--timeout", "30s", "--json", timeout=35)["data"]
        assert result["final_verdict"] == "PASS" and result["cleanup_outcome"] == "verified"
        evidence["auto_pass_job"] = job_id
        evidence["scenarios"] = {"auto": result}

        # 5b. Same-key identical replay returns the original job; changed payload conflicts.
        replay = respond(service_env, "submit", "--spec", str(auto_path),
                         "--request-key", f"{args.scenario}-auto", "--json")["data"]
        assert replay["job_id"] == job_id
        changed = json.loads(auto_path.read_text())
        changed["suite"] = "install-check-changed"
        changed_path = parent / "localized-changed.json"
        changed_path.write_text(json.dumps(changed))
        conflict = run(service_env, "submit", "--spec", str(changed_path),
                       "--request-key", f"{args.scenario}-auto", "--json")
        assert conflict.returncode == 2 and json.loads(conflict.stdout)["error"]["code"] == "CONFLICT"
        history = respond(service_env, "jobs", "list", "--resource", "pkg-local", "--json")["data"]
        assert not history["more"] and sum(
            1 for item in history["jobs"] if item["receipt"]["job_id"] == job_id) == 1, \
            "one admission must appear exactly once for the replayed key"

        # 5c. Wait timeout (exit 5) then reconnect on the same job ID.
        slow = localize("job-local-auto.json")
        document = json.loads(slow.read_text())
        document["suite"] = "install-check-slow"
        document["workload"]["argv"] = ["/bin/sh", "-c",
                                        "mkdir -p ../results; /bin/sleep 2; cat input.txt > ../results/out.txt"]
        slow.write_text(json.dumps(document))
        slow_receipt = respond(service_env, "submit", "--spec", str(slow),
                               "--request-key", f"{args.scenario}-slow", "--json")["data"]
        timed = run(service_env, "wait", slow_receipt["job_id"], "--timeout", "300ms", "--json")
        assert timed.returncode == 5 and json.loads(timed.stdout)["error"]["code"] == "WAIT_TIMEOUT"
        reconnected = respond(service_env, "wait", slow_receipt["job_id"], "--timeout", "30s", "--json", timeout=35)["data"]
        assert reconnected["final_verdict"] == "PASS" and reconnected["job_id"] == slow_receipt["job_id"]
        evidence["scenarios"]["reconnected"] = reconnected

        # 5d. Manual completion from the shipped manual example: pass, then unobserved -> INCOMPLETE.
        scenarios = {"source/scenarios.md": b"# Install check\n\nObserve the collected output.\n"}
        manual_path = localize("job-local-manual.json", scenarios)
        manual_receipt = respond(service_env, "submit", "--spec", str(manual_path),
                                 "--request-key", f"{args.scenario}-manual", "--json")["data"]

        def await_manual(job: str) -> dict:
            end = time.monotonic() + 15
            while time.monotonic() < end:
                data = respond(service_env, "inspect", job, "--json")["data"]
                if data["receipt"]["job_state"] == "awaiting_manual":
                    return data["receipt"]
                time.sleep(0.05)
            raise AssertionError("manual job did not reach awaiting_manual")

        def manual_record(receipt: dict, status: str) -> Path:
            doctor = respond(service_env, "doctor", "--resource", "pkg-local", "--json")["data"]
            payload = {"schema": "haepari.manual-result/v1", "job_id": receipt["job_id"],
                       "installation_id": doctor["installation_id"],
                       "profile_id": receipt["admission"]["environment_profile_id"],
                       "profile_digest": receipt["admission"]["environment_profile_digest"],
                       "expected_revision": receipt["job_revision"], "observer": "release-package-harness",
                       "scenarios": [{"id": "install-check", "status": status,
                                      "evidence_references": [] if status == "unobserved" else ["fixture:package-observation"]}]}
            path = parent / f"manual-result-{status}.json"
            path.write_text(json.dumps(payload))
            return path

        awaiting = await_manual(manual_receipt["job_id"])
        record = manual_record(awaiting, "pass")
        respond(service_env, "manual", "complete", awaiting["job_id"], "--input", str(record),
                "--expected-revision", str(awaiting["job_revision"]), "--json")
        finished = respond(service_env, "wait", awaiting["job_id"], "--timeout", "30s", "--json", timeout=35)["data"]
        assert finished["final_verdict"] == "PASS"
        evidence["scenarios"]["synthetic_manual_pass"] = finished

        unobserved_path = localize("job-local-manual.json", scenarios)
        document = json.loads(unobserved_path.read_text())
        document["suite"] = "install-manual-unobserved"
        unobserved_path.write_text(json.dumps(document))
        unobserved_receipt = respond(service_env, "submit", "--spec", str(unobserved_path),
                                     "--request-key", f"{args.scenario}-manual-unobserved", "--json")["data"]
        awaiting_unobserved = await_manual(unobserved_receipt["job_id"])
        record = manual_record(awaiting_unobserved, "unobserved")
        respond(service_env, "manual", "complete", awaiting_unobserved["job_id"], "--input", str(record),
                "--expected-revision", str(awaiting_unobserved["job_revision"]), "--json")
        incomplete = run(service_env, "wait", awaiting_unobserved["job_id"], "--timeout", "30s", "--json", timeout=35)
        assert incomplete.returncode == 3 and json.loads(incomplete.stdout)["data"]["final_verdict"] == "INCOMPLETE"
        evidence["scenarios"]["synthetic_manual_unobserved"] = json.loads(incomplete.stdout)["data"]

        # 5e. Queued cancellation settles CANCELLED with terminal exit 4 and exact replay.
        paused = respond(service_env, "resource", "pause", "pkg-local", "--expected-revision", "1",
                         "--request-key", f"{args.scenario}-pause", "--json")["data"]
        cancelled_path = localize("job-local-auto.json")
        document = json.loads(cancelled_path.read_text())
        document["suite"] = "install-check-cancel"
        cancelled_path.write_text(json.dumps(document))
        queued = respond(service_env, "submit", "--spec", str(cancelled_path),
                         "--request-key", f"{args.scenario}-cancel", "--json")["data"]
        assert queued["job_state"] == "queued"
        revision = str(queued["job_revision"])
        cancelled = respond(service_env, "cancel", queued["job_id"], "--expected-revision", revision,
                            "--request-key", f"{args.scenario}-cancel-intent", "--json")["data"]
        replayed_cancel = respond(service_env, "cancel", queued["job_id"], "--expected-revision", revision,
                                  "--request-key", f"{args.scenario}-cancel-intent", "--json")["data"]
        assert replayed_cancel["mutation"] == cancelled["mutation"]
        assert cancelled["receipt"]["lease_generation"] == 0 and cancelled["receipt"]["global_occupancy"] == 0
        respond(service_env, "resource", "resume", "pkg-local",
                "--expected-revision", str(paused["availability_revision"]),
                "--request-key", f"{args.scenario}-resume", "--json")
        terminal = run(service_env, "wait", queued["job_id"], "--timeout", "30s", "--json", timeout=35)
        assert terminal.returncode == 4
        settled = json.loads(terminal.stdout)["data"]
        assert settled["final_verdict"] == "CANCELLED" and settled["cleanup_outcome"] == "not_needed"
        evidence["scenarios"]["queued_cancel"] = settled

        # 5f. Package lock example lifecycle plus read-only borrower validation.
        respond(service_env, "resource", "register", "--file", str(package / "examples/resource-lock.json"), "--json")
        grant = respond(service_env, "lock", "acquire", "pkg-review", "--request-key", f"{args.scenario}-lock",
                        "--holder", "package-check", "--wait", "--json", timeout=30)["data"]["current"]
        assert grant["state"] == "granted"
        respond(service_env, "lock", "renew", grant["grant_id"], "--json")
        inherited = dict(service_env, HAEPARI_LOCK_GRANT=json.dumps({
            "installation_id": grant["installation_id"], "lock_id": "pkg-review",
            "grant_id": grant["grant_id"], "generation": grant["generation"]}))
        borrower = run(inherited, "lock", "release", grant["grant_id"],
                       "--force", "--reason", "borrower must not release", "--json")
        assert borrower.returncode == 2 and json.loads(borrower.stdout)["error"]["code"] == "CONFLICT"

        # Queued owner cleanup while the first grant stays held: an observation
        # timeout is not cancellation; a same-key acquire replay (no --wait)
        # recovers the original request ID; owner cancel settles it terminally.
        queued_key = f"{args.scenario}-queued"
        queued = respond(service_env, "lock", "acquire", "pkg-review", "--request-key", queued_key,
                         "--holder", "queued-owner", "--json")["data"]["current"]
        assert queued["state"] == "queued" and queued["request_id"]
        timed_lock = run(service_env, "lock", "wait", queued["request_id"], "--timeout", "100ms", "--json")
        assert timed_lock.returncode == 5 and json.loads(timed_lock.stdout)["error"]["code"] == "WAIT_TIMEOUT"
        recovered = respond(service_env, "lock", "acquire", "pkg-review", "--request-key", queued_key,
                            "--holder", "queued-owner", "--json")["data"]["current"]
        assert recovered["request_id"] == queued["request_id"] and recovered["state"] == "queued"
        respond(service_env, "lock", "cancel", queued["request_id"], "--json")
        ended_request = run(service_env, "lock", "acquire", "pkg-review", "--request-key", queued_key,
                            "--holder", "queued-owner", "--json")
        assert ended_request.returncode == 4
        assert json.loads(ended_request.stdout)["data"]["current"]["state"] == "cancelled"
        listing = respond(service_env, "locks", "list", "--lock", "pkg-review", "--json")["data"]
        held = next(item for item in listing["requests"] if item["request_id"] == grant["request_id"])
        assert held["state"] == "granted" and held["grant_id"] == grant["grant_id"]
        evidence["queued_lock_cleanup"] = {"request_id": queued["request_id"], "state": "cancelled",
                                           "first_grant_held": True}

        # Documented read-only borrower validation through the public CLI.
        # Connection/list success is not validity proof: the borrower pages its
        # caller-known intended lock and compares reference and deadline fields
        # on the SAME matching page. CONFLICT/LOCK_EXPIRED/NOT_FOUND below are
        # consumer validation classifications, not automatic CLI errors; every
        # list invocation exits 0 and the parent grant stays untouched.
        def parse_time(value: str) -> datetime:
            parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
            if parsed.tzinfo is None or parsed.year == 1:
                raise ValueError("missing timezone or zero server time")
            return parsed

        def borrower_validate(reference: dict, intended: str) -> tuple[str, dict | None]:
            if reference["lock_id"] != intended:
                return "CONFLICT", None
            env = dict(service_env, HAEPARI_LOCK_GRANT=json.dumps(reference))
            cursor, seen, matched, page_data = "", set(), None, None
            while True:
                listing_args = ["locks", "list", "--lock", intended, "--limit", "1", "--json"]
                if cursor:
                    listing_args += ["--cursor", cursor]
                page = run(env, *listing_args)
                assert page.returncode == 0, (page.returncode, page.stderr)
                page_data = json.loads(page.stdout)["data"]
                if page_data["installation_id"] != reference["installation_id"]:
                    return "CONFLICT", None
                if not page_data.get("now"):
                    return "FAIL_CLOSED", None
                try:
                    page_now = parse_time(page_data["now"])
                except (ValueError, TypeError, AttributeError):
                    return "FAIL_CLOSED", None
                more, next_cursor = bool(page_data.get("more")), page_data.get("next_cursor")
                if more and (not next_cursor or next_cursor in seen or next_cursor == cursor):
                    return "FAIL_CLOSED", None
                matched = next((item for item in page_data["requests"]
                                if item.get("grant_id") == reference["grant_id"]), None)
                if matched is not None or not more:
                    break
                seen.add(cursor)
                cursor = next_cursor
            if matched is None:
                return ("CONFLICT" if reference["lock_id"] != intended else "NOT_FOUND"), None
            if (reference["installation_id"] != page_data["installation_id"]
                    or matched["installation_id"] != reference["installation_id"]
                    or matched["lock_id"] != reference["lock_id"]):
                return "CONFLICT", matched
            deadlines = (matched.get("liveness_deadline"), matched.get("max_hold_deadline"))
            if not all(deadlines):
                return "FAIL_CLOSED", matched
            try:
                expired = any(page_now >= parse_time(value) for value in deadlines)
            except (ValueError, TypeError, AttributeError):
                return "FAIL_CLOSED", matched
            if (matched["state"] != "granted" or matched.get("generation") != reference["generation"] or expired):
                return "LOCK_EXPIRED", matched
            return "VALID", matched

        def parent_grant_state() -> dict:
            page = respond(service_env, "locks", "list", "--lock", "pkg-review", "--json")["data"]
            return next(item for item in page["requests"] if item["request_id"] == grant["request_id"])

        reference = {"installation_id": grant["installation_id"], "lock_id": "pkg-review",
                     "grant_id": grant["grant_id"], "generation": grant["generation"]}
        parent_before = parent_grant_state()
        outcome, matched = borrower_validate(reference, "pkg-review")
        assert outcome == "VALID" and matched["state"] == "granted"
        respond(inherited, "resources", "list", "--json")  # harmless use after validation; no acquisition
        negatives = {
            "foreign-installation": borrower_validate(dict(reference, installation_id="not-this-installation"), "pkg-review"),
            "wrong-intended-lock": borrower_validate(reference, "pkg-other"),
            "wrong-reference-lock": borrower_validate(dict(reference, lock_id="pkg-other"), "pkg-review"),
            "stale-generation": borrower_validate(dict(reference, generation=grant["generation"] + 5), "pkg-review"),
            "unknown-grant": borrower_validate(dict(reference, grant_id="unknown-grant-id"), "pkg-review"),
        }
        expected = {"foreign-installation": "CONFLICT", "wrong-intended-lock": "CONFLICT", "wrong-reference-lock": "CONFLICT",
                    "stale-generation": "LOCK_EXPIRED", "unknown-grant": "NOT_FOUND"}
        for case, (result, _) in negatives.items():
            assert result == expected[case], (case, result)
        held_parent = parent_grant_state()
        assert held_parent == parent_before, "borrower validation/use changed the parent grant"
        evidence["borrower_validation"] = {
            "positive": "VALID", **{case: result for case, (result, _) in negatives.items()},
            "list_cli_exit": 0, "parent_grant_untouched": True, "validated_read_only_use": "resources list",
            "note": "consumer validation classifications compared per matching page server now; distinct from CLI exits",
        }

        released = respond(service_env, "lock", "release", grant["grant_id"], "--json")["data"]
        assert released["current"]["request_key"] == f"{args.scenario}-lock"
        assert released["current"]["state"] == "released" and released["receipt"]["grant_id"] == grant["grant_id"]
        parent_released = parent_grant_state()
        ended_outcome, ended_match = borrower_validate(reference, "pkg-review")
        assert ended_outcome == "LOCK_EXPIRED" and ended_match["state"] == "released"
        assert parent_grant_state() == parent_released
        evidence["lock"] = released
        evidence["borrower_validation"]["ended-reference"] = "LOCK_EXPIRED"

        # ---- 6. Installed-service TERM/KILL reattachment (M2b) ------------
        barrier = parent / "barrier"
        count_marker = parent / "execution-count"
        interruption = localize("job-local-auto.json")
        document = json.loads(interruption.read_text())
        document["suite"] = "install-check-interruption"
        document["workload"]["argv"] = ["/bin/sh", "-c",
                                        'printf once >> "$COUNT_PATH"; while [ ! -f "$RELEASE_PATH" ]; do /bin/sleep 0.05; done; '
                                        'mkdir -p ../results; cat input.txt > ../results/out.txt']
        document["workload"]["environment"] = {"COUNT_PATH": str(count_marker), "RELEASE_PATH": str(barrier)}
        document["deadlines"]["run_seconds"] = 120
        interruption.write_text(json.dumps(document))
        survive_key = f"{args.scenario}-survive"
        survive_id = respond(service_env, "submit", "--spec", str(interruption),
                             "--request-key", survive_key, "--json")["data"]["job_id"]
        request = service_root / "jobs" / survive_id / "request.json"
        end = time.monotonic() + 10
        while time.monotonic() < end:
            if count_marker.exists() and count_marker.read_bytes() == b"once":
                view = respond(service_env, "runner", "inspect", "--request", str(request), "--json")["data"]
                if view.get("start"):
                    break
            time.sleep(0.05)
        else:
            raise AssertionError("release-service runner did not reach its barrier")
        supervisor = view["intent"]["supervisor"]
        assert supervisor["pid"] == supervisor["pgid"] == supervisor["sid"]
        original_deadline = json.loads(request.read_text())["deadline"]

        occupied = run(service_env, "service", "uninstall", "--plan-file", str(plan_path), "--json")
        assert occupied.returncode == 2 and json.loads(occupied.stdout)["error"]["code"] == "RECOVERY_BLOCKED"
        assert service_pid() > 0 and plist_path.exists()

        mismatch = dict(plan_data)
        mismatch["plist"] += "<!-- mismatch -->\n"
        mismatch_path = parent / "mismatched-plan.json"
        mismatch_path.write_text(json.dumps(mismatch))
        mismatch_path.chmod(0o600)
        refused_uninstall = run(scoped, "service", "uninstall", "--plan-file", str(mismatch_path), "--json")
        assert refused_uninstall.returncode == 2 and json.loads(refused_uninstall.stdout)["error"]["code"] == "CONFLICT"
        assert plist_path.exists() and service_pid() > 0

        for signal_name in ("SIGTERM", "SIGKILL"):
            prior = service_pid()
            assert prior > 0 and supervisor["pid"] != prior
            subprocess.run(["/bin/launchctl", "kill", signal_name, target], check=True, timeout=15)
            end = time.monotonic() + 15
            while time.monotonic() < end:
                current = service_pid()
                if current > 0 and current != prior and run(service_env, "inspect", survive_id, "--json").returncode == 0:
                    break
                time.sleep(0.1)
            else:
                raise AssertionError(f"service did not restart after {signal_name}")
            view = respond(service_env, "runner", "inspect", "--request", str(request), "--json")["data"]
            assert view["intent"]["supervisor"] == supervisor, f"runner changed incarnation after {signal_name}"
            status = respond(service_env, "inspect", survive_id, "--json")["data"]["receipt"]
            assert status["job_state"] == "running" and status["resource_occupancy"] == 1
            assert respond(service_env, "submit", "--spec", str(interruption),
                           "--request-key", survive_key, "--json")["data"]["job_id"] == survive_id
            assert count_marker.read_bytes() == b"once"
            assert json.loads(request.read_text())["deadline"] == original_deadline
        barrier.touch()
        survivor = respond(service_env, "wait", survive_id, "--timeout", "60s", "--json", timeout=65)["data"]
        assert survivor["final_verdict"] == "PASS" and survivor["cleanup_outcome"] == "verified"
        resource = respond(service_env, "resource", "inspect", "pkg-local", "--json")["data"]
        assert resource["availability"] == "available" and resource["occupancy"] == 0
        evidence["scenarios"]["service_interruption"] = survivor
        evidence["interruption"] = {"job": survive_id, "signals": ["SIGTERM", "SIGKILL"],
                                    "supervisor_pid": supervisor["pid"], "one_effect": True,
                                    "original_deadline": original_deadline}

        # ---- 7. Exact paired skill identity (flows exercised above) ---------
        skill_file = package / "skill/use-haepari/SKILL.md"
        assert skill_file.is_file()
        evidence["skill_tree"] = {"files": 1, "sha256": _digest_bytes(skill_file.read_bytes())}

        # ---- 8. Uninstall and final audit (AFTER removal) ------------------
        respond(scoped, "service", "uninstall", "--plan-file", str(plan_path), "--json")
        service_installed = False
        assert not plist_path.exists(), "uninstall left the plist behind"
        assert service_root.is_dir() and (service_root / ".development-service.json").exists(), "uninstall removed state"
        end = time.monotonic() + 5
        while service_pid() and time.monotonic() < end:
            time.sleep(0.05)
        assert service_pid() == 0
        changes = _diff_trees(baseline_scoped, _snapshot_tree(scoped_home))
        allowlisted, unexpected = [], []
        for change in changes:
            label = change.split(": ", 1)[0]
            name = change.split(": ", 1)[1].split(" (")[0]
            if label == "extra" and name in ALLOWED_NEW_DIRS and "directory" in change:
                allowlisted.append(change)
            else:
                unexpected.append(change)
        final_plists = sorted(p.name for p in launch_agents.glob(REAL_PLIST_GLOB))
        protected_changes = []
        if final_plists != baseline_real["plist-names"]:
            protected_changes.append(f"launchagent name set changed: {baseline_real['plist-names']} -> {final_plists}")
        if _snapshot(launch_agents / DEV_PLIST) != baseline_real["dev-plist"]:
            protected_changes.append("existing dev plist changed")
        for name in REAL_SENTINEL_TOPS:
            if _snapshot(Path.home() / name) != baseline_real["tops"][name]:
                protected_changes.append(f"protected top-level changed: {name}")
        evidence["audit"] = {
            "scoped_home_allowlisted": allowlisted,
            "scoped_home_unexpected": unexpected,
            "real_sentinel_changes": protected_changes,
            "scope": "complete before/after comparison over the one test-owned scoped HOME (all service/job CLI "
                     "invocations used it); explicitly allowlisted Haepari-owned empty Library/LaunchAgents parents; "
                     "real-host observation limited to read-only non-secret sentinels (dev plist digest/mode, "
                     "io.rootkernel.haepari* name set, selected top-level metadata); no whole-host write tracing, "
                     "no credential reads, no causal attribution",
        }
        assert not unexpected, f"scoped owned HOME changed outside the declared Haepari allowlist: {unexpected}"
        assert not protected_changes, f"protected real-host sentinel changed; inconclusive/failing pending investigation: {protected_changes}"
        evidence["package"] = {"archive": str(args.archive), "archive_sha256": expected_archive,
                               "binary_sha256": _digest_bytes(binary.read_bytes())}
        print(json.dumps({"ok": True, "evidence": evidence}, indent=2))
        completed = True
    finally:
        if service_installed:
            emergency = {"PATH": "/usr/bin:/bin", "HOME": str(parent / "scoped-home"), "LANG": "C"}
            if binary is not None and plan_path is not None and plan_path.exists():
                try:
                    preserved = run(emergency, "service", "uninstall",
                                    "--plan-file", str(plan_path), "--json", timeout=60)
                    print(json.dumps({"emergency_uninstall": preserved.returncode}, indent=2), file=sys.stderr)
                except subprocess.TimeoutExpired:
                    print('{"emergency_uninstall": "timed out; state preserved"}', file=sys.stderr)
            else:
                print('{"emergency_uninstall": "plan or binary unavailable; service state preserved for inspection"}',
                      file=sys.stderr)
        if completed and not service_installed:
            shutil.rmtree(parent)
        else:
            # A failed bootstrap, occupied uninstall or uncertain workload must
            # retain the executable, plan, runner receipts and evidence together.
            print(json.dumps({"preserved_qualification_scratch": str(parent),
                              "reason": "qualification did not complete; inspect owned service and jobs before cleanup"}),
                  file=sys.stderr, flush=True)


if __name__ == "__main__":
    main()
