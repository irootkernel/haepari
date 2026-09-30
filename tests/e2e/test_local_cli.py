"""Black-box checks for the local Haepari binary and foreground controller."""

from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import stat
import struct
import subprocess
import tempfile
import time

import pytest


BINARY = Path(__file__).resolve().parents[2] / "bin" / "haepari"
DIGEST = "a" * 64


def _preflight() -> None:
    if platform.system() != "Darwin" or platform.machine() != "arm64":
        pytest.fail("local E2E requires a macOS arm64 host", pytrace=False)
    try:
        info = BINARY.lstat()
    except FileNotFoundError:
        pytest.fail(f"missing built candidate: {BINARY}; run make build", pytrace=False)
    if not stat.S_ISREG(info.st_mode) or not os.access(BINARY, os.X_OK):
        pytest.fail(f"candidate is not a regular executable: {BINARY}", pytrace=False)


def _run(binary: Path, env: dict[str, str], *args: str, timeout: int = 15) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [str(binary), *args],
        env=env,
        text=True,
        capture_output=True,
        timeout=timeout,
        check=False,
    )


def _response(result: subprocess.CompletedProcess[str], expected: int = 0) -> dict:
    assert result.returncode == expected, (result.returncode, result.stdout[:4096], result.stderr[:4096])
    return json.loads(result.stdout)


def _tree_digest(relative: str, content: bytes) -> str:
    """Independent implementation of the public haepari-tree-sha256/v1 contract."""
    name = relative.encode("utf-8")
    encoded = (
        b"haepari-tree-sha256/v1\x00"
        + struct.pack(">Q", len(name))
        + name
        + b"\x00"  # non-executable
        + struct.pack(">Q", len(content))
        + hashlib.sha256(content).digest()
    )
    return hashlib.sha256(encoded).hexdigest()


@contextmanager
def _controller():
    _preflight()
    parent = Path(tempfile.mkdtemp(prefix="hp-e2e-", dir="/tmp")).resolve()
    identity = parent.lstat()
    state = parent / "state"
    socket = state / "control.sock"
    assert identity.st_uid == os.getuid() and stat.S_IMODE(identity.st_mode) == 0o700
    assert len(os.fsencode(socket)) < 104
    env = {
        "PATH": "/usr/bin:/bin",
        "HOME": str(parent),
        "TMPDIR": str(parent),
        "LANG": "C",
        "HAEPARI_HOME": str(state),
    }
    log_path = parent / "daemon.log"
    daemon = None
    owned_jobs: list[str] = []
    cleanup_error = None
    try:
        with log_path.open("wb") as log:
            daemon = subprocess.Popen(
                [str(BINARY), "daemon", "run", "--backend", "local"],
                env=env,
                stdout=log,
                stderr=subprocess.STDOUT,
            )
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if daemon.poll() is not None:
                pytest.fail(f"controller exited during startup: {log_path.read_text()[:4096]}", pytrace=False)
            probe = _run(BINARY, env, "inspect", "readiness-probe", "--json")
            if probe.returncode == 2:
                try:
                    if json.loads(probe.stdout).get("error", {}).get("code") == "NOT_FOUND":
                        break
                except json.JSONDecodeError:
                    pass
            time.sleep(0.05)
        else:
            pytest.fail(f"controller readiness timed out: {log_path.read_text()[:4096]}", pytrace=False)
        yield parent, env, owned_jobs
    finally:
        if daemon is not None:
            if owned_jobs and daemon.poll() is not None:
                cleanup_error = "controller exited before owned jobs were rechecked"
            elif owned_jobs:
                for job_id in owned_jobs:
                    try:
                        result = _run(BINARY, env, "wait", job_id, "--timeout", "5s", "--json", timeout=8)
                        data = json.loads(result.stdout).get("data", {})
                        safe_queued_cancel = (
                            result.returncode == 4
                            and data.get("cleanup_outcome") == "not_needed"
                            and data.get("lease_generation") == 0
                            and data.get("workload_outcome") == "cancelled"
                        )
                        if result.returncode not in (0, 1, 3, 4) or (data.get("cleanup_outcome") != "verified" and not safe_queued_cancel):
                            cleanup_error = f"owned job {job_id} lacks verified cleanup"
                    except (subprocess.TimeoutExpired, json.JSONDecodeError):
                        cleanup_error = f"owned job {job_id} could not be rechecked"
            if daemon.poll() is None:
                daemon.terminate()
            try:
                daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                cleanup_error = "controller did not shut down after SIGTERM"
                daemon.kill()
                try:
                    daemon.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    cleanup_error = "started controller process did not exit"
            if daemon.returncode != 0 and cleanup_error is None:
                cleanup_error = f"controller exited with status {daemon.returncode}"
        try:
            now = parent.lstat()
            if (now.st_dev, now.st_ino) != (identity.st_dev, identity.st_ino) or not stat.S_ISDIR(now.st_mode):
                cleanup_error = "test root identity changed"
            elif socket.exists() or socket.is_symlink():
                cleanup_error = "controller socket remains after shutdown"
        except FileNotFoundError:
            cleanup_error = "test root disappeared before owned cleanup"
        if cleanup_error is None:
            shutil.rmtree(parent)
        else:
            pytest.fail(f"{cleanup_error}; retained {parent}", pytrace=False)


def test_local_admission_result_replay_and_conflict():
    with _controller() as (parent, env, owned_jobs):
        source = parent / "input"
        selected = source / "source" / "input.txt"
        selected.parent.mkdir(parents=True)
        selected.write_bytes(b"isolated input\n")
        selected.chmod(0o600)
        marker = parent / "execution-count"
        resource_path = parent / "resource.json"
        resource_path.write_text(json.dumps({
            "schema": "haepari.resource/v1",
            "id": "e2e-local",
            "kind": "local",
            "execution_mode": "managed",
            "capacity": 1,
            "revision": 1,
            "adapter": {"qualification_reference": "isolated-e2e"},
            "cleanup": {"policy_reference": "owned-workspace"},
        }))
        _response(_run(BINARY, env, "resource", "register", "--file", str(resource_path), "--json"))

        paused = _response(_run(BINARY, env, "resource", "pause", "e2e-local", "--expected-revision", "1", "--request-key", "e2e-pause", "--json"))["data"]
        pause_replay = _response(_run(BINARY, env, "resource", "pause", "e2e-local", "--expected-revision", "1", "--request-key", "e2e-pause", "--json"))["data"]
        assert paused["availability"] == "paused"
        assert pause_replay["availability_revision"] == paused["availability_revision"] == 2
        stale = _response(_run(BINARY, env, "resource", "resume", "e2e-local", "--expected-revision", "1", "--json"), 2)
        assert stale["error"]["code"] == "STALE_REVISION"
        resumed = _response(_run(BINARY, env, "resource", "resume", "e2e-local", "--expected-revision", "2", "--request-key", "e2e-resume", "--json"))["data"]
        resume_replay = _response(_run(BINARY, env, "resource", "resume", "e2e-local", "--expected-revision", "2", "--request-key", "e2e-resume", "--json"))["data"]
        assert resumed["availability"] == "available"
        assert resume_replay["availability_revision"] == resumed["availability_revision"] == 3
        assert resumed["idle_since"] == paused["idle_since"]

        job = {
            "schema": "haepari.job/v1",
            "execution_mode": "managed",
            "resource_id": "e2e-local",
            "mode": "auto",
            "suite": "local-e2e",
            "input": {
                "root": str(source),
                "inventory_digest": _tree_digest("source/input.txt", selected.read_bytes()),
                "selected_files": ["source/input.txt"],
            },
            "environment": {"profile_id": "local", "profile_digest": DIGEST},
            "workload": {
                "argv": ["/bin/sh", "-c", "mkdir -p ../results; cat input.txt > ../results/out.txt; printf x >> \"$COUNT_PATH\""],
                "cwd": "source",
                "environment": {"COUNT_PATH": str(marker)},
                "required_outputs": ["results/out.txt"],
            },
            "deadlines": {
                "queue_seconds": 30,
                "prepare_seconds": 30,
                "run_seconds": 30,
                "collect_seconds": 30,
                "cleanup_seconds": 30,
            },
            "effects": {"mode": "offline", "authorization_reference": "isolated-e2e"},
            "provenance": {
                "candidate_digest": DIGEST,
                "tests_digest": DIGEST,
                "target_profile_digest": DIGEST,
                "evidence_class": "development",
            },
        }
        job_path = parent / "job.json"
        job_path.write_text(json.dumps(job))
        submit = lambda: _response(_run(BINARY, env, "submit", "--spec", str(job_path), "--request-key", "same-key", "--json"))
        first = submit()["data"]["job_id"]
        assert first
        owned_jobs.append(first)
        final = _response(_run(BINARY, env, "wait", first, "--timeout", "30s", "--json", timeout=40))["data"]
        assert final["final_verdict"] == "PASS"
        assert final["workload_outcome"] == "pass"
        assert final["evidence_completeness"] == "complete"
        assert final["cleanup_outcome"] == "verified"
        assert final["job_id"] == first
        artifact = next(a for a in final["artifacts"] if a["path"] == "results/out.txt")
        assert artifact["sha256"] == hashlib.sha256(b"isolated input\n").hexdigest()
        assert submit()["data"]["job_id"] == first
        assert marker.read_text() == "x"
        assert _response(_run(BINARY, env, "inspect", first, "--json"))["data"]["result"]["final_verdict"] == "PASS"

        job["suite"] = "changed-payload"
        job_path.write_text(json.dumps(job))
        rejected = _response(_run(BINARY, env, "submit", "--spec", str(job_path), "--request-key", "same-key", "--json"), expected=2)
        assert rejected["error"]["code"] == "CONFLICT"
        assert marker.read_text() == "x"

        resources = _response(_run(BINARY, env, "resources", "list", "--limit", "1", "--json"))["data"]
        assert resources["resources"][0]["resource_id"] == "e2e-local"
        jobs = _response(_run(BINARY, env, "jobs", "list", "--resource", "e2e-local", "--state", "finished", "--json"))["data"]
        assert jobs["jobs"][0]["receipt"]["job_id"] == first
        cursor = 0
        observed = []
        while True:
            page = _response(_run(BINARY, env, "events", first, "--after", str(cursor), "--limit", "1", "--json"))["data"]
            observed += page["events"]
            cursor = page["next_cursor"]
            if not page["more"]:
                break
        assert observed and all(a["cursor"] < b["cursor"] for a, b in zip(observed, observed[1:]))
        stream = _run(BINARY, env, "events", first, "--after", str(cursor), "--follow", "--json")
        assert stream.returncode == 0, stream.stderr
        records = [json.loads(line) for line in stream.stdout.splitlines()]
        assert records[-1]["type"] == "end" and records[-1]["cursor"] == cursor
        assert all(record["schema"] == "haepari.observer/v1" for record in records)
        late = _response(_run(BINARY, env, "cancel", first, "--expected-revision", "1", "--request-key", "late-cancel", "--json"))["data"]
        assert late["receipt"]["verdict"] == "PASS"
        assert not late["cancellation"]["requested"]

        job["suite"] = "failed-observation"
        job["workload"]["argv"] = ["/bin/sh", "-c", "mkdir -p ../results; printf result > ../results/out.txt; printf 'observed log\\n'; exit 7"]
        job_path.write_text(json.dumps(job))
        failure = _run(BINARY, env, "run", "--spec", str(job_path), "--request-key", "failed-job", "--json", timeout=40)
        assert failure.returncode == 1, (failure.stdout, failure.stderr)
        run_records = [json.loads(line) for line in failure.stdout.splitlines()]
        failed_id = run_records[0]["data"]["job_id"]
        owned_jobs.append(failed_id)
        assert run_records[-1]["data"]["workload_outcome"] == "fail"
        assert run_records[-1]["data"]["native_exit_code"] == 7
        inspected = _response(_run(BINARY, env, "inspect", failed_id, "--json"))
        assert inspected["ok"] and inspected["data"]["result"]["final_verdict"] == "FAIL"
        logs = _response(_run(BINARY, env, "logs", failed_id, "--json"))["data"]
        import base64
        assert base64.b64decode(logs["data"]) == b"observed log\n"
        assert logs["next_offset"] == len(b"observed log\n")
        plain = _run(BINARY, env, "logs", failed_id)
        assert plain.returncode == 0 and plain.stdout == "observed log\n" and plain.stderr == ""
        missing = _run(BINARY, env, "events", "absent-observer-job", "--follow", "--json")
        assert missing.returncode == 2 and "NOT_FOUND" in missing.stderr
        tail = _run(BINARY, env, "logs", failed_id, "--after", str(logs["next_offset"]), "--identity", logs["identity"], "--follow", "--json")
        assert tail.returncode == 0, tail.stderr
        assert json.loads(tail.stdout.splitlines()[-1])["type"] == "end"

        _response(_run(BINARY, env, "resource", "pause", "e2e-local", "--expected-revision", "3", "--request-key", "queue-pause", "--json"))
        job["suite"] = "queued-cancel"
        job_path.write_text(json.dumps(job))
        queued = _response(_run(BINARY, env, "submit", "--spec", str(job_path), "--request-key", "queued-job", "--json"))["data"]
        queued_id = queued["job_id"]
        owned_jobs.append(queued_id)
        timed = _response(_run(BINARY, env, "wait", queued_id, "--timeout", "50ms", "--json"), 5)
        assert timed["error"]["code"] == "WAIT_TIMEOUT"
        cancelled = _response(_run(BINARY, env, "cancel", queued_id, "--expected-revision", str(queued["job_revision"]), "--request-key", "cancel-queued", "--json"))["data"]
        replay = _response(_run(BINARY, env, "cancel", queued_id, "--expected-revision", str(queued["job_revision"]), "--request-key", "cancel-queued", "--json"))["data"]
        assert replay["mutation"] == cancelled["mutation"]
        assert cancelled["receipt"]["lease_generation"] == 0 and cancelled["receipt"]["global_occupancy"] == 0
        final_cancel = _response(_run(BINARY, env, "wait", queued_id, "--json"), 4)["data"]
        assert final_cancel["final_verdict"] == "CANCELLED" and final_cancel["cleanup_outcome"] == "not_needed"
