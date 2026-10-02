package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
)

var ErrSocketExists = errors.New("control socket already exists; refusing to replace it")

func Run(ctx context.Context, root state.Root) error {
	if err := root.Prepare(); err != nil {
		return err
	}
	ownership, err := root.AcquireController()
	if err != nil {
		return err
	}
	releaseHere := true
	defer func() {
		if releaseHere {
			_ = ownership.Close()
		}
	}()
	if err := recoverStaleSocket(root.SocketPath, ownership, os.Lstat); err != nil {
		return err
	}
	db, err := sqlite.Open(root.DBPath)
	if err != nil {
		return err
	}
	defer func() {
		if releaseHere {
			_ = db.Close()
		}
	}()
	ledger, err := managed.NewLedger(db, 1)
	if err != nil {
		return fmt.Errorf("initialize managed state: %w", err)
	}
	store, err := snapshot.OpenStore(root.Path)
	if err != nil {
		return fmt.Errorf("initialize sealed input store: %w", err)
	}
	jobsRoot := filepath.Join(root.Path, "jobs")
	if err := os.Mkdir(jobsRoot, 0700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create jobs root: %w", err)
	}
	if _, err := inspectPrivateJobsRoot(jobsRoot); err != nil {
		return fmt.Errorf("jobs root is not a private real directory: %w", err)
	}
	listener, cleanup, err := listenPrivate(root.SocketPath, os.Lstat, func(path string) error {
		return os.Chmod(path, 0600)
	}, ownership)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := ledger.MarkInterrupted(); err != nil {
		return fmt.Errorf("reconcile interrupted jobs: %w", err)
	}
	runtime, err := newRuntime(root, ledger, store)
	if err != nil {
		return fmt.Errorf("bind installation identity: %w", err)
	}
	// A graceful listener exit detaches observers, not the independent runner.
	// Retain the kernel ownership lock and database until in-process workers
	// actually stop, including a filesystem worker that outlived its observer.
	workerCtx, stopWorkers := context.WithCancel(ctx)
	releaseHere = false
	defer func() {
		runtime.detachWorkers()
		stopWorkers()
		stopped := make(chan struct{})
		go func() { runtime.workers.Wait(); close(stopped) }()
		// Ordinary scheduler/observer shutdown releases ownership before Run
		// returns. A worker that cannot stop within this bounded observation
		// keeps the database and kernel lock until it actually completes.
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-stopped:
			_ = db.Close()
			_ = ownership.Close()
		case <-timer.C:
			go func() { <-stopped; _ = db.Close(); _ = ownership.Close() }()
		}
	}()
	ledger.SetLockClock(platform.AdvisoryLockClock)
	if err := ledger.LockStartup(); err != nil {
		return fmt.Errorf("reconcile advisory locks: %w", err)
	}
	if err := runtime.reconcileStartup(); err != nil {
		return fmt.Errorf("observe interrupted executions: %w", err)
	}
	server := &http.Server{Handler: runtime.routes(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-done:
		}
	}()
	runtime.startWorker(func() { runtime.schedule(workerCtx) })
	runtime.startWorker(func() { runtime.scheduleLocks(workerCtx) })
	err = server.Serve(listener)
	if ctx.Err() != nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return fmt.Errorf("serve control socket: %w", err)
}

type socketIdentity struct {
	Schema     string `json:"schema"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
	ModifiedNS int64  `json:"modified_ns"`
}

func identityOf(info os.FileInfo) socketIdentity {
	stat := info.Sys().(*syscall.Stat_t)
	return socketIdentity{Schema: "haepari.controller-socket/v1", Device: uint64(stat.Dev), Inode: uint64(stat.Ino), ModifiedNS: info.ModTime().UnixNano()}
}

// Record the bound inode before publication, while holding the controller lock.
// Failure or partial writing prevents publication. A malformed record on restart
// fails closed; it never authorizes deleting an unknown socket.
func recordSocketOwnership(ownership *os.File, info os.FileInfo) error {
	data, err := json.Marshal(identityOf(info))
	if err != nil {
		return err
	}
	if err := ownership.Truncate(0); err != nil {
		return err
	}
	if _, err := ownership.WriteAt(data, 0); err != nil {
		return err
	}
	return ownership.Sync()
}

// A released kernel lock and its recorded inode prove that the prior cooperating
// controller is gone. ECONNREFUSED alone is insufficient on Darwin: even a live
// listener can refuse connections when its accept backlog is full.
func recoverStaleSocket(path string, ownership *os.File, inspect func(string) (os.FileInfo, error)) error {
	original, err := inspect(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect control socket: %w", err)
	}
	if original.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: %s is a foreign non-socket path", ErrSocketExists, path)
	}
	data, err := io.ReadAll(io.NewSectionReader(ownership, 0, 4097))
	var recorded socketIdentity
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &recorded) != nil || recorded != identityOf(original) {
		return fmt.Errorf("%w: %s ownership is unknown; confirm the prior controller is stopped before manual recovery", ErrSocketExists, path)
	}
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return fmt.Errorf("%w: %s is reachable", ErrSocketExists, path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w: cannot establish stale socket: %v", ErrSocketExists, err)
	}
	current, err := inspect(path)
	if err != nil || !os.SameFile(original, current) {
		return fmt.Errorf("%w: socket changed during recovery", ErrSocketExists)
	}
	return os.Remove(path)
}

func listenPrivate(path string, inspect func(string) (os.FileInfo, error), protect func(string) error, ownership *os.File) (*net.UnixListener, func(), error) {
	if ownership == nil {
		return nil, nil, fmt.Errorf("controller ownership is required before socket publication")
	}
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
	if err := recordSocketOwnership(ownership, original); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("record control socket ownership: %w", err)
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
