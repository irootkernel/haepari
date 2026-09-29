//go:build integration

package controller

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/irootkernel/haepari/internal/state"
)

func TestIsolatedControllerStartup(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root, err := state.Resolve(filepath.Join(parent, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, root) }()
	t.Cleanup(cancel)
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(root.SocketPath); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("controller stopped before startup: %v", err)
		case <-deadline:
			t.Fatal("controller socket did not appear")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if info, err := os.Stat(root.SocketPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("control socket permissions: %v, %v", info, err)
	}
	connection, err := net.Dial("unix", root.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var byteBuf [1]byte
	if _, err := connection.Read(byteBuf[:]); err != io.EOF {
		t.Fatalf("controller accepted an unexpected protocol: %v", err)
	}
	_ = connection.Close()
	if err := Run(context.Background(), root); err == nil {
		t.Fatal("second controller unexpectedly reused socket")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root.SocketPath); !os.IsNotExist(err) {
		t.Fatalf("control socket remains after stop: %v", err)
	}
	if info, err := os.Stat(root.DBPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("SQLite file permissions: %v, %v", info, err)
	}
	if info, err := os.Lstat(filepath.Join(root.Path, "objects")); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("sealed input store permissions: %v, %v", info, err)
	}
}

func TestShutdownPreservesReplacedSocketPath(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root, err := state.Resolve(filepath.Join(parent, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, root) }()
	t.Cleanup(cancel)
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Lstat(root.SocketPath); err == nil {
			if conn, err := net.Dial("unix", root.SocketPath); err == nil {
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				var byteBuf [1]byte
				_, readErr := conn.Read(byteBuf[:])
				_ = conn.Close()
				if readErr == io.EOF {
					break
				}
			}
		}
		select {
		case err := <-done:
			t.Fatalf("controller stopped before startup: %v", err)
		case <-deadline:
			t.Fatal("controller socket did not appear")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := os.Remove(root.SocketPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SocketPath, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(root.SocketPath)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement socket path changed: %q, %v", data, err)
	}
}

func TestSocketProtectionFailureRemovesCreatedSocket(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	path := filepath.Join(parent, "control.sock")
	failedProtection := errors.New("injected protection failure")
	listener, cleanup, err := listenPrivate(path, os.Lstat, func(string) error { return failedProtection })
	if listener != nil || cleanup != nil || !errors.Is(err, failedProtection) {
		t.Fatalf("unexpected listener outcome: %v, %v", listener, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("failed startup left control socket: %v", err)
	}
}

func TestSocketInspectionFailurePreservesReplacement(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	path := filepath.Join(parent, "control.sock")
	failedInspection := errors.New("injected inspection failure")
	listener, cleanup, err := listenPrivate(path, func(path string) (os.FileInfo, error) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
			t.Fatal(err)
		}
		return nil, failedInspection
	}, func(string) error { return nil })
	if listener != nil || cleanup != nil || !errors.Is(err, failedInspection) {
		t.Fatalf("unexpected listener outcome: %v, %v", listener, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("inspection failure changed replacement: %q, %v", data, err)
	}
}

func TestSocketInspectionFailureRemovesOwnedSocket(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	path := filepath.Join(parent, "control.sock")
	failedInspection := errors.New("injected inspection failure")
	listener, cleanup, err := listenPrivate(path, func(string) (os.FileInfo, error) {
		return nil, failedInspection
	}, func(string) error { return nil })
	if listener != nil || cleanup != nil || !errors.Is(err, failedInspection) {
		t.Fatalf("unexpected listener outcome: %v, %v", listener, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("inspection failure left control socket: %v", err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatalf("inspection failure left temporary socket: %v, %v", entries, err)
	}
}

func TestSocketPublicationPreservesExistingPath(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	path := filepath.Join(parent, "control.sock")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, cleanup, err := listenPrivate(path, os.Lstat, func(string) error { return nil })
	if listener != nil || cleanup != nil || !errors.Is(err, ErrSocketExists) {
		t.Fatalf("unexpected listener outcome: %v, %v", listener, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing" {
		t.Fatalf("publication changed existing path: %q, %v", data, err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 1 {
		t.Fatalf("publication left temporary socket: %v, %v", entries, err)
	}
}
