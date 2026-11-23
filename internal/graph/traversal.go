package graph

type visitState uint8

const (
	unvisited visitState = iota
	visiting
	finished
)

type prerequisiteFrame struct {
	name string
	next int
}

// prerequisiteWalk visits outgoing edges in tuple order. The stack keeps the
// current path and the next edge to visit for each node on it.
type prerequisiteWalk struct {
	state map[string]visitState
	stack []prerequisiteFrame
}

func prerequisiteRelation(relation string) bool {
	return relation == "depends_on" || relation == "is_a"
}

// visit calls finish in post-order and returns the first cycle, if any.
func (w *prerequisiteWalk) visit(g *InMemoryGraph, start string, finish func(string)) []string {
	cycle, _ := w.visitWithCancel(g, start, finish, nil)
	return cycle
}

// visitWithCancel checks for cancellation while processing the explicit stack.
func (w *prerequisiteWalk) visitWithCancel(g *InMemoryGraph, start string, finish func(string), checkCanceled func() error) ([]string, error) {
	if w.state[start] != unvisited {
		return nil, nil
	}
	w.push(start)
	for len(w.stack) > 0 {
		if checkCanceled != nil {
			if err := checkCanceled(); err != nil {
				return nil, err
			}
		}
		last := len(w.stack) - 1
		frame := &w.stack[last]
		edges := g.forward[frame.name]
		if frame.next == len(edges) {
			w.state[frame.name] = finished
			if finish != nil {
				finish(frame.name)
			}
			w.stack = w.stack[:last]
			continue
		}
		edge := edges[frame.next]
		frame.next++
		if !prerequisiteRelation(edge.Relation) {
			continue
		}
		switch w.state[edge.Object] {
		case unvisited:
			w.push(edge.Object)
		case visiting:
			return w.cycle(edge.Object), nil
		}
	}
	return nil, nil
}

func (w *prerequisiteWalk) push(name string) {
	w.state[name] = visiting
	w.stack = append(w.stack, prerequisiteFrame{name: name})
}

func (w *prerequisiteWalk) cycle(start string) []string {
	// A visiting node is on the stack. Skip the path leading into the cycle.
	i := len(w.stack) - 1
	for w.stack[i].name != start {
		i--
	}
	path := w.stack[i:]
	first := 0
	for i, frame := range path {
		if frame.name < path[first].name {
			first = i
		}
	}
	cycle := make([]string, len(path)+1)
	for i := range cycle {
		cycle[i] = path[(first+i)%len(path)].name
	}
	return cycle
}
