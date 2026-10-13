#!/usr/bin/env python3
"""A project-owned caller; Haepari owns resource control and Job execution."""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import uuid


PROJECT = Path(__file__).resolve().parent
from haepari_inputs import Refusal, capture_inputs, job_spec, regular_bytes, validate_manual_selection, validate_recipe


def save(path, value):
    fd, temporary = tempfile.mkstemp(prefix=".save-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(value, stream, sort_keys=True)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def invoke(binary, *args, timeout=25):
    try:
        result = subprocess.run([binary, *args], capture_output=True, text=True, timeout=timeout, check=False)
    except subprocess.TimeoutExpired:
        raise Refusal("Haepari caller deadline expired; inspect the controller and reconnect with this original run directory, spec and key") from None
    if result.returncode < 0:
        raise Refusal("Haepari CLI was interrupted; inspect the controller and reconnect with this original run directory, spec and key")
    try:
        envelope = json.loads(result.stdout)
    except json.JSONDecodeError:
        raise Refusal(f"Haepari response unavailable; select a compatible CLI and inspect its controller, then reconnect with this original run directory, spec and key: {result.stderr[:512]}") from None
    if not isinstance(envelope, dict) or envelope.get("schema") != "haepari.envelope/v1" or not isinstance(envelope.get("ok"), bool):
        raise Refusal("unsupported Haepari response; select a compatible CLI/controller and retain this original run directory, spec and key")
    payload = envelope.get("data" if envelope["ok"] else "error")
    if not isinstance(payload, dict):
        raise Refusal("malformed Haepari response payload; retain this original run directory, spec and key and inspect the compatible CLI/controller")
    return result.returncode, envelope


def prepare(run, args, installation):
    try:
        config = json.loads(regular_bytes(PROJECT / "project.json")[0])
    except json.JSONDecodeError as exc:
        raise Refusal(f"project.json is invalid JSON: {exc}") from None
    if not isinstance(config, dict) or not isinstance(config.get("suites"), dict):
        raise Refusal("project suites must be a mapping of declared suite names to recipes")
    suites = config["suites"]
    names = list(suites) if args.suite == "all" else [args.suite]
    if not names or any(name not in suites or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", name) for name in names):
        raise Refusal("select a consumer-declared suite; this project defaults to regression")
    policy = config.get("inputs")
    selected = policy.get("selected_files") if isinstance(policy, dict) else None
    for name in names:
        validate_recipe(suites[name], args.mode)
        validate_manual_selection(suites[name], args.mode, selected)
    inputs = capture_inputs(PROJECT, run / "bundle", config.get("inputs"))
    inventory = inputs["input"]["inventory_digest"]
    run_id = uuid.uuid4().hex
    record = {"schema": "haepari.project-run/v1", "installation_id": installation, "resource": args.resource,
              "suite": args.suite, "mode": args.mode, "inventory_digest": inventory, "declared_suites": names, "jobs": []}
    for index, name in enumerate(names):
        recipe = suites[name]
        spec = job_spec(inputs, args.resource, name, args.mode, recipe)
        path = run / f"spec-{index}.json"
        save(path, spec)
        record["jobs"].append({"suite": name, "request_key": f"project-{run_id}-{index}", "spec": path.name,
                               "spec_sha256": hashlib.sha256(regular_bytes(path)[0]).hexdigest(), "submission_started": False})
    save(run / "run.json", record)
    return record


def execute(run, args, record, doctor):
    if not isinstance(record, dict) or record.get("schema") != "haepari.project-run/v1":
        raise Refusal("saved run.json has an unsupported shape or schema; retain the original run directory and inspect its keys and specs")
    if any(record.get(key) != getattr(args, key) for key in ("resource", "suite", "mode")):
        raise Refusal("resume requires the original resource, suite and mode; select a new run directory for a new run")
    names = record.get("declared_suites")
    entries = record.get("jobs")
    valid_id = lambda value: isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", value)
    if (not isinstance(names, list) or not names or not all(valid_id(name) for name in names)
            or len(set(names)) != len(names) or (args.suite != "all" and names != [args.suite])
            or not isinstance(entries, list) or len(entries) != len(names)):
        raise Refusal("saved suite/Job membership is incomplete; retain and inspect the original run and keys")
    keys, job_ids = set(), set()
    for index, (name, entry) in enumerate(zip(names, entries)):
        if (not isinstance(entry, dict) or entry.get("suite") != name or entry.get("spec") != f"spec-{index}.json"
                or not valid_id(entry.get("request_key")) or entry["request_key"] in keys
                or not isinstance(entry.get("submission_started"), bool)):
            raise Refusal("saved Job binding is invalid; retain and inspect the original run and keys")
        keys.add(entry["request_key"])
        if "job_id" in entry:
            if not valid_id(entry["job_id"]) or entry["job_id"] in job_ids or not entry["submission_started"]:
                raise Refusal("saved Job identity is invalid; no aggregate PASS is available")
            job_ids.add(entry["job_id"])
    if doctor["installation_id"] != record["installation_id"]:
        raise Refusal("the selected Haepari installation changed; do not replay this key against another installation")
    outcome = "PASS"
    code = 0
    for entry in entries:
        if code:
            print(json.dumps({"kind": "skipped", "suite": entry["suite"], "reason": f"earlier suite status is {outcome}"}), flush=True)
            continue
        path = run / entry["spec"]
        raw = regular_bytes(path)[0]
        if path.parent != run or hashlib.sha256(raw).hexdigest() != entry["spec_sha256"]:
            raise Refusal("the saved spec changed; preserve this run instead of constructing a new request")
        spec = json.loads(raw)
        if not isinstance(spec, dict) or any(spec.get(key) != value for key, value in (
                ("resource_id", args.resource), ("suite", entry["suite"]), ("mode", args.mode))):
            raise Refusal("saved spec and Job binding disagree; preserve the original run")
        if not entry["submission_started"]:
            if not doctor["ready"]:
                raise Refusal("PREREQUISITE_UNAVAILABLE: " + "; ".join(g["message"] for g in doctor["gaps"]))
            entry["submission_started"] = True
            save(run / "run.json", record)
        submitted, response = invoke(args.haepari, "submit", "--spec", str(path), "--request-key", entry["request_key"], "--json")
        if submitted or not response["ok"]:
            issue = response.get("error", {})
            guidance = ("correct the declaration and select a new empty run directory; retain this original run for inspection"
                        if issue.get("code") == "INVALID_REQUEST" else
                        "retain this run directory without changing its key and inspect the error retry policy")
            raise Refusal("submission refused; " + guidance + ": " + json.dumps(issue))
        receipt = response["data"]
        if not valid_id(receipt.get("job_id")):
            raise Refusal("malformed Job receipt; retain this original run directory, spec and key")
        if entry.get("job_id") and entry["job_id"] != receipt["job_id"]:
            raise Refusal("replay returned a different Job; preserve the original run")
        entry["job_id"] = receipt["job_id"]
        save(run / "run.json", record)
        print(json.dumps({"kind": "admission", "suite": entry["suite"], "job_id": entry["job_id"]}), flush=True)
        waited, response = invoke(args.haepari, "wait", entry["job_id"], "--timeout", "15s", "--json", timeout=20)
        entry["observation"] = response
        save(run / "run.json", record)
        print(json.dumps({"kind": "result", "suite": entry["suite"], "job_id": entry["job_id"],
                          "cli_exit": waited, "response": response}), flush=True)
        if not response["ok"]:
            outcome, code = ("PENDING", 5) if waited == 5 else ("UNKNOWN", waited or 2)
        else:
            verdict = response["data"].get("final_verdict")
            expected = {"PASS": 0, "FAIL": 1, "INCOMPLETE": 3, "CANCELLED": 4}.get(verdict)
            if expected is None or waited != expected:
                raise Refusal("terminal verdict and CLI exit disagree; no aggregate PASS is available")
            outcome, code = verdict, waited
    print(json.dumps({"kind": "aggregate", "status": outcome, "run_dir": str(run),
                      "job_ids": [j["job_id"] for j in record["jobs"] if "job_id" in j]}), flush=True)
    return code


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--haepari", default="haepari")
    parser.add_argument("--resource", required=True)
    parser.add_argument("--suite", default="regression")
    parser.add_argument("--mode", choices=("auto", "manual"), default="auto")
    parser.add_argument("--run-dir", default="")
    args = parser.parse_args()
    if not args.resource or os.environ.get("HAEPARI_PARENT_CONTEXT"):
        raise Refusal("the host wrapper needs an explicit resource and cannot enqueue from a managed guest")
    binary = shutil.which(args.haepari)
    if binary is None:
        raise Refusal("PREREQUISITE_UNAVAILABLE: select an installed compatible Haepari CLI explicitly")
    args.haepari = str(Path(binary).resolve())
    if args.run_dir:
        run = Path(args.run_dir).absolute()
        run.mkdir(mode=0o700, exist_ok=True)
    else:
        parent = PROJECT / ".haepari-runs"
        parent.mkdir(mode=0o700, exist_ok=True)
        run = Path(tempfile.mkdtemp(prefix="run-", dir=parent))
    info = run.lstat()
    if not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o700 or info.st_uid != os.geteuid() or run.is_symlink():
        raise Refusal("the run directory must be a private owned directory (0700, owned by the current user)")
    run = run.resolve()
    print(json.dumps({"kind": "run", "run_dir": str(run)}), flush=True)
    fd = os.open(run / "caller.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "a+") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Refusal("another caller owns this run; reconnect after it exits") from None
        _, response = invoke(args.haepari, "doctor", "--resource", args.resource, "--json")
        if not response["ok"]:
            issue = response.get("error", {})
            raise Refusal(str(issue.get("code", "PREREQUISITE_UNAVAILABLE")) + ": " +
                          str(issue.get("message", "Inspect the intended controller and HAEPARI_HOME.")))
        if response.get("data", {}).get("schema") != "haepari.doctor/v1":
            raise Refusal("PREREQUISITE_UNAVAILABLE: the selected controller lacks the project preflight contract")
        doctor = response["data"]
        if (not isinstance(doctor.get("ready"), bool)
                or not isinstance(doctor.get("installation_id"), str) or not doctor["installation_id"]
                or not isinstance(doctor.get("gaps"), list)
                or any(not isinstance(gap, dict) or not isinstance(gap.get("message"), str)
                       for gap in doctor["gaps"])):
            raise Refusal("malformed Haepari doctor report; select a compatible CLI/controller and retain this original run directory")
        saved = run / "run.json"
        if saved.exists():
            try:
                record = json.loads(regular_bytes(saved)[0])
            except json.JSONDecodeError:
                raise Refusal("saved run.json is corrupt; retain the original run directory and inspect its keys and specs") from None
        else:
            if any(p.name != "caller.lock" for p in run.iterdir()):
                raise Refusal("unfinished run preparation is retained; inspect it and select a new empty run directory")
            if not doctor["ready"]:
                raise Refusal("PREREQUISITE_UNAVAILABLE: " + "; ".join(g["message"] for g in doctor["gaps"]))
            record = prepare(run, args, doctor["installation_id"])
        return execute(run, args, record, doctor)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (Refusal, OSError, ValueError, KeyError, TypeError, AttributeError, subprocess.TimeoutExpired) as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(2)
