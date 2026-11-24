package curriculum

import (
	"fmt"
	"io"
)

// Mapping status constants classify how well a knowledge graph covers an objective.
const (
	StatusMapped  = "mapped"
	StatusPartial = "partial"
	StatusMissing = "missing"
)

// Dispute status constants track independent specialist verification state.
const (
	DisputeUnresolved = "unresolved"
	DisputeConfirmed  = "confirmed"
	DisputeRejected   = "rejected"
)

// EvidenceEdge identifies a directed graph triple used as mapping or dispute evidence.
type EvidenceEdge struct {
	Subject  string `json:"subject"`
	Relation string `json:"relation"`
	Object   string `json:"object"`
}

// Dispute records a contested prerequisite, taxonomic, or scope link awaiting review.
type Dispute struct {
	ID        string        `json:"id"`
	Edge      *EvidenceEdge `json:"edge,omitempty"`
	Kind      string        `json:"kind"`
	Status    string        `json:"status"`
	Rationale string        `json:"rationale"`
}

// MappingRow maps a single manifest objective to graph evidence and unresolved disputes.
type MappingRow struct {
	ObjectiveID             string         `json:"objective_id"`
	Status                  string         `json:"status"`
	IsolatedUnrepresentable bool           `json:"isolated_unrepresentable,omitempty"`
	Concepts                []string       `json:"concepts"`
	EvidenceEdges           []EvidenceEdge `json:"evidence_edges"`
	Rationale               string         `json:"rationale"`
	Disputes                []Dispute      `json:"disputes,omitempty"`
}

// OutsideCourseConcept documents a graph concept that falls outside the target course scope.
type OutsideCourseConcept struct {
	Concept  string `json:"concept"`
	Category string `json:"category"`
	Reason   string `json:"reason"`
}

// Crosswalk records provisional objective mappings and out-of-course concepts for one graph.
type Crosswalk struct {
	SchemaVersion     string                 `json:"schema_version"`
	Edition           string                 `json:"edition"`
	GraphPath         string                 `json:"graph_path"`
	GraphSHA256       string                 `json:"graph_sha256"`
	Provisional       bool                   `json:"provisional"` // A crosswalk flag cannot attest to specialist review.
	ProvisionalNotice string                 `json:"provisional_notice"`
	ExpertReviewForm  string                 `json:"expert_review_form"`
	Mappings          []MappingRow           `json:"mappings"`
	OutsideCourse     []OutsideCourseConcept `json:"outside_course"`
}

// LoadCrosswalk reads and decodes a curriculum crosswalk JSON file.
func LoadCrosswalk(path string) (Crosswalk, error) {
	data, err := readCleanFile(path)
	if err != nil {
		return Crosswalk{}, fmt.Errorf("read crosswalk: %w", err)
	}
	return decodeStrictBytes[Crosswalk](data)
}

// ParseCrosswalk strictly decodes a curriculum crosswalk from JSON.
func ParseCrosswalk(r io.Reader) (Crosswalk, error) {
	return decodeStrictJSON[Crosswalk](r)
}
