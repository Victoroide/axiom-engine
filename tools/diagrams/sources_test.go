package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestSeedMatchesCurriculum(t *testing.T) {
	root := filepath.Join("..", "..")
	d, err := loadDiagram(root, "seed-graph")
	if err != nil {
		t.Fatal(err)
	}
	var tuples []tuple
	if err := readJSON(filepath.Join(root, "internal", "graph", "seed.json"), &tuples); err != nil {
		t.Fatal(err)
	}
	wantNodes := map[string]bool{}
	wantEdges := map[string]string{}
	for _, v := range tuples {
		wantNodes[v.Object] = true
		wantNodes[v.Subject] = true
		wantEdges[v.Object+"/"+v.Subject] = v.Relation
	}
	gotNodes := map[string]bool{}
	gotEdges := map[string]string{}
	for _, n := range d.Nodes {
		gotNodes[n.ID] = true
		if n.R <= 0 || n.Category == "" {
			t.Errorf("missing circle or category: %s", n.ID)
		}
	}
	for _, e := range d.Edges {
		gotEdges[e.From+"/"+e.To] = e.Relation
	}
	if !reflect.DeepEqual(wantNodes, gotNodes) || !reflect.DeepEqual(wantEdges, gotEdges) {
		t.Fatal("diagram differs from curriculum")
	}
	chains := [][]string{{"Variables", "Objects", "Classes", "Inheritance", "Polymorphism"}, {"Functions", "Recursion", "Merge Sort"}}
	index := 0
	for _, chain := range chains {
		for i, n := range chain {
			if index >= len(d.Motion) || d.Motion[index].Node != n {
				t.Fatalf("missing study highlight for %s", n)
			}
			index++
			if i < len(chain)-1 {
				if index >= len(d.Motion) || d.Motion[index].Edge != n+"/"+chain[i+1] {
					t.Fatalf("missing study edge after %s", n)
				}
				index++
			}
		}
	}
}

func sourceFiles(t *testing.T, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

func TestComponentsMatchImports(t *testing.T) {
	root := filepath.Join("..", "..")
	d, err := loadDiagram(root, "system-components")
	if err != nil {
		t.Fatal(err)
	}
	packages := map[string]string{}
	for _, n := range d.Nodes {
		if n.Package == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(root, n.Package))
		if err != nil || !info.IsDir() {
			t.Fatalf("package directory missing: %s", n.Package)
		}
		packages[n.Package] = n.ID
	}
	for _, pkg := range []string{"cmd/server", "internal/config", "internal/server", "internal/api", "internal/service", "internal/graph", "internal/llm", "baml_client", "internal/metrics", "internal/domain"} {
		if packages[pkg] == "" {
			t.Errorf("missing package %s", pkg)
		}
	}
	imports := map[string]bool{}
	for pkg, id := range packages {
		for _, f := range sourceFiles(t, filepath.Join(root, pkg)) {
			for _, imp := range f.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				path = strings.TrimPrefix(path, "github.com/Victoroide/axiom-engine/")
				for target, to := range packages {
					if pkg != target && (path == target || strings.HasPrefix(path, target+"/")) {
						imports[id+"/"+to] = true
					}
				}
			}
		}
	}
	expected := map[string]bool{}
	for _, pair := range []string{"entry/config", "entry/server", "entry/graph", "entry/llm", "server/api", "server/metrics", "server/service", "api/service", "api/graph", "service/graph", "service/metrics", "llm/service", "llm/baml", "baml/provider"} {
		expected[pair] = true
	}
	drawn := map[string]bool{}
	for _, e := range d.Edges {
		pair := e.From + "/" + e.To
		drawn[pair] = true
		if pair != "baml/provider" && !imports[pair] {
			t.Errorf("drawn dependency has no matching Go import: %s", pair)
		}
	}
	if !reflect.DeepEqual(drawn, expected) {
		keys := func(m map[string]bool) []string {
			out := make([]string, 0, len(m))
			for k := range m {
				out = append(out, k)
			}
			sort.Strings(out)
			return out
		}
		t.Fatalf("architectural dependencies differ: got %v, want %v", keys(drawn), keys(expected))
	}
	layers := map[string]int{"entry": 0, "config": 1, "server": 1, "api": 2, "metrics": 2, "service": 3, "graph": 4, "llm": 4, "baml": 5, "provider": 6, "domain": 7}
	for _, n := range d.Nodes {
		for _, other := range d.Nodes {
			if layers[n.ID] < layers[other.ID] && n.Y+n.H >= other.Y {
				t.Errorf("%s is not above %s", n.ID, other.ID)
			}
		}
		if n.ID == "domain" {
			if n.W < d.Width-80 || strings.Join(n.Title, " ") != "internal/domain: shared request, response and graph types" {
				t.Error("missing shared domain band")
			}
		}
	}
	implements := false
	for _, l := range d.Labels {
		implements = implements || l.Text == "implements"
	}
	if !implements {
		t.Error("missing Planner interface label")
	}
	rank := map[string]int{}
	for range len(d.Nodes) {
		for _, e := range d.Edges {
			rank[e.To] = max(rank[e.To], rank[e.From]+1)
		}
	}
	previous := -1
	for _, c := range d.Motion {
		for _, e := range d.Edges {
			if e.ID == c.Edge {
				if rank[e.From] < previous {
					t.Error("dependency pulses are not in topological order")
				}
				previous = rank[e.From]
			}
		}
	}
}

func TestRequestRoutesAreRegistered(t *testing.T) {
	root := filepath.Join("..", "..")
	d, err := loadDiagram(root, "request-path")
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, dir := range []string{"internal/api", "internal/server"} {
		for _, f := range sourceFiles(t, filepath.Join(root, dir)) {
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
					t.Fatal(err)
				}
				if parent, ok := sel.X.(*ast.Ident); ok {
					prefix = groups[parent.Name] + prefix
				}
				groups[id.Name] = prefix
				return true
			})
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
				case "Get", "Post", "Use":
				default:
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				if receiver, ok := sel.X.(*ast.Ident); ok {
					s = groups[receiver.Name] + s
				}
				registered[s] = true
				return true
			})
		}
	}
	count := 0
	for _, n := range d.Nodes {
		for _, route := range n.Routes {
			count++
			if !registered[route] {
				t.Errorf("unregistered route %s", route)
			}
			if !strings.Contains(strings.Join(n.Subtitle, " "), route) {
				t.Errorf("route %s is not visible", route)
			}
		}
	}
	if count != 7 {
		t.Fatalf("want seven route labels, got %d", count)
	}
}

func TestSeedSectionsMatchCategories(t *testing.T) {
	d, err := loadDiagram(filepath.Join("..", ".."), "seed-graph")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range d.Nodes {
		section := ""
		for _, g := range d.Groups {
			if (box{g.X, g.Y, g.W, g.H}).contains(point{n.X + n.R, n.Y + n.R}) {
				section = g.Category
			}
		}
		if section != n.Category {
			t.Errorf("%s category %s sits in section %s", n.ID, n.Category, section)
		}
	}
	for _, l := range d.Labels {
		if l.Style != "group" {
			continue
		}
		b, err := labelBox(l)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range d.Nodes {
			dx := max(b.x-n.X-n.R, 0, n.X+n.R-b.x-b.w)
			dy := max(b.y-n.Y-n.R, 0, n.Y+n.R-b.y-b.h)
			if gap := hypot(dx, dy) - n.R - 1; gap < 24 {
				t.Errorf("%s is %.2f px from section label %s", n.ID, gap, l.Text)
			}
		}
	}
}
