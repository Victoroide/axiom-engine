package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type imageFixture struct {
	root, manifest, config, base v1.Descriptor
	blobs                        map[string][]byte
}

func buildImageFixture(t *testing.T, bom []byte, extra ...[]tarMember) imageFixture {
	t.Helper()
	blobs := make(map[string][]byte)
	add := func(media types.MediaType, data []byte) v1.Descriptor {
		hash, size, err := v1.SHA256(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		blobs[hash.String()] = data
		return v1.Descriptor{MediaType: media, Digest: hash, Size: size}
	}
	layers := make([]v1.Descriptor, 0, 2+len(extra))
	diffIDs := make([]v1.Hash, 0, 2+len(extra))
	for _, members := range append([][]tarMember{
		{{name: "usr/marker", typeflag: tar.TypeReg, contents: "base layer"}},
		{{name: "app/", typeflag: tar.TypeDir}, {name: "app/libbaml_cffi.so", typeflag: tar.TypeReg, contents: fixtureNative},
			{name: "app/" + inventoryName, typeflag: tar.TypeReg, contents: string(bom)}},
	}, extra...) {
		layer := fixtureTar(t, members)
		hash, _, err := v1.SHA256(bytes.NewReader(layer))
		if err != nil {
			t.Fatal(err)
		}
		diffIDs = append(diffIDs, hash)
		layers = append(layers, add(types.OCILayer, gzipFixture(t, layer)))
	}
	config := add(types.OCIConfigJSON, fixtureJSON(t, v1.ConfigFile{Architecture: "amd64", OS: "linux", RootFS: v1.RootFS{Type: "layers", DiffIDs: diffIDs}}))
	manifest := add(types.OCIManifestSchema1, fixtureJSON(t, v1.Manifest{SchemaVersion: 2, MediaType: types.OCIManifestSchema1, Config: config, Layers: layers}))
	manifest.Platform = &v1.Platform{OS: "linux", Architecture: "amd64"}
	root := add(types.OCIImageIndex, fixtureJSON(t, v1.IndexManifest{SchemaVersion: 2, MediaType: types.OCIImageIndex, Manifests: []v1.Descriptor{manifest}}))
	return imageFixture{root: root, manifest: manifest, config: config, base: layers[0], blobs: blobs}
}

func TestOCIExportRequiresReferencedBlobs(t *testing.T) {
	dockerfile, schema, _, bom := testInventoryInputs(t)
	image := buildImageFixture(t, bom)
	for _, tc := range []struct {
		name, want string
		blob       v1.Hash
		change     func([]byte) []byte
	}{
		{name: "complete"},
		{name: "missing base layer", blob: image.base.Digest, change: func([]byte) []byte { return nil }, want: "missing OCI blob"},
		{name: "missing config", blob: image.config.Digest, change: func([]byte) []byte { return nil }, want: "missing OCI blob"},
		{name: "missing manifest", blob: image.manifest.Digest, change: func([]byte) []byte { return nil }, want: "missing OCI blob"},
		{name: "extra bytes", blob: image.config.Digest, change: func(data []byte) []byte { return append(slices.Clone(data), ' ') }, want: "size"},
		{name: "wrong digest", blob: image.config.Digest, change: func(data []byte) []byte { out := slices.Clone(data); out[0] = ' '; return out }, want: "digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			archive := image.export(t, tc.blob, tc.change)
			err := run([]string{"-dockerfile", dockerfile, "-target", "linux/amd64", "-schema-dir", schema, "-oci-digest", image.root.Digest.String(), "verify-oci", archive}, &stdout)
			if tc.want == "" {
				if err != nil || strings.TrimSpace(stdout.String()) != image.manifest.Digest.String() {
					t.Fatalf("complete export: %v, manifest=%q", err, stdout.String())
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
	writeFixture(t, filepath.Join(schema, schemaName), []byte(`{"properties":{"bomFormat":{"const":"different format"}}}`))
	if err := run([]string{"-dockerfile", dockerfile, "-target", "linux/amd64", "-schema-dir", schema, "-oci-digest", image.root.Digest.String(), "verify-oci", image.export(t, v1.Hash{}, nil)}, io.Discard); err == nil || !strings.Contains(err.Error(), "different format") {
		t.Fatalf("OCI verification did not enforce the schema: %v", err)
	}
}

func TestOCIUsesEffectiveFilesystem(t *testing.T) {
	dockerfile, schema, _, bom := testInventoryInputs(t)
	for _, tc := range []struct {
		name, path, data, want string
	}{
		{"whiteout", "app/.wh.libbaml_cffi.so", "", "missing"},
		{"opaque app", "app/.wh..wh..opq", "", "missing"},
		{"opaque root", ".wh..wh..opq", "", "missing"},
		{"replacement", "app/libbaml_cffi.so", "other library", "SHA-256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := buildImageFixture(t, bom, []tarMember{{name: tc.path, typeflag: tar.TypeReg, contents: tc.data}})
			err := run([]string{"-dockerfile", dockerfile, "-target", "linux/amd64", "-schema-dir", schema, "-oci-digest", image.root.Digest.String(), "verify-oci", image.export(t, v1.Hash{}, nil)}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestRegistryRequiresPushedBlobs(t *testing.T) {
	dockerfile, schema, _, bom := testInventoryInputs(t)
	image := buildImageFixture(t, bom)
	for _, omit := range []string{"", image.base.Digest.String()} {
		registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v2/" {
				return
			}
			digest := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			data, ok := image.blobs[digest]
			if !ok || digest == omit {
				http.NotFound(w, r)
				return
			}
			switch digest {
			case image.root.Digest.String():
				w.Header().Set("Content-Type", string(types.OCIImageIndex))
			case image.manifest.Digest.String():
				w.Header().Set("Content-Type", string(types.OCIManifestSchema1))
			}
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			if r.Method != http.MethodHead {
				_, _ = w.Write(data)
			}
		}))
		var stdout bytes.Buffer
		err := run([]string{"-dockerfile", dockerfile, "-target", "linux/amd64", "-schema-dir", schema, "-oci-digest", image.root.Digest.String(), "verify-registry", strings.TrimPrefix(registry.URL, "http://") + "/runtime"}, &stdout)
		registry.Close()
		if omit == "" && (err != nil || strings.TrimSpace(stdout.String()) != image.manifest.Digest.String()) {
			t.Fatalf("complete pushed image: %v, manifest=%q", err, stdout.String())
		}
		if omit != "" && (err == nil || !strings.Contains(err.Error(), "404")) {
			t.Fatalf("accepted a missing registry blob: %v", err)
		}
	}
}

func (image imageFixture) export(t *testing.T, changed v1.Hash, change func([]byte) []byte) string {
	t.Helper()
	members := []tarMember{{name: "index.json", typeflag: tar.TypeReg, contents: string(fixtureJSON(t, v1.IndexManifest{SchemaVersion: 2, Manifests: []v1.Descriptor{image.root}}))}}
	for digest, data := range image.blobs {
		if digest == changed.String() && change != nil {
			data = change(data)
		}
		if data != nil {
			members = append(members, tarMember{name: "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:"), typeflag: tar.TypeReg, contents: string(data)})
		}
	}
	archive := filepath.Join(t.TempDir(), "image.tar")
	writeFixture(t, archive, fixtureTar(t, members))
	return archive
}

func fixtureJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
