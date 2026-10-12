"""Offline campaign guards; no installed service, SSH server or VM is used."""

import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import tempfile
import time

import pytest


MODULE_PATH = Path(__file__).with_name("utm_campaign.py")
spec = importlib.util.spec_from_file_location("utm_campaign", MODULE_PATH)
campaign = importlib.util.module_from_spec(spec)
spec.loader.exec_module(campaign)


@pytest.fixture
def private_root():
    volume = Path("/Volumes/RootKernel")
    if os.path.ismount(volume) and os.access(volume, os.W_OK):
        parent = volume / "tmp"
        parent.mkdir(exist_ok=True)
    else:
        parent = Path(tempfile.gettempdir()).resolve()
    root = Path(tempfile.mkdtemp(prefix="haepari-campaign-test-", dir=parent))
    try:
        yield root
    finally:
        shutil.rmtree(root)


def write(path, value):
    path.write_text(json.dumps(value) if isinstance(value, dict) else value)
    path.chmod(0o600)
    return path


def manifest(root):
    digest = "sha256:" + "a" * 64
    result = {"schema": campaign.SCHEMA, "campaign_id": "offline-test",
              "repository": str(Path.cwd()), "source_revision": "a" * 40,
              "binary": str(root / "binary"), "binary_sha256": digest,
              "driver_sha256": campaign.digest(MODULE_PATH.read_bytes()),
              "plan_file": str(root / "plan"), "plan_sha256": digest,
              "aggregate_seconds": 600, "command_seconds": 2, "controls": {},
              "resource": {"id": "registered-one", "configuration_digest": "b" * 64,
                           "config_revision": 1, "uuid": "11111111-1111-1111-1111-111111111111",
                           "mac": "02:00:00:00:00:01", "connection_reference": digest + ":" + str(root / "mapping"),
                           "known_hosts_reference": digest + ":" + str(root / "pins"), "registration_file": str(root / "registration")},
              "rows": {}}
    for row in campaign.ROWS:
        observe = {"id": row + "-resource", "action": "resource", "seconds": 10}
        check = {"id": row + "-check", "action": "check", "seconds": 10,
                 "record": observe["id"], "checks": [{"path": ["occupancy"], "equals": 0}]}
        result["rows"][row] = [observe, check]
    for number in range(30):
        result["rows"]["soak"].insert(number, {"id": f"soak-{number}", "action": "submit", "seconds": 10,
                 "spec_file": str(root / "spec"), "spec_sha256": digest, "request_key": f"saved-{number}"})
    result["rows"]["large_log"].insert(0, {"id": "large-submit", "action": "submit", "seconds": 10,
                 "spec_file": str(root / "spec"), "spec_sha256": digest, "request_key": "large-key"})
    result["rows"]["large_log"].insert(1, {"id": "large-logs", "action": "logs", "seconds": 10,
                 "job": "large-submit", "size": 50 * 1024 * 1024, "sha256": digest})
    return result


def instance(root):
    value = manifest(root)
    path = write(root / "campaign.json", value)
    authority = write(root / "authority.json", {
        "schema": "haepari.utm-campaign-authority/v1", "campaign_sha256": campaign.digest(path.read_bytes()),
        "approval_reference": "offline-test-only", "expires_at": time.time() + 600,
        "effects": ["submit", "cancel", "purge", "export", "recover_apply"], "restoration": "no VM effects in offline test",
    })
    evidence = root / "evidence"
    evidence.mkdir(mode=0o700)
    return campaign.Campaign(path, authority, evidence)


@pytest.mark.parametrize("mutation", [
    lambda m: m["rows"].pop("success_reuse"),
    lambda m: m.update(aggregate_seconds=True),
    lambda m: m.update(command_seconds=0),
    lambda m: m["rows"]["soak"].pop(0),
    lambda m: m["rows"]["large_log"][1].update(size=1024),
    lambda m: m["rows"]["soak"][1].update(request_key="saved-0"),
    lambda m: m.update(unknown="not accepted"),
    lambda m: m["rows"]["success_reuse"][0].update(action="install"),
    lambda m: m["rows"]["success_reuse"][0].update(until=[], action="control"),
    lambda m: m["rows"]["success_reuse"][1].update(checks=True),
    lambda m: m["rows"]["success_reuse"][1].update(checks=[{"equals": 0}]),
    lambda m: m["rows"]["success_reuse"][1].update(checks=[{"path": [True], "equals": 0}]),
    lambda m: m["rows"]["success_reuse"][0].update(until=[]),
    lambda m: m["rows"]["success_reuse"][0].update(until=[{"path": [-1], "equals": 0}]),
])
def test_missing_rows_invalid_bounds_and_unsafe_operations_refuse(private_root, mutation):
    value = manifest(private_root)
    mutation(value)
    with pytest.raises(campaign.CampaignError):
        campaign.validate(value)


@pytest.mark.parametrize("row", ["cold_start", "guest_reboot", "power_intent", "identity_refusal"])
def test_excluded_scenarios_are_not_part_of_campaign(private_root, row):
    value = manifest(private_root)
    campaign.validate(value)
    value["rows"][row] = copy.deepcopy(value["rows"]["success_reuse"])
    with pytest.raises(campaign.CampaignError, match="complete single-VM matrix"):
        campaign.validate(value)


def test_withdrawn_power_recovery_action_refuses(private_root):
    value = manifest(private_root)
    value["rows"]["success_reuse"].insert(0, {
        "id": "power-plan", "action": "recover_plan", "seconds": 10,
        "kind": "resource", "plan_action": "attest-power-stopped",
    })
    with pytest.raises(campaign.CampaignError, match="unapproved recovery action"):
        campaign.validate(value)


def test_authority_cannot_bind_different_candidate_bytes(private_root):
    driver = instance(private_root)
    driver.close()
    path = private_root / "campaign.json"
    value = json.loads(path.read_text())
    value["source_revision"] = "b" * 40
    write(path, value)
    with pytest.raises(campaign.CampaignError, match="authority binds different"):
        campaign.Campaign(path, private_root / "authority.json", private_root / "evidence")


def test_one_observer_and_unchanged_checkpoint_are_required(private_root):
    driver = instance(private_root)
    try:
        with pytest.raises(campaign.CampaignError, match="another campaign observer"):
            campaign.Campaign(private_root / "campaign.json", private_root / "authority.json", private_root / "evidence")
        driver.state["started_at"] = time.time() - 20
        driver.checkpoint()
    finally:
        driver.close()
    resumed = campaign.Campaign(private_root / "campaign.json", private_root / "authority.json", private_root / "evidence")
    try:
        assert resumed.state["started_at"] == driver.state["started_at"]
        assert resumed.remaining() < 581
    finally:
        resumed.close()


def test_linked_parent_and_shared_file_refuse(private_root):
    source = write(private_root / "owned.json", {})
    link = private_root / "link"
    link.symlink_to(private_root, target_is_directory=True)
    with pytest.raises(campaign.CampaignError, match="symlink"):
        campaign.read_private(link / source.name)
    source.chmod(0o644)
    with pytest.raises(campaign.CampaignError, match="private owned"):
        campaign.read_private(source)


def test_duplicate_keys_and_false_boolean_equality_refuse():
    with pytest.raises(campaign.CampaignError, match="duplicate"):
        campaign.decode('{"ok":true,"ok":false}')
    with pytest.raises(campaign.CampaignError, match="mismatch"):
        campaign.check_values({"count": True}, [{"path": ["count"], "equals": 1}])


def test_make_live_gate_refuses_missing_explicit_inputs():
    result = campaign.subprocess.run(
        ["/usr/bin/make", "test-utm"], cwd=MODULE_PATH.parents[2],
        env={"PATH": "/usr/bin:/bin"}, capture_output=True, timeout=10)
    assert result.returncode != 0
    assert b"HAEPARI_UTM_CAMPAIGN" in result.stderr


def test_selected_input_digest_includes_bytes_and_mode(private_root):
    source = write(private_root / "input.txt", "actual selected bytes")
    name, raw = b"input.txt", source.read_bytes()
    expected = hashlib.sha256(b"haepari-tree-sha256/v1\0" + struct.pack(">Q", len(name)) + name
                              + b"\0" + struct.pack(">Q", len(raw)) + hashlib.sha256(raw).digest()).hexdigest()
    job = {"input": {"root": str(private_root), "selected_files": ["input.txt"], "inventory_digest": expected}}
    assert campaign.input_digest(job) == expected
    source.chmod(0o700)
    with pytest.raises(campaign.CampaignError, match="inventory changed"):
        campaign.input_digest(job)


def test_lost_submit_reply_reuses_exact_saved_spec_key_and_job(private_root, monkeypatch):
    driver = instance(private_root)
    try:
        step = driver.manifest["rows"]["large_log"][0]
        saved_spec = {"resource_id": "registered-one", "original": "immutable"}
        frozen = write(driver.root / (step["id"] + ".spec.json"), saved_spec)
        step["spec_sha256"] = campaign.digest(frozen.read_bytes())
        write(driver.root / (step["id"] + ".submission.json"),
              {"spec": saved_spec, "spec_sha256": step["spec_sha256"], "request_key": step["request_key"]})
        entry = {"submit_issued": True, "expires_at": time.time() + 10, "pending_job_id": "original-job"}
        calls = []

        def submit(args, *_):
            calls.append(copy.deepcopy(args))
            assert json.loads(Path(args[2]).read_text()) == saved_spec
            if len(calls) == 1:
                raise campaign.CampaignError("lost reply after native admission")
            return {"job_id": "original-job", "resource_id": "registered-one"}

        monkeypatch.setattr(driver, "cli", submit)
        with pytest.raises(campaign.CampaignError, match="lost reply"):
            driver.execute(step, entry)
        assert driver.execute(step, entry)["job_id"] == "original-job"
        assert calls[0] == calls[1]
        assert entry["expires_at"] < time.time() + 11
    finally:
        driver.close()


def test_resume_issued_admission_uses_frozen_bytes_after_original_inputs_disappear(private_root, monkeypatch):
    driver = instance(private_root)
    try:
        job = {"schema": "haepari.job/v2", "execution_mode": "managed", "resource_id": "registered-one",
               "mode": "auto", "effects": {"mode": "offline"}, "deadlines": {
                   name: 10 for name in ("queue_seconds", "prepare_seconds", "run_seconds", "collect_seconds", "cleanup_seconds")}}
        original = write(private_root / "spec", job)
        expected = campaign.digest(original.read_bytes())
        submits = [s for row in campaign.ROWS for s in driver.manifest["rows"][row] if s["action"] == "submit"]
        for step in submits:
            step["spec_sha256"] = expected
        monkeypatch.setattr(campaign, "input_digest", lambda *_: None)
        driver.freeze_submissions()
        for step in submits:
            assert (driver.root / (step["id"] + ".spec.json")).read_bytes() == original.read_bytes()
            driver.state["steps"][step["id"]] = {"submit_issued": True}
        original.unlink()
        monkeypatch.setattr(campaign, "input_digest", lambda *_: pytest.fail("issued request must use sealed inputs"))
        driver.freeze_submissions()
        frozen = driver.root / (submits[0]["id"] + ".spec.json")
        write(frozen, dict(job, mode="changed"))
        with pytest.raises(campaign.CampaignError, match="spec changed"):
            driver.freeze_submissions()
    finally:
        driver.close()


def test_uncertain_control_is_never_reissued(private_root, monkeypatch):
    driver = instance(private_root)
    try:
        monkeypatch.setattr(driver, "call", lambda *_: pytest.fail("must not issue a second control"))
        with pytest.raises(campaign.CampaignError, match="do not repeat"):
            driver.execute({"id": "fault", "action": "control", "control": "restart"},
                           {"expires_at": time.time() + 10, "control_issued": True})
    finally:
        driver.close()


@pytest.mark.parametrize("gap,identity,offset,more", [(True, "same", 0, False), (False, "other", 2, False),
                                                  (False, "same", 9, False), (False, "same", 2, True)])
def test_log_gap_replacement_bad_offset_and_stall_refuse(private_root, monkeypatch, gap, identity, offset, more):
    driver = instance(private_root)
    try:
        pages = iter([
            {"job_id": "job", "identity": "same", "offset": 0, "next_offset": 2, "size": 4, "data": "YWI=", "more": True},
            {"job_id": "job", "identity": identity, "offset": offset, "next_offset": offset,
             "size": 4, "data": "", "more": more, "gap": {"reason": "lost"} if gap else None},
        ])
        monkeypatch.setattr(driver, "cli", lambda *_: next(pages))
        with pytest.raises(campaign.CampaignError):
            driver.logs("job", {"size": 4, "sha256": campaign.digest(b"abcd")}, time.time() + 10)
    finally:
        driver.close()


def test_logs_drain_all_pages_and_verify_full_hash(private_root, monkeypatch):
    driver = instance(private_root)
    try:
        offsets = []

        def logs(args, *_):
            offset = int(args[3])
            offsets.append(offset)
            return {"job_id": "job", "identity": "same", "offset": offset, "next_offset": offset + 2,
                    "size": 4, "data": "YWI=" if offset == 0 else "Y2Q=", "more": offset == 0}

        monkeypatch.setattr(driver, "cli", logs)
        result = driver.logs("job", {"size": 4, "sha256": campaign.digest(b"abcd")}, time.time() + 10)
        assert offsets == [0, 2] and result["sha256"] == campaign.digest(b"abcd")
    finally:
        driver.close()


def test_event_gap_foreign_job_and_cursor_stall_refuse(private_root, monkeypatch):
    driver = instance(private_root)
    try:
        for page in [
            {"resync": {"job_id": "job"}, "snapshot": {"job_id": "job"}},
            {"snapshot": {"job_id": "other"}},
            {"snapshot": {"job_id": "job"}, "events": [], "next_cursor": 0, "more": True},
        ]:
            monkeypatch.setattr(driver, "cli", lambda *_, page=page: page)
            with pytest.raises(campaign.CampaignError):
                driver.events("job", time.time() + 10)
    finally:
        driver.close()


def test_complete_execution_never_claims_qualification(private_root):
    driver = instance(private_root)
    try:
        for row in campaign.ROWS:
            for step in driver.manifest["rows"][row]:
                driver.state["steps"][step["id"]] = {"status": "complete", "result": {"observed": True}}
        driver.checkpoint()
        report = driver.report()
        assert report["execution"] == "observations_collected"
        assert report["qualification"].startswith("unverified")
        assert "DF-004" in report["deferred"]
        driver.state["steps"].pop("soak-29")
        assert driver.report()["execution"] == "incomplete"
        retained = driver.root / driver.state["steps"]["success_reuse-resource"]["result_ref"]["file"]
        retained.unlink()
        report = driver.report()
        assert report["execution"] == "incomplete"
        assert "success_reuse-resource" in report["observation_gaps"]
    finally:
        driver.close()


def test_run_executes_dependencies_in_order_and_resume_skips_completed_steps(private_root, monkeypatch):
    driver = instance(private_root)
    calls = []
    interrupt = driver.manifest["rows"]["soak"][15]["id"]
    try:
        actual_execute = driver.execute

        def observation(step, entry):
            calls.append(step["id"])
            if step["id"] == interrupt and calls.count(interrupt) == 1:
                raise campaign.CampaignError("observer interrupted")
            if step["action"] == "check":
                return actual_execute(step, entry)
            return {"job_id": step["id"] + "-job", "occupancy": 0, "observed": True}

        monkeypatch.setattr(driver, "execute", observation)
        with pytest.raises(campaign.CampaignError, match="observer interrupted"):
            driver.run()
        deadline = driver.state["steps"][interrupt]["expires_at"]
        started = driver.state["started_at"]
    finally:
        driver.close()
    resumed = campaign.Campaign(private_root / "campaign.json", private_root / "authority.json", private_root / "evidence")
    try:
        actual_execute = resumed.execute
        monkeypatch.setattr(resumed, "execute", observation)
        assert resumed.run()["execution"] == "observations_collected"
        expected = [step["id"] for row in campaign.ROWS for step in resumed.manifest["rows"][row]]
        assert calls == expected[:expected.index(interrupt) + 1] + expected[expected.index(interrupt):]
        assert resumed.state["steps"][interrupt]["expires_at"] == deadline
        assert resumed.state["started_at"] == started
        count = len(calls)
        assert resumed.run()["execution"] == "observations_collected"
        assert len(calls) == count
        # Missing or replaced retained observations cannot report success.
        reference = resumed.state["steps"]["success_reuse-resource"]["result_ref"]
        write(resumed.root / reference["file"], {"occupancy": 99})
        result = resumed.report()
        assert result["execution"] == "incomplete"
        assert "success_reuse-resource" in result["observation_gaps"]
    finally:
        resumed.close()


def test_actual_cli_timeout_retains_uncertain_attempt_and_original_budget(private_root):
    driver = instance(private_root)
    try:
        binary = write(private_root / "binary", "#!/bin/sh\n/bin/sleep 5\n")
        binary.chmod(0o700)
        driver.manifest["binary_sha256"] = campaign.file_digest(binary)
        driver.plan = {"root": str(private_root)}
        expires = time.time() + 0.1
        with pytest.raises(campaign.CampaignError, match="return uncertain"):
            driver.call(["inspect", "job", "--json"], "inspect", expires)
        attempt = json.loads((driver.root / "attempt-000001.json").read_text())
        assert attempt["return"] == "uncertain" and attempt["timeout_seconds"] <= 0.1
        assert time.time() >= expires
    finally:
        driver.close()


def test_expiry_during_checkpoint_refuses_command_issuance(private_root, monkeypatch):
    driver = instance(private_root)
    try:
        binary = write(private_root / "binary", "#!/bin/sh\nexit 0\n")
        binary.chmod(0o700)
        driver.manifest["binary_sha256"] = campaign.file_digest(binary)
        driver.plan = {"root": str(private_root)}
        original = driver.checkpoint

        def delayed_checkpoint():
            original()
            driver.authority["expires_at"] = driver.now() - 1

        monkeypatch.setattr(driver, "checkpoint", delayed_checkpoint)
        monkeypatch.setattr(campaign.subprocess, "Popen", lambda *_a, **_k: pytest.fail("expired command must not launch"))
        with pytest.raises(campaign.CampaignError, match="budget exhausted"):
            driver.call(["inspect", "job", "--json"], "inspect")
        assert json.loads((driver.root / "attempt-000001.json").read_text())["return"] == "uncertain"
    finally:
        driver.close()


def test_results_over_checkpoint_limit_remain_restartable_and_digest_bound(private_root):
    driver = instance(private_root)
    try:
        payload = "native-event-evidence" * 60000
        for number in range(30):
            driver.state["steps"][f"soak-{number}"] = {
                "action": "events", "status": "complete", "expires_at": time.time() + 10,
                "result": {"events": [payload], "original_job": f"job-{number}"}}
        driver.state["started_at"] = time.time()
        driver.checkpoint()
        assert sum(p.stat().st_size for p in driver.root.glob("*.result.json")) > 32 << 20
        assert driver.state_path.stat().st_size < 10000
    finally:
        driver.close()
    resumed = campaign.Campaign(private_root / "campaign.json", private_root / "authority.json", private_root / "evidence")
    try:
        entry = resumed.state["steps"]["soak-0"]
        assert resumed.result(entry) == {"events": [payload], "original_job": "job-0"}
        assert resumed.report()["execution"] == "incomplete"
        write(resumed.root / entry["result_ref"]["file"], {"events": ["replacement"]})
        with pytest.raises(campaign.CampaignError, match="artifact changed"):
            resumed.result(entry)
    finally:
        resumed.close()


def test_native_socket_peer_proof_survives_atomic_name_publication(private_root):
    original, published = private_root / ".howned", private_root / "control.sock"
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener:
        listener.bind(str(original))
        listener.listen()
        original.rename(published)
        assert campaign.socket_peer_pid(published, os.getpid(), 1) == os.getpid()
        with pytest.raises(campaign.CampaignError, match="another process"):
            campaign.socket_peer_pid(published, os.getpid() + 1, 1)


@pytest.mark.parametrize("changed", [
    {"source_revision": "b" * 40}, {"source_modified": True},
    {"source_modified": 0}, {"executable_sha256": "sha256:" + "b" * 64},
    {"platform": "linux/arm64"},
])
def test_public_version_must_bind_clean_source_and_executable(private_root, changed):
    value = manifest(private_root)
    version = {"source_revision": value["source_revision"], "source_modified": False,
               "executable_sha256": value["binary_sha256"], "platform": "darwin/arm64"}
    campaign.verify_version(version, value)
    version.update(changed)
    with pytest.raises(campaign.CampaignError, match="public version differs"):
        campaign.verify_version(version, value)


@pytest.mark.parametrize("mutation", [
    lambda m: m.update(source_modified=True),
    lambda m: m.update(source_modified=1),
    lambda m: m.update(candidate_inventory_file="/undeclared"),
    lambda m: m.update(source_modified=True, candidate_inventory_file="relative",
                       candidate_inventory_sha256="sha256:" + "a" * 64),
])
def test_modified_source_requires_explicit_complete_inventory(private_root, mutation):
    value = manifest(private_root)
    mutation(value)
    with pytest.raises(campaign.CampaignError):
        campaign.validate(value)


@pytest.mark.parametrize("change", ["tracked", "untracked", "mode", "symlink", "inventory", "head"])
def test_modified_candidate_accepts_exact_bytes_and_refuses_drift(private_root, change):
    repository = private_root / "repository"
    repository.mkdir(mode=0o700)

    def git(*args):
        return subprocess.check_output(["git", "-C", str(repository), *args],
                                       stderr=subprocess.PIPE, timeout=10)

    git("init", "-q")
    tracked = write(repository / "tracked.txt", "base")
    git("add", "tracked.txt")
    git("-c", "user.name=Offline test", "-c", "user.email=offline@example.invalid",
        "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
        "commit", "-qm", "offline fixture")
    write(tracked, "selected")
    untracked = write(repository / "new.txt", "selected new file")
    value = manifest(private_root)
    value.update(repository=str(repository), source_revision=git("rev-parse", "HEAD").decode().strip(),
                 source_modified=True)
    inventory = write(private_root / "candidate.json",
                      campaign.capture_candidate(repository, value["binary_sha256"]))
    value.update(candidate_inventory_file=str(inventory),
                 candidate_inventory_sha256=campaign.digest(inventory.read_bytes()))
    campaign.validate(value)
    campaign.verify_candidate(value)
    version = {"source_revision": value["source_revision"], "source_modified": True,
               "executable_sha256": value["binary_sha256"], "platform": "darwin/arm64"}
    campaign.verify_version(version, value)
    version["source_modified"] = False
    with pytest.raises(campaign.CampaignError, match="public version differs"):
        campaign.verify_version(version, value)
    if change == "tracked":
        write(tracked, "changed after approval")
    elif change == "untracked":
        write(untracked, "changed after approval")
    elif change == "mode":
        untracked.chmod(0o700)
    elif change == "symlink":
        untracked.unlink()
        untracked.symlink_to(tracked)
    elif change == "inventory":
        write(inventory, {})
    else:
        git("add", "tracked.txt")
        git("-c", "user.name=Offline test", "-c", "user.email=offline@example.invalid",
            "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
            "commit", "-qm", "different fixture HEAD")
    with pytest.raises(campaign.CampaignError):
        campaign.verify_candidate(value)


def test_recollection_then_default_health_replays_original_apply_token_key(private_root, monkeypatch):
    driver = instance(private_root)
    try:
        steps = [
            {"id": "recollect-plan", "action": "recover_plan", "seconds": 10,
             "kind": "resource", "plan_action": "recollect"},
            {"id": "recollect-apply", "action": "recover_apply", "seconds": 10,
             "plan": "recollect-plan", "request_key": "original-recollect"},
            {"id": "health-plan", "action": "recover_plan", "seconds": 10,
             "kind": "resource", "plan_action": "health"},
            {"id": "health-apply", "action": "recover_apply", "seconds": 10,
             "plan": "health-plan", "request_key": "original-health"},
            {"id": "health-check", "action": "check", "seconds": 10,
             "record": "health-apply", "checks": [{"path": ["availability"], "equals": "available"}]},
        ]
        driver.manifest["rows"]["success_reuse"] = steps
        campaign.validate(driver.manifest)
        calls = []

        def cli(args, *_):
            calls.append(list(args))
            if "--plan" in args:
                return {"plan_token": "recollect-token" if "--action" in args else "health-token"}
            if args[args.index("--request-key") + 1] == "original-health":
                if sum("original-health" in call for call in calls) == 1:
                    raise campaign.CampaignError("lost health apply reply")
                return {"availability": "available"}
            return {"availability": "quarantined"}

        monkeypatch.setattr(driver, "cli", cli)
        for step in steps[:3]:
            entry = {"action": step["action"], "status": "started", "expires_at": time.time() + 10}
            driver.state["steps"][step["id"]] = entry
            entry["result"] = driver.execute(step, entry)
            entry["status"] = "complete"
            driver.checkpoint()
        entry = {"action": "recover_apply", "status": "started", "expires_at": time.time() + 10}
        step = steps[3]
        driver.state["steps"][step["id"]] = entry
        with pytest.raises(campaign.CampaignError, match="lost health"):
            driver.execute(step, entry)
        assert driver.execute(step, entry)["availability"] == "available"
        assert calls[0] == ["resource", "recover", "registered-one", "--plan", "--action", "recollect", "--json"]
        assert calls[2] == ["resource", "recover", "registered-one", "--plan", "--json"]
        assert calls[-2] == calls[-1]
        assert calls[-1][calls[-1].index("--plan-token") + 1] == "health-token"
        assert calls[-1][calls[-1].index("--request-key") + 1] == "original-health"
    finally:
        driver.close()


@pytest.mark.parametrize("missing", [True, False])
def test_resume_checks_retained_observations_before_pending_purge(private_root, monkeypatch, missing):
    driver = instance(private_root)
    try:
        # A valid dependency order: the original large Job already has captured
        # events/logs, and purge is the next unfinished step after interruption.
        row = driver.manifest["rows"]["large_log"]
        row.insert(2, {"id": "large-events", "action": "events", "seconds": 10, "job": "large-submit"})
        row.append({"id": "large-purge", "action": "purge", "seconds": 10, "job": "large-submit"})
        campaign.validate(driver.manifest)
        for name in campaign.ROWS:
            for step in driver.manifest["rows"][name]:
                if step["id"] == "large-purge":
                    break
                driver.state["steps"][step["id"]] = {
                    "action": step["action"], "status": "complete", "result": {"job_id": "original-job", "captured": True}}
            if name == "large_log":
                break
        driver.state["started_at"] = time.time()
        driver.checkpoint()
        reference = driver.state["steps"]["large-events"]["result_ref"]
        artifact = driver.root / reference["file"]
        if missing:
            artifact.unlink()
        else:
            write(artifact, {"captured": "replacement"})
        monkeypatch.setattr(driver, "execute", lambda *_: pytest.fail("no effect before retained-evidence validation"))
        with pytest.raises((campaign.CampaignError, FileNotFoundError)):
            driver.run()
        assert "large-purge" not in driver.state["steps"]
    finally:
        driver.close()


def test_actual_cli_refusal_requires_matching_exit_and_error_code(private_root):
    driver = instance(private_root)
    try:
        value = json.dumps({"schema": "haepari.envelope/v1", "operation": "wait", "ok": False,
                            "error": {"code": "WAIT_TIMEOUT", "message": "observer only", "retry": "none"}})
        binary = write(private_root / "binary", "#!/bin/sh\nprintf '%s\\n' '" + value + "'\nexit 2\n")
        binary.chmod(0o700)
        driver.manifest["binary_sha256"] = campaign.file_digest(binary)
        driver.plan = {"root": str(private_root)}
        result = driver.cli(["wait", "job", "--json"], "wait", expected_exit=2, expected_error="WAIT_TIMEOUT")
        assert result["error"]["code"] == "WAIT_TIMEOUT"
        with pytest.raises(campaign.CampaignError, match="expected public refusal"):
            driver.cli(["wait", "job", "--json"], "wait", expected_exit=2, expected_error="CONFLICT")
    finally:
        driver.close()


def test_measurements_keep_incomplete_pairs_and_gap_separate_from_network_bytes():
    shared = {"identity": {"job_id": "original"}, "vm_uuid": "original-vm", "operation": "inspect",
              "request_digest": "request", "runner_digest": "runner", "deadline": "deadline", "started_at": "start"}
    start = dict(shared, id="one", complete=False)
    end = dict(shared, id="one", complete=True, archive_bytes=20, runner_bytes=0, stdout_bytes=10, stderr_bytes=0)
    events = [{"kind": "vm_ssh_exchange_start", "payload": start},
              {"kind": "vm_ssh_exchange_complete", "payload": end},
              {"kind": "vm_ssh_exchange_start", "payload": dict(start, id="unfinished")},
              {"kind": "vm_ssh_measurement_gap", "payload": {"reason": "exchange_detail_limit"}}]
    result = campaign.measurements(events)
    assert result["complete_pairs"] == 1 and result["archive_bytes"] == 20
    assert result["incomplete_starts"] == ["unfinished"] and result["detail_gaps"]
    assert result["network_protocol_overhead"].startswith("unobserved")
    events[1]["payload"] = dict(end, runner_digest="replacement")
    with pytest.raises(campaign.CampaignError, match="binding changed"):
        campaign.measurements(events)
