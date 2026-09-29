package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
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
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=FULL",
		"PRAGMA fullfsync=ON",
		"PRAGMA checkpoint_fullfsync=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure SQLite %s: %w", pragma, err)
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, fmt.Errorf("protect SQLite file: %w", err)
	}
	return db, nil
}
