// Command native-inventory describes and verifies the pinned native library in a release.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	inventoryName = "baml-native.cdx.json"
	schemaName    = "bom-1.7.schema.json"
	schemaBase    = "http://cyclonedx.org/schema/"
)

var versionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]*$`)

type asset struct {
	name, version, checksum, target string
}

func pinnedAsset(dockerfile, target string) (asset, error) {
	var name, pin string
	switch target {
	case "linux/amd64":
		name, pin = "libbaml_cffi-x86_64-unknown-linux-gnu.so", "AMD64"
	case "linux/arm64":
		name, pin = "libbaml_cffi-aarch64-unknown-linux-gnu.so", "ARM64"
	case "darwin/arm64":
		name, pin = "libbaml_cffi-aarch64-apple-darwin.dylib", "DARWIN_ARM64"
	default:
		return asset{}, fmt.Errorf("unsupported release target %q", target)
	}
	data, err := os.ReadFile(dockerfile) //nolint:gosec // The caller selects a local Dockerfile.
	if err != nil {
		return asset{}, err
	}
	pins := make(map[string]string)
	for line := range strings.Lines(string(data)) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			pins[key] = value
		}
	}
	version := pins["ARG BAML_LIB_VERSION"]
	if !versionPattern.MatchString(version) {
		return asset{}, errors.New("invalid pinned BAML version")
	}
	return asset{name: name, version: version, checksum: pins["ARG BAML_LIB_SHA256_"+pin], target: target}, nil
}

func sha256Hex(reader io.Reader) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (a asset) inventory(library io.Reader) ([]byte, error) {
	digest, err := sha256Hex(library)
	if err != nil {
		return nil, err
	}
	if digest != a.checksum {
		return nil, fmt.Errorf("native library SHA-256 for %s: got %s, expected %s", a.target, digest, a.checksum)
	}
	document := map[string]any{
		"$schema":     "https://cyclonedx.org/schema/" + schemaName,
		"bomFormat":   "CycloneDX",
		"specVersion": "1.7",
		"version":     1,
		"components": []map[string]any{{
			"type":    "library",
			"name":    a.name,
			"version": a.version,
			"hashes":  []map[string]string{{"alg": "SHA-256", "content": digest}},
			"externalReferences": []map[string]string{{
				"type": "distribution",
				"url":  "https://github.com/boundaryml/baml/releases/download/" + a.version + "/" + a.name,
			}},
			"properties": []map[string]string{{"name": "axiom-engine:target", "value": a.target}},
		}},
	}
	return json.MarshalIndent(document, "", "  ")
}

func nativeInventory(dockerfile, target, library string) ([]byte, error) {
	a, err := pinnedAsset(dockerfile, target)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(library) //nolint:gosec // The caller selects a local native library.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return a.inventory(file)
}

func verifyInventory(expected, actual []byte) error {
	if !bytes.Equal(bytes.TrimSpace(expected), bytes.TrimSpace(actual)) {
		return errors.New("inventory does not match the pinned native library and target")
	}
	return nil
}

func verifySchema(dir string, actual []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	for _, name := range []string{schemaName, "cryptography-defs.schema.json", "jsf-0.82.schema.json", "spdx.schema.json"} {
		data, err := root.ReadFile(name)
		if err != nil {
			return err
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return err
		}
		if err := compiler.AddResource(schemaBase+name, document); err != nil {
			return err
		}
	}
	schema, err := compiler.Compile(schemaBase + schemaName)
	if err != nil {
		return err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(actual))
	if err != nil {
		return err
	}
	return schema.Validate(document)
}

func verifyArchive(dockerfile, target, filename, digest, schemaDir string) error {
	a, err := pinnedAsset(dockerfile, target)
	if err != nil {
		return err
	}
	file, err := os.Open(filename) //nolint:gosec // The caller selects a local release archive.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	actualDigest, err := sha256Hex(file)
	if err != nil {
		return err
	}
	if actualDigest != digest {
		return fmt.Errorf("archive SHA-256: got %s, expected %s", actualDigest, digest)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = compressed.Close() }()
	root := strings.TrimSuffix(filepath.Base(filename), ".tar.gz")
	osArch := strings.ReplaceAll(target, "/", "_")
	if !strings.HasPrefix(root, "axiom-engine_") || !strings.HasSuffix(root, "_"+osArch) {
		return fmt.Errorf("archive filename does not match release target %s", target)
	}
	expected, actual, err := a.readArchive(tar.NewReader(compressed), root)
	if err != nil {
		return err
	}
	if err := verifyInventory(expected, actual); err != nil {
		return err
	}
	return verifySchema(schemaDir, actual)
}

func (a asset) readArchive(archive *tar.Reader, root string) (expected, actual []byte, err error) {
	paths := make(map[string]archivePath)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		name, err := recordArchivePath(paths, root, header)
		if err != nil {
			return nil, nil, err
		}
		switch name {
		case path.Join(root, a.name):
			if expected != nil || header.Typeflag != tar.TypeReg {
				return nil, nil, errors.New("expected one regular native library in the archive")
			}
			expected, err = a.inventory(archive)
		case path.Join(root, inventoryName):
			if actual != nil || header.Typeflag != tar.TypeReg || header.Size > 8192 {
				return nil, nil, errors.New("expected one small regular inventory in the archive")
			}
			actual, err = io.ReadAll(archive)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if expected == nil || actual == nil {
		return nil, nil, errors.New("archive is missing its native library or inventory")
	}
	return expected, actual, nil
}

type archivePath struct {
	name          string
	dir, explicit bool
}

func recordArchivePath(paths map[string]archivePath, root string, header *tar.Header) (string, error) {
	isDir := header.Typeflag == tar.TypeDir
	if !isDir && header.Typeflag != tar.TypeReg {
		return "", fmt.Errorf("unsupported archive entry %q", header.Name)
	}
	if isDir && (header.Name == "." || header.Name == "./") {
		if paths["."].explicit {
			return "", fmt.Errorf("duplicate archive directory %q", header.Name)
		}
		paths["."] = archivePath{name: ".", dir: true, explicit: true}
		return ".", nil
	}
	name := strings.TrimPrefix(header.Name, "./")
	if isDir {
		name = strings.TrimSuffix(name, "/")
	}
	inRoot := name == root && isDir || strings.HasPrefix(name, root+"/")
	if path.Clean(name) != name || !inRoot {
		return "", fmt.Errorf("noncanonical or out-of-root archive path %q", header.Name)
	}
	for part := range strings.SplitSeq(name, "/") {
		if !versionPattern.MatchString(part) {
			return "", fmt.Errorf("nonportable archive path %q", header.Name)
		}
	}
	for current := name; current != "."; current = path.Dir(current) {
		explicit := current == name
		dir := !explicit || isDir
		key := strings.ToLower(current)
		old, exists := paths[key]
		if exists && (old.name != current || old.dir != dir || explicit && old.explicit) {
			return "", fmt.Errorf("duplicate or colliding archive path %q", header.Name)
		}
		paths[key] = archivePath{name: current, dir: dir, explicit: explicit || old.explicit}
	}
	return name, nil
}

func run(arguments []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("native-inventory", flag.ContinueOnError)
	dockerfile := flags.String("dockerfile", "../Dockerfile", "file with BAML asset pins")
	target := flags.String("target", "", "release target (OS/architecture)")
	schemaDir := flags.String("schema-dir", "", "directory containing the CycloneDX schemas")
	digest := flags.String("archive-sha256", "", "expected SHA-256 of the release archive")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	args := flags.Args()
	switch {
	case len(args) == 2 && args[0] == "write":
		inventory, err := nativeInventory(*dockerfile, *target, args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, string(inventory))
		return err
	case len(args) == 3 && args[0] == "verify" && *schemaDir != "":
		expected, err := nativeInventory(*dockerfile, *target, args[1])
		if err != nil {
			return err
		}
		actual, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		if err := verifyInventory(expected, actual); err != nil {
			return err
		}
		return verifySchema(*schemaDir, actual)
	case len(args) == 2 && args[0] == "verify-archive" && *schemaDir != "" && *digest != "":
		return verifyArchive(*dockerfile, *target, args[1], *digest, *schemaDir)
	default:
		return errors.New("usage: native-inventory [flags] write LIBRARY | verify LIBRARY INVENTORY | verify-archive ARCHIVE")
	}
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "native inventory:", err)
		os.Exit(1)
	}
}
