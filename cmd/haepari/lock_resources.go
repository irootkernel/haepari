package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
)

func lockResourceCommand(action string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("resource "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonFlag := flags.Bool("json", false, "JSON response")
	expected := flags.Int64("expected-revision", 0, "expected configuration revision")
	availability := flags.Int64("expected-availability-revision", 0, "expected availability revision for a VM update")
	file := flags.String("file", "", "new resource configuration")
	plan := flags.Bool("plan", false, "recovery plan")
	id := ""
	if action != "update" {
		if len(args) == 0 || !managed.ValidRequestKey(args[0]) {
			fmt.Fprintln(stderr, "resource ID required")
			return 2
		}
		id = args[0]
		args = args[1:]
	}
	if flags.Parse(args) != nil || flags.NArg() != 0 || !*jsonFlag {
		return 2
	}
	availabilitySupplied := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "expected-availability-revision" {
			availabilitySupplied = true
		}
	})
	if action != "update" && availabilitySupplied {
		fmt.Fprintln(stderr, "--expected-availability-revision is only valid for a VM update")
		return 2
	}
	var spec json.RawMessage
	vmUpdate := false
	switch action {
	case "update":
		if *file == "" || *expected <= 0 || *plan {
			return 2
		}
		f, err := os.Open(*file)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
		_ = f.Close()
		if err != nil || len(raw) > 1<<20 {
			fmt.Fprintln(stderr, "invalid resource file")
			return 2
		}
		resource, err := managed.DecodeResource(raw)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		id = resource.ID
		spec = raw
		vmUpdate = resource.Kind == "utm-macos"
		if vmUpdate && *availability <= 0 {
			fmt.Fprintln(stderr, "VM update requires a positive --expected-availability-revision")
			return 2
		}
		if !vmUpdate && availabilitySupplied {
			fmt.Fprintln(stderr, "--expected-availability-revision requires the resource file's kind to be utm-macos")
			return 2
		}
	case "remove":
		if *expected <= 0 || *file != "" || *plan {
			return 2
		}
	case "recover":
		if !*plan || *file != "" || *expected != 0 {
			return 2
		}
	default:
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	var envelope client.Envelope
	var err error
	if vmUpdate {
		_, envelope, err = c.UpdateVMResource(context.Background(), id, spec, *expected, *availability)
	} else {
		envelope, err = c.LockResourceAction(context.Background(), action, id, spec, *expected, *plan)
	}
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}
