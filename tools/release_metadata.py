"""Bind actual Go build information to the pins and distributed notices."""
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
binary, destination = map(Path, sys.argv[1:])
pins = dict(line.split("=", 1) for line in (ROOT / "toolchains.env").read_text().splitlines() if line and not line.startswith("#"))
result = subprocess.run([str(ROOT / "tools/toolchain"), "go", "version", "-m", str(binary)], check=True, text=True, capture_output=True)
lines = result.stdout.splitlines()
actual = lines[0].rsplit(": ", 1)[-1].strip()
expected = "go" + pins["GO_VERSION"]
if actual != expected:
    sys.exit(f"release Go mismatch: required={expected} actual={actual} path={binary}")
notices = (ROOT / "THIRD_PARTY_NOTICES.txt").read_text()
if f"Go {pins['GO_VERSION']} runtime/standard library" not in notices:
    sys.exit("third-party notices do not match the pinned Go runtime")
modules = []
for line in lines[1:]:
    fields = line.split()
    if fields and fields[0] == "=>":
        sys.exit("release module replacements need explicit notice reconciliation")
    if fields and fields[0] == "dep":
        name, version = fields[1:3]
        if not re.search(re.escape(name) + r"\s+" + re.escape(version) + r"(?:\s|$)", notices):
            sys.exit(f"third-party notice mismatch: {name} {version}")
        modules.append({"path": name, "version": version})
destination.write_text(json.dumps({"go_version": actual, "toolchains": pins, "modules": modules}, indent=2) + "\n")
print("Release build metadata and third-party notices match")
