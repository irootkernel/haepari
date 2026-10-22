// The controller's native guest fixture uses the production runner without
// initializing the controller's Ginkgo suite on every guest exchange.
package main

import (
	"os"

	"github.com/irootkernel/haepari/internal/runner"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "runner" {
		os.Exit(2)
	}
	os.Exit(runner.Command(os.Args[2:], os.Stdout, os.Stderr))
}
