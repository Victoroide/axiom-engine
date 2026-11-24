package quality_test

import (
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/Victoroide/axiom-engine/evaluation/quality"
)

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func loadScoringFixtures(t *testing.T) (
	quality.BenchmarkSuite,
	quality.IdentityKey,
	quality.RatingSheet,
	quality.RatingSheet,
	quality.AdjudicationSheet,
	quality.SyntheticFixtureSet,
) {
	t.Helper()
	_, _, suite, _, _ := loadAllFixtures(t)
	key, err := quality.ReadStrictJSONFile[quality.IdentityKey]("testdata", "identity_key.json")
	if err != nil {
		t.Fatalf("read identity_key.json: %v", err)
	}
	raterA, err := quality.ReadStrictJSONFile[quality.RatingSheet]("packets", "rater_a_blank.json")
	if err != nil {
		t.Fatalf("read rater_a_blank.json: %v", err)
	}
	raterB, err := quality.ReadStrictJSONFile[quality.RatingSheet]("packets", "rater_b_blank.json")
	if err != nil {
		t.Fatalf("read rater_b_blank.json: %v", err)
	}
	adj, err := quality.ReadStrictJSONFile[quality.AdjudicationSheet]("packets", "adjudication_blank.json")
	if err != nil {
		t.Fatalf("read adjudication_blank.json: %v", err)
	}
	syn, err := quality.ReadStrictJSONFile[quality.SyntheticFixtureSet]("testdata", "synthetic_completed_ratings.json")
	if err != nil {
		t.Fatalf("read synthetic_completed_ratings.json: %v", err)
	}
	return suite, key, raterA, raterB, adj, syn
}

func cloneSubmission(sub quality.SyntheticCompletedSubmission) (quality.RatingSheet, quality.RatingSheet, quality.AdjudicationSheet) {
	ra := sub.RaterA
	ra.Rows = make([]quality.RatingRow, len(sub.RaterA.Rows))
	for i, r := range sub.RaterA.Rows {
		ra.Rows[i] = r
		if r.Rating != nil {
			c := *r.Rating
			c.ResourceRatings = slices.Clone(r.Rating.ResourceRatings)
			ra.Rows[i].Rating = &c
		}
	}
	rb := sub.RaterB
	rb.Rows = make([]quality.RatingRow, len(sub.RaterB.Rows))
	for i, r := range sub.RaterB.Rows {
		rb.Rows[i] = r
		if r.Rating != nil {
			c := *r.Rating
			c.ResourceRatings = slices.Clone(r.Rating.ResourceRatings)
			rb.Rows[i].Rating = &c
		}
	}
	adj := sub.Adjudication
	adj.Rows = make([]quality.AdjudicationRow, len(sub.Adjudication.Rows))
	for i, r := range sub.Adjudication.Rows {
		adj.Rows[i] = r
		if r.RaterAInitial != nil {
			c := *r.RaterAInitial
			c.ResourceRatings = slices.Clone(r.RaterAInitial.ResourceRatings)
			adj.Rows[i].RaterAInitial = &c
		}
		if r.RaterBInitial != nil {
			c := *r.RaterBInitial
			c.ResourceRatings = slices.Clone(r.RaterBInitial.ResourceRatings)
			adj.Rows[i].RaterBInitial = &c
		}
		if r.Adjudicated != nil {
			c := *r.Adjudicated
			c.ResourceRatings = slices.Clone(r.Adjudicated.ResourceRatings)
			adj.Rows[i].Adjudicated = &c
		}
	}
	return ra, rb, adj
}

func TestBlankCandidatePacketIsInconclusiveAndNeverPasses(t *testing.T) {
	_, key, ra, rb, adj, _ := loadScoringFixtures(t)
	if _, err := quality.Evaluate(key, ra, rb, adj, quality.DatasetKindMeasuredCandidate, quality.ScoreOptions{}); !errors.Is(err, quality.ErrValidation) {
		t.Fatalf("strict evaluation of blank packet should fail validation, got: %v", err)
	}
	report, err := quality.Evaluate(key, ra, rb, adj, quality.DatasetKindUnmeasuredBlank, quality.ScoreOptions{AllowIncomplete: true})
	if err != nil {
		t.Fatalf("incomplete blank evaluation failed: %v", err)
	}
	if report.Gate.Passed || report.Gate.Status != quality.StatusInconclusive || report.MeasuredCandidate {
		t.Fatalf("unexpected blank report gate: %+v measured=%v", report.Gate, report.MeasuredCandidate)
	}
	if report.CompletedCases != 0 || report.MissingRatings != 28 {
		t.Fatalf("completed=%d missing=%d, want 0 and 28", report.CompletedCases, report.MissingRatings)
	}
	if report.Dimensions["factual_accuracy"].Score2 != 0 || report.Dimensions["factual_accuracy"].Missing != 28 {
		t.Fatalf("blank sheet must never count missing ratings as pass: %+v", report.Dimensions["factual_accuracy"])
	}
	if got := report.Dimensions["resource_relevance"]; got.TotalItems != 29 || got.Missing != 29 || got.NAItems != 6 {
		t.Fatalf("blank resource denominators: %+v", got)
	}
	for _, strata := range [][]quality.StratumSummary{report.ByUnit, report.ByTopic, report.ByPreference, report.ByProvider} {
		assertUnratedResourceStrata(t, strata)
	}
}

func assertUnratedResourceStrata(t *testing.T, strata []quality.StratumSummary) {
	t.Helper()
	var supplied, missing, absent int
	for _, stratum := range strata {
		supplied += stratum.SuppliedResources
		missing += stratum.MissingResources
		absent += stratum.NAItems
	}
	if supplied != 29 || missing != 29 || absent != 6 {
		t.Fatalf("stratum resource totals = %d supplied, %d missing, %d absent", supplied, missing, absent)
	}
}

func TestLockedAgreementBeforeAdjudication(t *testing.T) {
	_, key, _, _, blankAdj, syn := loadScoringFixtures(t)
	a, b, adj := cloneSubmission(syn.PassingArithmeticSet)
	adj.Rows = blankAdj.Rows
	adj.AdjudicatorName = ""
	rep, err := quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{AllowIncomplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.CompletedCases != 0 || rep.MissingRatings != 28 || rep.RaterAInitialRatings != 28 || rep.RaterBInitialRatings != 28 ||
		rep.Gate.Status != quality.StatusInconclusive {
		t.Fatalf("unexpected adjudication progress: %+v", rep.Gate)
	}
	if got := rep.GuidanceAgreement; got.PairedAssessable != 28 || got.Missing != 0 || !got.Defined {
		t.Fatalf("locked guidance pair counts before adjudication: %+v", got)
	}
	assertUnratedResourceStrata(t, rep.ByUnit)

	a.Locked = false
	rep, err = quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{AllowIncomplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.GuidanceAgreement; rep.Gate.Status != quality.StatusInconclusive || got.PairedAssessable != 0 || got.Missing != 28 || got.Defined {
		t.Fatalf("unlocked draft must have no paired agreement: %+v", got)
	}
}

func TestPartialMissingRatingNeverTreatsMissingAsPass(t *testing.T) {
	_, key, _, _, _, syn := loadScoringFixtures(t)
	ra, rb, adj := cloneSubmission(syn.PassingArithmeticSet)
	// Remove a single rating from Rater B and adjudication.
	rb.Rows[0].Rating = nil
	adj.Rows[0].RaterBInitial = nil
	adj.Rows[0].Adjudicated = nil

	if _, err := quality.Evaluate(key, ra, rb, adj, quality.DatasetKindSyntheticRegression, quality.ScoreOptions{}); !errors.Is(err, quality.ErrValidation) {
		t.Fatalf("strict evaluation with missing rating must fail, got: %v", err)
	}
	report, err := quality.Evaluate(key, ra, rb, adj, quality.DatasetKindSyntheticRegression, quality.ScoreOptions{AllowIncomplete: true})
	if err != nil {
		t.Fatalf("AllowIncomplete evaluation failed: %v", err)
	}
	if report.Gate.Passed || report.Gate.Status != quality.StatusInconclusive || report.Gate.AllRatingsComplete {
		t.Fatalf("incomplete submission must be inconclusive and never pass: %+v", report.Gate)
	}
	if report.MissingRatings != 1 || report.CompletedCases != 27 {
		t.Fatalf("missing=%d completed=%d, want 1 and 27", report.MissingRatings, report.CompletedCases)
	}
}

func TestMissingDuplicateUnknownAndIllegalSheetValues(t *testing.T) {
	_, key, _, _, _, syn := loadScoringFixtures(t)

	cases := []struct {
		name   string
		mutate func(ra, rb *quality.RatingSheet, adj *quality.AdjudicationSheet)
	}{
		{
			name: "missing row",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				ra.Rows = ra.Rows[:len(ra.Rows)-1]
			},
		},
		{
			name: "duplicate row",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				ra.Rows[len(ra.Rows)-1] = ra.Rows[0]
			},
		},
		{
			name: "unknown response id",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				ra.Rows[0].ResponseID = "resp-unknown"
			},
		},
		{
			name: "order mismatch",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				ra.Rows[0].PresentationOrder = 99
			},
		},
		{
			name: "illegal score 3",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				ra.Rows[0].Rating.FactualAccuracy.Score = intPtr(3)
			},
		},
		{
			name: "negative score -1",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				ra.Rows[0].Rating.ActionableGuidance.Score = intPtr(-1)
			},
		},
		{
			name: "na on factual dimension",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				ra.Rows[0].Rating.FactualAccuracy = quality.RubricScore{Status: quality.ScoreStatusNA}
			},
		},
		{
			name: "abstention missing reason",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				ra.Rows[0].Rating.FactualAccuracy = quality.RubricScore{Status: quality.ScoreStatusAbstain, Reason: "   "}
			},
		},
		{
			name: "critical true with type none",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				ra.Rows[0].Rating.CriticalError = boolPtr(true)
				ra.Rows[0].Rating.CriticalErrorType = quality.CriticalTypeNone
			},
		},
		{
			name: "resource count mismatch",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				for i := range ra.Rows {
					if len(ra.Rows[i].Items[0].Resources) > 0 {
						ra.Rows[i].Rating.ResourceRatings = nil
						break
					}
				}
			},
		},
		{
			name: "empty resources marked applicable instead of na",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				for i := range ra.Rows {
					if len(ra.Rows[i].Items[0].Resources) == 0 {
						ra.Rows[i].Rating.ResourceSummaryStatus = quality.ResourceSummaryApplicable
						break
					}
				}
			},
		},
		{
			name: "tampered initial rating in adjudication",
			mutate: func(_, _ *quality.RatingSheet, adj *quality.AdjudicationSheet) {
				adj.Rows[0].RaterAInitial.ActionableGuidance = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(0)}
			},
		},
		{
			name: "unlocked initial sheet",
			mutate: func(ra, _ *quality.RatingSheet, _ *quality.AdjudicationSheet) {
				ra.Locked = false
			},
		},
		{
			name: "wrong adjudicator role",
			mutate: func(_, _ *quality.RatingSheet, adj *quality.AdjudicationSheet) {
				adj.AdjudicatorRole = "Unverified Peer"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ra, rb, adj := cloneSubmission(syn.PassingArithmeticSet)
			tc.mutate(&ra, &rb, &adj)
			if _, err := quality.Evaluate(key, ra, rb, adj, quality.DatasetKindSyntheticRegression, quality.ScoreOptions{}); !errors.Is(err, quality.ErrValidation) {
				t.Fatalf("case %s expected ErrValidation, got %v", tc.name, err)
			}
		})
	}

	// Verify strict JSON decoding rejects unknown fields and trailing tokens.
	if _, err := quality.DecodeStrictJSON[quality.RatingSheet]([]byte(`{"unknown_field": true}`)); !errors.Is(err, quality.ErrInvalidSchema) {
		t.Fatalf("expected ErrInvalidSchema for unknown field, got %v", err)
	}
	if _, err := quality.DecodeStrictJSON[quality.RubricScore]([]byte(`{"status":"scored","score":2}{"extra":1}`)); !errors.Is(err, quality.ErrInvalidSchema) {
		t.Fatalf("expected ErrInvalidSchema for trailing JSON, got %v", err)
	}
}

func TestSyntheticCompletedSheetsAndEverySeededCriticalLabelFails(t *testing.T) {
	suite, key, _, _, _, syn := loadScoringFixtures(t)

	// 1. Failing seeded synthetic sheet.
	raFail, rbFail, adjFail := cloneSubmission(syn.FailingSeededSheet)
	failReport, err := quality.Evaluate(key, raFail, rbFail, adjFail, quality.DatasetKindSyntheticRegression, quality.ScoreOptions{})
	if err != nil {
		t.Fatalf("evaluate failing synthetic sheet: %v", err)
	}
	if failReport.Gate.Passed || failReport.Gate.Status != quality.StatusFail {
		t.Fatalf("expected failing synthetic sheet gate fail, got %+v", failReport.Gate)
	}
	if failReport.TotalCases != 28 || failReport.CompletedCases != 28 || failReport.MissingRatings != 0 ||
		failReport.AdjudicatedCriticalErrors != 9 || failReport.Disagreements != 9 || failReport.MeasuredCandidate {
		t.Fatalf("wrong complete failing-sheet counts: %d total, %d completed, %d missing, %d critical, %d disagreements, measured=%v",
			failReport.TotalCases, failReport.CompletedCases, failReport.MissingRatings, failReport.AdjudicatedCriticalErrors, failReport.Disagreements, failReport.MeasuredCandidate)
	}
	expectedTypes := map[string]string{
		"EVAL-U1-02": quality.CriticalTypeFactual,
		"EVAL-U1-05": quality.CriticalTypePrerequisite,
		"EVAL-U2-01": quality.CriticalTypeFactual,
		"EVAL-U2-04": quality.CriticalTypePrerequisite,
		"EVAL-U3-02": quality.CriticalTypeFactual,
		"EVAL-U3-04": quality.CriticalTypeFactual,
		"EVAL-U3-05": quality.CriticalTypeBoth,
		"EVAL-U4-02": quality.CriticalTypeFactual,
		"EVAL-U4-04": quality.CriticalTypeBoth,
	}
	expectedIDs := make([]string, 0, len(expectedTypes))
	for id := range expectedTypes {
		expectedIDs = append(expectedIDs, id)
	}
	sort.Strings(expectedIDs)
	if !slices.Equal(failReport.CriticalErrorCaseIDs, expectedIDs) {
		t.Fatalf("failing sheet critical IDs = %v, want %v", failReport.CriticalErrorCaseIDs, expectedIDs)
	}
	for i, entry := range key.Entries {
		wantType, critical := expectedTypes[entry.CaseID]
		if !critical {
			wantType = quality.CriticalTypeNone
		}
		final := adjFail.Rows[i].Adjudicated
		if *final.CriticalError != critical || final.CriticalErrorType != wantType ||
			entry.SeededCriticalError != critical || entry.SeededCriticalType != wantType {
			t.Fatalf("%s seeded and adjudicated types = %q / %q, want %q", entry.CaseID, entry.SeededCriticalType, final.CriticalErrorType, wantType)
		}
	}
	for _, s := range failReport.ByUnit {
		if s.Status != quality.StatusInconclusive || s.AdjudicatedCriticalErrors == 0 {
			t.Fatalf("small unit stratum must be inconclusive and retain its critical errors: %+v", s)
		}
	}
	if failReport.Dimensions["factual_accuracy"].Abstentions != 2 || failReport.Dimensions["resource_relevance"].NAItems != 6 {
		t.Fatalf("unexpected abstentions/NA: %+v / %+v", failReport.Dimensions["factual_accuracy"], failReport.Dimensions["resource_relevance"])
	}

	// 2. Passing synthetic arithmetic sheet (distinguished from measured results).
	raPass, rbPass, adjPass := cloneSubmission(syn.PassingArithmeticSet)
	passReport, err := quality.Evaluate(key, raPass, rbPass, adjPass, quality.DatasetKindSyntheticRegression, quality.ScoreOptions{})
	if err != nil {
		t.Fatalf("evaluate passing synthetic sheet: %v", err)
	}
	if !passReport.Gate.Passed || passReport.Gate.Status != quality.StatusPass || passReport.MeasuredCandidate {
		t.Fatalf("unexpected passing synthetic sheet gate: %+v measured=%v", passReport.Gate, passReport.MeasuredCandidate)
	}
	assertSyntheticArithmetic(t, passReport)
	// Every unit (<10) and preference (<10) stratum must be inconclusive, while provider-a (12 >= 10) passes.
	for _, s := range passReport.ByUnit {
		if s.Status != quality.StatusInconclusive {
			t.Fatalf("unit stratum %s (%d cases < 10) must be inconclusive, got %s", s.Key, s.TotalCases, s.Status)
		}
	}
	for _, s := range passReport.ByPreference {
		if s.Status != quality.StatusInconclusive {
			t.Fatalf("preference stratum %s (%d cases < 10) must be inconclusive, got %s", s.Key, s.TotalCases, s.Status)
		}
	}
	providerStatus := make(map[string]string, len(passReport.ByProvider))
	for _, s := range passReport.ByProvider {
		providerStatus[s.Key] = s.Status
	}
	if providerStatus["synthetic-provider-a"] != quality.StatusPass ||
		providerStatus["synthetic-provider-b"] != quality.StatusInconclusive ||
		providerStatus["synthetic-provider-c"] != quality.StatusInconclusive {
		t.Fatalf("unexpected provider stratum statuses: %+v", providerStatus)
	}

	// 3. Prove EVERY seeded critical label individually causes gate failure even on an otherwise passing sheet.
	entryByCase := make(map[string]quality.IdentityEntry, len(key.Entries))
	for _, e := range key.Entries {
		entryByCase[e.CaseID] = e
	}
	var seededCriticalCount int
	for _, c := range suite.EvaluationCases {
		if !c.Origin.SeededCriticalError {
			continue
		}
		seededCriticalCount++
		t.Run("isolated_critical_"+c.ID, func(t *testing.T) {
			ra, rb, adj := cloneSubmission(syn.PassingArithmeticSet)
			respID := entryByCase[c.ID].ResponseID
			for i := range ra.Rows {
				if ra.Rows[i].ResponseID != respID {
					continue
				}
				// Hold every ordinal score constant to isolate the critical decision.
				for _, ptr := range []*quality.DimensionRatings{ra.Rows[i].Rating, rb.Rows[i].Rating, adj.Rows[i].RaterAInitial, adj.Rows[i].RaterBInitial, adj.Rows[i].Adjudicated} {
					ptr.CriticalError = boolPtr(true)
					ptr.CriticalErrorType = c.Origin.SeededCriticalType
				}
				adj.Rows[i].Disagreement = quality.RatingsDisagree(*adj.Rows[i].RaterAInitial, *adj.Rows[i].RaterBInitial)
			}
			rep, err := quality.Evaluate(key, ra, rb, adj, quality.DatasetKindSyntheticRegression, quality.ScoreOptions{})
			if err != nil {
				t.Fatalf("evaluate isolated critical %s: %v", c.ID, err)
			}
			if rep.Gate.Passed || rep.Gate.Status != quality.StatusFail || rep.AdjudicatedCriticalErrors != 1 {
				t.Fatalf("case %s critical label failed to fail gate: passed=%v status=%s critical=%d",
					c.ID, rep.Gate.Passed, rep.Gate.Status, rep.AdjudicatedCriticalErrors)
			}
			if !slices.Equal(rep.CriticalErrorCaseIDs, []string{c.ID}) {
				t.Fatalf("critical_error_case_ids=%v, want [%s]", rep.CriticalErrorCaseIDs, c.ID)
			}
			if !rep.Gate.FactualPassAtLeast90 || !rep.Gate.GuidanceKappaAtLeast070 || len(rep.Gate.FailureReasons) != 1 {
				t.Fatalf("critical error must be the sole failure: %+v", rep.Gate)
			}
		})
	}
	if seededCriticalCount != 9 {
		t.Fatalf("expected 9 seeded critical cases, checked %d", seededCriticalCount)
	}
}

func assertSyntheticArithmetic(t *testing.T, report quality.ScoreReport) {
	t.Helper()
	if report.TotalCases != 28 || report.CompletedCases != 28 || report.MissingRatings != 0 || report.Disagreements != 3 {
		t.Fatalf("passing arithmetic case counts = %d total, %d completed, %d missing, %d disagreements",
			report.TotalCases, report.CompletedCases, report.MissingRatings, report.Disagreements)
	}
	checkCounts := func(dimension string, total, assessable, abstained, absent, score0, score1, score2 int) {
		t.Helper()
		got := report.Dimensions[dimension]
		if got.TotalItems != total || got.Assessable != assessable || got.Abstentions != abstained || got.NAItems != absent ||
			got.Missing != 0 || got.Score0 != score0 || got.Score1 != score1 || got.Score2 != score2 {
			t.Errorf("%s counts = %+v", dimension, got)
		}
	}
	checkCounts("factual_accuracy", 28, 26, 2, 0, 0, 2, 24)
	checkCounts("prerequisite_rationale", 28, 26, 2, 0, 0, 0, 26)
	checkCounts("actionable_guidance", 28, 28, 0, 0, 3, 10, 15)
	checkCounts("resource_relevance", 29, 29, 0, 6, 0, 4, 25)

	factual := report.Dimensions["factual_accuracy"]
	const tolerance = 0.0000001
	if factual.PassFraction != 24.0/26.0 || factual.PassCI95 == nil || !factual.PassCI95.Defined ||
		math.Abs(factual.PassCI95.Lower-0.7585845) > tolerance || math.Abs(factual.PassCI95.Upper-0.9786449) > tolerance {
		t.Errorf("factual fraction or interval: %+v / %+v", factual, factual.PassCI95)
	}
	kappa := report.GuidanceAgreement
	if kappa.PairedAssessable != 28 || kappa.Missing != 0 || kappa.Abstentions != 0 || !kappa.Defined ||
		kappa.BootstrapSamples != 2000 || kappa.UndefinedBootstrapReplicates != 0 || !kappa.BootstrapCI95.Defined ||
		math.Abs(kappa.Kappa-0.8415094) > tolerance || math.Abs(kappa.BootstrapCI95.Lower-0.6067139) > tolerance ||
		math.Abs(kappa.BootstrapCI95.Upper-1.0) > tolerance {
		t.Errorf("guidance agreement: %+v", kappa)
	}
}

func TestGatePerformanceThresholds(t *testing.T) {
	_, key, _, _, _, syn := loadScoringFixtures(t)
	for _, tc := range []struct {
		name       string
		factual    bool
		fail       bool
		wantMetric float64
	}{
		{"factual at threshold", true, false, 18.0 / 20.0},
		{"factual below threshold", true, true, 17.0 / 20.0},
		{"kappa above threshold", false, false, 1 - 2*4.0/28.0},
		{"kappa below threshold", false, true, 1 - 2*5.0/28.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, adj := cloneSubmission(syn.PassingArithmeticSet)
			if tc.factual {
				factualThresholdFixture(a, b, adj, tc.fail)
			} else {
				guidanceThresholdFixture(a, b, adj, tc.fail)
			}
			report, err := quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{BootstrapRuns: 200})
			if err != nil {
				t.Fatal(err)
			}
			gate := report.Gate
			metric := report.GuidanceAgreement.Kappa
			if tc.factual {
				metric = report.Dimensions["factual_accuracy"].PassFraction
			}
			if math.Abs(metric-tc.wantMetric) > 1e-12 || !gate.AllRatingsComplete || !gate.MinimumAssessableMet || !gate.KappaDefined || !gate.ZeroCriticalErrors {
				t.Fatalf("threshold fixture metric = %f, want %f, gate = %+v", metric, tc.wantMetric, gate)
			}
			if tc.fail {
				wantReason := "guidance linear-weighted kappa"
				if tc.factual {
					wantReason = "factual pass fraction"
				}
				if gate.Passed || gate.Status != quality.StatusFail || len(gate.FailureReasons) != 1 || !strings.Contains(gate.FailureReasons[0], wantReason) {
					t.Fatalf("expected only %q to fail: %+v", wantReason, gate)
				}
			} else if !gate.Passed || gate.Status != quality.StatusPass {
				t.Fatalf("threshold should pass: %+v", gate)
			}
		})
	}
}

// TestGuidanceKappaThresholdIsInclusive uses 28 guidance pairs over scores 0 and 1. For the
// exact case the observed disagreement sum is 3 and the cross-marginal sum is 280, so
// kappa = 1 - 28*3/280 = 7/10, which meets the inclusive 0.70 threshold.
func TestGuidanceKappaThresholdIsInclusive(t *testing.T) {
	_, key, _, _, _, syn := loadScoringFixtures(t)
	key.Entries = slices.Clone(key.Entries)
	for i := range key.Entries {
		// One provider stratum holds every pair so the stratum comparison is exercised too.
		key.Entries[i].Provider = "synthetic-provider-a"
	}
	for _, tc := range []struct {
		name      string
		counts    [2][2]int
		wantKappa float64
		pass      bool
	}{
		{"above", [2][2]int{{6, 1}, {2, 19}}, 1 - 28.0*3/308, true},
		{"exact", [2][2]int{{5, 1}, {2, 20}}, 0.7, true},
		{"below", [2][2]int{{4, 1}, {2, 21}}, 1 - 28.0*3/248, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, adj := cloneSubmission(syn.PassingArithmeticSet)
			guidanceCountsFixture(t, a, b, adj, tc.counts)
			report, err := quality.Evaluate(key, a, b, adj, "", quality.ScoreOptions{BootstrapRuns: 200})
			if err != nil {
				t.Fatal(err)
			}
			agreement := report.GuidanceAgreement
			if agreement.PairedAssessable != 28 || !agreement.Defined || math.Abs(agreement.Kappa-tc.wantKappa) > 1e-12 {
				t.Fatalf("guidance agreement = %+v, want kappa %.12f from 28 pairs", agreement, tc.wantKappa)
			}
			if len(report.ByProvider) != 1 || report.ByProvider[0].GuidanceAgreement.PairedAssessable != 28 {
				t.Fatalf("provider strata = %+v", report.ByProvider)
			}
			wantStatus := quality.StatusFail
			if tc.pass {
				wantStatus = quality.StatusPass
			}
			gate := report.Gate
			if gate.Passed != tc.pass || gate.Status != wantStatus || gate.GuidanceKappaAtLeast070 != tc.pass || !gate.ZeroCriticalErrors || !gate.FactualPassAtLeast90 {
				t.Fatalf("overall gate = %+v, want %s", gate, wantStatus)
			}
			if got := report.ByProvider[0].Status; got != wantStatus {
				t.Fatalf("provider stratum status = %s, want %s: %+v", got, wantStatus, report.ByProvider[0])
			}
			if tc.pass && len(gate.FailureReasons) != 0 {
				t.Fatalf("passing gate reported failures: %v", gate.FailureReasons)
			}
		})
	}
}

// guidanceCountsFixture gives every row the paired guidance scores in counts[rater A][rater B].
func guidanceCountsFixture(t *testing.T, a, b quality.RatingSheet, adj quality.AdjudicationSheet, counts [2][2]int) {
	t.Helper()
	var pairs [][2]int
	for left, row := range counts {
		for right, n := range row {
			for range n {
				pairs = append(pairs, [2]int{left, right})
			}
		}
	}
	if len(pairs) != len(adj.Rows) {
		t.Fatalf("fixture has %d pairs for %d rows", len(pairs), len(adj.Rows))
	}
	for i, row := range adj.Rows {
		left := quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(pairs[i][0])}
		right := quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(pairs[i][1])}
		for _, rating := range []*quality.DimensionRatings{a.Rows[i].Rating, row.RaterAInitial, row.Adjudicated} {
			rating.ActionableGuidance = left
		}
		for _, rating := range []*quality.DimensionRatings{b.Rows[i].Rating, row.RaterBInitial} {
			rating.ActionableGuidance = right
		}
		adj.Rows[i].Disagreement = quality.RatingsDisagree(*row.RaterAInitial, *row.RaterBInitial)
		adj.Rows[i].ResolutionReason = "Synthetic boundary fixture follows the initial left score."
	}
}

func factualThresholdFixture(a, b quality.RatingSheet, adj quality.AdjudicationSheet, below bool) {
	var changed int
	for i, row := range adj.Rows {
		s := row.Adjudicated.FactualAccuracy
		if s.Score == nil || *s.Score != 2 {
			continue
		}
		switch {
		case changed < 6:
			s = quality.RubricScore{Status: quality.ScoreStatusAbstain, Reason: "Synthetic threshold fixture abstention."}
		case changed == 6 && below:
			s.Score = intPtr(1)
		default:
			return
		}
		for _, rating := range []*quality.DimensionRatings{a.Rows[i].Rating, b.Rows[i].Rating, row.RaterAInitial, row.RaterBInitial, row.Adjudicated} {
			rating.FactualAccuracy = s
		}
		changed++
	}
}

func guidanceThresholdFixture(a, b quality.RatingSheet, adj quality.AdjudicationSheet, below bool) {
	disagreements := 4
	if below {
		disagreements++
	}
	for i, row := range adj.Rows {
		left, right := 0, 0
		if i >= len(adj.Rows)/2 {
			left, right = 2, 2
		}
		if i < disagreements {
			right = 2
		}
		for _, rating := range []*quality.DimensionRatings{a.Rows[i].Rating, row.RaterAInitial, row.Adjudicated} {
			rating.ActionableGuidance = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(left)}
		}
		for _, rating := range []*quality.DimensionRatings{b.Rows[i].Rating, row.RaterBInitial} {
			rating.ActionableGuidance = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(right)}
		}
		adj.Rows[i].Disagreement = quality.RatingsDisagree(*row.RaterAInitial, *row.RaterBInitial)
		adj.Rows[i].ResolutionReason = "Synthetic threshold fixture follows the initial left score."
	}
}

func TestStatisticalPropertiesAndInconclusiveEdgeCases(t *testing.T) {
	// Hand-checked Wilson 95% CI boundaries.
	ci := quality.WilsonCI95(10, 10)
	if !ci.Defined || ci.Upper != 1.0 || ci.Lower < 0.70 || ci.Lower > 0.75 {
		t.Fatalf("unexpected WilsonCI95(10, 10): %+v", ci)
	}
	if emptyCI := quality.WilsonCI95(0, 0); emptyCI.Defined {
		t.Fatalf("WilsonCI95(0,0) should be undefined: %+v", emptyCI)
	}

	// Undefined linear-weighted kappa when all ratings fall into one constant cell (Pe == 1).
	constantPairs := make([]quality.ScorePair, 12)
	for i := range constantPairs {
		constantPairs[i] = quality.ScorePair{RaterA: 2, RaterB: 2}
	}
	if _, ok := quality.LinearWeightedKappa(constantPairs); ok {
		t.Fatal("expected undefined kappa for constant-category pairs")
	}

	// Hand-calculated 3x3 linear-weighted kappa:
	// Pairs: 2x(0,0), 1x(0,1), 2x(1,1), 1x(1,2), 4x(2,2) => N=10
	// Po = (8*1 + 2*0.5)/10 = 0.90
	// Row sums: r=[3,3,4], Col sums: c=[2,3,5]
	// Weighted E: w(0,0)*6 + w(0,1)*9 + w(0,2)*15 + w(1,0)*6 + w(1,1)*9 + w(1,2)*15 + w(2,0)*8 + w(2,1)*12 + w(2,2)*20
	// = 6 + 4.5 + 0 + 3 + 9 + 7.5 + 0 + 6 + 20 = 56; Pe = 56/100 = 0.56
	// Kappa = (0.90 - 0.56) / (1 - 0.56) = 0.34 / 0.44 = 0.772727...
	handPairs := []quality.ScorePair{
		{0, 0}, {0, 0}, {0, 1}, {1, 1}, {1, 1},
		{1, 2}, {2, 2}, {2, 2}, {2, 2}, {2, 2},
	}
	k, ok := quality.LinearWeightedKappa(handPairs)
	if !ok || math.Abs(k-0.34/0.44) > 1e-9 {
		t.Fatalf("kappa=%v ok=%v, want %.9f", k, ok, 0.34/0.44)
	}

	// Undefined kappa in full submission marks gate inconclusive.
	_, key, _, _, _, syn := loadScoringFixtures(t)
	ra, rb, adj := cloneSubmission(syn.PassingArithmeticSet)
	factualThresholdFixture(ra, rb, adj, true)
	for i := range ra.Rows {
		ra.Rows[i].Rating.ActionableGuidance = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(2)}
		rb.Rows[i].Rating.ActionableGuidance = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(2)}
		adj.Rows[i].RaterAInitial.ActionableGuidance = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(2)}
		adj.Rows[i].RaterBInitial.ActionableGuidance = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(2)}
		adj.Rows[i].Adjudicated.ActionableGuidance = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(2)}
		adj.Rows[i].Disagreement = quality.RatingsDisagree(*adj.Rows[i].RaterAInitial, *adj.Rows[i].RaterBInitial)
	}
	undefReport, err := quality.Evaluate(key, ra, rb, adj, quality.DatasetKindSyntheticRegression, quality.ScoreOptions{})
	if err != nil {
		t.Fatalf("evaluate constant-guidance submission: %v", err)
	}
	if undefReport.Gate.Passed || undefReport.Gate.Status != quality.StatusInconclusive || undefReport.GuidanceAgreement.Defined ||
		undefReport.Gate.FactualPassAtLeast90 {
		t.Fatalf("undefined kappa must mark gate inconclusive: %+v kappa=%+v", undefReport.Gate, undefReport.GuidanceAgreement)
	}

	// Property tests for Wilson CI and linear-weighted kappa + bootstrap CI.
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 200).Draw(t, "n")
		k := rapid.IntRange(0, n).Draw(t, "k")
		w := quality.WilsonCI95(k, n)
		p := float64(k) / float64(n)
		if !w.Defined || w.Lower < 0 || w.Upper > 1 || w.Lower > p+1e-12 || p > w.Upper+1e-12 {
			t.Fatalf("WilsonCI95(%d,%d)=%+v violates [0,p,1] bounds", k, n, w)
		}
		sym := quality.WilsonCI95(n-k, n)
		if math.Abs(w.Lower-(1.0-sym.Upper)) > 1e-12 {
			t.Fatalf("WilsonCI95 symmetry failed: (%d,%d)=%+v vs (%d,%d)=%+v", k, n, w, n-k, n, sym)
		}

		m := rapid.IntRange(2, 40).Draw(t, "m")
		pairs := make([]quality.ScorePair, m)
		for i := range pairs {
			pairs[i] = quality.ScorePair{
				RaterA: rapid.IntRange(0, 2).Draw(t, "ra"),
				RaterB: rapid.IntRange(0, 2).Draw(t, "rb"),
			}
		}
		k1, ok1 := quality.LinearWeightedKappa(pairs)
		rev := slices.Clone(pairs)
		slices.Reverse(rev)
		k2, ok2 := quality.LinearWeightedKappa(rev)
		if ok1 != ok2 || (ok1 && math.Abs(k1-k2) > 1e-12) {
			t.Fatalf("kappa order invariance failed: (%v,%v) vs (%v,%v)", k1, ok1, k2, ok2)
		}
		if ok1 && (k1 < -1.0 || k1 > 1.0) {
			t.Fatalf("kappa %v outside [-1, 1]", k1)
		}
		boot, undef := quality.BootstrapKappaCI95(pairs, 200, 42)
		if boot.Defined && (boot.Lower < -1.0 || boot.Upper > 1.0 || boot.Lower > boot.Upper) {
			t.Fatalf("invalid bootstrap CI %+v (undef=%d)", boot, undef)
		}
	})
}
