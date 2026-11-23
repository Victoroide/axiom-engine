package graph

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkDecodeAtLimits(b *testing.B) {
	var fixture strings.Builder
	fixture.Grow(curriculumByteLimit)
	fixture.WriteByte('[')
	count := 0
	for distance := 1; count < curriculumTupleLimit; distance++ {
		for i := 0; i+distance < curriculumNodeLimit && count < curriculumTupleLimit; i++ {
			if count > 0 {
				fixture.WriteByte(',')
			}
			fmt.Fprintf(&fixture, `{"subject":"Topic %05d curriculum prerequisite concept","relation":"depends_on","object":"Topic %05d curriculum prerequisite concept"}`, i, i+distance)
			count++
		}
	}
	fixture.WriteByte(']')
	fixture.WriteString(strings.Repeat(" ", curriculumByteLimit-fixture.Len()))
	data := fixture.String()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Decode(strings.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
