package main

import (
	"fmt"
	"math"
)

func validateAlignment(d *diagram) error {
	rows := map[string][]node{}
	columns := map[string][]node{}
	for _, n := range d.Nodes {
		if n.Row != "" {
			for _, first := range rows[n.Row] {
				y, firstY := n.Y, first.Y
				if n.R > 0 {
					y += n.R
				}
				if first.R > 0 {
					firstY += first.R
				}
				if math.Abs(y-firstY) > .5 || math.Abs(n.H-first.H) > .5 {
					return fmt.Errorf("row %s is misaligned at %s", n.Row, n.ID)
				}
			}
			rows[n.Row] = append(rows[n.Row], n)
		}
		if n.Column != "" {
			for _, first := range columns[n.Column] {
				if math.Abs(n.X+n.W/2-first.X-first.W/2) > .5 {
					return fmt.Errorf("column %s is misaligned at %s", n.Column, n.ID)
				}
			}
			columns[n.Column] = append(columns[n.Column], n)
		}
	}
	return nil
}
