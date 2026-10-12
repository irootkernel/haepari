"""Offline regression checks for documentation scope and traceability rules."""

import importlib.util
from pathlib import Path

import pytest


SPEC = importlib.util.spec_from_file_location(
    "validate_docs", Path(__file__).resolve().parents[2] / "docs/tools/validate_docs.py"
)
DOCS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DOCS)


@pytest.fixture
def documents(monkeypatch):
    texts = {
        "requirements.md": """## Consumer-owned optional requirements
### HP-REQ-037
Consumer owner: example project.
## Requirements
### HP-REQ-001
Implementation owner: TASK-001.
## Deferred external-execution requirements
### HP-REQ-063
Implementation owner: TASK-029.
""",
        "verification.md": """## Consumer-owned optional cases
| V25 | Consumer discovery |
## Mandatory failure matrix
| V01 | Controller ownership |
## Additional active managed cases
| V32 | Isolated candidate |
## External execution and continuation matrix
| E01 | Future external execution |
""",
        "traceability.md": """| HP-REQ-001 | TASK-001 | V01,V32 | local |
""",
    }
    monkeypatch.setattr(DOCS, "read", texts.__getitem__)
    return texts


def check():
    return DOCS.check_traceability(
        DOCS.parse_requirements(),
        {"TASK-001": {"active": True}, "TASK-029": {"active": False}},
        {"TASK-001": {"HP-REQ-001"}, "TASK-029": {"HP-REQ-063"}},
    )


def test_noncontiguous_ids_and_reordered_scope_sections(documents):
    errors, mandatory, deferred, consumer = check()
    assert errors == []
    assert mandatory == {"V01", "V32"}
    assert consumer == {"V25"}
    assert deferred == {"E01"}
    reqs = DOCS.parse_requirements()
    assert reqs["HP-REQ-001"]["active"]
    assert reqs["HP-REQ-037"]["scope"] == "consumer-owned"
    assert not reqs["HP-REQ-063"]["active"]


@pytest.mark.parametrize("document,parser,extra", [
    ("requirements.md", DOCS.parse_requirements,
     "## Requirements\n### HP-REQ-037\nImplementation owner: TASK-001.\n"),
    ("verification.md", DOCS.parse_cases,
     "## Mandatory failure matrix\n| V25 | Duplicate consumer ID |\n"),
])
def test_duplicate_ids_across_scopes_are_rejected(documents, document, parser, extra):
    documents[document] += extra
    with pytest.raises(ValueError, match="duplicate scoped ID"):
        parser()


@pytest.mark.parametrize("document,parser,extra", [
    ("requirements.md", DOCS.parse_requirements,
     "## Unclassified\n### HP-REQ-002\nImplementation owner: TASK-001.\n"),
    ("verification.md", DOCS.parse_cases,
     "## Unclassified\n| V02 | No declared layer |\n"),
])
def test_unclassified_ids_cannot_disappear_from_counts(documents, document, parser, extra):
    documents[document] += extra
    with pytest.raises(ValueError, match="missing declared scope"):
        parser()


def test_missing_mandatory_requirement_mapping_fails(documents):
    documents["traceability.md"] = ""
    assert any("missing ['HP-REQ-001']" in error for error in check()[0])


@pytest.mark.parametrize("case", ["V25", "E01", "V99"])
def test_optional_deferred_or_unknown_case_cannot_satisfy_mandatory_evidence(documents, case):
    documents["traceability.md"] = f"| HP-REQ-001 | TASK-001 | V01,V32,{case} | local |\n"
    assert any("case references differ" in error for error in check()[0])


def test_consumer_requirement_cannot_join_active_traceability(documents):
    documents["traceability.md"] += "| HP-REQ-037 | TASK-001 | V25 | consumer |\n"
    errors = check()[0]
    assert any("extra ['HP-REQ-037']" in error for error in errors)
    assert any("case references differ" in error for error in errors)


def test_active_task_cannot_own_consumer_requirement(documents):
    documents["requirements.md"] = documents["requirements.md"].replace(
        "Consumer owner: example project.", "Implementation owner: TASK-001."
    )
    assert any("consumer-owned HP-REQ-037 owned by active TASK-001" in error for error in check()[0])


def test_owner_and_task_coverage_are_required(documents):
    documents["traceability.md"] = "| HP-REQ-001 | TASK-999 | V01,V32 | local |\n"
    errors = check()[0]
    assert any("not all in traceability" in error for error in errors)
    assert any("traced to non-active TASK-999" in error for error in errors)


def test_mandatory_requirement_needs_owner(documents):
    documents["requirements.md"] = documents["requirements.md"].replace(
        "Implementation owner: TASK-001.", ""
    )
    assert any("mandatory requirement has no implementation owner" in error for error in check()[0])


def test_unknown_requirement_in_task_coverage_fails(documents):
    errors = DOCS.check_traceability(
        DOCS.parse_requirements(), {"TASK-001": {"active": True}},
        {"TASK-001": {"HP-REQ-001", "HP-REQ-999"}},
    )[0]
    assert "TASK-001 covers unknown HP-REQ-999" in errors


def test_unknown_requirement_row_reports_error(documents):
    documents["traceability.md"] += "| HP-REQ-999 | TASK-001 | V01 | local |\n"
    assert "HP-REQ-999: unknown traced requirement" in check()[0]


def test_each_requirement_needs_cases_even_when_global_coverage_is_complete(documents):
    documents["requirements.md"] = documents["requirements.md"].replace(
        "### HP-REQ-001", "### HP-REQ-002\nImplementation owner: TASK-001.\n### HP-REQ-001"
    )
    documents["traceability.md"] = (
        "| HP-REQ-001 | TASK-001 |  | local |\n"
        "| HP-REQ-002 | TASK-001 | V01,V32 | local |\n"
    )
    errors = DOCS.check_traceability(
        DOCS.parse_requirements(), {"TASK-001": {"active": True}},
        {"TASK-001": {"HP-REQ-001", "HP-REQ-002"}},
    )[0]
    assert errors == ["HP-REQ-001: mandatory requirement has no verifying case"]


def test_duplicate_traceability_cannot_hide_an_invalid_row(documents):
    documents["traceability.md"] = (
        "| HP-REQ-001 | TASK-999 | V99 | local |\n" + documents["traceability.md"]
    )
    assert "HP-REQ-001: duplicate traceability row" in check()[0]


@pytest.fixture
def qualification_graph(documents):
    documents["roadmap.md"] = """Historical real-UTM qualification tasks: TASK-016.
Current real-VM acceptance tasks: TASK-027.
Current real-VM prerequisite closure: TASK-027, TASK-028.
"""
    return {
        "TASK-016": {"active": True, "status": "Accepted", "prereqs": set()},
        "TASK-017": {"active": True, "status": "Planned", "prereqs": {"TASK-016"}},
        "TASK-027": {"active": True, "status": "Planned", "prereqs": {"TASK-017"}},
        "TASK-028": {"active": True, "status": "Planned", "prereqs": {"TASK-027"}},
    }


def test_historical_vm_evidence_does_not_reopen_local_successors(qualification_graph):
    errors, dag = DOCS.check_dag(qualification_graph)
    assert errors == []
    assert dag["historical_utm_qualification"] == ["TASK-016"]
    assert dag["current_live_vm_gates"] == ["TASK-027"]
    assert dag["utm_gated"] == ["TASK-027", "TASK-028"]


@pytest.mark.parametrize("declared", ["TASK-017, TASK-027, TASK-028", "TASK-027"])
def test_live_closure_rejects_local_blocking_or_missing_release_successor(
    documents, qualification_graph, declared
):
    documents["roadmap.md"] = documents["roadmap.md"].replace(
        "closure: TASK-027, TASK-028.", f"closure: {declared}."
    )
    assert any("DAG closure" in error for error in DOCS.check_dag(qualification_graph)[0])


def test_historical_gate_cannot_be_reintroduced_as_current(documents, qualification_graph):
    documents["roadmap.md"] = documents["roadmap.md"].replace(
        "acceptance tasks: TASK-027.", "acceptance tasks: TASK-016, TASK-027."
    )
    assert "historical qualification cannot also be a current live gate" in DOCS.check_dag(qualification_graph)[0]


@pytest.mark.parametrize("task", ["TASK-029", "TASK-999"])
def test_unknown_or_deferred_current_gate_is_rejected(documents, qualification_graph, task):
    qualification_graph["TASK-029"] = {"active": False, "status": "Deferred", "prereqs": set()}
    documents["roadmap.md"] = documents["roadmap.md"].replace(
        "acceptance tasks: TASK-027.", f"acceptance tasks: {task}."
    )
    assert f"real-VM qualification names non-active {task}" in DOCS.check_dag(qualification_graph)[0]


def test_unaccepted_evidence_cannot_be_called_historical(qualification_graph):
    qualification_graph["TASK-016"]["status"] = "In review"
    assert "historical qualification TASK-016 is not Accepted" in DOCS.check_dag(qualification_graph)[0]
