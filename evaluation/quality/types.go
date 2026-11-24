package quality

import "github.com/Victoroide/axiom-engine/internal/domain"

// RubricScoreStatus identifies the state of a single rubric judgment.
const (
	ScoreStatusScored  = "scored"
	ScoreStatusAbstain = "abstain"
	ScoreStatusNA      = "na"
)

// ResourceSummaryStatus identifies whether resource relevance applies to an item.
const (
	ResourceSummaryApplicable = "applicable"
	ResourceSummaryNA         = "na"
)

// CriticalErrorType classifies critical errors recorded separately from 0-2 scores.
const (
	CriticalTypeNone         = "none"
	CriticalTypeFactual      = "factual"
	CriticalTypePrerequisite = "prerequisite"
	CriticalTypeBoth         = "both"
)

// EvaluationStatus classifies gate or stratum outcomes.
const (
	StatusPass         = "pass"
	StatusFail         = "fail"
	StatusInconclusive = "inconclusive"
)

// DatasetKind distinguishes synthetic regression sheets from real candidate evaluations.
const (
	DatasetKindSyntheticRegression = "synthetic_regression"
	DatasetKindUnmeasuredBlank     = "unmeasured_blank"
	DatasetKindMeasuredCandidate   = "measured_candidate"
)

// Source types identify original fixtures and fresh candidate responses.
const (
	SourceSyntheticAuthored = "synthetic_authored"
	SourceUnseenCandidate   = "unseen_candidate"
)

// NamedAdjudicatorRole names the governance role responsible for resolving disagreements.
const NamedAdjudicatorRole = "Lead CS Curriculum Adjudicator"

// ProvisionalFactStatus marks reference facts as pending qualified educator verification.
const ProvisionalFactStatus = "provisional_educator_review_pending"

// The inclusive guidance kappa threshold is the ratio 7/10. The scorer decides it from integer
// agreement counts so that exactly 0.70 passes whatever the floating-point rounding of the sums.
const (
	minGuidanceKappaNumerator   = 7
	minGuidanceKappaDenominator = 10
)

// Gate thresholds and minimum assessable stratum requirements.
const (
	MinAssessableStratum = 10
	MinFactualPassRate   = 0.90
	MinGuidanceKappa     = float64(minGuidanceKappaNumerator) / minGuidanceKappaDenominator
	DefaultBootstrapRuns = 2000
)

// ObjectiveCatalog stores the official AP CS A Fall 2025 learning objectives.
type ObjectiveCatalog struct {
	SchemaVersion       string         `json:"schema_version"`
	Framework           string         `json:"framework"`
	FrameworkURL        string         `json:"framework_url"`
	FrameworkRevision   string         `json:"framework_revision"`
	ClarificationsURL   string         `json:"clarifications_url"`
	ClarificationsRev   string         `json:"clarifications_revision"`
	ClarificationsNote  string         `json:"clarifications_note"`
	UnitObjectiveCounts map[string]int `json:"unit_objective_counts"`
	TotalObjectives     int            `json:"total_objectives"`
	Objectives          []Objective    `json:"objectives"`
}

// Objective represents one College Board AP CS A learning objective with locators.
type Objective struct {
	ID         string `json:"id"`
	Unit       int    `json:"unit"`
	UnitTitle  string `json:"unit_title"`
	TopicID    string `json:"topic_id"`
	TopicTitle string `json:"topic_title"`
	CEDPage    int    `json:"ced_page"`
	PDFPage    int    `json:"pdf_page"`
	Section    string `json:"section"`
	URL        string `json:"url"`
}

// ReferenceFactCatalog stores provisional educator-reviewable reference facts.
type ReferenceFactCatalog struct {
	SchemaVersion     string          `json:"schema_version"`
	ProvisionalNotice string          `json:"provisional_notice"`
	Facts             []ReferenceFact `json:"facts"`
}

// ReferenceFact records a provisional reference fact with official source locators.
type ReferenceFact struct {
	ID                  string            `json:"id"`
	Status              string            `json:"status"`
	Unit                int               `json:"unit"`
	ObjectiveIDs        []string          `json:"objective_ids"`
	Statement           string            `json:"statement"`
	CommonMisconception string            `json:"common_misconception"`
	JavaAssertion       *JavaAssertion    `json:"java_assertion,omitempty"`
	References          []SourceReference `json:"references"`
}

// JavaAssertion records a concise Java snippet and its expected outcome.
type JavaAssertion struct {
	Snippet         string `json:"snippet"`
	ExpectedOutcome string `json:"expected_outcome"`
}

// SourceReference links a fact to an official source and its relevant section.
type SourceReference struct {
	Document    string `json:"document"`
	Section     string `json:"section"`
	PageLocator string `json:"page_locator"`
	URL         string `json:"url"`
}

// BenchmarkSuite holds the calibration examples and designated evaluation cases.
type BenchmarkSuite struct {
	SchemaVersion    string            `json:"schema_version"`
	Course           string            `json:"course"`
	Language         string            `json:"language"`
	Seed             uint64            `json:"seed"`
	DatasetKind      string            `json:"dataset_kind"`
	CalibrationCases []CalibrationCase `json:"calibration_cases"`
	EvaluationCases  []EvaluationCase  `json:"evaluation_cases"`
}

// CalibrationCase represents a separate training example with worked anchor ratings.
type CalibrationCase struct {
	ID                string                     `json:"id"`
	Unit              int                        `json:"unit"`
	TopicID           string                     `json:"topic_id"`
	TopicTitle        string                     `json:"topic_title"`
	ObjectiveIDs      []string                   `json:"objective_ids"`
	ReferenceFactIDs  []string                   `json:"reference_fact_ids"`
	PromptContext     string                     `json:"prompt_context"`
	Request           domain.AdaptivePlanRequest `json:"request"`
	CandidateResponse domain.PlanResponse        `json:"candidate_response"`
	ResourceEvidence  []ResourceEvidence         `json:"resource_evidence,omitempty"`
	ShippedGraph      ShippedGraphStatus         `json:"shipped_graph"`
	ScenarioTags      []string                   `json:"scenario_tags"`
	Provenance        Provenance                 `json:"provenance"`
	WorkedAnchor      WorkedAnchor               `json:"worked_anchor"`
}

// EvaluationCase represents one designated offline benchmark case.
type EvaluationCase struct {
	ID                string                     `json:"id"`
	Unit              int                        `json:"unit"`
	TopicID           string                     `json:"topic_id"`
	TopicTitle        string                     `json:"topic_title"`
	ObjectiveIDs      []string                   `json:"objective_ids"`
	ReferenceFactIDs  []string                   `json:"reference_fact_ids"`
	PromptContext     string                     `json:"prompt_context"`
	Request           domain.AdaptivePlanRequest `json:"request"`
	CandidateResponse domain.PlanResponse        `json:"candidate_response"`
	ResourceEvidence  []ResourceEvidence         `json:"resource_evidence,omitempty"`
	ShippedGraph      ShippedGraphStatus         `json:"shipped_graph"`
	ScenarioTags      []string                   `json:"scenario_tags"`
	Origin            ResponseOrigin             `json:"origin"`
	Provenance        Provenance                 `json:"provenance"`
}

// ShippedGraphStatus records whether shipped repository graphs serve a case's target topic.
type ShippedGraphStatus struct {
	SeedGraphServable    bool   `json:"seed_graph_servable"`
	ExampleGraphServable bool   `json:"example_graph_servable"`
	TargetNode           string `json:"target_node"`
	ResponseGraph        string `json:"response_graph"`
	Note                 string `json:"note"`
}

// Response graphs identify the source of the items presented for rating.
const (
	ResponseGraphSeed    = "seed"
	ResponseGraphExample = "example"
	ResponseGraphNone    = "none"
)

// ResponseOrigin captures fixture-level provider/model/defect labels excluded from blind packets.
type ResponseOrigin struct {
	Provider            string `json:"provider"`
	Model               string `json:"model"`
	Variant             string `json:"variant"`
	SeededCriticalError bool   `json:"seeded_critical_error"`
	SeededCriticalType  string `json:"seeded_critical_type"`
	DefectNote          string `json:"defect_note"`
}

// Provenance documents origin, redistribution permission, and live-sampling caps.
type Provenance struct {
	SourceType               string  `json:"source_type"`
	Author                   string  `json:"author"`
	CreatedDate              string  `json:"created_date"`
	RedistributionPermission string  `json:"redistribution_permission"`
	LiveCapture              bool    `json:"live_capture"`
	ModelVersionOrDate       string  `json:"model_version_or_date"`
	CostCapUSD               float64 `json:"cost_cap_usd"`
	CaseCap                  int     `json:"case_cap"`
}

// WorkedAnchor records expected calibration scores and rationale.
type WorkedAnchor struct {
	FactualAccuracy       RubricScore      `json:"factual_accuracy"`
	PrerequisiteRationale RubricScore      `json:"prerequisite_rationale"`
	ActionableGuidance    RubricScore      `json:"actionable_guidance"`
	ResourceSummaryStatus string           `json:"resource_summary_status"`
	ResourceRatings       []ResourceRating `json:"resource_ratings"`
	CriticalError         *bool            `json:"critical_error"`
	CriticalErrorType     string           `json:"critical_error_type"`
	Rationale             string           `json:"rationale"`
}

// RubricScore represents a single 0-2 ordinal rating, abstention, or N/A value.
type RubricScore struct {
	Status string `json:"status"`
	Score  *int   `json:"score,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ResourceRating scores one supplied learning resource on relevance.
type ResourceRating struct {
	ItemIndex     int         `json:"item_index"`
	ResourceIndex int         `json:"resource_index"`
	Rating        RubricScore `json:"rating"`
}

// ResourceEvidence provides the material available offline for a supplied slot.
type ResourceEvidence struct {
	ItemIndex         int    `json:"item_index"`
	ResourceIndex     int    `json:"resource_index"`
	Excerpt           string `json:"excerpt,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// DimensionRatings holds all rubric judgments for one blinded response.
type DimensionRatings struct {
	FactualAccuracy       RubricScore      `json:"factual_accuracy"`
	PrerequisiteRationale RubricScore      `json:"prerequisite_rationale"`
	ActionableGuidance    RubricScore      `json:"actionable_guidance"`
	ResourceSummaryStatus string           `json:"resource_summary_status"`
	ResourceRatings       []ResourceRating `json:"resource_ratings"`
	CriticalError         *bool            `json:"critical_error"`
	CriticalErrorType     string           `json:"critical_error_type"`
	Notes                 string           `json:"notes,omitempty"`
}

// RatingSheet is one rater's independently fillable blinded packet.
type RatingSheet struct {
	SchemaVersion        string      `json:"schema_version"`
	Course               string      `json:"course"`
	PacketSeed           uint64      `json:"packet_seed"`
	RaterSlot            string      `json:"rater_slot"`
	RaterQualification   string      `json:"rater_qualification"`
	QualificationAttest  bool        `json:"qualification_attested"`
	CalibrationCompleted bool        `json:"calibration_completed"`
	Locked               bool        `json:"locked"`
	DatasetKind          string      `json:"dataset_kind"`
	InstructionsNote     string      `json:"instructions_note"`
	Rows                 []RatingRow `json:"rows"`
}

// RatingRow represents one blinded response presented in seeded randomized order.
type RatingRow struct {
	ResponseID        string             `json:"response_id"`
	PresentationOrder int                `json:"presentation_order"`
	PromptContext     string             `json:"prompt_context"`
	VARKProfile       string             `json:"vark_profile"`
	FailedTopics      []string           `json:"failed_topics"`
	MasteredTopics    []string           `json:"mastered_topics,omitempty"`
	Items             []domain.PlanItem  `json:"items"`
	ResourceEvidence  []ResourceEvidence `json:"resource_evidence,omitempty"`
	ReferenceLocators []SourceReference  `json:"reference_locators"`
	Rating            *DimensionRatings  `json:"rating"`
}

// AdjudicationSheet preserves both initial locked ratings and records adjudicated resolutions.
type AdjudicationSheet struct {
	SchemaVersion    string            `json:"schema_version"`
	Course           string            `json:"course"`
	PacketSeed       uint64            `json:"packet_seed"`
	AdjudicatorRole  string            `json:"adjudicator_role"`
	AdjudicatorName  string            `json:"adjudicator_name"`
	DatasetKind      string            `json:"dataset_kind"`
	InstructionsNote string            `json:"instructions_note"`
	Rows             []AdjudicationRow `json:"rows"`
}

// AdjudicationRow links a blinded response ID to both initial ratings and final adjudication.
type AdjudicationRow struct {
	ResponseID        string            `json:"response_id"`
	PresentationOrder int               `json:"presentation_order"`
	RaterAInitial     *DimensionRatings `json:"rater_a_initial"`
	RaterBInitial     *DimensionRatings `json:"rater_b_initial"`
	Disagreement      bool              `json:"disagreement"`
	ResolutionReason  string            `json:"resolution_reason,omitempty"`
	Adjudicated       *DimensionRatings `json:"adjudicated"`
}

// CalibrationPacket holds the separate calibration cases and worked anchor examples for raters.
type CalibrationPacket struct {
	SchemaVersion string            `json:"schema_version"`
	Course        string            `json:"course"`
	Note          string            `json:"note"`
	Examples      []CalibrationItem `json:"examples"`
}

// CalibrationItem holds one blinded calibration example with its worked anchor rationale.
type CalibrationItem struct {
	CalibrationID     string             `json:"calibration_id"`
	Unit              int                `json:"unit"`
	TopicID           string             `json:"topic_id"`
	TopicTitle        string             `json:"topic_title"`
	PromptContext     string             `json:"prompt_context"`
	VARKProfile       string             `json:"vark_profile"`
	FailedTopics      []string           `json:"failed_topics"`
	MasteredTopics    []string           `json:"mastered_topics,omitempty"`
	Items             []domain.PlanItem  `json:"items"`
	ResourceEvidence  []ResourceEvidence `json:"resource_evidence,omitempty"`
	ReferenceLocators []SourceReference  `json:"reference_locators"`
	WorkedAnchor      WorkedAnchor       `json:"worked_anchor"`
}

// IdentityKey maps opaque response IDs back to cases, providers, and seeded labels outside packets.
type IdentityKey struct {
	SchemaVersion string          `json:"schema_version"`
	Course        string          `json:"course"`
	PacketSeed    uint64          `json:"packet_seed"`
	DatasetKind   string          `json:"dataset_kind"`
	Warning       string          `json:"warning"`
	Entries       []IdentityEntry `json:"entries"`
}

// IdentityEntry holds the unblinded mapping for one opaque response ID.
type IdentityEntry struct {
	ResponseID           string     `json:"response_id"`
	MaterialSHA256       string     `json:"material_sha256"`
	PresentationOrder    int        `json:"presentation_order"`
	CaseID               string     `json:"case_id"`
	Unit                 int        `json:"unit"`
	TopicID              string     `json:"topic_id"`
	TopicTitle           string     `json:"topic_title"`
	GraphTopic           string     `json:"graph_topic"`
	VARKProfile          string     `json:"vark_profile"`
	Provider             string     `json:"provider"`
	Model                string     `json:"model"`
	Variant              string     `json:"variant"`
	Provenance           Provenance `json:"provenance"`
	SeededCriticalError  bool       `json:"seeded_critical_error"`
	SeededCriticalType   string     `json:"seeded_critical_type"`
	ScenarioTags         []string   `json:"scenario_tags"`
	SeedGraphServable    bool       `json:"seed_graph_servable"`
	ExampleGraphServable bool       `json:"example_graph_servable"`
}

// SyntheticFixtureSet stores explicitly synthetic completed sheets for scorer regression tests.
type SyntheticFixtureSet struct {
	SchemaVersion        string                       `json:"schema_version"`
	SyntheticNotice      string                       `json:"synthetic_notice"`
	FailingSeededSheet   SyntheticCompletedSubmission `json:"failing_seeded_sheet"`
	PassingArithmeticSet SyntheticCompletedSubmission `json:"passing_arithmetic_set"`
}

// SyntheticCompletedSubmission bundles paired synthetic sheets and adjudication for testing.
type SyntheticCompletedSubmission struct {
	Description  string            `json:"description"`
	RaterA       RatingSheet       `json:"rater_a"`
	RaterB       RatingSheet       `json:"rater_b"`
	Adjudication AdjudicationSheet `json:"adjudication"`
}

// CandidateBlankReport records the explicit unmeasured status of an empty candidate evaluation.
type CandidateBlankReport struct {
	SchemaVersion     string      `json:"schema_version"`
	DatasetKind       string      `json:"dataset_kind"`
	MeasuredCandidate bool        `json:"measured_candidate"`
	Status            string      `json:"status"`
	Notice            string      `json:"notice"`
	ScoreReport       ScoreReport `json:"score_report"`
}

// ConfidenceInterval represents a 95% confidence interval.
type ConfidenceInterval struct {
	Defined bool    `json:"defined"`
	Lower   float64 `json:"lower"`
	Upper   float64 `json:"upper"`
	Method  string  `json:"method"`
}

// DimensionSummary reports score counts and denominators for one rubric dimension.
type DimensionSummary struct {
	TotalItems   int                 `json:"total_items"`
	Assessable   int                 `json:"assessable"`
	Abstentions  int                 `json:"abstentions"`
	NAItems      int                 `json:"na_items"`
	Missing      int                 `json:"missing"`
	Score0       int                 `json:"score_0"`
	Score1       int                 `json:"score_1"`
	Score2       int                 `json:"score_2"`
	PassFraction float64             `json:"pass_fraction"`
	PassCI95     *ConfidenceInterval `json:"pass_ci_95,omitempty"`
}

// KappaSummary reports linear-weighted Cohen's kappa and paired bootstrap CI.
type KappaSummary struct {
	PairedAssessable             int                `json:"paired_assessable"`
	Abstentions                  int                `json:"abstentions"`
	Missing                      int                `json:"missing"`
	Kappa                        float64            `json:"kappa"`
	Defined                      bool               `json:"defined"`
	BootstrapCI95                ConfidenceInterval `json:"bootstrap_ci_95"`
	BootstrapSamples             int                `json:"bootstrap_samples"`
	UndefinedBootstrapReplicates int                `json:"undefined_bootstrap_replicates"`

	// meetsThreshold is the exact, count-based decision for kappa >= MinGuidanceKappa.
	meetsThreshold bool
}

// StratumSummary reports exact counts, intervals, and inconclusive status for one stratum.
type StratumSummary struct {
	Key                       string             `json:"key"`
	TotalCases                int                `json:"total_cases"`
	CompletedCases            int                `json:"completed_cases"`
	MissingRatings            int                `json:"missing_ratings"`
	AssessableFactual         int                `json:"assessable_factual"`
	FactualAbstentions        int                `json:"factual_abstentions"`
	FactualPasses             int                `json:"factual_passes"`
	FactualPassFraction       float64            `json:"factual_pass_fraction"`
	FactualPassCI95           ConfidenceInterval `json:"factual_pass_ci_95"`
	AssessablePrerequisites   int                `json:"assessable_prerequisites"`
	PrerequisiteAbstentions   int                `json:"prerequisite_abstentions"`
	GuidanceAgreement         KappaSummary       `json:"guidance_agreement"`
	SuppliedResources         int                `json:"supplied_resources"`
	MissingResources          int                `json:"missing_resources"`
	AssessableResources       int                `json:"assessable_resources"`
	ResourceAbstentions       int                `json:"resource_abstentions"`
	NAItems                   int                `json:"na_items"`
	AdjudicatedCriticalErrors int                `json:"adjudicated_critical_errors"`
	Status                    string             `json:"status"`
	InconclusiveReasons       []string           `json:"inconclusive_reasons,omitempty"`
}

// GateEvaluation summarizes the candidate gate outcome against prespecified thresholds.
type GateEvaluation struct {
	Passed                  bool     `json:"passed"`
	Status                  string   `json:"status"`
	ZeroCriticalErrors      bool     `json:"zero_critical_errors"`
	FactualPassAtLeast90    bool     `json:"factual_pass_at_least_90"`
	GuidanceKappaAtLeast070 bool     `json:"guidance_kappa_at_least_070"`
	AllRatingsComplete      bool     `json:"all_ratings_complete"`
	MinimumAssessableMet    bool     `json:"minimum_assessable_met"`
	KappaDefined            bool     `json:"kappa_defined"`
	FailureReasons          []string `json:"failure_reasons,omitempty"`
	InconclusiveReasons     []string `json:"inconclusive_reasons,omitempty"`
}

// ScoreOptions configures scorer execution parameters.
type ScoreOptions struct {
	AllowIncomplete bool
	BootstrapSeed   uint64
	BootstrapRuns   int
}

// ScoreReport contains complete benchmark scoring outputs, denominators, and strata breakdowns.
type ScoreReport struct {
	SchemaVersion             string                      `json:"schema_version"`
	DatasetKind               string                      `json:"dataset_kind"`
	MeasuredCandidate         bool                        `json:"measured_candidate"`
	ProvenanceNote            string                      `json:"provenance_note"`
	MinStratumAssessable      int                         `json:"min_stratum_assessable"`
	TotalCases                int                         `json:"total_cases"`
	CompletedCases            int                         `json:"completed_cases"`
	MissingRatings            int                         `json:"missing_ratings"`
	RaterAInitialRatings      int                         `json:"rater_a_initial_ratings"`
	RaterBInitialRatings      int                         `json:"rater_b_initial_ratings"`
	Disagreements             int                         `json:"disagreements"`
	AdjudicatedCriticalErrors int                         `json:"adjudicated_critical_errors"`
	CriticalErrorCaseIDs      []string                    `json:"critical_error_case_ids,omitempty"`
	Dimensions                map[string]DimensionSummary `json:"dimensions"`
	GuidanceAgreement         KappaSummary                `json:"guidance_agreement"`
	ByUnit                    []StratumSummary            `json:"by_unit"`
	ByTopic                   []StratumSummary            `json:"by_topic"`
	ByPreference              []StratumSummary            `json:"by_preference"`
	ByProvider                []StratumSummary            `json:"by_provider"`
	Gate                      GateEvaluation              `json:"gate"`
}
