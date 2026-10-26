// Package state is the director's SQLite file: the play history and the planned
// queue tail, so a restart resumes the same leg and the repeat filter survives.
package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vincent/neighbeats-radio/internal/plan"
)

// Outcomes of a play.
const (
	Playing  = "playing"
	Finished = "finished"
	Skipped  = "skipped"
)

type DB struct{ db *sql.DB }

type Play struct {
	ID        int64
	StartedAt time.Time
	Path      string
	ItemID    string
	Artist    string
	Title     string
	Reason    string
	Cosine    float64
	Leg       int
	Outcome   string
}

// migrations run in order; the index+1 is stored in PRAGMA user_version.
var migrations = []string{`
CREATE TABLE plays (
	id         INTEGER PRIMARY KEY,
	started_at INTEGER NOT NULL,
	path       TEXT NOT NULL,
	item_id    TEXT NOT NULL DEFAULT '',
	artist     TEXT NOT NULL DEFAULT '',
	title      TEXT NOT NULL DEFAULT '',
	reason     TEXT NOT NULL DEFAULT '',
	cosine     REAL NOT NULL DEFAULT 0,
	leg        INTEGER NOT NULL DEFAULT 0,
	outcome    TEXT NOT NULL DEFAULT 'playing'
);
CREATE INDEX plays_started ON plays(started_at);
CREATE TABLE queue (
	pos         INTEGER PRIMARY KEY,
	path        TEXT NOT NULL,
	item_id     TEXT NOT NULL DEFAULT '',
	artist      TEXT NOT NULL DEFAULT '',
	title       TEXT NOT NULL DEFAULT '',
	reason      TEXT NOT NULL DEFAULT '',
	cosine      REAL NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	leg         INTEGER NOT NULL DEFAULT 0,
	state       TEXT NOT NULL
);`,
}

// Open opens (creating if needed) the database and applies migrations. Use
// ":memory:" in tests.
func Open(path string) (*DB, error) {
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	db.SetMaxOpenConns(1) // single writer; also keeps :memory: on one connection
	d := &DB{db: db}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate() error {
	var v int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if v > len(migrations) {
		return fmt.Errorf("state: database is version %d, this build knows %d", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		tx, err := d.db.Begin()
		if err != nil {
			return fmt.Errorf("state: %w", err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("state: migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("state: migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("state: migration %d: %w", i+1, err)
		}
	}
	return nil
}

// StartPlay records a track start and returns its id.
func (d *DB) StartPlay(ctx context.Context, p Play) (int64, error) {
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO plays(started_at, path, item_id, artist, title, reason, cosine, leg, outcome)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		p.StartedAt.Unix(), p.Path, p.ItemID, p.Artist, p.Title, p.Reason, p.Cosine, p.Leg, Playing)
	if err != nil {
		return 0, fmt.Errorf("state: %w", err)
	}
	return res.LastInsertId()
}

func (d *DB) EndPlay(ctx context.Context, id int64, outcome string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE plays SET outcome=? WHERE id=?`, outcome, id)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	return nil
}

// ClosePlaying marks plays left open by a crash as finished.
func (d *DB) ClosePlaying(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `UPDATE plays SET outcome=? WHERE outcome=?`, Finished, Playing)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	return nil
}

// PlaysSince lists plays that started at or after since, oldest first.
func (d *DB) PlaysSince(ctx context.Context, since time.Time) ([]Play, error) {
	return d.plays(ctx, `WHERE started_at >= ? ORDER BY id`, since.Unix())
}

// History returns the last n plays, newest first.
func (d *DB) History(ctx context.Context, n int) ([]Play, error) {
	return d.plays(ctx, `ORDER BY id DESC LIMIT ?`, n)
}

func (d *DB) plays(ctx context.Context, tail string, args ...any) ([]Play, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, started_at, path, item_id, artist, title, reason, cosine, leg, outcome FROM plays `+tail, args...)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	defer rows.Close()
	var out []Play
	for rows.Next() {
		var p Play
		var at int64
		if err := rows.Scan(&p.ID, &at, &p.Path, &p.ItemID, &p.Artist, &p.Title, &p.Reason, &p.Cosine, &p.Leg, &p.Outcome); err != nil {
			return nil, fmt.Errorf("state: %w", err)
		}
		p.StartedAt = time.Unix(at, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveQueue replaces the stored queue with items.
func (d *DB) SaveQueue(ctx context.Context, items []plan.Item) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM queue`); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	for i, it := range items {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO queue(pos, path, item_id, artist, title, reason, cosine, duration_ms, leg, state)
			 VALUES(?,?,?,?,?,?,?,?,?,?)`,
			i, it.Path, it.ItemID, it.Artist, it.Title, it.Reason, it.Cosine, it.Duration.Milliseconds(), it.Leg, string(it.State)); err != nil {
			return fmt.Errorf("state: %w", err)
		}
	}
	return tx.Commit()
}

func (d *DB) LoadQueue(ctx context.Context) ([]plan.Item, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT path, item_id, artist, title, reason, cosine, duration_ms, leg, state FROM queue ORDER BY pos`)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	defer rows.Close()
	var out []plan.Item
	for rows.Next() {
		var it plan.Item
		var ms int64
		var st string
		if err := rows.Scan(&it.Path, &it.ItemID, &it.Artist, &it.Title, &it.Reason, &it.Cosine, &ms, &it.Leg, &st); err != nil {
			return nil, fmt.Errorf("state: %w", err)
		}
		it.Duration = time.Duration(ms) * time.Millisecond
		it.State = plan.State(st)
		out = append(out, it)
	}
	return out, rows.Err()
}
