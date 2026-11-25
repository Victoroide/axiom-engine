//go:build !linux && !darwin

package graph

import (
	"errors"
	"os"
)

func openSnapshotFile(string) (*os.File, error) {
	return nil, errors.New("graph reload is supported only on Linux and macOS")
}
