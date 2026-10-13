"""Synthetic manual records through real CLI/controller/native local workers."""

import json
import time

import pytest

from test_local_cli import BINARY, _controller, _response, _restart_job_fixture, _run
from test_project_inputs import independent_tree


def manual_fixture(parent, env, seconds=20, setup=None):
    path, marker = _restart_job_fixture(parent, env, "manual-local")
    source = parent / "input"
    (source / "source/scenarios.md").write_text("Synthetic checks one and two.\n")
    spec = json.loads(path.read_text())
    spec.update(schema="haepari.job/v2", mode="manual", suite="manual-regression",
                manual={"scenario_document": "source/scenarios.md", "required_scenarios": ["one", "two"]})
    selected = ["source/input.txt", "source/scenarios.md"]
    spec["input"].update(selected_files=selected, inventory_digest=independent_tree(source, selected))
    spec["deadlines"].pop("run_seconds")
    spec["deadlines"]["manual_seconds"] = seconds
    spec["workload"]["argv"] = ["/bin/sh", "-c", 'printf smoke >> "$COUNT_PATH"; mkdir -p ../results; cat input.txt > ../results/out.txt']
    if setup is not None:
        spec["setup"] = {"argv": ["/bin/sh", "-c", setup]}
    path.write_text(json.dumps(spec))
    return path, marker, spec


def submit(env, owned, path, key):
    receipt = _response(_run(BINARY, env, "submit", "--spec", str(path), "--request-key", key, "--json"))["data"]
    owned.append(receipt["job_id"])
    return receipt


def awaiting(env, job_id):
    end = time.monotonic() + 8
    while time.monotonic() < end:
        data = _response(_run(BINARY, env, "inspect", job_id, "--json"))["data"]
        if data["receipt"]["job_state"] == "awaiting_manual":
            assert "result" not in data
            return data["receipt"]
        if data["receipt"]["job_state"] == "finished":
            pytest.fail(f"manual preparation did not become awaiting: {data}")
        time.sleep(0.02)
    pytest.fail("manual preparation did not reach awaiting within fixture budget")


def record(parent, env, receipt, statuses=("pass", "pass")):
    doctor = _response(_run(BINARY, env, "doctor", "--resource", "manual-local", "--json"))["data"]
    payload = {"schema": "haepari.manual-result/v1", "job_id": receipt["job_id"],
               "installation_id": doctor["installation_id"], "profile_id": receipt["admission"]["environment_profile_id"],
               "profile_digest": receipt["admission"]["environment_profile_digest"],
               "expected_revision": receipt["job_revision"], "observer": "synthetic-fixture",
               "scenarios": [{"id": name, "status": status, "evidence_references": [] if status == "unobserved" else ["fixture:declared-observation"]}
                             for name, status in zip(("one", "two"), statuses)]}
    path = parent / "manual-result.json"
    path.write_text(json.dumps(payload))
    return path, payload


def complete(env, job_id, path, revision):
    return _run(BINARY, env, "manual", "complete", job_id, "--input", str(path), "--expected-revision", str(revision), "--json")


@pytest.mark.parametrize("statuses, expected, verdict", [(("pass", "pass"), 0, "PASS"), (("pass", "fail"), 1, "FAIL"),
                                                          (("pass", "unobserved"), 3, "INCOMPLETE"), (("fail", "unobserved"), 3, "INCOMPLETE")])
def test_manual_outcomes_exclusive_hold_and_exact_conflicting_retry(statuses, expected, verdict):
    with _controller() as (parent, env, owned, restart):
        path, marker, spec = manual_fixture(parent, env, setup='printf setup >> "$COUNT_PATH"')
        first = submit(env, owned, path, "manual-original")
        ready = awaiting(env, first["job_id"])
        assert marker.read_bytes() == b"setupsmoke"
        assert _run(BINARY, env, "wait", first["job_id"], "--timeout", "100ms", "--json").returncode == 5
        resource = _response(_run(BINARY, env, "resource", "inspect", "manual-local", "--json"))["data"]
        assert resource["occupancy"] == 1
        successor = dict(spec, mode="auto", manual=None, suite="successor")
        successor.pop("manual")
        successor["deadlines"] = dict(spec["deadlines"], run_seconds=10)
        successor["deadlines"].pop("manual_seconds")
        successor.pop("setup", None)
        other = parent / "successor.json"
        other.write_text(json.dumps(successor))
        second = submit(env, owned, other, "manual-successor")
        assert _response(_run(BINARY, env, "inspect", second["job_id"], "--json"))["data"]["receipt"]["job_state"] == "queued"
        result_path, payload = record(parent, env, ready, statuses)
        original = _response(complete(env, first["job_id"], result_path, ready["job_revision"]))["data"]
        final = _response(_run(BINARY, env, "wait", first["job_id"], "--timeout", "10s", "--json"), expected)["data"]
        assert final["final_verdict"] == verdict and final["cleanup_outcome"] == "verified"
        if "unobserved" in statuses:
            assert final["evidence_completeness"] == "partial"
            assert any(g["reason"] == "manual_observation_unobserved" for g in final["evidence_gaps"])
        if "fail" in statuses:
            assert final["workload_outcome"] == "fail"
            assert 'native_exit_code' not in final and 'native_signal' not in final
            events = _response(_run(BINARY, env, "events", first["job_id"], "--json"))["data"]["events"]
            preparation = [e for e in events if e['kind'] == 'manual_preparation_stopped']
            assert len(preparation) == 1
            assert preparation[0]['payload']['outcome']['workload_outcome'] == 'pass'
            assert preparation[0]['payload']['outcome']['native_exit_code'] == 0
        _response(_run(BINARY, env, "wait", second["job_id"], "--timeout", "10s", "--json"))
        assert marker.read_bytes() == b"setupsmokesmoke"
        restart(crash=True)
        assert _response(complete(env, first["job_id"], result_path, ready["job_revision"]))["data"] == original
        payload["observer"] = "different-observer"
        result_path.write_text(json.dumps(payload))
        conflict = _response(complete(env, first["job_id"], result_path, ready["job_revision"]), 2)
        assert conflict["error"]["code"] == "CONFLICT"
        current = _response(_run(BINARY, env, "inspect", first["job_id"], "--json"))["data"]["receipt"]
        purged = _response(_run(BINARY, env, "purge", first["job_id"], "--expected-revision", str(current["job_revision"]), "--json"))["data"]
        assert purged["complete"]
        # Settlement and owned cleanup were verified before explicit retention.
        owned.remove(first["job_id"])
        payload["observer"] = "synthetic-fixture"
        result_path.write_text(json.dumps(payload))
        assert _response(complete(env, first["job_id"], result_path, ready["job_revision"]))["data"] == original


@pytest.mark.parametrize("action", ["cancel", "deadline", "restart"])
def test_manual_waiter_exit_deadline_cancel_and_restart_original_identity(action):
    with _controller() as (parent, env, owned, restart):
        path, marker, _ = manual_fixture(parent, env, seconds=2 if action == "deadline" else 20)
        first = submit(env, owned, path, "manual-lifetime")
        ready = awaiting(env, first["job_id"])
        assert _run(BINARY, env, "wait", first["job_id"], "--timeout", "100ms", "--json").returncode == 5
        if action == "cancel":
            _response(_run(BINARY, env, "cancel", first["job_id"], "--expected-revision", str(ready["job_revision"]), "--request-key", "manual-abandon", "--json"))
            final = _response(_run(BINARY, env, "wait", first["job_id"], "--timeout", "10s", "--json"), 4)["data"]
            assert final["final_verdict"] == "CANCELLED"
        elif action == "deadline":
            final = _response(_run(BINARY, env, "wait", first["job_id"], "--timeout", "10s", "--json"), 3)["data"]
            assert final["final_verdict"] == "INCOMPLETE"
            assert final['workload_outcome'] == 'timed_out' and final['reason'] == 'manual_deadline'
            assert 'native_exit_code' not in final and 'native_signal' not in final
        else:
            before = _response(_run(BINARY, env, "events", first["job_id"], "--json"))["data"]
            restart(crash=True)
            current = awaiting(env, first["job_id"])
            after = _response(_run(BINARY, env, "events", first["job_id"], "--json"))["data"]
            assert [e for e in before["events"] if e["kind"] == "awaiting_manual"] == [e for e in after["events"] if e["kind"] == "awaiting_manual"]
            result_path, _ = record(parent, env, current)
            _response(complete(env, first["job_id"], result_path, current["job_revision"]))
            final = _response(_run(BINARY, env, "wait", first["job_id"], "--timeout", "10s", "--json"))["data"]
        assert final["cleanup_outcome"] == "verified" and marker.read_bytes() == b"smoke"
        assert _response(_run(BINARY, env, "resource", "inspect", "manual-local", "--json"))["data"]["occupancy"] == 0


def test_manual_completion_rejects_foreign_stale_duplicate_missing_and_unsealed_scenarios():
    import copy
    with _controller() as (parent, env, owned, _):
        path, _, spec = manual_fixture(parent, env)
        spec['manual']['scenario_document'] = 'source/not-selected.md'
        path.write_text(json.dumps(spec))
        refused = _response(_run(BINARY, env, 'submit', '--spec', str(path), '--request-key', 'unsealed-manual', '--json'), 2)
        assert refused['error']['code'] == 'INVALID_REQUEST'
        assert 'manual.scenario_document' in refused['error']['message']
        assert 'input.selected_files' in refused['error']['message']
        assert 'not-selected.md' not in refused['error']['message']
        spec['manual']['scenario_document'] = 'source/scenarios.md'
        path.write_text(json.dumps(spec))
        first = submit(env, owned, path, 'unsealed-manual')
        ready = awaiting(env, first['job_id'])
        result_path, original = record(parent, env, ready)
        negatives = []
        wrong_schema = copy.deepcopy(original)
        wrong_schema['schema'] = 'private-unsupported-schema-value'
        negatives.append((wrong_schema, 'UNSUPPORTED'))
        for field, value in [('job_id', 'foreign-job'), ('installation_id', 'foreign-install'),
                             ('profile_id', 'foreign-profile'), ('profile_digest', 'b' * 64)]:
            payload = copy.deepcopy(original)
            payload[field] = value
            negatives.append((payload, 'INVALID_REQUEST'))
        stale = copy.deepcopy(original)
        stale['expected_revision'] -= 1
        negatives.append((stale, 'STALE_REVISION'))
        missing = copy.deepcopy(original)
        missing['scenarios'].pop()
        negatives.append((missing, 'INVALID_REQUEST'))
        duplicate = copy.deepcopy(original)
        duplicate['scenarios'][1] = duplicate['scenarios'][0]
        negatives.append((duplicate, 'INVALID_REQUEST'))
        no_ref = copy.deepcopy(original)
        no_ref['scenarios'][0]['evidence_references'] = []
        negatives.append((no_ref, 'INVALID_REQUEST'))
        unknown = copy.deepcopy(original)
        unknown['environment_profile_id'] = 'secret-value-not-for-diagnostics'
        result_path.write_text(json.dumps(unknown))
        rejected = _response(complete(env, ready['job_id'], result_path, ready['job_revision']), 2)
        assert rejected['error']['code'] == 'INVALID_REQUEST'
        assert 'environment_profile_id' in rejected['error']['message']
        assert 'secret-value-not-for-diagnostics' not in rejected['error']['message']
        raw = json.dumps(original)
        result_path.write_text(raw[:-1] + ', "observer":"secret-duplicate-observer"}')
        refused = complete(env, ready['job_id'], result_path, ready['job_revision'])
        assert refused.returncode == 2
        assert 'observer' in refused.stderr and 'duplicated' in refused.stderr
        assert 'secret-duplicate-observer' not in refused.stderr
        for payload, error in negatives:
            result_path.write_text(json.dumps(payload))
            rejected = _response(complete(env, ready['job_id'], result_path, payload['expected_revision']), 2)
            assert rejected['error']['code'] == error
            if error == 'UNSUPPORTED':
                assert 'Manual result schema' in rejected['error']['message']
                assert 'haepari.manual-result/v1' in rejected['error']['message']
                assert wrong_schema['schema'] not in rejected['error']['message']
            current = awaiting(env, ready['job_id'])
            assert current['job_revision'] == ready['job_revision'] and current['resource_occupancy'] == 1
        result_path.write_text(json.dumps(original).replace('"observer":', '"observer":"duplicate","observer":'))
        malformed = complete(env, ready['job_id'], result_path, ready['job_revision'])
        assert malformed.returncode == 2 and 'Manual result observer is duplicated.' in malformed.stderr
        result_path.write_text(json.dumps(original))
        _response(complete(env, ready['job_id'], result_path, ready['job_revision']))
        _response(_run(BINARY, env, 'wait', ready['job_id'], '--timeout', '10s', '--json'))


@pytest.mark.parametrize('action', ['setup-failure', 'setup-signal', 'cancel-setup', 'restart-setup', 'prepare-deadline'])
def test_manual_preparation_failure_cancel_restart_and_deadline_do_not_repeat_setup(action):
    with _controller() as (parent, env, owned, restart):
        setup = 'printf setup >> "$COUNT_PATH"'
        if action == 'setup-failure':
            setup += '; exit 7'
        elif action == 'setup-signal':
            setup += '; kill -TERM $$'
        else:
            setup += '; while [ ! -f "$RELEASE_PATH" ]; do /bin/sleep 0.02; done'
        path, marker, spec = manual_fixture(parent, env, setup=setup)
        if action == 'prepare-deadline':
            spec['deadlines']['prepare_seconds'] = 1
            path.write_text(json.dumps(spec))
        first = submit(env, owned, path, 'manual-preparation-lifetime')
        end = time.monotonic() + 8
        while not marker.exists() and time.monotonic() < end:
            time.sleep(0.02)
        assert marker.read_bytes() == b'setup'
        if action == 'cancel-setup':
            current = _response(_run(BINARY, env, 'inspect', first['job_id'], '--json'))['data']['receipt']
            _response(_run(BINARY, env, 'cancel', first['job_id'], '--expected-revision', str(current['job_revision']), '--request-key', 'cancel-preparation', '--json'))
        if action == 'restart-setup':
            restart(crash=True)
            (parent / 'release').touch()
            ready = awaiting(env, first['job_id'])
            result_path, _ = record(parent, env, ready)
            _response(complete(env, first['job_id'], result_path, ready['job_revision']))
        expected = 0 if action == 'restart-setup' else 3
        final = _response(_run(BINARY, env, 'wait', first['job_id'], '--timeout', '10s', '--json'), expected)['data']
        assert final['cleanup_outcome'] == 'verified'
        assert marker.read_bytes() == (b'setupsmoke' if action == 'restart-setup' else b'setup')
        if action == 'prepare-deadline':
            assert final['workload_outcome'] == 'timed_out'
            # Positively absent physical output retains the existing public reason.
            assert final['reason'] == 'required_output_absent'
            events = _response(_run(BINARY, env, 'events', first['job_id'], '--json'))['data']['events']
            assert any(e['kind'] == 'manual_aborted' and e['payload']['reason'] == 'prepare_deadline' for e in events)
            assert 'native_exit_code' not in final and 'native_signal' not in final
        if action == 'cancel-setup':
            assert final['workload_outcome'] == 'cancelled' and final['final_verdict'] == 'INCOMPLETE'
        if action == 'setup-failure':
            assert final['native_exit_code'] == 7
        if action == 'setup-signal':
            assert final['native_signal'] == 'terminated' and 'native_exit_code' not in final


def test_manual_completion_cancel_race_settles_one_durable_decision():
    from concurrent.futures import ThreadPoolExecutor
    with _controller() as (parent, env, owned, _):
        path, marker, _ = manual_fixture(parent, env)
        first = submit(env, owned, path, 'manual-race')
        ready = awaiting(env, first['job_id'])
        result_path, _ = record(parent, env, ready)
        with ThreadPoolExecutor(max_workers=2) as pool:
            completed = pool.submit(complete, env, ready['job_id'], result_path, ready['job_revision'])
            cancelled = pool.submit(_run, BINARY, env, 'cancel', ready['job_id'], '--expected-revision', str(ready['job_revision']), '--request-key', 'manual-race-cancel', '--json')
            completion_reply, cancellation_reply = completed.result(), cancelled.result()
        assert sorted([completion_reply.returncode, cancellation_reply.returncode]) == [0, 2]
        expected = 0 if completion_reply.returncode == 0 else 4
        final = _response(_run(BINARY, env, 'wait', ready['job_id'], '--timeout', '10s', '--json'), expected)['data']
        assert final['final_verdict'] == ('PASS' if expected == 0 else 'CANCELLED')
        assert final['cleanup_outcome'] == 'verified' and marker.read_bytes() == b'smoke'
        if expected == 0:
            _response(complete(env, ready['job_id'], result_path, ready['job_revision']))
        else:
            assert complete(env, ready['job_id'], result_path, ready['job_revision']).returncode == 2


def test_manual_pending_preparation_refusal_names_original_job_and_field_error():
    with _controller() as (parent, env, owned, _):
        path, _, _ = manual_fixture(parent, env)
        _response(_run(BINARY, env, 'resource', 'pause', 'manual-local', '--expected-revision', '1', '--request-key', 'manual-pending-pause', '--json'))
        receipt = submit(env, owned, path, 'manual-pending-completion')
        doctor = _response(_run(BINARY, env, 'doctor', '--resource', 'manual-local', '--json'))['data']
        record = {'schema': 'haepari.manual-result/v1', 'job_id': receipt['job_id'], 'installation_id': doctor['installation_id'],
                  'profile_id': receipt['admission']['environment_profile_id'], 'profile_digest': receipt['admission']['environment_profile_digest'],
                  'expected_revision': receipt['job_revision'], 'observer': 'synthetic-pending', 'scenarios': [
                      {'id': name, 'status': 'pass', 'evidence_references': ['fixture:check']} for name in ('one', 'two')]}
        result_path = parent / 'pending-result.json'
        result_path.write_text(json.dumps(record))
        refused = _response(complete(env, receipt['job_id'], result_path, receipt['job_revision']), 2)
        assert refused['error']['code'] == 'RECOVERY_BLOCKED'
        assert 'preparation' in refused['error']['message'] and 'quarantine' not in refused['error']['message']
        record['scenarios'][0]['evidence_references'] = []
        result_path.write_text(json.dumps(record))
        refused = _response(complete(env, receipt['job_id'], result_path, receipt['job_revision']), 2)
        assert refused['error']['code'] == 'INVALID_REQUEST' and 'evidence_references' in refused['error']['message']
        _response(_run(BINARY, env, 'cancel', receipt['job_id'], '--expected-revision', str(receipt['job_revision']), '--request-key', 'manual-pending-owned-cancel', '--json'))
        _response(_run(BINARY, env, 'wait', receipt['job_id'], '--timeout', '5s', '--json'), 4)
