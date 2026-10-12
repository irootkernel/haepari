#!/usr/bin/env python3
"""Validate the Haepari documentation package.

Usage:
    python3 docs/tools/validate_docs.py          # check only; exit 1 on failure
    python3 docs/tools/validate_docs.py --write  # also rewrite checks.json and MANIFEST.json

Standard library only. The checks are documentation checks; they do not test product behavior.
"""
import datetime
import hashlib
import json
import os
import re
import subprocess
import sys

DOCS = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
REPO = os.path.dirname(DOCS)
REVISION = "3.5"
REVISION_DATE = "2026-10-12"
EXCLUDED_DIRS = {".omc"}
MANIFEST = "MANIFEST.json"
CHECKS = "checks.json"
AQUARIUM_ONLY_IDS = re.compile(r"(?<![:\w])(TASK-0(?:8[6-9]|9\d)|TASK-10[0-2]|EPIC-019)\b")
DEFERRED_MARKERS = re.compile(
    r"defer|Deferred|post-v0\.1\.0|re-entry|R2|B1|B2|not a v0\.1\.0|not v0\.1\.0|design history|retained",
    re.IGNORECASE)
DEFERRED_TERMS = re.compile(
    r"haepari lease|external-execution/v1|EPIC-008|TASK-0(?:29|3[0-2])\b|\bE(?:0[1-9]|1\d|20)\b|HP-REQ-0(?:6[3-9]|7\d)")
# Files that are deferred design history or disposition records; deferred terms are expected there.
DEFERRED_SCOPE_FILES = {
    "contracts/external-leases.md", "roadmap/EPIC-008.md",
    "architecture-decision-records/0014-external-execution-leases.md",
    "architecture-decision-records/0015-linked-recovery-segments.md",
    "review-disposition.md", "REVISION-NOTES.md", "sources.md",
    # Quotes other repositories' current R2 wording in order to request its removal.
    "to-external.md",
    # Validation record: lists deferred counts and changed file names.
    "package-validation.md",
}


def package_files():
    out = []
    for root, dirs, files in os.walk(DOCS):
        dirs[:] = sorted(d for d in dirs if d not in EXCLUDED_DIRS and not d.startswith("__"))
        for name in sorted(files):
            if name == ".DS_Store" or name.endswith(".pyc"):
                continue
            rel = os.path.relpath(os.path.join(root, name), DOCS)
            out.append(rel)
    return sorted(out)


def read(rel):
    with open(os.path.join(DOCS, rel), encoding="utf-8") as fh:
        return fh.read()


def slug(heading, seen):
    s = heading.strip().lower()
    s = re.sub(r"[^\w\- ]", "", s)
    s = s.replace(" ", "-")
    base, n = s, seen.get(s, 0)
    seen[base] = n + 1
    return base if n == 0 else f"{base}-{n}"


def anchors(rel):
    seen, result, in_fence = {}, set(), False
    for line in read(rel).splitlines():
        if line.startswith("```"):
            in_fence = not in_fence
            continue
        m = None if in_fence else re.match(r"^(#{1,6})\s+(.*)$", line)
        if m:
            result.add(slug(m.group(2), seen))
    return result


def check_links(md_files):
    errors, count = [], 0
    for rel in md_files:
        text = read(rel)
        text = re.sub(r"```.*?```", "", text, flags=re.S)
        for target in re.findall(r"\]\(([^)\s]+)\)", text):
            if re.match(r"^[a-z]+:", target):
                continue
            count += 1
            path, _, frag = target.partition("#")
            dest = os.path.normpath(os.path.join(os.path.dirname(rel), path)) if path else rel
            if not os.path.exists(os.path.join(DOCS, dest)):
                errors.append(f"{rel}: missing file {target}")
                continue
            if frag and dest.endswith(".md") and frag not in anchors(dest):
                errors.append(f"{rel}: missing anchor {target}")
    return count, errors


def check_fences(md_files):
    return [rel for rel in md_files if sum(1 for l in read(rel).splitlines() if l.startswith("```")) % 2]


def check_json(files):
    errors = []
    for rel in files:
        if rel.endswith(".json"):
            try:
                json.loads(read(rel))
            except ValueError as exc:
                errors.append(f"{rel}: {exc}")
    return errors


def scoped_items(text, pattern, sections):
    """Classify stable IDs by their declared section, independent of numbering/order."""
    scope, seen = None, set()
    for line in text.splitlines():
        if line.startswith("## "):
            scope = sections.get(line[3:])
        match = re.match(pattern, line)
        if match:
            identity = match.group(1)
            if scope is None:
                raise ValueError(f"{identity}: missing declared scope section")
            if identity in seen:
                raise ValueError(f"{identity}: duplicate scoped ID")
            seen.add(identity)
            yield identity, scope


def parse_requirements():
    text = read("requirements.md")
    scopes = dict(scoped_items(text, r"^### (HP-REQ-\d{3})$", {
        "Requirements": "mandatory",
        "Consumer-owned optional requirements": "consumer-owned",
        "Deferred external-execution requirements": "deferred",
    }))
    reqs = {}
    for m in re.finditer(r"^### (HP-REQ-\d{3})\n(.*?)(?=^### |^## |\Z)", text, re.S | re.M):
        owner = re.search(r"Implementation owner:(.*)", m.group(2))
        reqs[m.group(1)] = {
            "active": scopes[m.group(1)] == "mandatory",
            "scope": scopes[m.group(1)],
            "owners": set(re.findall(r"TASK-\d{3}", owner.group(1))) if owner else set(),
        }
    return reqs


def parse_cases():
    return dict(scoped_items(read("verification.md"), r"^\| ([VE]\d{2}) \|", {
        "Mandatory failure matrix": "mandatory",
        "Additional active managed cases": "mandatory",
        "Consumer-owned optional cases": "consumer-owned",
        "External execution and continuation matrix": "deferred",
    }))


def parse_roadmap():
    tasks = {}
    for line in read("roadmap.md").splitlines():
        m = re.match(r"^\| \[(TASK-\d{3})\]\([^)]*\) \| ([^|]+)\| ([^|]+)\| (Active|Deferred) \| (\w[\w ]*) \|$", line)
        if m:
            prereq = set(re.findall(r"TASK-\d{3}", m.group(3))) if m.group(4) == "Active" else set()
            tasks[m.group(1)] = {"title": m.group(2).strip(), "prereqs": prereq,
                                 "active": m.group(4) == "Active", "status": m.group(5)}
    epics = re.findall(r"^\| \[(EPIC-\d{3})\]\([^)]*\) \|[^|]*\|[^|]*\| (Active|Deferred) \|", read("roadmap.md"), re.M)
    return tasks, epics


def parse_epic_tasks():
    coverage, titles = {}, {}
    for name in sorted(os.listdir(os.path.join(DOCS, "roadmap"))):
        text = read(f"roadmap/{name}")
        for m in re.finditer(r"^## (TASK-\d{3})\n\n### (.+)\n\n(?:Requirement coverage: (.*))?", text, re.M):
            titles[m.group(1)] = m.group(2).strip()
            coverage[m.group(1)] = set(re.findall(r"HP-REQ-\d{3}", m.group(3) or ""))
    return coverage, titles


def check_dag(tasks):
    errors = []
    active = {t for t, v in tasks.items() if v["active"]}
    for t in active:
        for p in tasks[t]["prereqs"]:
            if p not in active:
                errors.append(f"{t} depends on non-active {p}")
    state = {}

    def visit(t, stack):
        if state.get(t) == 1:
            errors.append("cycle: " + " -> ".join(stack + [t]))
            return
        if state.get(t) == 2:
            return
        state[t] = 1
        for p in tasks[t]["prereqs"]:
            visit(p, stack + [t])
        state[t] = 2

    for t in sorted(active):
        visit(t, [])
    children = {t: set() for t in active}
    for t in active:
        for p in tasks[t]["prereqs"]:
            children[p].add(t)
    sinks = sorted(t for t in active if not children[t])

    def descendants(t):
        out, todo = set(), [t]
        while todo:
            for c in children[todo.pop()]:
                if c not in out:
                    out.add(c)
                    todo.append(c)
        return out

    # Historical qualification is preserved without reopening it when a VM is unavailable.
    # Only current live gates and their actual successors depend on new VM observations.
    prose = read("roadmap.md")
    hi = re.search(r"Historical real-UTM qualification tasks: (.*?)\.", prose)
    ev = re.search(r"Current real-VM acceptance tasks: (.*?)\.", prose)
    bl = re.search(r"Current real-VM prerequisite closure: (.*?)\.", prose)
    historical = set(re.findall(r"TASK-\d{3}", hi.group(1))) if hi else set()
    evidence = set(re.findall(r"TASK-\d{3}", ev.group(1))) if ev else set()
    declared_blocked = set(re.findall(r"TASK-\d{3}", bl.group(1))) if bl else set()
    if not historical or not evidence or not declared_blocked:
        errors.append("roadmap must distinguish historical qualification, current live gates and their closure")
    if historical & evidence:
        errors.append("historical qualification cannot also be a current live gate")
    for t in historical | evidence:
        if t not in active:
            errors.append(f"real-VM qualification names non-active {t}")
        elif t in historical and tasks[t]["status"] != "Accepted":
            errors.append(f"historical qualification {t} is not Accepted")
    blocked = set(evidence)
    for t in evidence:
        if t in children:
            blocked |= descendants(t)
    memo = {}

    def depth(t):
        if t not in memo:
            memo[t] = 1 + max((depth(p) for p in tasks[t]["prereqs"]), default=0)
        return memo[t]

    if sinks != ["TASK-028"]:
        errors.append(f"active sinks should be only TASK-028, found {sinks}")
    if blocked != declared_blocked:
        errors.append(f"DAG closure of current real-VM tasks {sorted(blocked)} differs from the stated prerequisite closure {sorted(declared_blocked)}")
    return errors, {"sinks": sinks, "utm_gated": sorted(blocked),
                    "historical_utm_qualification": sorted(historical),
                    "current_live_vm_gates": sorted(evidence),
                    "longest_chain": max(depth(t) for t in active)}


def check_traceability(reqs, tasks, coverage):
    errors = []
    text = read("traceability.md")
    rows = {}
    for m in re.finditer(r"^\| (HP-REQ-\d{3}) \| ([^|]+)\| ([^|]+)\| ([^|]+)\|$", text, re.M):
        if m.group(1) in rows:
            errors.append(f"{m.group(1)}: duplicate traceability row")
        rows[m.group(1)] = (set(re.findall(r"TASK-\d{3}", m.group(2))), set(re.findall(r"[VE]\d{2}", m.group(3))))
    active_reqs = {r for r, v in reqs.items() if v["active"]}
    if set(rows) != active_reqs:
        errors.append(f"traceability rows differ from active requirements: "
                      f"missing {sorted(active_reqs - set(rows))}, extra {sorted(set(rows) - active_reqs)}")
    cases = parse_cases()
    vcases = {c for c, scope in cases.items() if scope == "mandatory"}
    optional_cases = {c for c, scope in cases.items() if scope == "consumer-owned"}
    ecases = {c for c, scope in cases.items() if scope == "deferred"}
    referenced = set().union(*(c for _, c in rows.values())) if rows else set()
    if referenced != vcases:
        errors.append(f"case references differ: unreferenced {sorted(vcases - referenced)}, unknown {sorted(referenced - vcases)}")
    for req, (row_tasks, row_cases) in rows.items():
        if req not in reqs:
            errors.append(f"{req}: unknown traced requirement")
            continue
        if reqs[req]["active"] and not reqs[req]["owners"]:
            errors.append(f"{req}: mandatory requirement has no implementation owner")
        if reqs[req]["active"] and not row_cases:
            errors.append(f"{req}: mandatory requirement has no verifying case")
        if not reqs[req]["owners"] <= row_tasks:
            errors.append(f"{req}: owners {sorted(reqs[req]['owners'])} not all in traceability {sorted(row_tasks)}")
        for t in row_tasks:
            if t not in tasks or not tasks[t]["active"]:
                errors.append(f"{req}: traced to non-active {t}")
            elif req not in coverage.get(t, set()):
                errors.append(f"{req}: {t} coverage does not list it")
    for t, reqset in coverage.items():
        if t in tasks and tasks[t]["active"]:
            for r in reqset:
                if r not in reqs:
                    errors.append(f"{t} covers unknown {r}")
                if r in rows and t not in rows[r][0]:
                    errors.append(f"{t} covers {r} but traceability row omits {t}")
                if r in reqs and not reqs[r]["active"]:
                    errors.append(f"active {t} covers {reqs[r]['scope']} {r}")
    for r, v in reqs.items():
        if not v["active"]:
            for t in v["owners"]:
                if t in tasks and tasks[t]["active"]:
                    errors.append(f"{v['scope']} {r} owned by active {t}")
    return errors, vcases, ecases, optional_cases


def check_titles(tasks, titles):
    return [f"{t}: roadmap title {v['title']!r} != epic title {titles.get(t)!r}"
            for t, v in tasks.items() if titles.get(t) != v["title"]]


# to-external.md quotes Aquarium's own documents and proposes replacement text for them;
# inside Aquarium, its IDs are native and unqualified.
ID_CHECK_EXEMPT = {"to-external.md"}


def check_unqualified_ids(md_files):
    hits = []
    for rel in md_files:
        if rel in ID_CHECK_EXEMPT:
            continue
        for n, line in enumerate(read(rel).splitlines(), 1):
            for m in AQUARIUM_ONLY_IDS.finditer(line):
                hits.append(f"{rel}:{n}: {m.group(1)}")
    return hits


def check_deferred_terms(md_files):
    hits = []
    for rel in md_files:
        if rel in DEFERRED_SCOPE_FILES or rel.startswith("rollout-requests/"):
            continue
        stack = []  # (level, deferred) for enclosing headings
        for n, line in enumerate(read(rel).splitlines(), 1):
            m = re.match(r"^(#{1,6})\s", line)
            if m:
                level = len(m.group(1))
                while stack and stack[-1][0] >= level:
                    stack.pop()
                stack.append((level, bool(DEFERRED_MARKERS.search(line)) or "External execution" in line))
            in_deferred_section = any(d for _, d in stack)
            if DEFERRED_TERMS.search(line) and not in_deferred_section and not DEFERRED_MARKERS.search(line):
                hits.append(f"{rel}:{n}")
    return hits


WORDS = {"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8,
         "nine": 9, "ten": 10, "eleven": 11, "twelve": 12}


def number(token):
    token = token.strip().lower()
    return int(token) if token.isdigit() else WORDS.get(token)


def check_stated_counts(counts, link_count):
    """Numbers stated in prose must equal the computed counts."""
    c = counts
    expectations = [
        ("README.md", r"orders (\w+) active epics and (\w+) active implementation tasks \((\w+) in total\)",
         (c["epics"]["active"], c["tasks"]["active"], c["tasks"]["total"])),
        ("roadmap.md", r"Totals: (\w+) epics \((\w+) active, (\w+) deferred\); (\w+) tasks \((\w+) active, (\w+) deferred\)",
         (c["epics"]["total"], c["epics"]["active"], c["epics"]["deferred"],
          c["tasks"]["total"], c["tasks"]["active"], c["tasks"]["deferred"])),
        ("package-validation.md", r"\| Requirements \| (\d+) \| (\d+) \| (\d+) \| (\d+) \|",
         (c["requirements"]["total"], c["requirements"]["active"],
          c["requirements"]["consumer_owned"], c["requirements"]["deferred"])),
        ("package-validation.md", r"\| Epics \| (\d+) \| (\d+) \| 0 \| (\d+) \|",
         (c["epics"]["total"], c["epics"]["active"], c["epics"]["deferred"])),
        ("package-validation.md", r"\| Tasks \| (\d+) \| (\d+) \| 0 \| (\d+) \|",
         (c["tasks"]["total"], c["tasks"]["active"], c["tasks"]["deferred"])),
        ("package-validation.md", r"\| ADRs \| (\d+) \| (\d+) \| 0 \| (\d+) \|",
         (c["adrs"]["total"], c["adrs"]["active"], c["adrs"]["deferred"])),
        ("package-validation.md", r"\| Verification cases \| (\d+) \| (\d+) \| (\d+) \| (\d+) \|",
         (c["verification_cases"]["total"], c["verification_cases"]["active_managed"],
          c["verification_cases"]["consumer_owned"], c["verification_cases"]["deferred_external"])),
        ("package-validation.md", r"holds (\d+) Markdown documents, (\d+) JSON design templates",
         (c["markdown_documents"], c["json_templates"])),
        ("package-validation.md", r"Relative links and anchors \((\d+) links", (link_count,)),
        ("verification.md", r"Mandatory managed cases: (\d+)", (c["verification_cases"]["active_managed"],)),
        ("verification.md", r"Consumer-owned optional cases: (\d+)", (c["verification_cases"]["consumer_owned"],)),
    ]
    errors = []
    for rel, pattern, expected in expectations:
        found = re.findall(pattern, read(rel))
        if len(found) != 1:
            errors.append(f"{rel}: expected one statement matching {pattern!r}, found {len(found)}")
            continue
        got = found[0] if isinstance(found[0], tuple) else (found[0],)
        values = tuple(number(g) for g in got)
        if values != tuple(expected):
            errors.append(f"{rel}: stated {got} but computed {expected}")
    return errors


def collision_review_list(md_files):
    """Lines that mention Aquarium next to a bare task/epic ID; reviewed by hand, not failed."""
    out = []
    for rel in md_files:
        for n, line in enumerate(read(rel).splitlines(), 1):
            if re.search(r"\bAquarium\b(?!:)", line) and re.search(r"(?<![:\w])(TASK|EPIC)-\d{3}", line):
                out.append(f"{rel}:{n}")
    return out


def git(*args):
    try:
        return subprocess.run(["git", "-C", REPO, *args], capture_output=True, text=True, timeout=60)
    except (OSError, subprocess.SubprocessError):
        return None


def main():
    write = "--write" in sys.argv
    files = package_files()
    md = [f for f in files if f.endswith(".md")]
    json_templates = [f for f in files if f.startswith("examples/") and f.endswith(".json")]
    failures = {}

    def record(name, errors):
        if errors:
            failures[name] = errors
        return "passed" if not errors else f"failed ({len(errors)})"

    results = {}
    utf8_errors = []
    for f in files:
        try:
            read(f)
        except UnicodeDecodeError:
            utf8_errors.append(f)
    results["utf8"] = record("utf8", utf8_errors)
    link_count, link_errors = check_links(md)
    results["relative_links_and_anchors"] = record("links", link_errors)
    results["markdown_fences"] = record("fences", check_fences(md))
    results["json_syntax"] = record("json", check_json(files))
    reqs = parse_requirements()
    tasks, epics = parse_roadmap()
    coverage, titles = parse_epic_tasks()
    results["task_titles_match_epics"] = record("titles", check_titles(tasks, titles))
    dag_errors, dag = check_dag(tasks)
    results["roadmap_dag"] = record("dag", dag_errors)
    guards = []
    if len(reqs) < 1 or len(tasks) < 1 or len(coverage) < 1:
        guards.append("requirements, roadmap tasks or epic task sections were not parsed")
    trace_errors, vcases, ecases, optional_cases = check_traceability(reqs, tasks, coverage)
    if len(vcases) < 1:
        guards.append("no V-cases were parsed from verification.md")
    if not re.search(r"^\| HP-REQ-\d{3} \|", read("traceability.md"), re.M):
        guards.append("no traceability rows were parsed")
    results["parsers_found_items"] = record("parser_guards", guards)
    results["requirement_task_case_traceability"] = record("traceability", trace_errors)
    results["qualified_cross_project_ids"] = record("unqualified_ids", check_unqualified_ids(md))
    results["deferred_terms_in_active_text"] = record("deferred_terms", check_deferred_terms(md))
    diff = git("diff", "--check")
    results["worktree_diff_check"] = "not run" if diff is None else record(
        "diff_check", [l for l in diff.stdout.splitlines() if l.strip()])
    results["schema_validation"] = "not run; no standalone JSON Schema documents exist (Go decoder tests are separate)"

    counts = {
        "requirements": {"total": len(reqs), "active": sum(v["active"] for v in reqs.values()),
                         "consumer_owned": sum(v["scope"] == "consumer-owned" for v in reqs.values()),
                         "deferred": sum(v["scope"] == "deferred" for v in reqs.values())},
        "epics": {"total": len(epics), "active": sum(s == "Active" for _, s in epics),
                  "deferred": sum(s == "Deferred" for _, s in epics)},
        "tasks": {"total": len(tasks), "active": sum(v["active"] for v in tasks.values()),
                  "deferred": sum(not v["active"] for v in tasks.values())},
        "adrs": {"total": len([f for f in files if re.match(r"architecture-decision-records/\d{4}-", f)]),
                 "deferred": 2},
        "verification_cases": {"total": len(vcases) + len(optional_cases) + len(ecases),
                               "active_managed": len(vcases), "consumer_owned": len(optional_cases),
                               "deferred_external": len(ecases)},
        "markdown_documents": len(md),
        "json_templates": len(json_templates),
        "producer_request_drafts": len([f for f in files if f.startswith("rollout-requests/")]),
        "relative_links_checked": link_count,
    }
    counts["adrs"]["active"] = counts["adrs"]["total"] - 2

    results["stated_counts_match"] = record("stated_counts", check_stated_counts(counts, link_count))
    review = collision_review_list(md)
    results["collision_id_review_lines"] = f"{len(review)} lines listed for manual review (not a failure)"
    manifest_files = [f for f in files if f != MANIFEST]
    if write:
        checks = {
            "design_revision": REVISION,
            "validation_date": datetime.date.today().isoformat(),
            "scope": "documentation only",
            "generator": "docs/tools/validate_docs.py",
            "document_checks": results,
            "roadmap_dag": dag,
            "counts": counts,
            "product_tests": "not run by documentation validator",
            "platform_experiments": "not run by documentation validator",
            "aquarium_or_mulgae_repository_edits": "none",
            "external_messages": "not sent",
            "package_zip": "not built",
            "committed": False,
        }
        with open(os.path.join(DOCS, CHECKS), "w", encoding="utf-8") as fh:
            fh.write(json.dumps(checks, indent=2) + "\n")
        entries = []
        for f in manifest_files:
            data = open(os.path.join(DOCS, f), "rb").read()
            entries.append({"path": f, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()})
        manifest = {
            "package": "Haepari v0.1.0 managed-first design with advisory locks and Aquarium adoption handoff",
            "design_revision": REVISION,
            "revision_date": REVISION_DATE,
            "status": "implementation in progress; no product acceptance or release claimed",
            "counts": {"package_files_excluding_manifest": len(entries), **{
                k: v for k, v in counts.items() if k != "relative_links_checked"}},
            "files": entries,
        }
        with open(os.path.join(DOCS, MANIFEST), "w", encoding="utf-8") as fh:
            fh.write(json.dumps(manifest, indent=2) + "\n")
    else:
        try:
            manifest = json.loads(read(MANIFEST))
            listed = {e["path"]: e for e in manifest["files"]}
            errs = []
            if set(listed) != set(manifest_files):
                errs.append(f"file set differs: missing {sorted(set(manifest_files) - set(listed))}, "
                            f"extra {sorted(set(listed) - set(manifest_files))}")
            for f in manifest_files:
                if f in listed:
                    data = open(os.path.join(DOCS, f), "rb").read()
                    if listed[f]["bytes"] != len(data) or listed[f]["sha256"] != hashlib.sha256(data).hexdigest():
                        errs.append(f"{f}: size/hash mismatch")
            results["manifest_integrity"] = record("manifest", errs)
        except (OSError, ValueError, KeyError) as exc:
            results["manifest_integrity"] = record("manifest", [str(exc)])

    print(json.dumps({"results": results, "roadmap_dag": dag, "counts": counts,
                      "collision_review": review}, indent=2))
    for name, errs in failures.items():
        print(f"\n[{name}]", file=sys.stderr)
        for e in errs[:40]:
            print("  " + e, file=sys.stderr)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
