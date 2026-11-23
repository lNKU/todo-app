package main

import (
	"database/sql"
	"embed"
	"errors"
	"html/template"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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

// placeholders is the pool of hints for the "add todo" input. One is picked
// at random each time the full page loads. Add or remove entries freely.
var placeholders = []string{
	"Water the plants...",
	"Check emails...",
	"Something you keep putting off...",
	"Put gas in car...",
	"Take out the trash...",
	"Pay water bill...",
	"Buy that video game...",
	"Download a car...",
	"Sell some stuff...",
}

var titlebars = []string{
	"Wow, zany activities!",
	"Why are you looking here?",
	"I leave bugs in my code for fun.",
	"If you read this, go shower.",
	"RANDOM DISTRACTION MWAHAHA",
	"Lazers go pewpewpew",
	"The Sims is for guys too!",
	"Stay whimsy 🌈",
	"I'ma pirate Arr 🏴‍☠️",
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

	// Go 1.22 lets patterns include the HTTP method and {wildcards}.
	// "{$}" means "exactly /", otherwise "GET /" would match every unmatched path.
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /todos", s.handleAdd)
	mux.HandleFunc("POST /todos/{id}/toggle", s.handleToggle)
	mux.HandleFunc("POST /todos/{id}/star", s.handleStar)
	mux.HandleFunc("DELETE /todos/{id}", s.handleDelete)
	mux.HandleFunc("GET /todos/{id}", s.handleShow)
	mux.HandleFunc("GET /todos/{id}/edit", s.handleEditForm)
	mux.HandleFunc("PUT /todos/{id}", s.handleUpdate)

	addr := "127.0.0.1:8087"
	log.Printf("listening on http://%s", addr)
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

// pathID reads {id} from the URL. On failure it writes the 404 itself, so the
// caller only has to return.
func pathID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

// storeError turns an error from the store into the right response: a 404
// when the todo doesn't exist, a 500 for anything else.
func (s *Server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	s.serverError(w, err)
}

// normalizeFilter maps whatever is in the URL to one of the three filters we
// support, falling back to "all". Never trust raw query-string values.
func normalizeFilter(f string) string {
	switch f {
	case "active", "done", "starred":
		return f
	default:
		return "all"
	}
}

// currentFilter works out which filter the visitor's page is showing. htmx
// sends the page's address with every request in the HX-Current-URL header.
func currentFilter(r *http.Request) string {
	u, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if err != nil {
		return "all"
	}
	return normalizeFilter(u.Query().Get("filter"))
}

// matchesFilter reports whether a todo belongs in the list for a given filter.
func matchesFilter(filter string, t Todo) bool {
	switch filter {
	case "active":
		return !t.Done
	case "done":
		return t.Done
	case "starred":
		return t.Starred && !t.Done
	default:
		return true
	}
}

// handleIndex renders the full page.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	filter := normalizeFilter(r.URL.Query().Get("filter"))
	todos, err := s.store.All(filter)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data := struct {
		Todos       []Todo
		Filter      string
		Placeholder string
		Titlebar    string
		Today       string
	}{
		Todos:       todos,
		Filter:      filter,
		Placeholder: placeholders[rand.IntN(len(placeholders))],
		Titlebar:    titlebars[rand.IntN(len(titlebars))],
		Today:       time.Now().Format("2006-01-02"),
	}
	// The same URL serves two kinds of request. htmx asks only for the list
	// section; a normal browser visit (or refresh) gets the whole page.
	// Vary tells browsers and caches that the response depends on those headers.
	w.Header().Add("Vary", "HX-Request")
	w.Header().Add("Vary", "HX-History-Restore-Request")
	if r.Header.Get("HX-Request") == "true" &&
		r.Header.Get("HX-History-Restore-Request") != "true" {
		s.render(w, "todo-section", data)
		return
	}
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
	dueDate, dueTime, ok := parseDue(r)
	if !ok {
		http.Error(w, "invalid due date or time", http.StatusBadRequest)
		return
	}
	todo, err := s.store.Add(title, dueDate, dueTime)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// A new todo is never done, so it doesn't belong in the "Done" list.
	if !matchesFilter(currentFilter(r), todo) {
		w.WriteHeader(http.StatusOK)
		return
	}
	s.render(w, "todo-item", todo)
}

// handleToggle flips a todo's Done state and responds with the updated <li>.
func (s *Server) handleToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	todo, err := s.store.Toggle(id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	// Toggling can move a todo out of the current filter (e.g. finishing a
	// todo while viewing "Active"). An empty response removes its <li>.
	if !matchesFilter(currentFilter(r), todo) {
		// Override the swap style for this response only, so the item fades out
		// (same timing the delete button uses) instead of vanishing instantly.
		w.Header().Set("HX-Reswap", "outerHTML swap:400ms")
		w.WriteHeader(http.StatusOK)
		return
	}
	s.render(w, "todo-item", todo)
}

// handleDelete removes a todo. The empty 200 response tells htmx to replace
// the <li> with nothing. (A 204 would make htmx skip the swap entirely.)
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.Delete(id); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.Header().Set("HX-Reswap", "outerHTML swap:400ms")
	w.WriteHeader(http.StatusOK)
}

// renderTodo looks up the todo named in the URL and renders it with the given
// template. "Show" and "edit form" differ only in which template they use, so
// they share this helper instead of repeating the lookup and error handling.
func (s *Server) renderTodo(w http.ResponseWriter, r *http.Request, tmplName string) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	todo, err := s.store.Get(id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, tmplName, todo)
}

// handleShow returns the normal view of one todo. Cancel uses it.
func (s *Server) handleShow(w http.ResponseWriter, r *http.Request) {
	s.renderTodo(w, r, "todo-item")
}

// handleEditForm returns the edit form for one todo.
func (s *Server) handleEditForm(w http.ResponseWriter, r *http.Request) {
	s.renderTodo(w, r, "todo-edit")
}

// handleUpdate saves a new title and responds with the normal view of the todo.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	dueDate, dueTime, ok := parseDue(r)
	if !ok {
		http.Error(w, "invalid due date or time", http.StatusBadRequest)
		return
	}
	todo, err := s.store.Update(id, title, dueDate, dueTime)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, "todo-item", todo)
}

// handleStar flips a todo's starred state and responds with updated <li> section.
func (s *Server) handleStar(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.ToggleStar(id); err != nil {
		s.storeError(w, r, err)
		return
	}
	// Starring changes where this todo belongs in the list, so instead of
	// swapping one <li> we send the whole, freshly ordered list section.
	filter := currentFilter(r)
	todos, err := s.store.All(filter)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// Mark the todo that just changed so the template can animate it.
	for i := range todos {
		todos[i].Moved = todos[i].ID == id
	}
	// The star button targets its own <li>. This header redirects the
	// response to the whole list section for this one case.
	w.Header().Set("HX-Retarget", "#todo-section")
	data := struct {
		Todos  []Todo
		Filter string
	}{Todos: todos, Filter: filter}
	s.render(w, "todo-section", data)
}

// parseDue reads and validates the optional due date and time from a form.
// The browser's date and time inputs send "2026-11-30" and "14:30". Empty is
// fine and means "none". A time without a date is dropped. ok is false when a
// value is present but malformed. (The second result is called clock because
// "time" is the name of the package we use below.)
func parseDue(r *http.Request) (date, clock string, ok bool) {
	date = strings.TrimSpace(r.FormValue("due_date"))
	clock = strings.TrimSpace(r.FormValue("due_time"))
	if date == "" {
		return "", "", true
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return "", "", false
	}
	if clock != "" {
		if _, err := time.Parse("15:04", clock); err != nil {
			return "", "", false
		}
	}
	return date, clock, true
}
