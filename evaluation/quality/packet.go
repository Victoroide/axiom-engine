package quality

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

// BlindArtifacts bundles the reproducibly generated blind packets and separate identity key.
type BlindArtifacts struct {
	Calibration  CalibrationPacket
	RaterABlank  RatingSheet
	RaterBBlank  RatingSheet
	Adjudication AdjudicationSheet
	IdentityKey  IdentityKey
}

// BuildBlindArtifacts generates two blinded blank rating sheets with opaque response IDs,
// a seeded randomized presentation order, a blank adjudication sheet, a separate calibration
// packet, and a separate identity key keeping provider, model, variant, and seeded labels out
// of the rating packets.
func BuildBlindArtifacts(suite BenchmarkSuite, facts map[string]ReferenceFact) (BlindArtifacts, error) {
	if suite.SchemaVersion == "" || suite.Course == "" || suite.Seed == 0 {
		return BlindArtifacts{}, fmt.Errorf("%w: incomplete packet header", ErrValidation)
	}
	if suite.DatasetKind != DatasetKindSyntheticRegression && suite.DatasetKind != DatasetKindMeasuredCandidate {
		return BlindArtifacts{}, fmt.Errorf("%w: invalid packet dataset_kind %q", ErrValidation, suite.DatasetKind)
	}
	roster := newCaseRoster()
	if err := roster.addSuiteRoster(suite); err != nil {
		return BlindArtifacts{}, fmt.Errorf("%w: %w", ErrValidation, err)
	}
	n := len(suite.EvaluationCases)
	perm := make([]int, n)
	for i := range n {
		perm[i] = i
	}
	rng := newDeterministicRNG(suite.Seed)
	for i := n - 1; i > 0; i-- {
		j := rng.intN(i + 1)
		perm[i], perm[j] = perm[j], perm[i]
	}

	usedIDs := make(map[string]bool, n)
	rowsA := make([]RatingRow, n)
	rowsB := make([]RatingRow, n)
	adjRows := make([]AdjudicationRow, n)
	keyEntries := make([]IdentityEntry, n)

	for orderIdx, caseIdx := range perm {
		c := suite.EvaluationCases[caseIdx]
		if err := validateResponseOrigin(c.Origin); err != nil {
			return BlindArtifacts{}, fmt.Errorf("%w: case %q: %w", ErrValidation, c.ID, err)
		}
		if err := validateCaseProvenance(suite.DatasetKind, c.Provenance, c.Origin.SeededCriticalError, c.Origin.SeededCriticalType); err != nil {
			return BlindArtifacts{}, fmt.Errorf("%w: case %q: %w", ErrValidation, c.ID, err)
		}
		row, digest, err := roster.addCaseMaterial(c.ID, c.PromptContext, c.Request, c.CandidateResponse.Items, c.ResourceEvidence, c.ReferenceFactIDs, facts)
		if err != nil {
			return BlindArtifacts{}, fmt.Errorf("%w: case %q: %w", ErrValidation, c.ID, err)
		}
		order := orderIdx + 1
		respID := opaqueResponseID(suite.Seed, orderIdx, digest, usedIDs)
		usedIDs[respID] = true
		row.PresentationOrder = order
		row.ResponseID = respID
		rowsA[orderIdx] = row
		row.FailedTopics = slices.Clone(row.FailedTopics)
		row.MasteredTopics = slices.Clone(row.MasteredTopics)
		row.Items = clonePlanItems(row.Items)
		row.ResourceEvidence = slices.Clone(row.ResourceEvidence)
		row.ReferenceLocators = slices.Clone(row.ReferenceLocators)
		rowsB[orderIdx] = row
		adjRows[orderIdx] = AdjudicationRow{
			ResponseID:        respID,
			PresentationOrder: order,
		}
		keyEntries[orderIdx] = IdentityEntry{
			MaterialSHA256:       digest,
			ResponseID:           respID,
			PresentationOrder:    order,
			CaseID:               c.ID,
			Unit:                 c.Unit,
			TopicID:              c.TopicID,
			TopicTitle:           c.TopicTitle,
			GraphTopic:           c.ShippedGraph.TargetNode,
			VARKProfile:          c.Request.VARKProfile,
			Provider:             c.Origin.Provider,
			Model:                c.Origin.Model,
			Variant:              c.Origin.Variant,
			Provenance:           c.Provenance,
			SeededCriticalError:  c.Origin.SeededCriticalError,
			SeededCriticalType:   c.Origin.SeededCriticalType,
			ScenarioTags:         slices.Clone(c.ScenarioTags),
			SeedGraphServable:    c.ShippedGraph.SeedGraphServable,
			ExampleGraphServable: c.ShippedGraph.ExampleGraphServable,
		}
	}

	calExamples := make([]CalibrationItem, len(suite.CalibrationCases))
	for i, c := range suite.CalibrationCases {
		row, _, err := roster.addCaseMaterial(c.ID, c.PromptContext, c.Request, c.CandidateResponse.Items, c.ResourceEvidence, c.ReferenceFactIDs, facts)
		if err != nil {
			return BlindArtifacts{}, fmt.Errorf("%w: calibration %q: %w", ErrValidation, c.ID, err)
		}
		calExamples[i] = CalibrationItem{
			ResourceEvidence:  row.ResourceEvidence,
			ReferenceLocators: row.ReferenceLocators,
			CalibrationID:     c.ID,
			Unit:              c.Unit,
			TopicID:           c.TopicID,
			TopicTitle:        c.TopicTitle,
			PromptContext:     row.PromptContext,
			VARKProfile:       row.VARKProfile,
			FailedTopics:      row.FailedTopics,
			MasteredTopics:    row.MasteredTopics,
			Items:             row.Items,
			WorkedAnchor:      c.WorkedAnchor,
		}
	}

	instructions := "Use the packet rubric and finish calibration first. Rate independently, then lock the sheet. Use the supplied offline resource evidence and provisional course references. Record a reason for every abstention and an explicit true or false for every critical judgment."

	artifacts := BlindArtifacts{
		Calibration: CalibrationPacket{
			SchemaVersion: suite.SchemaVersion,
			Course:        suite.Course,
			Note:          "Calibration examples are separate from the designated evaluation packet and carry worked anchors for rater training.",
			Examples:      calExamples,
		},
		RaterABlank: RatingSheet{
			SchemaVersion:        suite.SchemaVersion,
			Course:               suite.Course,
			PacketSeed:           suite.Seed,
			RaterSlot:            "rater_a",
			RaterQualification:   "",
			QualificationAttest:  false,
			CalibrationCompleted: false,
			Locked:               false,
			DatasetKind:          DatasetKindUnmeasuredBlank,
			InstructionsNote:     instructions,
			Rows:                 rowsA,
		},
		RaterBBlank: RatingSheet{
			SchemaVersion:        suite.SchemaVersion,
			Course:               suite.Course,
			PacketSeed:           suite.Seed,
			RaterSlot:            "rater_b",
			RaterQualification:   "",
			QualificationAttest:  false,
			CalibrationCompleted: false,
			Locked:               false,
			DatasetKind:          DatasetKindUnmeasuredBlank,
			InstructionsNote:     instructions,
			Rows:                 rowsB,
		},
		Adjudication: AdjudicationSheet{
			SchemaVersion:    suite.SchemaVersion,
			Course:           suite.Course,
			PacketSeed:       suite.Seed,
			AdjudicatorRole:  NamedAdjudicatorRole,
			AdjudicatorName:  "",
			DatasetKind:      DatasetKindUnmeasuredBlank,
			InstructionsNote: "Copy both locked initial ratings in full. Name the adjudicator and give a resolution_reason for disagreements and any override of agreement.",
			Rows:             adjRows,
		},
		IdentityKey: IdentityKey{
			Course:        suite.Course,
			DatasetKind:   suite.DatasetKind,
			SchemaVersion: suite.SchemaVersion,
			PacketSeed:    suite.Seed,
			Warning:       "KEEP SEPARATE FROM BLIND RATING PACKETS UNTIL BOTH INITIAL SHEETS ARE LOCKED.",
			Entries:       keyEntries,
		},
	}
	if err := checkRaterFacingBlinding(artifacts); err != nil {
		return BlindArtifacts{}, err
	}
	return artifacts, nil
}

// checkRaterFacingBlinding refuses to return material that discloses a case ID, provider, model,
// variant, or seeded label to raters. The error names the affected record for the organizer.
func checkRaterFacingBlinding(artifacts BlindArtifacts) error {
	key := artifacts.IdentityKey
	forbidden := restrictedIdentifiers(key)
	for i, row := range artifacts.RaterABlank.Rows {
		if err := checkSerializedBlinding(row, forbidden); err != nil {
			return fmt.Errorf("%w: case %q at presentation order %d: %w", ErrValidation, key.Entries[i].CaseID, row.PresentationOrder, err)
		}
	}
	for _, example := range artifacts.Calibration.Examples {
		if err := checkSerializedBlinding(example, forbidden); err != nil {
			return fmt.Errorf("%w: calibration example %q: %w", ErrValidation, example.CalibrationID, err)
		}
	}
	for _, packet := range []struct {
		name  string
		value any
	}{
		{"calibration packet", artifacts.Calibration},
		{"rater_a sheet", artifacts.RaterABlank},
		{"rater_b sheet", artifacts.RaterBBlank},
		{"adjudication sheet", artifacts.Adjudication},
	} {
		if err := checkSerializedBlinding(packet.value, forbidden); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrValidation, packet.name, err)
		}
	}
	return nil
}

func checkSerializedBlinding(v any, forbidden []blindingIdentifier) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return checkBlinding(data, forbidden)
}

func opaqueResponseID(seed uint64, orderIdx int, digest string, used map[string]bool) string {
	var salt uint64
	for {
		var buf [24]byte
		binary.BigEndian.PutUint64(buf[:8], seed)
		binary.BigEndian.PutUint64(buf[8:16], uint64(orderIdx)) //nolint:gosec // orderIdx is a non-negative slice index.
		binary.BigEndian.PutUint64(buf[16:24], salt)
		h := sha256.New()
		h.Write(buf[:])
		h.Write([]byte(digest))
		sum := h.Sum(nil)
		id := "resp-" + hex.EncodeToString(sum[:6])
		if !used[id] {
			return id
		}
		salt++
	}
}

func materialDigest(row RatingRow) (string, error) {
	row.ResponseID = ""
	row.PresentationOrder = 0
	row.Rating = nil
	data, err := json.Marshal(row)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// addCaseMaterial hashes the fields presented in both calibration and designated cases.
func (r *caseRoster) addCaseMaterial(
	id, prompt string,
	request domain.AdaptivePlanRequest,
	items []domain.PlanItem,
	evidence []ResourceEvidence,
	factIDs []string,
	facts map[string]ReferenceFact,
) (RatingRow, string, error) {
	if err := validateResourceEvidence(items, evidence); err != nil {
		return RatingRow{}, "", err
	}
	locators, err := referenceLocators(factIDs, facts)
	if err != nil {
		return RatingRow{}, "", err
	}
	row := RatingRow{
		PromptContext:     prompt,
		VARKProfile:       request.VARKProfile,
		FailedTopics:      slices.Clone(request.FailedTopics),
		MasteredTopics:    slices.Clone(request.MasteredTopics),
		Items:             clonePlanItems(items),
		ResourceEvidence:  slices.Clone(evidence),
		ReferenceLocators: locators,
	}
	digest, err := materialDigest(row)
	if err != nil {
		return RatingRow{}, "", err
	}
	if err := r.addMaterial(id, digest); err != nil {
		return RatingRow{}, "", err
	}
	return row, digest, nil
}

func referenceLocators(ids []string, facts map[string]ReferenceFact) ([]SourceReference, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("missing reference facts")
	}
	var locators []SourceReference
	for _, id := range ids {
		fact, ok := facts[id]
		if !ok || len(fact.References) == 0 {
			return nil, fmt.Errorf("no locators for reference fact %q", id)
		}
		for _, ref := range fact.References {
			if !slices.Contains(locators, ref) {
				locators = append(locators, ref)
			}
		}
	}
	return locators, nil
}

func clonePlanItems(items []domain.PlanItem) []domain.PlanItem {
	out := make([]domain.PlanItem, len(items))
	for i, item := range items {
		out[i] = domain.PlanItem{
			Topic:             item.Topic,
			Priority:          item.Priority,
			PrerequisiteChain: slices.Clone(item.PrerequisiteChain),
			Explanation:       item.Explanation,
			Resources:         slices.Clone(item.Resources),
		}
	}
	return out
}

// MarshalCanonicalJSON encodes v deterministically with two-space indentation and trailing newline.
func MarshalCanonicalJSON(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// CheckNoBlindingLeaks checks decoded JSON field names and string values, as well as plain-text
// packet documents, for case IDs, provider/model/variant identifiers, and seeded scenario labels.
func CheckNoBlindingLeaks(serialized []byte, key IdentityKey) error {
	return checkBlinding(serialized, restrictedIdentifiers(key))
}

type blindingIdentifier struct {
	original string
	folded   string
}

func restrictedIdentifiers(key IdentityKey) []blindingIdentifier {
	tokens := []string{
		"seeded_critical",
		"synthetic_origin",
		`"origin":`,
		"scenario_tags",
		"defect_note",
		"synthetic-provider",
		"synth-model",
		"structurally_valid_wrong_answer",
		"prereq-invert",
		"link-mismatch",
		"baseline-v1",
		"limit-v1",
		"compact-v1",
		"ambig-v1",
		"missing-ev",
		"missing evidence:",
		"ambiguous prompt:",
		"/offtopic/",
		"output tests exact service length bounds",
		"candidate returns empty resources",
		"response returns empty resources",
		"no resources returned",
		"; empty resources",
	}
	for _, entry := range key.Entries {
		tokens = append(tokens, entry.CaseID, entry.Provider, entry.Model, entry.Variant)
		tokens = append(tokens, entry.ScenarioTags...)
	}
	forbidden := make([]blindingIdentifier, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		folded := strings.ToLower(token)
		if folded != "" && !seen[folded] {
			forbidden = append(forbidden, blindingIdentifier{original: token, folded: folded})
			seen[folded] = true
		}
	}
	return forbidden
}

func checkBlinding(serialized []byte, forbidden []blindingIdentifier) error {
	var value any
	if err := json.Unmarshal(serialized, &value); err != nil {
		return checkBlindingText(string(serialized), forbidden)
	}
	return checkBlindingValue(value, forbidden)
}

func checkBlindingValue(value any, forbidden []blindingIdentifier) error {
	switch value := value.(type) {
	case string:
		return checkBlindingText(value, forbidden)
	case []any:
		for _, item := range value {
			if err := checkBlindingValue(item, forbidden); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, field := range slices.Sorted(maps.Keys(value)) {
			switch strings.ToLower(field) {
			case "origin", "synthetic_origin", "scenario_tags", "defect_note", "seeded_critical", "seeded_critical_error", "seeded_critical_type":
				return fmt.Errorf("%w: blind packet leaked restricted field %q", ErrValidation, field)
			}
			if err := checkBlindingText(field, forbidden); err != nil {
				return err
			}
			if err := checkBlindingValue(value[field], forbidden); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkBlindingText(text string, forbidden []blindingIdentifier) error {
	text = strings.ToLower(text)
	for _, token := range forbidden {
		if strings.Contains(text, token.folded) {
			return fmt.Errorf("%w: blind packet leaked restricted identifier %q", ErrValidation, token.original)
		}
	}
	return nil
}
