//go:build integration

package controller

import (
	"github.com/irootkernel/haepari/internal/runner"
	"os"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "runner" {
		os.Exit(runner.Command(os.Args[2:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestController(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Controller Suite")
}
