// Package store owns the SQLite database: opening it, applying migrations and
// exposing the handle to the rest of memorycard.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"time"

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
	// busy_timeout goes first so the journal mode switch below also waits for a
	// lock held by another agent's process; immediate transactions take the
	// write lock up front, where the timeout applies, instead of failing on a
	// read-to-write upgrade.
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_txlock", "immediate")
	handle, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// Switching a brand-new file to WAL can report "locked" without waiting
	// when several agents start at once, so the first connection is retried.
	var err2 error
	for attempt := 0; attempt < 20; attempt++ {
		if err2 = handle.Ping(); err2 == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err2 != nil {
		handle.Close()
		return nil, fmt.Errorf("open %s: %w", path, err2)
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
		// Another process may have migrated while this one waited for the
		// write lock; the version is read again under the lock.
		var now int
		if err := tx.QueryRow(`pragma user_version`).Scan(&now); err != nil {
			tx.Rollback()
			return fmt.Errorf("read schema version: %w", err)
		}
		if now > v {
			tx.Rollback()
			continue
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
