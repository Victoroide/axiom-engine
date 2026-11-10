package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestStandaloneOutputs(t *testing.T) {
	for _, name := range []string{"request-path", "system-components", "seed-graph"} {
		for _, theme := range []string{"light", "dark"} {
			p := filepath.Join("..", "..", "docs", "assets", "diagrams", name+"-"+theme+".svg")
			data, err := readWithin("../..", strings.TrimPrefix(p, "../../"))
			if err != nil {
				t.Error(err)
				continue
			}
			for _, s := range []string{`viewBox=`, `role="img"`, `<title>`, `<desc>`, `<defs>`, `<use `, `@keyframes`, `prefers-reduced-motion`} {
				if !bytes.Contains(data, []byte(s)) {
					t.Errorf("%s missing %s", p, s)
				}
			}
			for _, s := range []string{`<text`, `<script`, `foreignObject`, `<image`, `@font-face`, `<animate`, `filter=`, `<linearGradient`} {
				if bytes.Contains(data, []byte(s)) {
					t.Errorf("%s contains %s", p, s)
				}
			}
			if len(data) > 250000 {
				t.Errorf("%s: %d bytes", p, len(data))
			}
		}
	}
}

func TestCheckRejectsStaleOutput(t *testing.T) {
	root := diagramInputs(t)
	if err := generate(root, false); err != nil {
		t.Fatal(err)
	}
	if err := generate(root, true); err != nil {
		t.Fatal(err)
	}
	if err := writeWithin(root, "docs/assets/diagrams/request-path-light.svg", []byte("stale")); err != nil {
		t.Fatal(err)
	}
	if err := generate(root, true); err == nil || !strings.Contains(err.Error(), "stale diagram") {
		t.Fatalf("stale output: %v", err)
	}
	if err := generate(root, false); err != nil {
		t.Fatal(err)
	}
	if err := generate(root, true); err != nil {
		t.Fatal(err)
	}
}

func TestAlternativeTextMatchesDescription(t *testing.T) {
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{"request-path": "README.md", "system-components": "docs/architecture.md", "seed-graph": "docs/knowledge-graph.md"}
	for _, d := range ds {
		doc, err := readWithin("../..", docs[d.Name])
		if err != nil {
			t.Fatal(err)
		}
		for _, theme := range []string{"light", "dark"} {
			data, err := readWithin("../..", "docs/assets/diagrams/"+d.Name+"-"+theme+".svg")
			if err != nil {
				t.Fatal(err)
			}
			var svg struct {
				Description string `xml:"desc"`
			}
			if err := xml.Unmarshal(data, &svg); err != nil {
				t.Fatal(err)
			}
			if svg.Description != d.Description {
				t.Fatal("SVG description differs from scene")
			}
			if err := checkEmbedding(doc, d.Name, svg.Description); err != nil {
				t.Errorf("%s: %v", docs[d.Name], err)
			}
		}
	}
}
func checkEmbedding(data []byte, name, description string) error {
	tags := regexp.MustCompile(`<img\s[^>]*>`).FindAll(data, -1)
	for _, tag := range tags {
		if !bytes.Contains(tag, []byte(name+"-light.svg")) {
			continue
		}
		alt := regexp.MustCompile(`alt="([^"]*)"`).FindSubmatch(tag)
		if len(alt) != 2 || html.UnescapeString(string(alt[1])) != description {
			return fmt.Errorf("alternative text differs from SVG description")
		}
		if bytes.Contains(tag, []byte(`width="100%"`)) {
			return fmt.Errorf("image must use natural width")
		}
		if !bytes.Contains(data, []byte("<picture>")) || !bytes.Contains(data, []byte(name+"-dark.svg")) {
			return fmt.Errorf("missing themed picture")
		}
		return nil
	}
	return fmt.Errorf("missing embedded image %s", name)
}
func TestRejectEmbeddingDefects(t *testing.T) {
	for _, input := range []string{`<picture><source srcset="sample-dark.svg"><img src="sample-light.svg" alt="Wrong"></picture>`, `<picture><source srcset="sample-dark.svg"><img src="sample-light.svg" width="100%" alt="Description"></picture>`, `<img src="sample-light.svg" alt="Description">`} {
		if err := checkEmbedding([]byte(input), "sample", "Description"); err == nil {
			t.Fatal("invalid embedding accepted")
		}
	}
}

func TestPreferredSizeBudget(t *testing.T) {
	matches, err := filepath.Glob("../../docs/assets/diagrams/*.svg")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, path := range matches {
		b, err := readWithin("../..", strings.TrimPrefix(path, "../../"))
		if err != nil {
			t.Fatal(err)
		}
		total += len(b)
		if len(b) >= 250000 {
			t.Errorf("%s exceeds 250 KB limit: %d", path, len(b))
		}
	}
	if total >= 600000 {
		t.Errorf("combined size %d exceeds 600 KB", total)
	}
}

func TestSVGReferencesAreInternalAndUnique(t *testing.T) {
	files, err := filepath.Glob("../../docs/assets/diagrams/*.svg")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		b, err := readWithin("../..", strings.TrimPrefix(file, "../../"))
		if err != nil {
			t.Fatal(err)
		}
		decoder := xml.NewDecoder(strings.NewReader(string(b)))
		ids := map[string]bool{}
		var refs []string
		for {
			token, err := decoder.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			start, ok := token.(xml.StartElement)
			if !ok {
				continue
			}
			for _, attr := range start.Attr {
				if attr.Name.Local == "id" {
					if ids[attr.Value] {
						t.Fatalf("duplicate SVG id %s", attr.Value)
					}
					ids[attr.Value] = true
				}
				if attr.Name.Local == "href" {
					if !strings.HasPrefix(attr.Value, "#") {
						t.Fatalf("external reference %s", attr.Value)
					}
					refs = append(refs, strings.TrimPrefix(attr.Value, "#"))
				}
			}
		}
		for _, ref := range refs {
			if !ids[ref] {
				t.Fatalf("unresolved glyph %s in %s", ref, file)
			}
		}
	}
}

func readWithin(root, path string) (data []byte, err error) {
	fs, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, fs.Close()) }()
	return fs.ReadFile(path)
}
func writeWithin(root, path string, data []byte) (err error) {
	fs, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, fs.Close()) }()
	return fs.WriteFile(path, data, 0o600)
}
