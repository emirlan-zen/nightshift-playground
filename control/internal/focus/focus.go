// Package focus owns the operator-editable north-star documents.
package focus

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

var ErrUnknownDocument = errors.New("unknown focus document")

var documentIDs = []string{"products", "projects"}

type Document struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	ModifiedAt int64  `json:"modifiedAt"`
}

type Collection struct {
	Files []Document `json:"files"`
}

type Store struct {
	dir      string
	ideasDir string
	logger   *slog.Logger
	mu       sync.RWMutex
}

func NewStore(home string, logger *slog.Logger) *Store {
	return &Store{
		dir:      filepath.Join(home, ".nightshift", "focus"),
		ideasDir: filepath.Join(home, ".nightshift", "research", "ideas"),
		logger:   logger,
	}
}

func (s *Store) All() Collection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	collection := Collection{Files: make([]Document, 0, len(documentIDs))}
	for _, id := range documentIDs {
		collection.Files = append(collection.Files, s.load(id))
	}
	return collection
}

func (s *Store) Get(id string) (Document, error) {
	if !validID(id) {
		return Document{}, ErrUnknownDocument
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.load(id), nil
}

func (s *Store) Save(id, content string) (Document, error) {
	if !validID(id) {
		return Document{}, ErrUnknownDocument
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(id, content)
}

// saveLocked is Save's body without the lock — callers that already hold s.mu
// (e.g. Promote's read-modify-write of products.md) use this to avoid a
// re-entrant lock. id must already be validated.
func (s *Store) saveLocked(id, content string) (Document, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Document{}, fmt.Errorf("create focus directory: %w", err)
	}
	path := s.path(id)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(content), 0o644); err != nil {
		return Document{}, fmt.Errorf("write focus document: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return Document{}, fmt.Errorf("replace focus document: %w", err)
	}
	s.logger.Info("focus.saved", "document", id, "bytes", len(content))
	return s.load(id), nil
}

func (s *Store) load(id string) Document {
	document := Document{ID: id}
	path := s.path(id)
	body, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logger.Warn("focus.read_failed", "document", id, "error", err)
		}
		return document
	}
	document.Content = string(body)
	if info, err := os.Stat(path); err == nil {
		document.ModifiedAt = info.ModTime().Unix()
	}
	return document
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".md")
}

func validID(id string) bool {
	for _, allowed := range documentIDs {
		if id == allowed {
			return true
		}
	}
	return false
}
