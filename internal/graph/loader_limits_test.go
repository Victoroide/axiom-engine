package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

const (
	curriculumByteLimit  = 8 * 1024 * 1024
	curriculumTupleLimit = 50_000
	curriculumNodeLimit  = 20_000
	validTupleJSON       = `{"subject":"A","relation":"depends_on","object":"B"}`
)

type invalidGraphReader struct {
	fragments []string
	reads     int
}

func (r *invalidGraphReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads <= len(r.fragments) {
		return copy(p, r.fragments[r.reads-1]), nil
	}
	return 0, io.EOF
}

func TestDecodeReportsSyntaxErrorAfterShortRead(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fragments []string
	}{
		{"invalid first byte", []string{"oops\n"}},
		{"raw newline in string", []string{`[{"subject":"Example`, "\n"}},
		{"space in partial literal", []string{"[nu", " "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &invalidGraphReader{fragments: tc.fragments}
			g, err := Decode(r)
			var syntax *json.SyntaxError
			if g != nil || !errors.As(err, &syntax) {
				t.Fatalf("expected syntax error, got %v", err)
			}
			if want := len(tc.fragments); r.reads != want {
				t.Fatalf("made %d reads to report a syntax error, want %d", r.reads, want)
			}
		})
	}
}

func TestDecodeByteLimit(t *testing.T) {
	valid := "[" + validTupleJSON + "]"
	whitespace := strings.Repeat(" \t\n\r", curriculumByteLimit/4)
	for _, tc := range []struct {
		name, input, want string
		read              int
	}{
		{"leading whitespace at limit", whitespace[:curriculumByteLimit-len(valid)] + valid, "", curriculumByteLimit},
		{"trailing whitespace at limit", valid + whitespace[:curriculumByteLimit-len(valid)], "", curriculumByteLimit},
		{"trailing whitespace over limit", valid + whitespace, "8388608 bytes", curriculumByteLimit + 1},
		{"whitespace only over limit", whitespace + "  ", "8388608 bytes", curriculumByteLimit + 1},
		{"trailing value at limit", valid + whitespace[:curriculumByteLimit-len(valid)-1] + "0", "graph must contain exactly one JSON array", curriculumByteLimit},
		{"trailing value over limit", valid + whitespace[:curriculumByteLimit-len(valid)] + "0", "8388608 bytes", curriculumByteLimit + 1},
		{"closing delimiter over limit", valid[:len(valid)-1] + whitespace[:curriculumByteLimit-len(valid)+1] + "]", "8388608 bytes", curriculumByteLimit + 1},
		{"empty input", "", "decode graph:", 0},
		{"empty array", "[ ]", "graph must contain at least one tuple", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader(tc.input)
			g, err := Decode(r)
			if tc.want == "" {
				if err != nil || len(g.All()) != 1 {
					t.Fatalf("expected one tuple, got %v", err)
				}
			} else if g != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected rejection containing %q, got %v", tc.want, err)
			}
			if n := len(tc.input) - r.Len(); n != tc.read {
				t.Errorf("read %d bytes, want %d", n, tc.read)
			}
		})
	}
	t.Run("fragmented leading whitespace", func(t *testing.T) {
		input := strings.NewReader(whitespace[:curriculumByteLimit-len(valid)] + valid)
		g, err := Decode(iotest.OneByteReader(input))
		if err != nil || len(g.All()) != 1 || input.Len() != 0 {
			t.Fatalf("expected one tuple and no remaining bytes, got %v with %d bytes left", err, input.Len())
		}
	})
}

func TestDecodeTupleLimitCountsDuplicates(t *testing.T) {
	exact := "[" + strings.Repeat(validTupleJSON+",", curriculumTupleLimit-1) + validTupleJSON
	g, err := Decode(strings.NewReader(exact + "]"))
	if err != nil || len(g.All()) != 1 {
		t.Fatalf("expected one distinct tuple at the input limit, got %v", err)
	}
	g, err = Decode(strings.NewReader(exact + "," + validTupleJSON + "]"))
	if g != nil || err == nil || !strings.Contains(err.Error(), "50000 input tuples") {
		t.Fatalf("expected tuple limit rejection, got %v", err)
	}
}

func TestDecodeStopsAtInvalidTuple(t *testing.T) {
	input := `[{"subject":"A","relation":"nope","object":"B"},` + strings.Repeat(validTupleJSON+",", curriculumTupleLimit) + validTupleJSON + "]"
	r := strings.NewReader(input)
	g, err := Decode(r)
	if g != nil || err == nil || err.Error() != `tuple 0 has unsupported relation "nope"` {
		t.Fatalf("expected first tuple rejection, got %v", err)
	}
	if n := len(input) - r.Len(); n > 4096 {
		t.Fatalf("read %d bytes after finding an invalid first tuple", n)
	}
}

func TestDecodeChainAtNodeLimit(t *testing.T) {
	var input strings.Builder
	input.WriteByte('[')
	for i := range curriculumNodeLimit - 1 {
		fmt.Fprintf(&input, `{"subject":"N%05d","relation":"depends_on","object":"N%05d"},`, i, i+1)
	}
	prefix := strings.TrimSuffix(input.String(), ",")
	g, err := Decode(strings.NewReader(prefix + "]"))
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, curriculumNodeLimit)
	for i := range want {
		want[i] = fmt.Sprintf("N%05d", curriculumNodeLimit-1-i)
	}
	if got := g.GetPrerequisiteChain("N00000"); !slices.Equal(got, want[:len(want)-1]) {
		t.Fatalf("unexpected prerequisite chain (length %d)", len(got))
	}
	if got := g.Summary().LongestChain; !slices.Equal(got, want) {
		t.Fatalf("unexpected longest chain (length %d)", len(got))
	}
	t.Run("cancels during traversal", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		const cancelAfter = 64
		checks := 0
		chain, err := g.prerequisiteChain("N00000", func() error {
			checks++
			if checks == cancelAfter {
				cancel()
			}
			return ctx.Err()
		})
		if !errors.Is(err, context.Canceled) || chain != nil || checks != cancelAfter {
			t.Fatalf("walk returned %d prerequisites and %v after %d cancellation checks", len(chain), err, checks)
		}
	})
	for _, tc := range []struct{ extra, want string }{
		{`,{"subject":"N19999","relation":"is_a","object":"N20000"}]`, "20000 distinct nodes"},
		{`,{"subject":"N19999","relation":"is_a","object":"N00000"}]`, "graph contains a prerequisite cycle: N00000 -> N00001"},
		{`,{"subject":"N19999","relation":"is_a","object":"n00000"}]`, `canonical node name collision: "N00000" and "n00000"`},
	} {
		if g, err := Decode(strings.NewReader(prefix + tc.extra)); g != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("expected rejection containing %q, got %v", tc.want, err)
		}
	}
}

func TestDecodeWideDAGAtNodeLimit(t *testing.T) {
	var input strings.Builder
	input.WriteByte('[')
	for i := curriculumNodeLimit - 1; i > 0; i-- {
		fmt.Fprintf(&input, `{"subject":"Topic","relation":"is_a","object":"N%05d"},`, i)
	}
	prefix := strings.TrimSuffix(input.String(), ",")
	g, err := Decode(strings.NewReader(prefix + "]"))
	if err != nil {
		t.Fatal(err)
	}
	chain := g.GetPrerequisiteChain("Topic")
	if len(chain) != curriculumNodeLimit-1 {
		t.Fatalf("got %d prerequisites", len(chain))
	}
	for i, name := range chain {
		if want := fmt.Sprintf("N%05d", curriculumNodeLimit-1-i); name != want {
			t.Fatalf("prerequisite %d = %q, want %q", i, name, want)
		}
	}
	if got := g.Summary().LongestChain; !slices.Equal(got, []string{"N00001", "Topic"}) {
		t.Fatalf("unexpected longest chain: %v", got)
	}
	if g, err := Decode(strings.NewReader(prefix + `,{"subject":"Topic","relation":"is_a","object":"Extra"}]`)); g != nil || err == nil || !strings.Contains(err.Error(), "20000 distinct nodes") {
		t.Fatalf("expected node limit rejection, got %v", err)
	}
}
