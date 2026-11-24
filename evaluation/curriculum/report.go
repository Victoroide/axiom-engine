package curriculum

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/graph"
)

const pendingReviewNotice = "Subject-specialist verification is pending."

// GraphStats captures deterministic structural properties and digest of the evaluated graph.
type GraphStats struct {
	Path                string `json:"path"`
	SHA256              string `json:"sha256"`
	Concepts            int    `json:"concepts"`
	Tuples              int    `json:"tuples"`
	DependsOnEdges      int    `json:"depends_on_edges"`
	IsAEdges            int    `json:"is_a_edges"`
	ConnectedComponents int    `json:"connected_components"`
}

// RelationCounts tracks evidence edge types separately between depends_on and is_a.
type RelationCounts struct {
	DependsOn int `json:"depends_on"`
	IsA       int `json:"is_a"`
}

// StatusCounts summarizes objective coverage across mapped, partial, and missing categories.
type StatusCounts struct {
	Mapped  int `json:"mapped"`
	Partial int `json:"partial"`
	Missing int `json:"missing"`
}

// UnitSummary aggregates objective counts and disputes for one course unit.
type UnitSummary struct {
	Unit               int    `json:"unit"`
	UnitTitle          string `json:"unit_title"`
	Objectives         int    `json:"objectives"`
	Mapped             int    `json:"mapped"`
	Partial            int    `json:"partial"`
	Missing            int    `json:"missing"`
	UnresolvedDisputes int    `json:"unresolved_disputes"`
}

// ReportMapping captures one evaluated objective row with canonicalized graph evidence.
type ReportMapping struct {
	ObjectiveID             string         `json:"objective_id"`
	Unit                    int            `json:"unit"`
	Topic                   string         `json:"topic"`
	SourcePages             string         `json:"source_pages"`
	Summary                 string         `json:"summary"`
	Status                  string         `json:"status"`
	IsolatedUnrepresentable bool           `json:"isolated_unrepresentable,omitempty"`
	Provisional             bool           `json:"provisional"`
	Concepts                []string       `json:"concepts"`
	EvidenceEdges           []EvidenceEdge `json:"evidence_edges"`
	Disputes                []Dispute      `json:"disputes,omitempty"`
	Rationale               string         `json:"rationale"`
}

// UnresolvedDispute exposes a pending dispute item in the top-level report summary.
type UnresolvedDispute struct {
	DisputeID   string        `json:"dispute_id"`
	ObjectiveID string        `json:"objective_id"`
	Kind        string        `json:"kind"`
	Edge        *EvidenceEdge `json:"edge,omitempty"`
	Rationale   string        `json:"rationale"`
}

// Report is the deterministic offline coverage evaluation output for one graph and manifest edition.
type Report struct {
	SchemaVersion           string                 `json:"schema_version"`
	Edition                 string                 `json:"edition"`
	Framework               string                 `json:"framework"`
	EffectiveTerm           string                 `json:"effective_term"`
	ClarificationsRevision  string                 `json:"clarifications_revision,omitempty"`
	Language                string                 `json:"language"`
	ProgrammingLanguage     string                 `json:"programming_language"`
	Provisional             bool                   `json:"provisional"`
	ProvisionalNotice       string                 `json:"provisional_notice"`
	ExpertReviewForm        string                 `json:"expert_review_form"`
	Sources                 []SourceDocument       `json:"sources"`
	Graph                   GraphStats             `json:"graph"`
	TotalDenominator        int                    `json:"total_denominator"`
	StatusCounts            StatusCounts           `json:"status_counts"`
	EvidenceRelations       RelationCounts         `json:"evidence_relations"`
	UnresolvedDisputesCount int                    `json:"unresolved_disputes_count"`
	OutsideCourseCount      int                    `json:"outside_course_count"`
	UnitBreakdown           []UnitSummary          `json:"unit_breakdown"`
	Mappings                []ReportMapping        `json:"mappings"`
	UnresolvedDisputes      []UnresolvedDispute    `json:"unresolved_disputes"`
	OutsideCourse           []OutsideCourseConcept `json:"outside_course"`
}

type graphIndex struct {
	graph      *graph.InMemoryGraph
	nodes      map[string]bool
	edges      map[EvidenceEdge]bool
	dependsOn  int
	isA        int
	components int
}

// EvaluateFiles loads manifest, crosswalk, and graph files and evaluates coverage.
func EvaluateFiles(manifestPath, crosswalkPath, graphPath string) (Report, error) {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return Report{}, err
	}
	c, err := LoadCrosswalk(crosswalkPath)
	if err != nil {
		return Report{}, err
	}
	f, err := os.Open(filepath.Clean(graphPath)) //nolint:gosec // The caller supplies the local graph path.
	if err != nil {
		return Report{}, fmt.Errorf("open graph: %w", err)
	}
	defer func() { _ = f.Close() }()
	return evaluate(m, c, f, c.GraphPath)
}

// EvaluateBytes validates and evaluates a manifest and crosswalk against raw graph JSON bytes.
func EvaluateBytes(m Manifest, c Crosswalk, graphBytes []byte, graphPath string) (Report, error) {
	return evaluate(m, c, bytes.NewReader(graphBytes), graphPath)
}

func evaluate(m Manifest, c Crosswalk, input io.Reader, graphPath string) (Report, error) {
	if err := ValidateManifest(m); err != nil {
		return Report{}, err
	}
	if err := validateCrosswalkHeader(m, c); err != nil {
		return Report{}, err
	}
	hash := sha256.New()
	g, err := graph.Decode(io.TeeReader(input, hash))
	if err != nil {
		return Report{}, fmt.Errorf("decode target graph: %w", err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(c.GraphSHA256, digest) {
		return Report{}, fmt.Errorf("graph SHA-256 mismatch: crosswalk pins %q, actual %q", c.GraphSHA256, digest)
	}
	idx := buildGraphIndex(g)
	if graphPath == "" {
		graphPath = c.GraphPath
	}
	mappings, usedConcepts, state, err := evaluateMappings(m, c, idx)
	if err != nil {
		return Report{}, err
	}
	outside, err := evaluateOutsideCourse(c.OutsideCourse, idx, usedConcepts)
	if err != nil {
		return Report{}, err
	}

	units := buildUnitBreakdown(m.Objectives, mappings)
	slices.SortFunc(state.unresolved, func(a, b UnresolvedDispute) int {
		if n := cmp.Compare(a.ObjectiveID, b.ObjectiveID); n != 0 {
			return n
		}
		return cmp.Compare(a.DisputeID, b.DisputeID)
	})

	return Report{
		SchemaVersion:          c.SchemaVersion,
		Edition:                m.Edition,
		Framework:              m.Framework,
		EffectiveTerm:          m.EffectiveTerm,
		ClarificationsRevision: m.ClarificationsRevision,
		Language:               m.Language,
		ProgrammingLanguage:    m.ProgrammingLanguage,
		Provisional:            true,
		ProvisionalNotice:      pendingReviewNotice + " " + strings.TrimSpace(c.ProvisionalNotice),
		ExpertReviewForm:       c.ExpertReviewForm,
		Sources:                slices.Clone(m.Sources),
		Graph: GraphStats{
			Path:                graphPath,
			SHA256:              digest,
			Concepts:            len(idx.nodes),
			Tuples:              len(idx.graph.All()),
			DependsOnEdges:      idx.dependsOn,
			IsAEdges:            idx.isA,
			ConnectedComponents: idx.components,
		},
		TotalDenominator:        m.Denominator,
		StatusCounts:            state.statusCounts,
		EvidenceRelations:       state.relations,
		UnresolvedDisputesCount: len(state.unresolved),
		OutsideCourseCount:      len(outside),
		UnitBreakdown:           units,
		Mappings:                mappings,
		UnresolvedDisputes:      state.unresolved,
		OutsideCourse:           outside,
	}, nil
}

func validateCrosswalkHeader(m Manifest, c Crosswalk) error {
	if strings.TrimSpace(c.SchemaVersion) == "" {
		return errors.New("crosswalk schema_version is required")
	}
	if c.Edition != m.Edition {
		return fmt.Errorf("crosswalk edition %q does not match manifest edition %q", c.Edition, m.Edition)
	}
	if !validSHA256Hex(strings.ToLower(c.GraphSHA256)) {
		return fmt.Errorf("crosswalk graph_sha256 %q is invalid", c.GraphSHA256)
	}
	if strings.TrimSpace(c.ProvisionalNotice) == "" {
		return errors.New("crosswalk provisional_notice is required")
	}
	if strings.TrimSpace(c.ExpertReviewForm) == "" {
		return errors.New("crosswalk expert_review_form is required")
	}
	return nil
}

func buildGraphIndex(g *graph.InMemoryGraph) graphIndex {
	tuples := g.All()
	idx := graphIndex{
		graph: g,
		nodes: make(map[string]bool),
		edges: make(map[EvidenceEdge]bool, len(tuples)),
	}
	adj := make(map[string][]string)
	for _, t := range tuples {
		idx.nodes[t.Subject] = true
		idx.nodes[t.Object] = true
		idx.edges[EvidenceEdge{Subject: t.Subject, Relation: t.Relation, Object: t.Object}] = true
		adj[t.Subject] = append(adj[t.Subject], t.Object)
		adj[t.Object] = append(adj[t.Object], t.Subject)
		switch t.Relation {
		case "depends_on":
			idx.dependsOn++
		case "is_a":
			idx.isA++
		}
	}
	visited := make(map[string]bool, len(idx.nodes))
	for _, start := range slices.Sorted(maps.Keys(idx.nodes)) {
		if visited[start] {
			continue
		}
		idx.components++
		visited[start] = true
		queue := []string{start}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, next := range adj[cur] {
				if !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
	}
	return idx
}

type evalState struct {
	statusCounts StatusCounts
	relations    RelationCounts
	unresolved   []UnresolvedDispute
}

func evaluateMappings(m Manifest, c Crosswalk, idx graphIndex) ([]ReportMapping, map[string]bool, evalState, error) {
	byObj := make(map[string]MappingRow, len(c.Mappings))
	manifestIDs := make(map[string]bool, len(m.Objectives))
	for _, obj := range m.Objectives {
		manifestIDs[obj.ID] = true
	}

	for _, row := range c.Mappings {
		if !manifestIDs[row.ObjectiveID] {
			return nil, nil, evalState{}, fmt.Errorf("unknown objective id %q in crosswalk", row.ObjectiveID)
		}
		if _, dup := byObj[row.ObjectiveID]; dup {
			return nil, nil, evalState{}, fmt.Errorf("duplicate objective id %q in crosswalk", row.ObjectiveID)
		}
		byObj[row.ObjectiveID] = row
	}

	state := evalState{unresolved: make([]UnresolvedDispute, 0)}
	seenDisputeIDs := make(map[string]bool)
	rejectedEdges := make(map[EvidenceEdge]bool)
	unresolvedEdges := make(map[EvidenceEdge]UnresolvedDispute)
	// Resolve all disputes before checking any objective's evidence.
	for _, obj := range m.Objectives {
		row, ok := byObj[obj.ID]
		if !ok {
			return nil, nil, evalState{}, fmt.Errorf("absent objective %q in crosswalk", obj.ID)
		}
		disputes, unresolved, err := resolveRowDisputes(obj.ID, row.Disputes, idx, seenDisputeIDs)
		if err != nil {
			return nil, nil, evalState{}, err
		}
		for _, dispute := range disputes {
			if dispute.Status == DisputeRejected && dispute.Edge != nil {
				rejectedEdges[*dispute.Edge] = true
			}
		}
		for _, dispute := range unresolved {
			if dispute.Edge != nil {
				unresolvedEdges[*dispute.Edge] = dispute
			}
		}
		row.Disputes = disputes
		byObj[obj.ID] = row
		state.unresolved = append(state.unresolved, unresolved...)
	}

	usedConcepts := make(map[string]bool)
	out := make([]ReportMapping, 0, len(m.Objectives))
	for _, obj := range m.Objectives {
		evaluated, rowRels, err := evaluateRow(obj, byObj[obj.ID], idx, rejectedEdges, unresolvedEdges, usedConcepts)
		if err != nil {
			return nil, nil, evalState{}, err
		}
		switch evaluated.Status {
		case StatusMapped:
			state.statusCounts.Mapped++
		case StatusPartial:
			state.statusCounts.Partial++
		case StatusMissing:
			state.statusCounts.Missing++
		}
		state.relations.DependsOn += rowRels.DependsOn
		state.relations.IsA += rowRels.IsA
		out = append(out, evaluated)
	}
	return out, usedConcepts, state, nil
}

func evaluateRow(
	obj Objective,
	row MappingRow,
	idx graphIndex,
	rejectedEdges map[EvidenceEdge]bool,
	unresolvedEdges map[EvidenceEdge]UnresolvedDispute,
	usedConcepts map[string]bool,
) (ReportMapping, RelationCounts, error) {
	if strings.TrimSpace(row.Rationale) == "" {
		return ReportMapping{}, RelationCounts{}, fmt.Errorf("objective %q requires a non-empty rationale", obj.ID)
	}
	switch row.Status {
	case StatusMapped, StatusPartial, StatusMissing:
	default:
		return ReportMapping{}, RelationCounts{}, fmt.Errorf("objective %q has invalid status %q", obj.ID, row.Status)
	}

	concepts, err := resolveRowConcepts(obj.ID, row.Concepts, idx, usedConcepts)
	if err != nil {
		return ReportMapping{}, RelationCounts{}, err
	}
	edges, relCounts, err := resolveRowEdges(obj.ID, row.EvidenceEdges, concepts, rejectedEdges, idx)
	if err != nil {
		return ReportMapping{}, RelationCounts{}, err
	}
	if err := enforceRowStatusRules(obj.ID, row, concepts, edges, relCounts, unresolvedEdges); err != nil {
		return ReportMapping{}, RelationCounts{}, err
	}

	return ReportMapping{
		ObjectiveID:             obj.ID,
		Unit:                    obj.Unit,
		Topic:                   obj.Topic,
		SourcePages:             formatSourcePages(obj.SourcePage, obj.SourceEndPage),
		Summary:                 obj.Summary,
		Status:                  row.Status,
		IsolatedUnrepresentable: row.IsolatedUnrepresentable,
		Provisional:             true,
		Concepts:                concepts,
		EvidenceEdges:           edges,
		Disputes:                row.Disputes,
		Rationale:               row.Rationale,
	}, relCounts, nil
}

func resolveRowConcepts(objectiveID string, raw []string, idx graphIndex, usedConcepts map[string]bool) ([]string, error) {
	seenCanonical := make(map[string]string, len(raw))
	resolved := make([]string, 0, len(raw))
	for _, name := range raw {
		stored, ok := idx.graph.Resolve(name)
		if !ok {
			return nil, fmt.Errorf("objective %q references nonexistent node %q", objectiveID, name)
		}
		key := graph.Canonical(name)
		if prev, dup := seenCanonical[key]; dup {
			return nil, fmt.Errorf("objective %q has canonical alias collision between %q and %q", objectiveID, prev, name)
		}
		seenCanonical[key] = name
		usedConcepts[stored] = true
		resolved = append(resolved, stored)
	}
	slices.Sort(resolved)
	return resolved, nil
}

func resolveRowEdges(objectiveID string, raw []EvidenceEdge, concepts []string, rejectedEdges map[EvidenceEdge]bool, idx graphIndex) ([]EvidenceEdge, RelationCounts, error) {
	seen := make(map[EvidenceEdge]bool, len(raw))
	resolved := make([]EvidenceEdge, 0, len(raw))
	var counts RelationCounts
	for _, edge := range raw {
		normalized, err := resolveEdge(objectiveID, "evidence edge", edge, idx)
		if err != nil {
			return nil, RelationCounts{}, err
		}
		if rejectedEdges[normalized] {
			return nil, RelationCounts{}, fmt.Errorf("objective %q cites rejected evidence edge %+v", objectiveID, normalized)
		}
		_, subjectCited := slices.BinarySearch(concepts, normalized.Subject)
		_, objectCited := slices.BinarySearch(concepts, normalized.Object)
		if !subjectCited || !objectCited {
			return nil, RelationCounts{}, fmt.Errorf("objective %q evidence edge %+v: both endpoints must be cited concepts", objectiveID, normalized)
		}
		if seen[normalized] {
			return nil, RelationCounts{}, fmt.Errorf("objective %q repeats evidence edge %+v", objectiveID, normalized)
		}
		seen[normalized] = true
		resolved = append(resolved, normalized)
		switch normalized.Relation {
		case "depends_on":
			counts.DependsOn++
		case "is_a":
			counts.IsA++
		}
	}
	slices.SortFunc(resolved, compareEdges)
	return resolved, counts, nil
}

func resolveRowDisputes(objectiveID string, raw []Dispute, idx graphIndex, seenDisputeIDs map[string]bool) ([]Dispute, []UnresolvedDispute, error) {
	disputes := make([]Dispute, 0, len(raw))
	unresolved := make([]UnresolvedDispute, 0, len(raw))
	for _, d := range raw {
		if strings.TrimSpace(d.ID) == "" {
			return nil, nil, fmt.Errorf("objective %q has dispute with empty id", objectiveID)
		}
		if seenDisputeIDs[d.ID] {
			return nil, nil, fmt.Errorf("duplicate dispute id %q", d.ID)
		}
		seenDisputeIDs[d.ID] = true
		if strings.TrimSpace(d.Kind) == "" || strings.TrimSpace(d.Rationale) == "" {
			return nil, nil, fmt.Errorf("dispute %q requires kind and rationale", d.ID)
		}
		switch d.Status {
		case DisputeUnresolved, DisputeConfirmed, DisputeRejected:
		default:
			return nil, nil, fmt.Errorf("dispute %q has invalid status %q", d.ID, d.Status)
		}
		copied := d
		if d.Edge != nil {
			normalized, err := resolveEdge(objectiveID, "dispute "+d.ID+" edge", *d.Edge, idx)
			if err != nil {
				return nil, nil, err
			}
			copied.Edge = &normalized
		}
		disputes = append(disputes, copied)
		if d.Status == DisputeUnresolved {
			unresolved = append(unresolved, UnresolvedDispute{
				DisputeID:   d.ID,
				ObjectiveID: objectiveID,
				Kind:        d.Kind,
				Edge:        copied.Edge,
				Rationale:   d.Rationale,
			})
		}
	}
	slices.SortFunc(disputes, func(a, b Dispute) int { return cmp.Compare(a.ID, b.ID) })
	return disputes, unresolved, nil
}

func resolveEdge(objectiveID, label string, edge EvidenceEdge, idx graphIndex) (EvidenceEdge, error) {
	if edge.Relation != "depends_on" && edge.Relation != "is_a" {
		return EvidenceEdge{}, fmt.Errorf("objective %q %s has unsupported relation %q", objectiveID, label, edge.Relation)
	}
	subj, ok := idx.graph.Resolve(edge.Subject)
	if !ok {
		return EvidenceEdge{}, fmt.Errorf("objective %q %s references nonexistent subject %q", objectiveID, label, edge.Subject)
	}
	obj, ok := idx.graph.Resolve(edge.Object)
	if !ok {
		return EvidenceEdge{}, fmt.Errorf("objective %q %s references nonexistent object %q", objectiveID, label, edge.Object)
	}
	normalized := EvidenceEdge{Subject: subj, Relation: edge.Relation, Object: obj}
	if !idx.edges[normalized] {
		return EvidenceEdge{}, fmt.Errorf("objective %q %s points to nonexistent edge (%s, %s, %s)", objectiveID, label, subj, edge.Relation, obj)
	}
	return normalized, nil
}

func enforceRowStatusRules(
	objectiveID string,
	row MappingRow,
	concepts []string,
	edges []EvidenceEdge,
	rels RelationCounts,
	unresolvedEdges map[EvidenceEdge]UnresolvedDispute,
) error {
	if row.IsolatedUnrepresentable && row.Status != StatusMissing {
		return fmt.Errorf("objective %q isolated_unrepresentable must remain missing", objectiveID)
	}
	if row.Status != StatusMissing && len(concepts) == 0 {
		return fmt.Errorf("objective %q marked %s must cite at least one graph concept", objectiveID, row.Status)
	}
	switch row.Status {
	case StatusMapped:
		for _, edge := range edges {
			if dispute, open := unresolvedEdges[edge]; open {
				return fmt.Errorf("objective %q cites unresolved evidence edge %+v from dispute %q on objective %q and cannot be marked mapped",
					objectiveID, edge, dispute.DisputeID, dispute.ObjectiveID)
			}
		}
		if slices.ContainsFunc(row.Disputes, func(d Dispute) bool { return d.Status == DisputeUnresolved }) {
			return fmt.Errorf("objective %q has unresolved disputes and cannot be marked mapped", objectiveID)
		}
		if slices.ContainsFunc(row.Disputes, func(d Dispute) bool { return d.Status == DisputeRejected }) {
			return fmt.Errorf("objective %q has a rejected dispute and cannot be marked mapped", objectiveID)
		}
		if rels.IsA > 0 && rels.DependsOn == 0 {
			return fmt.Errorf("objective %q relies solely on taxonomic is_a edges without a depends_on prerequisite", objectiveID)
		}
	case StatusMissing:
		if len(concepts) > 0 {
			return fmt.Errorf("objective %q marked missing must not claim supporting concepts or evidence edges", objectiveID)
		}
	}
	return nil
}

func evaluateOutsideCourse(raw []OutsideCourseConcept, idx graphIndex, usedConcepts map[string]bool) ([]OutsideCourseConcept, error) {
	seenCanonical := make(map[string]string, len(raw))
	outsideNodes := make(map[string]bool, len(raw))
	out := make([]OutsideCourseConcept, 0, len(raw))

	for _, entry := range raw {
		if strings.TrimSpace(entry.Concept) == "" || strings.TrimSpace(entry.Category) == "" || strings.TrimSpace(entry.Reason) == "" {
			return nil, errors.New("outside_course entry requires concept, category, and reason")
		}
		stored, ok := idx.graph.Resolve(entry.Concept)
		if !ok {
			return nil, fmt.Errorf("outside_course references nonexistent node %q", entry.Concept)
		}
		key := graph.Canonical(entry.Concept)
		if prev, dup := seenCanonical[key]; dup {
			return nil, fmt.Errorf("outside_course has canonical alias collision between %q and %q", prev, entry.Concept)
		}
		if usedConcepts[stored] {
			return nil, fmt.Errorf("concept %q cannot appear in both course evidence and outside_course", stored)
		}
		seenCanonical[key] = entry.Concept
		outsideNodes[stored] = true
		out = append(out, OutsideCourseConcept{
			Concept:  stored,
			Category: entry.Category,
			Reason:   entry.Reason,
		})
	}

	for _, node := range slices.Sorted(maps.Keys(idx.nodes)) {
		if !usedConcepts[node] && !outsideNodes[node] {
			return nil, fmt.Errorf("graph concept %q is neither mapped as course evidence nor listed in outside_course", node)
		}
	}

	slices.SortFunc(out, func(a, b OutsideCourseConcept) int { return cmp.Compare(a.Concept, b.Concept) })
	return out, nil
}

func buildUnitBreakdown(objectives []Objective, mappings []ReportMapping) []UnitSummary {
	unitTitles := make(map[int]string)
	for _, obj := range objectives {
		if _, exists := unitTitles[obj.Unit]; !exists {
			unitTitles[obj.Unit] = obj.UnitTitle
		}
	}
	byUnit := make(map[int]*UnitSummary)
	for _, m := range mappings {
		u, exists := byUnit[m.Unit]
		if !exists {
			u = &UnitSummary{Unit: m.Unit, UnitTitle: unitTitles[m.Unit]}
			byUnit[m.Unit] = u
		}
		u.Objectives++
		switch m.Status {
		case StatusMapped:
			u.Mapped++
		case StatusPartial:
			u.Partial++
		case StatusMissing:
			u.Missing++
		}
		for _, d := range m.Disputes {
			if d.Status == DisputeUnresolved {
				u.UnresolvedDisputes++
			}
		}
	}
	units := make([]UnitSummary, 0, len(byUnit))
	for _, unit := range slices.Sorted(maps.Keys(byUnit)) {
		units = append(units, *byUnit[unit])
	}
	return units
}

func compareEdges(a, b EvidenceEdge) int {
	if n := cmp.Compare(a.Subject, b.Subject); n != 0 {
		return n
	}
	if n := cmp.Compare(a.Relation, b.Relation); n != 0 {
		return n
	}
	return cmp.Compare(a.Object, b.Object)
}

func formatSourcePages(start, end int) string {
	if end > start {
		return strconv.Itoa(start) + "-" + strconv.Itoa(end)
	}
	return strconv.Itoa(start)
}

// RenderReportJSON serializes a report deterministically as formatted JSON.
func RenderReportJSON(report Report) ([]byte, error) {
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// RenderReportMarkdown renders a deterministic Markdown coverage report.
func RenderReportMarkdown(report Report) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Curriculum Coverage Report: %s (%s)\n\n", report.Framework, report.Edition)
	if report.Provisional {
		fmt.Fprintf(&b, "> **Provisional Status:** %s\n\n", report.ProvisionalNotice)
	}

	writeSummaryTable(&b, report)
	writeSourcesTable(&b, report.Sources)
	writeUnitTable(&b, report.UnitBreakdown)
	writeCrosswalkTable(&b, report.Mappings)
	writeDisputesTable(&b, report.UnresolvedDisputes)
	writeOutsideCourseTable(&b, report.OutsideCourse)

	return b.Bytes(), nil
}

func writeSummaryTable(b *bytes.Buffer, r Report) {
	b.WriteString("## Summary\n\n")
	b.WriteString("| Field | Value |\n|---|---|\n")
	fmt.Fprintf(b, "| Framework | %s |\n", escapeTextCell(r.Framework))
	fmt.Fprintf(b, "| Edition | `%s` |\n", escapeCell(r.Edition))
	fmt.Fprintf(b, "| Effective Term | %s |\n", escapeTextCell(r.EffectiveTerm))
	if r.ClarificationsRevision != "" {
		fmt.Fprintf(b, "| Clarifications Revision | %s |\n", escapeTextCell(r.ClarificationsRevision))
	}
	fmt.Fprintf(b, "| Course Language | %s (%s) |\n", escapeTextCell(r.Language), escapeTextCell(r.ProgrammingLanguage))
	fmt.Fprintf(b, "| Graph Path | `%s` |\n", escapeCell(r.Graph.Path))
	fmt.Fprintf(b, "| Graph SHA-256 | `%s` |\n", r.Graph.SHA256)
	fmt.Fprintf(b, "| Graph Size | %d concepts, %d tuples (%d `depends_on`, %d `is_a`), %d connected component(s) |\n",
		r.Graph.Concepts, r.Graph.Tuples, r.Graph.DependsOnEdges, r.Graph.IsAEdges, r.Graph.ConnectedComponents)
	fmt.Fprintf(b, "| Total Denominator | %d distinct learning objectives |\n", r.TotalDenominator)
	fmt.Fprintf(b, "| Status Counts | %d mapped, %d partial, %d missing |\n", r.StatusCounts.Mapped, r.StatusCounts.Partial, r.StatusCounts.Missing)
	fmt.Fprintf(b, "| Evidence Relations | %d `depends_on`, %d `is_a` |\n", r.EvidenceRelations.DependsOn, r.EvidenceRelations.IsA)
	fmt.Fprintf(b, "| Unresolved Disputes | %d |\n", r.UnresolvedDisputesCount)
	fmt.Fprintf(b, "| Outside-Course Concepts | %d |\n", r.OutsideCourseCount)
	fmt.Fprintf(b, "| Blank Dispute Form | `%s` |\n\n", escapeCell(r.ExpertReviewForm))
}

func writeSourcesTable(b *bytes.Buffer, sources []SourceDocument) {
	b.WriteString("## Authoritative Sources\n\n")
	b.WriteString("| Source | Publisher | Revision | SHA-256 | Link |\n|---|---|---|---|---|\n")
	for _, s := range sources {
		fmt.Fprintf(b, "| %s (`%s`) | %s | %s | `%s` | [PDF](%s) |\n",
			escapeTextCell(s.Title), escapeCell(s.ID), escapeTextCell(s.Publisher), escapeTextCell(s.Revision), s.SHA256, s.URL)
	}
	b.WriteByte('\n')
}

func writeUnitTable(b *bytes.Buffer, units []UnitSummary) {
	b.WriteString("## Coverage by Unit\n\n")
	b.WriteString("| Unit | Title | Objectives | Mapped | Partial | Missing | Unresolved Disputes |\n|---|---|---:|---:|---:|---:|---:|\n")
	for _, u := range units {
		fmt.Fprintf(b, "| Unit %d | %s | %d | %d | %d | %d | %d |\n",
			u.Unit, escapeTextCell(u.UnitTitle), u.Objectives, u.Mapped, u.Partial, u.Missing, u.UnresolvedDisputes)
	}
	b.WriteByte('\n')
}

func writeCrosswalkTable(b *bytes.Buffer, rows []ReportMapping) {
	b.WriteString("## Objective Crosswalk\n\n")
	b.WriteString("| Objective | Unit | Page(s) | Original Summary | Status | Graph Concepts | Directed Relations | Rationale | Disputes |\n|---|---:|---|---|---|---|---|---|---|\n")
	for _, row := range rows {
		concepts := "-"
		if len(row.Concepts) > 0 {
			concepts = strings.Join(row.Concepts, ", ")
		}
		edges := "-"
		if len(row.EvidenceEdges) > 0 {
			parts := make([]string, len(row.EvidenceEdges))
			for i, e := range row.EvidenceEdges {
				parts[i] = fmt.Sprintf("%s -[%s]-> %s", e.Subject, e.Relation, e.Object)
			}
			edges = strings.Join(parts, "; ")
		}
		disputes := "-"
		if len(row.Disputes) > 0 {
			parts := make([]string, len(row.Disputes))
			for i, d := range row.Disputes {
				parts[i] = fmt.Sprintf("%s (%s: %s)", d.ID, d.Status, d.Kind)
			}
			disputes = strings.Join(parts, "; ")
		}
		status := row.Status
		if row.IsolatedUnrepresentable {
			status += " (isolated)"
		}
		fmt.Fprintf(b, "| `%s` | %d | %s | %s | `%s` | %s | %s | %s | %s |\n",
			escapeCell(row.ObjectiveID), row.Unit, escapeCell(row.SourcePages), escapeTextCell(row.Summary),
			escapeCell(status), escapeTextCell(concepts), escapeTextCell(edges), escapeTextCell(row.Rationale), escapeTextCell(disputes))
	}
	b.WriteByte('\n')
}

func writeDisputesTable(b *bytes.Buffer, disputes []UnresolvedDispute) {
	b.WriteString("## Unresolved Disputes\n\n")
	if len(disputes) == 0 {
		b.WriteString("None.\n\n")
		return
	}
	b.WriteString("| Dispute ID | Objective | Kind | Edge | Rationale |\n|---|---|---|---|---|\n")
	for _, d := range disputes {
		edge := "-"
		if d.Edge != nil {
			edge = fmt.Sprintf("`%s -[%s]-> %s`", d.Edge.Subject, d.Edge.Relation, d.Edge.Object)
		}
		fmt.Fprintf(b, "| `%s` | `%s` | `%s` | %s | %s |\n",
			escapeCell(d.DisputeID), escapeCell(d.ObjectiveID), escapeCell(d.Kind), edge, escapeTextCell(d.Rationale))
	}
	b.WriteByte('\n')
}

func writeOutsideCourseTable(b *bytes.Buffer, outside []OutsideCourseConcept) {
	b.WriteString("## Concepts Outside Course Scope\n\n")
	if len(outside) == 0 {
		b.WriteString("None.\n")
		return
	}
	b.WriteString("| Concept | Category | Reason |\n|---|---|---|\n")
	for _, o := range outside {
		fmt.Fprintf(b, "| `%s` | `%s` | %s |\n", escapeCell(o.Concept), escapeCell(o.Category), escapeTextCell(o.Reason))
	}
}

var markdownEntities = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escapeTextCell(s string) string {
	return escapeCell(markdownEntities.Replace(s))
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", `\|`)
}
