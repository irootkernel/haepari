//go:build integration

package controller

import (
	"context"
	"errors"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

var _ = ginkgo.Describe("IsolatedControllerStartup", ginkgo.Label("integration"), func() {
	ginkgo.It("IsolatedControllerStartup", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root, err := state.Resolve(filepath.Join(parent, "state"))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
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
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		var byteBuf [1]byte
		if _, err := connection.Write([]byte("GET /v1/hello HTTP/1.1\r\nHost: haepari\r\n\r\n")); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := connection.Read(byteBuf[:]); err != nil {
			t.Fatalf("controller did not answer handshake: %v", err)
		}
		_ = connection.Close()
		if err := Run(context.Background(), root); err == nil {
			t.Fatal("second controller unexpectedly reused socket")
		}
		cancel()
		if err := <-done; err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
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

	})
})

var _ = ginkgo.Describe("ShutdownPreservesReplacedSocketPath", ginkgo.Label("integration"), func() {
	ginkgo.It("ShutdownPreservesReplacedSocketPath", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root, err := state.Resolve(filepath.Join(parent, "state"))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
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
					_, _ = conn.Write([]byte("GET /v1/hello HTTP/1.1\r\nHost: haepari\r\n\r\n"))
					var byteBuf [1]byte
					_, readErr := conn.Read(byteBuf[:])
					_ = conn.Close()
					if readErr == nil {
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
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.WriteFile(root.SocketPath, []byte("replacement"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		cancel()
		if err := <-done; err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		data, err := os.ReadFile(root.SocketPath)
		if err != nil || string(data) != "replacement" {
			t.Fatalf("replacement socket path changed: %q, %v", data, err)
		}

	})
})

var _ = ginkgo.Describe("SocketProtectionFailureRemovesCreatedSocket", ginkgo.Label("integration"), func() {
	ginkgo.It("SocketProtectionFailureRemovesCreatedSocket", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		path := filepath.Join(parent, "control.sock")
		failedProtection := errors.New("injected protection failure")
		listener, cleanup, err := listenPrivate(path, os.Lstat, func(string) error { return failedProtection }, testSocketOwnership())
		if listener != nil || cleanup != nil || !errors.Is(err, failedProtection) {
			t.Fatalf("unexpected listener outcome: %v, %v", listener, err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("failed startup left control socket: %v", err)
		}

	})
})

var _ = ginkgo.Describe("SocketInspectionFailurePreservesReplacement", ginkgo.Label("integration"), func() {
	ginkgo.It("SocketInspectionFailurePreservesReplacement", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		path := filepath.Join(parent, "control.sock")
		failedInspection := errors.New("injected inspection failure")
		listener, cleanup, err := listenPrivate(path, func(path string) (os.FileInfo, error) {
			if err := os.Remove(path); err != nil {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			return nil, failedInspection
		}, func(string) error { return nil }, testSocketOwnership())
		if listener != nil || cleanup != nil || !errors.Is(err, failedInspection) {
			t.Fatalf("unexpected listener outcome: %v, %v", listener, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "replacement" {
			t.Fatalf("inspection failure changed replacement: %q, %v", data, err)
		}

	})
})

var _ = ginkgo.Describe("SocketInspectionFailureRemovesOwnedSocket", ginkgo.Label("integration"), func() {
	ginkgo.It("SocketInspectionFailureRemovesOwnedSocket", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		path := filepath.Join(parent, "control.sock")
		failedInspection := errors.New("injected inspection failure")
		listener, cleanup, err := listenPrivate(path, func(string) (os.FileInfo, error) {
			return nil, failedInspection
		}, func(string) error { return nil }, testSocketOwnership())
		if listener != nil || cleanup != nil || !errors.Is(err, failedInspection) {
			t.Fatalf("unexpected listener outcome: %v, %v", listener, err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("inspection failure left control socket: %v", err)
		}
		if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
			t.Fatalf("inspection failure left temporary socket: %v, %v", entries, err)
		}

	})
})

var _ = ginkgo.Describe("SocketPublicationPreservesExistingPath", ginkgo.Label("integration"), func() {
	ginkgo.It("SocketPublicationPreservesExistingPath", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		path := filepath.Join(parent, "control.sock")
		if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		listener, cleanup, err := listenPrivate(path, os.Lstat, func(string) error { return nil }, testSocketOwnership())
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

	})
})

var _ = ginkgo.Describe("StaleSocketRecovery", ginkgo.Label("integration"), func() {
	ginkgo.It("recovers a refused socket but preserves reachable and foreign paths", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: root.SocketPath, Net: "unix"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		listener.SetUnlinkOnClose(false)
		original, err := os.Lstat(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(errors.Is(Run(context.Background(), root), ErrSocketExists)).To(gomega.BeTrue())
		current, err := os.Lstat(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.SameFile(original, current)).To(gomega.BeTrue())
		gomega.Expect(listener.Close()).To(gomega.Succeed())
		ownership, err := root.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(recordSocketOwnership(ownership, original)).To(gomega.Succeed())
		gomega.Expect(ownership.Close()).To(gomega.Succeed())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- Run(ctx, root) }()
		deadline := time.Now().Add(5 * time.Second)
		recovered := false
		for time.Now().Before(deadline) {
			current, err = os.Lstat(root.SocketPath)
			if err == nil && !os.SameFile(original, current) {
				recovered = true
				break
			}
			select {
			case err := <-done:
				ginkgo.Fail("controller failed: " + err.Error())
			case <-time.After(10 * time.Millisecond):
			}
		}
		gomega.Expect(recovered).To(gomega.BeTrue())
		cancel()
		gomega.Expect(<-done).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(root.SocketPath, []byte("foreign"), 0600)).To(gomega.Succeed())
		gomega.Expect(errors.Is(Run(context.Background(), root), ErrSocketExists)).To(gomega.BeTrue())
		data, err := os.ReadFile(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("foreign"))
	})
	ginkgo.It("races two controllers for exclusive startup without replacing the winner", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 2)
		start := make(chan struct{})
		for range 2 {
			go func() { <-start; done <- Run(ctx, root) }()
		}
		close(start)
		select {
		case err := <-done:
			gomega.Expect(errors.Is(err, state.ErrControllerOwned)).To(gomega.BeTrue())
		case <-time.After(5 * time.Second):
			ginkgo.Fail("competing controllers did not resolve")
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Lstat(root.SocketPath); err == nil {
				break
			}
			if time.Now().After(deadline) {
				ginkgo.Fail("winner did not publish socket")
			}
			time.Sleep(10 * time.Millisecond)
		}
		winner, err := os.Lstat(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(errors.Is(Run(context.Background(), root), state.ErrControllerOwned)).To(gomega.BeTrue())
		current, err := os.Lstat(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.SameFile(winner, current)).To(gomega.BeTrue())
		cancel()
		gomega.Expect(<-done).To(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("ControllerOwnershipRefusal", ginkgo.Label("integration"), func() {
	ginkgo.It("refuses before database or socket effects", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		owner, err := root.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer owner.Close()
		gomega.Expect(errors.Is(Run(context.Background(), root), state.ErrControllerOwned)).To(gomega.BeTrue())
		for _, path := range []string{root.DBPath, root.SocketPath, filepath.Join(root.Path, "objects"), filepath.Join(root.Path, "jobs")} {
			_, err := os.Lstat(path)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})
	ginkgo.It("preserves an unmarked live listener even with a full accept backlog", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: root.SocketPath, Net: "unix"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		listener.SetUnlinkOnClose(false)
		defer listener.Close()
		raw, err := listener.SyscallConn()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var listenErr error
		gomega.Expect(raw.Control(func(fd uintptr) { listenErr = syscall.Listen(int(fd), 1) })).To(gomega.Succeed())
		gomega.Expect(listenErr).NotTo(gomega.HaveOccurred())
		connections := []net.Conn{}
		defer func() {
			for _, c := range connections {
				_ = c.Close()
			}
		}()
		var refused error
		for range 16 {
			c, err := net.DialTimeout("unix", root.SocketPath, 100*time.Millisecond)
			if err != nil {
				refused = err
				break
			}
			connections = append(connections, c)
		}
		gomega.Expect(refused).To(gomega.HaveOccurred())
		if runtime.GOOS == "darwin" {
			gomega.Expect(errors.Is(refused, syscall.ECONNREFUSED)).To(gomega.BeTrue())
		}
		original, err := os.Lstat(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(errors.Is(Run(context.Background(), root), ErrSocketExists)).To(gomega.BeTrue())
		current, err := os.Lstat(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.SameFile(original, current)).To(gomega.BeTrue())
		gomega.Expect(listener.Close()).To(gomega.Succeed())
		gomega.Expect(errors.Is(Run(context.Background(), root), ErrSocketExists)).To(gomega.BeTrue())
	})
	ginkgo.It("preserves a replacement during known-socket recovery", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		owner, err := root.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer owner.Close()
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: root.SocketPath, Net: "unix"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		listener.SetUnlinkOnClose(false)
		original, err := os.Lstat(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(recordSocketOwnership(owner, original)).To(gomega.Succeed())
		gomega.Expect(listener.Close()).To(gomega.Succeed())
		count := 0
		inspect := func(path string) (os.FileInfo, error) {
			count++
			if count == 2 {
				if err := os.Remove(path); err != nil {
					return nil, err
				}
				if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
					return nil, err
				}
			}
			return os.Lstat(path)
		}
		gomega.Expect(errors.Is(recoverStaleSocket(root.SocketPath, owner, inspect), ErrSocketExists)).To(gomega.BeTrue())
		data, err := os.ReadFile(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("replacement"))
	})
})

func testSocketOwnership() *os.File {
	t := ginkgo.GinkgoT()
	file, err := (state.Root{Path: t.TempDir()}).AcquireController()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(func() { _ = file.Close() })
	return file
}

var _ = ginkgo.Describe("SocketOwnershipRecordFailure", ginkgo.Label("integration"), func() {
	ginkgo.It("refuses missing ownership and cleans an unpublished socket on record failure", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		path := filepath.Join(parent, "control.sock")
		ownership := testSocketOwnership()
		gomega.Expect(ownership.Close()).To(gomega.Succeed())
		for _, file := range []*os.File{nil, ownership} {
			listener, cleanup, err := listenPrivate(path, os.Lstat, func(string) error { return nil }, file)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(listener).To(gomega.BeNil())
			gomega.Expect(cleanup).To(gomega.BeNil())
			entries, err := os.ReadDir(parent)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(entries).To(gomega.BeEmpty())
		}
	})
})
