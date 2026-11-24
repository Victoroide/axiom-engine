package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type tarMember struct {
	name, contents, link string
	typeflag             byte
}

func fixtureTar(t *testing.T, members []tarMember) []byte {
	t.Helper()
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	for _, member := range members {
		h := &tar.Header{Name: member.name, Typeflag: member.typeflag, Linkname: member.link, Size: int64(len(member.contents))}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprint(w, member.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestArchiveExtractionPaths(t *testing.T) {
	const root = "axiom-engine_1.2.3_linux_amd64"
	const native = "native bytes"
	a := asset{name: "libbaml_cffi.so", version: "1.2.3", checksum: fmt.Sprintf("%x", sha256.Sum256([]byte(native))), target: "linux/amd64"}
	bom, err := a.inventory(bytes.NewBufferString(native))
	if err != nil {
		t.Fatal(err)
	}
	files := []tarMember{
		{name: root + "/", typeflag: tar.TypeDir},
		{name: root + "/" + a.name, contents: native, typeflag: tar.TypeReg},
		{name: root + "/" + inventoryName, contents: string(bom), typeflag: tar.TypeReg},
	}
	for _, tt := range []struct {
		name string
		file tarMember
	}{
		{"duplicate", files[1]},
		{"alias", tarMember{name: "./" + files[1].name, contents: "wrong", typeflag: tar.TypeReg}},
		{"case collision", tarMember{name: root + "/LIBBAML_CFFI.SO", contents: "wrong", typeflag: tar.TypeReg}},
		{"parent traversal", tarMember{name: root + "/other/../" + a.name, contents: "wrong", typeflag: tar.TypeReg}},
		{"child under regular file", tarMember{name: files[1].name + "/child", contents: "wrong", typeflag: tar.TypeReg}},
		{"outside root", tarMember{name: "other/file", typeflag: tar.TypeReg}},
		{"symlink", tarMember{name: root + "/alias", typeflag: tar.TypeSymlink, link: a.name}},
		{"hardlink", tarMember{name: root + "/alias", typeflag: tar.TypeLink, link: files[1].name}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			archive := tar.NewReader(bytes.NewReader(fixtureTar(t, append(append([]tarMember{}, files...), tt.file))))
			if _, _, err := a.readArchive(archive, root); err == nil {
				t.Fatal("accepted an ambiguous or unsupported extraction path")
			}
		})
	}
	for i := range files {
		files[i].name = "./" + files[i].name
	}
	files = append([]tarMember{{name: "./", typeflag: tar.TypeDir}}, files...)
	archive := tar.NewReader(bytes.NewReader(fixtureTar(t, files)))
	if _, _, err := a.readArchive(archive, root); err != nil {
		t.Fatalf("a leading ./ should be accepted: %v", err)
	}
}

const fixtureNative = "fixture library bytes"

func testInventoryInputs(t *testing.T) (dockerfile, schema, library string, inventory []byte) {
	t.Helper()
	dir := t.TempDir()
	dockerfile = filepath.Join(dir, "Dockerfile")
	writeFixture(t, dockerfile, fmt.Appendf(nil, "ARG BAML_LIB_VERSION=1.2.3\nARG BAML_LIB_SHA256_AMD64=%x\n", sha256.Sum256([]byte(fixtureNative))))
	library = filepath.Join(dir, "libbaml_cffi.so")
	writeFixture(t, library, []byte(fixtureNative))
	inventory, err := nativeInventory(dockerfile, "linux/amd64", library)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{schemaName, "cryptography-defs.schema.json", "jsf-0.82.schema.json", "spdx.schema.json"} {
		writeFixture(t, filepath.Join(dir, name), []byte(`{"type":"object"}`))
	}
	return dockerfile, dir, library, inventory
}

func TestInventoryVerificationEnforcesSchema(t *testing.T) {
	dockerfile, schema, library, inventory := testInventoryInputs(t)
	inventoryFile := filepath.Join(schema, inventoryName)
	writeFixture(t, inventoryFile, inventory)
	root := "axiom-engine_1.2.3_linux_amd64"
	archiveFile := filepath.Join(schema, root+".tar.gz")
	a, err := pinnedAsset(dockerfile, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	archive := gzipFixture(t, fixtureTar(t, []tarMember{
		{name: root + "/" + a.name, typeflag: tar.TypeReg, contents: fixtureNative},
		{name: root + "/" + inventoryName, typeflag: tar.TypeReg, contents: string(inventory)},
	}))
	writeFixture(t, archiveFile, archive)
	args := []string{"-dockerfile", dockerfile, "-target", "linux/amd64", "-schema-dir", schema, "-archive-sha256", fmt.Sprintf("%x", sha256.Sum256(archive))}
	commands := [][]string{{"verify", library, inventoryFile}, {"verify-archive", archiveFile}}
	for _, command := range commands {
		if err := run(append(slices.Clip(args), command...), io.Discard); err != nil {
			t.Fatalf("%s with matching schema: %v", command[0], err)
		}
	}
	writeFixture(t, filepath.Join(schema, schemaName), []byte(`{"properties":{"bomFormat":{"const":"different format"}}}`))
	for _, command := range commands {
		if err := run(append(slices.Clip(args), command...), io.Discard); err == nil || !strings.Contains(err.Error(), "different format") {
			t.Fatalf("%s did not reject the document against the controlled schema: %v", command[0], err)
		}
	}
}

func gzipFixture(t *testing.T, data []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}
