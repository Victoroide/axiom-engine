package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func run(root string) error {
	for _, name := range []string{"request-path", "system-components", "seed-graph"} {
		d, err := loadDiagram(root, name)
		if err != nil {
			return err
		}
		if err := validate(d); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		for _, theme := range []string{"light", "dark"} {
			data, err := render(d, theme)
			if err != nil {
				return err
			}
			path := filepath.Join(root, "docs", "assets", "diagrams", name+"-"+theme+".svg")
			if err := os.WriteFile(path, data, 0600); err != nil {
				return err
			}
			fmt.Printf("%s: %d bytes\n", path, len(data))
		}
	}
	return nil
}

func main() {
	root := flag.String("root", "..", "repository root")
	flag.Parse()
	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
