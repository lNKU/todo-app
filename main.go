package main

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
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

	s := &Server{store: NewStore(), tmpl: tmpl}

	mux := http.NewServeMux()

	// Static files are served from disk, so CSS edits show up on refresh without a rebuild.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Go 1.22+ lets patterns include the HTTP method and {wildcards}.
	// "{$}" means "exactly /", otherwise "GET /" would match every unmatched path.
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /todos", s.handleAdd)
	mux.HandleFunc("POST /todos/{id}/toggle", s.handleToggle)
	mux.HandleFunc("DELETE /todos/{id}", s.handleDelete)

	addr := ":8080"
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

// handleIndex renders the full page.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data := struct{ Todos []Todo }{Todos: s.store.All()}
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
	todo := s.store.Add(title)
	s.render(w, "todo-item", todo)
}

// handleToggle flips a todo's Done state and responds with the updated <li>.
func (s *Server) handleToggle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	todo, ok := s.store.Toggle(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "todo-item", todo)
}

// handleDelete removes a todo. The empty 200 response tells htmx to replace
// the <li> with nothing. (A 204 would make htmx skip the swap entirely.)
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || !s.store.Delete(id) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
}
