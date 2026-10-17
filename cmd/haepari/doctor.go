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

const doctorUsageLegacy = "usage: haepari doctor --resource <id> --json"
const doctorUsageProjection = "usage: haepari doctor --projection --section queue|retention [--resource <id>] [--cursor <cursor>] [--limit <1-20>] --json"

func doctorCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	resource := flags.String("resource", "", "registered resource to inspect without repair")
	jsonFlag := flags.Bool("json", false, "JSON response")
	projection := flags.Bool("projection", false, "read-only operator diagnostic projection")
	section := flags.String("section", "", "diagnostic section queue or retention")
	cursor := flags.String("cursor", "", "opaque diagnostic page cursor")
	limit := flags.String("limit", "", "diagnostic page size from 1 to 20")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, doctorUsageLegacy)
		fmt.Fprintln(stderr, doctorUsageProjection)
		return 2
	}
	visited := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if !*projection {
		if visited["section"] || visited["cursor"] || visited["limit"] || !*jsonFlag || !managed.ValidRequestKey(*resource) {
			fmt.Fprintln(stderr, doctorUsageLegacy)
			fmt.Fprintln(stderr, doctorUsageProjection)
			return 2
		}
		return doctorResource(stdout, stderr, *resource)
	}
	limitN := 0
	if visited["limit"] {
		parsed, ok := canonicalDiagnosticLimit(*limit)
		if !ok {
			fmt.Fprintln(stderr, doctorUsageLegacy)
			fmt.Fprintln(stderr, doctorUsageProjection)
			return 2
		}
		limitN = parsed
	}
	if !*jsonFlag || (*section != "queue" && *section != "retention") || (visited["resource"] && !managed.ValidRequestKey(*resource)) || (visited["cursor"] && *cursor == "") {
		fmt.Fprintln(stderr, doctorUsageLegacy)
		fmt.Fprintln(stderr, doctorUsageProjection)
		return 2
	}
	resourceID := ""
	if visited["resource"] {
		resourceID = *resource
	}
	cursorValue := ""
	if visited["cursor"] {
		cursorValue = *cursor
	}
	return doctorProjection(stdout, stderr, *section, resourceID, cursorValue, limitN)
}

func doctorResource(stdout, stderr io.Writer, id string) int {
	c, ctx, cancel, code := doctorClient(stdout, stderr)
	if c == nil {
		return code
	}
	defer cancel()
	defer c.Close()
	report, envelope, err := c.DoctorResource(ctx, id)
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

func doctorProjection(stdout, stderr io.Writer, section, resource, cursor string, limit int) int {
	c, ctx, cancel, code := doctorClient(stdout, stderr)
	if c == nil {
		return code
	}
	defer cancel()
	defer c.Close()
	_, envelope, err := c.OperatorDiagnostic(ctx, section, resource, cursor, limit)
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}

func doctorClient(stdout, stderr io.Writer) (*client.Client, context.Context, context.CancelFunc, int) {
	root, err := state.Resolve(os.Getenv("HAEPARI_HOME"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, nil, nil, 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	c, err := client.Connect(ctx, root)
	if err != nil {
		cancel()
		issue := &client.Issue{Code: string(managed.PrerequisiteUnavailable), Retry: "none",
			Message: "Cannot inspect the selected controller; check HAEPARI_HOME and start the intended controller explicitly."}
		if code := emit(stdout, stderr, client.Envelope{Schema: "haepari.envelope/v1", Operation: "doctor", Error: issue}); code != 0 {
			return nil, nil, nil, code
		}
		return nil, nil, nil, 2
	}
	return c, ctx, cancel, 0
}

func canonicalDiagnosticLimit(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	if len(raw) > 1 && raw[0] == '0' {
		return 0, false
	}
	n := 0
	for _, c := range raw {
		n = n*10 + int(c-'0')
		if n > 20 {
			return 0, false
		}
	}
	if n < 1 {
		return 0, false
	}
	return n, true
}
