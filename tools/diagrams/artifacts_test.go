package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccessibleVectorAssets(t *testing.T) {
	for _, name := range []string{"request-path", "system-components", "seed-graph"} {
		for _, theme := range []string{"light", "dark"} {
			t.Run(name+"/"+theme, func(t *testing.T) {
				data, err := readFile(filepath.Join("..", "..", "docs", "assets", "diagrams", name+"-"+theme+".svg"))
				if err != nil {
					t.Fatal(err)
				}
				if len(data) >= 80000 {
					t.Fatalf("SVG is %d bytes", len(data))
				}
				dec := xml.NewDecoder(bytes.NewReader(data))
				seen := map[string]int{}
				for {
					tok, err := dec.Token()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					el, ok := tok.(xml.StartElement)
					if !ok {
						continue
					}
					seen[el.Name.Local]++
					switch el.Name.Local {
					case "script", "image", "foreignObject", "text", "filter", "linearGradient", "radialGradient":
						t.Errorf("unexpected %s", el.Name.Local)
					}
					for _, a := range el.Attr {
						if a.Name.Local == "href" && !strings.HasPrefix(a.Value, "#") {
							t.Errorf("external reference %q", a.Value)
						}
						if el.Name.Local == "svg" && a.Name.Local == "role" && a.Value == "img" {
							seen["accessible"]++
						}
					}
				}
				for _, required := range []string{"title", "desc", "accessible", "defs", "use", "style"} {
					if seen[required] == 0 {
						t.Errorf("missing %s", required)
					}
				}
				if !bytes.Contains(data, []byte("prefers-reduced-motion:reduce")) {
					t.Error("missing reduced motion rule")
				}
			})
		}
	}
}
