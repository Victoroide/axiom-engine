package quality_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Victoroide/axiom-engine/evaluation/quality"
)

func TestRatingProvenance(t *testing.T) {
	_, key, _, _, _, syn := loadScoringFixtures(t)
	a, b, adj := cloneSubmission(syn.PassingArithmeticSet)
	report, err := quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{})
	if err != nil || report.MeasuredCandidate || report.DatasetKind != quality.DatasetKindSyntheticRegression {
		t.Fatalf("default synthetic evaluation: kind=%q, measured=%v, err=%v", report.DatasetKind, report.MeasuredCandidate, err)
	}
	if _, err := quality.Evaluate(key, a, b, adj, quality.DatasetKindMeasuredCandidate, quality.ScoreOptions{}); !errors.Is(err, quality.ErrValidation) {
		t.Fatalf("override of synthetic provenance returned %v", err)
	}
	b.DatasetKind = quality.DatasetKindMeasuredCandidate
	if _, err := quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{}); !errors.Is(err, quality.ErrValidation) {
		t.Fatalf("mixed sheet provenance returned %v", err)
	}
	a.DatasetKind, adj.DatasetKind = b.DatasetKind, b.DatasetKind
	if _, err := quality.Evaluate(key, a, b, adj, b.DatasetKind, quality.ScoreOptions{}); !errors.Is(err, quality.ErrValidation) {
		t.Fatalf("three reclassified sheets from synthetic packet returned %v", err)
	}
	key.DatasetKind = quality.DatasetKindMeasuredCandidate
	if _, err := quality.Evaluate(key, a, b, adj, b.DatasetKind, quality.ScoreOptions{}); !errors.Is(err, quality.ErrValidation) {
		t.Fatalf("reclassified identity key with synthetic sources returned %v", err)
	}
}

// loadCandidateSuite returns the designated roster relabelled as an unseen candidate packet.
func loadCandidateSuite(t *testing.T) (quality.BenchmarkSuite, map[string]quality.ReferenceFact) {
	t.Helper()
	_, facts, suite, _, _ := loadAllFixtures(t)
	suite.DatasetKind = quality.DatasetKindMeasuredCandidate
	for i, c := range suite.EvaluationCases {
		// Stand-in metadata for a fresh candidate packet in this unit test.
		c.Provenance.SourceType = quality.SourceUnseenCandidate
		c.Provenance.ModelVersionOrDate = "example-candidate-version"
		c.Origin.SeededCriticalError = false
		c.Origin.SeededCriticalType = quality.CriticalTypeNone
		c.CandidateResponse.Items[0].Explanation = "Example candidate text for " + c.TopicTitle + "."
		suite.EvaluationCases[i] = c
	}
	return suite, facts
}

// eligibleCandidate returns a candidate packet with the synthetic passing ratings copied onto it.
func eligibleCandidate(t *testing.T) (quality.BlindArtifacts, quality.RatingSheet, quality.RatingSheet, quality.AdjudicationSheet) {
	t.Helper()
	suite, facts := loadCandidateSuite(t)
	_, _, _, _, _, syn := loadScoringFixtures(t)
	packet, err := quality.BuildBlindArtifacts(suite, facts)
	if err != nil {
		t.Fatal(err)
	}
	a, b, adj := cloneSubmission(syn.PassingArithmeticSet)
	a.DatasetKind, b.DatasetKind, adj.DatasetKind = suite.DatasetKind, suite.DatasetKind, suite.DatasetKind
	for i := range a.Rows {
		ra, rb := packet.RaterABlank.Rows[i], packet.RaterBBlank.Rows[i]
		ra.Rating, rb.Rating = a.Rows[i].Rating, b.Rows[i].Rating
		a.Rows[i], b.Rows[i] = ra, rb
		adj.Rows[i].ResponseID = ra.ResponseID
	}
	return packet, a, b, adj
}

func TestCandidateProvenanceAndFinalEligibility(t *testing.T) {
	packet, a, b, adj := eligibleCandidate(t)
	kind := quality.DatasetKindMeasuredCandidate
	rep, err := quality.Evaluate(packet.IdentityKey, a, b, adj, kind, quality.ScoreOptions{AllowIncomplete: true, BootstrapRuns: 100})
	if err != nil || !rep.MeasuredCandidate || rep.Gate.Status != quality.StatusPass {
		t.Fatalf("eligible candidate submission: measured=%v gate=%+v err=%v", rep.MeasuredCandidate, rep.Gate, err)
	}
	adj.Rows = packet.Adjudication.Rows
	b.Locked = false
	rep, err = quality.Evaluate(packet.IdentityKey, a, b, adj, kind, quality.ScoreOptions{AllowIncomplete: true})
	if err != nil || rep.MeasuredCandidate || rep.Gate.Status != quality.StatusInconclusive {
		t.Fatalf("candidate draft: measured=%v gate=%+v err=%v", rep.MeasuredCandidate, rep.Gate, err)
	}
}

func TestCandidateRequiresCourseCoverage(t *testing.T) {
	kind := quality.DatasetKindMeasuredCandidate
	opts := quality.ScoreOptions{BootstrapRuns: 100}
	suite, facts := loadCandidateSuite(t)
	packet, sourceA, sourceB, sourceAdj := eligibleCandidate(t)
	for _, tc := range []struct {
		name    string
		keep    func(unit int, id string) bool
		wantErr string
	}{
		{"two units", func(unit int, _ string) bool { return unit <= 2 }, "at least 24 evaluation cases"},
		{"only five in one unit", func(unit int, id string) bool {
			return unit != 4 || (!strings.HasSuffix(id, "-06") && !strings.HasSuffix(id, "-07"))
		}, "unit 4 has 5 evaluation cases"},
		{"six per unit", func(_ int, id string) bool { return !strings.HasSuffix(id, "-07") }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortSuite := suite
			shortSuite.EvaluationCases = nil
			for _, c := range suite.EvaluationCases {
				if tc.keep(c.Unit, c.ID) {
					shortSuite.EvaluationCases = append(shortSuite.EvaluationCases, c)
				}
			}

			key, a, b, adj := packet.IdentityKey, sourceA, sourceB, sourceAdj
			key.Entries, a.Rows, b.Rows, adj.Rows = nil, nil, nil, nil
			for i, entry := range packet.IdentityKey.Entries {
				if !tc.keep(entry.Unit, entry.CaseID) {
					continue
				}
				ra, rb, resolution := sourceA.Rows[i], sourceB.Rows[i], sourceAdj.Rows[i]
				order := len(key.Entries) + 1
				entry.PresentationOrder, ra.PresentationOrder, rb.PresentationOrder, resolution.PresentationOrder = order, order, order, order
				rating := *ra.Rating
				rating.FactualAccuracy = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(2)}
				rating.ActionableGuidance = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(1 + order%2)}
				ra.Rating, rb.Rating = &rating, &rating
				resolution.RaterAInitial, resolution.RaterBInitial, resolution.Adjudicated = &rating, &rating, &rating
				resolution.Disagreement, resolution.ResolutionReason = false, ""
				key.Entries = append(key.Entries, entry)
				a.Rows, b.Rows, adj.Rows = append(a.Rows, ra), append(b.Rows, rb), append(adj.Rows, resolution)
			}

			t.Run("builder", func(t *testing.T) {
				artifacts, err := quality.BuildBlindArtifacts(shortSuite, facts)
				if tc.wantErr == "" {
					if err != nil || len(artifacts.IdentityKey.Entries) != 24 {
						t.Fatalf("minimum roster returned %d cases, err=%v", len(artifacts.IdentityKey.Entries), err)
					}
					return
				}
				if !errors.Is(err, quality.ErrValidation) || !strings.Contains(err.Error(), tc.wantErr) || len(artifacts.IdentityKey.Entries) != 0 {
					t.Fatalf("incomplete roster built %d cases, err=%v", len(artifacts.IdentityKey.Entries), err)
				}
			})
			t.Run("scorer", func(t *testing.T) {
				report, err := quality.Evaluate(key, a, b, adj, kind, opts)
				if tc.wantErr == "" {
					if err != nil || !report.MeasuredCandidate || !report.Gate.Passed || report.CompletedCases != 24 {
						t.Fatalf("minimum roster completed %d cases, gate=%+v, measured=%v, err=%v", report.CompletedCases, report.Gate, report.MeasuredCandidate, err)
					}
					for _, unit := range report.ByUnit {
						if unit.Status != quality.StatusInconclusive {
							t.Errorf("six-case stratum %s has status %s", unit.Key, unit.Status)
						}
					}
					return
				}
				if !errors.Is(err, quality.ErrValidation) || !strings.Contains(err.Error(), tc.wantErr) || report.MeasuredCandidate {
					t.Fatalf("incomplete roster completed %d cases, gate=%+v, measured=%v, err=%v", report.CompletedCases, report.Gate, report.MeasuredCandidate, err)
				}
			})
		})
	}
}

func TestPacketBuilderRejectsRepeatedCases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*quality.BenchmarkSuite)
	}{
		{"two records repeated", func(s *quality.BenchmarkSuite) {
			pair := slices.Clone(s.EvaluationCases[:2])
			for i := range s.EvaluationCases {
				s.EvaluationCases[i] = pair[i%2]
			}
		}},
		{"repeated case id", func(s *quality.BenchmarkSuite) { s.EvaluationCases[5].ID = s.EvaluationCases[4].ID }},
		{"empty case id", func(s *quality.BenchmarkSuite) { s.EvaluationCases[5].ID = "" }},
		{"calibration id reused", func(s *quality.BenchmarkSuite) { s.CalibrationCases[0].ID = s.EvaluationCases[0].ID }},
		{"repeated calibration id", func(s *quality.BenchmarkSuite) { s.CalibrationCases[1].ID = s.CalibrationCases[0].ID }},
		{"renamed copy", func(s *quality.BenchmarkSuite) {
			copied := s.EvaluationCases[0]
			copied.ID = "RENAMED-COPY"
			s.EvaluationCases[1] = copied
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suite, facts := loadCandidateSuite(t)
			if _, err := quality.BuildBlindArtifacts(suite, facts); err != nil {
				t.Fatalf("control roster: %v", err)
			}
			tc.change(&suite)
			artifacts, err := quality.BuildBlindArtifacts(suite, facts)
			if !errors.Is(err, quality.ErrValidation) || len(artifacts.RaterABlank.Rows) != 0 || len(artifacts.IdentityKey.Entries) != 0 {
				t.Fatalf("repeated roster built %d rows, err=%v", len(artifacts.RaterABlank.Rows), err)
			}
		})
	}
}

func TestEvaluateRejectsRepeatedKeyCases(t *testing.T) {
	kind := quality.DatasetKindMeasuredCandidate
	opts := quality.ScoreOptions{BootstrapRuns: 100}
	for _, tc := range []struct {
		name   string
		change func(*quality.IdentityKey, *quality.RatingSheet, *quality.RatingSheet, *quality.AdjudicationSheet)
	}{
		{"repeated case id", func(k *quality.IdentityKey, _, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
			k.Entries[1].CaseID = k.Entries[0].CaseID
		}},
		{"empty case id", func(k *quality.IdentityKey, _, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
			k.Entries[1].CaseID = ""
		}},
		{"repeated material", func(k *quality.IdentityKey, a, b *quality.RatingSheet, adj *quality.AdjudicationSheet) {
			for _, sheet := range []*quality.RatingSheet{a, b} {
				row, first := &sheet.Rows[1], sheet.Rows[0]
				row.PromptContext, row.VARKProfile, row.FailedTopics, row.MasteredTopics = first.PromptContext, first.VARKProfile, first.FailedTopics, first.MasteredTopics
				row.Items, row.ResourceEvidence, row.ReferenceLocators, row.Rating = first.Items, first.ResourceEvidence, first.ReferenceLocators, first.Rating
			}
			k.Entries[1].MaterialSHA256, k.Entries[1].VARKProfile = k.Entries[0].MaterialSHA256, k.Entries[0].VARKProfile
			adj.Rows[1].RaterAInitial, adj.Rows[1].RaterBInitial, adj.Rows[1].Adjudicated = adj.Rows[0].RaterAInitial, adj.Rows[0].RaterBInitial, adj.Rows[0].Adjudicated
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packet, a, b, adj := eligibleCandidate(t)
			if _, err := quality.Evaluate(packet.IdentityKey, a, b, adj, kind, opts); err != nil {
				t.Fatalf("control key: %v", err)
			}
			key := packet.IdentityKey
			key.Entries = slices.Clone(key.Entries)
			tc.change(&key, &a, &b, &adj)
			if rep, err := quality.Evaluate(key, a, b, adj, kind, opts); !errors.Is(err, quality.ErrValidation) {
				t.Fatalf("repeated key case returned gate=%+v measured=%v err=%v", rep.Gate, rep.MeasuredCandidate, err)
			}
		})
	}
}

func TestPacketBuilderRefusesSelfIdentifyingMaterial(t *testing.T) {
	const marker = "CourseMarkerX"
	withEvidence := func(s *quality.BenchmarkSuite) *quality.EvaluationCase {
		for i := range s.EvaluationCases {
			if len(s.EvaluationCases[i].ResourceEvidence) > 0 {
				return &s.EvaluationCases[i]
			}
		}
		t.Fatal("fixture has no resource evidence")
		return nil
	}
	for _, tc := range []struct {
		name   string
		change func(*quality.BenchmarkSuite) string
	}{
		{"provider in explanation", func(s *quality.BenchmarkSuite) string {
			c := &s.EvaluationCases[3]
			c.Origin.Provider = marker
			c.CandidateResponse.Items[0].Explanation = "I am " + marker + ". " + c.CandidateResponse.Items[0].Explanation
			return c.ID
		}},
		{"provider with different case", func(s *quality.BenchmarkSuite) string {
			c := &s.EvaluationCases[3]
			c.Origin.Provider = marker
			c.CandidateResponse.Items[0].Explanation = "i am " + strings.ToLower(marker) + "."
			return c.ID
		}},
		{"model in prompt", func(s *quality.BenchmarkSuite) string {
			c := &s.EvaluationCases[4]
			c.Origin.Model = "marker-model-9"
			c.PromptContext += " Answer as marker-model-9."
			return c.ID
		}},
		{"variant in resource evidence", func(s *quality.BenchmarkSuite) string {
			c := withEvidence(s)
			c.Origin.Variant = "marker-variant-9"
			c.ResourceEvidence = slices.Clone(c.ResourceEvidence)
			c.ResourceEvidence[0].Excerpt += " marker-variant-9"
			return c.ID
		}},
		{"another case identifier", func(s *quality.BenchmarkSuite) string {
			c := &s.EvaluationCases[6]
			c.CandidateResponse.Items[0].Explanation += " See " + s.EvaluationCases[7].ID + "."
			return c.ID
		}},
		{"calibration example", func(s *quality.BenchmarkSuite) string {
			s.EvaluationCases[3].Origin.Provider = marker
			c := &s.CalibrationCases[2]
			c.CandidateResponse.Items[0].Explanation += " " + marker + " wrote this."
			return c.ID
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suite, facts := loadCandidateSuite(t)
			suite.EvaluationCases[3].Origin.Provider = "clean-control-provider"
			if _, err := quality.BuildBlindArtifacts(suite, facts); err != nil {
				t.Fatalf("clean control: %v", err)
			}
			record := tc.change(&suite)
			artifacts, err := quality.BuildBlindArtifacts(suite, facts)
			if !errors.Is(err, quality.ErrValidation) || !strings.Contains(err.Error(), record) {
				t.Fatalf("self-identifying record %s returned %v", record, err)
			}
			if len(artifacts.RaterABlank.Rows)+len(artifacts.RaterBBlank.Rows)+len(artifacts.Calibration.Examples)+len(artifacts.IdentityKey.Entries) != 0 {
				t.Fatal("builder returned distributable material for a self-identifying record")
			}
		})
	}
}

func TestPacketBuilderRejectsEscapedProviderName(t *testing.T) {
	for _, provider := range []string{"Research & Learning", `Research "Learning"`} {
		t.Run(provider, func(t *testing.T) {
			suite, facts := loadCandidateSuite(t)
			c := &suite.EvaluationCases[0]
			c.Origin.Provider = provider
			if _, err := quality.BuildBlindArtifacts(suite, facts); err != nil {
				t.Fatalf("clean packet with private provider %q: %v", provider, err)
			}
			c.CandidateResponse.Items[0].Explanation += " Prepared by " + provider + "."
			artifacts, err := quality.BuildBlindArtifacts(suite, facts)
			if !errors.Is(err, quality.ErrValidation) || !strings.Contains(err.Error(), c.ID) ||
				len(artifacts.RaterABlank.Rows) != 0 || len(artifacts.RaterBBlank.Rows) != 0 {
				t.Fatalf("builder returned rating sheets for escaped provider %q, err=%v", provider, err)
			}
		})
	}
}

func TestPacketBuilderRejectsSeededScenarioLabel(t *testing.T) {
	cat, facts, suite, seed, example := loadAllFixtures(t)
	if _, err := quality.BuildBlindArtifacts(suite, facts); err != nil {
		t.Fatalf("clean packet with seeded scenarios: %v", err)
	}
	for i := range suite.EvaluationCases {
		c := &suite.EvaluationCases[i]
		if c.ID != "EVAL-U1-05" {
			continue
		}
		if !c.Origin.SeededCriticalError || len(c.ScenarioTags) == 0 {
			t.Fatal("fixture must be a seeded critical scenario with a private label")
		}
		label := c.ScenarioTags[0]
		c.CandidateResponse.Items[0].Explanation += " Scenario: " + label + "."
		if err := quality.ValidateSuite(suite, cat, facts, seed, example); err != nil {
			t.Fatalf("scenario leak must pass suite validation: %v", err)
		}
		artifacts, err := quality.BuildBlindArtifacts(suite, facts)
		if !errors.Is(err, quality.ErrValidation) || !strings.Contains(err.Error(), c.ID) ||
			!strings.Contains(err.Error(), label) || len(artifacts.RaterABlank.Rows) != 0 || len(artifacts.RaterBBlank.Rows) != 0 {
			t.Fatalf("builder returned rating sheets for private scenario label %q, err=%v", label, err)
		}
		return
	}
	t.Fatal("fixture has no EVAL-U1-05 scenario")
}

func TestDraftCannotBypassAdjudicationProtocol(t *testing.T) {
	_, key, _, _, _, syn := loadScoringFixtures(t)
	for _, tc := range []struct {
		name   string
		change func(*quality.RatingSheet, *quality.AdjudicationSheet)
	}{
		{"unlocked", func(a *quality.RatingSheet, _ *quality.AdjudicationSheet) { a.Locked = false }},
		{"unqualified", func(a *quality.RatingSheet, _ *quality.AdjudicationSheet) { a.QualificationAttest = false }},
		{"uncalibrated", func(a *quality.RatingSheet, _ *quality.AdjudicationSheet) { a.CalibrationCompleted = false }},
		{"unnamed adjudicator", func(_ *quality.RatingSheet, adj *quality.AdjudicationSheet) { adj.AdjudicatorName = "" }},
		{"missing original", func(a *quality.RatingSheet, _ *quality.AdjudicationSheet) { a.Rows[0].Rating = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, adj := cloneSubmission(syn.PassingArithmeticSet)
			tc.change(&a, &adj)
			if _, err := quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{AllowIncomplete: true}); !errors.Is(err, quality.ErrValidation) {
				t.Fatalf("invalid adjudication returned %v", err)
			}
		})
	}
}

func TestExplicitCriticalJudgmentInJSON(t *testing.T) {
	_, key, _, _, _, syn := loadScoringFixtures(t)
	for _, field := range []string{"critical_error", "critical_error_type"} {
		for _, raw := range []json.RawMessage{nil, json.RawMessage("null")} {
			t.Run(field+"/"+string(raw), func(t *testing.T) {
				a, b, adj := cloneSubmission(syn.PassingArithmeticSet)
				ratings := []*quality.DimensionRatings{a.Rows[0].Rating, b.Rows[0].Rating, adj.Rows[0].RaterAInitial, adj.Rows[0].RaterBInitial, adj.Rows[0].Adjudicated}
				for _, rating := range ratings {
					*rating = decodeWithCriticalField(t, *rating, field, raw)
				}
				if _, err := quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{}); !errors.Is(err, quality.ErrValidation) {
					t.Fatalf("missing or null critical decision returned %v", err)
				}
				a, b, adj = cloneSubmission(syn.PassingArithmeticSet)
				*adj.Rows[0].Adjudicated = decodeWithCriticalField(t, *adj.Rows[0].Adjudicated, field, raw)
				if _, err := quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{AllowIncomplete: true}); !errors.Is(err, quality.ErrValidation) {
					t.Fatalf("missing or null critical decision on final judgment returned %v", err)
				}
			})
		}
	}
}

func decodeWithCriticalField(t *testing.T, rating quality.DimensionRatings, field string, value json.RawMessage) quality.DimensionRatings {
	t.Helper()
	data, err := json.Marshal(rating)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if value == nil {
		delete(fields, field)
	} else {
		fields[field] = value
	}
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	result, err := quality.DecodeStrictJSON[quality.DimensionRatings](data)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestLockedRatingPreservation(t *testing.T) {
	_, key, _, _, _, syn := loadScoringFixtures(t)
	for _, tc := range []struct {
		name   string
		change func(*quality.RatingSheet, *quality.AdjudicationSheet)
	}{
		{"explanation", func(b *quality.RatingSheet, _ *quality.AdjudicationSheet) {
			b.Rows[0].Items[0].Explanation += " Changed."
		}},
		{"prompt", func(b *quality.RatingSheet, _ *quality.AdjudicationSheet) {
			b.Rows[0].PromptContext += " Changed."
		}},
		{"preference", func(b *quality.RatingSheet, _ *quality.AdjudicationSheet) {
			b.Rows[0].VARKProfile += "_changed"
		}},
		{"request context", func(b *quality.RatingSheet, _ *quality.AdjudicationSheet) {
			b.Rows[0].FailedTopics = append(slices.Clone(b.Rows[0].FailedTopics), "Changed topic")
		}},
		{"reference evidence", func(b *quality.RatingSheet, _ *quality.AdjudicationSheet) {
			b.Rows[0].ReferenceLocators = nil
		}},
		{"initial notes", func(_ *quality.RatingSheet, adj *quality.AdjudicationSheet) {
			adj.Rows[0].RaterAInitial.Notes += " Changed."
		}},
		{"initial abstention reason", func(_ *quality.RatingSheet, adj *quality.AdjudicationSheet) {
			for _, row := range adj.Rows {
				if row.RaterAInitial.FactualAccuracy.Status == quality.ScoreStatusAbstain {
					row.RaterAInitial.FactualAccuracy.Reason += " Changed."
					return
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, adj := cloneSubmission(syn.PassingArithmeticSet)
			b.Rows[0].Items = append(b.Rows[0].Items[:0:0], b.Rows[0].Items...)
			tc.change(&b, &adj)
			if _, err := quality.Evaluate(key, a, b, adj, quality.DatasetKindSyntheticRegression, quality.ScoreOptions{}); !errors.Is(err, quality.ErrValidation) {
				t.Fatalf("changed submission returned %v", err)
			}
		})
	}
}

func TestAgreementOverrideRequiresResolutionReason(t *testing.T) {
	_, key, _, _, _, syn := loadScoringFixtures(t)
	a, b, adj := cloneSubmission(syn.PassingArithmeticSet)
	for i := range adj.Rows {
		row := &adj.Rows[i]
		if row.Disagreement {
			continue
		}
		for _, rating := range []*quality.DimensionRatings{a.Rows[i].Rating, b.Rows[i].Rating, row.RaterAInitial, row.RaterBInitial} {
			rating.CriticalError = boolPtr(true)
			rating.CriticalErrorType = quality.CriticalTypeFactual
		}
		if _, err := quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{}); !errors.Is(err, quality.ErrValidation) {
			t.Fatalf("undocumented override of agreed critical decision returned %v", err)
		}
		row.ResolutionReason = "For this synthetic exercise, the reference resolves the raters' common error."
		rep, err := quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{BootstrapRuns: 100})
		if err != nil || rep.Gate.Status != quality.StatusPass {
			t.Fatalf("documented override returned gate=%+v err=%v", rep.Gate, err)
		}
		return
	}
	t.Fatal("fixture contained no agreed decisions")
}
