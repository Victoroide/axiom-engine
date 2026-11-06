package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Victoroide/axiom-engine/internal/graph"
)

func runValidateGraph(args []string, stdout io.Writer) (err error) {
	defer func() {
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
		}
	}()
	if len(args) != 1 {
		return fmt.Errorf("usage: axiom-server validate-graph <file>")
	}
	g, err := graph.Load(args[0])
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(g.Summary())
}
