// Command coverage-report evaluates a curriculum crosswalk against a manifest
// and knowledge graph, emitting deterministic JSON or Markdown output.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Victoroide/axiom-engine/evaluation/curriculum"
)

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("coverage-report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	manifestPath := fs.String("manifest", "evaluation/curriculum/editions/ap-csa-fall-2025/manifest.json", "Path to objective manifest JSON")
	crosswalkPath := fs.String("crosswalk", "evaluation/curriculum/editions/ap-csa-fall-2025/computer-science.crosswalk.json", "Path to crosswalk JSON")
	graphPath := fs.String("graph", "examples/graphs/computer-science.json", "Path to graph JSON")
	format := fs.String("format", "json", "Output format: json or markdown")
	outPath := fs.String("out", "", "Optional output file path (defaults to stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	report, err := curriculum.EvaluateFiles(*manifestPath, *crosswalkPath, *graphPath)
	if err != nil {
		return err
	}

	var output []byte
	switch *format {
	case "json":
		output, err = curriculum.RenderReportJSON(report)
	case "markdown":
		output, err = curriculum.RenderReportMarkdown(report)
	default:
		return fmt.Errorf("unsupported format %q (expected json or markdown)", *format)
	}
	if err != nil {
		return err
	}

	if *outPath != "" {
		return os.WriteFile(filepath.Clean(*outPath), output, 0o600) //nolint:gosec // Explicit output flag selected by operator.
	}
	_, err = stdout.Write(output)
	return err
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
