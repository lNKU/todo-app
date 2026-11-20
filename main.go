package main

import (
	"database/sql"
	"embed"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	// The blank identifier (_) imports this package only for its side effect:
	// it registers a database driver named "sqlite" with database/sql.
	_ "modernc.org/sqlite"
)

// The //go:embed line below is a compiler directive, not a comment: it bakes
// the template files into the binary at build time.
//
//go:embed templates/*.html
var templateFS embed.FS

// Server bundles the dependencies our handlers need.
type Server struct {
	store *Store
	tmpl  *template.Template
}

func main() {
	// template.Must panics if parsing fails, which is what we want at startup:
	// a broken template should stop the program immediately.
	tmpl := template.Must(template.ParseFS(templateFS, "templates/*.html"))

	// SQLite creates the database file but not its folder, so make sure db/ exists.
	// MkdirAll does nothing if the folder is already there. 0o755 means the owner
	// can read/write/enter it and everyone else can read/enter it.
	if err := os.MkdirAll("db", 0o755); err != nil {
		log.Fatal(err)
	}

	// sql.Open does NOT connect; it only prepares a connection pool.
	// The DSN creates todos.db in the db/ directory and sets two options:
	//   busy_timeout: wait up to 5s if the database is briefly locked, instead of failing
	//   journal_mode(WAL): lets reads and writes overlap, which suits a web app
	db, err := sql.Open("sqlite", "file:db/todos.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Ping forces a real connection, so a bad path or permission problem shows up now.
	if err := db.Ping(); err != nil {
		log.Fatalf("cannot open database: %v", err)
	}

	store, err := NewStore(db)
	if err != nil {
		log.Fatal(err)
	}

	s := &Server{store: store, tmpl: tmpl}

	mux := http.NewServeMux()

	// Static files are served from disk, so CSS edits show up on refresh without a rebuild.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Go 1.22+ lets patterns include the HTTP method and {wildcards}.
	// "{$}" means "exactly /", otherwise "GET /" would match every unmatched path.
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /todos", s.handleAdd)
	mux.HandleFunc("POST /todos/{id}/toggle", s.handleToggle)
	mux.HandleFunc("DELETE /todos/{id}", s.handleDelete)

	addr := ":8087"
	log.Printf("listening on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// render writes a named template to the response.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %q: %v", name, err)
	}
}

// serverError logs the real error for us, but shows the visitor a generic
// message so internal details (like SQL errors) never leak into the page.
func (s *Server) serverError(w http.ResponseWriter, err error) {
	log.Printf("server error: %v", err)
	http.Error(w, "something went wrong", http.StatusInternalServerError)
}

// handleIndex renders the full page.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	todos, err := s.store.All()
	if err != nil {
		s.serverError(w, err)
		return
	}
	data := struct{ Todos []Todo }{Todos: todos}
	s.render(w, "index.html", data)
}

// handleAdd creates a todo and responds with just that todo's <li>.
// htmx appends it to the list; the page never reloads.
func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	todo, err := s.store.Add(title)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "todo-item", todo)
}

// handleToggle flips a todo's Done state and responds with the updated <li>.
func (s *Server) handleToggle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	todo, err := s.store.Toggle(id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "todo-item", todo)
}

// handleDelete removes a todo. The empty 200 response tells htmx to replace
// the <li> with nothing. (A 204 would make htmx skip the swap entirely.)
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	err = s.store.Delete(id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
