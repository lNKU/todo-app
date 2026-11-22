package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Todo is a single task. Exported (capitalized) fields are required so that
// html/template can read them.
type Todo struct {
	ID      int
	Title   string
	Done    bool
	Starred bool
	DueDate string // "2026-11-30", or "" if none
	DueTime string // "14:30", or "" if none
	Moved   bool   // set only while rendering, to animate a todo that just moved; never stored
}

// DueDateLabel turns the stored "2026-11-30" into "Nov 30" for display, adding
// the year only when it isn't the current one. Templates can call any method
// on the value they're given, so the template uses this as {{.DueDateLabel}}.
// It returns "" when there is no (valid) date.
func (t Todo) DueDateLabel() string {
	// Go describes formats by showing the reference time "Mon Jan 2 15:04:05 2006"
	// (the numbers 1 2 3 4 5 6 in order) written the way you want to see it.
	d, err := time.Parse("2006-01-02", t.DueDate)
	if err != nil {
		return ""
	}
	if d.Year() != time.Now().Year() {
		return d.Format("Mon, Jan 2, 2006")
	}
	return d.Format("Mon, Jan 2")
}

// DueTimeLabel turns the stored "14:30" into "2:30 PM". "" if none.
func (t Todo) DueTimeLabel() string {
	d, err := time.Parse("15:04", t.DueTime)
	if err != nil {
		return ""
	}
	return d.Format("3:04 PM")
}

// TimeOption is one entry in the due-time dropdown.
type TimeOption struct {
	Value string // "14:30", what the form sends
	Label string // "2:30 PM", what the person sees
}

// timeOptions returns the dropdown choices: every 15 minutes across the day.
// If extra is a valid time that isn't on that grid (say "14:17", saved back
// when the time box allowed any minute), it is slotted in so editing the todo
// doesn't silently change its time.
func timeOptions(extra string) []TimeOption {
	var mins []int // minutes since midnight
	for m := 0; m < 24*60; m += 15 {
		mins = append(mins, m)
	}
	if t, err := time.Parse("15:04", extra); err == nil {
		if m := t.Hour()*60 + t.Minute(); m%15 != 0 {
			mins = append(mins, m)
			sort.Ints(mins)
		}
	}
	opts := make([]TimeOption, len(mins))
	for i, m := range mins {
		t := time.Date(2000, 1, 1, m/60, m%60, 0, 0, time.UTC) // the date is irrelevant
		opts[i] = TimeOption{Value: t.Format("15:04"), Label: t.Format("3:04 PM")}
	}
	return opts
}

// TimeOptions lets the edit form's template ask a todo for its own choices.
func (t Todo) TimeOptions() []TimeOption {
	return timeOptions(t.DueTime)
}

// DueStatus says how urgent the due date is: "overdue", "soon" (within 24
// hours), "near" (within 3 days), or "" when the todo is done, has no due
// date, or is further away. The thresholds are right here, so they are easy
// to change.
func (t Todo) DueStatus() string {
	if t.Done || t.DueDate == "" {
		return ""
	}
	clock := t.DueTime
	if clock == "" {
		clock = "23:59" // no time given: due by the end of that day
	}
	// ParseInLocation reads the text as local time, rather than UTC.
	due, err := time.ParseInLocation("2006-01-02 15:04", t.DueDate+" "+clock, time.Local)
	if err != nil {
		return ""
	}
	left := time.Until(due)
	switch {
	case left < 0:
		return "overdue"
	case left <= 6*time.Hour:
		return "soon"
	case left <= 12*time.Hour:
		return "near"
	default:
		return ""
	}
}

// todoColumns is the one place that lists the columns every query reads, in
// the order scanTodo expects them. A new column now means editing this line
// and scanTodo, instead of every query.
const todoColumns = `id, title, done, starred, due_date, due_time`

// scanner is satisfied by both *sql.Row and *sql.Rows (each has this Scan
// method), so one helper can read a todo from either.
type scanner interface {
	Scan(dest ...any) error
}

func scanTodo(sc scanner) (Todo, error) {
	var t Todo
	err := sc.Scan(&t.ID, &t.Title, &t.Done, &t.Starred, &t.DueDate, &t.DueTime)
	return t, err
}

// ErrNotFound is returned when an ID doesn't exist. Handlers compare against it
// (with errors.Is) to decide between a 404 and a 500.
var ErrNotFound = errors.New("todo not found")

// Store is the data layer. The handlers only call these methods and never see
// SQL, so changing storage later means changing this file and nothing else.
type Store struct {
	db *sql.DB
}

// NewStore creates the table if it doesn't exist yet and returns a ready Store.
// The schema step is a tiny "migration": it is safe to run on every startup.
func NewStore(db *sql.DB) (*Store, error) {
	const schema = `
		CREATE TABLE IF NOT EXISTS todos (
			id    INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT    NOT NULL,
			done  INTEGER NOT NULL DEFAULT 0
		)`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create schema: %w", err)
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// migrate brings the database schema up to date. SQLite has no
// "ADD COLUMN IF NOT EXISTS", so we record a schema version inside the
// database itself (PRAGMA user_version) and run each step only once.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}

	if version < 1 {
		// A transaction makes the column and the version bump succeed or fail
		// together. Otherwise a crash between them would leave the column added
		// but the version unchanged, and the next start would fail trying to add it again.
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback() // does nothing once Commit has succeeded

		if _, err := tx.Exec(`ALTER TABLE todos ADD COLUMN starred INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if _, err := tx.Exec(`PRAGMA user_version = 1`); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	if version < 2 {
		// Same pattern as above, one transaction per schema version.
		// '' means "no due date/time", so Go can scan the columns into plain strings.
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback() // does nothing once Commit has succeeded

		if _, err := tx.Exec(`ALTER TABLE todos ADD COLUMN due_date TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		if _, err := tx.Exec(`ALTER TABLE todos ADD COLUMN due_time TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		if _, err := tx.Exec(`PRAGMA user_version = 2`); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// All returns every todo, oldest first.
func (s *Store) All(filter string) ([]Todo, error) {
	// The filter only chooses between fixed SQL snippets below. It is never
	// pasted into the query, so there is no injection risk here.
	query := `SELECT ` + todoColumns + ` FROM todos`
	switch filter {
	case "active":
		query += ` WHERE done = 0`
	case "done":
		query += ` WHERE done = 1`
	case "starred":
		query += ` WHERE starred = 1 AND done = 0`
	}
	query += ` ORDER BY starred DESC, id`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close() // always release the connection back to the pool

	var todos []Todo
	for rows.Next() {
		// Scan copies each column, in order, into the pointers we give it.
		t, err := scanTodo(rows)
		if err != nil {
			return nil, err
		}
		todos = append(todos, t)
	}
	// rows.Next() returns false on both "finished" and "failed", so check Err().
	return todos, rows.Err()
}

// Add inserts a todo and returns it, including its new ID.
// RETURNING makes SQLite hand back the stored row in the same statement.
func (s *Store) Add(title, dueDate, dueTime string) (Todo, error) {
	t, err := scanTodo(s.db.QueryRow(
		`INSERT INTO todos (title, due_date, due_time) VALUES (?, ?, ?) RETURNING `+todoColumns,
		title, dueDate, dueTime,
	))
	if err != nil {
		return Todo{}, err
	}
	return t, nil
}

// Toggle flips Done and returns the updated todo.
func (s *Store) Toggle(id int) (Todo, error) {
	t, err := scanTodo(s.db.QueryRow(
		`UPDATE todos SET done = NOT done WHERE id = ? RETURNING `+todoColumns,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Todo{}, ErrNotFound // no row matched that id
	}
	if err != nil {
		return Todo{}, err
	}
	return t, nil
}

// Delete removes a todo. Exec is for statements where we don't need rows back.
func (s *Store) Delete(id int) error {
	res, err := s.db.Exec(`DELETE FROM todos WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns one todo by ID. The edit form and the cancel button both need it.
func (s *Store) Get(id int) (Todo, error) {
	t, err := scanTodo(s.db.QueryRow(
		`SELECT `+todoColumns+` FROM todos WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Todo{}, ErrNotFound
	}
	if err != nil {
		return Todo{}, err
	}
	return t, nil
}

// UpdateTitle changes a todo's title and returns the updated row. It follows
// the same UPDATE ... RETURNING pattern as Toggle.
func (s *Store) Update(id int, title, dueDate, dueTime string) (Todo, error) {
	t, err := scanTodo(s.db.QueryRow(
		`UPDATE todos SET title = ?, due_date = ?, due_time = ? WHERE id = ? RETURNING `+todoColumns,
		title, dueDate, dueTime, id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Todo{}, ErrNotFound
	}
	if err != nil {
		return Todo{}, err
	}
	return t, nil
}

// ToggleStar flips Starred and returns the updated todo.
func (s *Store) ToggleStar(id int) (Todo, error) {
	t, err := scanTodo(s.db.QueryRow(
		`UPDATE todos SET starred = NOT starred WHERE id = ? RETURNING `+todoColumns,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Todo{}, ErrNotFound
	}
	if err != nil {
		return Todo{}, err
	}
	return t, nil
}
