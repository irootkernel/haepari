package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	_ "modernc.org/sqlite"
)

var ErrInvalidStore = errors.New("invalid SQLite store")

func Open(path string) (*sql.DB, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("%w: path is not a private regular file", ErrInvalidStore)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect SQLite path: %w", err)
	}
	params := url.Values{}
	for _, pragma := range []string{
		"journal_mode=WAL",
		"foreign_keys=ON",
		"busy_timeout=5000",
		"synchronous=FULL",
		"fullfsync=ON",
		"checkpoint_fullfsync=ON",
	} {
		params.Add("_pragma", pragma)
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: params.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	// The driver applies DSN pragmas to every new database/sql connection.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure SQLite: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, fmt.Errorf("protect SQLite file: %w", err)
	}
	return db, nil
}
