"""Public extracted-package relocation observations; no fixture SQL or fake service."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import time


def _inventory(root: Path) -> dict:
    result = {}
    for path in sorted(root.rglob("*")):
        info = path.lstat()
        if stat.S_ISREG(info.st_mode):
            result[str(path.relative_to(root))] = {
                "device": info.st_dev, "inode": info.st_ino,
                "birth": info.st_birthtime, "size": info.st_size,
                "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}
    return result


def qualify(parent, binary, scoped, source, source_plan, source_plan_path,
            run, respond, auto_spec, auto_key, evidence):
    """Reverse while stopped, inspect restored history, then make a fresh move."""
    target = parent / "relocated"
    target_plan = respond(scoped, "service", "plan", "--release",
                          "--instance", source_plan["instance"], "--binary", str(binary),
                          "--root", str(target), "--json")["data"]
    target_plan_path = parent / "target-plan.json"
    target_plan_path.write_text(json.dumps(target_plan))
    target_plan_path.chmod(0o600)
    original = _inventory(source / "jobs")
    objects = _inventory(source / "objects")
    database = (source / "state.sqlite").stat()
    # Collect stable public observations before the stop in the caller.
    baseline = evidence["relocation_baseline"]
    plan_envelope = respond(scoped, "service", "relocate", "plan",
                            "--source-plan", str(source_plan_path),
                            "--target-plan", str(target_plan_path),
                            "--backup-dir", str(parent / "relocation-backup"), "--json")
    plan_path = parent / "relocation-plan.json"
    plan_path.write_text(json.dumps(plan_envelope["data"]))
    plan_path.chmod(0o600)
    planned = respond(scoped, "service", "relocate", "status",
                      "--plan-file", str(plan_path), "--json")["data"]
    assert planned["phase"] == "planned"
    moved = respond(scoped, "service", "relocate", "apply",
                    "--plan-file", str(plan_path), "--json")["data"]
    assert moved["phase"] == "complete" and not source.exists()
    assert _inventory(target / "jobs") == original
    assert _inventory(target / "objects") == objects
    assert os.path.samestat(database, (target / "state.sqlite").stat())
    repeated = respond(scoped, "service", "relocate", "apply",
                       "--plan-file", str(plan_path), "--json")["data"]
    assert repeated == moved
    reversed_receipt = respond(scoped, "service", "relocate", "rollback",
                               "--plan-file", str(plan_path), "--json")["data"]
    assert reversed_receipt["phase"] == "rolled_back" and not target.exists()
    assert _inventory(source / "jobs") == original
    assert _inventory(source / "objects") == objects

    def observe(root, suffix):
        env = dict(scoped, HAEPARI_HOME=str(root))
        log_path = parent / ("observer-" + suffix + ".log")
        daemon = None
        with log_path.open("ab") as log:
            daemon = subprocess.Popen([str(binary), "daemon", "run", "--backend", "local"],
                                      env=env, cwd=parent, stdout=log, stderr=subprocess.STDOUT)
        try:
            end = time.monotonic() + 20
            while time.monotonic() < end:
                assert daemon.poll() is None, log_path.read_text()
                if run(env, "resources", "list", "--json").returncode == 0:
                    break
                time.sleep(0.1)
            else:
                raise AssertionError("relocation observer readiness")
            for job, before in baseline.items():
                for operation in ("inspect", "logs", "events"):
                    after = respond(env, operation, job, "--json")["data"]
                    assert after == before[operation], (suffix, job, operation, before[operation], after)
                destination = parent / ("export-" + suffix + "-" + job)
                respond(env, "export", job, "--to", str(destination), "--json")
                exported = {name: row["sha256"] for name, row in _inventory(destination).items()
                            if name != "manifest.json"}
                assert exported == before["export"]
            replay = respond(env, "submit", "--spec", str(auto_spec),
                             "--request-key", auto_key, "--json")["data"]
            assert replay["job_id"] == evidence["auto_pass_job"]
            listed = respond(env, "jobs", "list", "--limit", "100", "--json")["data"]
            assert not listed["more"] and len(listed["jobs"]) == len(baseline)
            assert _inventory(root / "jobs") == original
            assert _inventory(root / "objects") == objects
        finally:
            if daemon.poll() is None:
                daemon.terminate()
            assert daemon.wait(timeout=15) == 0
        # Existing lock only; never create a replacement during verification.
        lock_path = root / "controller.lock"
        with lock_path.open("r+b") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        assert os.path.samestat(database, (root / "state.sqlite").stat())

    observe(source, "restored")
    next_plan = respond(scoped, "service", "relocate", "plan",
                        "--source-plan", str(source_plan_path),
                        "--target-plan", str(target_plan_path),
                        "--backup-dir", str(parent / "successor-backup"), "--json")["data"]
    assert next_plan["predecessor"]["relocation_id"] == moved["relocation_id"]
    successor_path = parent / "successor-plan.json"
    successor_path.write_text(json.dumps(next_plan))
    successor_path.chmod(0o600)
    successor = respond(scoped, "service", "relocate", "apply",
                        "--plan-file", str(successor_path), "--json")["data"]
    assert successor["installation_id"] == moved["installation_id"]
    observe(target, "forward")
    archived = respond(scoped, "service", "relocate", "status",
                       "--plan-file", str(plan_path), "--json")["data"]
    assert archived == reversed_receipt
    evidence["relocation"] = {"planned": planned, "moved": moved,
                              "reversed": reversed_receipt, "successor": successor,
                              "job_files": original, "object_files": objects,
                              "coverage": "native public local PASS/FAIL/missing-output INCOMPLETE and synthetic manual histories; legal reverse before activation, restored and successor-root reads/export/same-key replay; fake-native and crash matrix are separate common-source evidence"}
