//go:build linux || darwin

package graph

import (
	"os"
	"path/filepath"
	"syscall"
)

func openSnapshotFile(path string) (*os.File, error) {
	// A FIFO replacement must not block before we can inspect the opened file.
	return os.OpenFile(filepath.Clean(path), os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // GRAPH_FILE is operator-selected.
}
