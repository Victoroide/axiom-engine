package quality

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"slices"
)

// z95 is the standard normal 97.5th percentile for two-sided 95% confidence intervals.
const z95 = 1.959963984540054

type deterministicRNG struct {
	key     uint64
	counter uint64
}

func newDeterministicRNG(seed uint64) *deterministicRNG {
	return &deterministicRNG{key: seed}
}

func (r *deterministicRNG) nextUint64() uint64 {
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[:8], r.key)
	binary.BigEndian.PutUint64(buf[8:], r.counter)
	r.counter++
	sum := sha256.Sum256(buf[:])
	return binary.BigEndian.Uint64(sum[:8])
}

func (r *deterministicRNG) intN(n int) int {
	if n <= 1 {
		return 0
	}
	bound := uint64(n)
	limit := math.MaxUint64 - (math.MaxUint64 % bound)
	for {
		v := r.nextUint64()
		if v < limit {
			rem := v % bound
			if rem > math.MaxInt {
				continue
			}
			return int(rem)
		}
	}
}

// WilsonCI95 computes the two-sided 95% Wilson score binomial confidence interval.
func WilsonCI95(successes, trials int) ConfidenceInterval {
	if trials <= 0 {
		return ConfidenceInterval{Defined: false, Method: "wilson_score_95"}
	}
	if successes < 0 {
		successes = 0
	}
	if successes > trials {
		successes = trials
	}
	n := float64(trials)
	p := float64(successes) / n
	z2 := z95 * z95
	denom := 1.0 + z2/n
	center := (p + z2/(2.0*n)) / denom
	margin := (z95 * math.Sqrt((p*(1.0-p)+z2/(4.0*n))/n)) / denom
	return ConfidenceInterval{
		Defined: true,
		Lower:   max(0.0, center-margin),
		Upper:   min(1.0, center+margin),
		Method:  "wilson_score_95",
	}
}

// ScorePair holds paired 0-2 ordinal scores from two independent raters.
type ScorePair struct {
	RaterA int
	RaterB int
}

// LinearWeightedKappa computes Cohen's linear-weighted kappa across 0-2 ordinal classes.
// Returns (0, false) when fewer than two pairs exist or expected disagreement is zero.
func LinearWeightedKappa(pairs []ScorePair) (float64, bool) {
	kappa, defined, _ := linearWeightedKappa(pairs)
	return kappa, defined
}

// linearWeightedKappa also reports whether kappa meets MinGuidanceKappa. Linear weights make
// kappa = 1 - n*observed/expected, where observed sums |a-b| over the pairs and expected sums
// |i-j|*rowTotal(i)*columnTotal(j) over the classes. Integer sums decide the threshold, so a
// value exactly at 7/10 meets it.
func linearWeightedKappa(pairs []ScorePair) (kappa float64, defined, meetsThreshold bool) {
	n := len(pairs)
	if n < 2 {
		return 0, false, false
	}
	var rowSums, colSums [3]int
	observed := 0
	for _, p := range pairs {
		if p.RaterA < 0 || p.RaterA > 2 || p.RaterB < 0 || p.RaterB > 2 {
			return 0, false, false
		}
		rowSums[p.RaterA]++
		colSums[p.RaterB]++
		observed += absInt(p.RaterA - p.RaterB)
	}
	expected := 0
	for i := range 3 {
		for j := range 3 {
			expected += absInt(i-j) * rowSums[i] * colSums[j]
		}
	}
	if expected == 0 {
		return 0, false, false
	}
	kappa = clampUnitKappa(1.0 - float64(n*observed)/float64(expected))
	// kappa >= num/den  <=>  den*(expected - n*observed) >= num*expected
	meetsThreshold = minGuidanceKappaDenominator*(expected-n*observed) >= minGuidanceKappaNumerator*expected
	return kappa, true, meetsThreshold
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// BootstrapKappaCI95 computes a seeded paired case-bootstrap 95% interval for linear-weighted kappa.
func BootstrapKappaCI95(pairs []ScorePair, runs int, seed uint64) (ci ConfidenceInterval, undefinedReplicates int) {
	if runs <= 0 {
		runs = DefaultBootstrapRuns
	}
	point, ok := LinearWeightedKappa(pairs)
	if !ok || len(pairs) < 2 {
		return ConfidenceInterval{Defined: false, Method: "paired_case_bootstrap_95"}, runs
	}
	_ = point
	rng := newDeterministicRNG(seed ^ 0x9e3779b97f4a7c15)
	sample := make([]ScorePair, len(pairs))
	vals := make([]float64, 0, runs)
	for range runs {
		for i := range len(pairs) {
			sample[i] = pairs[rng.intN(len(pairs))]
		}
		k, valid := LinearWeightedKappa(sample)
		if !valid {
			undefinedReplicates++
			continue
		}
		vals = append(vals, k)
	}
	// Require at least half of bootstrap replicates to remain defined to form a percentile CI.
	if len(vals) < 2 || len(vals)*2 < runs {
		return ConfidenceInterval{Defined: false, Method: "paired_case_bootstrap_95"}, undefinedReplicates
	}
	slices.Sort(vals)
	return ConfidenceInterval{
		Defined: true,
		Lower:   clampUnitKappa(quantile(vals, 0.025)),
		Upper:   clampUnitKappa(quantile(vals, 0.975)),
		Method:  "paired_case_bootstrap_95",
	}, undefinedReplicates
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1.0-frac) + sorted[hi]*frac
}

func clampUnitKappa(k float64) float64 {
	if k > 1.0 {
		return 1.0
	}
	if k < -1.0 {
		return -1.0
	}
	return k
}
