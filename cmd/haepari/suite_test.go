package main

import (
	"os"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func TestMain(m *testing.M) {
	if os.Getenv("HAEPARI_TEST_DAEMON") == "1" {
		os.Exit(run([]string{"daemon", "run"}, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestHaepari(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Haepari Suite")
}
