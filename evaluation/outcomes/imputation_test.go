package outcomes

import (
	"math"
	"math/rand/v2"
)

const imputations = 200

func (a armData) canImpute() bool {
	if len(a.missingBaselines) == 0 {
		return true
	}
	if a.observed.baselineSS > 0 {
		return a.observed.n > 2
	}
	for _, baseline := range a.missingBaselines {
		if baseline != a.observed.baselineOrigin {
			return false
		}
	}
	return a.observed.n > 1
}

func imputedEstimates(graph, control armData) (*estimate, []sensitivityResult) {
	random := rand.New(rand.NewPCG(8192, 32768))
	var pools [len(missingScoreShifts)]effectPool
	for range imputations {
		graphDraws := graph.completedMoments(random, -1)
		controlDraws := control.completedMoments(random, 1)
		for i := range pools {
			fit, ok := adjustedFit(graphDraws[i], controlDraws[i])
			if !ok {
				return nil, nil
			}
			pools[i].add(fit)
		}
	}

	sensitivity := make([]sensitivityResult, 0, len(pools)-1)
	for i, shift := range missingScoreShifts[1:] {
		sensitivity = append(sensitivity, sensitivityResult{shift: shift, effect: *pools[i+1].estimate()})
	}
	return pools[0].estimate(), sensitivity
}

func (a armData) completedMoments(random *rand.Rand, direction float64) [len(missingScoreShifts)]scoreMoments {
	var completed [len(missingScoreShifts)]scoreMoments
	for i := range completed {
		completed[i] = a.observed
	}
	if len(a.missingBaselines) == 0 {
		return completed
	}

	m := a.observed
	degrees := m.n - 1
	explainedSS := 0.0
	slope := 0.0
	if m.baselineSS > 0 {
		slope = m.crossDeviation / m.baselineSS
		explainedSS = slope * m.crossDeviation
		degrees--
	}
	residualSS := residualSumSquares(m.endSS, explainedSS, m.n)

	// Draw residual variance and the regression coefficients once per arm per imputation.
	center := m.endMeanOffset
	sd := 0.0
	if residualSS > 0 {
		chiSquared := 0.0
		for range degrees {
			z := random.NormFloat64()
			chiSquared += z * z
		}
		sd = math.Sqrt(residualSS / chiSquared)
		center += sd * random.NormFloat64() / math.Sqrt(float64(m.n))
		if m.baselineSS > 0 {
			slope += sd * random.NormFloat64() / math.Sqrt(m.baselineSS)
		}
	}

	for _, baseline := range a.missingBaselines {
		centeredBaseline := baseline - m.baselineOrigin - m.baselineMeanOffset
		endOffset := center + slope*centeredBaseline + sd*random.NormFloat64()
		score := boundedScore(m.endOrigin + endOffset)
		for i, shift := range missingScoreShifts {
			completed[i].add(baseline, boundedScore(score+direction*shift))
		}
	}
	return completed
}

func boundedScore(score float64) float64 {
	return min(max(0, score), maxScore)
}

type effectPool struct {
	n                       int
	mean, betweenSS, within float64
}

func (p *effectPool) add(fit effectFit) {
	p.n++
	delta := fit.difference - p.mean
	p.mean += delta / float64(p.n)
	p.betweenSS += delta * (fit.difference - p.mean)
	p.within += fit.variance
}

func (p effectPool) estimate() *estimate {
	m := float64(p.n)
	// Rubin's total variance includes both the average fit variance and imputation variance.
	variance := p.within/m + (1+1/m)*p.betweenSS/(m-1)
	return newEstimate(p.mean, variance)
}
