"""Check Go formatting, or apply it for fmt and test-prepare."""
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
files = sorted(str(p.relative_to(ROOT)) for base in ("cmd", "internal") for p in (ROOT / base).rglob("*.go"))
command = [str(ROOT / "tools/toolchain"), "gofmt", "-w" if "--write" in sys.argv else "-l", *files]
result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, check=False)
sys.stderr.write(result.stderr)
if result.returncode:
    sys.exit(result.returncode)
if result.stdout:
    sys.stderr.write(result.stdout + "Run make fmt or make test-prepare.\n")
    sys.exit(1)
