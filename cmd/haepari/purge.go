package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/irootkernel/haepari/internal/managed"
)

func purgeJob(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || !managed.ValidRequestKey(args[0]) {
		fmt.Fprintln(stderr, "usage: haepari purge <job-id> --expected-revision <n> --json")
		return 2
	}
	f := flag.NewFlagSet("purge", flag.ContinueOnError)
	f.SetOutput(stderr)
	revision := f.Int64("expected-revision", 0, "finished Job revision")
	outputJSON := f.Bool("json", false, "machine-readable receipt")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || *revision <= 0 || !*outputJSON {
		return 2
	}
	c, code := connect(stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	_, e, err := c.Purge(context.Background(), args[0], *revision)
	if err != nil {
		return clientFailure(stdout, stderr, e, err)
	}
	return emit(stdout, stderr, e)
}
