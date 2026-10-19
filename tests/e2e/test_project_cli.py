"""The reference Make caller against an isolated public local controller."""

import json
import fcntl
import os
from pathlib import Path
import shutil
import socket
import threading
import runpy
import subprocess
import tempfile
import time

import pytest

from test_local_cli import BINARY, _controller, _response, _run


SOURCE = Path(__file__).resolve().parents[2] / "examples/project-test"


@pytest.fixture
def project_root():
    volume = Path("/Volumes/RootKernel")
    if os.path.ismount(volume) and os.access(volume, os.W_OK):
        parent = volume / "tmp"
        parent.mkdir(exist_ok=True)
    else:
        parent = Path(tempfile.gettempdir()).resolve()
    root = Path(tempfile.mkdtemp(prefix="hp-project-", dir=parent)).resolve()
    try:
        shutil.copytree(SOURCE, root / "project", ignore=shutil.ignore_patterns("__pycache__", ".haepari-runs", "results"))
        yield root
    finally:
        shutil.rmtree(root)


def register(parent, env):
    spec = {"schema": "haepari.resource/v1", "id": "project-local", "kind": "local",
            "execution_mode": "managed", "capacity": 1, "revision": 1,
            "adapter": {"qualification_reference": "isolated-project"},
            "cleanup": {"policy_reference": "owned-workspace"}}
    path = parent / "project-resource.json"
    path.write_text(json.dumps(spec))
    _response(_run(BINARY, env, "resource", "register", "--file", str(path), "--json"))


def host(root, env, run, suite="regression", mode="auto", binary=BINARY):
    return subprocess.run(["/usr/bin/python3", str(root / "project/host.py"), "--haepari", str(binary),
                           "--resource", "project-local", "--suite", suite, "--mode", mode, "--run-dir", str(run)],
                          env=env, text=True, capture_output=True, timeout=40)


def jobs(run, owned):
    record = json.loads((run / "run.json").read_text())
    for item in record["jobs"]:
        if item.get("job_id") and item["job_id"] not in owned:
            owned.append(item["job_id"])
    return record


def finish_synthetic_manual(parent, env, owned, run):
    # Declared fixture observations; this is not a real human/VM observation.
    from test_manual_cli import awaiting
    saved = jobs(run, owned)
    for index, entry in enumerate(saved["jobs"]):
        ready = awaiting(env, entry["job_id"])
        spec = json.loads((run / f"spec-{index}.json").read_text())
        installation = _response(_run(BINARY, env, "doctor", "--resource", "project-local", "--json"))["data"]["installation_id"]
        result = {"schema": "haepari.manual-result/v1", "job_id": ready["job_id"],
                  "installation_id": installation, "profile_id": spec["environment"]["profile_id"],
                  "profile_digest": spec["environment"]["profile_digest"], "expected_revision": ready["job_revision"],
                  "observer": "synthetic-project-fixture", "scenarios": [
                      {"id": name, "status": "pass", "evidence_references": ["fixture:project-check"]}
                      for name in spec["manual"]["required_scenarios"]]}
        path = parent / "project-manual-result.json"
        path.write_text(json.dumps(result))
        _response(_run(BINARY, env, "manual", "complete", ready["job_id"], "--input", str(path),
                       "--expected-revision", str(ready["job_revision"]), "--json"))
        _response(_run(BINARY, env, "wait", ready["job_id"], "--timeout", "10s", "--json"))


def test_reference_project_success_failure_replay_and_aggregate(project_root):
    project = project_root / "project"
    counter = project_root / "executions"
    config = json.loads((project / "project.json").read_text())
    recipe = config["suites"]["regression"]
    recipe["environment"] = {"COUNT_PATH": str(counter)}
    config["suites"]["failure"] = dict(recipe, environment={"COUNT_PATH": str(counter), "EXPECTED_INPUT": "wrong"})
    config["suites"]["later"] = recipe.copy()
    (project / "project.json").write_text(json.dumps(config))
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        first = project_root / "success"
        result = host(project_root, env, first)
        assert result.returncode == 0, (result.stdout, result.stderr)
        record = jobs(first, owned)
        assert json.loads(result.stdout.splitlines()[-1])["status"] == "PASS"
        result = host(project_root, env, first)
        assert result.returncode == 0, (result.stdout, result.stderr)
        assert jobs(first, owned)["jobs"][0]["job_id"] == record["jobs"][0]["job_id"]
        assert counter.read_bytes() == b"x"
        aggregate = project_root / "all"
        result = host(project_root, env, aggregate, "all")
        assert result.returncode == 1, (result.stdout, result.stderr)
        record = jobs(aggregate, owned)
        assert json.loads(result.stdout.splitlines()[-1])["status"] == "FAIL"
        assert [bool(j.get("job_id")) for j in record["jobs"]] == [True, True, False]
        failure = record["jobs"][1]["observation"]["data"]
        assert failure["native_exit_code"] == 7 and failure["cleanup_outcome"] == "verified"
        assert failure["evidence_completeness"] == "complete"
        assert counter.read_bytes() == b"xxx"
        result = host(project_root, env, aggregate, "all")
        assert result.returncode == 1
        assert counter.read_bytes() == b"xxx"

        manual = project_root / "combined-manual"
        result = host(project_root, env, manual, mode="manual")
        assert result.returncode == 5 and json.loads(result.stdout.splitlines()[-1])["status"] == "PENDING"
        saved = jobs(manual, owned)
        job_id = saved["jobs"][0]["job_id"]
        ready = _response(_run(BINARY, env, "inspect", job_id, "--json"))["data"]["receipt"]
        assert ready["job_state"] == "awaiting_manual" and ready["resource_occupancy"] == 1
        assert counter.read_bytes() == b"xxxx"
        finish_synthetic_manual(parent, env, owned, manual)
        assert host(project_root, env, manual, mode="manual").returncode == 0
        assert jobs(manual, owned)["jobs"][0]["request_key"] == saved["jobs"][0]["request_key"]
        assert jobs(manual, owned)["jobs"][0]["job_id"] == job_id
        assert counter.read_bytes() == b"xxxx"
        manual_result = jobs(manual, owned)["jobs"][0]["observation"]["data"]
        assert manual_result["final_verdict"] == "PASS" and manual_result["cleanup_outcome"] == "verified"
        assert any(artifact["path"] == "results/out.txt" for artifact in manual_result["artifacts"])
        destination = project_root / "combined-manual-export"
        _response(_run(BINARY, env, "export", job_id, "--to", str(destination), "--json"))
        assert (destination / "artifacts/results/out.txt").read_bytes() == b"ready\n"
        successor = project_root / "combined-successor"
        result = host(project_root, env, successor)
        assert result.returncode == 0
        observed = jobs(successor, owned)["jobs"][0]["observation"]["data"]
        assert observed["final_verdict"] == "PASS" and observed["cleanup_outcome"] == "verified"
        assert any(artifact["path"] == "results/out.txt" for artifact in observed["artifacts"])
        destination = project_root / "combined-successor-export"
        successor_id = jobs(successor, owned)["jobs"][0]["job_id"]
        _response(_run(BINARY, env, "export", successor_id, "--to", str(destination), "--json"))
        assert (destination / "artifacts/results/out.txt").read_bytes() == b"ready\n"
        assert counter.read_bytes() == b"xxxxx"
        assert _response(_run(BINARY, env, "resource", "inspect", "project-local", "--json"))["data"]["occupancy"] == 0


def test_reference_project_freezes_queued_source_and_refuses_host_guest_entrypoint(project_root):
    project = project_root / "project"
    result = subprocess.run(["make", "test"], cwd=project, text=True, capture_output=True)
    assert result.returncode == 0
    result = subprocess.run(["make", "test-vm-guest"], cwd=project, text=True, capture_output=True,
                            env={"PATH": "/usr/bin:/bin"})
    assert result.returncode != 0 and "managed runner Job/lease context" in result.stderr
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        paused = _response(_run(BINARY, env, "resource", "pause", "project-local", "--expected-revision", "1",
                                "--request-key", "project-pause", "--json"))["data"]
        run = project_root / "queued"
        process = subprocess.Popen(["/usr/bin/python3", str(project / "host.py"), "--haepari", str(BINARY),
                                    "--resource", "project-local", "--run-dir", str(run)],
                                   env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                if (run / "run.json").exists():
                    record = jobs(run, owned)
                    if record["jobs"][0].get("job_id"):
                        break
                time.sleep(0.02)
            else:
                pytest.fail("caller did not persist the queued Job ID")
            (project / "source/input.txt").write_text("changed after admission\n")
            _response(_run(BINARY, env, "resource", "resume", "project-local", "--expected-revision",
                           str(paused["availability_revision"]), "--request-key", "project-resume", "--json"))
            stdout, stderr = process.communicate(timeout=20)
            assert process.returncode == 0, (stdout, stderr)
            record = jobs(run, owned)
            destination = project_root / "export"
            _response(_run(BINARY, env, "export", record["jobs"][0]["job_id"], "--to", str(destination), "--json"))
            assert (destination / "artifacts/results/out.txt").read_bytes() == b"ready\n"
            assert (project / "source/input.txt").read_text() == "changed after admission\n"
        finally:
            if process.poll() is None:
                process.terminate()
                process.communicate(timeout=5)


def test_reference_project_missing_controller_and_vm_are_truthful_prerequisite_failures(project_root):
    project = project_root / "project"
    env = {"PATH": "/usr/bin:/bin", "HAEPARI_HOME": "/tmp/hp-project-missing-" + project_root.name}
    result = host(project_root, env, project_root / "no-controller")
    assert result.returncode != 0 and "PREREQUISITE_UNAVAILABLE" in result.stderr
    assert not Path(env["HAEPARI_HOME"]).exists()
    with _controller() as (parent, env, owned, _):
        result = host(project_root, env, project_root / "no-resource")
        assert result.returncode != 0 and "PREREQUISITE_UNAVAILABLE" in result.stderr
        assert not owned
        result = subprocess.run(["make", "test"], cwd=project, env=env)
        assert result.returncode == 0


def test_reference_project_rejects_lost_saved_suite_membership(project_root):
    project = project_root / "project"
    config = json.loads((project / "project.json").read_text())
    counter = project_root / "executions"
    config["suites"]["regression"]["environment"] = {"COUNT_PATH": str(counter)}
    config["suites"]["second"] = config["suites"]["regression"].copy()
    (project / "project.json").write_text(json.dumps(config))
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        run = project_root / "all"
        result = host(project_root, env, run, "all")
        assert result.returncode == 0, (result.stdout, result.stderr)
        record = jobs(run, owned)
        assert len(owned) == 2 and counter.read_bytes() == b"xx"
        for entries in ([], record["jobs"][:1]):
            corrupted = dict(record, jobs=entries)
            (run / "run.json").write_text(json.dumps(corrupted))
            result = host(project_root, env, run, "all")
            assert result.returncode == 2 and "saved suite/Job membership is incomplete" in result.stderr
            assert '"status": "PASS"' not in result.stdout
            assert counter.read_bytes() == b"xx"
        corrupted = dict(record)
        del corrupted["declared_suites"]
        (run / "run.json").write_text(json.dumps(corrupted))
        result = host(project_root, env, run, "all")
        assert result.returncode == 2 and '"status": "PASS"' not in result.stdout
        for unsupported in ([], None, dict(record, schema="unsupported")):
            (run / "run.json").write_text(json.dumps(unsupported))
            result = host(project_root, env, run, "all")
            assert result.returncode == 2 and "saved run.json has an unsupported shape or schema" in result.stderr
            assert "retain the original run directory" in result.stderr
            assert '"kind": "aggregate"' not in result.stdout and counter.read_bytes() == b"xx"
        for raw in ("", "{", json.dumps(record)[:-8]):
            (run / "run.json").write_text(raw)
            result = host(project_root, env, run, "all")
            assert result.returncode == 2 and "saved run.json is corrupt" in result.stderr
            assert "retain the original run directory" in result.stderr
            assert '"kind": "aggregate"' not in result.stdout and counter.read_bytes() == b"xx"
        (run / "run.json").write_text(json.dumps(record))
        result = host(project_root, env, run, "all")
        assert result.returncode == 0 and counter.read_bytes() == b"xx"


def test_reference_project_manual_spec_and_direct_doctor_contract(project_root):
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        ready = _response(_run(BINARY, env, "doctor", "--resource", "project-local", "--json"))
        assert ready["ok"] and ready["data"]["schema"] == "haepari.doctor/v1" and ready["data"]["ready"]
        unavailable = _response(_run(BINARY, env, "doctor", "--resource", "missing", "--json"), expected=2)
        assert unavailable["ok"] and not unavailable["data"]["ready"]
        assert unavailable["data"]["gaps"][0]["code"] == "resource_not_found"
        run = project_root / "manual"
        result = host(project_root, env, run, mode="manual")
        assert result.returncode == 5 and '"status": "PENDING"' in result.stdout
        assert '"status": "PASS"' not in result.stdout
        spec = json.loads((run / "spec-0.json").read_text())
        assert spec["suite"] == "regression" and spec["mode"] == "manual"
        assert spec["manual"] == {"scenario_document": "scenarios/check.md", "required_scenarios": ["expected-output"]}
        assert spec["deadlines"]["manual_seconds"] == 60 and "run_seconds" not in spec["deadlines"]
        record = jobs(run, owned)
        key = record["jobs"][0]["request_key"]
        saved_spec = (run / "spec-0.json").read_bytes()
        assert spec["deadlines"]["collect_seconds"] == spec["deadlines"]["cleanup_seconds"] == 90
        result = host(project_root, env, run, mode="manual")
        assert result.returncode == 5 and jobs(run, owned)["jobs"][0]["request_key"] == key
        assert (run / "spec-0.json").read_bytes() == saved_spec
        assert len(_response(_run(BINARY, env, "jobs", "list", "--json"))["data"]["jobs"]) == 1
        finish_synthetic_manual(parent, env, owned, run)
        assert host(project_root, env, run, mode="manual").returncode == 0


def test_reference_project_refuses_tampering_and_another_installation(project_root):
    run = project_root / "original"
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        result = host(project_root, env, run)
        assert result.returncode == 0
        record = jobs(run, owned)
        spec = run / "spec-0.json"
        original = spec.read_bytes()
        spec.write_bytes(original + b"\n")
        result = host(project_root, env, run)
        assert result.returncode == 2 and "saved spec changed" in result.stderr
        spec.write_bytes(original)
        with _controller() as (other, other_env, other_owned, _):
            register(other, other_env)
            result = host(project_root, other_env, run)
            listed = _response(_run(BINARY, other_env, "jobs", "list", "--json"))["data"]["jobs"]
            other_owned.extend(item["receipt"]["job_id"] for item in listed)
            assert result.returncode == 2 and "installation changed" in result.stderr
            assert listed == []
        result = host(project_root, env, run)
        assert result.returncode == 0 and jobs(run, owned)["jobs"][0]["job_id"] == record["jobs"][0]["job_id"]


def test_reference_project_refuses_nested_invalid_private_and_concurrent_callers(project_root):
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        for suite in ("undeclared", "../unsafe"):
            result = host(project_root, env, project_root / ("bad-" + str(len(suite))), suite=suite)
            assert result.returncode == 2 and "consumer-declared suite" in result.stderr
        result = host(project_root, dict(env, HAEPARI_PARENT_CONTEXT="fixture"), project_root / "nested")
        assert result.returncode == 2 and "cannot enqueue" in result.stderr
        public = project_root / "public"
        public.mkdir(mode=0o755)
        result = host(project_root, env, public)
        assert result.returncode == 2 and "private owned directory" in result.stderr
        unfinished = project_root / "unfinished"
        unfinished.mkdir(mode=0o700)
        (unfinished / "keep.txt").write_text("retained")
        result = host(project_root, env, unfinished)
        assert result.returncode == 2 and "unfinished run preparation" in result.stderr
        assert (unfinished / "keep.txt").read_text() == "retained"
        locked = project_root / "locked"
        locked.mkdir(mode=0o700)
        with (locked / "caller.lock").open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            result = host(project_root, env, locked)
            assert result.returncode == 2 and "another caller owns" in result.stderr
        linked = project_root / "linked-run"
        linked.symlink_to(locked, target_is_directory=True)
        result = host(project_root, env, linked)
        assert result.returncode == 2 and "private owned directory" in result.stderr
        result = subprocess.run(["make", "test-vm", "HAEPARI=" + str(BINARY), "RESOURCE="],
                                cwd=project_root / "project", env=env, text=True, capture_output=True, timeout=5)
        assert result.returncode == 2 and "explicit resource" in result.stderr
        assert _response(_run(BINARY, env, "jobs", "list", "--json"))["data"]["jobs"] == []


def test_reference_project_wait_expiry_keeps_the_original_job(project_root):
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        paused = _response(_run(BINARY, env, "resource", "pause", "project-local", "--expected-revision", "1",
                                "--request-key", "pending-pause", "--json"))["data"]
        run = project_root / "pending"
        result = host(project_root, env, run)
        record = jobs(run, owned)
        assert result.returncode == 5 and json.loads(result.stdout.splitlines()[-1])["status"] == "PENDING"
        job_id = record["jobs"][0]["job_id"]
        observed = _response(_run(BINARY, env, "inspect", job_id, "--json"))["data"]
        assert observed["receipt"]["job_state"] == "queued"
        _response(_run(BINARY, env, "resource", "resume", "project-local", "--expected-revision",
                       str(paused["availability_revision"]), "--request-key", "pending-resume", "--json"))
        result = host(project_root, env, run)
        assert result.returncode == 0 and jobs(run, owned)["jobs"][0]["job_id"] == job_id


def test_reference_project_peer_faults_preserve_original_identity_and_unknown_outcome(project_root):
    # A test-owned protocol proxy forwards to the actual isolated controller,
    # then injects malformed peer observations; it cannot create a new key.
    proxy = project_root / "haepari-proxy"
    proxy.write_text('''#!/usr/bin/python3
import json, os, subprocess, sys
r = subprocess.run([os.environ["HAEPARI_REAL_CLI"], *sys.argv[1:]], capture_output=True, text=True)
reply = json.loads(r.stdout)
code = r.returncode
fault = os.environ["HAEPARI_TEST_REPLY"]
if sys.argv[1] == "submit" and fault == "different-job":
    reply["data"]["job_id"] = "foreign-job"
elif sys.argv[1] == "submit" and fault == "submission-refused":
    reply = {"schema":"haepari.envelope/v1", "operation":"submit", "ok":False,
             "error":{"code":"CONFLICT", "message":"injected conflict", "retry":"none"}}
    code = 2
elif sys.argv[1] == "submit" and fault == "invalid-request":
    reply = {"schema":"haepari.envelope/v1", "operation":"submit", "ok":False,
             "error":{"code":"INVALID_REQUEST", "message":"injected invalid declaration", "retry":"none"}}
    code = 2
elif sys.argv[1] == "wait" and fault == "verdict-exit":
    code = 1
elif sys.argv[1] == "wait" and fault == "transport-uncertain":
    reply = {"schema":"haepari.envelope/v1", "operation":"wait", "ok":False,
             "error":{"code":"TRANSPORT_UNCERTAIN", "message":"injected observation loss", "retry":"retry_same_key"}}
    code = 6
print(json.dumps(reply))
sys.exit(code)
''')
    proxy.chmod(0o700)
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        run = project_root / "original"
        result = host(project_root, env, run)
        assert result.returncode == 0
        job_id = jobs(run, owned)["jobs"][0]["job_id"]
        for fault, message in (("different-job", "different Job"), ("submission-refused", "without changing its key"),
                               ("invalid-request", "correct the declaration and select a new empty run directory"),
                               ("verdict-exit", "verdict and CLI exit disagree")):
            fault_env = dict(env, HAEPARI_REAL_CLI=str(BINARY), HAEPARI_TEST_REPLY=fault)
            result = host(project_root, fault_env, run, binary=proxy)
            assert result.returncode == 2 and message in result.stderr
            assert '"kind": "aggregate"' not in result.stdout
            assert jobs(run, owned)["jobs"][0]["job_id"] == job_id
        fault_env = dict(env, HAEPARI_REAL_CLI=str(BINARY), HAEPARI_TEST_REPLY="transport-uncertain")
        result = host(project_root, fault_env, run, binary=proxy)
        assert result.returncode == 6 and json.loads(result.stdout.splitlines()[-1])["status"] == "UNKNOWN"
        native = _response(_run(BINARY, env, "inspect", job_id, "--json"))["data"]["result"]
        assert native["final_verdict"] == "PASS"
        listed = _response(_run(BINARY, env, "jobs", "list", "--json"))["data"]["jobs"]
        assert len(listed) == 1
        result = host(project_root, env, run)
        assert result.returncode == 0 and jobs(run, owned)["jobs"][0]["job_id"] == job_id

def test_reference_make_entrypoint_and_lost_submit_reply(project_root):
    project = project_root / "project"
    counter = project_root / "executions"
    config = json.loads((project / "project.json").read_text())
    config["suites"]["regression"]["environment"] = {"COUNT_PATH": str(counter)}
    (project / "project.json").write_text(json.dumps(config))
    proxy = project_root / "drop-submit-reply"
    proxy.write_text('''#!/usr/bin/python3
import os, subprocess, sys
r = subprocess.run([os.environ["HAEPARI_REAL_CLI"], *sys.argv[1:]], capture_output=True, text=True)
if sys.argv[1] == "submit":
    sys.stderr.write("injected loss after real admission")
    sys.exit(6)
sys.stdout.write(r.stdout)
sys.stderr.write(r.stderr)
sys.exit(r.returncode)
''')
    proxy.chmod(0o700)
    def make(env, run, binary=BINARY, *settings):
        return subprocess.run(["make", "test-vm", "HAEPARI=" + str(binary), "RESOURCE=project-local",
                               "RUN_DIR=" + str(run), *settings], cwd=project, env=env,
                              text=True, capture_output=True, timeout=25)
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        run = project_root / "make run with spaces"
        result = make(dict(env, HAEPARI_REAL_CLI=str(BINARY)), run, proxy)
        listed = _response(_run(BINARY, env, "jobs", "list", "--json"))["data"]["jobs"]
        owned.extend(item["receipt"]["job_id"] for item in listed)
        assert result.returncode == 2 and "original run directory, spec and key" in result.stderr
        record = jobs(run, owned)
        assert record["suite"] == "regression" and record["mode"] == "auto"
        assert record["jobs"][0]["submission_started"] and "job_id" not in record["jobs"][0]
        assert len(listed) == 1
        key = record["jobs"][0]["request_key"]
        job_id = listed[0]["receipt"]["job_id"]
        result = make(env, run)
        assert result.returncode == 0, (result.stdout, result.stderr)
        recovered = jobs(run, owned)
        assert recovered["jobs"][0]["request_key"] == key and recovered["jobs"][0]["job_id"] == job_id
        assert counter.read_bytes() == b"x"
        result = make(env, run)
        assert result.returncode == 0 and counter.read_bytes() == b"x"
        listed = _response(_run(BINARY, env, "jobs", "list", "--json"))["data"]["jobs"]
        assert len(listed) == 1
        manual = project_root / "make manual"
        result = make(env, manual, BINARY, "SUITE=all", "MODE=manual")
        assert result.returncode == 2 and '"status": "PENDING"' in result.stdout
        saved = json.loads((manual / "run.json").read_text())
        assert saved["suite"] == "all" and saved["mode"] == "manual"
        assert json.loads((manual / "spec-0.json").read_text())["mode"] == "manual"
        assert len(_response(_run(BINARY, env, "jobs", "list", "--json"))["data"]["jobs"]) == 2
        finish_synthetic_manual(parent, env, owned, manual)
        assert make(env, manual, BINARY, "SUITE=all", "MODE=manual").returncode == 0


def test_reference_project_malformed_config_and_peer_refuse_without_false_fail(project_root):
    project = project_root / "project"
    config_path = project / "project.json"
    original = config_path.read_bytes()
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        for index, config in enumerate(([], {"suites": []}, {"suites": {"regression": "not-a-recipe"}})):
            config_path.write_text(json.dumps(config))
            result = host(project_root, env, project_root / f"invalid-config-{index}")
            assert result.returncode == 2 and "Traceback" not in result.stderr
            assert '"kind": "aggregate"' not in result.stdout
        config_path.write_text("{")
        malformed = project_root / "invalid-json-config"
        result = host(project_root, env, malformed)
        assert result.returncode == 2 and "project.json is invalid JSON" in result.stderr
        assert not (malformed / "run.json").exists()
        config_path.write_bytes(original)
        assert _response(_run(BINARY, env, "jobs", "list", "--json"))["data"]["jobs"] == []
    stub = project_root / "incompatible-cli"
    stub.write_text('''#!/usr/bin/python3
import os, signal, sys
if os.environ.get("HAEPARI_TEST_SIGNAL"):
    os.kill(os.getpid(), signal.SIGKILL)
sys.stdout.write(os.environ["HAEPARI_TEST_ENVELOPE"])
''')
    stub.chmod(0o700)
    ready = {"schema": "haepari.doctor/v1", "ready": True, "installation_id": "fixture", "gaps": []}
    envelopes = ["not-json", json.dumps({"schema": "foreign", "ok": True}),
                 json.dumps({"schema": "haepari.envelope/v1", "ok": True, "data": None}),
                 json.dumps({"schema": "haepari.envelope/v1", "ok": False, "error": []})]
    for data in (dict(ready, ready="yes"), dict(ready, installation_id=None), dict(ready, gaps=None),
                 dict(ready, gaps=[None]), dict(ready, gaps=[{"message": 1}])):
        envelopes.append(json.dumps({"schema": "haepari.envelope/v1", "ok": True, "data": data}))
    for index, envelope in enumerate(envelopes):
        env = dict(os.environ, HAEPARI_TEST_ENVELOPE=envelope)
        result = host(project_root, env, project_root / f"bad-peer-{index}", binary=stub)
        assert result.returncode == 2 and "Traceback" not in result.stderr
        assert '"kind": "aggregate"' not in result.stdout
        assert not (project_root / f"bad-peer-{index}" / "run.json").exists()
    result = host(project_root, dict(os.environ, HAEPARI_TEST_SIGNAL="1", HAEPARI_TEST_ENVELOPE=""),
                  project_root / "interrupted", binary=stub)
    assert result.returncode == 2 and "original run directory, spec and key" in result.stderr
    result = host(project_root, os.environ.copy(), project_root / "missing-binary", binary=project_root / "absent")
    assert result.returncode == 2 and "compatible Haepari CLI" in result.stderr


def test_reference_project_selected_input_guards_and_malformed_guest_context(project_root):
    project = project_root / "project"
    selected = project / "source/input.txt"
    original = selected.read_bytes()
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        for kind in ("symlink", "hardlink", "fifo"):
            selected.unlink()
            target = project_root / (kind + "-target")
            target.write_bytes(original)
            if kind == "symlink":
                selected.symlink_to(target)
            elif kind == "hardlink":
                os.link(target, selected)
            else:
                os.mkfifo(selected)
            run = project_root / (kind + "-run")
            result = host(project_root, env, run)
            assert result.returncode == 2 and "Traceback" not in result.stderr
            assert not (run / "run.json").exists() and not (run / "spec-0.json").exists()
            assert '"kind": "aggregate"' not in result.stdout
            selected.unlink()
            selected.write_bytes(original)
        source = project / "source"
        retained = project_root / "real-source"
        source.rename(retained)
        try:
            source.symlink_to(retained, target_is_directory=True)
            run = project_root / "parent-symlink-run"
            result = host(project_root, env, run)
            assert result.returncode == 2 and "without links" in result.stderr
            assert not (run / "run.json").exists()
        finally:
            source.unlink()
            retained.rename(source)
        assert _response(_run(BINARY, env, "jobs", "list", "--json"))["data"]["jobs"] == []
    for context in ("garbage", json.dumps([{"job_id": "valid", "lease_generation": 0, "nonce": "a" * 64}]),
                    json.dumps([{"job_id": "../invalid", "lease_generation": 1, "nonce": "a" * 64}]),
                    json.dumps([{"job_id": "valid", "lease_generation": 1, "nonce": "invalid"}])):
        result = subprocess.run(["make", "test-vm-guest"], cwd=project, text=True, capture_output=True,
                                env=dict(os.environ, HAEPARI_PARENT_CONTEXT=context))
        assert result.returncode != 0 and "managed runner Job/lease context" in result.stderr
        assert not (project / "results").exists()


def test_reference_project_deadlines_retain_admission_and_bound_doctor(project_root):
    project = project_root / "project"
    counter = project_root / "deadline-executions"
    config = json.loads((project / "project.json").read_text())
    config["suites"]["regression"]["environment"] = {"COUNT_PATH": str(counter)}
    (project / "project.json").write_text(json.dumps(config))
    proxy = project_root / "delayed-submit-reply"
    proxy.write_text('''#!/usr/bin/python3
import os, subprocess, sys, time
r = subprocess.run([os.environ["HAEPARI_REAL_CLI"], *sys.argv[1:]], capture_output=True, text=True)
if sys.argv[1] == "submit":
    time.sleep(30)
sys.stdout.write(r.stdout)
sys.stderr.write(r.stderr)
sys.exit(r.returncode)
''')
    proxy.chmod(0o700)
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        run = project_root / "caller-deadline"
        started = time.monotonic()
        result = host(project_root, dict(env, HAEPARI_REAL_CLI=str(BINARY)), run, binary=proxy)
        listed = _response(_run(BINARY, env, "jobs", "list", "--json"))["data"]["jobs"]
        owned.extend(item["receipt"]["job_id"] for item in listed)
        assert result.returncode == 2 and "caller deadline expired" in result.stderr
        assert "original run directory, spec and key" in result.stderr
        assert 24 <= time.monotonic() - started < 40
        assert '"kind": "aggregate"' not in result.stdout and len(listed) == 1
        record = jobs(run, owned)
        assert record["jobs"][0]["submission_started"] and "job_id" not in record["jobs"][0]
        job_id = listed[0]["receipt"]["job_id"]
        assert host(project_root, env, run).returncode == 0
        assert jobs(run, owned)["jobs"][0]["job_id"] == job_id and counter.read_bytes() == b"x"
    # Fault peer only: private short state/socket, no real controller or VM.
    with tempfile.TemporaryDirectory(prefix="hp-doctor-hang-", dir="/tmp") as temporary:
        root = Path(temporary).resolve()
        listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        listener.bind(str(root / "control.sock"))
        os.chmod(root / "control.sock", 0o600)
        listener.listen(1)
        stopped = threading.Event()
        accepted = threading.Event()
        errors = []
        def stall():
            try:
                with listener.accept()[0] as connection:
                    accepted.set()
                    connection.recv(4096)
                    if not stopped.wait(30):
                        errors.append("CLI failed to release its deadline")
            except OSError as exc:
                errors.append(str(exc))
        thread = threading.Thread(target=stall)
        thread.start()
        try:
            started = time.monotonic()
            result = _run(BINARY, {"PATH": "/usr/bin:/bin", "HOME": str(root), "TMPDIR": str(root), "LANG": "C", "HAEPARI_HOME": str(root)}, "doctor", "--resource", "missing", "--json", timeout=30)
            response = _response(result, expected=2)
            assert accepted.is_set() and 19 <= time.monotonic() - started < 30
            assert not response["ok"] and response["error"]["code"] == "PREREQUISITE_UNAVAILABLE"
            assert "intended controller" in response["error"]["message"]
        finally:
            stopped.set()
            listener.close()
            thread.join(timeout=2)
        assert not thread.is_alive() and not errors
        assert sorted(p.name for p in root.iterdir()) == ["control.sock"]


def test_reference_project_refuses_selected_input_changed_during_read(project_root, monkeypatch):
    # Coordinate an actual write between the before/after metadata reads without
    # adding a production injection seam or depending on file size/race luck.
    monkeypatch.syspath_prepend(str(project_root / "project"))
    module = runpy.run_path(str(project_root / "project/host.py"))
    selected = project_root / "project/source/input.txt"
    start = threading.Event()
    changed = threading.Event()
    original = os.fdopen
    class CoordinatedStream:
        def __init__(self, stream):
            self.stream = stream
        def __enter__(self):
            return self
        def __exit__(self, *args):
            return self.stream.__exit__(*args)
        def fileno(self):
            return self.stream.fileno()
        def read(self):
            start.set()
            assert changed.wait(2)
            return self.stream.read()
    def mutate():
        assert start.wait(2)
        selected.write_bytes(b"changed during capture\n")
        changed.set()
    thread = threading.Thread(target=mutate)
    thread.start()
    try:
        monkeypatch.setattr(os, "fdopen", lambda *args, **kwargs: CoordinatedStream(original(*args, **kwargs)))
        with pytest.raises(module["Refusal"], match="selected input changed during capture"):
            module["regular_bytes"](selected)
    finally:
        start.set()
        thread.join(timeout=2)
    assert not thread.is_alive() and selected.read_bytes() == b"changed during capture\n"
