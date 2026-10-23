// Package store owns the SQLite database: opening it, applying migrations and
// exposing the handle to the rest of memorycard.
package store

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

// DB wraps the SQLite handle.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if needed) the database at path and brings its schema
// up to SchemaVersion. It is safe to call on an existing database.
func Open(path string) (*DB, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	handle, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db := &DB{sql: handle}
	if err := db.migrate(); err != nil {
		handle.Close()
		return nil, err
	}
	return db, nil
}

// SQL returns the underlying handle.
func (d *DB) SQL() *sql.DB { return d.sql }

// Close releases the database.
func (d *DB) Close() error { return d.sql.Close() }

func (d *DB) migrate() error {
	var current int
	if err := d.sql.QueryRow(`pragma user_version`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > SchemaVersion {
		return fmt.Errorf("database schema v%d is newer than this build (v%d)", current, SchemaVersion)
	}
	for v := current; v < SchemaVersion; v++ {
		tx, err := d.sql.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrate to v%d: %w", v+1, err)
		}
		// pragma values cannot be bound; v is an internal int, not user input.
		if _, err := tx.Exec(fmt.Sprintf(`pragma user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
