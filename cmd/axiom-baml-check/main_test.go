//go:build darwin || linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestVerifyLibrary(t *testing.T) {
	fixtures := map[string][]byte{
		"linux/amd64":  {0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 0, 0x3e, 0, 0x61},
		"linux/arm64":  {0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 0, 0xb7, 0, 0x61},
		"darwin/arm64": {0xcf, 0xfa, 0xed, 0xfe, 0x0c, 0, 0, 1, 0, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0, 0x61},
	}
	for platform, asset := range fixtures {
		t.Run(platform, func(t *testing.T) {
			dir := t.TempDir()
			valid := writeAsset(t, dir, "valid native library", asset, 0444)
			unreadable := writeAsset(t, dir, "unreadable", asset, 0000)
			corrupt := bytes.Clone(asset)
			corrupt[len(corrupt)-1] ^= 1
			changed := writeAsset(t, dir, "corrupted", corrupt, 0600)
			short := writeAsset(t, dir, "short", asset[:4], 0600)
			invalid := writeAsset(t, dir, "invalid", make([]byte, len(asset)), 0600)
			symlink := filepath.Join(dir, "selected")
			if err := os.Symlink(valid, symlink); err != nil {
				t.Fatal(err)
			}
			fifo := filepath.Join(dir, "fifo")
			if err := syscall.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			wantDigest := sha256.Sum256(asset)
			for _, tc := range []struct{ name, path, want string }{
				{"valid read-only file", valid, "verified"},
				{"symlink to valid file", symlink, "verified"},
				{"missing", filepath.Join(dir, "missing"), "missing_file"},
				{"directory", dir, "not_regular_file"},
				{"fifo", fifo, "not_regular_file"},
				{"unreadable", unreadable, "unreadable_file"},
				{"one-byte corruption", changed, "digest_mismatch"},
				{"short header", short, "invalid_header"},
				{"invalid magic", invalid, "invalid_header"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if got := verifyLibrary(tc.path, platform, wantDigest); got != tc.want {
						t.Fatalf("reason = %q, want %q", got, tc.want)
					}
				})
			}
			for other, otherAsset := range fixtures {
				if other == platform {
					continue
				}
				t.Run("wrong target "+other, func(t *testing.T) {
					path := writeAsset(t, t.TempDir(), "other", otherAsset, 0600)
					if got := verifyLibrary(path, platform, wantDigest); got != "platform_mismatch" {
						t.Fatalf("reason = %q, want platform_mismatch", got)
					}
				})
			}
			if platform == "darwin/arm64" {
				intel := bytes.Clone(asset)
				intel[4] = 7 // CPU_TYPE_X86_64
				path := writeAsset(t, dir, "intel dylib", intel, 0600)
				if got := verifyLibrary(path, platform, wantDigest); got != "platform_mismatch" {
					t.Fatalf("reason = %q for Intel Mach-O, want platform_mismatch", got)
				}
			}
		})
	}
}

func TestVerifyLibraryBounds(t *testing.T) {
	path := writeAsset(t, t.TempDir(), "oversized", nil, 0600)
	if err := os.Truncate(path, 256<<20+1); err != nil {
		t.Fatal(err)
	}
	if got := verifyLibrary(path, "linux/amd64", sha256.Sum256(nil)); got != "file_too_large" {
		t.Fatalf("reason = %q, want file_too_large", got)
	}
}

func TestDiagnosticRecord(t *testing.T) {
	const platform = "linux/arm64"
	const version = "1.2.3"
	const secret = "synthetic-private-path-or-credential"
	asset := []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 0, 0xb7, 0, 0x61}
	digest := sha256.Sum256(asset)
	pin := hex.EncodeToString(digest[:])
	dir := t.TempDir()
	valid := writeAsset(t, dir, secret, asset, 0400)
	t.Chdir(dir)
	for _, tc := range []struct {
		name, path, platform, pin, status, reason string
		args                                      []string
		exit                                      int
	}{
		{name: "valid absolute path", path: valid, platform: platform, pin: pin, status: "ok", reason: "verified"},
		{name: "valid explicit relative path", path: "./" + secret, platform: platform, pin: pin, status: "ok", reason: "verified"},
		{name: "ambiguous Linux library name", path: secret, platform: platform, pin: pin, status: "error", reason: "path_requires_slash", exit: 2},
		{name: "empty settings", platform: platform, pin: pin, status: "error", reason: "path_not_set", exit: 2},
		{name: "private path", path: valid + "\nmissing", platform: platform, pin: pin, status: "error", reason: "missing_file", exit: 1},
		{name: "unexpected argument", args: []string{secret}, platform: platform, pin: pin, status: "error", reason: "invalid_arguments", exit: 2},
		{name: "invalid pin", path: valid, platform: platform, pin: secret, status: "error", reason: "invalid_build_pin", exit: 2},
		{name: "unsupported target", path: valid, platform: "darwin/amd64", pin: pin, status: "error", reason: "unsupported_platform", exit: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if got := run(&stderr, tc.args, tc.path, tc.platform, version, tc.pin); got != tc.exit {
				t.Fatalf("exit = %d, want %d, stderr = %s", got, tc.exit, &stderr)
			}
			if stderr.Len() > 160 || bytes.Count(stderr.Bytes(), []byte{'\n'}) != 1 || bytes.Contains(stderr.Bytes(), []byte(secret)) {
				t.Fatalf("invalid stderr framing or input disclosure: %q", &stderr)
			}
			var got map[string]string
			if err := json.Unmarshal(stderr.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != 4 || got["status"] != tc.status || got["reason"] != tc.reason || got["platform"] != tc.platform || got["baml_version"] != version {
				t.Fatalf("unexpected diagnostic: %v", got)
			}
		})
	}
}

func writeAsset(t *testing.T, dir, name string, content []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, mode); err != nil { //nolint:gosec // The asset is a fixture in a fresh test directory.
		t.Fatal(err)
	}
	return path
}
