// Package graph implements the in-memory knowledge-graph layer for the
// AxiomEngine. It stores curriculum prerequisite relationships as directed
// GraphTuple triples and provides algorithms for subgraph extraction (BFS),
// topological prerequisite ordering (DFS), and cognitive shadow graph
// generation for teacher-dashboard visualization.
//
// The graph is safe for concurrent reads but is NOT safe for concurrent
// writes. In production, it is populated once at startup and treated as
// immutable thereafter.
package graph

import "axiom-reasoning-svc/internal/domain"

// bfsEntry is an internal queue element used by the breadth-first subgraph
// extraction algorithm. It pairs a node name with the current traversal depth
// to enforce depth-limited exploration.
type bfsEntry struct {
	node  string
	depth int
}

// InMemoryGraph is a bidirectional adjacency-list representation of a
// curriculum knowledge graph. It maintains three indexes:
//   - forward: subject -> []GraphTuple (outgoing edges)
//   - reverse: object  -> []GraphTuple (incoming edges)
//   - nodes:   set of all unique node names
//
// All map capacities are pre-allocated at construction time to minimize
// runtime allocations during traversal.
type InMemoryGraph struct {
	forward map[string][]domain.GraphTuple // subject → tuples
	reverse map[string][]domain.GraphTuple // object  → tuples
	nodes   map[string]bool                // all unique node names
	all     []domain.GraphTuple
}

// NewInMemoryGraph constructs an InMemoryGraph pre-populated with the
// default computer science curriculum tuples returned by defaultTuples().
// All internal maps are pre-sized based on the tuple count.
func NewInMemoryGraph() *InMemoryGraph {
	tuples := defaultTuples()
	g := &InMemoryGraph{
		forward: make(map[string][]domain.GraphTuple, len(tuples)),
		reverse: make(map[string][]domain.GraphTuple, len(tuples)),
		nodes:   make(map[string]bool, len(tuples)*2),
		all:     make([]domain.GraphTuple, 0, len(tuples)),
	}
	for _, t := range tuples {
		g.Add(t)
	}
	return g
}

// Add inserts a GraphTuple into the forward index (keyed by Subject),
// the reverse index (keyed by Object), the global node set, and the
// ordered list of all tuples.
func (g *InMemoryGraph) Add(t domain.GraphTuple) {
	g.forward[t.Subject] = append(g.forward[t.Subject], t)
	g.reverse[t.Object] = append(g.reverse[t.Object], t)
	g.nodes[t.Subject] = true
	g.nodes[t.Object] = true
	g.all = append(g.all, t)
}

// All returns a defensive copy of every GraphTuple in the graph.
// The returned slice is safe to mutate without affecting the graph.
func (g *InMemoryGraph) All() []domain.GraphTuple {
	out := make([]domain.GraphTuple, len(g.all))
	copy(out, g.all)
	return out
}

// NodeExists returns true if the given name appears as either a Subject
// or Object in any tuple. This is an O(1) lookup against the internal
// node set and is used by the hallucination-guard validation stage.
func (g *InMemoryGraph) NodeExists(name string) bool {
	return g.nodes[name]
}

// GetPrerequisiteChain performs a depth-first post-order traversal of the
// forward adjacency list starting from the given topic, following only
// "depends_on" and "is_a" edges. It returns the prerequisite nodes in
// topological order (deepest prerequisite first, immediate prerequisite last).
// The topic itself is excluded from the returned slice.
//
// If the topic does not exist in the graph or has no outgoing prerequisite
// edges, an empty slice is returned. Cycles are implicitly handled by the
// visited set.
func (g *InMemoryGraph) GetPrerequisiteChain(topic string) []string {
	visited := make(map[string]bool, 8)
	var chain []string

	var dfs func(node string)
	dfs = func(node string) {
		if visited[node] {
			return
		}
		visited[node] = true
		for _, t := range g.forward[node] {
			if t.Relation == "depends_on" || t.Relation == "is_a" {
				dfs(t.Object)
			}
		}
		// Post-order: append after visiting all dependencies
		chain = append(chain, node)
	}

	dfs(topic)

	// chain is in post-order (roots first, topic last). Remove the topic itself.
	if len(chain) > 0 && chain[len(chain)-1] == topic {
		chain = chain[:len(chain)-1]
	}
	return chain
}

// GetLocalSubgraph performs a depth-limited breadth-first traversal starting
// from each topic in failedTopics. It walks both forward (subject->object)
// and reverse (object->subject) edges, collecting all unique tuples reachable
// within the specified depth. This bidirectional traversal ensures that both
// prerequisites and dependent concepts are captured in the returned subgraph.
//
// Implementation optimizations:
//   - All maps are pre-sized using a heuristic of len(topics) * (depth+1) * 4.
//   - The BFS queue uses a head-pointer pattern instead of slice re-slicing
//     to avoid O(n) memory copies on each dequeue operation.
//   - Tuples are deduplicated by a [3]string composite key.
func (g *InMemoryGraph) GetLocalSubgraph(failedTopics []string, depth int) []domain.GraphTuple {
	estimatedNodes := len(failedTopics) * (depth + 1) * 4
	visited := make(map[string]bool, estimatedNodes)
	seen := make(map[[3]string]bool, estimatedNodes)
	result := make([]domain.GraphTuple, 0, estimatedNodes)

	queue := make([]bfsEntry, 0, estimatedNodes)
	for _, topic := range failedTopics {
		queue = append(queue, bfsEntry{node: topic, depth: 0})
		visited[topic] = true
	}

	head := 0
	for head < len(queue) {
		cur := queue[head]
		head++

		// Collect tuples where cur.node is the subject
		for _, t := range g.forward[cur.node] {
			key := [3]string{t.Subject, t.Relation, t.Object}
			if !seen[key] {
				seen[key] = true
				result = append(result, t)
			}
			if cur.depth < depth && !visited[t.Object] {
				visited[t.Object] = true
				queue = append(queue, bfsEntry{node: t.Object, depth: cur.depth + 1})
			}
		}

		// Collect tuples where cur.node is the object (reverse edges)
		for _, t := range g.reverse[cur.node] {
			key := [3]string{t.Subject, t.Relation, t.Object}
			if !seen[key] {
				seen[key] = true
				result = append(result, t)
			}
			if cur.depth < depth && !visited[t.Subject] {
				visited[t.Subject] = true
				queue = append(queue, bfsEntry{node: t.Subject, depth: cur.depth + 1})
			}
		}
	}

	return result
}

// GenerateCognitiveShadow constructs a complete Cognitive Shadow Graph for
// teacher-dashboard visualization. It iterates over every node in the
// knowledge graph and classifies each into one of three cognitive states:
//
//   - "failed":   the node name appears in the failedTopics input.
//   - "learning": the node is a transitive prerequisite of at least one
//     failed topic (determined via GetPrerequisiteChain) but is not itself
//     in the failed set.
//   - "mastered": all remaining nodes for which no deficit evidence exists.
//
// The returned CognitiveGraphResponse contains the full set of VisualNodes
// and VisualEdges, making it directly consumable by Cytoscape.js or D3.js
// frontend renderers.
func (g *InMemoryGraph) GenerateCognitiveShadow(studentID string, failedTopics []string) domain.CognitiveGraphResponse {
	failedSet := make(map[string]bool, len(failedTopics))
	for _, t := range failedTopics {
		failedSet[t] = true
	}

	// Collect all prerequisite nodes of failed topics → "learning"
	learningSet := make(map[string]bool, len(failedTopics)*4)
	for _, topic := range failedTopics {
		for _, prereq := range g.GetPrerequisiteChain(topic) {
			if !failedSet[prereq] {
				learningSet[prereq] = true
			}
		}
	}

	// Build nodes from the full node set
	nodes := make([]domain.VisualNode, 0, len(g.nodes))
	for name := range g.nodes {
		status := "mastered"
		if failedSet[name] {
			status = "failed"
		} else if learningSet[name] {
			status = "learning"
		}
		nodes = append(nodes, domain.VisualNode{
			ID:     name,
			Label:  name,
			Status: status,
		})
	}

	// Build edges from all tuples
	edges := make([]domain.VisualEdge, len(g.all))
	for i, t := range g.all {
		edges[i] = domain.VisualEdge{
			Source:   t.Subject,
			Target:   t.Object,
			Relation: t.Relation,
		}
	}

	return domain.CognitiveGraphResponse{
		StudentID: studentID,
		Nodes:     nodes,
		Edges:     edges,
	}
}

// defaultTuples returns the seed dataset of 20 computer science curriculum
// triples used to bootstrap the in-memory knowledge graph. Each triple
// represents a directed prerequisite or taxonomy relationship between
// two CS concepts.
func defaultTuples() []domain.GraphTuple {
	return []domain.GraphTuple{
		{Subject: "Polymorphism", Relation: "depends_on", Object: "Inheritance"},
		{Subject: "Inheritance", Relation: "depends_on", Object: "Classes"},
		{Subject: "Classes", Relation: "depends_on", Object: "Objects"},
		{Subject: "Objects", Relation: "depends_on", Object: "Variables"},
		{Subject: "Interfaces", Relation: "depends_on", Object: "Abstract Classes"},
		{Subject: "Abstract Classes", Relation: "is_a", Object: "Classes"},
		{Subject: "Generics", Relation: "depends_on", Object: "Polymorphism"},
		{Subject: "Design Patterns", Relation: "depends_on", Object: "Polymorphism"},
		{Subject: "Design Patterns", Relation: "depends_on", Object: "Interfaces"},
		{Subject: "Binary Search", Relation: "depends_on", Object: "Arrays"},
		{Subject: "Merge Sort", Relation: "depends_on", Object: "Recursion"},
		{Subject: "Recursion", Relation: "depends_on", Object: "Functions"},
		{Subject: "Dynamic Programming", Relation: "depends_on", Object: "Recursion"},
		{Subject: "Graph Traversal", Relation: "depends_on", Object: "Graphs"},
		{Subject: "Graphs", Relation: "depends_on", Object: "Linked Lists"},
		{Subject: "Linked Lists", Relation: "depends_on", Object: "Pointers"},
		{Subject: "Hash Tables", Relation: "depends_on", Object: "Arrays"},
		{Subject: "Trees", Relation: "depends_on", Object: "Linked Lists"},
		{Subject: "Binary Search Trees", Relation: "is_a", Object: "Trees"},
		{Subject: "AVL Trees", Relation: "is_a", Object: "Binary Search Trees"},
	}
}
