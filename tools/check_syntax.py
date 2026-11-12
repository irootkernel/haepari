"""Compile Python source in memory, without writing bytecode into the checkout."""
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
for directory in ("tools", "tests", "docs/tools", "examples/project-test", "packaging"):
    for path in sorted((ROOT / directory).rglob("*.py")):
        compile(path.read_bytes(), str(path), "exec")
print("Python syntax OK (no bytecode written)")
