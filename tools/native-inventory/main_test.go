package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeInventory(t *testing.T) {
	dir := t.TempDir()
	const version = "0.3.7"
	targets := []struct {
		target, pin, asset string
	}{
		{"linux/amd64", "AMD64", "libbaml_cffi-x86_64-unknown-linux-gnu.so"},
		{"linux/arm64", "ARM64", "libbaml_cffi-aarch64-unknown-linux-gnu.so"},
		{"darwin/arm64", "DARWIN_ARM64", "libbaml_cffi-aarch64-apple-darwin.dylib"},
	}
	pins := "ARG BAML_LIB_VERSION=" + version + "\n"
	for _, target := range targets {
		hash := sha256.Sum256([]byte(target.target))
		pins += fmt.Sprintf("ARG BAML_LIB_SHA256_%s=%x\n", target.pin, hash)
	}
	dockerfile := filepath.Join(dir, "Dockerfile")
	writeFixture(t, dockerfile, []byte(pins))
	library := filepath.Join(dir, "native library")
	var otherInventory []byte
	for _, target := range targets {
		t.Run(target.target, func(t *testing.T) {
			writeFixture(t, library, []byte(target.target))
			inventory, err := nativeInventory(dockerfile, target.target, library)
			if err != nil {
				t.Fatal(err)
			}
			again, err := nativeInventory(dockerfile, target.target, library)
			if err != nil || !bytes.Equal(inventory, again) {
				t.Fatalf("second generation differs: %v", err)
			}
			var doc struct {
				BOMFormat, SpecVersion string
				Components             []struct {
					Type, Name, Version string
					Hashes              []struct{ Alg, Content string }
					ExternalReferences  []struct{ Type, URL string }
					Properties          []struct{ Name, Value string }
				}
			}
			if err := json.Unmarshal(inventory, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.BOMFormat != "CycloneDX" || doc.SpecVersion != "1.7" || len(doc.Components) != 1 {
				t.Fatalf("unexpected BOM: %s", inventory)
			}
			component := doc.Components[0]
			if component.Type != "library" || component.Name != target.asset || component.Version != version ||
				len(component.Hashes) != 1 || component.Hashes[0].Alg != "SHA-256" || component.Hashes[0].Content != fmt.Sprintf("%x", sha256.Sum256([]byte(target.target))) ||
				len(component.ExternalReferences) != 1 || component.ExternalReferences[0].Type != "distribution" || component.ExternalReferences[0].URL != "https://github.com/boundaryml/baml/releases/download/"+version+"/"+target.asset ||
				len(component.Properties) != 1 || component.Properties[0].Name != "axiom-engine:target" || component.Properties[0].Value != target.target {
				t.Fatalf("incorrect inventory for %s: %s", target.target, inventory)
			}
			if err := verifyInventory(inventory, inventory); err != nil {
				t.Fatalf("matching inventory was rejected: %v", err)
			}
			wrongVersion := bytes.Replace(inventory, []byte(`"version": "`+version+`"`), []byte(`"version": "0.0.1"`), 1)
			if err := verifyInventory(inventory, wrongVersion); err == nil {
				t.Fatal("accepted a different component version")
			}
			if otherInventory != nil {
				if err := verifyInventory(inventory, otherInventory); err == nil {
					t.Fatal("accepted an inventory from another target")
				}
			}
			otherInventory = inventory
			wrongTarget := "linux/arm64"
			if target.target == wrongTarget {
				wrongTarget = "linux/amd64"
			}
			if _, err := nativeInventory(dockerfile, wrongTarget, library); err == nil || !strings.Contains(err.Error(), "SHA-256") {
				t.Fatalf("wrong target did not fail its pin check: %v", err)
			}
			writeFixture(t, library, []byte(target.target+"tampered"))
			if _, err := nativeInventory(dockerfile, target.target, library); err == nil || !strings.Contains(err.Error(), "SHA-256") {
				t.Fatalf("tampered library did not fail its pin check: %v", err)
			}
		})
	}
	if _, err := nativeInventory(dockerfile, "darwin/amd64", library); err == nil || !strings.Contains(err.Error(), "unsupported release target") {
		t.Fatalf("accepted an unpublished target: %v", err)
	}
}

func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
