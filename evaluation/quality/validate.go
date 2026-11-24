package quality

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

// ErrInvalidSchema indicates malformed JSON or a schema constraint violation.
var ErrInvalidSchema = errors.New("invalid benchmark schema")

// ErrValidation indicates a semantic or rating-sheet validation failure.
var ErrValidation = errors.New("benchmark validation failed")

// DecodeStrictJSON decodes a single JSON value while rejecting unknown fields and trailing data.
func DecodeStrictJSON[T any](data []byte) (T, error) {
	var out T
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("%w: trailing JSON data", ErrInvalidSchema)
	}
	return out, nil
}

// ReadStrictJSONFile reads and strictly decodes a JSON file located under dir.
func ReadStrictJSONFile[T any](dir, relPath string) (T, error) {
	var zero T
	root, err := os.OpenRoot(filepath.Clean(dir))
	if err != nil {
		return zero, err
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(relPath)
	if err != nil {
		return zero, err
	}
	return DecodeStrictJSON[T](data)
}

// ValidateObjectiveCatalog checks the 73-objective Fall 2025 AP CS A catalog invariants.
func ValidateObjectiveCatalog(cat ObjectiveCatalog) error {
	if cat.SchemaVersion == "" || cat.Framework == "" || cat.FrameworkURL == "" || cat.ClarificationsURL == "" {
		return fmt.Errorf("%w: objective catalog metadata is incomplete", ErrValidation)
	}
	if cat.TotalObjectives != 73 || len(cat.Objectives) != 73 {
		return fmt.Errorf("%w: expected 73 objectives, got total=%d len=%d", ErrValidation, cat.TotalObjectives, len(cat.Objectives))
	}
	wantUnitCounts := map[int]int{1: 28, 2: 14, 3: 10, 4: 21}
	gotUnitCounts := make(map[int]int, 4)
	seen := make(map[string]bool, len(cat.Objectives))
	for _, obj := range cat.Objectives {
		if obj.ID == "" || seen[obj.ID] {
			return fmt.Errorf("%w: invalid or duplicate objective id %q", ErrValidation, obj.ID)
		}
		seen[obj.ID] = true
		if obj.Unit < 1 || obj.Unit > 4 || obj.TopicID == "" || obj.TopicTitle == "" || obj.Section == "" {
			return fmt.Errorf("%w: objective %q missing required fields", ErrValidation, obj.ID)
		}
		if obj.CEDPage <= 0 || obj.PDFPage <= 0 || !strings.HasPrefix(obj.URL, "https://") {
			return fmt.Errorf("%w: objective %q missing page locator or HTTPS URL", ErrValidation, obj.ID)
		}
		gotUnitCounts[obj.Unit]++
	}
	for unit, want := range wantUnitCounts {
		if gotUnitCounts[unit] != want {
			return fmt.Errorf("%w: unit %d has %d objectives, want %d", ErrValidation, unit, gotUnitCounts[unit], want)
		}
		key := fmt.Sprintf("%d", unit)
		if cat.UnitObjectiveCounts[key] != want {
			return fmt.Errorf("%w: unit_objective_counts[%q] = %d, want %d", ErrValidation, key, cat.UnitObjectiveCounts[key], want)
		}
	}
	return nil
}

// ValidateReferenceFacts checks provisional status, objective IDs, locators, and Java assertions.
func ValidateReferenceFacts(facts ReferenceFactCatalog, cat ObjectiveCatalog) (map[string]ReferenceFact, error) {
	if len(facts.Facts) == 0 || !strings.Contains(strings.ToUpper(facts.ProvisionalNotice), "PROVISIONAL") {
		return nil, fmt.Errorf("%w: reference fact catalog missing provisional notice or facts", ErrValidation)
	}
	objSet := make(map[string]bool, len(cat.Objectives))
	for _, obj := range cat.Objectives {
		objSet[obj.ID] = true
	}
	byID := make(map[string]ReferenceFact, len(facts.Facts))
	for _, fact := range facts.Facts {
		if fact.ID == "" {
			return nil, fmt.Errorf("%w: empty reference fact id", ErrValidation)
		}
		if _, dup := byID[fact.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate reference fact id %q", ErrValidation, fact.ID)
		}
		if fact.Status != ProvisionalFactStatus {
			return nil, fmt.Errorf("%w: fact %q status must be %q", ErrValidation, fact.ID, ProvisionalFactStatus)
		}
		if strings.TrimSpace(fact.Statement) == "" || len(fact.ObjectiveIDs) == 0 || len(fact.References) == 0 {
			return nil, fmt.Errorf("%w: fact %q missing statement, objectives, or references", ErrValidation, fact.ID)
		}
		for _, oid := range fact.ObjectiveIDs {
			if !objSet[oid] {
				return nil, fmt.Errorf("%w: fact %q references unknown objective %q", ErrValidation, fact.ID, oid)
			}
		}
		for _, ref := range fact.References {
			if ref.Document == "" || ref.Section == "" || ref.PageLocator == "" || !strings.HasPrefix(ref.URL, "https://") {
				return nil, fmt.Errorf("%w: fact %q has incomplete source reference", ErrValidation, fact.ID)
			}
		}
		if fact.JavaAssertion != nil {
			if err := ValidateJavaSnippet(fact.JavaAssertion.Snippet); err != nil {
				return nil, fmt.Errorf("%w: fact %q java_assertion: %w", ErrValidation, fact.ID, err)
			}
			if strings.TrimSpace(fact.JavaAssertion.ExpectedOutcome) == "" {
				return nil, fmt.Errorf("%w: fact %q missing expected_outcome", ErrValidation, fact.ID)
			}
		}
		byID[fact.ID] = fact
	}
	return byID, nil
}

// ValidateJavaSnippet performs pure-Go structural validation on Java code snippets.
// It checks non-empty content, closed string/character literals and comments,
// balanced delimiters, and statement structure without requiring javac.
func ValidateJavaSnippet(snippet string) error {
	trimmed := strings.TrimSpace(snippet)
	if trimmed == "" {
		return errors.New("empty Java snippet")
	}
	var stack []rune
	inLineComment := false
	inBlockComment := false
	var quote rune
	escaped := false
	hasCodeToken := false

	runes := []rune(trimmed)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		if inLineComment {
			if r == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if r == '*' && next == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '/' && next == '/' {
			inLineComment = true
			i++
			continue
		}
		if r == '/' && next == '*' {
			inBlockComment = true
			i++
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			hasCodeToken = true
			continue
		}
		if !unicode.IsSpace(r) {
			hasCodeToken = true
		}
		switch r {
		case '(', '[', '{':
			stack = append(stack, r)
		case ')', ']', '}':
			if len(stack) == 0 || !matchingDelimiter(stack[len(stack)-1], r) {
				return fmt.Errorf("unmatched closing delimiter %q", r)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if inBlockComment {
		return errors.New("unclosed block comment")
	}
	if quote != 0 {
		return fmt.Errorf("unclosed %c literal", quote)
	}
	if len(stack) > 0 {
		return fmt.Errorf("unclosed opening delimiter %q", stack[len(stack)-1])
	}
	if !hasCodeToken || (!strings.Contains(trimmed, ";") && !strings.Contains(trimmed, "{")) {
		return errors.New("snippet must contain at least one Java statement or block")
	}
	return nil
}

func matchingDelimiter(openDelim, closeDelim rune) bool {
	return (openDelim == '(' && closeDelim == ')') ||
		(openDelim == '[' && closeDelim == ']') ||
		(openDelim == '{' && closeDelim == '}')
}

// ValidateSuite verifies all benchmark suite cases, calibration examples, references,
// wire contracts, provenance metadata, and shipped-graph servability flags.
func ValidateSuite(suite BenchmarkSuite, cat ObjectiveCatalog, facts map[string]ReferenceFact, seedGraph, exampleGraph *graph.InMemoryGraph) error {
	if suite.SchemaVersion == "" || suite.Course == "" || suite.Seed == 0 {
		return fmt.Errorf("%w: incomplete suite header", ErrValidation)
	}
	if len(suite.CalibrationCases) < 4 {
		return fmt.Errorf("%w: expected at least 4 calibration cases, got %d", ErrValidation, len(suite.CalibrationCases))
	}
	objSet := make(map[string]Objective, len(cat.Objectives))
	for _, o := range cat.Objectives {
		objSet[o.ID] = o
	}

	roster := newCaseRoster()
	if err := roster.addSuiteRoster(suite); err != nil {
		return fmt.Errorf("%w: %w", ErrValidation, err)
	}
	for _, c := range suite.CalibrationCases {
		if err := validateCaseCore(c.ID, c.Unit, c.TopicID, c.ObjectiveIDs, c.ReferenceFactIDs, c.PromptContext, c.Request, c.CandidateResponse, c.ShippedGraph, c.Provenance, objSet, facts, seedGraph, exampleGraph); err != nil {
			return err
		}
		if strings.TrimSpace(c.WorkedAnchor.Rationale) == "" {
			return fmt.Errorf("%w: calibration case %q missing worked anchor rationale", ErrValidation, c.ID)
		}
		anchorRatings := DimensionRatings{
			FactualAccuracy:       c.WorkedAnchor.FactualAccuracy,
			PrerequisiteRationale: c.WorkedAnchor.PrerequisiteRationale,
			ActionableGuidance:    c.WorkedAnchor.ActionableGuidance,
			ResourceSummaryStatus: c.WorkedAnchor.ResourceSummaryStatus,
			ResourceRatings:       c.WorkedAnchor.ResourceRatings,
			CriticalError:         c.WorkedAnchor.CriticalError,
			CriticalErrorType:     c.WorkedAnchor.CriticalErrorType,
		}
		if _, _, err := roster.addCaseMaterial(c.ID, c.PromptContext, c.Request, c.CandidateResponse.Items, c.ResourceEvidence, c.ReferenceFactIDs, facts); err != nil {
			return fmt.Errorf("%w: calibration case %q: %w", ErrValidation, c.ID, err)
		}
		if err := validateDimensionRatings(anchorRatings, c.CandidateResponse.Items, c.ResourceEvidence); err != nil {
			return fmt.Errorf("%w: calibration case %q worked anchor: %w", ErrValidation, c.ID, err)
		}
	}

	varkSeen := make(map[string]bool, 4)
	tagSeen := make(map[string]bool)
	for _, c := range suite.EvaluationCases {
		if err := validateCaseCore(c.ID, c.Unit, c.TopicID, c.ObjectiveIDs, c.ReferenceFactIDs, c.PromptContext, c.Request, c.CandidateResponse, c.ShippedGraph, c.Provenance, objSet, facts, seedGraph, exampleGraph); err != nil {
			return err
		}
		if _, _, err := roster.addCaseMaterial(c.ID, c.PromptContext, c.Request, c.CandidateResponse.Items, c.ResourceEvidence, c.ReferenceFactIDs, facts); err != nil {
			return fmt.Errorf("%w: evaluation case %q: %w", ErrValidation, c.ID, err)
		}
		if err := validateCaseProvenance(suite.DatasetKind, c.Provenance, c.Origin.SeededCriticalError, c.Origin.SeededCriticalType); err != nil {
			return fmt.Errorf("%w: evaluation case %q: %w", ErrValidation, c.ID, err)
		}
		if err := validateResponseOrigin(c.Origin); err != nil {
			return fmt.Errorf("%w: evaluation case %q: %w", ErrValidation, c.ID, err)
		}
		varkSeen[c.Request.VARKProfile] = true
		for _, tag := range c.ScenarioTags {
			tagSeen[tag] = true
		}
	}
	for _, profile := range []string{"visual", "auditory", "read_write", "kinesthetic"} {
		if !varkSeen[profile] {
			return fmt.Errorf("%w: evaluation cases missing VARK profile %q", ErrValidation, profile)
		}
	}
	requiredTags := []string{
		"acceptable_anchor",
		"plausible_false_statement",
		"structurally_valid_wrong_answer",
		"reversed_prerequisite",
		"irrelevant_resource",
		"empty_resources",
		"missing_evidence",
		"ambiguous_prompt",
		"service_limits",
	}
	for _, tag := range requiredTags {
		if suite.DatasetKind == DatasetKindSyntheticRegression && !tagSeen[tag] {
			return fmt.Errorf("%w: evaluation cases missing required scenario tag %q", ErrValidation, tag)
		}
	}
	return nil
}

func validateCaseCore(
	id string,
	unit int,
	topicID string,
	objectiveIDs, factIDs []string,
	promptCtx string,
	req domain.AdaptivePlanRequest,
	resp domain.PlanResponse,
	ship ShippedGraphStatus,
	prov Provenance,
	objSet map[string]Objective,
	facts map[string]ReferenceFact,
	seedGraph, exampleGraph *graph.InMemoryGraph,
) error {
	if unit < 1 || unit > 4 || topicID == "" || strings.TrimSpace(promptCtx) == "" {
		return fmt.Errorf("%w: case %q invalid unit, topic_id, or prompt_context", ErrValidation, id)
	}
	if len(objectiveIDs) == 0 || len(factIDs) == 0 {
		return fmt.Errorf("%w: case %q missing objective_ids or reference_fact_ids", ErrValidation, id)
	}
	for _, oid := range objectiveIDs {
		obj, ok := objSet[oid]
		if !ok {
			return fmt.Errorf("%w: case %q references unknown objective %q", ErrValidation, id, oid)
		}
		if obj.Unit != unit {
			return fmt.Errorf("%w: case %q objective %q belongs to unit %d, not unit %d", ErrValidation, id, oid, obj.Unit, unit)
		}
	}
	for _, fid := range factIDs {
		if _, ok := facts[fid]; !ok {
			return fmt.Errorf("%w: case %q references unknown fact %q", ErrValidation, id, fid)
		}
	}
	if err := validateWireCompatibility(req, resp); err != nil {
		return fmt.Errorf("%w: case %q wire schema: %w", ErrValidation, id, err)
	}
	if err := validateProvenance(prov); err != nil {
		return fmt.Errorf("%w: case %q provenance: %w", ErrValidation, id, err)
	}
	if ship.TargetNode == "" || strings.TrimSpace(ship.Note) == "" {
		return fmt.Errorf("%w: case %q missing shipped_graph metadata", ErrValidation, id)
	}
	wantGraph := ResponseGraphNone
	switch {
	case ship.ExampleGraphServable:
		wantGraph = ResponseGraphExample
	case ship.SeedGraphServable:
		wantGraph = ResponseGraphSeed
	}
	if ship.ResponseGraph != wantGraph {
		return fmt.Errorf("%w: case %q response_graph=%q, want %q", ErrValidation, id, ship.ResponseGraph, wantGraph)
	}
	if seedGraph != nil {
		wantSeed := GraphCanServe(seedGraph, req)
		if ship.SeedGraphServable != wantSeed {
			return fmt.Errorf("%w: case %q seed_graph_servable=%v, actual=%v", ErrValidation, id, ship.SeedGraphServable, wantSeed)
		}
	}
	if exampleGraph != nil {
		wantExample := GraphCanServe(exampleGraph, req)
		if ship.ExampleGraphServable != wantExample {
			return fmt.Errorf("%w: case %q example_graph_servable=%v, actual=%v", ErrValidation, id, ship.ExampleGraphServable, wantExample)
		}
	}
	return nil
}

// GraphCanServe reports whether a shipped knowledge graph contains all requested and mastered topics
// without contradictory overlap.
func GraphCanServe(g *graph.InMemoryGraph, req domain.AdaptivePlanRequest) bool {
	if g == nil || len(req.FailedTopics) == 0 {
		return false
	}
	if _, err := service.ValidateTopics("failed_topics", req.FailedTopics, 10, g); err != nil {
		return false
	}
	if _, err := service.ValidateMasteredTopics("mastered_topics", req.MasteredTopics, req.FailedTopics, 100, g); err != nil {
		return false
	}
	return true
}

func validateWireCompatibility(req domain.AdaptivePlanRequest, resp domain.PlanResponse) error {
	if len(req.FailedTopics) == 0 || len(req.FailedTopics) > 10 {
		return errors.New("failed_topics must have 1..10 entries")
	}
	if req.StudentID != "" && !service.ValidIdentifier(req.StudentID) {
		return fmt.Errorf("invalid student_id %q", req.StudentID)
	}
	if req.CourseID != "" && !service.ValidIdentifier(req.CourseID) {
		return fmt.Errorf("invalid course_id %q", req.CourseID)
	}
	if !validVARK(req.VARKProfile) {
		return fmt.Errorf("invalid vark_profile %q", req.VARKProfile)
	}
	if len(resp.Items) == 0 {
		return errors.New("candidate_response must contain at least one item")
	}
	if resp.StudentID != req.StudentID || resp.CourseID != req.CourseID {
		return errors.New("candidate_response student_id/course_id must match request")
	}
	for i, item := range resp.Items {
		if strings.TrimSpace(item.Topic) == "" || item.Priority < 1 {
			return fmt.Errorf("item[%d] missing topic or priority", i)
		}
		if item.PrerequisiteChain == nil {
			return fmt.Errorf("item[%d] prerequisite_chain must be non-nil array", i)
		}
		if utf8.RuneCountInString(item.Explanation) > 1000 || strings.TrimSpace(item.Explanation) == "" {
			return fmt.Errorf("item[%d] explanation length %d outside 1..1000 runes", i, utf8.RuneCountInString(item.Explanation))
		}
		if item.Resources == nil || len(item.Resources) > 3 {
			return fmt.Errorf("item[%d] resources must be non-nil array of 0..3 elements", i)
		}
		for j, res := range item.Resources {
			if utf8.RuneCountInString(res.Title) > 200 || strings.TrimSpace(res.Title) == "" {
				return fmt.Errorf("item[%d].resources[%d] title invalid", i, j)
			}
			if !validResourceURL(res.URL) {
				return fmt.Errorf("item[%d].resources[%d] invalid URL %q", i, j, res.URL)
			}
			if !allowedVARKResourceType(req.VARKProfile, res.ResourceType) {
				return fmt.Errorf("item[%d].resources[%d] resource_type %q incompatible with %q", i, j, res.ResourceType, req.VARKProfile)
			}
		}
	}
	return nil
}

func validVARK(profile string) bool {
	switch profile {
	case "visual", "auditory", "read_write", "kinesthetic":
		return true
	default:
		return false
	}
}

func allowedVARKResourceType(profile, kind string) bool {
	switch profile {
	case "visual":
		return kind == "video" || kind == "diagram" || kind == "infographic"
	case "auditory":
		return kind == "podcast" || kind == "lecture" || kind == "audio-course"
	case "read_write":
		return kind == "article" || kind == "textbook" || kind == "documentation"
	case "kinesthetic":
		return kind == "exercise" || kind == "lab" || kind == "coding-challenge"
	default:
		return false
	}
}

func validResourceURL(raw string) bool {
	if utf8.RuneCountInString(raw) > 2048 || strings.TrimSpace(raw) != raw {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.User == nil && u.Hostname() != ""
}

func validateResponseOrigin(origin ResponseOrigin) error {
	if strings.TrimSpace(origin.Provider) == "" || strings.TrimSpace(origin.Model) == "" || strings.TrimSpace(origin.Variant) == "" {
		return errors.New("provider, model, and variant must be non-empty")
	}
	return nil
}

func validateCaseProvenance(kind string, p Provenance, critical bool, criticalType string) error {
	if err := validateProvenance(p); err != nil {
		return err
	}
	switch kind {
	case DatasetKindSyntheticRegression:
		if p.SourceType != SourceSyntheticAuthored || p.LiveCapture {
			return errors.New("synthetic packets require original synthetic provenance")
		}
	case DatasetKindMeasuredCandidate:
		if p.SourceType != SourceUnseenCandidate || strings.TrimSpace(p.ModelVersionOrDate) == "" {
			return errors.New("candidate packets require an explicit unseen candidate source and model version or date")
		}
		if critical || criticalType != CriticalTypeNone {
			return errors.New("candidate packets cannot contain seeded critical labels")
		}
	default:
		return fmt.Errorf("invalid packet dataset_kind %q", kind)
	}
	return validateCriticalDecision(&critical, criticalType)
}

func validateProvenance(p Provenance) error {
	if strings.TrimSpace(p.SourceType) == "" || strings.TrimSpace(p.Author) == "" || strings.TrimSpace(p.CreatedDate) == "" || strings.TrimSpace(p.RedistributionPermission) == "" {
		return errors.New("provenance missing source_type, author, created_date, or redistribution_permission")
	}
	if p.LiveCapture {
		if p.CaseCap <= 0 || p.CostCapUSD <= 0 || strings.TrimSpace(p.ModelVersionOrDate) == "" {
			return errors.New("live_capture outputs require positive case_cap, cost_cap_usd, and model_version_or_date")
		}
	}
	return nil
}

// ValidateSubmission validates paired rating sheets and an adjudication sheet against the identity key.
// When allowIncomplete is false, missing ratings, unlocked sheets, or missing adjudications fail.
// Duplicate rows, unknown response IDs, and illegal rubric values fail regardless of allowIncomplete.
func ValidateSubmission(key IdentityKey, raterA, raterB RatingSheet, adj AdjudicationSheet, allowIncomplete bool) error {
	entries, err := validateKey(key)
	if err != nil {
		return err
	}
	kind := raterA.DatasetKind
	if (kind != key.DatasetKind && kind != DatasetKindUnmeasuredBlank) || kind != raterB.DatasetKind || kind != adj.DatasetKind {
		return fmt.Errorf("%w: dataset_kind must match across both rating sheets and adjudication and agree with the originating packet", ErrValidation)
	}
	rowsA, err := validateRatingSheet(raterA, "rater_a", key, entries, allowIncomplete)
	if err != nil {
		return err
	}
	rowsB, err := validateRatingSheet(raterB, "rater_b", key, entries, allowIncomplete)
	if err != nil {
		return err
	}
	return validateAdjudicationSheet(adj, key, entries, rowsA, rowsB, raterReady(raterA) && raterReady(raterB), allowIncomplete)
}

// caseRoster records the identifiers and presented material of a benchmark and rejects empty or
// repeated values, so a few responses cannot stand in for the full designated roster.
type caseRoster struct {
	ids       map[string]bool
	materials map[string]string
}

func newCaseRoster() *caseRoster {
	return &caseRoster{ids: make(map[string]bool), materials: make(map[string]string)}
}

func (r *caseRoster) addID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("empty case id")
	}
	if r.ids[id] {
		return fmt.Errorf("repeated case id %q", id)
	}
	r.ids[id] = true
	return nil
}

// addMaterial rejects a case whose digest matches an earlier case under another identifier.
func (r *caseRoster) addMaterial(id, digest string) error {
	if first, dup := r.materials[digest]; dup {
		return fmt.Errorf("case %q repeats the material of case %q", id, first)
	}
	r.materials[digest] = id
	return nil
}

// addSuiteRoster registers calibration and designated IDs and checks designated course coverage.
func (r *caseRoster) addSuiteRoster(suite BenchmarkSuite) error {
	for _, c := range suite.CalibrationCases {
		if err := r.addID(c.ID); err != nil {
			return fmt.Errorf("calibration case: %w", err)
		}
	}
	for _, c := range suite.EvaluationCases {
		if err := r.addID(c.ID); err != nil {
			return fmt.Errorf("evaluation case: %w", err)
		}
	}
	return validateCourseCoverage(suite.EvaluationCases, func(c EvaluationCase) int { return c.Unit })
}

func validateCourseCoverage[T any](cases []T, unitOf func(T) int) error {
	const numUnits, minPerUnit = 4, 6
	const minCases = numUnits * minPerUnit
	if len(cases) < minCases {
		return fmt.Errorf("expected at least %d evaluation cases, got %d", minCases, len(cases))
	}
	var unitCounts [numUnits]int
	for i, c := range cases {
		unit := unitOf(c)
		if unit < 1 || unit > numUnits {
			return fmt.Errorf("evaluation case at position %d has invalid unit %d", i+1, unit)
		}
		unitCounts[unit-1]++
	}
	for i, count := range unitCounts {
		if count < minPerUnit {
			return fmt.Errorf("unit %d has %d evaluation cases, require at least %d", i+1, count, minPerUnit)
		}
	}
	return nil
}

func validateKey(key IdentityKey) (map[string]IdentityEntry, error) {
	if key.SchemaVersion == "" || key.Course == "" || key.PacketSeed == 0 || len(key.Entries) == 0 {
		return nil, fmt.Errorf("%w: incomplete identity key", ErrValidation)
	}
	entries := make(map[string]IdentityEntry, len(key.Entries))
	roster := newCaseRoster()
	for i, entry := range key.Entries {
		if entry.ResponseID == "" || entry.MaterialSHA256 == "" || entry.PresentationOrder != i+1 {
			return nil, fmt.Errorf("%w: invalid identity entry at position %d", ErrValidation, i+1)
		}
		if err := errors.Join(roster.addID(entry.CaseID), roster.addMaterial(entry.CaseID, entry.MaterialSHA256)); err != nil {
			return nil, fmt.Errorf("%w: identity entry at position %d: %w", ErrValidation, i+1, err)
		}
		if _, dup := entries[entry.ResponseID]; dup {
			return nil, fmt.Errorf("%w: duplicate response_id %q in key", ErrValidation, entry.ResponseID)
		}
		if err := validateCaseProvenance(key.DatasetKind, entry.Provenance, entry.SeededCriticalError, entry.SeededCriticalType); err != nil {
			return nil, fmt.Errorf("%w: identity entry %q: %w", ErrValidation, entry.ResponseID, err)
		}
		entries[entry.ResponseID] = entry
	}
	if key.DatasetKind == DatasetKindMeasuredCandidate {
		if err := validateCourseCoverage(key.Entries, func(entry IdentityEntry) int { return entry.Unit }); err != nil {
			return nil, fmt.Errorf("%w: identity key: %w", ErrValidation, err)
		}
	}
	return entries, nil
}

func raterReady(sheet RatingSheet) bool {
	return sheet.Locked && sheet.QualificationAttest && sheet.CalibrationCompleted && strings.TrimSpace(sheet.RaterQualification) != ""
}

func validateRatingSheet(sheet RatingSheet, expectedSlot string, key IdentityKey, entries map[string]IdentityEntry, allowIncomplete bool) (map[string]RatingRow, error) {
	if sheet.RaterSlot != expectedSlot {
		return nil, fmt.Errorf("%w: rating sheet slot=%q, want %q", ErrValidation, sheet.RaterSlot, expectedSlot)
	}
	if sheet.PacketSeed != key.PacketSeed || sheet.SchemaVersion != key.SchemaVersion || sheet.Course != key.Course {
		return nil, fmt.Errorf("%w: rating sheet %s header does not match identity key", ErrValidation, expectedSlot)
	}
	if !allowIncomplete && !raterReady(sheet) {
		return nil, fmt.Errorf("%w: rating sheet %s must attest qualification, calibration, and locked status", ErrValidation, expectedSlot)
	}
	seen := make(map[string]RatingRow, len(sheet.Rows))
	for i, row := range sheet.Rows {
		entry, ok := entries[row.ResponseID]
		if !ok {
			return nil, fmt.Errorf("%w: rating sheet %s contains unknown response_id %q", ErrValidation, expectedSlot, row.ResponseID)
		}
		if _, dup := seen[row.ResponseID]; dup {
			return nil, fmt.Errorf("%w: rating sheet %s contains duplicate response_id %q", ErrValidation, expectedSlot, row.ResponseID)
		}
		if row.PresentationOrder != entry.PresentationOrder || row.PresentationOrder != i+1 {
			return nil, fmt.Errorf("%w: rating sheet %s row %q is out of order", ErrValidation, expectedSlot, row.ResponseID)
		}
		digest, err := materialDigest(row)
		if err != nil || digest != entry.MaterialSHA256 || row.VARKProfile != entry.VARKProfile {
			return nil, fmt.Errorf("%w: rating sheet %s row %q material differs from originating packet", ErrValidation, expectedSlot, row.ResponseID)
		}
		switch {
		case row.Rating == nil:
			if !allowIncomplete {
				return nil, fmt.Errorf("%w: rating sheet %s missing rating for %q", ErrValidation, expectedSlot, row.ResponseID)
			}
		case sheet.DatasetKind == DatasetKindUnmeasuredBlank:
			return nil, fmt.Errorf("%w: blank rating sheet %s contains a rating for %q", ErrValidation, expectedSlot, row.ResponseID)
		default:
			if err := validateDimensionRatings(*row.Rating, row.Items, row.ResourceEvidence); err != nil {
				return nil, fmt.Errorf("%w: rating sheet %s row %q: %w", ErrValidation, expectedSlot, row.ResponseID, err)
			}
		}
		seen[row.ResponseID] = row
	}
	if len(seen) != len(entries) {
		return nil, fmt.Errorf("%w: rating sheet %s has %d rows, expected %d", ErrValidation, expectedSlot, len(seen), len(entries))
	}
	return seen, nil
}

func validateAdjudicationSheet(
	adj AdjudicationSheet,
	key IdentityKey,
	entries map[string]IdentityEntry,
	rowsA, rowsB map[string]RatingRow,
	ratersReady, allowIncomplete bool,
) error {
	if adj.PacketSeed != key.PacketSeed || adj.SchemaVersion != key.SchemaVersion || adj.Course != key.Course {
		return fmt.Errorf("%w: adjudication sheet header does not match identity key", ErrValidation)
	}
	if adj.AdjudicatorRole != NamedAdjudicatorRole {
		return fmt.Errorf("%w: adjudicator_role=%q, want %q", ErrValidation, adj.AdjudicatorRole, NamedAdjudicatorRole)
	}
	named := strings.TrimSpace(adj.AdjudicatorName) != ""
	seen := make(map[string]bool, len(adj.Rows))
	for i, row := range adj.Rows {
		entry, ok := entries[row.ResponseID]
		if !ok {
			return fmt.Errorf("%w: adjudication sheet contains unknown response_id %q", ErrValidation, row.ResponseID)
		}
		if seen[row.ResponseID] {
			return fmt.Errorf("%w: adjudication sheet contains duplicate response_id %q", ErrValidation, row.ResponseID)
		}
		seen[row.ResponseID] = true
		if row.PresentationOrder != entry.PresentationOrder || row.PresentationOrder != i+1 {
			return fmt.Errorf("%w: adjudication row %q is out of order", ErrValidation, row.ResponseID)
		}
		if err := validateAdjudicationRow(row, rowsA[row.ResponseID], rowsB[row.ResponseID], ratersReady, named, allowIncomplete); err != nil {
			return fmt.Errorf("%w: adjudication row %q: %w", ErrValidation, row.ResponseID, err)
		}
	}
	if len(seen) != len(entries) {
		return fmt.Errorf("%w: adjudication sheet has %d rows, expected %d", ErrValidation, len(seen), len(entries))
	}
	return nil
}

func validateAdjudicationRow(row AdjudicationRow, a, b RatingRow, ratersReady, named, allowIncomplete bool) error {
	if !ratersReady && (row.RaterAInitial != nil || row.RaterBInitial != nil || row.Adjudicated != nil) {
		return errors.New("adjudication requires both qualified, calibrated and locked initial sheets")
	}
	if row.RaterAInitial != nil && !reflect.DeepEqual(row.RaterAInitial, a.Rating) {
		return errors.New("rater_a_initial does not match locked sheet A")
	}
	if row.RaterBInitial != nil && !reflect.DeepEqual(row.RaterBInitial, b.Rating) {
		return errors.New("rater_b_initial does not match locked sheet B")
	}
	if row.Adjudicated == nil {
		if !allowIncomplete {
			return errors.New("missing adjudicated rating")
		}
		return nil
	}
	if !named {
		return errors.New("missing adjudicator_name")
	}
	if row.RaterAInitial == nil || row.RaterBInitial == nil {
		return errors.New("must preserve both initial ratings before adjudication")
	}
	if err := validateDimensionRatings(*row.Adjudicated, a.Items, a.ResourceEvidence); err != nil {
		return fmt.Errorf("adjudicated: %w", err)
	}
	wantDisagree := RatingsDisagree(*row.RaterAInitial, *row.RaterBInitial)
	if row.Disagreement != wantDisagree {
		return fmt.Errorf("disagreement=%v, want %v", row.Disagreement, wantDisagree)
	}
	if (wantDisagree || RatingsDisagree(*row.RaterAInitial, *row.Adjudicated)) && strings.TrimSpace(row.ResolutionReason) == "" {
		return errors.New("resolution_reason is required for a disagreement or an override of agreement")
	}
	return nil
}

func resourceSlots(items []domain.PlanItem) map[[2]int]bool {
	slots := make(map[[2]int]bool)
	for itemIdx, item := range items {
		for resIdx := range item.Resources {
			slots[[2]int{itemIdx, resIdx}] = true
		}
	}
	return slots
}

func validateResourceEvidence(items []domain.PlanItem, evidence []ResourceEvidence) error {
	slots := resourceSlots(items)
	if len(evidence) != len(slots) {
		return fmt.Errorf("got %d resource evidence records, want %d", len(evidence), len(slots))
	}
	for _, record := range evidence {
		slot := [2]int{record.ItemIndex, record.ResourceIndex}
		if !slots[slot] {
			return fmt.Errorf("invalid or duplicate resource evidence slot %v", slot)
		}
		delete(slots, slot)
		if (strings.TrimSpace(record.Excerpt) == "") == (strings.TrimSpace(record.UnavailableReason) == "") {
			return fmt.Errorf("resource evidence slot %v requires an excerpt or a reason evidence is unavailable", slot)
		}
	}
	return nil
}

func validateDimensionRatings(r DimensionRatings, items []domain.PlanItem, evidence []ResourceEvidence) error {
	for _, pair := range []struct {
		name  string
		score RubricScore
	}{
		{"factual_accuracy", r.FactualAccuracy},
		{"prerequisite_rationale", r.PrerequisiteRationale},
		{"actionable_guidance", r.ActionableGuidance},
	} {
		if err := validateRubricScore(pair.score, false); err != nil {
			return fmt.Errorf("%s: %w", pair.name, err)
		}
	}

	validSlots := resourceSlots(items)
	unavailable := make(map[[2]int]bool)
	for _, record := range evidence {
		if strings.TrimSpace(record.UnavailableReason) != "" {
			unavailable[[2]int{record.ItemIndex, record.ResourceIndex}] = true
		}
	}
	totalResources := len(validSlots)
	if totalResources == 0 {
		if r.ResourceSummaryStatus != ResourceSummaryNA {
			return fmt.Errorf("absent resources require resource_summary_status=%q, got %q", ResourceSummaryNA, r.ResourceSummaryStatus)
		}
		if len(r.ResourceRatings) != 0 {
			return fmt.Errorf("absent resources require 0 resource_ratings, got %d", len(r.ResourceRatings))
		}
	} else {
		if r.ResourceSummaryStatus != ResourceSummaryApplicable {
			return fmt.Errorf("supplied resources require resource_summary_status=%q, got %q", ResourceSummaryApplicable, r.ResourceSummaryStatus)
		}
		if len(r.ResourceRatings) != totalResources {
			return fmt.Errorf("got %d resource_ratings, want %d", len(r.ResourceRatings), totalResources)
		}
		seenSlots := make(map[[2]int]bool, totalResources)
		for _, rr := range r.ResourceRatings {
			slot := [2]int{rr.ItemIndex, rr.ResourceIndex}
			if !validSlots[slot] || seenSlots[slot] {
				return fmt.Errorf("invalid or duplicate resource slot (%d, %d)", rr.ItemIndex, rr.ResourceIndex)
			}
			seenSlots[slot] = true
			if unavailable[slot] && rr.Rating.Status != ScoreStatusAbstain {
				return fmt.Errorf("resource %v requires abstention because its offline evidence is unavailable", slot)
			}
			if err := validateRubricScore(rr.Rating, false); err != nil {
				return fmt.Errorf("resource (%d, %d): %w", rr.ItemIndex, rr.ResourceIndex, err)
			}
		}
	}

	return validateCriticalDecision(r.CriticalError, r.CriticalErrorType)
}

func validateCriticalDecision(decision *bool, kind string) error {
	if decision == nil {
		return errors.New("critical_error requires an explicit true or false")
	}
	if *decision {
		if kind != CriticalTypeFactual && kind != CriticalTypePrerequisite && kind != CriticalTypeBoth {
			return fmt.Errorf("critical_error=true requires critical_error_type factual, prerequisite, or both; got %q", kind)
		}
	} else if kind != CriticalTypeNone {
		return fmt.Errorf("critical_error=false requires critical_error_type none, got %q", kind)
	}
	return nil
}

func validateRubricScore(s RubricScore, allowNA bool) error {
	switch s.Status {
	case ScoreStatusScored:
		if s.Score == nil || *s.Score < 0 || *s.Score > 2 {
			return errors.New("scored rating requires integer score in 0..2")
		}
		if strings.TrimSpace(s.Reason) != "" {
			return errors.New("scored rating must not set abstention reason")
		}
	case ScoreStatusAbstain:
		if s.Score != nil {
			return errors.New("abstained rating must not carry numeric score")
		}
		if strings.TrimSpace(s.Reason) == "" {
			return errors.New("abstained rating requires non-empty reason")
		}
	case ScoreStatusNA:
		if !allowNA {
			return errors.New("status na is not permitted on this field")
		}
		if s.Score != nil || strings.TrimSpace(s.Reason) != "" {
			return errors.New("status na must not carry score or reason")
		}
	default:
		return fmt.Errorf("illegal rating status %q", s.Status)
	}
	return nil
}

// RatingsDisagree compares scores, statuses and critical decisions independently of notes or reasons.
func RatingsDisagree(a, b DimensionRatings) bool {
	if !rubricScoreEqual(a.FactualAccuracy, b.FactualAccuracy) ||
		!rubricScoreEqual(a.PrerequisiteRationale, b.PrerequisiteRationale) ||
		!rubricScoreEqual(a.ActionableGuidance, b.ActionableGuidance) ||
		a.ResourceSummaryStatus != b.ResourceSummaryStatus ||
		!reflect.DeepEqual(a.CriticalError, b.CriticalError) ||
		a.CriticalErrorType != b.CriticalErrorType {
		return true
	}
	if len(a.ResourceRatings) != len(b.ResourceRatings) {
		return true
	}
	aCopy := slices.Clone(a.ResourceRatings)
	bCopy := slices.Clone(b.ResourceRatings)
	cmpResource := func(x, y ResourceRating) int {
		if x.ItemIndex != y.ItemIndex {
			return x.ItemIndex - y.ItemIndex
		}
		return x.ResourceIndex - y.ResourceIndex
	}
	slices.SortFunc(aCopy, cmpResource)
	slices.SortFunc(bCopy, cmpResource)
	for i := range aCopy {
		if aCopy[i].ItemIndex != bCopy[i].ItemIndex ||
			aCopy[i].ResourceIndex != bCopy[i].ResourceIndex ||
			!rubricScoreEqual(aCopy[i].Rating, bCopy[i].Rating) {
			return true
		}
	}
	return false
}

func rubricScoreEqual(a, b RubricScore) bool {
	if a.Status != b.Status {
		return false
	}
	if a.Score == nil || b.Score == nil {
		return a.Score == b.Score
	}
	return *a.Score == *b.Score
}
