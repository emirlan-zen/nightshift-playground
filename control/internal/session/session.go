// Package session owns remote-control session lifecycle rules.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"nightshift/control/internal/agent"
	"nightshift/control/internal/run"
)

var (
	ErrUnknownAgent = errors.New("unknown agent")
	ErrBadSlot      = errors.New("bad session name")
	ErrBadInstance  = errors.New("bad instance")
)

// Controller is the privileged remote-control boundary.
type Controller interface {
	Run(context.Context, string, string) (string, error)
}

type Session struct {
	Instance string `json:"instance"`
	Slot     string `json:"slot,omitempty"`
	Active   bool   `json:"active"`
	TTLUntil int64  `json:"ttl_until,omitempty"`
	// Executor is the engine behind the session (ADR-0020). Remote-control
	// sessions are always Claude — codex has no attach — but the field is
	// explicit so the UI can render an executor icon and a future codex-backed
	// session surface is a data change, not a schema change.
	Executor string `json:"executor"`
}

type AgentState struct {
	Company  string    `json:"company"`
	Active   bool      `json:"active"`
	TTLUntil int64     `json:"ttl_until,omitempty"`
	Sessions []Session `json:"sessions"`
}

type Result struct {
	Instance string `json:"instance"`
	Result   string `json:"result"`
}

type ActionResult struct {
	Company string `json:"company"`
	Action  string `json:"action"`
	Result  string `json:"result"`
}

type Service struct {
	agents     agent.Registry
	controller Controller
	logger     *slog.Logger
	mint       func() string
}

// slotPattern mirrors files/control/nightshift-rc. Keep both in sync.
var slotPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,19}[a-z0-9])?$`)

func NewService(agents agent.Registry, controller Controller, logger *slog.Logger) *Service {
	return &Service{agents: agents, controller: controller, logger: logger, mint: mintSlot}
}

func (s *Service) Status(ctx context.Context) []AgentState {
	states := make([]AgentState, 0, len(s.agents.All()))
	for _, agentID := range s.agents.All() {
		state := AgentState{Company: agentID, Sessions: s.sessions(ctx, agentID)}
		for _, current := range state.Sessions {
			if current.Active {
				state.Active = true
				if current.Instance == agentID {
					state.TTLUntil = current.TTLUntil
				}
			}
		}
		states = append(states, state)
	}
	return states
}

func (s *Service) Start(ctx context.Context, agentID, requestedSlot string) (Result, error) {
	if !s.agents.Contains(agentID) {
		return Result{}, ErrUnknownAgent
	}
	slot := Slugify(requestedSlot)
	instance := agentID
	if slot != "" {
		if !slotPattern.MatchString(slot) {
			return Result{}, ErrBadSlot
		}
		instance += "__" + slot
	} else if s.defaultActive(ctx, agentID) {
		instance += "__" + s.mint()
	}
	output, err := s.controller.Run(ctx, "start", instance)
	if err != nil {
		s.logger.Error("session.start_failed", "agent", agentID, "instance", instance, "error", err)
		return Result{Instance: instance, Result: output}, fmt.Errorf("start session: %w", err)
	}
	s.logger.Info("session.started", "agent", agentID, "instance", instance)
	return Result{Instance: instance, Result: output}, nil
}

func (s *Service) Stop(ctx context.Context, agentID, instance string) (Result, error) {
	if !s.agents.Contains(agentID) {
		return Result{}, ErrUnknownAgent
	}
	if !validInstance(agentID, instance) {
		return Result{}, ErrBadInstance
	}
	output, err := s.controller.Run(ctx, "stop", instance)
	if err != nil {
		s.logger.Error("session.stop_failed", "agent", agentID, "instance", instance, "error", err)
		return Result{Instance: instance, Result: output}, fmt.Errorf("stop session: %w", err)
	}
	s.logger.Info("session.stopped", "agent", agentID, "instance", instance)
	return Result{Instance: instance, Result: output}, nil
}

func (s *Service) Action(ctx context.Context, action, agentID string) (ActionResult, error) {
	if !s.agents.Contains(agentID) {
		return ActionResult{}, ErrUnknownAgent
	}
	output, err := s.controller.Run(ctx, action, agentID)
	if err != nil {
		s.logger.Error("agent.action_failed", "agent", agentID, "action", action, "error", err)
		return ActionResult{Company: agentID, Action: action, Result: output}, fmt.Errorf("%s agent: %w", action, err)
	}
	s.logger.Info("agent.action_completed", "agent", agentID, "action", action)
	return ActionResult{Company: agentID, Action: action, Result: output}, nil
}

func (s *Service) sessions(ctx context.Context, agentID string) []Session {
	raw, err := s.controller.Run(ctx, "sessions", agentID)
	if err != nil {
		s.logger.Warn("session.list_failed", "agent", agentID, "error", err)
		return []Session{}
	}
	result := []Session{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		instance := fields[0]
		if !validInstance(agentID, instance) {
			continue
		}
		current := Session{Instance: instance, Active: fields[1] == "active", Executor: string(run.ExecutorClaude)}
		if instance != agentID {
			current.Slot = strings.TrimPrefix(instance, agentID+"__")
		}
		if current.Active {
			current.TTLUntil = s.ttl(ctx, instance)
		}
		result = append(result, current)
	}
	return result
}

func (s *Service) defaultActive(ctx context.Context, agentID string) bool {
	for _, current := range s.sessions(ctx, agentID) {
		if current.Instance == agentID && current.Active {
			return true
		}
	}
	return false
}

func (s *Service) ttl(ctx context.Context, instance string) int64 {
	output, err := s.controller.Run(ctx, "ttl", instance)
	if err != nil {
		return 0
	}
	ttl, err := strconv.ParseInt(strings.TrimSpace(output), 10, 64)
	if err != nil {
		return 0
	}
	return ttl
}

func validInstance(agentID, instance string) bool {
	if instance == agentID {
		return true
	}
	slot, ok := strings.CutPrefix(instance, agentID+"__")
	return ok && slotPattern.MatchString(slot)
}

// Slugify converts an operator label to the constrained systemd slot syntax.
func Slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	lastDash := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			result.WriteRune(r)
			lastDash = false
		} else if !lastDash && result.Len() > 0 {
			result.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func mintSlot() string {
	var body [3]byte
	_, _ = rand.Read(body[:])
	return "s" + hex.EncodeToString(body[:])
}
