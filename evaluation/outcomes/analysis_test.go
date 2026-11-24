package outcomes

import (
	"fmt"
	"math"
)

const (
	minimumEffect        = 5.0
	expectedEffect       = 10.0
	withinArmSD          = 15.0
	targetPower          = 0.80
	confidenceLevel      = 0.95
	maxScore             = 100.0
	maxArmMissingPercent = 15
	maxMissingGapPercent = 10
)

var missingScoreShifts = [...]float64{0, 2.5, 5}

type outcome struct {
	baseline float64
	score    float64
	observed bool
}

type estimate struct {
	difference float64
	interval   *scoreInterval
}

type scoreInterval struct {
	lower, upper float64
}

type armSummary struct {
	assigned, scored int
}

func (a armSummary) missing() int {
	return a.assigned - a.scored
}

type armData struct {
	summary          armSummary
	observed         scoreMoments
	missingBaselines []float64
}

type sensitivityResult struct {
	shift  float64
	effect estimate
}

type analysis struct {
	graph, control armSummary
	primary        *estimate
	sensitivity    []sensitivityResult
	improvement    bool
}

func sampleSize() (scored, assigned int) {
	gap := expectedEffect - minimumEffect
	z := criticalValue() + normalQuantile(targetPower)
	scaled := withinArmSD * z / gap
	scored = int(math.Ceil(2 * scaled * scaled))
	remainingPercent := 100 - maxArmMissingPercent
	assigned = (100*scored + remainingPercent - 1) / remainingPercent
	return scored, assigned
}

func analyze(graph, control []outcome) (analysis, error) {
	graphData, err := summarize(graph)
	if err != nil {
		return analysis{}, fmt.Errorf("graph arm: %w", err)
	}
	controlData, err := summarize(control)
	if err != nil {
		return analysis{}, fmt.Errorf("control arm: %w", err)
	}
	result := analysis{graph: graphData.summary, control: controlData.summary}
	if result.graph.assigned == 0 || result.control.assigned == 0 {
		return result, nil
	}

	if result.graph.missing()+result.control.missing() == 0 {
		if fit, ok := adjustedFit(graphData.observed, controlData.observed); ok {
			result.primary = newEstimate(fit.difference, fit.variance)
		}
	} else if graphData.canImpute() && controlData.canImpute() {
		result.primary, result.sensitivity = imputedEstimates(graphData, controlData)
	}

	result.improvement = result.permitsImprovement()
	return result, nil
}

func summarize(rows []outcome) (armData, error) {
	data := armData{summary: armSummary{assigned: len(rows)}}
	for i, row := range rows {
		if !validScore(row.baseline) {
			return armData{}, fmt.Errorf("baseline at row %d is outside [0, %.0f]", i+1, maxScore)
		}
		if !row.observed {
			data.missingBaselines = append(data.missingBaselines, row.baseline)
			continue
		}
		if !validScore(row.score) {
			return armData{}, fmt.Errorf("score at row %d is outside [0, %.0f]", i+1, maxScore)
		}
		data.observed.add(row.baseline, row.score)
	}
	data.summary.scored = data.observed.n
	return data, nil
}

func validScore(score float64) bool {
	return 0 <= score && score <= maxScore
}

func (a analysis) permitsImprovement() bool {
	_, required := sampleSize()
	if a.graph.assigned < required || a.control.assigned < required ||
		!meetsAttritionLimits(a.graph, a.control) || !clearsMargin(a.primary) {
		return false
	}
	if a.graph.missing()+a.control.missing() > 0 && len(a.sensitivity) != len(missingScoreShifts)-1 {
		return false
	}
	for _, sensitivity := range a.sensitivity {
		if !clearsMargin(&sensitivity.effect) {
			return false
		}
	}
	return true
}

func clearsMargin(effect *estimate) bool {
	return effect != nil && effect.interval != nil && effect.interval.lower > minimumEffect
}

func meetsAttritionLimits(graph, control armSummary) bool {
	graphAssigned, controlAssigned := int64(graph.assigned), int64(control.assigned)
	graphMissing, controlMissing := int64(graph.missing()), int64(control.missing())
	if 100*graphMissing > maxArmMissingPercent*graphAssigned ||
		100*controlMissing > maxArmMissingPercent*controlAssigned {
		return false
	}

	// Compare the two missing-rate fractions on a common denominator.
	gap := graphMissing*controlAssigned - controlMissing*graphAssigned
	gapLimit := maxMissingGapPercent * graphAssigned * controlAssigned / 100
	return -gapLimit <= gap && gap <= gapLimit
}

type scoreMoments struct {
	n                                 int
	baselineOrigin, endOrigin         float64
	baselineMeanOffset, endMeanOffset float64
	baselineSS, endSS                 float64
	crossDeviation                    float64
}

func (m *scoreMoments) add(baseline, endpoint float64) {
	if m.n == 0 {
		m.baselineOrigin, m.endOrigin = baseline, endpoint
	}
	// Accumulate offsets to preserve small differences between nearby scores.
	baseline -= m.baselineOrigin
	endpoint -= m.endOrigin
	m.n++
	baselineDelta := baseline - m.baselineMeanOffset
	endDelta := endpoint - m.endMeanOffset
	m.baselineMeanOffset += baselineDelta / float64(m.n)
	m.endMeanOffset += endDelta / float64(m.n)
	m.baselineSS += baselineDelta * (baseline - m.baselineMeanOffset)
	m.endSS += endDelta * (endpoint - m.endMeanOffset)
	m.crossDeviation += baselineDelta * (endpoint - m.endMeanOffset)
}

type effectFit struct {
	difference, variance float64
}

func adjustedFit(graph, control scoreMoments) (effectFit, bool) {
	if graph.n == 0 || control.n == 0 {
		return effectFit{}, false
	}
	difference := graph.endOrigin - control.endOrigin + (graph.endMeanOffset - control.endMeanOffset)
	baselineGap := graph.baselineOrigin - control.baselineOrigin + (graph.baselineMeanOffset - control.baselineMeanOffset)
	baselineSS := graph.baselineSS + control.baselineSS
	if baselineSS == 0 && baselineGap != 0 {
		return effectFit{}, false
	}

	degrees := graph.n + control.n - 2
	explainedSS := 0.0
	armVariance := 1/float64(graph.n) + 1/float64(control.n)
	if baselineSS > 0 {
		// Remove the common within-arm baseline slope from the arm contrast.
		crossDeviation := graph.crossDeviation + control.crossDeviation
		slope := crossDeviation / baselineSS
		difference -= slope * baselineGap
		explainedSS = slope * crossDeviation
		armVariance += baselineGap * baselineGap / baselineSS
		degrees--
	}

	fit := effectFit{difference: difference}
	if degrees > 0 {
		residualSS := residualSumSquares(graph.endSS+control.endSS, explainedSS, graph.n+control.n)
		fit.variance = residualSS * armVariance / float64(degrees)
	}
	return fit, true
}

func residualSumSquares(total, explained float64, rows int) float64 {
	const float64Epsilon = 0x1p-52
	residual := total - explained
	// Nearly equal sums can leave accumulated rounding error after subtraction.
	roundoff := 8 * float64Epsilon * float64(rows) * max(total, explained)
	if residual <= roundoff {
		return 0
	}
	return residual
}

func newEstimate(difference, variance float64) *estimate {
	result := &estimate{difference: difference}
	if variance > 0 {
		margin := criticalValue() * math.Sqrt(variance)
		result.interval = &scoreInterval{lower: difference - margin, upper: difference + margin}
	}
	return result
}

func criticalValue() float64 {
	return normalQuantile((1 + confidenceLevel) / 2)
}

func normalQuantile(p float64) float64 {
	return math.Sqrt2 * math.Erfinv(2*p-1)
}
