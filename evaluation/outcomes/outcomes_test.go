package outcomes

import (
	"fmt"
	"go/build"
	"math"
	"testing"
)

func TestSampleSize(t *testing.T) {
	scored, assigned := sampleSize()
	if scored != 142 || assigned != 168 {
		t.Fatalf("sample size per arm = %d scored / %d assigned, want 142 / 168", scored, assigned)
	}
}

func TestSyntheticAnalysis(t *testing.T) {
	for _, tc := range []struct {
		name        string
		effect      float64
		lower       float64
		upper       float64
		improvement bool
	}{
		{"null", 0, -3.2173, 3.2173, false},
		{"known effect", 10, 6.7827, 13.2173, true},
		{"estimate clears margin and interval crosses it", 8, 4.7827, 11.2173, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph, control := centeredScores(168, 55+tc.effect, 15), centeredScores(168, 55, 15)
			result := checkedAnalysis(t, graph, control)
			interval := checkedInterval(t, result.primary)
			assertNear(t, "effect", result.primary.difference, tc.effect)
			assertNear(t, "CI lower", interval.lower, tc.lower)
			assertNear(t, "CI upper", interval.upper, tc.upper)
			if result.improvement != tc.improvement {
				t.Errorf("improvement = %v, want %v", result.improvement, tc.improvement)
			}
		})
	}
}

func TestBaselineImbalanceAlone(t *testing.T) {
	result := checkedAnalysis(t, baselineScores(56, 112, 0), baselineScores(112, 56, 0))
	interval := checkedInterval(t, result.primary)
	assertNear(t, "effect at common baseline", result.primary.difference, 0)
	if interval.lower >= 0 || interval.upper <= 0 || result.improvement {
		t.Errorf("baseline imbalance alone produced interval %+v and improvement = %v", interval, result.improvement)
	}
}

func TestBaselineDependentMissingness(t *testing.T) {
	graph, control := baselineScores(84, 84, 10), baselineScores(84, 84, 0)
	complete := checkedAnalysis(t, graph, control)
	completeInterval := checkedInterval(t, complete.primary)

	graph, control = baselineDependentDropout()
	result := checkedAnalysis(t, graph, control)
	interval := checkedInterval(t, result.primary)
	if math.Abs(result.primary.difference-10) > 0.12 {
		t.Errorf("effect after baseline-conditional imputation = %.4f, want close to 10", result.primary.difference)
	}
	if interval.upper-interval.lower <= completeInterval.upper-completeInterval.lower {
		t.Errorf("imputed CI %+v should be wider than complete-data CI %+v", interval, completeInterval)
	}
	if !result.improvement || len(result.sensitivity) != 2 {
		t.Errorf("known effect should clear all missing-score scenarios: %+v", result)
	}
}

func TestAdverseShiftPreventsImprovement(t *testing.T) {
	graph := withMissing(centeredScores(168, 60.5, 1), 16)
	control := withMissing(centeredScores(168, 55, 1), 16)
	result := checkedAnalysis(t, graph, control)
	primary := checkedInterval(t, result.primary)
	if primary.lower <= 5 || !meetsAttritionLimits(result.graph, result.control) {
		t.Fatalf("fixture should clear the primary and attrition gates: %+v, %+v", primary, result)
	}
	if len(result.sensitivity) != 2 || result.sensitivity[0].shift != 2.5 || result.sensitivity[1].shift != 5 {
		t.Fatalf("expected the prespecified shifts: %+v", result.sensitivity)
	}
	moderate := result.sensitivity[0].effect
	severe := result.sensitivity[1].effect
	if checkedInterval(t, &moderate).lower >= 5 || checkedInterval(t, &severe).upper >= 5 {
		t.Errorf("expected tipping point at 2.5 and entire interval below 5 at 5 points: %+v", result.sensitivity)
	}
	if math.Abs(severe.difference-4.5476) > 0.08 {
		t.Errorf("effect under 5-point shift = %.4f, want close to 4.5476", severe.difference)
	}
	if result.improvement {
		t.Error("an adverse missing-score shift reverses the conclusion")
	}
}

func TestPooledVarianceIncludesImputationUncertainty(t *testing.T) {
	var pool effectPool
	pool.add(effectFit{difference: 8, variance: 1})
	pool.add(effectFit{difference: 12, variance: 3})
	pooled := pool.estimate()
	interval := checkedInterval(t, pooled)
	assertNear(t, "pooled difference", pooled.difference, 10)
	assertNear(t, "CI lower", interval.lower, 2.6665)
	assertNear(t, "CI upper", interval.upper, 17.3335)
}

func TestRecruitmentTarget(t *testing.T) {
	for _, count := range []int{167, 168} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			result := checkedAnalysis(t, centeredScores(count, 70, 1), centeredScores(168, 50, 1))
			if checkedInterval(t, result.primary).lower <= 5 {
				t.Fatal("fixture should clear the primary interval gate")
			}
			if result.improvement != (count == 168) {
				t.Errorf("improvement with %d graph assignments = %v", count, result.improvement)
			}
		})
	}
}

func TestPublishedAttritionLimits(t *testing.T) {
	for _, tc := range []struct {
		name           string
		graph, control armSummary
		allowed        bool
	}{
		{"seven-point gap", armSummary{assigned: 168, scored: 156}, armSummary{assigned: 168, scored: 168}, true},
		{"25 losses at target", armSummary{assigned: 168, scored: 143}, armSummary{assigned: 168, scored: 143}, true},
		{"26 losses at target", armSummary{assigned: 168, scored: 142}, armSummary{assigned: 168, scored: 142}, false},
		{"exactly both limits", armSummary{assigned: 180, scored: 153}, armSummary{assigned: 200, scored: 190}, true},
		{"one extra loss above rate", armSummary{assigned: 180, scored: 152}, armSummary{assigned: 200, scored: 188}, false},
		{"exact gap with unequal denominators", armSummary{assigned: 180, scored: 162}, armSummary{assigned: 200, scored: 200}, true},
		{"one extra loss above gap", armSummary{assigned: 200, scored: 200}, armSummary{assigned: 180, scored: 161}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := withMissing(centeredScores(tc.graph.assigned, 70, 1), tc.graph.missing())
			control := withMissing(centeredScores(tc.control.assigned, 50, 1), tc.control.missing())
			result := checkedAnalysis(t, graph, control)
			if checkedInterval(t, result.primary).lower <= 5 {
				t.Fatal("fixture should clear the primary interval gate")
			}
			for _, caseResult := range result.sensitivity {
				if checkedInterval(t, &caseResult.effect).lower <= 5 {
					t.Fatal("fixture should clear the sensitivity interval gate")
				}
			}
			if result.improvement != tc.allowed {
				t.Errorf("improvement with %d/%d and %d/%d missing = %v, want %v",
					tc.graph.missing(), tc.graph.assigned, tc.control.missing(), tc.control.assigned,
					result.improvement, tc.allowed)
			}
		})
	}
}

func TestDecisionAtMeaningfulMargin(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lower float64
		want  bool
	}{
		{"below", math.Nextafter(5, 0), false},
		{"equal", 5, false},
		{"above", math.Nextafter(5, 10), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			complete := armSummary{assigned: 168, scored: 168}
			nearMargin := &estimate{difference: 8, interval: &scoreInterval{lower: tc.lower, upper: 11}}
			result := analysis{graph: complete, control: complete, primary: nearMargin}
			if got := result.permitsImprovement(); got != tc.want {
				t.Errorf("decision with primary lower endpoint %.17g = %v, want %v", tc.lower, got, tc.want)
			}

			result.graph.scored--
			result.primary = &estimate{difference: 10, interval: &scoreInterval{lower: 8, upper: 12}}
			result.sensitivity = []sensitivityResult{{shift: 2.5, effect: *result.primary}, {shift: 5, effect: *nearMargin}}
			if got := result.permitsImprovement(); got != tc.want {
				t.Errorf("decision with sensitivity lower endpoint %.17g = %v, want %v", tc.lower, got, tc.want)
			}
		})
	}
}

func TestPointEstimateWithoutInterval(t *testing.T) {
	result := checkedAnalysis(t, centeredScores(168, 70, 0), centeredScores(168, 60, 0))
	if result.primary == nil {
		t.Fatal("point estimate unavailable")
	}
	assertNear(t, "effect", result.primary.difference, 10)
	if result.primary.interval != nil || result.improvement {
		t.Errorf("zero residual variance produced interval %+v and improvement = %v", result.primary.interval, result.improvement)
	}
}

func TestResidualVarianceByRowOrder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture affineFixture
	}{
		{"perfect fit", affineFixture{baselineStep: 2, slope: 1}},
		{"perfect fit with close baselines", affineFixture{baselineStart: 0x1p-13, baselineStep: 0x1p-25, slope: 1}},
		{"perfect fit with slope seven", affineFixture{baselineStep: 2, slope: 7}},
		{"small genuine residuals", affineFixture{baselineStep: 2, slope: 1, variation: 1e-6}},
	} {
		for _, order := range []struct {
			name             string
			missing, grouped bool
		}{
			{name: "complete/alternating"},
			{name: "complete/grouped", grouped: true},
			{name: "missing/alternating", missing: true},
			{name: "missing/grouped", missing: true, grouped: true},
		} {
			t.Run(tc.name+"/"+order.name, func(t *testing.T) {
				graph := tc.fixture.scores(10, order.missing, order.grouped)
				control := tc.fixture.scores(0, order.missing, order.grouped)
				result := checkedAnalysis(t, graph, control)
				if result.primary == nil {
					t.Fatal("point estimate unavailable")
				}
				assertNear(t, "adjusted effect", result.primary.difference, 10)
				if tc.fixture.variation == 0 {
					if result.primary.interval != nil || result.improvement {
						t.Errorf("perfect fit produced interval %+v and improvement = %v", result.primary.interval, result.improvement)
					}
					return
				}
				interval := checkedInterval(t, result.primary)
				if interval.lower >= result.primary.difference || interval.upper <= result.primary.difference || !result.improvement {
					t.Errorf("genuine residuals produced interval %+v and improvement = %v", interval, result.improvement)
				}
			})
		}
	}
}

type affineFixture struct {
	baselineStart, baselineStep, slope, variation float64
}

func (f affineFixture) scores(effect float64, missing, grouped bool) []outcome {
	rows := make([]outcome, 168)
	for i := range rows {
		baseline := f.baselineStart + f.baselineStep*float64(i%2)
		score := f.slope*baseline + effect
		if f.variation > 0 {
			// Move zero scores away from the floor before adding balanced residuals.
			score += 1 + f.variation
			if (i/2)%2 != 0 {
				score -= 2 * f.variation
			}
		}
		row := outcome{baseline: baseline, score: score, observed: true}
		if missing && i < 2 {
			row = outcome{baseline: baseline}
		}
		index := i
		if grouped {
			index = i/2 + (i%2)*(len(rows)/2)
		}
		rows[index] = row
	}
	return rows
}

func TestUnavailableEstimate(t *testing.T) {
	control := centeredScores(168, 55, 2)
	for _, tc := range []struct {
		name    string
		graph   []outcome
		control []outcome
	}{
		{"empty arm", nil, control},
		{"all scores missing in one arm", withMissing(control, 168), control},
		{"arm confounded with baseline", []outcome{{baseline: 10, score: 80, observed: true}}, control},
		{"missing baseline outside constant observed support", []outcome{
			{baseline: 90}, {baseline: 50, score: 55, observed: true}, {baseline: 50, score: 60, observed: true},
		}, control},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := checkedAnalysis(t, tc.graph, tc.control)
			if result.primary != nil || result.improvement {
				t.Errorf("unsupported estimate produced %+v", result)
			}
		})
	}
}

func TestRecordedZero(t *testing.T) {
	graph := []outcome{{baseline: 50, score: 0, observed: true}}
	result := checkedAnalysis(t, graph, centeredScores(168, 55, 2))
	if result.primary == nil || result.graph.scored != 1 {
		t.Fatalf("zero recorded as missing: %+v", result)
	}
	assertNear(t, "effect", result.primary.difference, -55)
}

func TestInvalidScores(t *testing.T) {
	for _, score := range []float64{-1, 101, math.NaN(), math.Inf(1)} {
		t.Run(fmt.Sprint(score), func(t *testing.T) {
			if _, err := analyze(nil, []outcome{{score: score, observed: true}}); err == nil {
				t.Error("invalid recorded score accepted")
			}
			if _, err := analyze(nil, []outcome{{baseline: score}}); err == nil {
				t.Error("invalid baseline accepted for a missing outcome")
			}
		})
	}
}

func TestSyntheticEvaluationIsTestOnly(t *testing.T) {
	pkg, err := build.Default.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.GoFiles) != 0 || len(pkg.CgoFiles) != 0 {
		t.Fatalf("evaluation source is visible to a normal build: Go %v, cgo %v", pkg.GoFiles, pkg.CgoFiles)
	}
}

func Example_unaidedTransfer() {
	scored, assigned := sampleSize()
	fmt.Printf("Required per arm: %d scores, %d assignments\n", scored, assigned)

	graph, control := baselineDependentDropout()
	printExample("Baseline-dependent dropout", graph, control)
	graph = withMissing(centeredScores(assigned, 60.5, 1), 16)
	control = withMissing(centeredScores(assigned, 55, 1), 16)
	printExample("Tipping point", graph, control)
	// Output:
	// Required per arm: 142 scores, 168 assignments
	// Baseline-dependent dropout, scored 152/168 graph and 152/168 control: improvement
	//   Shift 0.0: +10.00 points (95% CI 9.55 to 10.46)
	//   Shift 2.5: +9.52 points (95% CI 9.05 to 10.00)
	//   Shift 5.0: +9.05 points (95% CI 8.50 to 9.59)
	// Tipping point, scored 152/168 graph and 152/168 control: inconclusive
	//   Shift 0.0: +5.50 points (95% CI 5.27 to 5.73)
	//   Shift 2.5: +5.02 points (95% CI 4.75 to 5.30)
	//   Shift 5.0: +4.55 points (95% CI 4.16 to 4.93)
}

func printExample(label string, graph, control []outcome) {
	result, err := analyze(graph, control)
	if err != nil {
		panic(err)
	}
	conclusion := "inconclusive"
	if result.improvement {
		conclusion = "improvement"
	}
	fmt.Printf("%s, scored %d/%d graph and %d/%d control: %s\n",
		label, result.graph.scored, result.graph.assigned, result.control.scored, result.control.assigned, conclusion)
	printEffect(0, *result.primary)
	for _, scenario := range result.sensitivity {
		printEffect(scenario.shift, scenario.effect)
	}
}

func printEffect(shift float64, effect estimate) {
	fmt.Printf("  Shift %.1f: %+.2f points (95%% CI %.2f to %.2f)\n",
		shift, effect.difference, effect.interval.lower, effect.interval.upper)
}

func baselineDependentDropout() (graph, control []outcome) {
	graph, control = baselineScores(84, 84, 10), baselineScores(84, 84, 0)
	for i := range 16 {
		graph[84+i] = outcome{baseline: graph[84+i].baseline}
		control[i] = outcome{baseline: control[i].baseline}
	}
	return graph, control
}

func baselineScores(low, high int, effect float64) []outcome {
	rows := make([]outcome, low+high)
	for i := range rows {
		baseline := 40.0
		if i >= low {
			baseline = 70
		}
		noise := -2.0
		if i%2 != 0 {
			noise = 2
		}
		rows[i] = outcome{baseline: baseline, score: baseline + effect + noise, observed: true}
	}
	return rows
}

func centeredScores(count int, mean, spread float64) []outcome {
	rows := make([]outcome, count)
	for i := range rows {
		score := mean - spread
		if i%2 != 0 {
			score = mean + spread
		}
		rows[i] = outcome{baseline: 50, score: score, observed: true}
	}
	return rows
}

func withMissing(scores []outcome, count int) []outcome {
	rows := append([]outcome(nil), scores...)
	for i := range count {
		rows[i] = outcome{baseline: rows[i].baseline}
	}
	return rows
}

func checkedAnalysis(t *testing.T, graph, control []outcome) analysis {
	t.Helper()
	result, err := analyze(graph, control)
	if err != nil {
		t.Fatal(err)
	}
	if result.graph.assigned != len(graph) || result.control.assigned != len(control) {
		t.Fatalf("assigned denominators differ from the rows: %+v", result)
	}
	return result
}

func checkedInterval(t *testing.T, effect *estimate) *scoreInterval {
	t.Helper()
	if effect == nil || effect.interval == nil {
		t.Fatalf("interval unavailable: %+v", effect)
	}
	return effect.interval
}

func assertNear(t *testing.T, name string, got, want float64) {
	t.Helper()
	if !(math.Abs(got-want) <= 0.0001) {
		t.Errorf("%s = %.6f, want %.4f", name, got, want)
	}
}
