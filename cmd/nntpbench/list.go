package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// Listing never runs host preconditions, starts services, or generates data.
func listBenchmarks(args []string, out io.Writer) error {
	f := flag.NewFlagSet("list", flag.ContinueOnError)
	chain := f.String("chain", "", "list experiments in a chain configuration")
	matrix := f.String("matrix", "fixtures/matrix.json", "fixture matrix to describe")
	asJSON := f.Bool("json", false, "emit machine-readable IDs and display names")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("list accepts flags only")
	}
	type row struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	var rows []row
	if *chain != "" {
		config, err := loadChainConfig(*chain)
		if err != nil {
			return err
		}
		for _, p := range config.Phases {
			rows = append(rows, row{p.Name, p.displayName()})
		}
	} else {
		m, err := fixture.LoadMatrix(*matrix)
		if err != nil {
			return err
		}
		cases, err := m.Expand()
		if err != nil {
			return err
		}
		for _, c := range cases {
			rows = append(rows, row{c.ID, c.DisplayName()})
		}
	}
	if *asJSON {
		e := json.NewEncoder(out)
		e.SetIndent("", "  ")
		return e.Encode(rows)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "BENCHMARK\tSTABLE ID"); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", r.DisplayName, r.ID); err != nil {
			return err
		}
	}
	return w.Flush()
}
