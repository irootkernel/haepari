package runner

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"golang.org/x/sys/unix"
)

const usage = "usage: haepari runner start/inspect/watch/cancel --request <absolute-request.json> [--json]"

// Command is shared by the executable and native integration subprocesses.
func Command(args []string, stdout, stderr io.Writer) int {
	if len(args) == 2 && args[0] == "guest-transport" && args[1] == "--stdio" {
		return GuestTransportCommand(os.Stdin, stdout, stderr)
	}
	if len(args) == 2 && args[0] == "guest-recovery" && args[1] == "--stdio" {
		return GuestRecoveryCommand(os.Stdin, stdout, stderr)
	}
	if len(args) == 2 && args[0] == "guest" && args[1] == "--stdio" {
		return GuestCommand(os.Stdin, stdout, stderr)
	}
	if len(args) < 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	operation := args[0]
	if operation != "start" && operation != "inspect" && operation != "watch" && operation != "cancel" && operation != "_exec" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet("runner "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("request", "", "contained run request")
	_ = flags.Bool("json", false, "structured runner observation")
	if flags.Parse(args[1:]) != nil || *path == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if operation == "_exec" {
		if err := execWorkload(*path); err != nil {
			fmt.Fprintln(stderr, err)
			return 126
		}
		return 0
	}
	var view Observation
	var err error
	switch operation {
	case "start":
		view, err = StartRun(*path)
	case "inspect":
		view, err = Inspect(*path)
	case "cancel":
		err = Cancel(*path)
		if err == nil {
			view, err = Inspect(*path)
		}
	case "watch":
		d, q, e := readRequest(*path)
		if e != nil {
			err = e
			break
		}
		defer d.close()
		budget := max(time.Duration(0), min(time.Until(q.Deadline), time.Duration(q.RunSeconds)*time.Second))
		// Keep observer duration finite even for individually valid very long
		// budgets; duration addition must not wrap into an immediate timeout.
		for _, additional := range []time.Duration{time.Duration(q.TerminationSeconds) * time.Second, time.Second} {
			budget += min(additional, time.Duration(1<<63-1)-budget)
		}
		deadline := time.Now().Add(budget)
		for {
			view, err = inspect(d, q)
			if err != nil || view.Exit != nil {
				break
			}
			if !time.Now().Before(deadline) {
				err = ErrUnproven
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	response := managed.Envelope{Schema: "haepari.envelope/v1", Operation: "runner." + operation, OK: err == nil}
	exitCode := 0
	if err != nil {
		response.Error = runnerError(err)
		exitCode, _ = managed.ExitCode(response.Error.Code)
		fmt.Fprintln(stderr, err)
	} else {
		response.Data, _ = json.Marshal(view)
	}
	if err := json.NewEncoder(stdout).Encode(response); err != nil {
		fmt.Fprintln(stderr, err)
		return 6
	}
	return exitCode
}

func runnerError(err error) *managed.MachineError {
	code, retry := managed.StorageUnavailable, "reconcile"
	message := "runner storage or native observation is unavailable; preserve receipts and reconcile"
	if errors.Is(err, ErrInvalid) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
		code, retry = managed.InvalidRequest, "none"
		message = "request must name an existing absolute, private runner request with valid receipts"
	}
	if errors.Is(err, ErrConflict) {
		code, retry = managed.Conflict, "none"
		message = ErrConflict.Error()
	}
	if errors.Is(err, ErrAmbiguous) || errors.Is(err, ErrUnproven) {
		code, retry = managed.RecoveryBlocked, "reconcile"
		message = ErrUnproven.Error()
		if errors.Is(err, ErrAmbiguous) {
			message = ErrAmbiguous.Error()
		}
	}
	return &managed.MachineError{Code: code, Message: message, Retry: retry}
}
