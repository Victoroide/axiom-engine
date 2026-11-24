package curriculum

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/Victoroide/axiom-engine/internal/graph"
)

const (
	shippedCSCrosswalk    = "editions/ap-csa-fall-2025/computer-science.crosswalk.json"
	shippedCSReportJSON   = "editions/ap-csa-fall-2025/computer-science.report.json"
	shippedCSReportMD     = "editions/ap-csa-fall-2025/computer-science.report.md"
	shippedSeedCrosswalk  = "editions/ap-csa-fall-2025/seed.crosswalk.json"
	shippedSeedReportJSON = "editions/ap-csa-fall-2025/seed.report.json"
	shippedSeedReportMD   = "editions/ap-csa-fall-2025/seed.report.md"
	shippedCSGraphPath    = "../../examples/graphs/computer-science.json"
	shippedSeedGraphPath  = "../../internal/graph/seed.json"
)

func TestShippedGraphsEvaluationAndReproducibility(t *testing.T) {
	cases := []struct {
		name           string
		crosswalkPath  string
		graphPath      string
		reportJSONPath string
		reportMDPath   string
		wantNodes      int
		wantTuples     int
		wantDependsOn  int
		wantIsA        int
		wantComponents int
	}{
		{
			name:           "computer-science example",
			crosswalkPath:  shippedCSCrosswalk,
			graphPath:      shippedCSGraphPath,
			reportJSONPath: shippedCSReportJSON,
			reportMDPath:   shippedCSReportMD,
			wantNodes:      130,
			wantTuples:     173,
			wantDependsOn:  154,
			wantIsA:        19,
			wantComponents: 1,
		},
		{
			name:           "embedded seed",
			crosswalkPath:  shippedSeedCrosswalk,
			graphPath:      shippedSeedGraphPath,
			reportJSONPath: shippedSeedReportJSON,
			reportMDPath:   shippedSeedReportMD,
			wantNodes:      23,
			wantTuples:     20,
			wantDependsOn:  17,
			wantIsA:        3,
			wantComponents: 4,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Verify shipped graph itself remains untouched and valid under graph.Load.
			g, err := graph.Load(tc.graphPath)
			if err != nil {
				t.Fatalf("graph.Load: %v", err)
			}
			if got := len(g.Topics("")); got != tc.wantNodes {
				t.Fatalf("graph topics = %d, want %d", got, tc.wantNodes)
			}
			if got := len(g.All()); got != tc.wantTuples {
				t.Fatalf("graph tuples = %d, want %d", got, tc.wantTuples)
			}

			r1, err := EvaluateFiles(shippedManifestPath, tc.crosswalkPath, tc.graphPath)
			if err != nil {
				t.Fatalf("EvaluateFiles run 1: %v", err)
			}
			r2, err := EvaluateFiles(shippedManifestPath, tc.crosswalkPath, tc.graphPath)
			if err != nil {
				t.Fatalf("EvaluateFiles run 2: %v", err)
			}

			if r1.TotalDenominator != 73 || len(r1.Mappings) != 73 {
				t.Fatalf("denominator=%d mappings=%d, want 73", r1.TotalDenominator, len(r1.Mappings))
			}
			if r1.StatusCounts.Mapped+r1.StatusCounts.Partial+r1.StatusCounts.Missing != 73 {
				t.Fatalf("status counts do not sum to 73: %+v", r1.StatusCounts)
			}
			if !r1.Provisional {
				t.Fatal("expected provisional=true before independent expert sign-off")
			}
			if r1.UnresolvedDisputesCount == 0 || len(r1.UnresolvedDisputes) != r1.UnresolvedDisputesCount {
				t.Fatalf("unresolved disputes mismatch: count=%d len=%d", r1.UnresolvedDisputesCount, len(r1.UnresolvedDisputes))
			}
			if r1.Graph.Concepts != tc.wantNodes || r1.Graph.Tuples != tc.wantTuples || r1.Graph.DependsOnEdges != tc.wantDependsOn || r1.Graph.IsAEdges != tc.wantIsA || r1.Graph.ConnectedComponents != tc.wantComponents {
				t.Fatalf("unexpected graph stats: %+v", r1.Graph)
			}

			j1, err := RenderReportJSON(r1)
			if err != nil {
				t.Fatal(err)
			}
			j2, err := RenderReportJSON(r2)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(j1, j2) {
				t.Fatal("JSON report bytes differed across runs")
			}

			m1, err := RenderReportMarkdown(r1)
			if err != nil {
				t.Fatal(err)
			}
			m2, err := RenderReportMarkdown(r2)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(m1, m2) {
				t.Fatal("Markdown report bytes differed across runs")
			}

			wantJSON, err := os.ReadFile(filepath.Clean(tc.reportJSONPath))
			if err != nil {
				t.Fatalf("read committed JSON report: %v", err)
			}
			if !bytes.Equal(j1, wantJSON) {
				t.Fatalf("committed %s does not match generated JSON bytes", tc.reportJSONPath)
			}
			wantMD, err := os.ReadFile(filepath.Clean(tc.reportMDPath))
			if err != nil {
				t.Fatalf("read committed Markdown report: %v", err)
			}
			if !bytes.Equal(m1, wantMD) {
				t.Fatalf("committed %s does not match generated Markdown bytes", tc.reportMDPath)
			}
		})
	}
}

func TestIsolatedObjectivePreservedInDenominator(t *testing.T) {
	rep, err := EvaluateFiles(shippedManifestPath, shippedCSCrosswalk, shippedCSGraphPath)
	if err != nil {
		t.Fatal(err)
	}
	foundIsolated := false
	for _, row := range rep.Mappings {
		if row.ObjectiveID == "1.1.A" {
			foundIsolated = true
			if row.Status != StatusMissing || !row.IsolatedUnrepresentable || len(row.Concepts) != 0 || len(row.EvidenceEdges) != 0 {
				t.Fatalf("isolated objective 1.1.A unexpected state: %+v", row)
			}
		}
	}
	if !foundIsolated {
		t.Fatal("1.1.A not found in report mappings")
	}
}

func TestShippedCrosswalkScopeEvidence(t *testing.T) {
	example, err := LoadCrosswalk(shippedCSCrosswalk)
	if err != nil {
		t.Fatal(err)
	}
	findRow := func(t *testing.T, c Crosswalk, id string) MappingRow {
		t.Helper()
		i := slices.IndexFunc(c.Mappings, func(row MappingRow) bool { return row.ObjectiveID == id })
		if i < 0 {
			t.Fatalf("missing objective %s", id)
		}
		return c.Mappings[i]
	}
	t.Run("1.4.B evaluates code for reading input", func(t *testing.T) {
		row := findRow(t, example, "1.4.B")
		if !strings.Contains(row.Rationale, "write Java code to read input") || !strings.Contains(row.Rationale, "any particular input form") {
			t.Fatalf("input rationale needs the objective and the input-form exclusion: %q", row.Rationale)
		}
	})
	t.Run("3.2.A cites reliability testing and records remaining gaps", func(t *testing.T) {
		row := findRow(t, example, "3.2.A")
		wantEdge := EvidenceEdge{Subject: "Unit Testing", Relation: "depends_on", Object: "Functions"}
		if row.Status != StatusPartial || row.IsolatedUnrepresentable || !slices.Contains(row.EvidenceEdges, wantEdge) {
			t.Fatalf("reliability topic needs partial coverage with the testing edge: %+v", row)
		}
		for _, gap := range []string{"ethical", "licensing", "social"} {
			if !strings.Contains(row.Rationale, gap) {
				t.Errorf("3.2.A rationale omits %s gap: %q", gap, row.Rationale)
			}
		}
	})
	seed, err := LoadCrosswalk(shippedSeedCrosswalk)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Crosswalk{example, seed} {
		t.Run(c.GraphPath+" documents the scope of AVL trees", func(t *testing.T) {
			i := slices.IndexFunc(c.OutsideCourse, func(row OutsideCourseConcept) bool { return row.Concept == "AVL Trees" })
			if i < 0 || !strings.Contains(c.OutsideCourse[i].Reason, "1D and 2D arrays") || !strings.Contains(c.OutsideCourse[i].Reason, "balancing") {
				t.Fatal("AVL exclusion needs the Unit 4 collection boundary and the specific tree operation")
			}
		})
	}
	t.Run("missing seed objectives give concrete gaps", func(t *testing.T) {
		seen := map[string]string{}
		for _, row := range seed.Mappings {
			if row.Status != StatusMissing {
				continue
			}
			if other, exists := seen[row.Rationale]; exists {
				t.Errorf("seed objectives %s and %s share a missing rationale without a concrete gap", other, row.ObjectiveID)
			}
			seen[row.Rationale] = row.ObjectiveID
		}
	})
}

func TestSeparateEditionReproducibility(t *testing.T) {
	m, err := LoadManifest(shippedManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadCrosswalk(shippedSeedCrosswalk)
	if err != nil {
		t.Fatal(err)
	}
	seedBytes, err := os.ReadFile(filepath.Clean(shippedSeedGraphPath))
	if err != nil {
		t.Fatal(err)
	}

	baseline, err := EvaluateBytes(m, c, seedBytes, c.GraphPath)
	if err != nil {
		t.Fatal(err)
	}
	baselineJSON, err := RenderReportJSON(baseline)
	if err != nil {
		t.Fatal(err)
	}

	// Pin a hypothetical later edition independently without mutating the Fall 2025 result.
	later := m
	later.Edition = "ap-csa-fall-2026"
	later.EffectiveTerm = "Fall 2026"
	laterCrosswalk := c
	laterCrosswalk.Edition = "ap-csa-fall-2026"
	laterReport, err := EvaluateBytes(later, laterCrosswalk, seedBytes, c.GraphPath)
	if err != nil {
		t.Fatalf("later edition evaluation failed: %v", err)
	}
	if laterReport.Edition != "ap-csa-fall-2026" {
		t.Fatalf("later edition = %q", laterReport.Edition)
	}

	recheck, err := EvaluateFiles(shippedManifestPath, shippedSeedCrosswalk, shippedSeedGraphPath)
	if err != nil {
		t.Fatal(err)
	}
	recheckJSON, err := RenderReportJSON(recheck)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(baselineJSON, recheckJSON) {
		t.Fatal("earlier edition report changed after evaluating a separate edition")
	}
}

func TestReportDeterministicAcrossInputPermutationsProperty(t *testing.T) {
	m, err := LoadManifest(shippedManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadCrosswalk(shippedCSCrosswalk)
	if err != nil {
		t.Fatal(err)
	}
	graphBytes, err := os.ReadFile(filepath.Clean(shippedCSGraphPath))
	if err != nil {
		t.Fatal(err)
	}
	wantReport, err := EvaluateBytes(m, c, graphBytes, c.GraphPath)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := RenderReportJSON(wantReport)
	if err != nil {
		t.Fatal(err)
	}
	wantMD, err := RenderReportMarkdown(wantReport)
	if err != nil {
		t.Fatal(err)
	}

	rapid.Check(t, func(rt *rapid.T) {
		permCrosswalk := c
		permCrosswalk.Mappings = rapid.Permutation(slices.Clone(c.Mappings)).Draw(rt, "mappings")
		for i := range permCrosswalk.Mappings {
			row := permCrosswalk.Mappings[i]
			row.Concepts = rapid.Permutation(slices.Clone(row.Concepts)).Draw(rt, "concepts")
			row.EvidenceEdges = rapid.Permutation(slices.Clone(row.EvidenceEdges)).Draw(rt, "edges")
			row.Disputes = rapid.Permutation(slices.Clone(row.Disputes)).Draw(rt, "disputes")
			permCrosswalk.Mappings[i] = row
		}
		permCrosswalk.OutsideCourse = rapid.Permutation(slices.Clone(c.OutsideCourse)).Draw(rt, "outside_course")

		gotReport, err := EvaluateBytes(m, permCrosswalk, graphBytes, c.GraphPath)
		if err != nil {
			rt.Fatalf("EvaluateBytes on permuted crosswalk: %v", err)
		}
		gotJSON, err := RenderReportJSON(gotReport)
		if err != nil {
			rt.Fatal(err)
		}
		gotMD, err := RenderReportMarkdown(gotReport)
		if err != nil {
			rt.Fatal(err)
		}
		if !bytes.Equal(gotJSON, wantJSON) {
			rt.Fatal("JSON report varied with crosswalk input permutation")
		}
		if !bytes.Equal(gotMD, wantMD) {
			rt.Fatal("Markdown report varied with crosswalk input permutation")
		}
	})
}
