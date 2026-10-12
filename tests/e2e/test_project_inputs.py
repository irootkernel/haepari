"""Opaque consumer input helpers through actual local Job admission."""

import hashlib
import json
from pathlib import Path
import runpy
import struct

import pytest

from test_local_cli import BINARY, _controller, _response, _run
from test_project_cli import host, jobs, project_root, register


def helpers():
    return runpy.run_path(str(Path(__file__).resolve().parents[2] / "examples/project-test/haepari_inputs.py"))


def independent_tree(root, names):
    digest = hashlib.sha256(b"haepari-tree-sha256/v1\0")
    for name in sorted(names, key=lambda value: value.encode("utf-8")):
        path = root / name
        data = path.read_bytes()
        encoded = name.encode("utf-8")
        digest.update(struct.pack(">Q", len(encoded)) + encoded + bytes([bool(path.stat().st_mode & 0o111)]))
        digest.update(struct.pack(">Q", len(data)) + hashlib.sha256(data).digest())
    return digest.hexdigest()


def test_skill_only_payload_raw_profile_and_executable_modes(project_root):
    project = project_root / "project"
    (project / "skills").mkdir()
    (project / "skills/SKILL.md").write_bytes(b"# Consumer-owned skill\n" + b"Opaque instruction bytes.\n" * 7000)
    profile = b"\x00raw consumer profile\xff\n"
    (project / "profile.json").write_bytes(profile)
    script = project / "tests/vm/run.sh"
    script.write_text("#!/bin/sh\nset -eu\ntest -x ../tests/vm/run.sh\ntest ! -x SKILL.md\nmkdir -p ../results\ncat SKILL.md > ../results/out.txt\nprintf synthetic-output > ../results/server.key\n")
    script.chmod(0o700)
    config = json.loads((project / "project.json").read_text())
    selected = ["skills/SKILL.md", "tests/vm/run.sh", "profile.json"]
    config["inputs"] = {"selected_files": selected, "candidate_files": [selected[0]],
                        "test_files": [selected[1]], "profile_file": selected[2], "profile_id": "opaque-skill"}
    config["suites"]["regression"]["cwd"] = "skills"
    config["suites"]["regression"]["required_outputs"].append("results/server.key")
    (project / "project.json").write_text(json.dumps(config))
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        run = project_root / "skill-run"
        result = host(project_root, env, run)
        assert result.returncode == 0, (result.stdout, result.stderr)
        record = jobs(run, owned)
        spec_bytes = (run / "spec-0.json").read_bytes()
        spec = json.loads(spec_bytes)
        assert spec["environment"] == {"profile_id": "opaque-skill", "profile_digest": hashlib.sha256(profile).hexdigest()}
        assert spec["input"]["inventory_digest"] == independent_tree(project, selected)
        assert spec["provenance"]["candidate_digest"] == independent_tree(project, [selected[0]])
        assert spec["provenance"]["tests_digest"] == independent_tree(project, [selected[1]])
        module = helpers()
        again = project_root / "second-bundle"
        assert module["capture_inputs"](project, again, config["inputs"])["provenance"] == spec["provenance"]
        (project / "project.json").write_text("changed current policy is not consulted on replay")
        (project / "profile.json").unlink()
        result = host(project_root, env, run)
        assert result.returncode == 0, (result.stdout, result.stderr)
        assert jobs(run, owned)["jobs"][0]["job_id"] == record["jobs"][0]["job_id"]
        assert (run / "spec-0.json").read_bytes() == spec_bytes
        exported = project_root / "skill-export"
        _response(_run(BINARY, env, "export", owned[0], "--to", str(exported), "--json"))
        assert (exported / "artifacts/results/out.txt").read_bytes() == (project / "skills/SKILL.md").read_bytes()
        assert (exported / "artifacts/results/server.key").read_bytes() == b"synthetic-output"


@pytest.mark.parametrize("selected", [["../outside"], ["/outside"], ["source//input.txt"], ["source/input.txt", "source/input.txt"],
                                       ["Source/a", "source/b"], ["source", "source/input.txt"], [".env"], ["source/secret.key"], ["caf\u00e9/a", "cafe\u0301/b"], ["caf\u00e9.md", "cafe\u0301.md"]])
def test_unsafe_selection_refused_before_copy(project_root, selected):
    module = helpers()
    policy = {"selected_files": selected, "candidate_files": selected, "test_files": selected,
              "profile_file": selected[0], "profile_id": "opaque"}
    bundle = project_root / "unsafe-bundle"
    with pytest.raises(module["Refusal"]):
        module["capture_inputs"](project_root / "project", bundle, policy)
    assert not bundle.exists()


def test_missing_input_and_corrupt_bundle_preserve_original_key(project_root):
    project = project_root / "project"
    original = (project / "source/input.txt").read_bytes()
    (project / "source/input.txt").unlink()
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        run = project_root / "missing"
        result = host(project_root, env, run)
        assert result.returncode == 2 and not (run / "run.json").exists()
        assert not owned
        (project / "source/input.txt").write_bytes(original)
        # Prepare a genuine saved request, then corrupt its selected bundle
        # before first admission. Restoration reuses the exact spec and key.
        module = helpers()
        bundle = project_root / "corrupt-bundle"
        policy = json.loads((project / "project.json").read_text())["inputs"]
        inputs = module["capture_inputs"](project, bundle, policy)
        spec = module["job_spec"](inputs, "project-local", "regression", "auto",
                                  {"argv": ["/bin/sh", "../tests/vm/run.sh"], "cwd": "source",
                                   "authorization_reference": "project-owned-offline-test", "required_outputs": ["results/out.txt"]})
        path = project_root / "corrupt-spec.json"
        path.write_text(json.dumps(spec))
        key = "task018-corrupt-original"
        (bundle / "source/input.txt").write_bytes(b"corrupt\n")
        refused = _response(_run(BINARY, env, "submit", "--spec", str(path), "--request-key", key, "--json"), 2)
        assert not refused["ok"]
        (bundle / "source/input.txt").write_bytes(original)
        receipt = _response(_run(BINARY, env, "submit", "--spec", str(path), "--request-key", key, "--json"))["data"]
        owned.append(receipt["job_id"])
        _response(_run(BINARY, env, "wait", receipt["job_id"], "--timeout", "15s", "--json"))
        replay = _response(_run(BINARY, env, "submit", "--spec", str(path), "--request-key", key, "--json"))["data"]
        assert replay["job_id"] == receipt["job_id"]


@pytest.mark.parametrize("changes", [None, {"candidate_files": ["missing/file"]}, {"test_files": []},
                                       {"profile_file": "missing/profile"}, {"profile_id": ""},
                                       {"profile_id": "space is invalid"}, {"profile_id": "p" * 129},
                                       {"profile_id": 7}, {"selected_files": ["\ud800"]}, {"selected_files": ["p" * 513]},
                                       {"selected_files": [f"payload/{index}" for index in range(1001)]}])
def test_invalid_policy_refuses_before_bundle(project_root, changes):
    module = helpers()
    policy = json.loads((project_root / "project/project.json").read_text())["inputs"]
    policy = None if changes is None else dict(policy, **changes)
    bundle = project_root / "invalid-policy"
    with pytest.raises(module["Refusal"]):
        module["capture_inputs"](project_root / "project", bundle, policy)
    assert not bundle.exists()


@pytest.mark.parametrize("changes", [{"argv": []}, {"argv": ["x"] * 129}, {"required_outputs": []},
                                       {"environment": {"MY-VAR": "x"}}, {"environment": {"OK": 7}},
                                       {"cwd": "../escape"}, {"cwd": "/escape"}, {"cwd": "missing"},
                                       {"cwd": "source/input.txt"}, {"authorization_reference": ""}])
def test_invalid_recipe_uses_refusal(project_root, changes):
    module = helpers()
    config = json.loads((project_root / "project/project.json").read_text())
    inputs = module["capture_inputs"](project_root / "project", project_root / "recipe-bundle", config["inputs"])
    recipe = dict(config["suites"]["regression"], **changes)
    with pytest.raises(module["Refusal"]):
        module["job_spec"](inputs, "project-local", "regression", "auto", recipe)


@pytest.mark.parametrize("manual", [None, "not-a-mapping", {"scenario_document": "../outside", "required_scenarios": ["one"]},
                                      {"scenario_document": "scenarios/check.md", "required_scenarios": []},
                                      {"scenario_document": "scenarios/check.md", "required_scenarios": ["one", "one"]},
                                      {"scenario_document": "scenarios/check.md", "required_scenarios": ["invalid id"]}])
def test_manual_recipe_refuses_without_admission(project_root, manual):
    module = helpers()
    config_path = project_root / "project/project.json"
    config = json.loads(config_path.read_text())
    config["suites"]["regression"]["manual"] = manual
    inputs = module["capture_inputs"](project_root / "project", project_root / "manual-bundle", config["inputs"])
    with pytest.raises(module["Refusal"]):
        module["job_spec"](inputs, "project-local", "regression", "manual", config["suites"]["regression"])
    config_path.write_text(json.dumps(config))
    with _controller() as (parent, env, owned, _):
        register(parent, env)
        run = project_root / "invalid-manual"
        result = host(project_root, env, run, mode="manual")
        assert result.returncode == 2 and not (run / "run.json").exists() and not (run / "bundle").exists()
        assert not owned
        assert _response(_run(BINARY, env, "jobs", "list", "--json"))["data"]["jobs"] == []


@pytest.mark.parametrize("field, changes", [("test_files", {"test_files": []}), ("candidate_files", {"candidate_files": []})])
def test_policy_message_names_omitted_group(project_root, field, changes):
    module = helpers()
    policy = json.loads((project_root / "project/project.json").read_text())["inputs"]
    with pytest.raises(module["Refusal"], match=field):
        module["capture_inputs"](project_root / "project", project_root / "missing-group", dict(policy, **changes))
