package runner

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

const receiptLimit = 2 << 20

type directory struct{ file *os.File }

// Open every path component without following links. File operations remain
// anchored to this directory even if a path is renamed by same-user code.
func openDirectory(path string) (*directory, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrInvalid
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	if err := privateFile(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &directory{os.NewFile(uintptr(fd), path)}, nil
}

func privateFile(fd int, dir bool) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	kind := uint32(unix.S_IFREG)
	if dir {
		kind = unix.S_IFDIR
	}
	if uint32(st.Mode)&unix.S_IFMT != kind || st.Mode&0077 != 0 || st.Uid != uint32(os.Geteuid()) || (!dir && st.Nlink != 1) {
		return ErrInvalid
	}
	return nil
}

func (d *directory) close() { _ = d.file.Close() }
func (d *directory) open(name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(d.file.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	if err := privateFile(fd, false); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func (d *directory) read(name string, into any) error {
	f, err := d.open(name, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, receiptLimit+1))
	if err != nil {
		return err
	}
	if len(data) > receiptLimit || managed.ValidateUniqueJSONKeys(data) != nil {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(into) != nil {
		return ErrInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	canonical, err := json.Marshal(into)
	if err != nil || !canonicalKeys(data, canonical) {
		return ErrInvalid
	}
	return nil
}

// encoding/json accepts case aliases. A receipt accepts only the exact keys
// emitted by its typed schema, including the flattened binding fields.
func canonicalKeys(raw, canonical []byte) bool {
	var input, expected any
	if json.Unmarshal(raw, &input) != nil || json.Unmarshal(canonical, &expected) != nil {
		return false
	}
	var check func(any, any) bool
	check = func(a, b any) bool {
		switch value := a.(type) {
		case map[string]any:
			object, ok := b.(map[string]any)
			if !ok || len(value) != len(object) {
				return false
			}
			for key, child := range value {
				other, found := object[key]
				if !found || !check(child, other) {
					return false
				}
			}
		case []any:
			items, ok := b.([]any)
			if !ok || len(items) != len(value) {
				return false
			}
			for i, child := range value {
				if !check(child, items[i]) {
					return false
				}
			}
		case nil:
			return b == nil
		case bool:
			_, ok := b.(bool)
			return ok
		case string:
			_, ok := b.(string)
			return ok
		case float64:
			_, ok := b.(float64)
			return ok
		}
		return true
	}
	return check(input, expected)
}

// Receipts are immutable and written file-sync -> rename -> directory-sync.
// The supervisor holds run.lock; cancellation writers hold cancel.lock.
func (d *directory) write(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > receiptLimit {
		return ErrInvalid
	}
	if f, err := d.open(name, unix.O_RDONLY); err == nil {
		prior, readErr := io.ReadAll(io.LimitReader(f, receiptLimit+1))
		f.Close()
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(prior, data) {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := ".receipt-" + hex.EncodeToString(nonce[:])
	f, err := d.open(temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(d.file.Fd()), temporary, 0)
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := platform.PublishExclusive(int(d.file.Fd()), temporary, name); errors.Is(err, unix.EEXIST) {
		// Another exact writer may win. Compare its immutable bytes; never
		// overwrite a different claim through a check-then-rename race.
		prior, e := d.open(name, unix.O_RDONLY)
		if e != nil {
			return e
		}
		bytesWritten, readErr := io.ReadAll(io.LimitReader(prior, receiptLimit+1))
		prior.Close()
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(bytesWritten, data) {
			return ErrConflict
		}
	} else if err != nil {
		return err
	}
	return d.file.Sync()
}

func Prepare(root string, q Request) error {
	if err := q.Validate(root); err != nil {
		return err
	}
	d, err := openDirectory(root)
	if err != nil {
		return err
	}
	defer d.close()
	return d.write("request.json", q)
}

func readRequest(path string) (*directory, Request, error) {
	var q Request
	if filepath.Base(path) != "request.json" {
		return nil, q, ErrInvalid
	}
	d, err := openDirectory(filepath.Dir(path))
	if err != nil {
		return nil, q, err
	}
	if err := d.read("request.json", &q); err != nil {
		d.close()
		return nil, q, err
	}
	if err := q.Validate(filepath.Dir(path)); err != nil {
		d.close()
		return nil, q, err
	}
	return d, q, nil
}

func inspect(d *directory, q Request) (Observation, error) {
	view := Observation{State: "prepared"}
	intent, start, outcome, exit := new(Intent), new(Start), new(Outcome), new(Exit)
	for _, item := range []struct {
		name, schema string
		value        any
		binding      *Binding
		attach       func()
	}{
		{"intent.json", managed.RunnerSchema("intent"), intent, &intent.Binding, func() { view.Intent = intent; view.State = "spawning" }},
		{"start.json", managed.RunnerSchema("start"), start, &start.Binding, func() { view.Start = start; view.State = "running" }},
		{"outcome.json", managed.RunnerSchema("outcome"), outcome, &outcome.Binding, func() { view.Outcome = outcome; view.State = "terminating" }},
		{"exit.json", managed.RunnerSchema("exit"), exit, &exit.Binding, func() { view.Exit = exit; view.State = "exited" }},
	} {
		if err := d.read(item.name, item.value); errors.Is(err, unix.ENOENT) {
			continue
		} else if err != nil {
			return view, err
		}
		encoded, _ := json.Marshal(item.value)
		var header struct {
			Schema string `json:"schema"`
		}
		_ = json.Unmarshal(encoded, &header)
		if header.Schema != item.schema || *item.binding != q.Binding() {
			return view, ErrConflict
		}
		item.attach()
	}
	validProcess := func(id platform.ProcessIdentity) bool {
		return id.BootID == q.BootID && id.PID > 0 && id.PGID == id.PID && id.SID == id.PID && id.StartedSeconds > 0 && id.StartedMicros >= 0 && id.StartedMicros < 1000000 && id.UID == uint32(os.Geteuid())
	}
	if view.Intent != nil && (!validProcess(view.Intent.Supervisor) || view.Intent.RecordedAt.IsZero()) {
		return view, ErrInvalid
	}
	if view.Start != nil && (view.Intent == nil || !validProcess(view.Start.Process) || view.Start.StartedAt.IsZero()) {
		return view, ErrInvalid
	}
	if view.Outcome != nil && (view.Intent == nil || view.Outcome.ObservedAt.IsZero() || !validOutcome(view.Outcome.Outcome) || (view.Outcome.Outcome.Started && view.Start == nil)) {
		return view, ErrInvalid
	}
	if view.Exit != nil && (view.Outcome == nil || view.Exit.FinishedAt.IsZero() || !reflect.DeepEqual(view.Exit.Outcome, view.Outcome.Outcome) || (!view.Exit.TerminationProven && view.Exit.Reason == "")) {
		return view, ErrInvalid
	}
	if view.Exit != nil && view.Exit.Log != nil && !validLogManifest(*view.Exit.Log, q) {
		return view, ErrInvalid
	}
	return view, nil
}

func Inspect(path string) (Observation, error) {
	d, q, err := readRequest(path)
	if err != nil {
		return Observation{}, err
	}
	defer d.close()
	return inspect(d, q)
}

// ReadRequest uses the same private, no-follow, closed-shape reader as the
// supervisor. Recovery must not adopt an arbitrary historical request path.
func ReadRequest(path string) (Request, error) {
	d, q, err := readRequest(path)
	if err != nil {
		return q, err
	}
	defer d.close()
	return q, nil
}

func Cancel(path string) error {
	d, q, err := readRequest(path)
	if err != nil {
		return err
	}
	defer d.close()
	lock, err := d.open("cancel.lock", unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.Now().Add(time.Duration(q.TerminationSeconds) * time.Second)
	for {
		// Another canceller may have already made the same intent durable.
		if present, err := cancellation(d, q); err != nil || present {
			return err
		}
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			break
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		if !time.Now().Before(deadline) {
			return ErrUnproven
		}
		time.Sleep(min(25*time.Millisecond, time.Until(deadline)))
	}
	if present, err := cancellation(d, q); err != nil || present {
		return err
	}
	return d.write("cancel.json", Cancellation{Schema: managed.RunnerSchema("cancel"), Binding: q.Binding(), RequestedAt: time.Now().UTC()})
}
