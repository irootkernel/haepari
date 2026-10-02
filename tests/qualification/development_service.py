"""Explicit installed-service qualification; never run by the common gate.

Install the saved, reviewed plan separately. This check restarts only that
exact owned development service and retains its state for operator inspection.
"""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import time


def _owned(path: Path, *, directory: bool = False) -> None:
    info = path.lstat()
    assert info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) & 0o077 == 0
    assert stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)


def _launchctl(*args: str) -> str:
    result = subprocess.run(["/bin/launchctl", *args], text=True, capture_output=True, timeout=15, check=False)
    assert result.returncode == 0, (args[0], result.returncode, result.stderr[:4096])
    return result.stdout


def _pid(target: str) -> int:
    output = _launchctl("print", target)
    match = re.search(r"^\s*pid = (\d+)\s*$", output, re.MULTILINE)
    return int(match.group(1)) if match else 0


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan-file", type=Path, required=True)
    parser.add_argument("--scenario",default="service-qualification",help="new owned observation name; previous evidence is preserved")
    args = parser.parse_args()
    assert re.fullmatch(r"[a-z][a-z0-9-]{0,31}",args.scenario)
    plan = json.loads(args.plan_file.read_text())
    assert plan["schema"] == "haepari.development-service-plan/v1"
    assert re.fullmatch(r"[a-z0-9][a-z0-9-]{0,47}", plan["instance"])
    assert plan["owner_uid"] == os.geteuid()
    assert plan["domain"] == f"gui/{os.geteuid()}"
    assert plan["label"] == "io.rootkernel.haepari.dev." + plan["instance"]
    target = plan["domain"] + "/" + plan["label"]
    binary, root, plist = Path(plan["binary"]), Path(plan["root"]), Path(plan["plist_path"])
    assert plist == Path.home() / "Library" / "LaunchAgents" / (plan["label"] + ".plist")
    _owned(binary)
    _owned(root, directory=True)
    _owned(plist)
    assert plist.read_text() == plan["plist"]
    assert "<key>LimitLoadToSessionType</key><string>Aqua</string>" in plan["plist"]
    assert "<key>AbandonProcessGroup</key><true/>" in plan["plist"]
    assert "sha256:" + hashlib.sha256(binary.read_bytes()).hexdigest() == plan["binary_sha256"]
    owner = json.loads((root / ".development-service.json").read_text())
    assert owner == {"schema": "haepari.development-service-owner/v1", "instance": plan["instance"], "owner_uid": plan["owner_uid"], "root": plan["root"]}
    assert _pid(target) > 0

    spec = importlib.util.spec_from_file_location("haepari_local_fixture", Path(__file__).resolve().parents[1] / "e2e" / "test_local_cli.py")
    fixture = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(fixture)
    fixture.BINARY = binary
    fixture._preflight()
    parent = root / args.scenario
    parent.mkdir(mode=0o700)  # Exclusive scenario; existing evidence is preserved.
    env = {"PATH": "/usr/bin:/bin", "HOME": str(Path.home()), "HAEPARI_HOME": str(root), "LANG": "C"}
    resource_id = "service-"+args.scenario
    job_path, marker = fixture._restart_job_fixture(parent, env,resource_id)

    job = json.loads(job_path.read_text())
    job["deadlines"]["run_seconds"] = 60
    job_path.write_text(json.dumps(job))
    submit = lambda: fixture._response(fixture._run(binary, env, "submit", "--spec", str(job_path), "--request-key", "service-survival-"+args.scenario, "--json"))["data"]
    job_id = submit()["job_id"]
    request = root / "jobs" / job_id / "request.json"
    observations = []
    try:
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if marker.exists() and marker.read_bytes() == b"once":
                view = fixture._response(fixture._run(binary, env, "runner", "inspect", "--request", str(request), "--json"))["data"]
                if view.get("start"):
                    break
            time.sleep(0.025)
        else:
            raise AssertionError("product runner did not reach its bounded release barrier")
        identity = view["intent"]["supervisor"]
        assert identity["pid"] == identity["pgid"] == identity["sid"]
        original_deadline = json.loads(request.read_text())["deadline"]
        controller_pid = _pid(target)
        refused = fixture._run(binary, env, "service", "uninstall", "--plan-file", str(args.plan_file), "--json")
        assert refused.returncode == 2 and json.loads(refused.stdout)["error"]["code"] == "RECOVERY_BLOCKED"
        assert _pid(target) == controller_pid and plist.read_text() == plan["plist"]
        repeated = fixture._run(binary, env, "service", "install", "--plan-file", str(args.plan_file), "--json")
        assert repeated.returncode == 6 and json.loads(repeated.stdout)["error"]["code"] == "STORAGE_UNAVAILABLE"
        assert _pid(target) == controller_pid and plist.read_text() == plan["plist"]
        mismatched_plan = dict(plan, plist=plan["plist"] + "<!-- mismatch -->\n")
        mismatch_path = parent / "mismatched-plan.json"
        with mismatch_path.open("x") as mismatch_file:
            json.dump(mismatched_plan, mismatch_file)
        mismatch_path.chmod(0o600)
        refused = fixture._run(binary, env, "service", "uninstall", "--plan-file", str(mismatch_path), "--json")
        assert refused.returncode == 2 and json.loads(refused.stdout)["error"]["code"] == "CONFLICT"
        assert _pid(target) == controller_pid and plist.read_text() == plan["plist"]
        for signal in ("SIGTERM", "SIGKILL"):
            prior = _pid(target)
            assert prior > 0 and identity["pid"] != prior
            _launchctl("kill", signal, target)
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline:
                current = _pid(target)
                if current > 0 and current != prior:
                    probe = fixture._run(binary, env, "inspect", job_id, "--json")
                    if probe.returncode == 0:
                        receipt = json.loads(probe.stdout)["data"]["receipt"]
                        if receipt["job_state"] == "running" and receipt["resource_occupancy"] == 1:
                            break
                time.sleep(0.05)
            else:
                raise AssertionError("development service did not reattach its exact runner")
            observed = fixture._response(fixture._run(binary, env, "runner", "inspect", "--request", str(request), "--json"))["data"]
            assert observed["intent"]["supervisor"] == identity and "exit" not in observed
            assert json.loads(request.read_text())["deadline"] == original_deadline
            assert submit()["job_id"] == job_id and marker.read_bytes() == b"once"

            cursor, recoveries = 0, []
            while True:
                page = fixture._response(fixture._run(binary, env, "events", job_id, "--after", str(cursor), "--limit", "100", "--json"))["data"]
                recoveries.extend(event["payload"] for event in page["events"] if event["kind"] == "recovery_intent")
                cursor = page["next_cursor"]
                if not page["more"]:
                    break
            assert len(recoveries) == len(observations) + 1
            recovery = recoveries[-1]
            assert recovery["action"] == "reattach"
            assert recovery["evidence"]["source"] == "machine-observed"
            assert recovery["evidence"]["mechanism"] == "native-supervisor-incarnation"
            observations.append({"signal": signal, "prior_controller_pid": prior, "controller_pid": current, "supervisor": identity, "reattached_receipt": receipt})
    finally:
        (parent / "release").touch()
    result = fixture._response(fixture._run(binary, env, "wait", job_id, "--timeout", "10s", "--json"))["data"]
    assert result["final_verdict"] == "PASS" and result["cleanup_outcome"] == "verified"
    assert marker.read_bytes() == b"once" and submit()["job_id"] == job_id
    resource = fixture._response(fixture._run(binary, env, "resource", "inspect", resource_id, "--json"))["data"]
    assert resource["availability"] == "available" and resource["occupancy"] == 0
    print(json.dumps({"schema": "haepari.development-service-observation/v1", "candidate_sha256": plan["binary_sha256"], "domain": plan["domain"], "label": plan["label"], "job_id": job_id, "restarts": observations, "result": result, "resource": resource}))


if __name__ == "__main__":
    main()
