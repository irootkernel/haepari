#!/usr/bin/env python3
"""Complete release-tree inventory verification against SHA256SUMS.

Usage: python3 verify_tree.py [root]      (default: the directory containing
this script and SHA256SUMS)

Compares the complete unpacked tree with the recorded inventory without
following symlinks: reports each **missing** file, each **added** entry
(including unlisted symlinks and special files) and each **tampered** entry.
A recorded regular file replaced by a symlink, special file or directory is
reported as tampered even when the target bytes are identical, and SHA256SUMS
itself must be a regular file. Ordinary structural directories that the
inventory does not list are fine. Exits 0 only when the complete tree matches;
exits 1 with each difference by name, leaving all files in place for
inspection. Standard library only; independent of the packaged haepari binary.
"""

import hashlib
import os
import stat
import sys
from pathlib import Path


def digest(path: str) -> str:
    sha = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            sha.update(block)
    return sha.hexdigest()


def classify(root: str) -> dict[str, str]:
    """Map every tree entry (never following symlinks) to its lstat kind."""
    def scan_error(error: OSError) -> None:
        raise error

    kinds: dict[str, str] = {}
    for current, dirnames, filenames in os.walk(root, followlinks=False, onerror=scan_error):
        for name in list(dirnames) + filenames:
            full = os.path.join(current, name)
            rel = os.path.relpath(full, root)
            mode = os.lstat(full).st_mode
            if stat.S_ISLNK(mode):
                kinds[rel] = "symlink"
            elif stat.S_ISDIR(mode):
                kinds[rel] = "directory"
            elif stat.S_ISREG(mode):
                kinds[rel] = "regular"
            else:
                kinds[rel] = "special"
    return kinds


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).resolve().parent).resolve()
    kinds = classify(str(root))
    if "SHA256SUMS" not in kinds:
        print(f"missing inventory: {root / 'SHA256SUMS'}", file=sys.stderr)
        return 1
    if kinds["SHA256SUMS"] != "regular":
        print(f"tampered: SHA256SUMS (not a regular file; {kinds['SHA256SUMS']})", file=sys.stderr)
        return 1
    sums = root / "SHA256SUMS"
    recorded: dict[str, str] = {}
    for line in sums.read_text().splitlines():
        value, _, name = line.partition(" ")
        name = name.strip()
        if not name:
            value, _, name = line.partition("*")
        recorded[name.strip()] = value.strip()

    differences: list[str] = []
    for name in sorted(set(recorded) | set(kinds)):
        kind = kinds.get(name)
        if kind is None:
            differences.append(f"missing: {name}")
        elif kind in ("symlink", "special"):
            if name == "SHA256SUMS" or name in recorded:
                where = "SHA256SUMS" if name == "SHA256SUMS" else "recorded regular file"
                differences.append(f"tampered: {name} ({kind} replaces {where})")
            else:
                differences.append(f"added: {name} ({kind})")
        elif kind == "directory":
            if name in recorded:
                differences.append(f"tampered: {name} (directory replaces recorded regular file)")
        elif name == "SHA256SUMS":
            continue
        elif name not in recorded:
            differences.append(f"added: {name}")
        elif digest(str(root / name)) != recorded[name]:
            differences.append(f"tampered: {name}")

    if differences:
        for line in differences:
            print(line, file=sys.stderr)
        print(f"tree verification FAILED: {len(differences)} difference(s); files left in place", file=sys.stderr)
        return 1
    regular = sum(1 for kind in kinds.values() if kind == "regular")
    print(f"tree verification OK: {regular - 1} file(s) match SHA256SUMS "
          "(inventory itself excluded; symlinks and special files rejected, structural directories ignored)")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except OSError as error:
        print(f"tree verification FAILED: cannot inspect tree: {error}", file=sys.stderr)
        sys.exit(1)
