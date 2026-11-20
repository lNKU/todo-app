package main

import "sync"

// Todo is a single task. Exported (capitalized) fields are required so that
// html/template can read them.
type Todo struct {
	ID    int
	Title string
	Done  bool
}

// Store holds todos in memory. It is deliberately simple: when you are ready
// for SQLite, this is the one file you replace. Handlers only call its methods.
type Store struct {
	mu     sync.Mutex // net/http runs each request in its own goroutine, so we guard shared data
	todos  []Todo
	nextID int
}

func NewStore() *Store {
	return &Store{nextID: 1}
}

// All returns a copy of the list so callers can't modify our internal slice.
func (s *Store) All() []Todo {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Todo, len(s.todos))
	copy(out, s.todos)
	return out
}

func (s *Store) Add(title string) Todo {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := Todo{ID: s.nextID, Title: title}
	s.nextID++
	s.todos = append(s.todos, t)
	return t
}

// Toggle flips Done and returns the updated todo. The bool reports whether the ID existed.
func (s *Store) Toggle(id int) (Todo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.todos {
		if s.todos[i].ID == id {
			s.todos[i].Done = !s.todos[i].Done
			return s.todos[i], true
		}
	}
	return Todo{}, false
}

func (s *Store) Delete(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, t := range s.todos {
		if t.ID == id {
			s.todos = append(s.todos[:i], s.todos[i+1:]...)
			return true
		}
	}
	return false
}
