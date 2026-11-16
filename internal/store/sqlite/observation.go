package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"golang.org/x/sys/unix"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	driver "modernc.org/sqlite"
)

// Observation gives SQLite only disposable copies. In particular, mode=ro is
// never used against the installation: SQLite may create a shared-memory file.
// The caller holds the original ownership locks and validates every copied
// descriptor against its complete inventory before and after this observation.
type Observation struct {
	DB              *sql.DB
	Conn            *sql.Conn
	Tx              *sql.Tx
	directory       string
	directoryHandle *os.File
	owned           map[string]os.FileInfo
	engineNames     map[string]bool
}

func Observe(database, wal []byte) (_ *Observation, finalErr error) {
	dir, err := os.MkdirTemp("/tmp", "haepari-observe-")
	if err != nil {
		return nil, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	o := &Observation{directory: dir, owned: map[string]os.FileInfo{}, engineNames: map[string]bool{}}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	o.directoryHandle = os.NewFile(uintptr(fd), dir)
	defer func() {
		if finalErr != nil {
			o.Close()
		}
	}()
	path := filepath.Join(dir, "state.sqlite")
	if err = o.writeOwned("state.sqlite", database); err != nil {
		return nil, err
	}
	if wal != nil {
		if err = o.writeOwned("state.sqlite-wal", wal); err != nil {
			return nil, err
		}
	}
	o.engineNames["state.sqlite-wal"] = true
	o.engineNames["state.sqlite-shm"] = true
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	o.DB, err = sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	o.DB.SetMaxOpenConns(1)
	o.Conn, err = o.DB.Conn(context.Background())
	if err != nil {
		return nil, err
	}
	o.Tx, err = o.Conn.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	var integrity string
	if err = o.Tx.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return nil, fmt.Errorf("%w: observation integrity", ErrInvalidStore)
	}
	if err = o.captureEngineFiles(); err != nil {
		return nil, err
	}
	return o, nil
}
func (o *Observation) Close() error {
	captureErr := o.captureEngineFiles()
	if o.Tx != nil {
		_ = o.Tx.Rollback()
	}
	if o.Conn != nil {
		_ = o.Conn.Close()
	}
	if o.DB != nil {
		_ = o.DB.Close()
	}
	if o.directoryHandle == nil || captureErr != nil {
		return ErrInvalidStore
	}
	defer o.directoryHandle.Close()
	held, err := o.directoryHandle.Stat()
	if err != nil {
		return err
	}
	named, err := os.Lstat(o.directory)
	if err != nil || !os.SameFile(held, named) {
		return ErrInvalidStore
	}
	entries, err := os.ReadDir(o.directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		info, err := entry.Info()
		expected, ok := o.owned[entry.Name()]
		if err != nil || !ok || !info.Mode().IsRegular() || !os.SameFile(expected, info) {
			return ErrInvalidStore
		}
	}
	for _, entry := range entries {
		var st unix.Stat_t
		if unix.Fstatat(int(o.directoryHandle.Fd()), entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Mode&0077 != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG {
			return ErrInvalidStore
		}
		info, err := os.Lstat(filepath.Join(o.directory, entry.Name()))
		if err != nil || !os.SameFile(o.owned[entry.Name()], info) {
			return ErrInvalidStore
		}
		if err = unix.Unlinkat(int(o.directoryHandle.Fd()), entry.Name(), 0); err != nil {
			return err
		}
	}
	named, err = os.Lstat(o.directory)
	if err != nil || !os.SameFile(held, named) {
		return ErrInvalidStore
	}
	parent, err := os.Open(filepath.Dir(o.directory))
	if err != nil {
		return err
	}
	defer parent.Close()
	return unix.Unlinkat(int(parent.Fd()), filepath.Base(o.directory), unix.AT_REMOVEDIR)
}

// Backup uses SQLite's engine backup on the same read transaction whose logical
// inventory was checked. Only a new caller-owned destination is writable.
func (o *Observation) Backup(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			return ErrInvalidStore
		}
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return err
	}
	if filepath.Dir(path) == o.directory {
		o.owned[filepath.Base(path)] = before
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			o.engineNames[filepath.Base(path)+suffix] = true
		}
	}
	err = o.Conn.Raw(func(raw any) error {
		c, ok := raw.(interface {
			NewBackup(string) (*driver.Backup, error)
		})
		if !ok {
			return ErrInvalidStore
		}
		b, err := c.NewBackup((&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}).String())
		if err != nil {
			return err
		}
		_, err = b.Step(-1)
		end := b.Finish()
		if err != nil {
			return err
		}
		return end
	})
	if err != nil {
		return err
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, named) {
		return ErrInvalidStore
	}
	return file.Sync()
}

func quoteIdentifier(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func field(out *bytes.Buffer, raw []byte) {
	_ = binary.Write(out, binary.BigEndian, uint64(len(raw)))
	out.Write(raw)
}

// LogicalDigest includes all schema objects/column declarations and every stored
// value, tagged with its SQLite storage class. Row sorting retains duplicates;
// integers and IEEE real bits never pass through JSON or decimal rounding.
func LogicalDigest(tx *sql.Tx) (string, error) {
	var inventory bytes.Buffer
	var version int64
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return "", err
	}
	_ = binary.Write(&inventory, binary.BigEndian, version)
	rows, err := tx.Query("SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY tbl_name,type,name")
	if err != nil {
		return "", err
	}
	type schema struct {
		kind, name, table string
		sql               sql.NullString
	}
	var objects []schema
	for rows.Next() {
		var s schema
		if err = rows.Scan(&s.kind, &s.name, &s.table, &s.sql); err != nil {
			rows.Close()
			return "", err
		}
		objects = append(objects, s)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	_ = binary.Write(&inventory, binary.BigEndian, uint64(len(objects)))
	for _, s := range objects {
		field(&inventory, []byte("schema_object"))
		field(&inventory, []byte(fmt.Sprint(s.sql.Valid)))
		for _, v := range []string{s.kind, s.name, s.table, s.sql.String} {
			field(&inventory, []byte(v))
		}
		if s.kind != "table" {
			continue
		}
		if strings.Contains(strings.ToUpper(s.sql.String), "CREATE VIRTUAL") {
			return "", ErrInvalidStore
		}
		columns, err := tx.Query("PRAGMA table_xinfo(" + quoteIdentifier(s.name) + ")")
		if err != nil {
			return "", err
		}
		var names []string
		for columns.Next() {
			var cid, notnull, pk, hidden int64
			var name, typ string
			var def sql.NullString
			if err = columns.Scan(&cid, &name, &typ, &notnull, &def, &pk, &hidden); err != nil {
				columns.Close()
				return "", err
			}
			field(&inventory, []byte("column_declaration"))
			names = append(names, name)
			for _, v := range []string{fmt.Sprint(cid), name, typ, fmt.Sprint(notnull), fmt.Sprint(def.Valid), def.String, fmt.Sprint(pk), fmt.Sprint(hidden)} {
				field(&inventory, []byte(v))
			}
		}
		if err = columns.Err(); err != nil {
			columns.Close()
			return "", err
		}
		columns.Close()
		field(&inventory, []byte("rows"))
		_ = binary.Write(&inventory, binary.BigEndian, uint64(len(names)))
		var expressions []string
		for _, n := range names {
			expressions = append(expressions, "typeof("+quoteIdentifier(n)+")", "CASE WHEN typeof("+quoteIdentifier(n)+")='text' THEN CAST("+quoteIdentifier(n)+" AS BLOB) ELSE "+quoteIdentifier(n)+" END")
		}
		values, err := tx.Query("SELECT " + strings.Join(expressions, ",") + " FROM " + quoteIdentifier(s.name))
		if err != nil {
			return "", err
		}
		var encoded [][]byte
		for values.Next() {
			v := make([]any, len(names)*2)
			dest := make([]any, len(v))
			for i := range v {
				dest[i] = &v[i]
			}
			if err = values.Scan(dest...); err != nil {
				values.Close()
				return "", err
			}
			var row bytes.Buffer
			for i := range names {
				kind, ok := v[i*2].(string)
				if !ok {
					values.Close()
					return "", ErrInvalidStore
				}
				field(&row, []byte(kind))
				var raw []byte
				switch value := v[i*2+1].(type) {
				case nil:
				case int64:
					raw = make([]byte, 8)
					binary.BigEndian.PutUint64(raw, uint64(value))
				case float64:
					raw = make([]byte, 8)
					binary.BigEndian.PutUint64(raw, math.Float64bits(value))
				case string:
					raw = []byte(value)
				case []byte:
					raw = value
				default:
					values.Close()
					return "", ErrInvalidStore
				}
				field(&row, raw)
			}
			encoded = append(encoded, row.Bytes())
		}
		if err = values.Err(); err != nil {
			values.Close()
			return "", err
		}
		values.Close()
		sort.Slice(encoded, func(i, j int) bool { return bytes.Compare(encoded[i], encoded[j]) < 0 })
		_ = binary.Write(&inventory, binary.BigEndian, uint64(len(encoded)))
		for _, row := range encoded {
			field(&inventory, row)
		}
	}
	sum := sha256.Sum256(inventory.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// BackupBytes leaves the source read transaction in place and registers every
// engine-created scratch file before cleanup. The caller publishes these bytes
// only through its independently held destination descriptors.
func (o *Observation) BackupBytes() ([]byte, error) {
	path := filepath.Join(o.directory, "engine-backup.sqlite")
	err := o.Backup(path)
	if captureErr := o.captureEngineFiles(); err == nil && captureErr != nil {
		err = captureErr
	}
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (o *Observation) writeOwned(name string, raw []byte) error {
	fd, err := unix.Openat(int(o.directoryHandle.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	o.owned[name] = info
	_, err = f.Write(raw)
	return err
}
func (o *Observation) captureEngineFiles() error {
	if o.directoryHandle == nil {
		return ErrInvalidStore
	}
	held, err := o.directoryHandle.Stat()
	if err != nil {
		return err
	}
	named, err := os.Lstat(o.directory)
	if err != nil || !os.SameFile(held, named) {
		return ErrInvalidStore
	}
	for name := range o.engineNames {
		if _, bound := o.owned[name]; bound {
			continue
		}
		fd, err := unix.Openat(int(o.directoryHandle.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), name)
		info, err := f.Stat()
		var st unix.Stat_t
		nativeErr := unix.Fstat(fd, &st)
		f.Close()
		if err != nil || nativeErr != nil || !info.Mode().IsRegular() || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Mode&0077 != 0 {
			return ErrInvalidStore
		}
		o.owned[name] = info
	}
	return nil
}
