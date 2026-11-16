package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/relocation"
)

func relocationEnvironment() (relocation.Environment, error) {
	binary, err := os.Executable()
	if err != nil {
		return relocation.Environment{}, err
	}
	return relocation.Environment{MoverBinary: binary, ValidateService: func(p relocation.ServicePlan) error {
		current, err := recreateDevelopmentServicePlan(p)
		if err != nil {
			return err
		}
		if current != p {
			return managed.ErrConflict
		}
		return nil
	}, ServicesStopped: func(p relocation.ServicePlan) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "/bin/launchctl", "print", p.Domain+"/"+p.Label).CombinedOutput()
		if err == nil {
			return managed.ErrBlocked
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 113 && strings.Contains(string(output), "Could not find service") {
			return nil
		}
		return errors.New("native service observation unavailable")
	}}, nil
}
func relocateCommand(args []string, stdout, stderr io.Writer) int {
	return relocateCommandUsing(args, stdout, stderr, relocationEnvironment)
}
func relocateCommandUsing(args []string, stdout, stderr io.Writer, environmentProvider func() (relocation.Environment, error)) int {
	if len(args) == 0 {
		return 2
	}
	action := args[0]
	if action != "plan" && action != "apply" && action != "status" && action != "rollback" {
		return 2
	}
	flags := flag.NewFlagSet("service relocate "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source-plan", "", "exact source service plan")
	target := flags.String("target-plan", "", "exact target service plan")
	backup := flags.String("backup-dir", "", "absent private backup directory")
	file := flags.String("plan-file", "", "original relocation plan")
	machine := flags.Bool("json", false, "JSON response")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !*machine {
		return 2
	}
	visited := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if action == "plan" {
		if *source == "" || *target == "" || *backup == "" || visited["plan-file"] {
			return 2
		}
	} else if *file == "" || visited["source-plan"] || visited["target-plan"] || visited["backup-dir"] {
		return 2
	}
	var value any
	environment, err := environmentProvider()
	if err == nil && action == "plan" {
		var a, b developmentServicePlan
		a, err = readDevelopmentServicePlan(*source, true)
		if err == nil {
			b, err = readDevelopmentServicePlan(*target, true)
		}
		if err == nil {
			value, err = relocation.NewPlan(a, b, *backup, environment)
		}
	} else if err == nil {
		var raw []byte
		raw, err = readRelocationPlan(*file)
		if err == nil {
			var p relocation.Plan
			p, err = relocation.DecodePlan(raw)
			if err == nil {
				value, err = relocation.Operate(p, action, environment)
			}
		}
	}
	if err == nil {
		return writeJSON(stdout, stderr, "service.relocate."+action, value)
	}
	code := managed.StorageUnavailable
	retry := "reconcile"
	if errors.Is(err, managed.ErrInvalid) || errors.Is(err, managed.ErrUnsupported) {
		code = managed.InvalidRequest
		retry = "none"
	} else if errors.Is(err, managed.ErrConflict) {
		code = managed.Conflict
		retry = "none"
	} else if errors.Is(err, managed.ErrBlocked) || errors.Is(err, managed.ErrBusy) {
		code = managed.RecoveryBlocked
	}
	message := "relocation refused at input_observation"
	var refusal *relocation.Error
	if errors.As(err, &refusal) {
		message = refusal.Error()
	}
	issue := &managed.MachineError{Code: code, Message: message, Retry: retry}
	if json.NewEncoder(stdout).Encode(managed.Envelope{Schema: "haepari.envelope/v1", Operation: "service.relocate." + action, OK: false, Error: issue}) != nil {
		return 6
	}
	exit, _ := managed.ExitCode(code)
	return exit
}

// Relocation plans contain the complete retained inventory and use their own
// reader. The managed Job/spec size policy remains in readSpec unchanged.
func readRelocationPlan(name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("relocation plan file required")
	}
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("relocation plan must be a regular file")
	}
	return os.ReadFile(name)
}
