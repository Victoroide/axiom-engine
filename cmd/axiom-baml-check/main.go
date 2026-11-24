//go:build darwin || linux

// Command axiom-baml-check verifies the selected native library without loading it.
package main

import (
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"
)

// The archive and image builds set these from the Dockerfile pins.
var (
	libraryVersion string
	librarySHA256  string
)

const maxLibrarySize = 256 << 20

type diagnostic struct {
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	Platform    string `json:"platform"`
	BAMLVersion string `json:"baml_version"`
}

func main() {
	os.Exit(run(os.Stderr, os.Args[1:], os.Getenv("BAML_LIBRARY_PATH"), runtime.GOOS+"/"+runtime.GOARCH, libraryVersion, librarySHA256))
}

func run(stderr io.Writer, args []string, path, platform, version, pin string) int {
	record := diagnostic{Status: "error", Platform: platform}
	if len(version) <= 32 {
		record.BAMLVersion = version
	}
	exit := 2
	checksum, err := hex.DecodeString(pin)
	switch {
	case len(args) != 0:
		record.Reason = "invalid_arguments"
	case platform != "linux/amd64" && platform != "linux/arm64" && platform != "darwin/arm64":
		record.Reason = "unsupported_platform"
	case record.BAMLVersion == "" || err != nil || len(checksum) != sha256.Size:
		record.Reason = "invalid_build_pin"
	case path == "":
		record.Reason = "path_not_set"
	case strings.HasPrefix(platform, "linux/") && !strings.ContainsRune(path, '/'):
		// A slash makes dlopen use the supplied pathname instead of its search path.
		record.Reason = "path_requires_slash"
	default:
		record.Reason = verifyLibrary(path, platform, [sha256.Size]byte(checksum))
		exit = 1
		if record.Reason == "verified" {
			record.Status = "ok"
			exit = 0
		}
	}
	if err := json.NewEncoder(stderr).Encode(record); err != nil {
		return 2
	}
	return exit
}

func verifyLibrary(path, platform string, want [sha256.Size]byte) string {
	info, err := os.Stat(path) //nolint:gosec // Inspect the library selected by the operator.
	if reason := checkFile(info, err); reason != "" {
		return reason
	}
	// Avoid blocking if the path is replaced with a FIFO before open.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // Inspect the library selected by the operator.
	if err != nil {
		return fileError(err)
	}
	defer func() { _ = file.Close() }()
	if reason := checkFile(file.Stat()); reason != "" {
		return reason
	}
	var header [20]byte
	if _, err := file.ReadAt(header[:], 0); errors.Is(err, io.EOF) {
		return "invalid_header"
	} else if err != nil {
		return "unreadable_file"
	}
	if reason := checkHeader(header, platform); reason != "" {
		return reason
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, maxLibrarySize+1))
	if err != nil {
		return "unreadable_file"
	}
	if size > maxLibrarySize {
		return "file_too_large"
	}
	if [sha256.Size]byte(hash.Sum(nil)) != want {
		return "digest_mismatch"
	}
	return "verified"
}

func checkFile(info os.FileInfo, err error) string {
	switch {
	case err != nil:
		return fileError(err)
	case !info.Mode().IsRegular():
		return "not_regular_file"
	case info.Mode().Perm()&0444 == 0:
		return "unreadable_file"
	case info.Size() > maxLibrarySize:
		return "file_too_large"
	default:
		return ""
	}
}

func fileError(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "missing_file"
	}
	return "unreadable_file"
}

// Read only the fixed header fields needed to identify a 64-bit shared library.
// The digest verifies the remaining bytes without parsing their structure.
func checkHeader(header [20]byte, platform string) string {
	switch {
	case string(header[:4]) == elf.ELFMAG:
		if header[elf.EI_CLASS] != byte(elf.ELFCLASS64) || header[elf.EI_DATA] != byte(elf.ELFDATA2LSB) {
			return "platform_mismatch"
		}
		if binary.LittleEndian.Uint16(header[16:18]) != uint16(elf.ET_DYN) {
			return "invalid_header"
		}
		machine := elf.Machine(binary.LittleEndian.Uint16(header[18:20]))
		if (platform == "linux/amd64" && machine == elf.EM_X86_64) || (platform == "linux/arm64" && machine == elf.EM_AARCH64) {
			return ""
		}
	case binary.LittleEndian.Uint32(header[:4]) == macho.Magic64:
		if binary.LittleEndian.Uint32(header[12:16]) != uint32(macho.TypeDylib) {
			return "invalid_header"
		}
		if platform == "darwin/arm64" && binary.LittleEndian.Uint32(header[4:8]) == uint32(macho.CpuArm64) {
			return ""
		}
	default:
		return "invalid_header"
	}
	return "platform_mismatch"
}
