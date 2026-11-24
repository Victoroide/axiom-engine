package quality_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Victoroide/axiom-engine/evaluation/quality"
)

func TestReproduciblePacketsAndZeroBlindingLeaks(t *testing.T) {
	_, facts, suite, _, _ := loadAllFixtures(t)
	artifacts, err := quality.BuildBlindArtifacts(suite, facts)
	if err != nil {
		t.Fatalf("BuildBlindArtifacts: %v", err)
	}
	blankReport, err := quality.BuildCandidateBlankReport(artifacts)
	if err != nil {
		t.Fatalf("BuildCandidateBlankReport: %v", err)
	}

	checks := []struct {
		dir      string
		file     string
		value    any
		checkBld bool
	}{
		{"packets", "calibration_packet.json", artifacts.Calibration, true},
		{"packets", "rater_a_blank.json", artifacts.RaterABlank, true},
		{"packets", "rater_b_blank.json", artifacts.RaterBBlank, true},
		{"packets", "adjudication_blank.json", artifacts.Adjudication, true},
		{"testdata", "identity_key.json", artifacts.IdentityKey, false},
		{"testdata", "candidate_score_report_blank.json", blankReport, false},
	}

	for _, tc := range checks {
		got, err := quality.MarshalCanonicalJSON(tc.value)
		if err != nil {
			t.Fatalf("marshal %s/%s: %v", tc.dir, tc.file, err)
		}
		if tc.checkBld {
			if err := quality.CheckNoBlindingLeaks(got, artifacts.IdentityKey); err != nil {
				t.Fatalf("blinding leak in %s/%s: %v", tc.dir, tc.file, err)
			}
		}
		disk, err := readRepoFile(filepath.Join(tc.dir, tc.file))
		if err != nil {
			t.Fatalf("read %s/%s: %v", tc.dir, tc.file, err)
		}
		if !bytes.Equal(got, disk) {
			t.Fatalf("%s/%s on disk differs from reproducible generation", tc.dir, tc.file)
		}
	}

	for _, file := range []string{"README.md", "rubric.md"} {
		data, err := readRepoFile(filepath.Join("packets", file))
		if err != nil {
			t.Fatalf("read packet document %s: %v", file, err)
		}
		if err := quality.CheckNoBlindingLeaks(data, artifacts.IdentityKey); err != nil {
			t.Fatalf("packet document %s leaked blinding info: %v", file, err)
		}
	}

	// Verify seeded order is shuffled relative to source case order.
	origOrder := make([]string, 0, len(suite.EvaluationCases))
	packetOrder := make([]string, 0, len(artifacts.IdentityKey.Entries))
	for _, c := range suite.EvaluationCases {
		origOrder = append(origOrder, c.ID)
	}
	for _, e := range artifacts.IdentityKey.Entries {
		packetOrder = append(packetOrder, e.CaseID)
	}
	if slices.Equal(origOrder, packetOrder) {
		t.Fatal("seeded packet order matched unshuffled input order")
	}
}

func TestBlindingAuditRejectsPrivateAnnotationNames(t *testing.T) {
	key, err := quality.ReadStrictJSONFile[quality.IdentityKey]("testdata", "identity_key.json")
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(key.Entries, func(entry quality.IdentityEntry) bool {
		return entry.CaseID == "EVAL-U1-05"
	})
	if index < 0 {
		t.Fatal("identity key has no seeded prerequisite case")
	}
	entry := &key.Entries[index]
	if len(entry.ScenarioTags) == 0 {
		t.Fatal("seeded prerequisite case has no scenario tags")
	}
	entry.Provider = "Research & Learning"

	data, err := readRepoFile("packets/rater_a_blank.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := quality.CheckNoBlindingLeaks(data, key); err != nil {
		t.Fatalf("unmodified rating sheet: %v", err)
	}
	var sheet map[string]json.RawMessage
	if err := json.Unmarshal(data, &sheet); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(sheet["rows"], &rows); err != nil {
		t.Fatal(err)
	}
	if index >= len(rows) {
		t.Fatalf("rating sheet has no row at index %d", index)
	}
	row := rows[index]
	var responseID string
	if err := json.Unmarshal(row["response_id"], &responseID); err != nil || responseID != entry.ResponseID {
		t.Fatalf("rating row does not match identity key: %q, err=%v", responseID, err)
	}

	fields := append([]string{entry.CaseID, "review by " + strings.ToLower(entry.Provider)}, entry.ScenarioTags...)
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			row[field] = json.RawMessage("true")
			defer delete(row, field)
			annotatedRows, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			sheet["rows"] = annotatedRows
			annotatedSheet, err := json.Marshal(sheet)
			if err != nil {
				t.Fatal(err)
			}
			if err := quality.CheckNoBlindingLeaks(annotatedSheet, key); !errors.Is(err, quality.ErrValidation) {
				t.Fatalf("annotation field %q returned %v", field, err)
			}
		})
	}
}

func TestDocumentationAndSchemaParity(t *testing.T) {
	readme, err := readRepoFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	doc := string(readme)
	requiredStrings := []string{
		"https://apcentral.collegeboard.org/media/pdf/ap-computer-science-a-course-and-exam-description.pdf",
		"https://apcentral.collegeboard.org/media/pdf/ap-computer-science-a-course-and-exam-description-clarification.pdf",
		"provisional_educator_review_pending",
		quality.NamedAdjudicatorRole,
		"0.90",
		"0.70",
		"10",
		"Wilson",
		"bootstrap",
		"inconclusive",
		"go test -race -count=1 ./evaluation/quality/...",
	}
	for _, needle := range requiredStrings {
		if !strings.Contains(doc, needle) {
			t.Fatalf("README.md missing required benchmark specification string %q", needle)
		}
	}
	rubric, err := readRepoFile(filepath.Join("packets", "rubric.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dim := range quality.AnchoredRubric() {
		if !bytes.Contains(rubric, []byte(dim.Name)) {
			t.Fatalf("rubric.md missing rubric dimension %q", dim.Name)
		}
		if len(dim.Anchors) != 3 {
			t.Fatalf("dimension %q expected 3 anchors (0,1,2), got %d", dim.Name, len(dim.Anchors))
		}
	}
}

func readRepoFile(relPath string) ([]byte, error) {
	root, err := os.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(relPath)
}

func TestBlindPacketCourseEvidence(t *testing.T) {
	_, facts, suite, _, _ := loadAllFixtures(t)
	data, err := readRepoFile("packets/rater_a_blank.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, cue := range []string{"missing evidence:", "ambiguous prompt:", "/offtopic/", "output tests exact service length bounds", "response returns empty resources"} {
		if strings.Contains(strings.ToLower(string(data)), cue) {
			t.Errorf("designated packet contains a scoring cue %q", cue)
		}
		if err := quality.CheckNoBlindingLeaks([]byte(cue), quality.IdentityKey{}); !errors.Is(err, quality.ErrValidation) {
			t.Errorf("blinding check accepted known scoring cue %q", cue)
		}
	}
	if !bytes.Contains(data, []byte(`"resource_evidence"`)) || !bytes.Contains(data, []byte(`"reference_locators"`)) {
		t.Error("designated packet omits offline resource evidence or reference locators")
	}
	if bytes.Contains(data, []byte("Check overflow cases xxxxxxxxx")) {
		t.Error("service-limit explanation exposes fixture padding")
	}
	for _, tc := range []struct{ fact, statement, page string }{
		{"FACT-U1-04", "positive", "PDF p. 51"},
		{"FACT-U4-07", "catch", "PDF p. 113"},
	} {
		f := facts[tc.fact]
		located := false
		for _, ref := range f.References {
			located = located || strings.Contains(ref.PageLocator, tc.page)
		}
		if !strings.Contains(f.Statement, tc.statement) || !located {
			t.Errorf("%s lacks the required qualification or source page", tc.fact)
		}
	}
	for _, c := range suite.EvaluationCases {
		for _, e := range c.ResourceEvidence {
			if strings.TrimSpace(e.Excerpt) == "" {
				t.Errorf("designated fixture %s lacks an offline excerpt for resource (%d, %d)", c.ID, e.ItemIndex, e.ResourceIndex)
			}
		}
	}
}

func TestPacketBindsMaterialAndRequiresOfflineEvidence(t *testing.T) {
	cat, facts, suite, seed, example := loadAllFixtures(t)
	original, err := quality.BuildBlindArtifacts(suite, facts)
	if err != nil {
		t.Fatal(err)
	}
	c := &suite.EvaluationCases[0]
	c.CandidateResponse.Items[0].Explanation += " Changed."
	changed, err := quality.BuildBlindArtifacts(suite, facts)
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range original.IdentityKey.Entries {
		newEntry := changed.IdentityKey.Entries[i]
		wantChange := entry.CaseID == c.ID
		if (entry.ResponseID != newEntry.ResponseID) != wantChange || (entry.MaterialSHA256 != newEntry.MaterialSHA256) != wantChange {
			t.Fatalf("content binding mismatch for %s", entry.CaseID)
		}
	}
	c.ResourceEvidence = nil
	if _, err := quality.BuildBlindArtifacts(suite, facts); !errors.Is(err, quality.ErrValidation) {
		t.Fatalf("packet with missing resource evidence returned %v", err)
	}
	_, _, suite, _, _ = loadAllFixtures(t)
	c = &suite.EvaluationCases[0]
	c.ReferenceFactIDs = []string{"unknown-reference"}
	if _, err := quality.BuildBlindArtifacts(suite, facts); !errors.Is(err, quality.ErrValidation) {
		t.Fatalf("packet with unknown reference returned %v", err)
	}
	_, _, suite, _, _ = loadAllFixtures(t)
	for i, cal := range suite.CalibrationCases {
		for j, e := range cal.ResourceEvidence {
			if e.UnavailableReason == "" {
				continue
			}
			cal.WorkedAnchor.ResourceRatings[j].Rating = quality.RubricScore{Status: quality.ScoreStatusScored, Score: intPtr(2)}
			suite.CalibrationCases[i] = cal
			if err := quality.ValidateSuite(suite, cat, facts, seed, example); !errors.Is(err, quality.ErrValidation) {
				t.Fatalf("scored calibration slot with unavailable content returned %v", err)
			}
			return
		}
	}
	t.Fatal("missing calibration anchor for unavailable resource evidence")
}
