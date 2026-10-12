# Active requirement traceability

Design Revision 3.4. This table is bidirectional: search by requirement, implementation Task, or V-case. Cases are planned, not executed. A consumer-owned evidence class means Haepari checks declared result completeness and provides a sample recipe; the consumer owns product assertions. The detailed case assertions live in [verification](verification.md).

| Requirement | Implementation Task | Verifying cases | Evidence class |
| --- | --- | --- | --- |
| HP-REQ-001 | TASK-001 | V37 | fixture/unit + local integration |
| HP-REQ-002 | TASK-001 | V37,V43,V47 | local integration + real UTM |
| HP-REQ-003 | TASK-002 | V04,V07,V51,V58 | fixture/unit + local integration |
| HP-REQ-004 | TASK-003 | V06,V07,V52 | fixture/unit + local integration |
| HP-REQ-005 | TASK-003 | V06 | fixture/unit + local integration |
| HP-REQ-006 | TASK-004, TASK-034 | V13,V46,V47,V56 | macOS supervision + real UTM probe |
| HP-REQ-007 | TASK-005, TASK-033 (initial slice) | V01,V03,V37,V56 | fixture/unit + local integration + macOS supervision |
| HP-REQ-008 | TASK-005 | V01 | fixture/unit + local integration |
| HP-REQ-009 | TASK-006, TASK-033 (initial slice), TASK-039 | V03,V37,V52 | fixture/unit + local integration |
| HP-REQ-010 | TASK-006, TASK-033 (initial slice), TASK-039 | V02,V04,V05,V37,V54 | fixture/unit + local integration |
| HP-REQ-011 | TASK-007, TASK-033 (initial slice), TASK-037, TASK-038, TASK-039 | V08,V09,V38,V39,V41,V45,V49 | fixture/unit + local integration |
| HP-REQ-012 | TASK-007, TASK-033 (initial slice) | V08,V21,V51 | fixture/unit + local integration |
| HP-REQ-013 | TASK-007 | V22,V42,V49 | fixture/unit + local integration |
| HP-REQ-014 | TASK-008, TASK-033 (initial slice), TASK-039 | V12,V37 | fixture/unit + local integration |
| HP-REQ-015 | TASK-008, TASK-033 (initial slice), TASK-039 | V20,V23,V37 | fixture/unit + local integration |
| HP-REQ-016 | TASK-009, TASK-033 (initial slice) | V10,V11,V13,V46 | macOS supervision + fault injection |
| HP-REQ-017 | TASK-009 | V44 | fixture/unit + local integration |
| HP-REQ-018 | TASK-010, TASK-036, TASK-038, TASK-039 | V14,V19,V20,V21,V51,V53 | fixture/unit + local integration |
| HP-REQ-019 | TASK-010, TASK-036, TASK-037, TASK-038, TASK-039 | V40,V53 | fixture/unit + local integration |
| HP-REQ-020 | TASK-011, TASK-036, TASK-038 | V10,V11,V42,V46 | macOS supervision + fault injection |
| HP-REQ-021 | TASK-011, TASK-036, TASK-038, TASK-039 | V11,V42 | macOS supervision + fault injection |
| HP-REQ-022 | TASK-012, TASK-036, TASK-037, TASK-038, TASK-039 | V20,V21,V53 | fixture/unit + local integration |
| HP-REQ-023 | TASK-012, TASK-037, TASK-038 | V05,V20,V53 | fixture/unit + local integration |
| HP-REQ-024 | TASK-013, TASK-036, TASK-037, TASK-038 | V17,V18,V49,V50,V58 | real UTM + fixture |
| HP-REQ-025 | TASK-013, TASK-036, TASK-038 | V18,V21,V49 | real UTM + fixture |
| HP-REQ-026 | TASK-014, TASK-034 (probe), TASK-037, TASK-038 | V15,V17,V47,V58 | real UTM + fixture |
| HP-REQ-027 | TASK-014, TASK-036, TASK-037, TASK-038 | V15,V16 | real UTM + fixture |
| HP-REQ-028 | TASK-014, TASK-036, TASK-038 | V17 | real UTM + fixture |
| HP-REQ-029 | TASK-015, TASK-036, TASK-038 | V19,V20,V21,V49,V51,V53,V58 | real UTM + fixture |
| HP-REQ-030 | TASK-015, TASK-036, TASK-038 | V07,V17 | real UTM + fixture |
| HP-REQ-031 | TASK-016, TASK-038 | V15,V17,V21,V47,V65 | real UTM + fixture |
| HP-REQ-032 | TASK-017, TASK-039 | V25,V36 | real UTM + fixture |
| HP-REQ-033 | TASK-017 | V29,V36 | real UTM + fixture |
| HP-REQ-034 | TASK-017, TASK-039 | V36 | fixture/unit + local integration |
| HP-REQ-035 | TASK-018, TASK-039 | V35,V54 | fixture + local integration |
| HP-REQ-036 | TASK-018 | V25,V28 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-037 | TASK-018 | V25,V26 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-038 | TASK-018 | V26,V28 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-039 | TASK-019 | V25,V27 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-040 | TASK-019 | V23,V24 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-041 | TASK-019 | V23,V34 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-042 | TASK-020 | V29 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-043 | TASK-020 | V29,V31 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-044 | TASK-020 | V30 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-045 | TASK-021 | V01,V02,V10,V11,V21,V38,V39 | fixture/unit + local integration |
| HP-REQ-046 | TASK-021 | V14 | fixture/unit + local integration |
| HP-REQ-047 | TASK-022 | V07,V17,V48 | fixture/unit + local integration |
| HP-REQ-048 | TASK-022 | V34 | fixture/unit + local integration |
| HP-REQ-049 | TASK-022 | V07,V34 | fixture/unit + local integration |
| HP-REQ-050 | TASK-023, TASK-037, TASK-038 | V20 | fixture/unit + local integration |
| HP-REQ-051 | TASK-023, TASK-037, TASK-038 | V32,V42 | fixture/unit + local integration |
| HP-REQ-052 | TASK-024 | V33 | isolated storage fixture |
| HP-REQ-053 | TASK-024 | V32 | isolated storage fixture |
| HP-REQ-054 | TASK-025 | V27,V37 | installed release/skill observation |
| HP-REQ-055 | TASK-025 | V37,V55 | installed release/skill observation |
| HP-REQ-056 | TASK-026 | V18,V21,V42 | real UTM + fixture |
| HP-REQ-057 | TASK-026 | V25,V29 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-058 | TASK-027 | V25,V27,V31 | consumer-owned recipe/observations + Haepari completeness fixture |
| HP-REQ-059 | TASK-027, TASK-038, TASK-039 | V08,V09,V37 | real UTM + fixture |
| HP-REQ-060 | TASK-028 | V08,V17,V21 | real UTM + fixture |
| HP-REQ-061 | TASK-028 | V32,V57 | installed release observation + fixture |
| HP-REQ-062 | TASK-002, TASK-012 (F1), TASK-016 (F2), TASK-020 (F3), TASK-036, TASK-037, TASK-038, TASK-039 | V43 | fixture/unit + local integration |
| HP-REQ-080 | TASK-035 | V59,V60,V61,V63,V64 | fixture/unit + local integration |
| HP-REQ-081 | TASK-035 | V62 | fixture/unit + local integration |

## Initial-slice and probe contributions

TASK-033 implements the first slice of HP-REQ-007, 009, 010, 011, 012, 014, 015 and 016; the owning tasks harden the same path. TASK-034 contributes the UTM probe observations to HP-REQ-006 and HP-REQ-026. Freeze gates F1 (TASK-012), F2 (TASK-016) and F3 (TASK-020) complete HP-REQ-062.

For requirements owned by TASK-013 through TASK-015 and TASK-017 through TASK-020, focused implementation checks use local resources or the fake UTM/SSH backend. TASK-016 retains historical backend qualification; TASK-038 adds corrected-candidate operational qualification, TASK-039 adds a small isolated native caller-e2e cycle under the 2026-10-12 scope revision, and TASK-027 qualifies the exact release. TASK-036–039 are corrective contributions, not replacements for the original owners. TASK-039 local observations do not claim the real-VM or full consumer integration coverage owned by TASK-038 and TASK-017–020. The [dossier campaign](TODO-VM-OPERATIONAL-READINESS.md#campaign-and-acceptance-matrix) extends the listed V-case assertions with task-specific scenarios without claiming them executed.

## Deferred traceability

HP-REQ-063–079 map to EPIC-008/TASK-029–032 (except their retained R2 cross-cutting owner references), and E01–E20 remain deferred design cases. They contribute zero to active requirement, task and case acceptance counts. B1/B2 must close and this mapping must be re-reviewed before implementation. No active Task has an EPIC-008 prerequisite.
