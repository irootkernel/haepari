"""Consumer-owned opaque inputs; capture requires a trusted private bundle parent."""

from contextlib import contextmanager
import errno
import hashlib
import os
from pathlib import Path
import re
import stat
import struct
import unicodedata


class Refusal(Exception):
    pass


@contextmanager
def regular_file(path):
    path = Path(path).absolute()
    parent = os.open(path.anchor, os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in path.parts[1:-1]:
            successor = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
            os.close(parent)
            parent = successor
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
    except OSError as exc:
        if exc.errno in (errno.ELOOP, errno.EMLINK, errno.ENOTDIR):
            raise Refusal(f"selected input must use real directories and a regular file, without links: {path}") from None
        raise
    finally:
        os.close(parent)
    with os.fdopen(fd, "rb") as stream:
        before = os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1:
            raise Refusal(f"selected input must be a regular file with one link: {path}")
        yield stream, before
        after = os.fstat(stream.fileno())
        if (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns, before.st_mode) != (
                after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns, after.st_mode):
            raise Refusal(f"selected input changed during capture: {path}")


def regular_bytes(path):
    with regular_file(path) as (stream, info):
        return stream.read(), bool(info.st_mode & 0o111)


def file_digest(path):
    with regular_file(path) as (stream, info):
        digest = hashlib.sha256()
        for chunk in iter(lambda: stream.read(65536), b""):
            digest.update(chunk)
        return digest.digest(), info.st_size, bool(info.st_mode & 0o111)


def validate_paths(selected, *, field="selected_files", selected_inputs=True):
    if not isinstance(selected, list) or not 1 <= len(selected) <= 1000:
        raise Refusal(f"{field}: declare between one and 1000 paths")
    components = []
    for name in selected:
        if not isinstance(name, str):
            raise Refusal(f"{field}: path must be a string: {name!r}")
        try:
            encoded = name.encode("utf-8")
        except UnicodeEncodeError:
            raise Refusal(f"{field}: path must be valid UTF-8: {name!r}") from None
        if (not name or (selected_inputs and len(encoded) > 512)
                or name.startswith("/") or "\\" in name or any(ord(c) < 32 or ord(c) == 127 for c in name)
                or any(part in ("", ".", "..") for part in name.split("/"))):
            raise Refusal(f"{field}: requires a normalized relative POSIX path: {name!r}")
        sensitive = {".ssh", ".aws", ".gnupg", ".kube", ".codex", ".azure", ".docker", "gcloud",
                     ".npmrc", ".pypirc", ".netrc", ".env", "credentials", "credentials.json",
                     "application_default_credentials.json", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"}
        if selected_inputs and any(part in sensitive or part.startswith(".env.") or part.endswith((".pem", ".key", ".p12", ".pfx"))
               for part in name.lower().split("/")):
            raise Refusal(f"{field}: known credential path names cannot be selected: {name!r}")
        parts = tuple(unicodedata.normalize("NFC", part).casefold() for part in name.split("/"))
        if any(parts[:len(other)] == other or other[:len(parts)] == parts for other in components):
            raise Refusal(f"{field}: repeats, aliases by case/Unicode normalization or overlaps a parent: {name!r}")
        # A component whose spelling differs only by case is also ambiguous.
        for other_name in selected[:len(components)]:
            for left, right in zip(name.split("/"), other_name.split("/")):
                if unicodedata.normalize("NFC", left).casefold() != unicodedata.normalize("NFC", right).casefold():
                    break
                if left != right:
                    raise Refusal(f"{field}: components alias by case/Unicode normalization: {name!r}, {other_name!r}")
        components.append(parts)
    return tuple(selected)


def tree_digest(root, selected):
    selected = validate_paths(list(selected))
    digest = hashlib.sha256(b"haepari-tree-sha256/v1\0")
    for name in sorted(selected, key=lambda value: value.encode("utf-8")):
        content, size, executable = file_digest(Path(root) / name)
        encoded = name.encode("utf-8")
        digest.update(struct.pack(">Q", len(encoded)) + encoded + bytes([executable]))
        digest.update(struct.pack(">Q", size) + content)
    return digest.hexdigest()


def capture_inputs(source, bundle, policy):
    if not isinstance(policy, dict):
        raise Refusal("project inputs must declare selected files, candidate/test groups and a profile reference")
    selected = validate_paths(policy.get("selected_files"))
    groups = {}
    for key in ("candidate_files", "test_files"):
        groups[key] = validate_paths(policy.get(key), field=key)
        if not set(groups[key]).issubset(selected):
            raise Refusal(f"{key} must use selected files; undeclared: {sorted(set(groups[key]) - set(selected))!r}")
    profile = policy.get("profile_file")
    if profile not in selected:
        raise Refusal(f"profile_file must be selected: {profile!r}")
    if not valid_identifier(policy.get("profile_id")):
        raise Refusal("profile_id requires 1..128 ASCII identifier characters (letters, digits, dot, underscore or hyphen)")
    bundle.mkdir(mode=0o700)
    for name in selected:
        target = bundle / name
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with regular_file(Path(source) / name) as (stream, info):
            with target.open("xb") as output:
                for chunk in iter(lambda: stream.read(65536), b""):
                    output.write(chunk)
                output.flush()
                os.fsync(output.fileno())
            target.chmod(0o700 if info.st_mode & 0o111 else 0o600)
    profile_digest = file_digest(bundle / profile)[0].hex()
    return {"input": {"root": str(bundle), "selected_files": list(selected), "inventory_digest": tree_digest(bundle, selected)},
            "environment": {"profile_id": policy["profile_id"], "profile_digest": profile_digest},
            "provenance": {"candidate_digest": tree_digest(bundle, groups["candidate_files"]),
                           "tests_digest": tree_digest(bundle, groups["test_files"]),
                           "target_profile_digest": profile_digest, "evidence_class": "development"}}


def valid_identifier(value):
    return isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", value) is not None


def validate_recipe(recipe, mode):
    if not isinstance(recipe, dict):
        raise Refusal("project suite recipe must be a mapping")
    argv = recipe.get("argv")
    outputs = recipe.get("required_outputs")
    environment = recipe.get("environment", {})
    if (not isinstance(argv, list) or not 1 <= len(argv) <= 128
            or not all(isinstance(arg, str) and arg and "\0" not in arg for arg in argv)
            or not isinstance(outputs, list) or not 1 <= len(outputs) <= 1000
            or not isinstance(environment, dict)
            or not all(isinstance(key, str) and re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key)
                       and isinstance(value, str) and "\0" not in value for key, value in environment.items())):
        raise Refusal("project suite recipe requires 1..128 argv, 1..1000 required_outputs and valid string environment mappings")
    validate_paths(outputs, field="required_outputs", selected_inputs=False)
    validate_paths([recipe.get("cwd")], field="cwd", selected_inputs=False)
    reference = recipe.get("authorization_reference")
    if not isinstance(reference, str) or not reference:
        raise Refusal("project suite recipe requires an explicit offline authorization_reference")
    if mode == "manual":
        manual = recipe.get("manual")
        if not isinstance(manual, dict):
            raise Refusal("manual recipe requires explicit scenario_document and required_scenarios")
        validate_paths([manual.get("scenario_document")], field="scenario_document", selected_inputs=False)
        scenarios = manual.get("required_scenarios")
        if (not isinstance(scenarios, list) or not scenarios or not all(valid_identifier(value) for value in scenarios)
                or len(set(scenarios)) != len(scenarios)):
            raise Refusal("manual recipe requires unique identifier-shaped required_scenarios")


def validate_manual_selection(recipe, mode, selected_files):
    if mode == "manual" and (not isinstance(selected_files, list)
                             or recipe["manual"]["scenario_document"] not in selected_files):
        raise Refusal("manual.scenario_document must be included in input.selected_files")


def job_spec(inputs, resource, suite, mode, recipe):
    if not valid_identifier(resource) or not valid_identifier(suite) or mode not in ("auto", "manual"):
        raise Refusal("Job requires identifier-shaped resource/suite and auto or manual mode")
    validate_recipe(recipe, mode)
    validate_manual_selection(recipe, mode, inputs["input"]["selected_files"])
    cwd = recipe["cwd"]
    if not any(name.startswith(cwd + "/") for name in inputs["input"]["selected_files"]):
        raise Refusal(f"recipe cwd must name a directory containing selected files: {cwd!r}")
    spec = {"schema": "haepari.job/v2", "execution_mode": "managed", "resource_id": resource,
            "mode": mode, "suite": suite, **inputs,
            "workload": {"argv": recipe["argv"], "cwd": cwd, "environment": recipe.get("environment", {}),
                         "required_outputs": recipe["required_outputs"]},
            "deadlines": {"queue_seconds": 30, "prepare_seconds": 10, "collect_seconds": 5, "cleanup_seconds": 5},
            "effects": {"mode": "offline", "authorization_reference": recipe["authorization_reference"]}}
    if mode == "manual":
        spec["manual"] = recipe["manual"]
        spec["deadlines"]["manual_seconds"] = 60
    else:
        spec["deadlines"]["run_seconds"] = 10
    return spec
