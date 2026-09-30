package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
)

func mutationKey() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "mutation-" + hex.EncodeToString(nonce[:]), nil
}
func observerContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
}

func plainLogFailure(stderr io.Writer, err error) int {
	var issue *client.Issue
	if errors.As(err, &issue) {
		fmt.Fprintf(stderr, "%s: %s\n", issue.Code, issue.Message)
		if code, mappingErr := managed.ExitCode(managed.ErrorCode(issue.Code)); mappingErr == nil {
			return code
		}
		return 6
	}
	return clientFailure(io.Discard, stderr, client.Envelope{}, err)
}

func list(kind string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		filters := ""
		if kind == "jobs" {
			filters = " [--resource <id>] [--state <state>]"
		}
		fmt.Fprintf(stderr, "usage: haepari %s list%s [--cursor <cursor>] [--limit <1-100>] --json\n", kind, filters)
		return 2
	}
	flags := flag.NewFlagSet(kind+" list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cursor := flags.String("cursor", "", "page cursor")
	limit := flags.Int("limit", 50, "page size, 1-100")
	jsonFlag := flags.Bool("json", false, "JSON response")
	resource, state := new(string), new(string)
	if kind == "jobs" {
		resource = flags.String("resource", "", "resource filter")
		state = flags.String("state", "", "Job state filter: queued, preparing, ready, running, awaiting_manual, cancelling, collecting, cleaning, settling, reconciling, finished")
	}
	if flags.Parse(args[1:]) != nil {
		return 2
	}
	if flags.NArg() != 0 || !*jsonFlag || *limit < 1 || *limit > 100 {
		fmt.Fprintln(stderr, "valid bounded page and --json required")
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	var envelope client.Envelope
	var err error
	if kind == "jobs" {
		_, envelope, err = c.Jobs(context.Background(), managed.JobsQuery{Resource: *resource, State: *state, Cursor: *cursor, Limit: *limit})
	} else {
		_, envelope, err = c.Resources(context.Background(), *cursor, *limit)
	}
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}

func cancelJob(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: haepari cancel <job-id> --expected-revision <n> [--request-key <key>] --json")
		return 2
	}
	flags := flag.NewFlagSet("cancel", flag.ContinueOnError)
	flags.SetOutput(stderr)
	revision := flags.Int64("expected-revision", 0, "expected Job revision")
	key := flags.String("request-key", "", "persisted mutation key for exact replay")
	jsonFlag := flags.Bool("json", false, "JSON response")
	if flags.Parse(args[1:]) != nil {
		return 2
	}
	if flags.NArg() != 0 || *revision <= 0 || !*jsonFlag {
		fmt.Fprintln(stderr, "positive expected revision and --json required")
		return 2
	}
	if *key == "" {
		var err error
		*key, err = mutationKey()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 6
		}
	}
	if !managed.ValidRequestKey(*key) {
		fmt.Fprintln(stderr, "invalid request key")
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	_, envelope, err := c.Cancel(context.Background(), managed.CancelRequest{JobID: args[0], ExpectedRevision: *revision, MutationKey: *key})
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}

func observe(kind string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		if kind == "logs" {
			fmt.Fprintln(stderr, "usage: haepari logs <job-id> [--after <offset>] [--identity <id>] [--follow] [--json]")
		} else {
			fmt.Fprintln(stderr, "usage: haepari events <job-id> [--after <cursor>] [--limit <1-1000>] [--follow] --json")
		}
		return 2
	}
	flags := flag.NewFlagSet(kind, flag.ContinueOnError)
	flags.SetOutput(stderr)
	after := flags.Int64("after", 0, "event cursor or exact byte offset")
	follow := flags.Bool("follow", false, "block on one observer stream")
	jsonFlag := flags.Bool("json", false, "JSON response")
	limit := 100
	identity := ""
	if kind == "events" {
		flags.IntVar(&limit, "limit", 100, "event page size, 1-1000")
	} else {
		flags.StringVar(&identity, "identity", "", "log file identity from the previous page")
	}
	if flags.Parse(args[1:]) != nil {
		return 2
	}
	if flags.NArg() != 0 || *after < 0 || limit < 1 || limit > 1000 {
		fmt.Fprintln(stderr, "invalid observer position or page size")
		return 2
	}
	if kind == "events" && !*jsonFlag {
		fmt.Fprintln(stderr, "events requires --json")
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	ctx, stop := observerContext()
	defer stop()
	if *follow {
		consume := func(record client.Record) error {
			if kind == "logs" && !*jsonFlag {
				if record.Logs != nil {
					if record.Logs.Gap != nil {
						fmt.Fprintf(stderr, "LOG_GAP: %s; resume with --after %d --identity %s\n", record.Logs.Gap.Reason, record.Logs.Gap.ResumeOffset, record.Logs.Identity)
					}
					_, err := stdout.Write(record.Logs.Data)
					return err
				}
				return nil
			}
			return json.NewEncoder(stdout).Encode(record)
		}
		var err error
		if kind == "events" {
			err = c.FollowEvents(ctx, args[0], client.EventsOptions{After: *after, Limit: limit}, consume)
		} else {
			err = c.FollowLogs(ctx, args[0], client.LogsOptions{After: *after, Identity: identity}, consume)
		}
		if err != nil {
			if kind == "logs" && !*jsonFlag {
				return plainLogFailure(stderr, err)
			}
			return clientFailure(stdout, stderr, client.Envelope{}, err)
		}
		return 0
	}
	if kind == "events" {
		_, envelope, err := c.Events(ctx, args[0], client.EventsOptions{After: *after, Limit: limit})
		if err != nil {
			return clientFailure(stdout, stderr, envelope, err)
		}
		return emit(stdout, stderr, envelope)
	}
	page, envelope, err := c.Logs(ctx, args[0], client.LogsOptions{After: *after, Identity: identity})
	if err != nil {
		if !*jsonFlag {
			return plainLogFailure(stderr, err)
		}
		return clientFailure(stdout, stderr, envelope, err)
	}
	if *jsonFlag {
		return emit(stdout, stderr, envelope)
	}
	if page.Gap != nil {
		fmt.Fprintf(stderr, "LOG_GAP: %s; resume with --after %d --identity %s\n", page.Gap.Reason, page.Gap.ResumeOffset, page.Identity)
	}
	if _, err = stdout.Write(page.Data); err != nil {
		fmt.Fprintln(stderr, err)
		return 6
	}
	if page.More {
		fmt.Fprintf(stderr, "LOG_MORE: resume with --after %d --identity %s\n", page.NextOffset, page.Identity)
	}
	return 0
}
