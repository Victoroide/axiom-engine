package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/klauspost/compress/zstd"
)

const (
	imageLibraryPath   = "app/libbaml_cffi.so"
	imageInventoryPath = "app/" + inventoryName
	maxOCIJSON         = 4 << 20
)

var ociDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type openBlob func(v1.Descriptor) (io.ReadCloser, error)

// An OCI export can be verified directly without importing any blobs into Docker.
func verifyOCIExport(a asset, filename, digest, schemaDir string) (string, error) {
	file, err := os.Open(filename) //nolint:gosec // The caller selects a local OCI export.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	entries, err := indexOCIExport(file)
	if err != nil {
		return "", err
	}
	layoutIndex := entries["index.json"]
	if layoutIndex.size <= 0 || layoutIndex.size > maxOCIJSON {
		return "", errors.New("missing or oversized OCI index.json")
	}
	data, err := io.ReadAll(io.NewSectionReader(file, layoutIndex.offset, layoutIndex.size))
	if err != nil {
		return "", err
	}
	var index v1.IndexManifest
	if err := json.Unmarshal(data, &index); err != nil {
		return "", err
	}
	if index.SchemaVersion != 2 || len(index.Manifests) != 1 || index.Manifests[0].Digest.String() != digest {
		return "", fmt.Errorf("OCI export does not name only the expected digest %s", digest)
	}
	open := func(d v1.Descriptor) (io.ReadCloser, error) {
		entry, ok := entries["blobs/sha256/"+d.Digest.Hex]
		if !ok {
			return nil, fmt.Errorf("missing OCI blob %s", d.Digest)
		}
		return io.NopCloser(io.NewSectionReader(file, entry.offset, entry.size)), nil
	}
	return verifyImageBlobs(a, index.Manifests[0], open, schemaDir)
}

type tarLocation struct{ offset, size int64 }

func indexOCIExport(file *os.File) (map[string]tarLocation, error) {
	entries := make(map[string]tarLocation)
	r := tar.NewReader(file)
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		member := path.Clean(h.Name)
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return nil, fmt.Errorf("unsupported OCI export entry %q", h.Name)
		}
		if member != "index.json" && !strings.HasPrefix(member, "blobs/sha256/") {
			continue
		}
		if _, duplicate := entries[member]; duplicate || h.Typeflag != tar.TypeReg || len(h.PAXRecords) > 0 ||
			strings.TrimPrefix(h.Name, "./") != member {
			return nil, fmt.Errorf("expected one regular OCI export entry %q", member)
		}
		offset, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		entries[member] = tarLocation{offset: offset, size: h.Size}
	}
}

func verifyRegistryImage(ctx context.Context, a asset, repository, digest, schemaDir string) (string, error) {
	repo, err := name.NewRepository(repository, name.StrictValidation)
	if err != nil {
		return "", err
	}
	options := []remote.Option{remote.WithAuthFromKeychain(authn.DefaultKeychain), remote.WithContext(ctx)}
	root, err := remote.Get(repo.Digest(digest), options...)
	if err != nil {
		return "", err
	}
	if root.Digest.String() != digest {
		return "", fmt.Errorf("registry returned %s for expected digest %s", root.Digest, digest)
	}
	open := func(d v1.Descriptor) (io.ReadCloser, error) {
		if d.Digest == root.Digest {
			return io.NopCloser(bytes.NewReader(root.Manifest)), nil
		}
		ref := repo.Digest(d.Digest.String())
		if d.MediaType.IsIndex() || d.MediaType.IsImage() {
			doc, err := remote.Get(ref, options...)
			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(doc.Manifest)), nil
		}
		layer, err := remote.Layer(ref, options...)
		if err != nil {
			return nil, err
		}
		return layer.Compressed()
	}
	return verifyImageBlobs(a, root.Descriptor, open, schemaDir)
}

func verifyImageBlobs(a asset, root v1.Descriptor, open openBlob, schemaDir string) (string, error) {
	osName, arch, _ := strings.Cut(a.target, "/")
	manifestRef := root
	if root.MediaType.IsIndex() {
		var index v1.IndexManifest
		if err := readOCIJSON(root, open, &index); err != nil {
			return "", err
		}
		if index.SchemaVersion != 2 {
			return "", errors.New("unsupported OCI index version")
		}
		var matches int
		for _, d := range index.Manifests {
			if d.Platform != nil && d.Platform.OS == osName && d.Platform.Architecture == arch {
				matches++
				manifestRef = d
			}
		}
		if matches != 1 {
			return "", fmt.Errorf("expected one OCI manifest for %s, found %d", a.target, matches)
		}
	}
	if !manifestRef.MediaType.IsImage() {
		return "", fmt.Errorf("unsupported OCI manifest type %q", manifestRef.MediaType)
	}
	var manifest v1.Manifest
	if err := readOCIJSON(manifestRef, open, &manifest); err != nil {
		return "", err
	}
	if manifest.SchemaVersion != 2 || !manifest.Config.MediaType.IsConfig() {
		return "", errors.New("invalid OCI image manifest or config type")
	}
	var config v1.ConfigFile
	if err := readOCIJSON(manifest.Config, open, &config); err != nil {
		return "", err
	}
	if config.OS != osName || config.Architecture != arch || config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(manifest.Layers) {
		return "", fmt.Errorf("invalid OCI image config for %s", a.target)
	}
	var files imageFiles
	for i, layer := range manifest.Layers {
		if err := files.readLayer(open, layer, config.RootFS.DiffIDs[i]); err != nil {
			return "", err
		}
	}
	if files.library == "" || files.inventory == nil {
		return "", errors.New("OCI image is missing its native library or inventory")
	}
	expected, err := a.inventoryForDigest(files.library)
	if err != nil {
		return "", err
	}
	if err := verifyInventory(expected, files.inventory); err != nil {
		return "", err
	}
	if err := verifySchema(schemaDir, files.inventory); err != nil {
		return "", err
	}
	return manifestRef.Digest.String(), nil
}

func readOCIJSON(d v1.Descriptor, open openBlob, value any) error {
	if d.Size > maxOCIJSON {
		return fmt.Errorf("OCI metadata %s is too large", d.Digest)
	}
	var data []byte
	if err := checkBlob(d, open, func(r io.Reader) error {
		var err error
		data, err = io.ReadAll(r)
		return err
	}); err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func checkBlob(d v1.Descriptor, open openBlob, consume func(io.Reader) error) error {
	if !ociDigestPattern.MatchString(d.Digest.String()) || d.Size < 0 || d.Size == math.MaxInt64 {
		return fmt.Errorf("invalid OCI descriptor %s", d.Digest)
	}
	stream, err := open(d)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	hash := sha256.New()
	limit := &io.LimitedReader{R: stream, N: d.Size + 1}
	reader := io.TeeReader(limit, hash)
	if err := consume(reader); err != nil {
		return fmt.Errorf("OCI blob %s: %w", d.Digest, err)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("OCI blob %s: %w", d.Digest, err)
	}
	if limit.N != 1 {
		return fmt.Errorf("OCI blob %s has the wrong size", d.Digest)
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != d.Digest.Hex {
		return fmt.Errorf("OCI blob %s has the wrong digest", d.Digest)
	}
	return nil
}

type imageFiles struct {
	library   string
	inventory []byte
}

func (files *imageFiles) readLayer(open openBlob, layer v1.Descriptor, diffID v1.Hash) error {
	return checkBlob(layer, open, func(r io.Reader) error {
		switch layer.MediaType {
		case types.OCILayer, types.DockerLayer, types.OCIRestrictedLayer:
			gzipReader, err := gzip.NewReader(r)
			if err != nil {
				return err
			}
			defer func() { _ = gzipReader.Close() }()
			r = gzipReader
		case types.OCILayerZStd:
			decoder, err := zstd.NewReader(r)
			if err != nil {
				return err
			}
			defer decoder.Close()
			r = decoder
		case types.OCIUncompressedLayer, types.DockerUncompressedLayer, types.OCIUncompressedRestrictedLayer:
		default:
			return fmt.Errorf("unsupported OCI layer type %q", layer.MediaType)
		}
		hash := sha256.New()
		uncompressed := io.TeeReader(r, hash)
		if err := files.applyLayer(tar.NewReader(uncompressed)); err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, uncompressed); err != nil {
			return err
		}
		if fmt.Sprintf("sha256:%x", hash.Sum(nil)) != diffID.String() {
			return fmt.Errorf("uncompressed layer digest does not match %s", diffID)
		}
		return nil
	})
}

func (files *imageFiles) applyLayer(r *tar.Reader) error {
	var changes imageFiles
	seen := make(map[string]bool)
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe OCI layer path %q", h.Name)
		}
		if h.Typeflag == tar.TypeLink || h.Typeflag == tar.TypeSymlink {
			target := path.Clean(h.Linkname)
			if h.Typeflag == tar.TypeSymlink && !path.IsAbs(target) {
				target = path.Join(path.Dir(name), target)
			}
			if isAppPath(name) || isAppPath(strings.TrimPrefix(target, "/")) || target == "." || target == "/" {
				return fmt.Errorf("link affecting OCI application files %q", h.Name)
			}
		}
		if isAppPath(name) {
			if seen[name] || name == "app" && h.Typeflag != tar.TypeDir ||
				strings.HasPrefix(name, imageLibraryPath+"/") || strings.HasPrefix(name, imageInventoryPath+"/") {
				return fmt.Errorf("colliding OCI application path %q", h.Name)
			}
			seen[name] = true
		}
		switch name {
		case ".wh..wh..opq", ".wh.app", "app/.wh..wh..opq":
			*files = imageFiles{}
		case "app/.wh.libbaml_cffi.so":
			files.library = ""
		case "app/.wh." + inventoryName:
			files.inventory = nil
		case imageLibraryPath:
			if h.Typeflag != tar.TypeReg {
				return errors.New("expected a regular OCI native library")
			}
			changes.library, err = sha256Hex(r)
		case imageInventoryPath:
			if h.Typeflag != tar.TypeReg || h.Size > 8192 {
				return errors.New("expected a small regular OCI native inventory")
			}
			changes.inventory, err = io.ReadAll(r)
		}
		if err != nil {
			return err
		}
	}
	if changes.library != "" {
		files.library = changes.library
	}
	if changes.inventory != nil {
		files.inventory = changes.inventory
	}
	return nil
}

func isAppPath(name string) bool { return name == "app" || strings.HasPrefix(name, "app/") }
