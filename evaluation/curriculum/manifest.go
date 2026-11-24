// Package curriculum evaluates knowledge graphs against versioned course
// objective manifests and generates deterministic coverage reports.
package curriculum

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// APCSAFall2025Edition identifies the Fall 2025 AP Computer Science A
	// course framework combined with the January 2026 clarifications.
	APCSAFall2025Edition = "ap-csa-fall-2025"

	// APCSACourseDescriptionSHA256 pins the College Board Fall 2025 Course and Exam Description PDF.
	APCSACourseDescriptionSHA256 = "c81501ad5a022c83f1ca51119cccd1907b1394e1416d8c2a01bd43a38ab16486"

	// APCSAClarificationsSHA256 pins the College Board January 2026 Clarifications and Corrections PDF.
	APCSAClarificationsSHA256 = "d39626653656bf0baa0127dc8c3fa50c5e7a41bc7b3747e43d997f69a2d18e56"

	// APCSACourseDescriptionURL links to the publisher's Fall 2025 CED PDF.
	APCSACourseDescriptionURL = "https://apcentral.collegeboard.org/media/pdf/ap-computer-science-a-course-and-exam-description.pdf"

	// APCSAClarificationsURL links to the publisher's Clarifications PDF.
	APCSAClarificationsURL = "https://apcentral.collegeboard.org/media/pdf/ap-computer-science-a-course-and-exam-description-clarification.pdf"
)

// SourceDocument identifies an authoritative curriculum source pinned by SHA-256 digest.
type SourceDocument struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Publisher string `json:"publisher"`
	URL       string `json:"url"`
	Revision  string `json:"revision"`
	SHA256    string `json:"sha256"`
}

// Objective represents one distinct required learning objective in a course framework.
type Objective struct {
	ID            string `json:"id"`
	Unit          int    `json:"unit"`
	UnitTitle     string `json:"unit_title"`
	Topic         string `json:"topic"`
	TopicTitle    string `json:"topic_title"`
	SourceID      string `json:"source_id"`
	SourcePage    int    `json:"source_page"`
	SourceEndPage int    `json:"source_end_page,omitempty"`
	Summary       string `json:"summary"`
}

// Manifest defines a versioned, graph-independent set of course learning objectives.
type Manifest struct {
	SchemaVersion          string           `json:"schema_version"`
	Edition                string           `json:"edition"`
	Framework              string           `json:"framework"`
	EffectiveTerm          string           `json:"effective_term"`
	ClarificationsRevision string           `json:"clarifications_revision,omitempty"`
	Language               string           `json:"language"`
	ProgrammingLanguage    string           `json:"programming_language"`
	PageReference          string           `json:"page_reference"`
	RedistributionNote     string           `json:"redistribution_note"`
	Denominator            int              `json:"denominator"`
	ExpectedUnitCounts     map[int]int      `json:"expected_unit_counts"`
	Sources                []SourceDocument `json:"sources"`
	Objectives             []Objective      `json:"objectives"`
}

func readCleanFile(path string) ([]byte, error) {
	return os.ReadFile(filepath.Clean(path)) //nolint:gosec // Evaluation helpers read explicit caller-supplied fixture paths.
}

func decodeStrictJSON[T any](r io.Reader) (T, error) {
	var out T
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("decode JSON: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return out, errors.New("trailing JSON data")
		}
		return out, fmt.Errorf("trailing JSON data: %w", err)
	}
	return out, nil
}

func decodeStrictBytes[T any](data []byte) (T, error) {
	return decodeStrictJSON[T](bytes.NewReader(data))
}

func validSHA256Hex(digest string) bool {
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func validHTTPSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// LoadManifest reads, decodes, and validates a course objective manifest file.
func LoadManifest(path string) (Manifest, error) {
	data, err := readCleanFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	return decodeAndValidateManifest(data)
}

// ParseManifest decodes and validates a course objective manifest from JSON.
func ParseManifest(r io.Reader) (Manifest, error) {
	m, err := decodeStrictJSON[Manifest](r)
	if err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifest(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func decodeAndValidateManifest(data []byte) (Manifest, error) {
	m, err := decodeStrictBytes[Manifest](data)
	if err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifest(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ValidateManifest checks manifest invariants, pinned source digests, and denominator reconciliation.
func ValidateManifest(m Manifest) error {
	if strings.TrimSpace(m.SchemaVersion) == "" {
		return errors.New("manifest schema_version is required")
	}
	if strings.TrimSpace(m.Edition) == "" {
		return errors.New("manifest edition is required")
	}
	if strings.TrimSpace(m.Framework) == "" {
		return errors.New("manifest framework is required")
	}
	if strings.TrimSpace(m.EffectiveTerm) == "" {
		return errors.New("manifest effective_term is required")
	}
	if strings.TrimSpace(m.Language) == "" || strings.TrimSpace(m.ProgrammingLanguage) == "" {
		return errors.New("manifest language and programming_language are required")
	}
	if m.Denominator <= 0 {
		return fmt.Errorf("manifest denominator must be positive, got %d", m.Denominator)
	}
	sourceIDs, err := validateSources(m)
	if err != nil {
		return err
	}
	unitCounts, err := validateObjectives(m, sourceIDs)
	if err != nil {
		return err
	}
	if len(m.ExpectedUnitCounts) == 0 {
		return errors.New("manifest expected_unit_counts is required")
	}
	expectedTotal := 0
	for _, unit := range slices.Sorted(maps.Keys(m.ExpectedUnitCounts)) {
		want := m.ExpectedUnitCounts[unit]
		expectedTotal += want
		if unitCounts[unit] != want {
			return fmt.Errorf("unit %d objective count = %d, expected %d", unit, unitCounts[unit], want)
		}
	}
	for unit, got := range unitCounts {
		if m.ExpectedUnitCounts[unit] != got {
			return fmt.Errorf("unexpected unit %d with %d objectives", unit, got)
		}
	}
	if expectedTotal != m.Denominator {
		return fmt.Errorf("expected_unit_counts sum = %d, denominator = %d", expectedTotal, m.Denominator)
	}
	return nil
}

func validateSources(m Manifest) (map[string]SourceDocument, error) {
	if len(m.Sources) == 0 {
		return nil, errors.New("manifest must pin at least one source document")
	}
	byID := make(map[string]SourceDocument, len(m.Sources))
	for _, src := range m.Sources {
		if strings.TrimSpace(src.ID) == "" {
			return nil, errors.New("source document id is required")
		}
		if _, dup := byID[src.ID]; dup {
			return nil, fmt.Errorf("duplicate source id %q", src.ID)
		}
		if strings.TrimSpace(src.Title) == "" || strings.TrimSpace(src.Publisher) == "" || strings.TrimSpace(src.Revision) == "" {
			return nil, fmt.Errorf("source %q requires title, publisher, and revision", src.ID)
		}
		if !validHTTPSURL(src.URL) {
			return nil, fmt.Errorf("source %q has invalid HTTPS URL %q", src.ID, src.URL)
		}
		if !validSHA256Hex(src.SHA256) {
			return nil, fmt.Errorf("source %q has invalid SHA-256 digest %q", src.ID, src.SHA256)
		}
		byID[src.ID] = src
	}
	if m.Edition == APCSAFall2025Edition {
		ced, ok := byID["ced"]
		if !ok || ced.SHA256 != APCSACourseDescriptionSHA256 || ced.URL != APCSACourseDescriptionURL {
			return nil, errors.New("ap-csa-fall-2025 manifest must pin the official Fall 2025 CED URL and SHA-256")
		}
		clar, ok := byID["clarifications"]
		if !ok || clar.SHA256 != APCSAClarificationsSHA256 || clar.URL != APCSAClarificationsURL {
			return nil, errors.New("ap-csa-fall-2025 manifest must pin the official January 2026 Clarifications URL and SHA-256")
		}
	}
	return byID, nil
}

func validateObjectives(m Manifest, sourceIDs map[string]SourceDocument) (map[int]int, error) {
	if len(m.Objectives) != m.Denominator {
		return nil, fmt.Errorf("manifest objective count = %d, denominator = %d", len(m.Objectives), m.Denominator)
	}
	seenIDs := make(map[string]bool, len(m.Objectives))
	unitCounts := make(map[int]int)
	for _, obj := range m.Objectives {
		if strings.TrimSpace(obj.ID) == "" {
			return nil, errors.New("objective id is required")
		}
		if seenIDs[obj.ID] {
			return nil, fmt.Errorf("duplicate objective id %q in manifest", obj.ID)
		}
		seenIDs[obj.ID] = true
		if obj.Unit <= 0 || strings.TrimSpace(obj.UnitTitle) == "" {
			return nil, fmt.Errorf("objective %q requires positive unit and unit_title", obj.ID)
		}
		if strings.TrimSpace(obj.Topic) == "" || strings.TrimSpace(obj.TopicTitle) == "" {
			return nil, fmt.Errorf("objective %q requires topic and topic_title", obj.ID)
		}
		if _, ok := sourceIDs[obj.SourceID]; !ok {
			return nil, fmt.Errorf("objective %q references unknown source_id %q", obj.ID, obj.SourceID)
		}
		if obj.SourcePage < 1 || (obj.SourceEndPage != 0 && obj.SourceEndPage < obj.SourcePage) {
			return nil, fmt.Errorf("objective %q has invalid page range %d..%d", obj.ID, obj.SourcePage, obj.SourceEndPage)
		}
		if strings.TrimSpace(obj.Summary) == "" {
			return nil, fmt.Errorf("objective %q requires a non-empty summary", obj.ID)
		}
		unitCounts[obj.Unit]++
	}
	return unitCounts, nil
}
