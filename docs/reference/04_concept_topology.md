# 04 – Concept Topology

## Overview

The concept topology is an in-memory directed graph where each node is a curriculum concept name (string) and each directed edge is a labeled prerequisite or taxonomy relationship. The graph is the data substrate for all reasoning operations in AxiomEngine: BFS subgraph extraction, topological sort, hallucination validation, and cognitive shadow graph generation.

The graph is represented by `InMemoryGraph` (`internal/graph/memory.go`) using three internal indexes:

| Index | Type | Key | Value |
|-------|------|-----|-------|
| `forward` | `map[string][]domain.GraphTuple` | Subject node name | All outgoing edges from that subject |
| `reverse` | `map[string][]domain.GraphTuple` | Object node name | All incoming edges into that object |
| `nodes` | `map[string]bool` | Node name | Presence set for O(1) existence checks |

The graph is safe for concurrent reads. It is populated once at startup and treated as immutable thereafter.

## Data Source

The topology is loaded entirely from a hardcoded seed function. There is no file read, database query, or environment variable involved.

**Loading mechanism** (`internal/graph/memory.go`):

1. `cmd/server/main.go:main` calls `graph.NewInMemoryGraph()`.
2. `NewInMemoryGraph` calls `defaultTuples()`, which returns a literal slice of 20 `domain.GraphTuple` values.
3. Each tuple is inserted into the three indexes via `InMemoryGraph.Add`.
4. The server logs `graph.loaded tuples=20` on startup.

To extend the topology, add entries to `defaultTuples()` or call `g.Add(t)` before the server starts listening.

## Graph Operations

| Operation | Description | Algorithm | Complexity |
|-----------|-------------|-----------|------------|
| `Add` | Insert a GraphTuple into forward index, reverse index, and node set | Direct map append | O(1) |
| `NodeExists` | Check whether a concept name is present in the graph | Hash map lookup on `nodes` | O(1) |
| `All` | Return a defensive copy of all tuples in insertion order | `copy` to new slice | O(n) |
| `GetLocalSubgraph` | Bidirectional BFS from a set of seed topics up to a given depth | BFS with head-pointer dequeue, composite-key dedup via `[3]string` map | O(V + E) bounded by `depth` |
| `GetPrerequisiteChain` | Ordered prerequisite path for a single topic following `depends_on` and `is_a` edges | DFS post-order traversal with visited set | O(V + E) |
| `GenerateCognitiveShadow` | Classify every node as `failed`, `learning`, or `mastered` for a given student | Iterate node set; classify via two hash sets (failedSet, learningSet) | O(V + E) |

## Concept ID Format

Concept IDs are human-readable multi-word strings using title-case capitalization with spaces. They are not slugs or UUIDs.

**Examples from `defaultTuples()` in `internal/graph/memory.go`**:

```
"Polymorphism"
"Inheritance"
"Classes"
"Objects"
"Variables"
"Interfaces"
"Abstract Classes"
"Generics"
"Design Patterns"
"Binary Search"
"Merge Sort"
"Recursion"
"Dynamic Programming"
"Graph Traversal"
"Graphs"
"Linked Lists"
"Pointers"
"Hash Tables"
"Trees"
"Functions"
"Binary Search Trees"
"AVL Trees"
"Arrays"
```

The `failed_topics` field in `AdaptivePlanRequest` must use these exact strings (case-sensitive) for the graph lookup to return any results. The `topics` query parameter in the cognitive graph endpoint follows the same constraint.

## Seed Triples

The 20 triples loaded by `defaultTuples()` form a directed acyclic graph covering OOP concepts, sorting algorithms, data structures, and recursion:

| Subject | Relation | Object |
|---------|----------|--------|
| Polymorphism | depends_on | Inheritance |
| Inheritance | depends_on | Classes |
| Classes | depends_on | Objects |
| Objects | depends_on | Variables |
| Interfaces | depends_on | Abstract Classes |
| Abstract Classes | is_a | Classes |
| Generics | depends_on | Polymorphism |
| Design Patterns | depends_on | Polymorphism |
| Design Patterns | depends_on | Interfaces |
| Binary Search | depends_on | Arrays |
| Merge Sort | depends_on | Recursion |
| Recursion | depends_on | Functions |
| Dynamic Programming | depends_on | Recursion |
| Graph Traversal | depends_on | Graphs |
| Graphs | depends_on | Linked Lists |
| Linked Lists | depends_on | Pointers |
| Hash Tables | depends_on | Arrays |
| Trees | depends_on | Linked Lists |
| Binary Search Trees | is_a | Trees |
| AVL Trees | is_a | Binary Search Trees |
