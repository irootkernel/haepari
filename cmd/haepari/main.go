package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/irootkernel/haepari/internal/controller"
	"github.com/irootkernel/haepari/internal/identity"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
)

type envelope struct {
	Schema    string `json:"schema"`
	Operation string `json:"operation"`
	OK        bool   `json:"ok"`
	Data      any    `json:"data,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: haepari version --json | capabilities --json | daemon run [--backend local]")
		return 2
	}
	switch args[0] {
	case "version":
		if len(args) != 2 || args[1] != "--json" {
			fmt.Fprintln(stderr, "usage: haepari version --json")
			return 2
		}
		info, err := identity.Read()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 6
		}
		return writeJSON(stdout, stderr, "version", info)
	case "capabilities":
		if len(args) != 2 || args[1] != "--json" {
			fmt.Fprintln(stderr, "usage: haepari capabilities --json")
			return 2
		}
		return writeJSON(stdout, stderr, "capabilities", struct {
			CompiledBackends  []string `json:"compiled_backends"`
			QualifiedBackends []string `json:"qualified_backends"`
			Protocols         []string `json:"protocol_versions"`
		}{platform.CompiledBackends(), []string{}, []string{}})
	case "daemon":
		if len(args) < 2 || args[1] != "run" {
			fmt.Fprintln(stderr, "usage: haepari daemon run [--backend local]")
			return 2
		}
		flags := flag.NewFlagSet("daemon run", flag.ContinueOnError)
		flags.SetOutput(stderr)
		backend := flags.String("backend", platform.DefaultBackend, "bootstrap backend")
		if err := flags.Parse(args[2:]); err != nil {
			return 2
		}
		if flags.NArg() != 0 {
			fmt.Fprintln(stderr, "usage: haepari daemon run [--backend local]")
			return 2
		}
		if err := platform.Validate(runtime.GOOS, runtime.GOARCH, *backend); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		root, err := state.Resolve(os.Getenv("HAEPARI_HOME"))
		if err != nil {
			fmt.Fprintln(stderr, err)
			if errors.Is(err, state.ErrInvalidRoot) {
				return 2
			}
			return 6
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		if err := controller.Run(ctx, root); err != nil {
			fmt.Fprintln(stderr, err)
			if errors.Is(err, state.ErrInvalidRoot) || errors.Is(err, controller.ErrSocketExists) || errors.Is(err, sqlite.ErrInvalidStore) {
				return 2
			}
			return 6
		}
		return 0
	case "runner":
		fmt.Fprintln(stderr, "runner protocol is not available until TASK-033")
		return 2
	default:
		fmt.Fprintf(stderr, "unsupported command %q\n", args[0])
		return 2
	}
}

func writeJSON(w, stderr io.Writer, operation string, data any) int {
	if err := json.NewEncoder(w).Encode(envelope{
		Schema: "haepari.envelope/v1", Operation: operation, OK: true, Data: data,
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 6
	}
	return 0
}
