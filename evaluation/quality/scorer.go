package quality

import (
	"fmt"
	"slices"
	"sort"
)

type evalObservation struct {
	entry              IdentityEntry
	initialA           *DimensionRatings
	initialB           *DimensionRatings
	final              *DimensionRatings
	suppliedResources  int
	emptyResourceItems int
}

// Evaluate validates the paired rating sheets and adjudication record and produces a ScoreReport.
// Missing ratings fail when opts.AllowIncomplete is false, or mark the report inconclusive when allowed.
// A missing rating is never treated as a pass.
func Evaluate(
	key IdentityKey,
	raterA, raterB RatingSheet,
	adj AdjudicationSheet,
	datasetKind string,
	opts ScoreOptions,
) (ScoreReport, error) {
	if datasetKind == "" {
		datasetKind = raterA.DatasetKind
	}
	if datasetKind != raterA.DatasetKind {
		return ScoreReport{}, fmt.Errorf("%w: report dataset_kind %q differs from rating sheet dataset_kind %q", ErrValidation, datasetKind, raterA.DatasetKind)
	}
	if err := ValidateSubmission(key, raterA, raterB, adj, opts.AllowIncomplete); err != nil {
		return ScoreReport{}, err
	}

	if opts.BootstrapRuns <= 0 {
		opts.BootstrapRuns = DefaultBootstrapRuns
	}
	if opts.BootstrapSeed == 0 {
		opts.BootstrapSeed = key.PacketSeed
	}

	ratersReady := raterReady(raterA) && raterReady(raterB)

	obs := make([]evalObservation, len(key.Entries))
	var raterARatings, raterBRatings int
	for i, entry := range key.Entries {
		ra, rb := raterA.Rows[i], raterB.Rows[i]
		o := evalObservation{entry: entry, final: adj.Rows[i].Adjudicated}
		if ra.Rating != nil {
			raterARatings++
		}
		if rb.Rating != nil {
			raterBRatings++
		}
		for _, item := range ra.Items {
			o.suppliedResources += len(item.Resources)
			if len(item.Resources) == 0 {
				o.emptyResourceItems++
			}
		}
		if ratersReady {
			o.initialA, o.initialB = ra.Rating, rb.Rating
		}
		obs[i] = o
	}

	overall := buildStratum("overall", obs, opts)
	dimFactual := summarizeDimension(obs, func(r DimensionRatings) RubricScore { return r.FactualAccuracy }, true)
	dimPrereq := summarizeDimension(obs, func(r DimensionRatings) RubricScore { return r.PrerequisiteRationale }, false)
	dimGuidance := summarizeDimension(obs, func(r DimensionRatings) RubricScore { return r.ActionableGuidance }, false)
	dimResources := summarizeResourceDimension(obs)

	var criticalIDs []string
	disagreements := 0
	for _, o := range obs {
		if o.initialA != nil && o.initialB != nil && RatingsDisagree(*o.initialA, *o.initialB) {
			disagreements++
		}
		if o.final != nil && *o.final.CriticalError {
			criticalIDs = append(criticalIDs, o.entry.CaseID)
		}
	}
	slices.Sort(criticalIDs)

	measured := datasetKind == DatasetKindMeasuredCandidate && ratersReady && overall.CompletedCases == overall.TotalCases
	report := ScoreReport{
		SchemaVersion:             key.SchemaVersion,
		DatasetKind:               datasetKind,
		MeasuredCandidate:         measured,
		ProvenanceNote:            provenanceSummary(datasetKind, measured),
		MinStratumAssessable:      MinAssessableStratum,
		TotalCases:                overall.TotalCases,
		CompletedCases:            overall.CompletedCases,
		MissingRatings:            overall.MissingRatings,
		RaterAInitialRatings:      raterARatings,
		RaterBInitialRatings:      raterBRatings,
		Disagreements:             disagreements,
		AdjudicatedCriticalErrors: overall.AdjudicatedCriticalErrors,
		CriticalErrorCaseIDs:      criticalIDs,
		Dimensions: map[string]DimensionSummary{
			"factual_accuracy":       dimFactual,
			"prerequisite_rationale": dimPrereq,
			"actionable_guidance":    dimGuidance,
			"resource_relevance":     dimResources,
		},
		GuidanceAgreement: overall.GuidanceAgreement,
		ByUnit:            aggregateStrata(obs, func(e IdentityEntry) string { return fmt.Sprintf("unit_%d", e.Unit) }, opts),
		ByTopic:           aggregateStrata(obs, func(e IdentityEntry) string { return fmt.Sprintf("%s %s", e.TopicID, e.TopicTitle) }, opts),
		ByPreference:      aggregateStrata(obs, func(e IdentityEntry) string { return e.VARKProfile }, opts),
		ByProvider:        aggregateStrata(obs, func(e IdentityEntry) string { return e.Provider }, opts),
		Gate:              evaluateGate(overall),
	}
	return report, nil
}

// BuildCandidateBlankReport computes the empty/unmeasured report from a blank packet set.
func BuildCandidateBlankReport(artifacts BlindArtifacts) (CandidateBlankReport, error) {
	report, err := Evaluate(
		artifacts.IdentityKey,
		artifacts.RaterABlank,
		artifacts.RaterBBlank,
		artifacts.Adjudication,
		DatasetKindUnmeasuredBlank,
		ScoreOptions{AllowIncomplete: true, BootstrapSeed: artifacts.IdentityKey.PacketSeed},
	)
	if err != nil {
		return CandidateBlankReport{}, err
	}
	return CandidateBlankReport{
		SchemaVersion:     artifacts.IdentityKey.SchemaVersion,
		DatasetKind:       DatasetKindUnmeasuredBlank,
		MeasuredCandidate: false,
		Status:            report.Gate.Status,
		Notice:            "Blank candidate packet: zero independent human ratings recorded. Factual accuracy, prerequisite validity, guidance agreement, and provider performance remain unmeasured.",
		ScoreReport:       report,
	}, nil
}

func provenanceSummary(kind string, measured bool) string {
	switch kind {
	case DatasetKindSyntheticRegression:
		return "SYNTHETIC REGRESSION ONLY: Computed over synthetic completed rating sheets to verify scorer arithmetic and gate rules; NOT measured candidate results."
	case DatasetKindUnmeasuredBlank:
		return "UNMEASURED BLANK PACKET: No independent specialist ratings recorded yet."
	default:
		if measured {
			return "MEASURED CANDIDATE EVALUATION: Locked independent specialist ratings and named-role adjudication."
		}
		return "CANDIDATE RATINGS IN PROGRESS: Independent rating and adjudication are incomplete."
	}
}

func evaluateGate(overall StratumSummary) GateEvaluation {
	allComplete := overall.MissingRatings == 0 && overall.CompletedCases == overall.TotalCases && overall.TotalCases > 0
	zeroCritical := overall.AdjudicatedCriticalErrors == 0
	minAssessable := overall.AssessableFactual >= MinAssessableStratum && overall.GuidanceAgreement.PairedAssessable >= MinAssessableStratum
	kappaDefined := overall.GuidanceAgreement.Defined && overall.GuidanceAgreement.BootstrapCI95.Defined
	passRateOK := overall.AssessableFactual > 0 && overall.FactualPassFraction >= MinFactualPassRate
	kappaOK := kappaDefined && overall.GuidanceAgreement.meetsThreshold

	var failures []string
	var inconclusives []string

	if !zeroCritical {
		failures = append(failures, fmt.Sprintf("adjudicated critical errors = %d (must be 0)", overall.AdjudicatedCriticalErrors))
	}
	if !allComplete {
		inconclusives = append(inconclusives, fmt.Sprintf("incomplete ratings (%d of %d cases completed; %d missing)", overall.CompletedCases, overall.TotalCases, overall.MissingRatings))
	}
	if !minAssessable {
		inconclusives = append(inconclusives, fmt.Sprintf("assessable cases below minimum %d (factual=%d, guidance_pairs=%d)", MinAssessableStratum, overall.AssessableFactual, overall.GuidanceAgreement.PairedAssessable))
	}
	if !kappaDefined {
		inconclusives = append(inconclusives, "linear-weighted guidance kappa or bootstrap interval is undefined")
	}
	thresholdsEligible := allComplete && minAssessable && kappaDefined
	if thresholdsEligible && !passRateOK {
		failures = append(failures, fmt.Sprintf("factual pass fraction %.4f is below %.2f", overall.FactualPassFraction, MinFactualPassRate))
	}
	if thresholdsEligible && !kappaOK {
		failures = append(failures, fmt.Sprintf("guidance linear-weighted kappa %.4f is below %.2f", overall.GuidanceAgreement.Kappa, MinGuidanceKappa))
	}

	// Critical errors decide the overall gate even when other metrics are inconclusive.
	status := StatusPass
	passed := true
	switch {
	case len(failures) > 0:
		status = StatusFail
		passed = false
	case len(inconclusives) > 0:
		status = StatusInconclusive
		passed = false
	}

	return GateEvaluation{
		Passed:                  passed,
		Status:                  status,
		ZeroCriticalErrors:      zeroCritical,
		FactualPassAtLeast90:    allComplete && passRateOK,
		GuidanceKappaAtLeast070: allComplete && kappaOK,
		AllRatingsComplete:      allComplete,
		MinimumAssessableMet:    minAssessable,
		KappaDefined:            kappaDefined,
		FailureReasons:          failures,
		InconclusiveReasons:     inconclusives,
	}
}

func aggregateStrata(obs []evalObservation, keyFn func(IdentityEntry) string, opts ScoreOptions) []StratumSummary {
	groups := make(map[string][]evalObservation)
	for _, o := range obs {
		k := keyFn(o.entry)
		groups[k] = append(groups[k], o)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]StratumSummary, 0, len(keys))
	for _, k := range keys {
		out = append(out, buildStratum(k, groups[k], opts))
	}
	return out
}

func buildStratum(key string, obs []evalObservation, opts ScoreOptions) StratumSummary {
	s := StratumSummary{
		Key:        key,
		TotalCases: len(obs),
	}
	var pairs []ScorePair
	guidanceAbstentions := 0
	guidanceMissing := 0

	for _, o := range obs {
		s.SuppliedResources += o.suppliedResources
		s.NAItems += o.emptyResourceItems
		if o.initialA == nil || o.initialB == nil {
			guidanceMissing++
		} else {
			ga := o.initialA.ActionableGuidance
			gb := o.initialB.ActionableGuidance
			if ga.Status == ScoreStatusScored && gb.Status == ScoreStatusScored {
				pairs = append(pairs, ScorePair{RaterA: *ga.Score, RaterB: *gb.Score})
			} else {
				guidanceAbstentions++
			}
		}
		if o.final == nil {
			s.MissingRatings++
			s.MissingResources += o.suppliedResources
			continue
		}
		s.CompletedCases++
		adj := *o.final
		if *adj.CriticalError {
			s.AdjudicatedCriticalErrors++
		}
		switch adj.FactualAccuracy.Status {
		case ScoreStatusScored:
			s.AssessableFactual++
			if *adj.FactualAccuracy.Score == 2 {
				s.FactualPasses++
			}
		case ScoreStatusAbstain:
			s.FactualAbstentions++
		}
		switch adj.PrerequisiteRationale.Status {
		case ScoreStatusScored:
			s.AssessablePrerequisites++
		case ScoreStatusAbstain:
			s.PrerequisiteAbstentions++
		}
		for _, rr := range adj.ResourceRatings {
			switch rr.Rating.Status {
			case ScoreStatusScored:
				s.AssessableResources++
			case ScoreStatusAbstain:
				s.ResourceAbstentions++
			}
		}
	}

	if s.AssessableFactual > 0 {
		s.FactualPassFraction = float64(s.FactualPasses) / float64(s.AssessableFactual)
	}
	s.FactualPassCI95 = WilsonCI95(s.FactualPasses, s.AssessableFactual)

	kappa, defined, meetsThreshold := linearWeightedKappa(pairs)
	bootCI, undefBoot := BootstrapKappaCI95(pairs, opts.BootstrapRuns, opts.BootstrapSeed)
	s.GuidanceAgreement = KappaSummary{
		PairedAssessable:             len(pairs),
		Abstentions:                  guidanceAbstentions,
		Missing:                      guidanceMissing,
		Kappa:                        kappa,
		Defined:                      defined,
		BootstrapCI95:                bootCI,
		BootstrapSamples:             opts.BootstrapRuns,
		UndefinedBootstrapReplicates: undefBoot,
		meetsThreshold:               meetsThreshold,
	}

	var inconclusives []string
	if s.MissingRatings > 0 {
		inconclusives = append(inconclusives, fmt.Sprintf("incomplete ratings (%d missing)", s.MissingRatings))
	}
	if s.AssessableFactual < MinAssessableStratum {
		inconclusives = append(inconclusives, fmt.Sprintf("assessable factual cases %d < %d", s.AssessableFactual, MinAssessableStratum))
	}
	if s.GuidanceAgreement.PairedAssessable < MinAssessableStratum {
		inconclusives = append(inconclusives, fmt.Sprintf("paired guidance ratings %d < %d", s.GuidanceAgreement.PairedAssessable, MinAssessableStratum))
	}
	if !s.GuidanceAgreement.Defined || !s.GuidanceAgreement.BootstrapCI95.Defined {
		inconclusives = append(inconclusives, "guidance kappa or bootstrap interval undefined")
	}
	s.InconclusiveReasons = inconclusives

	switch {
	case len(inconclusives) > 0:
		s.Status = StatusInconclusive
	case s.AdjudicatedCriticalErrors > 0:
		s.Status = StatusFail
	case s.FactualPassFraction < MinFactualPassRate || !s.GuidanceAgreement.meetsThreshold:
		s.Status = StatusFail
	default:
		s.Status = StatusPass
	}
	return s
}

func summarizeDimension(obs []evalObservation, pick func(DimensionRatings) RubricScore, includeCI bool) DimensionSummary {
	summary := DimensionSummary{TotalItems: len(obs)}
	for _, o := range obs {
		if o.final == nil {
			summary.Missing++
			continue
		}
		s := pick(*o.final)
		switch s.Status {
		case ScoreStatusAbstain:
			summary.Abstentions++
		case ScoreStatusNA:
			summary.NAItems++
		case ScoreStatusScored:
			summary.Assessable++
			switch *s.Score {
			case 0:
				summary.Score0++
			case 1:
				summary.Score1++
			case 2:
				summary.Score2++
			}
		}
	}
	if summary.Assessable > 0 {
		summary.PassFraction = float64(summary.Score2) / float64(summary.Assessable)
	}
	if includeCI {
		ci := WilsonCI95(summary.Score2, summary.Assessable)
		summary.PassCI95 = &ci
	}
	return summary
}

func summarizeResourceDimension(obs []evalObservation) DimensionSummary {
	var summary DimensionSummary
	for _, o := range obs {
		summary.TotalItems += o.suppliedResources
		summary.NAItems += o.emptyResourceItems
		if o.final == nil {
			summary.Missing += o.suppliedResources
			continue
		}
		for _, rr := range o.final.ResourceRatings {
			switch rr.Rating.Status {
			case ScoreStatusAbstain:
				summary.Abstentions++
			case ScoreStatusScored:
				summary.Assessable++
				switch *rr.Rating.Score {
				case 0:
					summary.Score0++
				case 1:
					summary.Score1++
				case 2:
					summary.Score2++
				}
			}
		}
	}
	if summary.Assessable > 0 {
		summary.PassFraction = float64(summary.Score2) / float64(summary.Assessable)
	}
	return summary
}
