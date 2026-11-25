package graph

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type interruptedCurriculum struct{}

func (interruptedCurriculum) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestGraphReloadSnapshotHashesFullStream(t *testing.T) {
	const data = `[{"subject":"Lesson","relation":"depends_on","object":"Basics"}]`
	input := strings.Repeat(" ", 805) + data + strings.Repeat(" \r\n\t", 2048)
	initial, err := decodeSnapshot(t.Context(), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256([]byte(input))); initial.Revision() != want {
		t.Fatalf("revision=%q want %q", initial.Revision(), want)
	}
	if next, err := decodeSnapshot(t.Context(), io.MultiReader(strings.NewReader(input), interruptedCurriculum{})); next != nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("incomplete stream yielded snapshot=%v err=%v", next, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if next, err := decodeSnapshot(ctx, strings.NewReader(input)); next != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stream yielded snapshot=%v err=%v", next, err)
	}
	curriculum := NewCurriculum(initial)
	identical, err := decodeSnapshot(t.Context(), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if curriculum.Publish(identical) || curriculum.Current() != initial {
		t.Fatal("identical bytes changed the snapshot")
	}
	equivalent, err := decodeSnapshot(t.Context(), strings.NewReader(input+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !curriculum.Publish(equivalent) || curriculum.Current() != equivalent || !equivalent.Graph().NodeExists("Basics") {
		t.Fatal("changed source bytes did not publish a new complete revision")
	}
}
