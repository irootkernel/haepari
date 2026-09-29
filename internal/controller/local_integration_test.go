//go:build integration

package controller

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCollectorRejectsSymlinkedOutput(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "results"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "results", "output.txt")); err != nil {
		t.Fatal(err)
	}
	artifacts, complete, expired := collectOutputs(workspace, filepath.Join(parent, "artifacts"), []string{"results/output.txt"}, time.Now().Add(time.Second))
	if complete || expired || len(artifacts) != 0 {
		t.Fatalf("followed symlinked result: %+v", artifacts)
	}
	if _, err := os.Stat(filepath.Join(parent, "artifacts", "results", "output.txt")); !os.IsNotExist(err) {
		t.Fatalf("external file was collected: %v", err)
	}
}

func TestOwnedBackgroundGroupIsStopped(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 >/dev/null 2>&1 &")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := ensureGroupStopped(pgid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("owned group still present: %v", err)
	}
}

func TestCollectorDoesNotBlockOnFIFO(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "results"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(workspace, "results", "output.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		artifacts, complete, expired := collectOutputs(workspace, filepath.Join(parent, "artifacts"), []string{"results/output.txt"}, time.Now().Add(time.Second))
		done <- (!complete && !expired && len(artifacts) == 0)
	}()
	select {
	case correct := <-done:
		if !correct {
			t.Fatal("FIFO accepted as a regular artifact")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO opened in blocking mode")
	}
}

func TestCollectionAndCleanupDeadlines(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "results"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "results", "output.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	artifacts, complete, expired := collectOutputs(workspace, filepath.Join(parent, "artifacts"), []string{"results/output.txt"}, time.Now().Add(-time.Second))
	if complete || !expired || len(artifacts) != 0 {
		t.Fatalf("expired collection accepted: %+v, %t, %t", artifacts, complete, expired)
	}
	if err := removeTreeUntil(workspace, time.Now().Add(-time.Second)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "results", "output.txt")); err != nil {
		t.Fatalf("deadline removed protected output: %v", err)
	}
	if err := removeTreeUntil(workspace, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("bounded cleanup: %v", err)
	}
	if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
		t.Fatalf("cleanup left workspace: %v", err)
	}
}

func TestBlockedPhaseReturnsUnprovenAtDeadline(t *testing.T) {
	blocked := make(chan struct{})
	finished := make(chan struct{})
	start := time.Now()
	_, status := boundedPhase(start.Add(20*time.Millisecond), func() error {
		<-blocked
		close(finished)
		return nil
	})
	if status != phaseUnproven || time.Since(start) > time.Second {
		t.Fatal("blocked filesystem phase was reported stopped or exceeded watchdog budget")
	}
	select {
	case <-finished:
		t.Fatal("phase was reported unproven but already completed")
	default:
	}
	close(blocked)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("phase worker did not finish after release")
	}
}

func TestLateCompletedPhaseIsNotVerified(t *testing.T) {
	deadline := time.Now()
	if got := completionStatus(deadline.Add(time.Nanosecond), deadline); got != phaseLate {
		t.Fatalf("late completion status: %v", got)
	}
	if got := completionStatus(deadline.Add(-time.Nanosecond), deadline); got != phaseInTime {
		t.Fatalf("on-time completion status: %v", got)
	}
}
