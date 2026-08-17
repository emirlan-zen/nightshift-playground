package focus

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

const maxBody = 256 << 10

type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
}

func (h *Handler) Get(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, h.store.All())
}

func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		http.Error(w, ErrUnknownDocument.Error(), http.StatusNotFound)
		return
	}
	var request struct {
		Content string `json:"content"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := decoder.Decode(&request); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "bad body: "+err.Error(), status)
		return
	}
	document, err := h.store.Save(id, request.Content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, document)
}

// Ideas lists the scout's discovery backlog (GET /api/ideas).
func (h *Handler) Ideas(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, h.store.Ideas())
}

// Idea returns one backlog file's title + body (GET /api/idea?id=<id>).
func (h *Handler) Idea(w http.ResponseWriter, r *http.Request) {
	idea, err := h.store.Idea(r.URL.Query().Get("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, idea)
}

// Promote appends an idea to products.md (POST /api/ideas/{id}/promote). The id
// is path-bound and allowlist-checked in the store; the optional note is the
// only body field.
func (h *Handler) Promote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var request struct {
		Note string `json:"note"`
	}
	// A body is optional (promote with no note); tolerate an empty one.
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := decoder.Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	document, err := h.store.Promote(id, request.Note, time.Now())
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrUnknownIdea) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, document)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
