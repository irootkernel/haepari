package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
)

func doctorCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	id := flags.String("resource", "", "registered resource to inspect without repair")
	jsonFlag := flags.Bool("json", false, "JSON response")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !*jsonFlag || !managed.ValidRequestKey(*id) {
		fmt.Fprintln(stderr, "usage: haepari doctor --resource <id> --json")
		return 2
	}
	root, err := state.Resolve(os.Getenv("HAEPARI_HOME"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := client.Connect(ctx, root)
	if err != nil {
		issue := &client.Issue{Code: string(managed.PrerequisiteUnavailable), Retry: "none",
			Message: "Cannot inspect the selected controller; check HAEPARI_HOME and start the intended controller explicitly."}
		if code := emit(stdout, stderr, client.Envelope{Schema: "haepari.envelope/v1", Operation: "doctor", Error: issue}); code != 0 {
			return code
		}
		return 2
	}
	defer c.Close()
	report, envelope, err := c.DoctorResource(ctx, *id)
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	if code := emit(stdout, stderr, envelope); code != 0 {
		return code
	}
	if !report.Ready {
		return 2
	}
	return 0
}
