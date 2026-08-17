package ticket

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewAndOperatorUpdate(t *testing.T) {
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	ticket, err := New("id", "playground", "  ship it ", " body ", "improve", "operator", now)
	if err != nil || ticket.Title != "ship it" || ticket.Status != "open" {
		t.Fatalf("ticket = %+v err = %v", ticket, err)
	}
	if err := ticket.UpdateByOperator("review", " ready ", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ticket.Status != "review" || len(ticket.Notes) != 1 || ticket.Notes[0].Text != "ready" {
		t.Fatalf("ticket = %+v", ticket)
	}
	if err := ticket.UpdateByOperator("review", "", now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewRejectsInvalidInput(t *testing.T) {
	if _, err := New("id", "a", "", "", "", "operator", time.Now()); !errors.Is(err, ErrInvalidTitle) {
		t.Fatalf("error = %v", err)
	}
	if _, err := New("id", "a", "title", "", "bad", "operator", time.Now()); !errors.Is(err, ErrInvalidLane) {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenSectionOrdersAndCapsTickets(t *testing.T) {
	now := time.Now()
	section := OpenSection([]Ticket{
		{ID: "new", Title: "new", Status: "open", Created: now},
		{ID: "old", Title: "old", Status: "open", Created: now.Add(-time.Hour)},
		{ID: "closed", Title: "closed", Status: "closed", Created: now.Add(-2 * time.Hour)},
	})
	if strings.Index(section, "old") > strings.Index(section, "new") || strings.Contains(section, "closed") {
		t.Fatalf("section = %q", section)
	}
}
