package main

import (
	"database/sql"
	"errors"
	"fmt"
)

// Todo is a single task. Exported (capitalized) fields are required so that
// html/template can read them.
type Todo struct {
	ID    int
	Title string
	Done  bool
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
	return &Store{db: db}, nil
}

// All returns every todo, oldest first.
func (s *Store) All() ([]Todo, error) {
	rows, err := s.db.Query(`SELECT id, title, done FROM todos ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() // always release the connection back to the pool

	var todos []Todo
	for rows.Next() {
		var t Todo
		// Scan copies each column, in order, into the pointers we give it.
		if err := rows.Scan(&t.ID, &t.Title, &t.Done); err != nil {
			return nil, err
		}
		todos = append(todos, t)
	}
	// rows.Next() returns false on both "finished" and "failed", so check Err().
	return todos, rows.Err()
}

// Add inserts a todo and returns it, including its new ID.
// RETURNING makes SQLite hand back the stored row in the same statement.
func (s *Store) Add(title string) (Todo, error) {
	var t Todo
	err := s.db.QueryRow(
		`INSERT INTO todos (title) VALUES (?) RETURNING id, title, done`,
		title,
	).Scan(&t.ID, &t.Title, &t.Done)
	if err != nil {
		return Todo{}, err
	}
	return t, nil
}

// Toggle flips Done and returns the updated todo.
func (s *Store) Toggle(id int) (Todo, error) {
	var t Todo
	err := s.db.QueryRow(
		`UPDATE todos SET done = NOT done WHERE id = ? RETURNING id, title, done`,
		id,
	).Scan(&t.ID, &t.Title, &t.Done)
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
