package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
)

var ErrSocketExists = errors.New("control socket already exists; refusing to replace it")

func Run(ctx context.Context, root state.Root) error {
	if err := root.Prepare(); err != nil {
		return err
	}
	db, err := sqlite.Open(root.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := managed.NewLedger(db, 1); err != nil {
		return fmt.Errorf("initialize managed state: %w", err)
	}
	if _, err := snapshot.OpenStore(root.Path); err != nil {
		return fmt.Errorf("initialize sealed input store: %w", err)
	}
	if _, err := os.Lstat(root.SocketPath); err == nil {
		return fmt.Errorf("%w: %s; see README stale-socket recovery", ErrSocketExists, root.SocketPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect control socket: %w", err)
	}

	listener, cleanup, err := listenPrivate(root.SocketPath, os.Lstat, func(path string) error {
		return os.Chmod(path, 0600)
	})
	if err != nil {
		return err
	}
	defer cleanup()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-done:
		}
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept control connection: %w", err)
		}
		_ = connection.Close()
	}
}

func listenPrivate(path string, inspect func(string) (os.FileInfo, error), protect func(string) error) (*net.UnixListener, func(), error) {
	var listener *net.UnixListener
	var temporary string
	for attempt := 0; attempt < 3; attempt++ {
		var nonce [4]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, nil, fmt.Errorf("name temporary control socket: %w", err)
		}
		temporary = filepath.Join(filepath.Dir(path), ".h"+hex.EncodeToString(nonce[:]))
		var err error
		listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: temporary, Net: "unix"})
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, nil, fmt.Errorf("listen on temporary control socket: %w", err)
		}
	}
	if listener == nil {
		return nil, nil, fmt.Errorf("name temporary control socket: no available name")
	}
	original, err := os.Lstat(temporary)
	if err != nil {
		_ = listener.Close()
		return nil, nil, fmt.Errorf("inspect temporary control socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	removeOwned := func(name string) {
		if current, err := os.Lstat(name); err == nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(original, current) {
			_ = os.Remove(name)
		}
	}
	cleanup := func() {
		_ = listener.Close()
		removeOwned(path)
		removeOwned(temporary)
	}
	if original.Mode()&os.ModeSocket == 0 {
		cleanup()
		return nil, nil, fmt.Errorf("temporary control socket was replaced")
	}
	if err := protect(temporary); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("protect temporary control socket: %w", err)
	}
	// Linking publishes the bound socket without replacing an existing path.
	if err := os.Link(temporary, path); err != nil {
		cleanup()
		if errors.Is(err, os.ErrExist) {
			return nil, nil, fmt.Errorf("%w: %s; see README stale-socket recovery", ErrSocketExists, path)
		}
		return nil, nil, fmt.Errorf("publish control socket: %w", err)
	}
	if published, err := os.Lstat(path); err != nil || !os.SameFile(original, published) {
		cleanup()
		return nil, nil, fmt.Errorf("control socket changed during publication")
	}
	removeOwned(temporary)
	if _, err := inspect(path); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("inspect control socket: %w", err)
	}
	if err := protect(path); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("protect control socket: %w", err)
	}
	return listener, cleanup, nil
}
