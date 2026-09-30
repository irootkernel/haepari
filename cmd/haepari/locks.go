package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
)

func lockCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: haepari lock acquire/wait/renew/keepalive/release/cancel <id> [options]")
		return 2
	}
	action, id := args[0], args[1]
	if !managed.ValidRequestKey(id) {
		fmt.Fprintln(stderr, "invalid lock, request or grant ID")
		return 2
	}
	flags := flag.NewFlagSet("lock "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonFlag := flags.Bool("json", false, "JSON response")
	var key, holder, reason *string
	var queue *int64
	var wait, force *bool
	var timeout *time.Duration
	switch action {
	case "acquire":
		key = flags.String("request-key", "", "durable caller request key")
		holder = flags.String("holder", "", "holder label")
		queue = flags.Int64("queue-seconds", 0, "optional queue deadline")
		wait = flags.Bool("wait", false, "wait for grant")
		timeout = flags.Duration("timeout", 5*time.Minute, "caller wait deadline")
	case "wait":
		timeout = flags.Duration("timeout", 5*time.Minute, "caller wait deadline")
	case "release", "cancel":
		force = flags.Bool("force", false, "operator action")
		reason = flags.String("reason", "", "operator reason")
	case "renew", "keepalive":
	default:
		fmt.Fprintln(stderr, "unknown lock action")
		return 2
	}
	if flags.Parse(args[2:]) != nil || flags.NArg() != 0 || (action != "keepalive" && !*jsonFlag) {
		return 2
	}
	if timeout != nil && (*timeout <= 0 || *timeout > 24*time.Hour) {
		fmt.Fprintln(stderr, "timeout must be positive and at most 24h")
		return 2
	}
	var queueSeconds *int64
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "queue-seconds" {
			queueSeconds = queue
		}
	})
	if action == "acquire" && (!managed.ValidRequestKey(*key) || *holder == "" || queueSeconds != nil && *queueSeconds <= 0) {
		fmt.Fprintln(stderr, "acquire requires --request-key, --holder and a positive optional --queue-seconds")
		return 2
	}
	if force != nil && ((*force && *reason == "") || (!*force && *reason != "")) {
		fmt.Fprintln(stderr, "--force requires --reason; reason is only for force actions")
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var reply managed.LockReply
	var envelope client.Envelope
	var err error
	switch action {
	case "acquire":
		reply, envelope, err = c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: id, RequestKey: *key, Holder: *holder, QueueSeconds: queueSeconds})
		if err == nil && *wait && reply.Current.State == "queued" {
			reply, envelope, err = c.WaitLock(ctx, reply.Current.RequestID, *timeout)
		}
	case "wait":
		reply, envelope, err = c.WaitLock(ctx, id, *timeout)
	case "renew":
		reply, envelope, err = c.RenewLock(ctx, id)
	case "release":
		reply, envelope, err = c.ReleaseLock(ctx, id, *force, *reason)
	case "cancel":
		reply, envelope, err = c.CancelLock(ctx, id, *force, *reason)
	case "keepalive":
		return keepLockAlive(ctx, c, id, *jsonFlag, stdout, stderr)
	}
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	if code := emit(stdout, stderr, envelope); code != 0 {
		return code
	}
	if reply.Current.State == "queued" || reply.Current.State == "granted" || (action == "cancel" || action == "release") && reply.Receipt.Operation == action && reply.Receipt.Force == *force && reply.Receipt.Reason == *reason {
		return 0
	}
	return 4
}

func keepLockAlive(ctx context.Context, c *client.Client, id string, jsonFlag bool, stdout, stderr io.Writer) int {
	for {
		reply, envelope, err := c.RenewLock(ctx, id)
		if err != nil {
			if jsonFlag {
				return clientFailure(stdout, stderr, envelope, err)
			}
			return plainLogFailure(stderr, err)
		}
		if reply.Current.State != "granted" || reply.Current.LivenessSeconds <= 0 || reply.Current.LivenessDeadline == nil || reply.Current.MaxHoldDeadline == nil {
			fmt.Fprintln(stderr, "controller returned an invalid advisory renewal")
			return 6
		}
		if jsonFlag {
			if code := emit(stdout, stderr, envelope); code != 0 {
				return code
			}
		}
		interval := time.Duration(reply.Current.LivenessSeconds) * time.Second / 3
		interval = min(interval, time.Until(*reply.Current.LivenessDeadline), time.Until(*reply.Current.MaxHoldDeadline))
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			fmt.Fprintln(stderr, "keepalive interrupted; renewal stopped")
			return 6
		case <-timer.C:
		}
	}
}

func locksList(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "usage: haepari locks list [--lock <id>] --json")
		return 2
	}
	flags := flag.NewFlagSet("locks list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	lock := flags.String("lock", "", "lock resource filter")
	jsonFlag := flags.Bool("json", false, "JSON response")
	limit := flags.Int("limit", 50, "bounded page size,1..100")
	cursor := flags.String("cursor", "", "page continuation")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !*jsonFlag || *limit < 1 || *limit > 100 || *lock != "" && !managed.ValidRequestKey(*lock) {
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	_, envelope, err := c.LocksPage(context.Background(), *lock, *cursor, *limit)
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}
