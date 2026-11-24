# Curriculum Coverage Dispute Review Form

Status: **Pending independent subject-specialist verification.**

Actual expert confirmation and any claim of verified instructional coverage, course alignment, exam readiness, or learning outcomes must wait until an independent subject specialist completes this form with cited primary evidence. Disputed edges and taxonomic (`is_a`) links do not count as verified instructional prerequisites until confirmed here.

While an edge dispute is open, any objective in the crosswalk can show that edge as partial evidence. Full coverage requires independent evidence, and any open dispute on the objective itself must be resolved. An open dispute that names only an objective applies to that objective.

---

## 1. Independent Subject Expertise

- **Reviewer Name:**
- **Role / Affiliation:**
- **AP Computer Science A / Java Instructional Qualifications:**
- **Independence Declaration:**
  - [ ] Reviewer is independent of graph authoring and implementation and has no conflict of interest in the coverage outcome.

## 2. Target Artifacts Reviewed

- **Course Framework Edition:** (for example, `ap-csa-fall-2025`)
- **Course Description PDF SHA-256 Verified:**
- **Clarifications PDF SHA-256 Verified:**
- **Knowledge Graph File Path:**
- **Knowledge Graph SHA-256 Digest:**
- **Crosswalk File Path:**
- **Dispute ID(s) / Objective ID(s) / Edge(s) Under Review:**

## 3. Source Evidence

Cite primary framework evidence (objective identifiers, printed page numbers, essential knowledge numbers, exclusion statements, and Java language semantics without copying copyrighted exam tables or question banks verbatim):

- **Primary Framework Citations:**
- **Java / Curriculum Ordering Citations:**
- **Observed Graph Evidence (`depends_on` vs `is_a`):**

## 4. Conclusion

Select one conclusion per disputed objective or edge (do not pre-confirm before review):

- [ ] **Confirmed Prerequisite (`depends_on`)** — Graph edge accurately represents required instructional ordering for the cited objective.
- [ ] **Taxonomic Only (`is_a` — Not Instructional Prerequisite)** — Edge represents type specialization rather than instructional prerequisite ordering; keep excluded from prerequisite proof.
- [ ] **Rejected / Disputed Prerequisite** — A rejected edge is excluded from evidence for every objective in the course crosswalk. If the rejection names only an objective, it applies to that objective. Use independent evidence to substantiate partial coverage.
- [ ] **Partial Coverage Only** — Concepts cover part of the objective but omit required Java semantics, APIs, or scope boundaries.
- [ ] **Missing / Isolated Objective** — Objective is absent from the graph or cannot be expressed in edge-only tuples without inventing an edge.
- [ ] **Deferred / Insufficient Evidence** — Additional curriculum documentation or domain review required.

## 5. Rationale and Required Follow-Up

- **Rationale:**
- **Conditions Required Before Any Claim of Verified Instructional Coverage:**
- **Recommended Graph or Crosswalk Change (if any):**

## 6. Sign-off

- **Verification State:** Pending independent expert review
- **Date:**
- **Reviewer Signature / Identifier:**
