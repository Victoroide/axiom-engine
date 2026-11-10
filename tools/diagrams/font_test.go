package main

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestBundledFontIntegrity(t *testing.T) {
	hashes := map[string]string{"Inter-Regular.ttf": "40d692fce188e4471e2b3cba937be967878f631ad3ebbbdcd587687c7ebe0c82", "Inter-SemiBold.ttf": "78a843fade9d4612a5567302fb595b56976eb5fcebf4fea5a5912d638bafcde3"}
	for name, want := range hashes {
		b, err := fontFiles.ReadFile("fonts/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != want {
			t.Errorf("%s hash changed: %s", name, got)
		}
	}
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	wide, err := f.measure(label{Text: "WWW", Size: 14})
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := f.measure(label{Text: "iii", Size: 14})
	if err != nil {
		t.Fatal(err)
	}
	if wide.W < 2*narrow.W {
		t.Fatal("glyph advances do not reflect character widths")
	}
}
