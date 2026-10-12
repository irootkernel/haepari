"""Run an explicitly authorized, restartable single-VM public-CLI campaign.

This driver collects observations; Task acceptance belongs to the roadmap and
an assessment of the retained evidence. It never installs a service or creates
a resource, and never retries a control command with an uncertain return.
"""

import argparse
import base64
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import plistlib
import re
import signal
import socket
import stat
import struct
import subprocess
import time
import uuid


SCHEMA = "haepari.utm-campaign/v1"
ROWS = (
    "success_reuse", "missing_output", "uncollected_output",
    "native_failure", "cancel", "timeout", "ssh_interruption",
    "controller_restart", "large_log",
    "soak",
)
ACTIONS = {
    "submit", "wait", "inspect", "cancel", "events", "logs", "export",
    "purge", "resource", "control", "check",
    "replay", "recover_plan", "recover_apply",
}
KEY = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}\Z")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
LOADED_DRIVER_DIGEST = "sha256:" + hashlib.sha256(Path(__file__).read_bytes()).hexdigest()


class CampaignError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise CampaignError(message)


def digest(raw):
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f"duplicate JSON key: {key}")
        result[key] = value
    return result


def decode(raw):
    return json.loads(raw, object_pairs_hook=unique_object,
                      parse_constant=lambda value: require(False, f"invalid number: {value}"))


def owned(path, directory=False):
    path = Path(path)
    require(path.is_absolute() and str(path) == os.path.normpath(path), "absolute clean path required")
    # Reject links at every component, including symlinked parent directories.
    for parent in [path, *path.parents]:
        info = parent.lstat()
        require(not stat.S_ISLNK(info.st_mode), f"symlink refused: {parent}")
    info = path.lstat()
    require(info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) & 0o077 == 0,
            f"private owned path required: {path}")
    require(stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode),
            f"wrong path kind: {path}")
    return path


def read_private(path, maximum=4 << 20):
    path = owned(path)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as source:
        before = os.fstat(source.fileno())
        require(before.st_uid == os.geteuid() and before.st_mode & 0o077 == 0,
                "file changed ownership or mode")
        raw = source.read(maximum + 1)
        after = os.fstat(source.fileno())
    require(len(raw) <= maximum and (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns)
            == (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns), "unstable or oversized file")
    require((after.st_dev, after.st_ino) == (path.lstat().st_dev, path.lstat().st_ino), "replaced file")
    return raw


def file_digest(path):
    path = owned(path)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as source:
        before = os.fstat(source.fileno())
        require(before.st_uid == os.geteuid() and before.st_mode & 0o077 == 0,
                "executable changed ownership or mode")
        h = hashlib.sha256()
        for block in iter(lambda: source.read(1 << 20), b""):
            h.update(block)
        after = os.fstat(source.fileno())
    require((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns)
            == (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns), "unstable executable")
    current = path.lstat()
    require((after.st_dev, after.st_ino) == (current.st_dev, current.st_ino)
            and not stat.S_ISLNK(current.st_mode), "replaced executable")
    return "sha256:" + h.hexdigest()


def fields(value, required, optional=()):
    require(isinstance(value, dict) and set(required) <= set(value)
            and set(value) <= set(required) | set(optional), "missing or unknown fields")


def positive(value, maximum=86400):
    require(type(value) is int and 0 < value <= maximum, "positive finite integer budget required")


def save(path, value):
    """Publish a durable checkpoint before issuing the effect it describes."""
    save_bytes(path, (json.dumps(value, sort_keys=True, indent=2, allow_nan=False) + "\n").encode())


def save_bytes(path, raw):
    path = Path(path)
    temporary = path.with_name(path.name + "." + uuid.uuid4().hex + ".tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as output:
        output.write(raw)
        output.flush()
        os.fsync(output.fileno())
    os.replace(temporary, path)
    fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def envelope(raw, operation):
    value = decode(raw)
    require(value.get("schema") == "haepari.envelope/v1"
            and value.get("operation") == operation, "wrong public envelope binding")
    return value


def check_values(value, checks):
    validate_checks(checks)
    for check in checks:
        current = value
        try:
            for part in check["path"]:
                current = current[part]
        except (KeyError, IndexError, TypeError):
            raise CampaignError(f"missing observation: {check['path']}") from None
        require(type(current) is type(check["equals"]) and current == check["equals"],
                f"observation mismatch: {check['path']}")


def validate_checks(checks):
    require(isinstance(checks, list) and checks, "observation checks required")
    for check in checks:
        fields(check, ("path", "equals"))
        require(isinstance(check["path"], list) and check["path"], "nonempty JSON path required")
        require(all((isinstance(part, str) and part) or (type(part) is int and part >= 0)
                    for part in check["path"]), "JSON path needs keys or nonnegative indices")


def socket_peer_pid(path, expected_pid, timeout):
    # Darwin sys/un.h: SOL_LOCAL=0, LOCAL_PEERPID=0x002. The controller
    # binds a private temporary name then renames it to control.sock; lsof
    # retains that original kernel name, so match the connected peer instead.
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
        connection.settimeout(timeout)
        connection.connect(str(path))
        peer = connection.getsockopt(0, 0x002)
    require(peer == expected_pid, "control socket belongs to another process")
    return peer


def verify_version(value, manifest):
    require(value.get("source_revision") == manifest["source_revision"]
            and value.get("source_modified") is manifest.get("source_modified", False)
            and value.get("executable_sha256") == manifest["binary_sha256"]
            and value.get("platform") == "darwin/arm64",
            "candidate public version differs from selected source/executable")


def capture_candidate(repository, binary_sha256):
    """Bind HEAD, the complete tracked diff and every nonignored untracked file."""
    repository = Path(repository)
    require(repository.is_absolute() and repository.resolve() == repository,
            "canonical candidate repository required")
    require(DIGEST.fullmatch(binary_sha256), "exact candidate executable digest required")
    env = dict(os.environ, GIT_OPTIONAL_LOCKS="0")

    def git(*args):
        return subprocess.check_output(
            ["git", "-C", str(repository), "-c", "core.fsmonitor=false",
             "-c", "color.ui=false", *args], env=env, timeout=10)

    revision = git("rev-parse", "HEAD").decode().strip()
    patch_args = ("diff", "--binary", "--no-ext-diff", "--no-textconv",
                  "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", "HEAD", "--")
    patch = git(*patch_args)
    changed = git("diff", "--name-only", "-z", "--no-renames", "HEAD", "--")
    untracked = git("ls-files", "--others", "--exclude-standard", "-z")
    paths = sorted(set(p.decode() for p in (changed + untracked).split(b"\0") if p))
    inventory = []
    for relative in paths:
        path = repository / relative
        require(not Path(relative).is_absolute() and ".." not in Path(relative).parts
                and relative == os.path.normpath(relative), "unsafe candidate path")
        if not path.exists() and not path.is_symlink():
            # The tracked diff binds the deletion; a missing untracked file is
            # caught by the final inventory observation below.
            inventory.append({"path": relative, "sha256": None, "executable": False})
            continue
        require(path.resolve() == path, "candidate symlink refused")
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as source:
            before = os.fstat(source.fileno())
            require(stat.S_ISREG(before.st_mode), "regular candidate file required")
            h = hashlib.sha256()
            for block in iter(lambda: source.read(1 << 20), b""):
                h.update(block)
            after = os.fstat(source.fileno())
        require((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_mode)
                == (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_mode)
                and (after.st_dev, after.st_ino) == (path.lstat().st_dev, path.lstat().st_ino),
                "candidate file changed during observation")
        inventory.append({"path": relative, "sha256": h.hexdigest(),
                          "executable": bool(after.st_mode & 0o111)})
    require(git("rev-parse", "HEAD").decode().strip() == revision
            and git(*patch_args) == patch
            and git("diff", "--name-only", "-z", "--no-renames", "HEAD", "--") == changed
            and git("ls-files", "--others", "--exclude-standard", "-z") == untracked,
            "candidate changed during observation")
    return {"schema": "haepari.candidate-inventory/v1", "head": revision,
            "binary_sha256": binary_sha256, "tracked_patch_sha256": digest(patch),
            "paths": inventory}


def verify_candidate(manifest):
    if not manifest.get("source_modified", False):
        return
    raw = read_private(manifest["candidate_inventory_file"])
    require(digest(raw) == manifest["candidate_inventory_sha256"], "candidate inventory changed")
    selected = decode(raw)
    require(isinstance(selected, dict), "candidate inventory object required")
    require(selected.get("head") == manifest["source_revision"]
            and selected.get("binary_sha256") == manifest["binary_sha256"],
            "candidate inventory source/executable mismatch")
    current = capture_candidate(manifest["repository"], manifest["binary_sha256"])
    require(digest(json.dumps(selected, sort_keys=True).encode())
            == digest(json.dumps(current, sort_keys=True).encode()),
            "selected candidate source bytes changed")


def input_digest(spec):
    """Independently verify the selected inventory, including executable bits."""
    root = owned(spec["input"]["root"], directory=True)
    selected = spec["input"]["selected_files"]
    require(isinstance(selected, list) and selected and len(selected) <= 1000
            and len(set(selected)) == len(selected), "explicit unique selected inputs required")
    h = hashlib.sha256(b"haepari-tree-sha256/v1\0")
    for relative in sorted(selected):
        require(isinstance(relative, str) and relative and relative == os.path.normpath(relative)
                and not Path(relative).is_absolute() and ".." not in Path(relative).parts,
                "unsafe selected path")
        path = owned(root / relative)
        raw = read_private(path, 256 << 20)
        name = relative.encode()
        h.update(struct.pack(">Q", len(name)) + name)
        h.update(bytes([bool(path.stat().st_mode & 0o111)]))
        h.update(struct.pack(">Q", len(raw)) + hashlib.sha256(raw).digest())
    require(h.hexdigest() == spec["input"]["inventory_digest"], "selected input inventory changed")
    return h.hexdigest()


def validate(manifest):
    fields(manifest, ("schema", "campaign_id", "repository", "source_revision", "binary",
                     "binary_sha256", "driver_sha256", "plan_file", "plan_sha256", "resource",
                     "aggregate_seconds", "command_seconds", "controls", "rows"),
           ("source_modified", "candidate_inventory_file", "candidate_inventory_sha256"))
    require(manifest["schema"] == SCHEMA and KEY.fullmatch(manifest["campaign_id"]), "invalid campaign identity")
    require(re.fullmatch(r"[0-9a-f]{40}", manifest["source_revision"]), "exact source commit required")
    for key in ("binary_sha256", "driver_sha256", "plan_sha256"):
        require(DIGEST.fullmatch(manifest[key]), "exact digest required")
    require(type(manifest.get("source_modified", False)) is bool, "source_modified must be boolean")
    candidate_fields = {"candidate_inventory_file", "candidate_inventory_sha256"}
    require((candidate_fields <= set(manifest)) if manifest.get("source_modified", False)
            else not (candidate_fields & set(manifest)),
            "modified source requires exact candidate inventory; clean source uses HEAD")
    if manifest.get("source_modified", False):
        require(isinstance(manifest["candidate_inventory_file"], str)
                and Path(manifest["candidate_inventory_file"]).is_absolute()
                and DIGEST.fullmatch(manifest["candidate_inventory_sha256"]),
                "exact private candidate inventory path/digest required")
    positive(manifest["aggregate_seconds"])
    positive(manifest["command_seconds"], 600)
    resource = manifest["resource"]
    fields(resource, ("id", "configuration_digest", "config_revision", "uuid", "mac",
                      "connection_reference", "known_hosts_reference", "registration_file"))
    require(KEY.fullmatch(resource["id"]) and re.fullmatch(r"[0-9a-f]{64}", resource["configuration_digest"]), "invalid resource binding")
    positive(resource["config_revision"], 2**63 - 1)
    require(re.fullmatch(r"[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}", resource["uuid"]), "invalid VM UUID")
    require(re.fullmatch(r"(?:[0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}", resource["mac"]), "invalid MAC")
    require(isinstance(manifest["controls"], dict), "controls must be explicit")
    for name, control in manifest["controls"].items():
        require(KEY.fullmatch(name), "invalid control name")
        fields(control, ("argv", "executable_sha256", "seconds", "purpose", "restoration"))
        require(isinstance(control["argv"], list) and control["argv"]
                and all(isinstance(a, str) and a and "\0" not in a for a in control["argv"]), "invalid control argv")
        require(Path(control["argv"][0]).is_absolute() and DIGEST.fullmatch(control["executable_sha256"]), "pinned control executable required")
        positive(control["seconds"], 600)
        require(isinstance(control["purpose"], str) and control["purpose"]
                and isinstance(control["restoration"], str) and control["restoration"], "control and restoration disclosure required")
    require(isinstance(manifest["rows"], dict) and set(manifest["rows"]) == set(ROWS), "complete single-VM matrix required")
    seen, keys = {}, set()
    for row in ROWS:
        steps = manifest["rows"][row]
        require(isinstance(steps, list) and 1 <= len(steps) <= 500, "bounded nonempty row required")
        submissions = 0
        for step in steps:
            fields(step, ("id", "action", "seconds"), ("spec_file", "spec_sha256", "request_key",
                   "job", "control", "checks", "record", "size", "sha256", "expected_exit", "until",
                   "expected_error", "kind", "plan_action", "plan", "evidence_file", "evidence_sha256"))
            require(KEY.fullmatch(step["id"]) and step["id"] not in seen, "unique step ID required")
            require(step["action"] in ACTIONS, "unsupported action")
            positive(step["seconds"], 3600)
            action = step["action"]
            allowed = {
                "submit": {"spec_file", "spec_sha256", "request_key"}, "replay": {"job"},
                "wait": {"job", "expected_exit", "expected_error"},
                "inspect": {"job", "until"}, "resource": {"until"},
                "cancel": {"job", "expected_exit", "expected_error"},
                "events": {"job"}, "logs": {"job", "size", "sha256"},
                "export": {"job"}, "purge": {"job", "expected_exit", "expected_error"},
                "control": {"control", "expected_exit"}, "check": {"record", "checks"},
                "recover_plan": {"kind", "job", "plan_action"},
                "recover_apply": {"plan", "request_key", "evidence_file", "evidence_sha256", "expected_exit", "expected_error"},
            }
            require(set(step) <= {"id", "action", "seconds"} | allowed[action], "fields do not belong to action")
            if action == "submit":
                require(set(("spec_file", "spec_sha256", "request_key")) <= set(step), "saved spec and key required")
                require(DIGEST.fullmatch(step["spec_sha256"]) and KEY.fullmatch(step["request_key"])
                        and step["request_key"] not in keys, "unique submission key required")
                keys.add(step["request_key"])
                submissions += 1
            elif action in {"wait", "inspect", "cancel", "events", "logs", "export", "purge", "replay"}:
                require(step.get("job") in seen and seen[step["job"]] == "submit", "previous submission reference required")
            elif action == "recover_plan":
                require(step.get("kind") in {"job", "resource"}, "recovery target kind required")
                if step["kind"] == "job":
                    require(seen.get(step.get("job")) == "submit", "recovery Job binding required")
                actions = {"resource": {"health", "recollect", "attest-guest-stopped"},
                           "job": {"reconcile", "attest-stopped", "attest-no-start"}}
                require(step.get("plan_action") in actions[step["kind"]], "unapproved recovery action")
            elif action == "recover_apply":
                require(seen.get(step.get("plan")) == "recover_plan" and KEY.fullmatch(step.get("request_key", "")), "saved recovery plan/key required")
                require(step["request_key"] not in keys and len(step["request_key"]) <= 90, "unique bounded recovery key required")
                keys.add(step["request_key"])
                if "evidence_file" in step:
                    require(DIGEST.fullmatch(step.get("evidence_sha256", "")), "recovery evidence digest required")
            elif action == "control":
                require(step.get("control") in manifest["controls"], "unbound control refused")
            elif action == "check":
                require(step.get("record") in seen and step.get("checks"), "previous observation and checks required")
                validate_checks(step["checks"])
            if action == "logs":
                require(type(step.get("size")) is int and 0 <= step["size"] <= 256 << 20
                        and DIGEST.fullmatch(step.get("sha256", "")), "full expected log size/hash required")
            require(type(step.get("expected_exit", 0)) is int and 0 <= step.get("expected_exit", 0) <= 255, "invalid expected exit")
            require("until" not in step or action in {"inspect", "resource"}, "only read-only observations may poll")
            if "until" in step:
                validate_checks(step["until"])
            seen[step["id"]] = action
        require(any(s["action"] == "check" for s in steps), "each row needs explicit observed assertions")
        if row == "soak":
            require(submissions == 30, "soak must own exactly 30 distinct submissions")
        if row == "large_log":
            require(any(s["action"] == "logs" and s["size"] == 50 * 1024 * 1024 for s in steps), "50 MiB log check required")
    return manifest


class Campaign:
    def __init__(self, manifest_path, authority_path, evidence_root):
        self.manifest_raw = read_private(manifest_path)
        self.manifest = validate(decode(self.manifest_raw))
        self.authority_raw = read_private(authority_path)
        authority = decode(self.authority_raw)
        fields(authority, ("schema", "campaign_sha256", "approval_reference", "expires_at", "effects", "restoration"))
        require(authority["schema"] == "haepari.utm-campaign-authority/v1"
                and authority["campaign_sha256"] == digest(self.manifest_raw), "authority binds different campaign bytes")
        require(isinstance(authority["approval_reference"], str) and authority["approval_reference"], "actual approval reference required")
        require(type(authority["expires_at"]) in (int, float) and math.isfinite(authority["expires_at"])
                and authority["expires_at"] > 0, "finite authority expiry required")
        require(isinstance(authority["effects"], list) and set(authority["effects"])
                == {"submit", "cancel", "purge", "export", "recover_apply", *self.manifest["controls"]}, "exact effect inventory required")
        require(isinstance(authority["restoration"], str) and authority["restoration"], "restoration plan required")
        self.authority = authority
        self.root = owned(evidence_root, directory=True)
        self.lock = os.open(self.root / "campaign.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            os.close(self.lock)
            raise CampaignError("another campaign observer owns this evidence directory") from None
        self.state_path = self.root / "checkpoint.json"
        self.state = {"schema": "haepari.utm-campaign-checkpoint/v1", "campaign_sha256": digest(self.manifest_raw),
                      "authority_sha256": digest(self.authority_raw), "started_at": None, "steps": {}, "attempts": 0}
        try:
            if self.state_path.exists():
                self.state = decode(read_private(self.state_path, 32 << 20))
                require(self.state["campaign_sha256"] == digest(self.manifest_raw)
                        and self.state["authority_sha256"] == digest(self.authority_raw), "resume input changed")
        except Exception:
            self.close()
            raise
        self.wall_anchor, self.monotonic_anchor = time.time(), time.monotonic()
        if self.wall_anchor < self.state.get("last_clock_at", 0):
            self.close()
            raise CampaignError("wall clock moved backward; inspect original deadlines")

    def close(self):
        os.close(self.lock)

    def checkpoint(self):
        # Large observations live in private digest-bound result artifacts;
        # the bounded checkpoint retains original control/key/deadline state.
        for step_id, entry in self.state["steps"].items():
            if "result" not in entry:
                continue
            raw = (json.dumps(entry["result"], sort_keys=True, allow_nan=False) + "\n").encode()
            require(len(raw) <= 32 << 20, "step result exceeds retained artifact bound")
            name = step_id + ".result.json"
            path = self.root / name
            if path.exists():
                require(read_private(path, 32 << 20) == raw,
                        "existing result differs; retain original evidence")
            else:
                save_bytes(path, raw)
            entry["result_ref"] = {"file": name, "sha256": digest(raw)}
            del entry["result"]
        self.state["last_clock_at"] = self.now()
        require(len(json.dumps(self.state, allow_nan=False).encode()) <= 16 << 20,
                "checkpoint exceeds retained bound; keep original state")
        save(self.state_path, self.state)

    def result(self, entry):
        if "result" in entry:
            return entry["result"]
        reference = entry["result_ref"]
        name = reference["file"]
        require(isinstance(name, str) and Path(name).name == name and name.endswith(".result.json"),
                "invalid result artifact reference")
        raw = read_private(self.root / name, 32 << 20)
        require(digest(raw) == reference["sha256"], "retained result artifact changed")
        return decode(raw)

    def now(self):
        return max(time.time(), self.wall_anchor + time.monotonic() - self.monotonic_anchor)

    def remaining(self, expires=None):
        now = self.now()
        limits = [self.authority["expires_at"] - now]
        if self.state["started_at"] is not None:
            limits.append(self.state["started_at"] + self.manifest["aggregate_seconds"] - now)
        if expires is not None:
            limits.append(expires - now)
        left = min(limits)
        require(left > 0, "original authority/campaign/step budget exhausted; retain evidence and occupancy")
        return left

    def call(self, args, operation, expires=None, control=None):
        m = self.manifest
        require(LOADED_DRIVER_DIGEST == digest(Path(__file__).read_bytes()) == m["driver_sha256"], "approved campaign driver changed")
        if control is not None or operation in {"submit", "cancel", "purge", "export",
                                               "resource.recover", "job.recover"}:
            verify_candidate(m)
        if control is None:
            require(file_digest(m["binary"]) == m["binary_sha256"], "candidate executable changed")
            argv = [m["binary"], *args]
            timeout = m["command_seconds"]
        else:
            require(file_digest(control["argv"][0]) == control["executable_sha256"], "control executable changed")
            argv, timeout = control["argv"], control["seconds"]
        timeout = min(timeout, self.remaining(expires))
        self.state["attempts"] += 1
        number = self.state["attempts"]
        prefix = self.root / f"attempt-{number:06d}"
        record = {"argv": argv, "operation": operation, "started_at": time.time(),
                  "timeout_seconds": timeout, "return": "uncertain"}
        save(prefix.with_suffix(".json"), record)
        self.checkpoint()
        env = {"HOME": str(Path.home()), "PATH": "/usr/bin:/bin", "LANG": "C",
               "HAEPARI_HOME": self.plan["root"]}
        started = time.monotonic()
        with prefix.with_suffix(".stdout").open("xb") as out, prefix.with_suffix(".stderr").open("xb") as err:
            out_path, err_path = prefix.with_suffix(".stdout"), prefix.with_suffix(".stderr")
            out_path.chmod(0o600)
            err_path.chmod(0o600)
            # No durable I/O occurs between this last budget check and issuance.
            timeout = min(timeout, self.remaining(expires))
            record["timeout_seconds"] = timeout
            record["issued_at"] = self.now()
            process = subprocess.Popen(argv, env=env, stdout=out, stderr=err, start_new_session=True)
            try:
                process.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                # This terminates our CLI waiter only; it never cancels its Job.
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
                record["elapsed_seconds"] = time.monotonic() - started
                save(prefix.with_suffix(".json"), record)
                raise CampaignError("command return uncertain; resume only exact safe requests") from None
        record.update({"return": "observed", "exit_code": process.returncode,
                       "elapsed_seconds": time.monotonic() - started})
        save(prefix.with_suffix(".json"), record)
        if control is None:
            raw = read_private(prefix.with_suffix(".stdout"), 4 << 20)
            record["envelope"] = envelope(raw, operation)
        save(prefix.with_suffix(".json"), record)
        return record

    def cli(self, args, operation, expires=None, expected_exit=0, expected_error=None):
        record = self.call(args, operation, expires)
        require(record["exit_code"] == expected_exit, f"unexpected CLI exit: {operation}; see retained attempt")
        if expected_error is not None:
            require(record["envelope"].get("ok") is False
                    and record["envelope"].get("error", {}).get("code") == expected_error, "expected public refusal was not observed")
            return {"error": record["envelope"]["error"], "exit_code": record["exit_code"]}
        require(record["envelope"].get("ok") is True, f"public operation failed: {operation}")
        return record["envelope"]["data"]

    def preflight(self):
        m = self.manifest
        require(platform.system() == "Darwin" and platform.machine() == "arm64", "macOS arm64 host required; no host fallback")
        repository = Path(m["repository"])
        require(repository.is_absolute() and repository.resolve() == repository, "canonical repository required")
        revision = subprocess.check_output(["git", "-C", str(repository), "-c", "core.fsmonitor=false",
                                           "rev-parse", "HEAD"], text=True, timeout=10).strip()
        require(revision == m["source_revision"], "source HEAD changed")
        verify_candidate(m)
        require(file_digest(m["binary"]) == m["binary_sha256"], "selected executable digest mismatch")
        raw = read_private(m["plan_file"])
        require(digest(raw) == m["plan_sha256"], "selected service plan changed")
        self.plan = decode(raw)
        p = self.plan
        require(p["schema"] == "haepari.development-service-plan/v2" and p["global_limit"] == 1
                and p["host_macos_guest_limit"] == 1, "single-VM v2 plan required")
        require(p["binary"] == m["binary"] and p["binary_sha256"] == m["binary_sha256"]
                and p["owner_uid"] == os.geteuid() and p["domain"] == f"gui/{os.geteuid()}", "wrong installed service binding")
        require(p["label"] == "io.rootkernel.haepari.dev." + p["instance"], "wrong service label")
        require(Path(p["plist_path"]) == Path.home() / "Library/LaunchAgents" / (p["label"] + ".plist"), "wrong plist path")
        owned(p["root"], directory=True)
        require(read_private(p["plist_path"]).decode() == p["plist"], "installed plist differs")
        plist = plistlib.loads(p["plist"].encode())
        require(plist["Label"] == p["label"] and plist["EnvironmentVariables"]["HAEPARI_HOME"] == p["root"]
                and plist["ProgramArguments"] == [m["binary"], "daemon", "run", "--backend", "local",
                     "--global-limit", "1", "--host-macos-guest-limit", "1"], "installed argv/root differs")
        owner = decode(read_private(Path(p["root"]) / ".development-service.json"))
        require(owner == {"schema": "haepari.development-service-owner/v1", "instance": p["instance"],
                          "owner_uid": p["owner_uid"], "root": p["root"]}, "wrong service root owner")
        version = self.cli(["version", "--json"], "version")
        verify_version(version, m)
        self.runtime_binding = self.observe_service()
        r = m["resource"]
        registration_raw = read_private(r["registration_file"], 1 << 20)
        require(hashlib.sha256(registration_raw).hexdigest() == r["configuration_digest"], "registration bytes do not match public configuration digest")
        registration = decode(registration_raw)
        require(registration["id"] == r["id"] and registration["kind"] == "utm-macos"
                and registration["vm"]["utm_vm_uuid"] == r["uuid"]
                and registration["vm"]["mac_address"] == r["mac"]
                and registration["guest"]["connection_reference"] == r["connection_reference"]
                and registration["guest"]["known_hosts_reference"] == r["known_hosts_reference"], "UUID/MAC/pin mapping differs from registration")
        for key in ("connection_reference", "known_hosts_reference"):
            reference = r[key]
            require(isinstance(reference, str) and len(reference) > 72 and reference[71] == ":"
                    and DIGEST.fullmatch(reference[:71]), "digest-bound guest mapping/pin required")
            require(digest(read_private(reference[72:])) == reference[:71], "guest mapping/pin changed")
        capabilities = self.cli(["capabilities", "--json"], "capabilities")
        require("managed-execution/v1" in capabilities["capabilities"], "public controller unavailable")
        resource = self.cli(["resource", "inspect", r["id"], "--json"], "resource.inspect")
        require(resource["kind"] == "utm-macos" and resource["capacity"] == 1
                and resource["configuration_digest"] == r["configuration_digest"]
                and resource["config_revision"] == r["config_revision"], "registered resource changed")
        allowed_jobs = {self.result(s)["job_id"] for s in self.state["steps"].values()
                        if s.get("action") == "submit" and s.get("status") == "complete"}
        pending = {s["request_key"]: s for row in ROWS for s in m["rows"][row]
                   if s["action"] == "submit" and self.state["steps"].get(s["id"], {}).get("submit_issued")}
        known_active = set()
        cursor = ""
        cursors = set()
        while True:
            page = self.cli(["jobs", "list", "--limit", "100",
                             "--cursor", cursor, "--json"], "jobs.list")
            for job in page.get("jobs", []):
                receipt = job["receipt"]
                saved = pending.get(receipt.get("admission", {}).get("request_key"))
                if saved and receipt["resource_id"] == r["id"]:
                    entry = self.state["steps"][saved["id"]]
                    require(entry.get("pending_job_id", receipt["job_id"]) == receipt["job_id"], "pending submission changed Job")
                    entry["pending_job_id"] = receipt["job_id"]
                    allowed_jobs.add(receipt["job_id"])
                require(receipt["job_state"] == "finished" or receipt["job_id"] in allowed_jobs,
                        "unrelated unfinished Job blocks campaign")
                if receipt["job_state"] != "finished":
                    known_active.add(receipt["job_id"])
            if not page["more"]:
                break
            cursor = page["next_cursor"]
            require(cursor and cursor not in cursors, "job pagination made no progress")
            cursors.add(cursor)
        require(resource["occupancy"] == 0 or known_active, "unproven occupancy blocks campaign")
        self.freeze_submissions()
        self.checkpoint()
        return {"resource": resource, "candidate_sha256": m["binary_sha256"],
                "service_plan_sha256": m["plan_sha256"], "ready": True,
                "runtime_binding": self.runtime_binding, "qualification": "unverified", "live_effects": "none"}

    def freeze_submissions(self):
        # Freeze exact spec bytes before effects. Issued admissions replay their
        # sealed controller input rather than depending on the original files.
        m = self.manifest
        r = m["resource"]
        for row in ROWS:
            for step in m["rows"][row]:
                if step["action"] != "submit":
                    continue
                path = self.root / (step["id"] + ".spec.json")
                entry = self.state["steps"].get(step["id"], {})
                issued = entry.get("submit_issued", False)
                require(not issued or path.exists(), "issued submission lost frozen spec")
                raw = read_private(path if path.exists() else step["spec_file"], 1 << 20)
                require(digest(raw) == step["spec_sha256"], "job spec changed")
                spec = decode(raw)
                require(spec["schema"] == "haepari.job/v2" and spec["execution_mode"] == "managed"
                        and spec["resource_id"] == r["id"] and spec["mode"] == "auto"
                        and spec["effects"]["mode"] == "offline", "single-VM offline managed spec required")
                for phase in ("queue_seconds", "prepare_seconds", "run_seconds", "collect_seconds", "cleanup_seconds"):
                    positive(spec["deadlines"][phase])
                if not issued:
                    input_digest(spec)
                if not path.exists():
                    save_bytes(path, raw)
                saved = {"spec": spec, "spec_sha256": step["spec_sha256"], "request_key": step["request_key"]}
                path = self.root / (step["id"] + ".submission.json")
                if path.exists():
                    require(decode(read_private(path)) == saved, "saved submission changed")
                else:
                    save(path, saved)

    def observe_service(self):
        """Bind the running LaunchAgent's text vnode and Unix listening socket."""
        target = self.plan["domain"] + "/" + self.plan["label"]
        observation_id = uuid.uuid4().hex

        def read(argv, name):
            result = subprocess.run(argv, capture_output=True, timeout=min(10, self.remaining()), check=False,
                                    env={"PATH": "/usr/bin:/bin", "LANG": "C"})
            save(self.root / ("service-" + observation_id + "-" + name + ".json"),
                 {"argv": argv, "exit_code": result.returncode, "stdout": result.stdout.decode(errors="replace"),
                  "stderr": result.stderr.decode(errors="replace"), "observed_at": self.now()})
            require(result.returncode == 0, "running service identity observation unavailable")
            return result.stdout.decode()

        def pid(output):
            match = re.search(r"^\s*pid = ([1-9][0-9]*)\s*$", output, re.MULTILINE)
            require(match is not None, "service has no observed running PID")
            return int(match.group(1))

        process = pid(read(["/bin/launchctl", "print", target], "before"))
        info = Path(self.manifest["binary"]).stat()
        text = read(["/usr/sbin/lsof", "-a", "-p", str(process), "-d", "txt", "-Ffin"], "text")
        inode, matched = None, False
        for line in text.splitlines():
            if line.startswith("f"):
                inode = None
            if line.startswith("i"):
                inode = line[1:]
            if line == "n" + self.manifest["binary"]:
                require(inode == str(info.st_ino), "running controller executable vnode differs")
                matched = True
        require(matched, "selected binary is not the running controller text vnode")
        socket_path = Path(self.plan["root"]) / "control.sock"
        peer = socket_peer_pid(socket_path, process, min(10, self.remaining()))
        save(self.root / ("service-" + observation_id + "-socket.json"),
             {"socket": str(socket_path), "peer_pid": peer, "observed_at": self.now()})
        require(pid(read(["/bin/launchctl", "print", target], "after")) == process, "service changed during preflight")
        return {"pid": process, "binary_inode": info.st_ino, "service": target,
                "socket": str(Path(self.plan["root"]) / "control.sock")}

    def events(self, job, expires):
        cursor, all_events = 0, []
        while True:
            page = self.cli(["events", job, "--after", str(cursor), "--limit", "1000", "--json"], "events", expires)
            require(not page.get("resync") and page["snapshot"]["job_id"] == job, "event gap or wrong Job")
            events = page.get("events") or []
            for event in events:
                require(event["job_id"] == job and event["cursor"] > cursor, "foreign or unordered event")
                cursor = event["cursor"]
                all_events.append(event)
            require(page["next_cursor"] == cursor, "event cursor mismatch")
            if not page["more"]:
                return {"job_id": job, "events": all_events, "measurement": measurements(all_events)}
            require(events, "event pagination stalled")

    def logs(self, job, step, expires):
        offset, identity, h = 0, "", hashlib.sha256()
        while True:
            page = self.cli(["logs", job, "--after", str(offset), "--identity", identity, "--json"], "logs", expires)
            require(page["job_id"] == job and not page.get("gap") and page["offset"] == offset,
                    "wrong Job, log gap or offset mismatch")
            require(page["size"] == step["size"] and page["identity"] and (not identity or identity == page["identity"]), "log size/incarnation changed")
            chunk = base64.b64decode(page["data"] or "", validate=True)
            require(page["next_offset"] == offset + len(chunk) and page["next_offset"] <= step["size"], "invalid log progression")
            h.update(chunk)
            offset, identity = page["next_offset"], page["identity"]
            if not page["more"]:
                require(offset == step["size"] and "sha256:" + h.hexdigest() == step["sha256"], "incomplete or corrupt full log")
                return {"job_id": job, "size": offset, "sha256": "sha256:" + h.hexdigest(), "identity": identity}
            require(chunk, "log pagination stalled")

    def execute(self, step, entry):
        action, expires = step["action"], entry["expires_at"]
        job = self.result(self.state["steps"][step["job"]])["job_id"] if "job" in step else None
        if action in {"submit", "replay"}:
            original = step if action == "submit" else next(s for row in ROWS for s in self.manifest["rows"][row] if s["id"] == step["job"])
            saved = decode(read_private(self.root / (original["id"] + ".submission.json")))
            require(saved["spec_sha256"] == original["spec_sha256"] and saved["request_key"] == original["request_key"], "saved submission differs")
            if action == "submit" and not entry.get("submit_issued"):
                input_digest(saved["spec"])
                entry["submit_issued"] = True
                self.checkpoint()
            # Submit the frozen copy, including on an uncertain prior return.
            spec_path = self.root / (original["id"] + ".spec.json")
            raw = read_private(spec_path, 1 << 20)
            require(digest(raw) == original["spec_sha256"]
                    and decode(raw) == saved["spec"], "frozen spec replaced")
            result = self.cli(["submit", "--spec", str(spec_path), "--request-key", saved["request_key"], "--json"], "submit", expires)
            require(result["resource_id"] == self.manifest["resource"]["id"] and KEY.fullmatch(result["job_id"]), "wrong admission resource/Job")
            require(entry.get("pending_job_id", result["job_id"]) == result["job_id"], "original admission changed after lost reply")
            if action == "replay":
                require(result["job_id"] == job, "exact replay returned another Job")
            return result
        if action == "recover_plan":
            kind = step["kind"]
            target = job if kind == "job" else self.manifest["resource"]["id"]
            args = [kind, "recover", target, "--plan"]
            if step["plan_action"] != "health":
                args += ["--action", step["plan_action"]]
            return self.cli([*args, "--json"], kind + ".recover", expires)
        if action == "recover_apply":
            plan_step = next(s for row in ROWS for s in self.manifest["rows"][row] if s["id"] == step["plan"])
            plan = self.result(self.state["steps"][step["plan"]])
            kind = plan_step["kind"]
            target = self.result(self.state["steps"][plan_step["job"]])["job_id"] if kind == "job" else self.manifest["resource"]["id"]
            args = [kind, "recover", target, "--plan-token", plan["plan_token"], "--request-key", step["request_key"]]
            if "evidence_file" in step:
                require(digest(read_private(step["evidence_file"])) == step["evidence_sha256"], "approved recovery evidence changed")
                args += ["--evidence-file", step["evidence_file"]]
            return self.cli([*args, "--json"], kind + ".recover", expires,
                            step.get("expected_exit", 0), step.get("expected_error"))
        if action == "check":
            record = self.result(self.state["steps"][step["record"]])
            check_values(record, step["checks"])
            return {"checks": step["checks"], "observed": True}
        if action == "control":
            require(not entry.get("control_issued"), "control return uncertain; do not repeat; inspect and restore under original authority")
            entry["control_issued"] = True
            self.checkpoint()
            control = self.manifest["controls"][step["control"]]
            result = self.call([], "control", expires, control)
            require(result["exit_code"] == step.get("expected_exit", 0), "control failed; preserve evidence and use disclosed restoration")
            observation = decode(read_private(self.root / f"attempt-{self.state['attempts']:06d}.stdout"))
            require(observation.get("schema") == "haepari.utm-control-observation/v1"
                    and observation.get("vm_uuid") == self.manifest["resource"]["uuid"], "control observation binding required")
            result["observation"] = observation
            return result
        if action == "events":
            return self.events(job, expires)
        if action == "logs":
            return self.logs(job, step, expires)
        if action == "export":
            destination = self.root / (step["id"] + ".export")
            require(not destination.exists(), "export destination already exists; inspect retained partial export instead of replacing it")
            return self.cli(["export", job, "--to", str(destination), "--json"], "export", expires)
        if action in {"cancel", "purge"}:
            if "revision" not in entry:
                view = self.cli(["inspect", job, "--json"], "inspect", expires)
                entry["revision"] = view["receipt"]["job_revision"]
                entry["mutation_key"] = "campaign-" + uuid.uuid4().hex
                self.checkpoint()
            args = [action, job, "--expected-revision", str(entry["revision"])]
            if action == "cancel":
                args += ["--request-key", entry["mutation_key"]]
            return self.cli([*args, "--json"], action, expires, step.get("expected_exit", 0), step.get("expected_error"))
        if action == "wait":
            seconds = min(step["seconds"], max(1, int(self.remaining(expires)) - 1))
            return self.cli(["wait", job, "--timeout", f"{seconds}s", "--json"], "wait", expires, step.get("expected_exit", 0), step.get("expected_error"))
        args, operation = (["inspect", job, "--json"], "inspect") if action == "inspect" else (
            ["resource", "inspect", self.manifest["resource"]["id"], "--json"], "resource.inspect")
        while True:
            result = self.cli(args, operation, expires)
            if "until" not in step:
                return result
            try:
                check_values(result, step["until"])
                return result
            except CampaignError:
                time.sleep(min(0.25, self.remaining(expires)))

    def run(self):
        # Before any resumed effect (including purge), prove the retained
        # observations that justified completed campaign steps still exist.
        for entry in self.state["steps"].values():
            if entry.get("status") == "complete":
                require("result_ref" in entry, "completed step lacks retained result reference")
                self.result(entry)
        if self.state["started_at"] is None:
            self.state["started_at"] = self.now()
            self.checkpoint()
        self.remaining()
        for row in ROWS:
            for step in self.manifest["rows"][row]:
                entry = self.state["steps"].get(step["id"])
                if entry and entry["status"] == "complete":
                    continue
                if entry is None:
                    entry = {"row": row, "action": step["action"], "status": "started",
                             "expires_at": self.now() + step["seconds"]}
                    self.state["steps"][step["id"]] = entry
                    self.checkpoint()
                self.remaining(entry["expires_at"])
                entry["result"] = self.execute(step, entry)
                entry["status"] = "complete"
                try:
                    self.checkpoint()
                except Exception:
                    entry["status"] = "started"
                    raise
        return self.report()

    def report(self, error=None):
        rows, gaps = {}, {}
        for row in ROWS:
            rows[row] = "observations_collected"
            for step in self.manifest["rows"][row]:
                entry = self.state["steps"].get(step["id"], {})
                if entry.get("status") != "complete":
                    rows[row] = "incomplete"
                    continue
                try:
                    require("result_ref" in entry, "completed step lacks retained result reference")
                    self.result(entry)
                except (CampaignError, OSError, ValueError, KeyError, TypeError) as problem:
                    rows[row] = "incomplete"
                    gaps[step["id"]] = str(problem)
        value = {"schema": "haepari.utm-campaign-report/v1", "campaign_sha256": digest(self.manifest_raw),
                 "candidate_sha256": self.manifest["binary_sha256"], "source_revision": self.manifest["source_revision"],
                 "rows": rows, "execution": "observations_collected" if all(v == "observations_collected" for v in rows.values()) else "incomplete",
                 "qualification": "unverified; assess native evidence against TASK-038 criteria",
                 "deferred": ["DF-001", "DF-002", "DF-003", "DF-004"], "error": error,
                 "observation_gaps": gaps,
                 "restoration": self.authority["restoration"], "automatic_service_vm_restoration": "none"}
        save(self.root / "report.json", value)
        return value


def measurements(events):
    starts, completions, gaps = {}, {}, []
    for event in events:
        if event["kind"] == "vm_ssh_measurement_gap":
            gaps.append(event["payload"])
        if event["kind"] not in {"vm_ssh_exchange_start", "vm_ssh_exchange_complete"}:
            continue
        data = event["payload"]
        bucket = completions if data["complete"] else starts
        require(data["id"] not in bucket, "duplicate SSH measurement ID")
        bucket[data["id"]] = data
    pairs = []
    for key in starts.keys() & completions.keys():
        start, end = starts[key], completions[key]
        for field in ("identity", "vm_uuid", "operation", "request_digest", "runner_digest", "deadline", "started_at"):
            require(start[field] == end[field], "SSH measurement pair binding changed")
        pairs.append(end)
    return {"complete_pairs": len(pairs), "incomplete_starts": sorted(starts.keys() - completions.keys()),
            "orphan_completions": sorted(completions.keys() - starts.keys()), "detail_gaps": gaps,
            "archive_bytes": sum(p["archive_bytes"] for p in pairs),
            "runner_bytes": sum(p["runner_bytes"] for p in pairs),
            "response_bytes": sum(p["stdout_bytes"] + p["stderr_bytes"] for p in pairs),
            "network_protocol_overhead": "unobserved; application bytes are not packet measurements"}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("preflight", "run", "resume", "report"))
    parser.add_argument("--campaign-file", type=Path, required=True)
    parser.add_argument("--authority-file", type=Path, required=True)
    parser.add_argument("--evidence-root", type=Path, required=True)
    args = parser.parse_args(argv)
    os.umask(0o077)
    campaign = None
    try:
        campaign = Campaign(args.campaign_file, args.authority_file, args.evidence_root)
        if args.mode == "report":
            result = campaign.report()
        else:
            require(args.mode != "run" or campaign.state["started_at"] is None, "existing campaign requires resume")
            require(args.mode != "resume" or campaign.state["started_at"] is not None, "no campaign to resume")
            result = campaign.preflight()
            if args.mode != "preflight":
                result = campaign.run()
        print(json.dumps(result, sort_keys=True))
        return 0 if args.mode == "preflight" or result["execution"] == "observations_collected" else 2
    except (CampaignError, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        result = campaign.report(str(error)) if campaign else {"execution": "incomplete", "qualification": "unverified", "error": str(error)}
        print(json.dumps(result, sort_keys=True))
        return 2
    finally:
        if campaign:
            campaign.close()


if __name__ == "__main__":
    raise SystemExit(main())
