package quality_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"

	"github.com/Victoroide/axiom-engine/evaluation/quality"
)

type fixturePlanner map[string][]domain.PlanItem

func (p fixturePlanner) GenerateTopic(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
	return p[req.Topic], nil
}

func loadAllFixtures(t *testing.T) (
	quality.ObjectiveCatalog,
	map[string]quality.ReferenceFact,
	quality.BenchmarkSuite,
	*graph.InMemoryGraph,
	*graph.InMemoryGraph,
) {
	t.Helper()
	cat, err := quality.ReadStrictJSONFile[quality.ObjectiveCatalog]("testdata", "objectives.json")
	if err != nil {
		t.Fatalf("read objectives.json: %v", err)
	}
	if err := quality.ValidateObjectiveCatalog(cat); err != nil {
		t.Fatalf("validate objectives: %v", err)
	}

	factDoc, err := quality.ReadStrictJSONFile[quality.ReferenceFactCatalog]("testdata", "reference_facts.json")
	if err != nil {
		t.Fatalf("read reference_facts.json: %v", err)
	}
	facts, err := quality.ValidateReferenceFacts(factDoc, cat)
	if err != nil {
		t.Fatalf("validate reference facts: %v", err)
	}

	suite, err := quality.ReadStrictJSONFile[quality.BenchmarkSuite]("testdata", "cases.json")
	if err != nil {
		t.Fatalf("read cases.json: %v", err)
	}
	seedGraph := graph.NewInMemoryGraph()
	exampleGraph, err := graph.Load(filepath.Join("..", "..", "examples", "graphs", "computer-science.json"))
	if err != nil {
		t.Fatalf("load computer-science.json: %v", err)
	}
	if err := quality.ValidateSuite(suite, cat, facts, seedGraph, exampleGraph); err != nil {
		t.Fatalf("validate suite: %v", err)
	}
	return cat, facts, suite, seedGraph, exampleGraph
}

func TestObjectiveCatalogAndProvisionalFacts(t *testing.T) {
	cat, facts, _, _, _ := loadAllFixtures(t)
	if cat.TotalObjectives != 73 || len(cat.Objectives) != 73 {
		t.Fatalf("expected 73 objectives, got %d / %d", cat.TotalObjectives, len(cat.Objectives))
	}
	for id, fact := range facts {
		if fact.Status != quality.ProvisionalFactStatus {
			t.Fatalf("fact %s status=%q, want provisional", id, fact.Status)
		}
	}

	// Verify structural Java snippet checks on valid and malformed Java inputs.
	validSnippets := []string{
		`int a = 1 / 2;`,
		`if (a > 0) { System.out.println("ok // not comment"); } /* ok */`,
		`char quote = '\''; String s = "x\"y";`,
	}
	for _, s := range validSnippets {
		if err := quality.ValidateJavaSnippet(s); err != nil {
			t.Fatalf("valid snippet rejected (%q): %v", s, err)
		}
	}
	badSnippets := []string{
		``,
		`int a = (1 + 2;`,
		`if (true) { x = 1;`,
		`String s = "unterminated;`,
		`/* unclosed comment int x = 1;`,
		`plain English text without code delimiters`,
	}
	for _, s := range badSnippets {
		if err := quality.ValidateJavaSnippet(s); err == nil {
			t.Fatalf("malformed snippet unexpectedly accepted: %q", s)
		}
	}
}

func TestCalibrationMaterialCannotBeDesignated(t *testing.T) {
	cat, facts, suite, seed, example := loadAllFixtures(t)
	if _, err := quality.BuildBlindArtifacts(suite, facts); err != nil {
		t.Fatalf("control packet: %v", err)
	}

	cal := suite.CalibrationCases[0]
	c := &suite.EvaluationCases[0]
	c.Unit, c.TopicID, c.TopicTitle = cal.Unit, cal.TopicID, cal.TopicTitle
	c.ObjectiveIDs, c.ReferenceFactIDs = cal.ObjectiveIDs, cal.ReferenceFactIDs
	c.PromptContext, c.Request, c.ShippedGraph = cal.PromptContext, cal.Request, cal.ShippedGraph
	c.Request.StudentID = c.CandidateResponse.StudentID
	c.CandidateResponse.Items, c.ResourceEvidence = cal.CandidateResponse.Items, cal.ResourceEvidence
	if c.ID == cal.ID || c.Request.StudentID == cal.Request.StudentID || cal.WorkedAnchor.Rationale == "" {
		t.Fatal("fixture must have different administrative IDs and a worked rationale")
	}

	checkOverlap := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, quality.ErrValidation) || !strings.Contains(err.Error(), "repeats the material") ||
			!strings.Contains(err.Error(), c.ID) || !strings.Contains(err.Error(), cal.ID) {
			t.Fatalf("overlap between %s and %s returned %v", cal.ID, c.ID, err)
		}
	}
	t.Run("suite", func(t *testing.T) {
		checkOverlap(t, quality.ValidateSuite(suite, cat, facts, seed, example))
	})
	t.Run("builder", func(t *testing.T) {
		packet, err := quality.BuildBlindArtifacts(suite, facts)
		checkOverlap(t, err)
		if len(packet.RaterABlank.Rows) != 0 || len(packet.RaterBBlank.Rows) != 0 {
			t.Fatal("builder returned rating sheets containing worked calibration material")
		}
	})
}

func TestBenchmarkSuiteAndShippedGraphServability(t *testing.T) {
	_, _, suite, seedGraph, exampleGraph := loadAllFixtures(t)

	if len(suite.CalibrationCases) != 6 {
		t.Fatalf("calibration cases = %d, want 6", len(suite.CalibrationCases))
	}
	if len(suite.EvaluationCases) != 28 {
		t.Fatalf("evaluation cases = %d, want 28", len(suite.EvaluationCases))
	}

	var seedCount, exampleCount int
	var limitTested bool
	var structurallyValidWrongTested int

	for _, c := range suite.EvaluationCases {
		if c.ShippedGraph.SeedGraphServable {
			seedCount++
			resp := verifyServableThroughPipeline(t, seedGraph, c.ID, c.ShippedGraph.TargetNode, c.Request, c.CandidateResponse.Items)
			if c.ShippedGraph.ResponseGraph == quality.ResponseGraphSeed {
				verifyRatedItems(t, c.ID, c.CandidateResponse.Items, resp.Items)
			}
		}
		if c.ShippedGraph.ExampleGraphServable {
			exampleCount++
			resp := verifyServableThroughPipeline(t, exampleGraph, c.ID, c.ShippedGraph.TargetNode, c.Request, c.CandidateResponse.Items)
			verifyRatedItems(t, c.ID, c.CandidateResponse.Items, resp.Items)
			if slices.Contains(c.ScenarioTags, "service_limits") {
				limitTested = true
				item := resp.Items[0]
				if got := utf8.RuneCountInString(item.Explanation); got != 1000 {
					t.Fatalf("%s explanation runes = %d, want 1000", c.ID, got)
				}
				if len(item.Resources) != 3 {
					t.Fatalf("%s resources = %d, want 3", c.ID, len(item.Resources))
				}
				if got := utf8.RuneCountInString(item.Resources[0].Title); got != 200 {
					t.Fatalf("%s resource[0].Title runes = %d, want 200", c.ID, got)
				}
				if got := len(item.Resources[0].URL); got != 2048 {
					t.Fatalf("%s resource[0].URL len = %d, want 2048", c.ID, got)
				}
			}
			if slices.Contains(c.ScenarioTags, "structurally_valid_wrong_answer") {
				structurallyValidWrongTested++
			}
		}
		// Verify wire DTO round-trip compatibility with strict JSON decoding.
		reqBytes, err := json.Marshal(c.Request)
		if err != nil {
			t.Fatalf("%s marshal request: %v", c.ID, err)
		}
		if _, err := quality.DecodeStrictJSON[domain.AdaptivePlanRequest](reqBytes); err != nil {
			t.Fatalf("%s round-trip request: %v", c.ID, err)
		}
		respBytes, err := json.Marshal(c.CandidateResponse)
		if err != nil {
			t.Fatalf("%s marshal response: %v", c.ID, err)
		}
		if _, err := quality.DecodeStrictJSON[domain.PlanResponse](respBytes); err != nil {
			t.Fatalf("%s round-trip response: %v", c.ID, err)
		}
	}

	if seedCount != 8 || exampleCount != 22 {
		t.Fatalf("shipped graph coverage seed=%d example=%d, want 8 and 22", seedCount, exampleCount)
	}
	if !limitTested {
		t.Fatal("service_limits boundary case was not exercised through pipeline")
	}
	if structurallyValidWrongTested < 5 {
		t.Fatalf("expected at least 5 structurally valid wrong answers tested through graph pipeline, got %d", structurallyValidWrongTested)
	}
	for _, c := range suite.CalibrationCases {
		switch c.ShippedGraph.ResponseGraph {
		case quality.ResponseGraphExample:
			resp := verifyServableThroughPipeline(t, exampleGraph, c.ID, c.ShippedGraph.TargetNode, c.Request, c.CandidateResponse.Items)
			verifyRatedItems(t, c.ID, c.CandidateResponse.Items, resp.Items)
		case quality.ResponseGraphSeed:
			resp := verifyServableThroughPipeline(t, seedGraph, c.ID, c.ShippedGraph.TargetNode, c.Request, c.CandidateResponse.Items)
			verifyRatedItems(t, c.ID, c.CandidateResponse.Items, resp.Items)
		}
	}
}

func verifyRatedItems(t *testing.T, id string, want, got []domain.PlanItem) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("case %s material differs from its reference graph response: got %+v, want %+v", id, got, want)
	}
}

func verifyServableThroughPipeline(t *testing.T, g *graph.InMemoryGraph, id, topic string, req domain.AdaptivePlanRequest, items []domain.PlanItem) domain.PlanResponse {
	t.Helper()
	planner := fixturePlanner{topic: items}
	svc := service.NewReasoningService(g, planner, service.Options{
		PromptBudget:      config.DefaultPromptBudget(),
		LLMTimeout:        5 * time.Second,
		MaxFailedTopics:   10,
		MaxMasteredTopics: 100,
		MaxConcurrency:    2,
		Logger:            slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	resp, err := svc.GeneratePlan(context.Background(), req)
	if err != nil {
		t.Fatalf("case %s failed ReasoningService pipeline on servable graph: %v", id, err)
	}
	if resp.Meta.ItemsDropped != 0 {
		t.Fatalf("case %s dropped %d items in pipeline, want 0", id, resp.Meta.ItemsDropped)
	}
	return resp
}
