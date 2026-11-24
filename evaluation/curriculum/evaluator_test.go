package curriculum

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/graph"
)

const shippedManifestPath = "editions/ap-csa-fall-2025/manifest.json"

func TestShippedManifestValidAndPinned(t *testing.T) {
	m, err := LoadManifest(shippedManifestPath)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Edition != APCSAFall2025Edition {
		t.Fatalf("edition = %q, want %q", m.Edition, APCSAFall2025Edition)
	}
	if m.Denominator != 73 || len(m.Objectives) != 73 {
		t.Fatalf("denominator=%d objectives=%d, want 73", m.Denominator, len(m.Objectives))
	}
	wantUnits := map[int]int{1: 28, 2: 14, 3: 10, 4: 21}
	gotUnits := map[int]int{}
	byID := map[string]Objective{}
	for _, obj := range m.Objectives {
		gotUnits[obj.Unit]++
		byID[obj.ID] = obj
	}
	for unit, want := range wantUnits {
		if gotUnits[unit] != want {
			t.Fatalf("unit %d count = %d, want %d", unit, gotUnits[unit], want)
		}
	}
	// Printed learning objective IDs in the Fall 2025 CED, sorted and LF-terminated.
	const wantRoster = "f77e6d09d4fbf3ac08146fdbf6cd7717ee1df2657b835dc33cf61df59ef51050"
	ids := make([]string, 0, len(m.Objectives))
	for _, obj := range m.Objectives {
		ids = append(ids, obj.ID)
	}
	slices.Sort(ids)
	if got := sha256.Sum256([]byte(strings.Join(ids, "\n") + "\n")); hex.EncodeToString(got[:]) != wantRoster {
		t.Fatalf("Fall 2025 objective roster digest = %x, want %s", got, wantRoster)
	}

	// Continuation objectives must be counted once with multi-page citations.
	for _, id := range []string{"1.1.C", "1.3.C", "1.5.B", "1.9.B", "1.13.C", "1.15.A", "1.15.B", "3.1.A", "3.5.A", "4.6.A", "4.7.A", "4.8.A", "4.11.A", "4.17.C"} {
		obj, ok := byID[id]
		if !ok {
			t.Fatalf("missing continuation objective %s", id)
		}
		if obj.SourceEndPage <= obj.SourcePage {
			t.Fatalf("objective %s expected multi-page range, got %d..%d", id, obj.SourcePage, obj.SourceEndPage)
		}
	}

	digests := map[string]string{}
	for _, src := range m.Sources {
		digests[src.ID] = src.SHA256
	}
	if digests["ced"] != APCSACourseDescriptionSHA256 {
		t.Fatalf("ced digest = %q, want %q", digests["ced"], APCSACourseDescriptionSHA256)
	}
	if digests["clarifications"] != APCSAClarificationsSHA256 {
		t.Fatalf("clarifications digest = %q, want %q", digests["clarifications"], APCSAClarificationsSHA256)
	}

	raw, err := os.ReadFile(filepath.Clean(shippedManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"ESSENTIAL KNOWLEDGE", "SUGGESTED SKILLS", "EXCLUSION STATEMENT", "Sample Exam Questions", `"subject":`, `"relation":`, `"object":`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("manifest contains forbidden verbatim table or graph structure %q", forbidden)
		}
	}
}

func TestShippedManifestDistinguishesCourseSemantics(t *testing.T) {
	m, err := LoadManifest(shippedManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for id, terms := range map[string][]string{
		"2.2.A": {"numeric ordering", "reference identity"},
		"4.7.A": {"boxing and unboxing", "parse strings"},
	} {
		t.Run(id, func(t *testing.T) {
			i := slices.IndexFunc(m.Objectives, func(obj Objective) bool { return obj.ID == id })
			if i < 0 {
				t.Fatalf("missing objective %s", id)
			}
			for _, term := range terms {
				if !strings.Contains(m.Objectives[i].Summary, term) {
					t.Errorf("summary must distinguish %q: %s", term, m.Objectives[i].Summary)
				}
			}
		})
	}
	t.Run("January revision distinguishes implemented and planned changes", func(t *testing.T) {
		i := slices.IndexFunc(m.Sources, func(src SourceDocument) bool { return src.ID == "clarifications" })
		if i < 0 {
			t.Fatal("missing clarifications source")
		}
		for _, term := range []string{"career-preparation wording implemented", "resource links and Progress Check edits announced"} {
			if !strings.Contains(m.Sources[i].Revision, term) {
				t.Errorf("clarification revision must distinguish %q: %s", term, m.Sources[i].Revision)
			}
		}
	})
}

func TestRejectsInvalidManifestsAndCrosswalks(t *testing.T) {
	baseManifest := func() Manifest {
		return Manifest{
			SchemaVersion:       "1.0.0",
			Edition:             "sample-2025",
			Framework:           "Sample Course",
			EffectiveTerm:       "Fall 2025",
			Language:            "en",
			ProgrammingLanguage: "Java",
			PageReference:       "Printed page",
			RedistributionNote:  "Original summaries only.",
			Denominator:         2,
			ExpectedUnitCounts:  map[int]int{1: 1, 2: 1},
			Sources: []SourceDocument{
				{
					ID:        "ced",
					Title:     "Sample CED",
					Publisher: "Publisher",
					URL:       "https://example.org/ced.pdf",
					Revision:  "2025",
					SHA256:    strings.Repeat("a", 64),
				},
			},
			Objectives: []Objective{
				{ID: "1.1.A", Unit: 1, UnitTitle: "Unit 1", Topic: "1.1", TopicTitle: "Topic 1", SourceID: "ced", SourcePage: 10, Summary: "First objective summary."},
				{ID: "2.1.A", Unit: 2, UnitTitle: "Unit 2", Topic: "2.1", TopicTitle: "Topic 2", SourceID: "ced", SourcePage: 20, Summary: "Second objective summary."},
			},
		}
	}
	graphBytes := []byte(`[{"subject":"Functions","relation":"depends_on","object":"Variables"},{"subject":"Arrays","relation":"depends_on","object":"Variables"}]`)
	sum := sha256.Sum256(graphBytes)
	graphHash := hex.EncodeToString(sum[:])

	baseCrosswalk := func() Crosswalk {
		return Crosswalk{
			SchemaVersion:     "1.0.0",
			Edition:           "sample-2025",
			GraphPath:         "sample.json",
			GraphSHA256:       graphHash,
			Provisional:       true,
			ProvisionalNotice: "Provisional until specialist review.",
			ExpertReviewForm:  "evaluation/curriculum/dispute-form.md",
			Mappings: []MappingRow{
				{
					ObjectiveID:   "1.1.A",
					Status:        StatusMapped,
					Concepts:      []string{"Functions", "Variables"},
					EvidenceEdges: []EvidenceEdge{{Subject: "Functions", Relation: "depends_on", Object: "Variables"}},
					Rationale:     "Functions and Variables cover the entire first objective.",
				},
				{
					ObjectiveID: "2.1.A",
					Status:      StatusMissing,
					Rationale:   "No matching concept in the sample graph.",
				},
			},
			OutsideCourse: []OutsideCourseConcept{
				{Concept: "Arrays", Category: "beyond_course_scope", Reason: "Out of scope for sample course."},
			},
		}
	}

	if _, err := EvaluateBytes(baseManifest(), baseCrosswalk(), graphBytes, "sample.json"); err != nil {
		t.Fatalf("valid baseline failed: %v", err)
	}

	t.Run("duplicate objective in manifest", func(t *testing.T) {
		m := baseManifest()
		m.Objectives[1].ID = "1.1.A"
		if err := ValidateManifest(m); err == nil {
			t.Fatal("expected error for duplicate manifest objective")
		}
	})

	t.Run("wrong pinned digest for AP CSA", func(t *testing.T) {
		m, err := LoadManifest(shippedManifestPath)
		if err != nil {
			t.Fatal(err)
		}
		m.Sources[0].SHA256 = strings.Repeat("0", 64)
		if err := ValidateManifest(m); err == nil {
			t.Fatal("expected error for altered AP CSA source digest")
		}
	})

	t.Run("duplicate objective in crosswalk", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings[1].ObjectiveID = "1.1.A"
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected duplicate crosswalk objective error")
		}
	})

	t.Run("absent objective in crosswalk", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings = c.Mappings[:1]
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected absent objective error")
		}
	})

	t.Run("unknown objective ID", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings[1].ObjectiveID = "9.9.Z"
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected unknown objective ID error")
		}
	})

	t.Run("nonexistent node in concepts", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings[0].Concepts = append(c.Mappings[0].Concepts, "MissingNode")
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected nonexistent node error")
		}
	})

	t.Run("nonexistent node in evidence_edges", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings[0].EvidenceEdges = append(c.Mappings[0].EvidenceEdges, EvidenceEdge{Subject: "MissingNode", Relation: "depends_on", Object: "Variables"})
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected nonexistent node in evidence_edges error")
		}
	})

	t.Run("nonexistent node in outside_course", func(t *testing.T) {
		c := baseCrosswalk()
		c.OutsideCourse = append(c.OutsideCourse, OutsideCourseConcept{
			Concept:  "GhostNode",
			Category: "beyond_course_scope",
			Reason:   "Absent from graph.",
		})
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected nonexistent node in outside_course error")
		}
	})

	t.Run("concept in both evidence and outside_course", func(t *testing.T) {
		c := baseCrosswalk()
		c.OutsideCourse = append(c.OutsideCourse, OutsideCourseConcept{
			Concept:  "Variables",
			Category: "beyond_course_scope",
			Reason:   "Overlap with mapped concept.",
		})
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected overlap error between course evidence and outside_course")
		}
	})

	t.Run("unaccounted graph concept", func(t *testing.T) {
		c := baseCrosswalk()
		c.OutsideCourse = nil
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected unaccounted graph concept error")
		}
	})

	t.Run("mapped rejects unresolved dispute", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings[0].Disputes = []Dispute{{
			ID:        "DSP-OPEN",
			Kind:      "disputed_prerequisite",
			Status:    DisputeUnresolved,
			Rationale: "Needs specialist review.",
		}}
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected mapped objective with unresolved dispute to fail")
		}
	})

	for _, field := range []string{"omitted", "false"} {
		t.Run("pending review when provisional is "+field, func(t *testing.T) {
			c := baseCrosswalk()
			c.Provisional = false
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if field == "omitted" {
				raw = bytes.Replace(raw, []byte(`"provisional":false,`), nil, 1)
			}
			c, err = ParseCrosswalk(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			report, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json")
			if err != nil {
				t.Fatal(err)
			}
			if !report.Provisional || !report.Mappings[0].Provisional || !report.Mappings[1].Provisional {
				t.Fatal("crosswalk without specialist sign-off must remain provisional")
			}
			markdown, err := RenderReportMarkdown(report)
			if err != nil {
				t.Fatal(err)
			}
			const notice = "Subject-specialist verification is pending."
			if !strings.Contains(report.ProvisionalNotice, notice) || !bytes.Contains(markdown, []byte(notice)) {
				t.Fatalf("pending subject-specialist review notice missing from report: %q", report.ProvisionalNotice)
			}
		})
	}

	for _, status := range []string{StatusMapped, StatusPartial} {
		t.Run(status+" rejects rejected evidence", func(t *testing.T) {
			c := baseCrosswalk()
			c.Mappings[0].Status = status
			c.Mappings[0].Disputes = []Dispute{{
				ID:        "DSP-REJECTED",
				Edge:      &EvidenceEdge{Subject: " functions ", Relation: "depends_on", Object: "variables"},
				Kind:      "disputed_prerequisite",
				Status:    DisputeRejected,
				Rationale: "Does not establish instructional ordering.",
			}}
			if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil || !strings.Contains(err.Error(), "rejected evidence edge") {
				t.Fatalf("expected rejected evidence edge error, got %v", err)
			}
			c.Mappings[0].EvidenceEdges = nil
			if status == StatusMapped {
				if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil || !strings.Contains(err.Error(), "rejected dispute") {
					t.Fatalf("expected rejected dispute to prevent full mapping, got %v", err)
				}
				return
			}
			report, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json")
			if err != nil {
				t.Fatalf("independent partial concept evidence should pass: %v", err)
			}
			row := report.Mappings[0]
			if len(row.EvidenceEdges) != 0 || len(row.Disputes) != 1 || row.Disputes[0].Status != DisputeRejected || report.EvidenceRelations.DependsOn != 0 {
				t.Fatalf("rejected edge must remain visible only as a dispute: %+v", row)
			}
		})
	}

	t.Run("course-wide rejection of a shared edge", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings[1].Disputes = []Dispute{{
			ID:        "DSP-SHARED",
			Edge:      &EvidenceEdge{Subject: " functions ", Relation: "depends_on", Object: "VARIABLES"},
			Kind:      "disputed_prerequisite",
			Status:    DisputeRejected,
			Rationale: "Functions do not require Variables as an instructional prerequisite in this course.",
		}}

		t.Run("mapped evidence on an earlier objective", func(t *testing.T) {
			if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil || !strings.Contains(err.Error(), `objective "1.1.A" cites rejected evidence edge`) {
				t.Fatalf("expected the later objective's rejection to block mapped evidence, got %v", err)
			}
		})
		c.Mappings[0].Status = StatusPartial
		t.Run("partial evidence with reversed input rows", func(t *testing.T) {
			reversed := c
			reversed.Mappings = slices.Clone(c.Mappings)
			slices.Reverse(reversed.Mappings)
			if _, err := EvaluateBytes(baseManifest(), reversed, graphBytes, "sample.json"); err == nil || !strings.Contains(err.Error(), `objective "1.1.A" cites rejected evidence edge`) {
				t.Fatalf("expected the rejection to block partial evidence in either row order, got %v", err)
			}
		})

		c.Mappings[0].EvidenceEdges = nil
		report, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json")
		if err != nil {
			t.Fatalf("independent concept evidence should still support partial coverage: %v", err)
		}
		if report.StatusCounts.Partial != 1 || report.EvidenceRelations.DependsOn != 0 || report.Graph.DependsOnEdges != 2 {
			t.Fatalf("unexpected coverage or graph counts: %+v, %+v, %+v", report.StatusCounts, report.EvidenceRelations, report.Graph)
		}
		wantEdge := EvidenceEdge{Subject: "Functions", Relation: "depends_on", Object: "Variables"}
		disputes := report.Mappings[1].Disputes
		if len(disputes) != 1 || disputes[0].Status != DisputeRejected || disputes[0].Edge == nil || *disputes[0].Edge != wantEdge {
			t.Fatalf("expected the canonical rejected edge in the reporting objective's disputes: %+v", disputes)
		}
		c.Mappings[0].EvidenceEdges = []EvidenceEdge{wantEdge}
		c.Mappings[1].Disputes[0].Edge = nil
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err != nil {
			t.Fatalf("a rejection without an edge should stay on its own objective: %v", err)
		}
	})

	t.Run("course-wide unresolved dispute on a shared edge", func(t *testing.T) {
		wantEdge := EvidenceEdge{Subject: "Functions", Relation: "depends_on", Object: "Variables"}
		for i, origin := range []string{"1.1.A", "2.1.A"} {
			t.Run("filed under "+origin, func(t *testing.T) {
				c := baseCrosswalk()
				c.Mappings[1].Status = StatusPartial
				c.Mappings[1].Concepts = []string{"Functions", "Variables"}
				c.Mappings[1].EvidenceEdges = []EvidenceEdge{wantEdge}
				c.Mappings[1].Rationale = "Functions and Variables support part of the second objective."
				c.Mappings[i].Disputes = []Dispute{{
					ID:        "DSP-SHARED-OPEN",
					Edge:      &EvidenceEdge{Subject: " functions ", Relation: "depends_on", Object: "VARIABLES"},
					Kind:      "disputed_prerequisite",
					Status:    DisputeUnresolved,
					Rationale: "Needs specialist review for this course.",
				}}
				_, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json")
				if err == nil || !strings.Contains(err.Error(), `objective "1.1.A" cites unresolved evidence edge`) ||
					!strings.Contains(err.Error(), `dispute "DSP-SHARED-OPEN" on objective "`+origin+`"`) {
					t.Errorf("expected the cited edge to block full coverage and identify its dispute, got %v", err)
				}

				c.Mappings[0].Status = StatusPartial
				report, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json")
				if err != nil {
					t.Fatalf("partial coverage should retain the unresolved edge: %v", err)
				}
				for _, row := range report.Mappings {
					if row.Status != StatusPartial || !slices.Equal(row.EvidenceEdges, []EvidenceEdge{wantEdge}) {
						t.Errorf("objective %q lost its partial coverage or shared edge: %+v", row.ObjectiveID, row)
					}
				}
				disputes := report.UnresolvedDisputes
				if len(disputes) != 1 || disputes[0].ObjectiveID != origin || disputes[0].DisputeID != "DSP-SHARED-OPEN" ||
					disputes[0].Edge == nil || *disputes[0].Edge != wantEdge {
					t.Errorf("expected the canonical unresolved edge on its originating objective, got %+v", disputes)
				}

				if i == 1 {
					c.Mappings[0].Status = StatusMapped
					c.Mappings[0].EvidenceEdges = nil
					if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err != nil {
						t.Errorf("independent concept evidence should still support full coverage: %v", err)
					}
					c.Mappings[0].EvidenceEdges = []EvidenceEdge{wantEdge}
					c.Mappings[1].Disputes[0].Edge = nil
					if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err != nil {
						t.Errorf("an unresolved dispute without an edge should stay on its own objective: %v", err)
					}
				}
			})
		}
	})

	t.Run("partial requires supporting evidence", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings[1].Status = StatusPartial
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil || !strings.Contains(err.Error(), "must cite at least one graph concept") {
			t.Fatalf("expected unsupported partial row to fail, got %v", err)
		}
	})

	t.Run("isolated row with evidence still cannot be partial", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings[0].Status = StatusPartial
		c.Mappings[0].IsolatedUnrepresentable = true
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil || !strings.Contains(err.Error(), "isolated_unrepresentable must remain missing") {
			t.Fatalf("expected isolated partial row to fail, got %v", err)
		}
	})

	t.Run("oversized graph files reach the production byte limit", func(t *testing.T) {
		dir := t.TempDir()
		manifestPath := filepath.Join(dir, "manifest.json")
		crosswalkPath := filepath.Join(dir, "crosswalk.json")
		for path, value := range map[string]any{manifestPath: baseManifest(), crosswalkPath: baseCrosswalk()} {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		graphPath := filepath.Join(dir, "graph.json")
		oversized := append(slices.Clone(graphBytes), bytes.Repeat([]byte{' '}, 8*1024*1024)...)
		if err := os.WriteFile(graphPath, oversized, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := EvaluateFiles(manifestPath, crosswalkPath, graphPath); err == nil || !strings.Contains(err.Error(), "graph exceeds 8388608 bytes") {
			t.Fatalf("expected production graph byte limit to reject the file, got %v", err)
		}
	})

	t.Run("alias collision in concepts", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings[0].Concepts = []string{"Functions", "  functions ", "Variables"}
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected alias collision error")
		}
	})

	t.Run("alias collision in outside_course", func(t *testing.T) {
		c := baseCrosswalk()
		c.OutsideCourse = append(c.OutsideCourse, OutsideCourseConcept{
			Concept:  "arrays",
			Category: "beyond_course_scope",
			Reason:   "Duplicate alias.",
		})
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected outside_course alias collision error")
		}
	})

	t.Run("evidence pointing to nonexistent edge", func(t *testing.T) {
		for _, badEdge := range []EvidenceEdge{
			{Subject: "Variables", Relation: "depends_on", Object: "Functions"}, // reversed direction
			{Subject: "Functions", Relation: "is_a", Object: "Variables"},       // wrong relation
			{Subject: "Functions", Relation: "depends_on", Object: "Arrays"},    // absent pair
		} {
			c := baseCrosswalk()
			c.Mappings[0].EvidenceEdges = []EvidenceEdge{badEdge}
			if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
				t.Fatalf("expected nonexistent edge error for %+v", badEdge)
			}
		}
	})

	t.Run("dispute pointing to nonexistent edge", func(t *testing.T) {
		c := baseCrosswalk()
		c.Mappings[0].Status = StatusPartial
		c.Mappings[0].Disputes = []Dispute{{
			ID:        "DSP-BAD",
			Edge:      &EvidenceEdge{Subject: "Variables", Relation: "depends_on", Object: "Functions"},
			Kind:      "disputed_prerequisite",
			Status:    DisputeUnresolved,
			Rationale: "Invalid direction.",
		}}
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected error for dispute edge not in graph")
		}
	})

	t.Run("graph digest mismatch", func(t *testing.T) {
		c := baseCrosswalk()
		c.GraphSHA256 = strings.Repeat("b", 64)
		if _, err := EvaluateBytes(baseManifest(), c, graphBytes, "sample.json"); err == nil {
			t.Fatal("expected graph SHA-256 mismatch error")
		}
	})

	t.Run("graph canonical collision rejected", func(t *testing.T) {
		colliding := []byte(`[{"subject":"Functions","relation":"depends_on","object":"Variables"},{"subject":"functions","relation":"depends_on","object":"Variables"}]`)
		csum := sha256.Sum256(colliding)
		c := baseCrosswalk()
		c.GraphSHA256 = hex.EncodeToString(csum[:])
		if _, err := EvaluateBytes(baseManifest(), c, colliding, "colliding.json"); err == nil {
			t.Fatal("expected graph canonical collision error")
		}
	})
}

func TestTaxonomicIsAAndDependsOnSeparation(t *testing.T) {
	graphBytes := []byte(`[
		{"subject":"Square","relation":"is_a","object":"Rectangle"},
		{"subject":"Methods","relation":"depends_on","object":"Variables"}
	]`)
	g, err := graph.Decode(strings.NewReader(string(graphBytes)))
	if err != nil {
		t.Fatal(err)
	}
	// Runtime traverses both is_a and depends_on identically.
	if chain := g.GetPrerequisiteChain("Square"); len(chain) != 1 || chain[0] != "Rectangle" {
		t.Fatalf("runtime GetPrerequisiteChain(Square) = %v, want [Rectangle]", chain)
	}

	sum := sha256.Sum256(graphBytes)
	hash := hex.EncodeToString(sum[:])

	m := Manifest{
		SchemaVersion:       "1.0.0",
		Edition:             "taxonomic-fixture",
		Framework:           "Fixture",
		EffectiveTerm:       "Fall 2025",
		Language:            "en",
		ProgrammingLanguage: "Java",
		PageReference:       "Printed page",
		RedistributionNote:  "Original summary.",
		Denominator:         2,
		ExpectedUnitCounts:  map[int]int{1: 2},
		Sources:             []SourceDocument{{ID: "src", Title: "Src", Publisher: "Pub", URL: "https://example.org/doc.pdf", Revision: "1", SHA256: strings.Repeat("c", 64)}},
		Objectives: []Objective{
			{ID: "1.1.A", Unit: 1, UnitTitle: "Unit 1", Topic: "1.1", TopicTitle: "T1", SourceID: "src", SourcePage: 1, Summary: "Taxonomy example."},
			{ID: "1.2.A", Unit: 1, UnitTitle: "Unit 1", Topic: "1.2", TopicTitle: "T2", SourceID: "src", SourcePage: 2, Summary: "Prerequisite example."},
		},
	}

	// Attempting to count a taxonomic is_a edge alone as fully mapped must fail.
	bad := Crosswalk{
		SchemaVersion:     "1.0.0",
		Edition:           "taxonomic-fixture",
		GraphPath:         "fixture.json",
		GraphSHA256:       hash,
		Provisional:       true,
		ProvisionalNotice: "Provisional.",
		ExpertReviewForm:  "evaluation/curriculum/dispute-form.md",
		Mappings: []MappingRow{
			{
				ObjectiveID:   "1.1.A",
				Status:        StatusMapped,
				Concepts:      []string{"Square", "Rectangle"},
				EvidenceEdges: []EvidenceEdge{{Subject: "Square", Relation: "is_a", Object: "Rectangle"}},
				Rationale:     "Improperly treats taxonomic is_a as full instructional prerequisite coverage.",
			},
			{
				ObjectiveID:   "1.2.A",
				Status:        StatusMapped,
				Concepts:      []string{"Methods", "Variables"},
				EvidenceEdges: []EvidenceEdge{{Subject: "Methods", Relation: "depends_on", Object: "Variables"}},
				Rationale:     "Direct depends_on prerequisite.",
			},
		},
	}
	if _, err := EvaluateBytes(m, bad, graphBytes, "fixture.json"); err == nil {
		t.Fatal("expected rejection when mapped objective relies solely on is_a")
	}
	t.Run("detached prerequisite outside the course cannot support taxonomy", func(t *testing.T) {
		c := bad
		c.Mappings = []MappingRow{bad.Mappings[0], {ObjectiveID: "1.2.A", Status: StatusMissing, Rationale: "The entire prerequisite component is outside the course."}}
		c.Mappings[0].EvidenceEdges = append(slices.Clone(c.Mappings[0].EvidenceEdges), bad.Mappings[1].EvidenceEdges[0])
		c.OutsideCourse = []OutsideCourseConcept{
			{Concept: "Methods", Category: "outside", Reason: "Only the geometry component is in scope."},
			{Concept: "Variables", Category: "outside", Reason: "Only the geometry component is in scope."},
		}
		if _, err := EvaluateBytes(m, c, graphBytes, "fixture.json"); err == nil || !strings.Contains(err.Error(), "both endpoints must be cited concepts") {
			t.Fatalf("expected detached evidence edge error, got %v", err)
		}
	})

	// Keeping the disputed is_a edge visible as partial with an unresolved dispute passes.
	good := bad
	good.Mappings = []MappingRow{
		{
			ObjectiveID:   "1.1.A",
			Status:        StatusPartial,
			Concepts:      []string{"Square", "Rectangle"},
			EvidenceEdges: []EvidenceEdge{{Subject: "Square", Relation: "is_a", Object: "Rectangle"}},
			Rationale:     "Taxonomic relation does not establish instructional prerequisite order.",
			Disputes: []Dispute{{
				ID:        "DSP-ISA-1",
				Edge:      &EvidenceEdge{Subject: "Square", Relation: "is_a", Object: "Rectangle"},
				Kind:      "taxonomic_not_prerequisite",
				Status:    DisputeUnresolved,
				Rationale: "Runtime traverses is_a as a prerequisite, which is pedagogically disputed.",
			}},
		},
		good.Mappings[1],
	}
	rep, err := EvaluateBytes(m, good, graphBytes, "fixture.json")
	if err != nil {
		t.Fatalf("valid disputed is_a crosswalk failed: %v", err)
	}
	if rep.EvidenceRelations.DependsOn != 1 || rep.EvidenceRelations.IsA != 1 {
		t.Fatalf("evidence relations = %+v, want depends_on=1 is_a=1", rep.EvidenceRelations)
	}
	if rep.UnresolvedDisputesCount != 1 {
		t.Fatalf("unresolved disputes = %d, want 1", rep.UnresolvedDisputesCount)
	}
}

func TestCanonicalAliasResolutionAndDisconnectedComponents(t *testing.T) {
	graphBytes := []byte(`[
		{"subject":"Boolean Logic","relation":"depends_on","object":"Expressions"},
		{"subject":"Recursión","relation":"depends_on","object":"Funciones"},
		{"subject":"Straße","relation":"depends_on","object":"Variables"}
	]`)
	sum := sha256.Sum256(graphBytes)
	hash := hex.EncodeToString(sum[:])

	m := Manifest{
		SchemaVersion:       "1.0.0",
		Edition:             "alias-components",
		Framework:           "Fixture",
		EffectiveTerm:       "Fall 2025",
		Language:            "en",
		ProgrammingLanguage: "Java",
		PageReference:       "Printed page",
		RedistributionNote:  "Original summary.",
		Denominator:         1,
		ExpectedUnitCounts:  map[int]int{1: 1},
		Sources:             []SourceDocument{{ID: "src", Title: "Src", Publisher: "Pub", URL: "https://example.org/doc.pdf", Revision: "1", SHA256: strings.Repeat("d", 64)}},
		Objectives: []Objective{
			{ID: "1.1.A", Unit: 1, UnitTitle: "Unit 1", Topic: "1.1", TopicTitle: "T1", SourceID: "src", SourcePage: 1, Summary: "Alias resolution across disconnected components."},
		},
	}

	c := Crosswalk{
		SchemaVersion:     "1.0.0",
		Edition:           "alias-components",
		GraphPath:         "components.json",
		GraphSHA256:       hash,
		Provisional:       true,
		ProvisionalNotice: "Provisional.",
		ExpertReviewForm:  "evaluation/curriculum/dispute-form.md",
		Mappings: []MappingRow{
			{
				ObjectiveID: "1.1.A",
				Status:      StatusPartial,
				Concepts:    []string{"  boolean   logic ", "expressions", "RECURSIO\u0301N", " funciones ", "STRASSE"},
				EvidenceEdges: []EvidenceEdge{
					{Subject: "boolean  logic", Relation: "depends_on", Object: "EXPRESSIONS"},
					{Subject: "Recursio\u0301n", Relation: "depends_on", Object: "funciones"},
				},
				Rationale: "Resolves aliases via production canonicalizer across disconnected components.",
			},
		},
		OutsideCourse: []OutsideCourseConcept{
			{Concept: "variables", Category: "beyond_course_scope", Reason: "Excluded in this synthetic fixture."},
		},
	}

	rep, err := EvaluateBytes(m, c, graphBytes, "components.json")
	if err != nil {
		t.Fatalf("EvaluateBytes: %v", err)
	}
	if rep.Graph.ConnectedComponents != 3 {
		t.Fatalf("connected components = %d, want 3", rep.Graph.ConnectedComponents)
	}
	wantConcepts := []string{"Boolean Logic", "Expressions", "Funciones", "Recursión", "Straße"}
	gotConcepts := rep.Mappings[0].Concepts
	b1, err := json.Marshal(gotConcepts)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := json.Marshal(wantConcepts)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("normalized concepts = %s, want %s", b1, b2)
	}
}

func TestTupleFormatRejectsIsolatedNodes(t *testing.T) {
	// Edge-only tuple format rejects empty graphs and single-node self loops.
	for _, invalid := range []string{`[]`, `[{"subject":"Computing Ethics","relation":"depends_on","object":"Computing Ethics"}]`, `[{"subject":"Computing Ethics","relation":"depends_on","object":""}]`} {
		if _, err := graph.Decode(strings.NewReader(invalid)); err == nil {
			t.Fatalf("expected graph.Decode to reject isolated/invalid tuple input %s", invalid)
		}
	}
}

func TestBlankDisputeFormHasRequiredSectionsAndUnconfirmedState(t *testing.T) {
	raw, err := os.ReadFile("dispute-form.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{"Independent Subject Expertise", "Source Evidence", "Conclusion", "Rationale", "Pending"} {
		if !strings.Contains(text, required) {
			t.Fatalf("dispute-form.md missing %q", required)
		}
	}
	if regexp.MustCompile(`(?m)^[ \t]*[-+*][ \t]+\[[xX]\]`).MatchString(text) {
		t.Fatal("blank dispute form must leave every checkbox unchecked")
	}
}

func TestMarkdownRendersJavaTypeParametersAndOneTerminalNewline(t *testing.T) {
	md, err := RenderReportMarkdown(Report{Mappings: []ReportMapping{{Summary: "Create ArrayList<E> values."}}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(md, []byte("Create ArrayList&lt;E&gt; values.")) {
		t.Error("Markdown must escape Java type parameters in plain-text table cells")
	}
	if !bytes.HasSuffix(md, []byte("\n")) || bytes.HasSuffix(md, []byte("\n\n")) {
		t.Error("Markdown must end with one newline")
	}
}
