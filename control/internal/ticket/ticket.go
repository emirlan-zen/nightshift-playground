// Package ticket owns ticketboard aggregate rules independent of persistence
// and HTTP delivery.
package ticket

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	MaxTitleLength      = 200
	MaxBodyLength       = 16 * 1024
	MaxSweepSectionSize = 16 * 1024
)

var (
	ErrInvalidTitle      = errors.New("title empty or too long")
	ErrInvalidBody       = errors.New("body too long")
	ErrInvalidLane       = errors.New("bad lane (want hunt|improve|ops)")
	ErrEmptyUpdate       = errors.New("nothing to update")
	ErrInvalidTransition = errors.New("bad status transition")
	ErrInvalidNote       = errors.New("note too long")
)

type Note struct {
	At   time.Time `json:"at"`
	By   string    `json:"by"`
	Text string    `json:"text"`
}

type Ticket struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Status    string    `json:"status"`
	Lane      string    `json:"lane,omitempty"`
	CreatedBy string    `json:"createdBy"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	Notes     []Note    `json:"notes,omitempty"`
}

func New(id, agent, title, body, lane, createdBy string, now time.Time) (Ticket, error) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" || len(title) > MaxTitleLength {
		return Ticket{}, ErrInvalidTitle
	}
	if len(body) > MaxBodyLength {
		return Ticket{}, ErrInvalidBody
	}
	if !ValidLane(lane) {
		return Ticket{}, ErrInvalidLane
	}
	return Ticket{
		ID: id, Agent: agent, Title: title, Body: body, Lane: lane,
		Status: "open", CreatedBy: createdBy, Created: now, Updated: now,
	}, nil
}

func (t *Ticket) UpdateByOperator(status, note string, now time.Time) error {
	note = strings.TrimSpace(note)
	if status == "" && note == "" {
		return ErrEmptyUpdate
	}
	if len(note) > MaxBodyLength {
		return ErrInvalidNote
	}
	if status != "" {
		if !ValidStatus(status) || status == t.Status {
			return ErrInvalidTransition
		}
		t.Status = status
	}
	if note != "" {
		t.Notes = append(t.Notes, Note{At: now, By: "operator", Text: note})
	}
	t.Updated = now
	return nil
}

func ValidStatus(status string) bool {
	return status == "open" || status == "review" || status == "closed"
}

func ValidLane(lane string) bool {
	return lane == "" || lane == "hunt" || lane == "improve" || lane == "ops"
}

func SortByUpdated(tickets []Ticket) {
	sort.Slice(tickets, func(left, right int) bool {
		return tickets[left].Updated.After(tickets[right].Updated)
	})
}

func OpenSection(tickets []Ticket) string {
	open := make([]Ticket, 0, len(tickets))
	for _, current := range tickets {
		if current.Status == "open" {
			open = append(open, current)
		}
	}
	if len(open) == 0 {
		return ""
	}
	sort.Slice(open, func(left, right int) bool {
		return open[left].Created.Before(open[right].Created)
	})
	var result strings.Builder
	result.WriteString("\n\n## Open tickets (work these first)\n\n")
	result.WriteString("These are filed on the ticketboard. Prioritize them, in order, over " +
		"self-found work. When you finish one, move it to review with a note:\n" +
		"`nightshift-ticket review <id> \"what you did + where to look\"`\n" +
		"Never close tickets — only the operator closes. File follow-ups with " +
		"`nightshift-ticket create`.\n")
	for _, current := range open {
		entry := fmt.Sprintf("\n### %s — %s\n", current.ID, current.Title)
		body := current.Body
		remaining := MaxSweepSectionSize - result.Len() - len(entry)
		if len(body) > remaining {
			if remaining <= 0 {
				result.WriteString("\n(more open tickets omitted — run `nightshift-ticket list open`)\n")
				break
			}
			body = body[:remaining] + "\n[truncated — run `nightshift-ticket show " + current.ID + "`]"
		}
		result.WriteString(entry)
		if body != "" {
			result.WriteString(body + "\n")
		}
	}
	return result.String()
}
