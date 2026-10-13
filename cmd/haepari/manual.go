package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/irootkernel/haepari/internal/managed"
)

func manualCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "complete" || !managed.ValidRequestKey(args[1]) {
		fmt.Fprintln(stderr, "usage: haepari manual complete <job-id> --input <json> --expected-revision <n> --json")
		return 2
	}
	f := flag.NewFlagSet("manual complete", flag.ContinueOnError)
	f.SetOutput(stderr)
	path := f.String("input", "", "manual observation record")
	revision := f.Int64("expected-revision", 0, "original expected Job revision")
	output := f.Bool("json", false, "machine-readable receipt")
	if f.Parse(args[2:]) != nil || f.NArg() != 0 || *path == "" || *revision <= 0 || !*output {
		fmt.Fprintln(stderr, "usage: haepari manual complete <job-id> --input <json> --expected-revision <n> --json")
		return 2
	}
	file, err := os.Open(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		fmt.Fprintln(stderr, "invalid manual observation document")
		return 2
	}
	if err := managed.ValidateManualResultJSONKeys(raw); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	c, code := connect(stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	_, envelope, err := c.CompleteManual(context.Background(), args[1], raw, *revision)
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}
