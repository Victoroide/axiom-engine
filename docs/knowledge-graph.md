# Knowledge Graph

AxiomEngine models curriculum prerequisites using an in-memory directed graph. The graph governs local context extraction during adaptive plan generation, topological prerequisite ordering, and student cognitive state classification.

---

## Tuple Model

The knowledge graph models domain relationships as directed Subject-Relation-Object (SRO) triples represented by `domain.GraphTuple`:

```json
{
  "subject": "Polymorphism",
  "relation": "depends_on",
  "object": "Inheritance"
}
```

A directed edge originates at `subject` and terminates at `object`, labeled by `relation`.

### In-Memory Representation

The graph is loaded into `graph.InMemoryGraph` (`internal/graph/memory.go`), which indexes tuples across five structures:
- `forward: map[string][]domain.GraphTuple`: Outgoing edges indexed by `subject`.
- `reverse: map[string][]domain.GraphTuple`: Incoming edges indexed by `object`.
- `nodes: map[string]bool`: Complete set of all unique concept names.
- `canonical: map[string]string`: Canonical representation (`internal/graph/canonical.go`) mapped to authoritative stored node name.
- `all: []domain.GraphTuple`: Slice containing every unique tuple.

Once loaded during process startup, `InMemoryGraph` is immutable. Read operations (BFS subgraph extraction, DFS prerequisite traversal, and existence checks) run concurrently without mutex contention.

---

## Relations and Semantics

The graph supports two edge relations:

| Relation | Semantic Meaning | Prerequisite Behavior |
|---|---|---|
| `depends_on` | Strict conceptual prerequisite. The `subject` cannot be understood without prior knowledge of the `object`. | Traversed by `GetPrerequisiteChain` as an upstream dependency. |
| `is_a` | Taxonomic specialization. The `subject` is a specific subtype or implementation of the `object`. | Traversed by `GetPrerequisiteChain` identically to `depends_on`, ensuring foundational abstractions precede specializations. |

---

## Node Naming

Concept node names in the knowledge graph are indexed canonically to support matching across casing, spacing, and Unicode representation differences.

After outer whitespace is trimmed, subject and object node names must be non-empty and at most 128 Unicode runes. Node names cannot contain commas, Unicode control characters (`unicode.Cc`), format characters (`unicode.Cf`), or the Unicode replacement character U+FFFD. Commas cannot be addressed as a single name because the cognitive-graph `topics` query parameter splits values on commas. Printable Unicode such as `Recursión` and `Straße` remains supported. Invalid UTF-8 sequences are rejected because `encoding/json` replaces invalid bytes with U+FFFD. Canonical request matching remains unchanged.

Canonical normalization (`internal/graph/canonical.go`) applies:
1. Removal of Unicode format characters (`unicode.Cf`).
2. Whitespace collapsing and trimming via `strings.Fields`.
3. Unicode case folding via `cases.Fold()`.
4. Unicode Normalization Form C (`norm.NFC`).

Distinct stored node names cannot share the same canonical form. During graph loading, the loader rejects any collision where two distinct node names produce an identical canonical key with `tuple %d has canonical node name collision: %q and %q`.

Original spellings, casing, and spacing of stored graph nodes are preserved in memory and returned in API responses.

---

## Querying Topics and Prerequisite Paths

`InMemoryGraph` provides methods for inspecting topics, direct neighbor relationships, and topological study paths:

- `Topics(filter string) []domain.Topic`: Returns all curriculum concepts sorted alphabetically by stored name. When `filter` is non-empty, matches topics whose canonical name contains the canonical filter string (`strings.Contains(Canonical(name), Canonical(filter))`). Each `domain.Topic` entry contains the stored `Name`, direct `Prerequisites`, and direct `Dependents`. If no concepts match, returns an empty slice.
- `Prerequisites(topic string) []string`: Returns a sorted, deduplicated slice of direct prerequisites for a concept, following outgoing edges from `forward` across both `depends_on` and `is_a` relations.
- `Dependents(topic string) []string`: Returns a sorted, deduplicated slice of direct dependents for a concept, following incoming edges from `reverse` across both `depends_on` and `is_a` relations.
- `Path(topic string) (domain.TopicPathResponse, bool)`: Resolves `topic` against canonical node names. If found, returns a `domain.TopicPathResponse` with `Topic` set to the stored name, `PrerequisiteChain` containing all transitive prerequisites in topological study order (excluding the requested concept itself), and `Dependents` containing direct dependents. Returns `false` if the concept is not found in the graph.

All querying methods return newly allocated defensive copies of topic and relationship slices, ensuring concurrent callers cannot mutate internal graph indices.

---

## Seed Graph Summary

When `GRAPH_FILE` is unset, AxiomEngine loads an embedded seed graph containing 20 triples across 23 computer science concepts defined in [internal/graph/seed.json](../internal/graph/seed.json).

### Seed Graph Topology

Each arrow points from a topic to its prerequisite. Solid arrows are `depends_on`; dashed arrows are `is_a`.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/diagrams/seed-graph-dark.webp">
  <img src="assets/diagrams/seed-graph-light.webp" width="100%" alt="Seed graph of 23 topics in four groups. Object-oriented: Generics and Design Patterns depend on Polymorphism, which depends on Inheritance, Classes, Objects and Variables; Design Patterns also depends on Interfaces, which depends on Abstract Classes, an is_a of Classes. Algorithms: Dynamic Programming and Merge Sort depend on Recursion, which depends on Functions. Linear and hashing: Binary Search and Hash Tables depend on Arrays. Graphs and trees: Graph Traversal depends on Graphs, Linked Lists and Pointers; AVL Trees is_a Binary Search Trees is_a Trees, which depends on Linked Lists.">
</picture>

---

## GRAPH_FILE Format and Validation

Custom curricula can be loaded by setting the `GRAPH_FILE` environment variable to the path of a JSON file.

### Format Example

```json
[
  {
    "subject": "Merge Sort",
    "relation": "depends_on",
    "object": "Recursion"
  },
  {
    "subject": "Recursion",
    "relation": "depends_on",
    "object": "Functions"
  },
  {
    "subject": "Binary Search Trees",
    "relation": "is_a",
    "object": "Trees"
  }
]
```

### Validation Rules

The graph loader (`Decode` in `internal/graph/loader.go`) enforces the following validation checks at startup:

1. **Strict JSON Array Structure**: The payload must consist of a single top-level JSON array. Extraneous keys or multiple JSON documents trigger `graph must contain exactly one JSON array`.
2. **Disallowed Unknown Fields**: JSON objects containing keys other than `subject`, `relation`, or `object` are rejected.
3. **Non-Empty Content**: The file must contain at least one tuple (`graph must contain at least one tuple`).
4. **Non-Empty Fields**: After whitespace trimming, `subject`, `relation`, and `object` must all be non-empty strings (`tuple %d has an empty field`).
5. **Permitted Relations**: The `relation` field must strictly match either `"depends_on"` or `"is_a"` (`tuple %d has unsupported relation %q`).
6. **No Self Loops**: A tuple cannot reference the same concept as both subject and object. Violations trigger `tuple %d is a self loop`.
7. **Deduplication**: Repeated identical triples are deduplicated during decoding.
8. **Acyclic Prerequisite Structure**: Both `depends_on` and `is_a` edges must form a directed acyclic graph (DAG). Cyclic prerequisite dependencies are rejected at startup with `graph contains a prerequisite cycle: <path>` (for example, `graph contains a prerequisite cycle: B -> C -> B`). Traversal evaluates starting nodes in sorted order and rotates the cycle to its smallest node to report the path deterministically. Diagnostics show at most 16 nodes and truncate node names exceeding 64 runes with an ellipsis, reporting total node count when nodes are omitted.
9. **Canonical Node Name Uniqueness**: Distinct node names may not share the same canonical form. Canonical collisions are rejected at startup (see [Node Naming](#node-naming)).
10. **Addressable Node Names**: After outer whitespace is trimmed, subject and object node names must be at most 128 Unicode runes and must not contain commas, Unicode control characters (`unicode.Cc`), format characters (`unicode.Cf`), or the replacement character U+FFFD (including invalid UTF-8 sequences). Violations fail startup validation reporting the tuple index, field (`subject` or `object`), a quoted prefix of at most 64 runes, and the violated rule (`tuple %d subject %.64q contains a comma`, `contains a control character`, `contains a format character`, `contains a replacement character`, or `exceeds 128 runes`).

---

## Cognitive Shadow States

The `GET /api/v1/tutor/student/:student_id/cognitive-graph` endpoint evaluates a student's concept deficits against the knowledge graph to produce a filtered Cognitive Shadow Graph.

### State Definitions

| State | Condition | Meaning |
|---|---|---|
| `failed` | Concept is present in the `topics` query parameter. | Direct evidence of assessment failure. |
| `learning` | Concept is an upstream prerequisite of a failed concept (computed via `GetPrerequisiteChain`), but was not explicitly failed. | Latent prerequisite deficit requiring reinforcement. |
| `mastered` | Concept is present in the filtered connected subgraph with no failed assessment record or prerequisite deficit. | Presumed mastered or supporting concept. |

### Computation Algorithm

The shadow graph is generated by `GenerateCognitiveShadow` in `internal/graph/memory.go` through the following procedure:

1. **Populate Deficit Sets**:
   - `failedSet`: Hash set of all topics in `topics`.
   - `learningSet`: For each topic in `topics`, perform a post-order depth-first traversal (`GetPrerequisiteChain`) following outgoing `depends_on` and `is_a` edges. Add all discovered prerequisites that are not in `failedSet` to `learningSet`.
2. **Seed Selection**:
   - If `target_topic` is specified in the query parameters, set the BFS traversal seed to `[target_topic]`.
   - If `target_topic` is omitted, set the BFS traversal seeds to all `topics`.
3. **Depth-1 Local Subgraph Extraction**:
   - Run a breadth-first search (`GetLocalSubgraph(seeds, 1)`) following both forward and reverse edges to collect all direct relationships.
4. **Orphan Node Pruning**:
   - Only concepts that appear as either `source` or `target` in at least one edge in the depth-1 subgraph are retained in the `nodes` output. Disconnected concepts are pruned.
5. **State Assignment**:
   - For each connected node, determine `cognitive_state`:
     - If in `failedSet` -> `"failed"`.
     - Else if in `learningSet` -> `"learning"`.
     - Else -> `"mastered"`.
6. **Deterministic Output Sorting**:
   - Sort `nodes` alphabetically by `id`.
   - Sort `edges` lexicographically by `source`, `target`, and `relation`.
