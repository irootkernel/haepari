package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/irootkernel/haepari/internal/managed"
)

// PreparationSteps is private argv carried by one ordinary supervised run.
// Children inherit its owned process group; there is no second supervisor.
type PreparationSteps struct {
	Setup    []string `json:"setup,omitempty"`
	Workload []string `json:"workload"`
}

const manualPreparationVerb = "_manual-prepare"
const manualPreparationFlag = "--steps-json"

// PreparationArgv and Command share ownership of this private transport.
func PreparationArgv(executable string, steps PreparationSteps) ([]string, error) {
	raw, err := json.Marshal(steps)
	if err != nil {
		return nil, err
	}
	return []string{executable, "runner", manualPreparationVerb, manualPreparationFlag, string(raw)}, nil
}

func manualPrepareCommand(raw string, stderr io.Writer) int {
	chain, err := managed.DecodeParentChain(os.Getenv(managed.ParentContextEnvironment))
	if err != nil || len(chain) == 0 || len(raw) > 1<<20 || managed.ValidateUniqueJSONKeys([]byte(raw)) != nil {
		fmt.Fprintln(stderr, "manual preparation requires its managed Job/lease context and structured steps")
		return 126
	}
	var steps PreparationSteps
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if d.Decode(&steps) != nil || d.Decode(new(any)) != io.EOF || len(steps.Workload) == 0 || len(steps.Workload) > 128 || len(steps.Setup) > 128 {
		fmt.Fprintln(stderr, "invalid manual preparation steps")
		return 126
	}
	for _, argv := range [][]string{steps.Setup, steps.Workload} {
		for index, arg := range argv {
			if index == 0 && arg == "" || strings.ContainsRune(arg, 0) {
				fmt.Fprintln(stderr, "invalid manual preparation argument")
				return 126
			}
		}
	}
	work, err := os.Getwd()
	if err != nil {
		return 126
	}
	if len(steps.Setup) > 0 {
		binary, err := resolveCommand(steps.Setup[0], work, os.Environ())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 126
		}
		command := exec.Command(binary, steps.Setup[1:]...)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					_ = syscall.Kill(os.Getpid(), status.Signal())
					return 128 + int(status.Signal())
				}
				return exit.ExitCode()
			}
			fmt.Fprintln(stderr, err)
			return 126
		}
	}
	binary, err := resolveCommand(steps.Workload[0], work, os.Environ())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 126
	}
	if err := syscall.Exec(binary, steps.Workload, os.Environ()); err != nil {
		fmt.Fprintln(stderr, err)
		return 126
	}
	return 0
}
