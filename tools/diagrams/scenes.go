package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func buildDiagrams(root string, fonts *fontSet) (result []diagram, err error) {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rootFS.Close()) }()
	tuples, err := readCurriculum(root)
	if err != nil {
		return nil, err
	}
	ds := []diagram{requestPath(), systemComponents(), seedGraph(tuples)}
	for i := range ds {
		d := &ds[i]
		data, err := rootFS.ReadFile(filepath.Join("docs", "assets", "diagrams", "src", d.Name+".json"))
		if err != nil {
			return nil, err
		}
		var p placement
		if err := decodePlacement(data, &p); err != nil {
			return nil, fmt.Errorf("%s: %w", d.Name, err)
		}
		d.W, d.H = p.Width, p.Height
		for j := range d.Nodes {
			n := &d.Nodes[j]
			pos, ok := p.Nodes[n.ID]
			if !ok {
				return nil, fmt.Errorf("%s has no placement for %s", d.Name, n.ID)
			}
			n.Box = pos
			delete(p.Nodes, n.ID)
		}
		for j := range d.Edges {
			e := &d.Edges[j]
			points, ok := p.Edges[e.ID]
			if !ok {
				return nil, fmt.Errorf("%s has no route for %s", d.Name, e.ID)
			}
			e.Points = points
			delete(p.Edges, e.ID)
		}
		for j := range d.Groups {
			g := &d.Groups[j]
			pos, ok := p.Groups[g.ID]
			if !ok {
				return nil, fmt.Errorf("%s has no placement for group %s", d.Name, g.ID)
			}
			g.Box = pos
			delete(p.Groups, g.ID)
		}
		if d.Name == "seed-graph" {
			if err := alignLegend(d, fonts); err != nil {
				return nil, err
			}
		}

		if len(p.Nodes)+len(p.Edges)+len(p.Groups) != 0 {
			return nil, fmt.Errorf("%s has obsolete placements", d.Name)
		}
		switch d.Name {
		case "request-path":
			shared := []string{"reasoning/graph", "reasoning/guard", "guard/baml", "baml/provider", "reasoning/traces"}
			err = schedule(d, append([]string{"client/router", "router/api", "api/handlers", "handlers/replay", "handlers/reasoning"}, shared...), append([]string{"mcp/reasoning"}, shared...))
		case "system-components":
			err = schedule(d, []string{"entry/server", "server/api", "api/service", "service/graph"})
		case "seed-graph":
			err = schedule(d, []string{"Variables/Objects", "Objects/Classes", "Classes/Abstract Classes", "Abstract Classes/Interfaces", "Interfaces/Design Patterns"}, []string{"Pointers/Linked Lists", "Linked Lists/Trees", "Trees/Binary Search Trees", "Binary Search Trees/AVL Trees"})
		}
		if err != nil {
			return nil, err
		}
	}
	return ds, nil
}

func decodePlacement(data []byte, p *placement) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(p); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing placement data")
		}
		return fmt.Errorf("trailing placement data: %w", err)
	}
	return nil
}

func addNode(d *diagram, id, title string, lines ...string) {
	d.Nodes = append(d.Nodes, node{ID: id, Title: title, Lines: lines})
}
func addEdge(d *diagram, from, to string) {
	d.Edges = append(d.Edges, edge{ID: from + "/" + to, From: from, To: to})
}

func requestPath() diagram {
	d := diagram{Name: "request-path", Title: "AxiomEngine request path", Subtitle: "HTTP AND MCP FLOWS",
		Description: "HTTP clients pass through Fiber middleware and the protected API to JSON or SSE plan handlers. JSON supports Idempotency-Key replay in memory or Redis. MCP over stdio calls the same ReasoningService implementation directly. The service validates input, extracts graph context, generates topics with concurrency and circuit-breaker limits, then merges and validates output. The graph uses an embedded seed or GRAPH_FILE. The BAML registry selects Amazon Bedrock by default, or OpenAI, Anthropic, Google, Ollama or an OpenAI-compatible provider. Public probes and optional Swagger and metrics bypass API authentication. Topic routes read the graph. Optional OTLP and LangSmith exporters receive traces. Motion follows an HTTP request, then an MCP request."}
	addNode(&d, "client", "HTTP client", "JSON or SSE response")
	addNode(&d, "router", "Fiber router", "Request ID · logs · recover", "Security headers · CORS")
	addNode(&d, "probes", "Public endpoints", "GET /health · /livez · /readyz", "/swagger · /metrics (opt.)")
	addNode(&d, "api", "API middleware", "Token · per-IP limit", "Local or Redis · /api/v1")
	addNode(&d, "handlers", "Plan handlers", "Base /api/v1", "POST /adaptive-plan", "POST /adaptive-plan/stream", "SSE progress")
	addNode(&d, "replay", "Idempotency-Key", "JSON replay only", "Memory or Redis")
	addNode(&d, "topics", "Topic handlers", "Base /api/v1", "GET /topics", "GET /topics/:topic/path", "GET /tutor/student/", ":student_id/cognitive-graph")
	addNode(&d, "mcp", "MCP over stdio", "axiom-server mcp", "Direct service access")
	addNode(&d, "reasoning", "ReasoningService", "Validate · subgraph · fan-out", "Merge · validate output")
	addNode(&d, "graph", "Knowledge graph", "In memory · embedded seed", "Override with GRAPH_FILE")
	addNode(&d, "guard", "Planner protection", "Concurrency limit", "Circuit breaker · timeout")
	addNode(&d, "baml", "BAML planner", "Provider registry", "Per-topic generation")
	addNode(&d, "provider", "Configured provider", "Amazon Bedrock (default)", "OpenAI · Anthropic · Google", "Ollama · OpenAI-compatible")
	addNode(&d, "traces", "Trace export", "OTLP · LangSmith", "Optional exporters")
	routes := map[string][]string{
		"probes":   {"GET /health", "GET /livez", "GET /readyz", "GET /metrics", "GET /swagger/doc.json", "GET /swagger/*"},
		"handlers": {"POST /api/v1/adaptive-plan", "POST /api/v1/adaptive-plan/stream"},
		"topics":   {"GET /api/v1/topics", "GET /api/v1/topics/:topic/path", "GET /api/v1/tutor/student/:student_id/cognitive-graph"},
	}
	for i := range d.Nodes {
		d.Nodes[i].Routes = routes[d.Nodes[i].ID]
	}
	for _, pair := range [][2]string{{"client", "router"}, {"router", "probes"}, {"router", "api"}, {"api", "handlers"}, {"api", "topics"}, {"handlers", "replay"}, {"handlers", "reasoning"}, {"topics", "graph"}, {"mcp", "reasoning"}, {"reasoning", "graph"}, {"reasoning", "guard"}, {"guard", "baml"}, {"baml", "provider"}, {"reasoning", "traces"}} {
		addEdge(&d, pair[0], pair[1])
	}
	return d
}

func systemComponents() diagram {
	d := diagram{Name: "system-components", Title: "AxiomEngine packages", Subtitle: "PACKAGE IMPORTS",
		Description: "Go package imports reduced to direct arrows that are not implied by a longer chain. From cmd/server, HTTP packages lead through internal/server and internal/api to the reasoning service; MCP and the LLM adapter also import that service. The adapter imports baml_client. The API imports idempotency storage, and the service imports the graph and metrics. The transitive closure of these arrows equals the non-shared imports among the packages shown. The docs and test packages are excluded, along with generated baml_client support packages whose imports are represented by baml_client. The edge-less band contains config, telemetry and domain, which are imported throughout. Motion follows cmd/server to internal/server, internal/api, internal/service and internal/graph."}
	for _, n := range []struct{ id, pkg, detail string }{
		{"entry", "cmd/server", "Startup · HTTP or stdio"}, {"server", "internal/server", "Fiber · middleware"}, {"api", "internal/api", "JSON · SSE · topic routes"}, {"mcp", "internal/mcpserver", "Stdio tools · lifecycle"}, {"service", "internal/service", "Reasoning · Planner interface"}, {"graph", "internal/graph", "Seed · prerequisite paths"}, {"llm", "internal/llm", "BAML provider adapter"}, {"baml", "baml_client", "Generated typed client"}, {"idempotency", "internal/idempotency", "Memory or Redis replay"}, {"metrics", "internal/metrics", "Prometheus metrics"}, {"config", "internal/config", "Shared startup settings"}, {"telemetry", "internal/telemetry", "Shared tracing · OTLP export"}, {"domain", "internal/domain", "Shared request / graph types"},
	} {
		addNode(&d, n.id, n.pkg, n.detail)
		d.Nodes[len(d.Nodes)-1].Package = n.pkg
	}
	for _, pair := range [][2]string{{"entry", "server"}, {"entry", "mcp"}, {"entry", "llm"}, {"server", "api"}, {"api", "service"}, {"api", "idempotency"}, {"mcp", "service"}, {"llm", "service"}, {"llm", "baml"}, {"service", "graph"}, {"service", "metrics"}} {
		addEdge(&d, pair[0], pair[1])
	}
	d.Labels = []label{{Text: "Arrows show direct imports not implied by a longer chain. config, telemetry and domain are imported throughout.", X: 16, Y: 568, Size: 12, Kind: "muted"}}
	return d
}

func seedGraph(tuples []tuple) diagram {
	topics := map[string]bool{}
	for _, t := range tuples {
		topics[t.Subject], topics[t.Object] = true, true
	}
	d := diagram{Name: "seed-graph", Title: "Seed knowledge graph", Subtitle: fmt.Sprintf("%d TOPICS · %d RELATIONS", len(topics), len(tuples)),
		Description: fmt.Sprintf("Seed knowledge graph with %d topics and %d relations, grouped by connected component: Object-oriented, Graphs and trees, Algorithms, and Arrays and hashing. Circles advance right or down by prerequisite depth. Solid arrows mean required before; dashed arrows mean has subtype. Motion follows Variables to Objects to Classes to Abstract Classes to Interfaces to Design Patterns, then Pointers to Linked Lists to Trees to Binary Search Trees to AVL Trees.", len(topics), len(tuples))}
	for _, g := range []struct{ id, title, topics string }{
		{"objects", "Object-oriented", "Variables|Objects|Classes|Inheritance|Polymorphism|Generics|Abstract Classes|Interfaces|Design Patterns"},
		{"graphs", "Graphs and trees", "Pointers|Linked Lists|Graphs|Graph Traversal|Trees|Binary Search Trees|AVL Trees"},
		{"algorithms", "Algorithms", "Functions|Recursion|Merge Sort|Dynamic Programming"},
		{"arrays", "Arrays and hashing", "Arrays|Binary Search|Hash Tables"},
	} {
		d.Groups = append(d.Groups, group{ID: g.id, Title: g.title})
		for _, title := range strings.Split(g.topics, "|") {
			d.Nodes = append(d.Nodes, node{ID: title, Title: title, Circle: true, Group: g.id})
		}
	}
	for _, pair := range [][2]string{{"Variables", "Objects"}, {"Objects", "Classes"}, {"Classes", "Inheritance"}, {"Inheritance", "Polymorphism"}, {"Polymorphism", "Generics"}, {"Polymorphism", "Design Patterns"}, {"Abstract Classes", "Interfaces"}, {"Interfaces", "Design Patterns"}, {"Arrays", "Binary Search"}, {"Arrays", "Hash Tables"}, {"Functions", "Recursion"}, {"Recursion", "Merge Sort"}, {"Recursion", "Dynamic Programming"}, {"Pointers", "Linked Lists"}, {"Linked Lists", "Graphs"}, {"Graphs", "Graph Traversal"}, {"Linked Lists", "Trees"}} {
		addEdge(&d, pair[0], pair[1])
	}
	for _, pair := range [][2]string{{"Classes", "Abstract Classes"}, {"Trees", "Binary Search Trees"}, {"Binary Search Trees", "AVL Trees"}} {
		addEdge(&d, pair[0], pair[1])
		d.Edges[len(d.Edges)-1].Relation = "is_a"
	}
	for i := range d.Edges {
		if d.Edges[i].Relation == "" {
			d.Edges[i].Relation = "depends_on"
		}
	}
	d.Legend = []legend{{Text: "Required before", Y: 52}, {Text: "Has subtype", Y: 52, Dashed: true}}
	return d
}

// schedule sends one pulse at a time, lighting the source on departure and
// the target after arrival. Each target lights fully before the next pulse.
func schedule(d *diagram, flows ...[]string) error {
	at := .1
	for i, flow := range flows {
		if i > 0 {
			at += flowPause
		}
		for _, id := range flow {
			k := slices.IndexFunc(d.Edges, func(e edge) bool { return e.ID == id })
			if k < 0 {
				return fmt.Errorf("%s has no edge %s", d.Name, id)
			}
			e := &d.Edges[k]
			e.Times = append(e.Times, at)
			arrival := at + pulseDuration(edgeLength(*e))
			for j := range d.Nodes {
				n := &d.Nodes[j]
				if n.ID == e.From {
					n.Times = append(n.Times, at)
				}
				if n.ID == e.To {
					n.Times = append(n.Times, arrival+targetHighlightDelay)
				}
			}
			at = arrival + stepPause
		}
	}
	d.Duration = loopDuration(*d)
	return nil
}

func sceneLabels(d diagram, f *fontSet) ([]label, error) {
	labels := []label{{Text: d.Title, X: 16, Y: 32, Size: titleSize, Bold: true}, {Text: d.Subtitle, X: 16, Y: 56, Size: groupSize, Bold: true, Kind: "group"}}
	labels = append(labels, d.Labels...)
	for _, g := range d.Groups {
		labels = append(labels, label{Text: g.Title, X: g.Box.X + 16, Y: g.Box.Y + 18, Size: groupSize, Bold: true, Kind: "group"})
	}
	for _, n := range d.Nodes {
		if n.Circle {
			circle, err := circleLabels(n, f)
			if err != nil {
				return nil, err
			}
			labels = append(labels, circle...)
			continue
		}
		labels = append(labels, label{Text: n.Title, X: n.Box.X + 12, Y: n.Box.Y + 21, Size: titleSize, Bold: true, Owner: n.ID})
		for i, line := range n.Lines {
			labels = append(labels, label{Text: line, X: n.Box.X + 12, Y: n.Box.Y + 41 + float64(i)*20, Size: sublabelSize, Owner: n.ID, Kind: "muted"})
		}
	}
	for _, l := range d.Legend {
		labels = append(labels, label{Text: l.Text, X: l.X + 48, Y: l.Y + 4, Size: 12, Kind: "muted"})
	}
	return labels, nil
}

func wrapCircle(text string, width float64, f *fontSet) ([]string, error) {
	var lines []string
	for _, word := range strings.Fields(text) {
		candidate := word
		if len(lines) > 0 {
			candidate = lines[len(lines)-1] + " " + word
		}
		b, err := f.measure(label{Text: candidate, Size: 12, Bold: true})
		if err != nil {
			return nil, err
		}
		if len(lines) == 0 || b.W > width {
			lines = append(lines, word)
		} else {
			lines[len(lines)-1] = candidate
		}
	}
	return lines, nil
}

func circleLabels(n node, f *fontSet) ([]label, error) {
	lines, err := wrapCircle(n.Title, n.Box.W-20, f)
	if err != nil {
		return nil, err
	}
	labels := make([]label, 0, len(lines))
	for i, line := range lines {
		l := label{Text: line, Size: 12, Bold: true, Owner: n.ID}
		bounds, err := f.measure(l)
		if err != nil {
			return nil, err
		}
		l.X = n.Box.X + n.Box.W/2 - bounds.X - bounds.W/2
		l.Y = n.Box.Y + n.Box.H/2 + float64(i)*15 - float64(len(lines)-1)*7.5 - bounds.Y - bounds.H/2
		labels = append(labels, l)
	}
	return labels, nil
}

func alignLegend(d *diagram, f *fontSet) error {
	right := d.W - 16
	for i := len(d.Legend) - 1; i >= 0; i-- {
		sample := &d.Legend[i]
		b, err := f.measure(label{Text: sample.Text, Size: sublabelSize})
		if err != nil {
			return err
		}
		sample.X = right - b.X - b.W - 48
		right = sample.X - 24
	}
	return nil
}
