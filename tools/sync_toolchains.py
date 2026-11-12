"""Generate/check portable example snapshots from the root authority."""
import argparse
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--write", action="store_true")
parser.add_argument("--check", action="store_true")
args = parser.parse_args()
errors = []
for relative in ("toolchains.env", "tools/toolchain"):
    source = ROOT / relative
    target = ROOT / "examples/project-test" / relative
    if args.write:
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(source.read_bytes())
        target.chmod(source.stat().st_mode & 0o777)
    if not target.is_file() or target.read_bytes() != source.read_bytes() or (target.stat().st_mode & 0o777) != (source.stat().st_mode & 0o777):
        errors.append(str(target.relative_to(ROOT)))
if errors:
    sys.exit("stale generated toolchain snapshots: " + ", ".join(errors) + "; run make docs-update")
print("Toolchain snapshots OK")
