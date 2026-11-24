# Curriculum Coverage Evaluation

Offline evaluation suite measuring shipped knowledge graphs against versioned, graph-independent course objective manifests.

## Authoritative Edition (`ap-csa-fall-2025`)

- **Framework**: English, Java-based [AP Computer Science A Course and Exam Description](https://apcentral.collegeboard.org/media/pdf/ap-computer-science-a-course-and-exam-description.pdf) effective Fall 2025 (PDF SHA-256 `c81501ad5a022c83f1ca51119cccd1907b1394e1416d8c2a01bd43a38ab16486`).
- **Clarifications**: [AP Computer Science A Clarifications and Corrections](https://apcentral.collegeboard.org/media/pdf/ap-computer-science-a-course-and-exam-description-clarification.pdf), January 2026 PDF revision (PDF SHA-256 `d39626653656bf0baa0127dc8c3fa50c5e7a41bc7b3747e43d997f69a2d18e56`).
- **Denominator**: **73 distinct learning objectives** (`28`, `14`, `10`, and `21` across Units 1 through 4). Continuation-page objectives are counted once, and essential-knowledge items, suggested skills, and exam questions are excluded from this denominator.
- **Copyright & Redistribution**: [manifest.json](editions/ap-csa-fall-2025/manifest.json) records only objective identifiers, units, printed source page citations, and short original summaries; copyrighted question banks and verbatim course tables are excluded.
- **Edition Pinning**: Later framework revisions live in separate directories under `evaluation/curriculum/editions/` so earlier evaluations remain reproducible.

## Shipped Graph Evaluations (Provisional)

| Graph | Digest (SHA-256) | Graph Size | Components | Mapped | Partial | Missing | Unresolved Disputes | Outside Course |
|---|---|---|---:|---:|---:|---:|---:|---:|
| [examples/graphs/computer-science.json](../../examples/graphs/computer-science.json) | `cb6e4fce400c34f6850e813b95f3ba3612db6f5b02bb20fec71e9b3e109651ee` | 130 concepts, 173 tuples (154 `depends_on`, 19 `is_a`) | 1 | 0 | 49 | 24 | 35 | 103 |
| [internal/graph/seed.json](../../internal/graph/seed.json) | `7a9f78f6a80eeb7bc6e21afbf71b735621604615f0faf9c881d600ab5a7d84aa` | 23 concepts, 20 tuples (17 `depends_on`, 3 `is_a`) | 4 | 0 | 24 | 49 | 13 | 15 |

Both graphs remain unmodified. Crosswalk artifacts and deterministic reports live under [editions/ap-csa-fall-2025/](editions/ap-csa-fall-2025/).

## Semantics and Verification Rules

- A `mapped` objective requires cited concepts that support the entire objective and a row free of unresolved or rejected disputes. A `partial` objective requires at least one cited concept that supports part of the objective.
- Both endpoints of an evidence edge must be cited concepts for that objective. An unresolved dispute that names an edge prevents every objective in the crosswalk from using that edge for full coverage. Partial rows can still cite it, and the report records the dispute under its original objective. A rejected edge is excluded from all evidence. A dispute that names no edge applies only to its objective. Independent concepts can still support partial coverage.
- **Separation of `depends_on` and `is_a`**: Even though runtime prerequisite traversal walks both edge types, the evaluator counts `depends_on` and `is_a` separately and rejects marking an objective `mapped` on taxonomic `is_a` evidence alone.
- **Isolated objectives**: Standalone topics that cannot be expressed in edge-only `(subject, relation, object)` tuples without inventing edges remain in the denominator as `missing`.
- **Canonical alias & integrity checks**: Concept and edge evidence resolves through `graph.Canonical` / `InMemoryGraph.Resolve`. Evaluation fails on duplicate or absent objectives, unknown IDs, nonexistent nodes, alias collisions, nonexistent edges, graph or source SHA-256 mismatches, or unaccounted graph nodes.
- All reports show that independent subject-specialist verification is pending. The [dispute form](dispute-form.md) records the reviewer’s qualifications, source evidence, and decision. The input `provisional` flag and dispute counts cannot attest to specialist sign-off.

## Running Checks

```sh
go test -race -count=1 ./evaluation/curriculum/... ./internal/graph
go run ./evaluation/curriculum/cmd/coverage-report -format json
go run ./evaluation/curriculum/cmd/coverage-report -format markdown
```
