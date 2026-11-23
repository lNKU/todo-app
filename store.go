package main

import (
	"database/sql"
	"errors"
	"fmt"
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

// DueDateLabel turns the stored "2026-11-30" into "Mon, Nov 30" for display, adding
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

// DueStatus says how urgent the due date is: "overdue", "soon" (within 6
// hours), "near" (within 12 hours), or "" when the todo is done, has no due
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

// migrations lists the schema upgrades in order: migrations[0] brings the
// database to version 1, migrations[1] to version 2, and so on. To change the
// schema, add a new entry at the end. Never edit or reorder old entries, because
// they have already run on existing databases.
var migrations = [][]string{
	// version 1: starred
	{`ALTER TABLE todos ADD COLUMN starred INTEGER NOT NULL DEFAULT 0`},

	// version 2: due date and time. '' means "no due date/time", so Go can
	// scan the columns into plain strings.
	{
		`ALTER TABLE todos ADD COLUMN due_date TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE todos ADD COLUMN due_time TEXT NOT NULL DEFAULT ''`,
	},
}

// migrate brings the database schema up to date. SQLite has no
// "ADD COLUMN IF NOT EXISTS", so we record a schema version inside the
// database itself (PRAGMA user_version) and run each step only once.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	// Run every migration this database hasn't seen yet. A database at
	// version 2 starts the loop at index 2, finds nothing left, and does no work.
	for i := version; i < len(migrations); i++ {
		if err := applyMigration(db, i+1, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

// applyMigration runs one version's statements and records the new version.
func applyMigration(db *sql.DB, version int, stmts []string) error {
	// A transaction makes the columns and the version bump succeed or fail
	// together. Otherwise a crash between them would leave a column added but
	// the version unchanged, and the next start would fail trying to add it again.
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // does nothing once Commit has succeeded

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	// PRAGMA can't take a ? placeholder, so the number is formatted into the
	// text. That's safe here because version is an int we control.
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return err
	}
	return tx.Commit()
}

// queryTodo runs a query that returns one todo row (a SELECT, or an INSERT or
// UPDATE with RETURNING) and scans it. A query that matches no row becomes
// ErrNotFound, so every caller reports "missing" the same way.
func (s *Store) queryTodo(query string, args ...any) (Todo, error) {
	t, err := scanTodo(s.db.QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Todo{}, ErrNotFound
	}
	if err != nil {
		return Todo{}, err
	}
	return t, nil
}

// All returns every todo, oldest first - unless starred - then those come first.
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
	return s.queryTodo(
		`INSERT INTO todos (title, due_date, due_time) VALUES (?, ?, ?) RETURNING `+todoColumns,
		title, dueDate, dueTime,
	)
}

// Toggle flips Done and returns the updated todo.
func (s *Store) Toggle(id int) (Todo, error) {
	return s.queryTodo(
		`UPDATE todos SET done = NOT done WHERE id = ? RETURNING `+todoColumns,
		id,
	)
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
	return s.queryTodo(
		`SELECT `+todoColumns+` FROM todos WHERE id = ?`,
		id,
	)
}

// Update changes a todo's title and due date/time and returns the updated row.
// It follows the same UPDATE ... RETURNING pattern as Toggle.
func (s *Store) Update(id int, title, dueDate, dueTime string) (Todo, error) {
	return s.queryTodo(
		`UPDATE todos SET title = ?, due_date = ?, due_time = ? WHERE id = ? RETURNING `+todoColumns,
		title, dueDate, dueTime, id,
	)
}

// ToggleStar flips Starred and returns the updated todo.
func (s *Store) ToggleStar(id int) (Todo, error) {
	return s.queryTodo(
		`UPDATE todos SET starred = NOT starred WHERE id = ? RETURNING `+todoColumns,
		id,
	)
}
