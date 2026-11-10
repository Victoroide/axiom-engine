package main

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func sourceFiles(root, dir string) ([]*ast.File, error) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, dir, entry.Name()), nil, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

func checkCurriculum(d diagram, tuples []tuple) error {
	nodes, edges := map[string]bool{}, map[string]string{}
	for _, t := range tuples {
		nodes[t.Object] = true
		nodes[t.Subject] = true
		edges[t.Object+"/"+t.Subject] = t.Relation
	}
	gotNodes, gotEdges := map[string]bool{}, map[string]string{}
	categories := map[string]string{}
	for _, n := range d.Nodes {
		if !n.Circle || n.Title != n.ID || n.Group == "" {
			return fmt.Errorf("topic %s lacks its circular label or category", n.ID)
		}
		gotNodes[n.ID] = true
		categories[n.ID] = n.Group
	}
	for _, e := range d.Edges {
		gotEdges[e.From+"/"+e.To] = e.Relation
		if categories[e.From] != categories[e.To] {
			return fmt.Errorf("relation crosses component groups")
		}
	}
	if len(d.Nodes) != len(nodes) || len(d.Edges) != len(edges) || !maps.Equal(nodes, gotNodes) || !maps.Equal(edges, gotEdges) {
		return fmt.Errorf("seed diagram differs from curriculum")
	}
	// Each group is exactly one connected component, including subtype links.
	for _, start := range d.Nodes {
		visited := map[string]bool{start.ID: true}
		queue := []string{start.ID}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for _, e := range d.Edges {
				next := ""
				if e.From == id {
					next = e.To
				}
				if e.To == id {
					next = e.From
				}
				if next != "" && !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
		for _, other := range d.Nodes {
			if (other.Group == start.Group) != visited[other.ID] {
				return fmt.Errorf("group combines disconnected components")
			}
		}
	}
	want := []string{"Variables/Objects", "Objects/Classes", "Classes/Abstract Classes", "Abstract Classes/Interfaces", "Interfaces/Design Patterns", "Pointers/Linked Lists", "Linked Lists/Trees", "Trees/Binary Search Trees", "Binary Search Trees/AVL Trees"}
	if !slices.Equal(animatedEdges(d), want) {
		return fmt.Errorf("seed motion differs from the two documented chains")
	}
	if len(d.Legend) != 2 || d.Legend[0].Dashed || !d.Legend[1].Dashed || d.Legend[0].Text != "Required before" || d.Legend[1].Text != "Has subtype" {
		return fmt.Errorf("seed relation legend differs")
	}
	return nil
}

func animatedEdges(d diagram) []string {
	type cue struct {
		id string
		at float64
	}
	var cues []cue
	for _, e := range d.Edges {
		for _, at := range e.Times {
			cues = append(cues, cue{e.ID, at})
		}
	}
	slices.SortFunc(cues, func(a, b cue) int { return cmp.Compare(a.at, b.at) })
	out := make([]string, 0, len(cues))
	for _, c := range cues {
		out = append(out, c.id)
	}
	return out
}

type dependency struct{ From, To string }

func packageImports(root string) (map[string]bool, map[dependency]bool, error) {
	inventory := map[string]bool{}
	files := map[string][]*ast.File{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root {
			if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		dir, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parsed, err := sourceFiles(root, dir)
		if err != nil {
			return err
		}
		if len(parsed) > 0 {
			inventory[filepath.ToSlash(dir)] = true
			files[filepath.ToSlash(dir)] = parsed
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	imports := map[dependency]bool{}
	for from, parsed := range files {
		if sharedPackage(from) || excludedPackage(from) {
			continue
		}
		for _, f := range parsed {
			for _, imp := range f.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return nil, nil, err
				}
				path = strings.TrimPrefix(path, "github.com/Victoroide/axiom-engine/")
				for to := range inventory {
					if to != from && !sharedPackage(to) && !excludedPackage(to) && (path == to || strings.HasPrefix(path, to+"/")) {
						imports[dependency{from, to}] = true
					}
				}
			}
		}
	}
	return inventory, imports, nil
}

// Support imports are represented by the baml_client card.
func excludedPackage(pkg string) bool {
	switch pkg {
	case "docs", "baml_client/types", "baml_client/stream_types", "baml_client/type_builder":
		return true
	}
	return strings.HasPrefix(pkg, "test/")
}
func sharedPackage(pkg string) bool {
	return pkg == "internal/config" || pkg == "internal/telemetry" || pkg == "internal/domain"
}

func checkImports(d diagram, inventory map[string]bool, imports map[dependency]bool) error {
	packages := map[string]bool{}
	byID := map[string]node{}
	bottom := 0.0
	for _, n := range d.Nodes {
		if n.Package == "" || n.Title != n.Package {
			return fmt.Errorf("package card lacks its import path")
		}
		packages[n.Package] = true
		byID[n.ID] = n
		if !sharedPackage(n.Package) {
			bottom = max(bottom, n.Box.Y+n.Box.H)
		}
	}
	included := maps.Clone(inventory)
	for pkg := range included {
		if excludedPackage(pkg) {
			delete(included, pkg)
		}
	}
	if len(d.Nodes) != len(included) || !maps.Equal(packages, included) {
		return fmt.Errorf("package cards differ from Go packages")
	}
	for _, n := range d.Nodes {
		if sharedPackage(n.Package) && n.Box.Y <= bottom {
			return fmt.Errorf("shared package is outside the lower band")
		}
	}
	drawn := map[dependency]bool{}
	for _, e := range d.Edges {
		a, b := byID[e.From], byID[e.To]
		if a.ID == "" || b.ID == "" {
			return fmt.Errorf("import edge references an unknown package")
		}
		drawn[dependency{a.Package, b.Package}] = true
	}
	reduced, err := transitiveReduction(imports)
	if err != nil {
		return err
	}
	if len(d.Edges) != len(reduced) || !maps.Equal(drawn, reduced) {
		return fmt.Errorf("package edges differ from the transitive reduction of Go imports")
	}
	if !maps.Equal(transitiveClosure(drawn), transitiveClosure(imports)) {
		return fmt.Errorf("package arrows do not preserve Go import reachability")
	}

	want := []string{"entry/server", "server/api", "api/service", "service/graph"}
	if !slices.Equal(animatedEdges(d), want) {
		return fmt.Errorf("package motion differs from main import path")
	}
	return nil
}

func registeredRoutes(root string) (map[string]bool, error) {
	routes := map[string]bool{}
	for _, dir := range []string{"internal/api", "internal/server"} {
		files, err := sourceFiles(root, dir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			maps.Copy(routes, routesInFile(f))
		}
	}
	return routes, nil
}
func routesInFile(f *ast.File) map[string]bool {
	groups := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		id, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Group" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		prefix, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if parent, ok := sel.X.(*ast.Ident); ok {
			prefix = groups[parent.Name] + prefix
		}
		groups[id.Name] = prefix
		return true
	})
	routes := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Get", "Post", "Put", "Patch", "Delete", "Head", "Options":
		default:
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		path, err := strconv.Unquote(lit.Value)
		if err != nil || !strings.HasPrefix(path, "/") {
			return true
		}
		if receiver, ok := sel.X.(*ast.Ident); ok {
			path = groups[receiver.Name] + path
		}
		routes[strings.ToUpper(sel.Sel.Name)+" "+path] = true
		return true
	})
	return routes
}
func checkRoutes(d diagram, registered map[string]bool) error {
	declared := map[string]bool{}
	for _, n := range d.Nodes {
		visible := displayedRoutes(n)
		for _, route := range n.Routes {
			if declared[route] {
				return fmt.Errorf("duplicate route label %s", route)
			}
			declared[route] = true
			method, path, ok := strings.Cut(route, " ")
			if !ok {
				return fmt.Errorf("missing route method")
			}
			if !visible[method+" "+path] {
				return fmt.Errorf("route %s has no visible label", route)
			}
		}
		for route := range visible {
			if !slices.Contains(n.Routes, route) {
				return fmt.Errorf("undeclared visible route %s on %s", route, n.ID)
			}
		}
	}
	if !maps.Equal(declared, registered) {
		return fmt.Errorf("diagram routes differ from registered Fiber routes")
	}
	shared := []string{"reasoning/graph", "reasoning/guard", "guard/baml", "baml/provider", "reasoning/traces"}
	want := append([]string{"client/router", "router/api", "api/handlers", "handlers/replay", "handlers/reasoning"}, shared...)
	want = append(want, "mcp/reasoning")
	want = append(want, shared...)
	if !slices.Equal(animatedEdges(d), want) {
		return fmt.Errorf("request motion differs from HTTP then MCP flow")
	}
	return nil
}

func displayedRoutes(n node) map[string]bool {
	base, method := "", ""
	var pairs []string
	previous := -1
	for _, line := range n.Lines {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "Base" {
			base = fields[1]
			previous = -1
			continue
		}
		// A parameter on the next line continues a path ending at a slash.
		if previous >= 0 && len(fields) == 1 && strings.HasPrefix(line, ":") && strings.HasSuffix(pairs[previous], "/") {
			pairs[previous] += line
			previous = -1
			continue
		}
		previous = -1
		// Public endpoints share the last explicit method across bullet items.
		for part := range strings.SplitSeq(line, "·") {
			fields = strings.Fields(part)
			if len(fields) == 0 {
				continue
			}
			switch fields[0] {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
				method, fields = fields[0], fields[1:]
			}
			if len(fields) == 2 && fields[1] == "(opt.)" {
				fields = fields[:1]
			}
			if method != "" && len(fields) == 1 && strings.HasPrefix(fields[0], "/") {
				pairs = append(pairs, method+" "+base+fields[0])
				previous = len(pairs) - 1
			}
		}
	}
	visible := map[string]bool{}
	for _, pair := range pairs {
		if pair == "GET /swagger" {
			visible["GET /swagger/doc.json"], visible["GET /swagger/*"] = true, true
		} else {
			visible[pair] = true
		}
	}
	return visible
}

func TestSourcesMatchDiagrams(t *testing.T) {
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	tuples, err := readCurriculum("../..")
	if err != nil {
		t.Fatal(err)
	}
	inventory, imports, err := packageImports("../..")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := registeredRoutes("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		switch d.Name {
		case "seed-graph":
			err = checkCurriculum(d, tuples)
		case "system-components":
			err = checkImports(d, inventory, imports)
		case "request-path":
			err = checkRoutes(d, routes)
		}
		if err != nil {
			t.Errorf("%s: %v", d.Name, err)
		}
	}
}

func TestRejectCurriculumDefects(t *testing.T) {
	tuples, err := readCurriculum("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"topic", "relation", "component", "circle", "motion", "legend"} {
		ds, err := buildDiagrams("../..", testFonts(t))
		if err != nil {
			t.Fatal(err)
		}
		d := ds[2]
		switch kind {
		case "topic":
			d.Nodes = d.Nodes[1:]
		case "relation":
			d.Edges[0].Relation = "is_a"
		case "component":
			d.Nodes[0].Group = "graphs"
		case "circle":
			d.Nodes[0].Circle = false
		case "motion":
			d.Edges[0].Times = nil
		case "legend":
			d.Legend[1].Dashed = false
		}
		if err := checkCurriculum(d, tuples); err == nil {
			t.Fatalf("accepted %s defect", kind)
		}
	}
}
func TestRejectImportDefects(t *testing.T) {
	inventory, imports, err := packageImports("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing import", "invented import", "package", "shared band", "label", "motion"} {
		ds, err := buildDiagrams("../..", testFonts(t))
		if err != nil {
			t.Fatal(err)
		}
		d := ds[1]
		switch kind {
		case "missing import":
			d.Edges = d.Edges[1:]
		case "invented import":
			d.Edges[0].To = "baml"
		case "package":
			d.Nodes = d.Nodes[1:]
		case "shared band":
			d.Nodes[len(d.Nodes)-1].Box.Y = 80
		case "label":
			d.Nodes[0].Title = "Other"
		case "motion":
			d.Edges[0].Times = nil
		}
		if err := checkImports(d, inventory, imports); err == nil {
			t.Fatalf("accepted %s defect", kind)
		}
	}
}
func TestRejectRouteDefects(t *testing.T) {
	registered, err := registeredRoutes("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing route", "invented route", "method", "label", "new endpoint", "motion"} {
		ds, err := buildDiagrams("../..", testFonts(t))
		if err != nil {
			t.Fatal(err)
		}
		d := ds[0]
		routes := maps.Clone(registered)
		for i := range d.Nodes {
			if d.Nodes[i].ID == "probes" {
				switch kind {
				case "missing route":
					d.Nodes[i].Routes = d.Nodes[i].Routes[1:]
				case "invented route":
					d.Nodes[i].Routes[0] = "GET /missing"
				case "method":
					d.Nodes[i].Routes[0] = "POST /health"
				case "label":
					d.Nodes[i].Lines = nil
				}
			}
		}
		if kind == "new endpoint" {
			routes["GET /new"] = true
		}
		if kind == "motion" {
			d.Edges[0].Times = nil
		}
		if err := checkRoutes(d, routes); err == nil {
			t.Fatalf("accepted %s defect", kind)
		}
	}
}
func TestRouteParsingResolvesNestedGroupsAndMethods(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "routes.go", `package endpoint
 func register(app Router) {
 v1:=app.Group("/api/v1")
 nested:=v1.Group("/nested")
 nested.Post("/entry",handler)
 app.Get("/health",handler)
 app.Use("/api/v1",middleware)
 c.Get("Header")
 }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"POST /api/v1/nested/entry": true, "GET /health": true}
	if got := routesInFile(f); !maps.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestReducedImportsRejectAnImpliedDirectArrow(t *testing.T) {
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	inventory, imports, err := packageImports("../..")
	if err != nil {
		t.Fatal(err)
	}
	d := ds[1]
	if err := checkImports(d, inventory, imports); err != nil {
		t.Fatalf("reduced imports must retain full reachability: %v", err)
	}
	expanded := maps.Clone(imports)
	expanded[dependency{"cmd/server", "internal/api"}] = true
	if err := checkImports(d, inventory, expanded); err != nil {
		t.Fatalf("redundant source import must preserve the reduced image: %v", err)
	}
	d.Edges = append(d.Edges, edge{ID: "entry/graph", From: "entry", To: "graph"})
	if err := checkImports(d, inventory, imports); err == nil {
		t.Fatal("accepted a direct import already implied by a longer chain")
	}
}

func TestWrappedTopicRouteRemainsExact(t *testing.T) {
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registeredRoutes("../..")
	if err != nil {
		t.Fatal(err)
	}
	d := ds[0]
	if err := checkRoutes(d, registered); err != nil {
		t.Fatalf("wrapped complete route must remain visible: %v", err)
	}
	for i := range d.Nodes {
		if d.Nodes[i].ID == "topics" {
			d.Nodes[i].Lines[len(d.Nodes[i].Lines)-1] = ":student_id/"
		}
	}
	if err := checkRoutes(d, registered); err == nil {
		t.Fatal("accepted an incomplete wrapped route")
	}
}

func TestRejectVisibleRoutePairDefects(t *testing.T) {
	registered, err := registeredRoutes("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, before, after string
	}{
		{"omitted beside longer route", "POST /adaptive-plan", ""},
		{"wrong method beside correct method", "POST /adaptive-plan", "GET /adaptive-plan"},
		{"longer path cannot supply prefix", "GET /topics", "GET /topics/extra"},
		{"public shorthand retains method", "GET /health · /livez · /readyz", "POST /health · /livez · /readyz"},
		{"swagger shorthand has boundary", "/swagger · /metrics (opt.)", "/swagger/extra · /metrics (opt.)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds, err := buildDiagrams("../..", testFonts(t))
			if err != nil {
				t.Fatal(err)
			}
			d := ds[0]
			changed := false
			for i := range d.Nodes {
				for j, line := range d.Nodes[i].Lines {
					if line == tc.before {
						d.Nodes[i].Lines[j] = tc.after
						changed = true
					}
				}
			}
			if !changed {
				t.Fatal("route fixture was not changed")
			}
			if err := checkRoutes(d, registered); err == nil || !strings.Contains(err.Error(), "no visible label") {
				t.Fatalf("accepted incorrect visible method/path pair: %v", err)
			}
		})
	}
}

func transitiveClosure(edges map[dependency]bool) map[dependency]bool {
	closure := maps.Clone(edges)
	for changed := true; changed; {
		changed = false
		for a := range closure {
			for b := range closure {
				next := dependency{a.From, b.To}
				if a.To == b.From && !closure[next] {
					closure[next] = true
					changed = true
				}
			}
		}
	}
	return closure
}

func transitiveReduction(edges map[dependency]bool) (map[dependency]bool, error) {
	closure := transitiveClosure(edges)
	for e := range closure {
		if e.From == e.To {
			return nil, fmt.Errorf("package imports contain a cycle")
		}
	}
	reduced := maps.Clone(edges)
	for direct := range edges {
		for first := range edges {
			if first.From == direct.From && first.To != direct.To && closure[dependency{first.To, direct.To}] {
				delete(reduced, direct)
				break
			}
		}
	}
	return reduced, nil
}

func TestImportReductionAndClosure(t *testing.T) {
	edges := map[dependency]bool{{"a", "b"}: true, {"b", "c"}: true, {"a", "c"}: true, {"b", "d"}: true, {"c", "e"}: true, {"d", "e"}: true, {"a", "e"}: true}
	want := map[dependency]bool{{"a", "b"}: true, {"b", "c"}: true, {"b", "d"}: true, {"c", "e"}: true, {"d", "e"}: true}
	closure := map[dependency]bool{{"a", "b"}: true, {"a", "c"}: true, {"a", "d"}: true, {"a", "e"}: true, {"b", "c"}: true, {"b", "d"}: true, {"b", "e"}: true, {"c", "e"}: true, {"d", "e"}: true}
	got, err := transitiveReduction(edges)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, want) {
		t.Fatalf("reduction %v, want %v", got, want)
	}
	if !maps.Equal(transitiveClosure(got), closure) {
		t.Fatal("reduction lost or invented reachability")
	}
	if len(edges) != 7 {
		t.Fatal("reduction mutated its input")
	}
	for _, empty := range []map[dependency]bool{nil, {}} {
		got, err := transitiveReduction(empty)
		if err != nil || len(got) != 0 {
			t.Fatalf("empty reduction: %v, %v", got, err)
		}
	}
	for _, cycle := range []map[dependency]bool{{{"a", "a"}: true}, {{"a", "b"}: true, {"b", "a"}: true}} {
		if _, err := transitiveReduction(cycle); err == nil {
			t.Fatal("cyclic imports accepted")
		}
	}
}
