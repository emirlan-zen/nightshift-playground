package focus

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// The scout wave writes discovery ideas to ~/.nightshift/research/ideas/<date>.md
// as a detached backlog; today the only way to act on one is to SSH-edit
// focus/products.md. This surface lets the operator read the backlog and promote
// an idea into products.md (the Lane A gate) from the phone — the UX review's
// highest-leverage remaining feature.

var ErrUnknownIdea = errors.New("unknown idea")

// safeIdeaID rejects anything that could escape the ideas dir (slashes, dots,
// traversal). It is a cheap first gate; the authoritative allowlist is
// membership in the directory listing (ideaIDs), so an id is only ever joined to
// a path after we've confirmed the file exists.
var safeIdeaID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString

// Idea is one backlog file's metadata (no body — the list stays cheap).
type Idea struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	ModifiedAt int64  `json:"modifiedAt"`
}

// IdeaBody is a single idea with its rendered markdown body.
type IdeaBody struct {
	Idea
	Content string `json:"content"`
}

type IdeaCollection struct {
	Files []Idea `json:"files"`
}

// ideaIDs is the allowlist: the base names (without .md) of the regular markdown
// files actually present in the ideas dir, newest-first. Every read/promote is
// gated on membership here, so a client id can never address an arbitrary path.
// Only *regular* files qualify: a directory, socket, or — the one that matters —
// a symlink is excluded, so a link dropped into the (scout-writable) ideas dir
// can never become a known id that os.ReadFile would follow out of the backlog.
func (s *Store) ideaIDs() []string {
	entries, err := os.ReadDir(s.ideasDir)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		id := strings.TrimSuffix(name, ".md")
		if safeIdeaID(id) {
			ids = append(ids, id)
		}
	}
	// Idea files are dated (YYYY-MM-DD), so a descending name sort is
	// newest-first; falls back gracefully for any other naming.
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids
}

func (s *Store) knownIdea(id string) bool {
	if !safeIdeaID(id) {
		return false
	}
	for _, known := range s.ideaIDs() {
		if known == id {
			return true
		}
	}
	return false
}

// Ideas lists the backlog metadata, newest-first.
func (s *Store) Ideas() IdeaCollection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.ideaIDs()
	files := make([]Idea, 0, len(ids))
	for _, id := range ids {
		files = append(files, s.ideaMeta(id))
	}
	return IdeaCollection{Files: files}
}

// Idea returns one backlog file's title, mtime, and body. Unknown/unsafe id →
// ErrUnknownIdea (never a path error that could leak the filesystem shape).
func (s *Store) Idea(id string) (IdeaBody, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.knownIdea(id) {
		return IdeaBody{}, ErrUnknownIdea
	}
	body, info, err := s.readIdeaFile(id)
	if err != nil {
		return IdeaBody{}, ErrUnknownIdea
	}
	return IdeaBody{Idea: s.ideaMetaFrom(id, body, info), Content: string(body)}, nil
}

// readIdeaFile reads ideasDir/<id>.md, refusing to follow a symlink at the final
// component (O_NOFOLLOW → ELOOP) and refusing anything that is not a regular
// file. safeIdeaID already bars path separators and traversal, so the join stays
// inside the ideas dir; O_NOFOLLOW closes the one residual escape — a symlink
// swapped in between the ideaIDs() listing and this open (a list→open TOCTOU) —
// that would otherwise let a link read a secret outside the backlog.
func (s *Store) readIdeaFile(id string) ([]byte, os.FileInfo, error) {
	f, err := os.OpenFile(s.ideaPath(id), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, ErrUnknownIdea
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	return body, info, nil
}

// Promote appends a templated block for the idea (plus an optional operator
// note) to products.md through the same atomic Save path the editor uses, and
// returns the updated document. Refuses an unknown id and a result over the size
// cap. The idea file itself is left untouched (the backlog is history).
func (s *Store) Promote(id, note string, now time.Time) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.knownIdea(id) {
		return Document{}, ErrUnknownIdea
	}
	title := s.ideaMeta(id).Title
	current := s.load("products").Content
	next := appendPromotion(current, id, title, note, now)
	if len(next) > maxBody {
		return Document{}, fmt.Errorf("promoting would exceed the %d-byte products.md cap", maxBody)
	}
	return s.saveLocked("products", next)
}

func (s *Store) ideaPath(id string) string { return filepath.Join(s.ideasDir, id+".md") }

// ideaMeta reads the file (through the same no-follow path as Idea) to derive its
// title and mtime; a read failure degrades to the id as the title.
func (s *Store) ideaMeta(id string) Idea {
	body, info, err := s.readIdeaFile(id)
	if err != nil {
		return Idea{ID: id, Title: id}
	}
	return s.ideaMetaFrom(id, body, info)
}

func (s *Store) ideaMetaFrom(id string, body []byte, info os.FileInfo) Idea {
	idea := Idea{ID: id, Title: id, ModifiedAt: info.ModTime().Unix()}
	if t := firstHeading(string(body)); t != "" {
		idea.Title = t
	}
	return idea
}

// firstHeading returns the first non-empty line of a markdown file, stripped of
// leading '#'/whitespace and clamped, for use as a display title.
func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 120 {
			line = line[:120]
		}
		return line
	}
	return ""
}

// appendPromotion renders the block appended to products.md. Kept deterministic
// (the timestamp is passed in) so the promotion is testable.
func appendPromotion(current, id, title, note string, now time.Time) string {
	var b strings.Builder
	b.WriteString(current)
	if current != "" && !strings.HasSuffix(current, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n## ")
	b.WriteString(title)
	b.WriteString("\n\n")
	if note = strings.TrimSpace(note); note != "" {
		b.WriteString(note)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "_Promoted %s from the ideas backlog (`research/ideas/%s.md`)._\n",
		now.Format("2006-01-02"), id)
	return b.String()
}
