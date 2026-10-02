package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/managed"
)

func exportJob(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || !managed.ValidRequestKey(args[0]) {
		fmt.Fprintln(stderr, "usage: haepari export <job-id> --to <absolute-directory> [--json]")
		return 2
	}
	f := flag.NewFlagSet("export", flag.ContinueOnError)
	f.SetOutput(stderr)
	to := f.String("to", "", "exclusive explicit destination")
	outputJSON := f.Bool("json", false, "machine-readable receipt")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || !filepath.IsAbs(*to) || filepath.Clean(*to) != *to || *to == "/" {
		return 2
	}
	c, code := connect(stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	m, e, err := c.Export(context.Background(), args[0], *to)
	if err != nil {
		fmt.Fprintln(stderr, "Export incomplete; any newly created destination is retained for inspection.")
		return clientFailure(stdout, stderr, e, err)
	}
	if *outputJSON {
		return writeJSON(stdout, stderr, "export", map[string]any{"job_id": m.JobID, "destination": *to, "files": len(m.Files)})
	}
	fmt.Fprintln(stdout, *to)
	return 0
}
