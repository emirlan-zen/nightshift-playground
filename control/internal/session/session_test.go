package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nightshift/control/internal/agent"
)

type fakeController struct {
	calls []string
	run   func(string, string) (string, error)
}

func (f *fakeController) Run(_ context.Context, action, target string) (string, error) {
	f.calls = append(f.calls, action+" "+target)
	if f.run == nil {
		return "", nil
	}
	return f.run(action, target)
}

func testService(controller *fakeController) *Service {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewService(agent.NewRegistry([]string{"agent-a"}), controller, logger)
	service.mint = func() string { return "s123abc" }
	return service
}

func TestStatusParsesAndBoundsSessions(t *testing.T) {
	controller := &fakeController{run: func(action, _ string) (string, error) {
		switch action {
		case "sessions":
			return "agent-a active\nagent-a__cleanup inactive\nother active", nil
		case "ttl":
			return "1712345678", nil
		}
		return "", nil
	}}
	states := testService(controller).Status(context.Background())
	if len(states) != 1 || !states[0].Active || states[0].TTLUntil != 1712345678 {
		t.Fatalf("state = %+v", states)
	}
	if len(states[0].Sessions) != 2 || states[0].Sessions[1].Slot != "cleanup" {
		t.Fatalf("sessions = %+v", states[0].Sessions)
	}
}

func TestSessionsCarryExecutor(t *testing.T) {
	// Remote-control sessions always run Claude (codex has no attach), but the
	// payload carries the executor explicitly so the UI can render an executor
	// icon and stay future-proof (implement-2 → implement-3 contract, ADR-0020).
	controller := &fakeController{run: func(action, _ string) (string, error) {
		switch action {
		case "sessions":
			return "agent-a active", nil
		case "ttl":
			return "1", nil
		}
		return "", nil
	}}
	states := testService(controller).Status(context.Background())
	if len(states) != 1 || len(states[0].Sessions) != 1 {
		t.Fatalf("states = %+v", states)
	}
	if got := states[0].Sessions[0].Executor; got != "claude" {
		t.Fatalf("session executor = %q, want claude", got)
	}
}

func TestStartSelectsDefaultNamedAndMintedInstances(t *testing.T) {
	t.Run("default when free", func(t *testing.T) {
		controller := &fakeController{run: func(action, _ string) (string, error) {
			if action == "sessions" {
				return "agent-a inactive", nil
			}
			return "started", nil
		}}
		result, err := testService(controller).Start(context.Background(), "agent-a", "")
		if err != nil || result.Instance != "agent-a" {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})

	t.Run("minted when default active", func(t *testing.T) {
		controller := &fakeController{run: func(action, _ string) (string, error) {
			if action == "sessions" {
				return "agent-a active", nil
			}
			if action == "ttl" {
				return "123", nil
			}
			return "started", nil
		}}
		result, err := testService(controller).Start(context.Background(), "agent-a", "")
		if err != nil || result.Instance != "agent-a__s123abc" {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})

	t.Run("named slug", func(t *testing.T) {
		controller := &fakeController{}
		result, err := testService(controller).Start(context.Background(), "agent-a", "Repo Cleanup")
		if err != nil || result.Instance != "agent-a__repo-cleanup" {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})
}

func TestServiceRejectsBoundaryViolations(t *testing.T) {
	service := testService(&fakeController{})
	if _, err := service.Start(context.Background(), "evil", ""); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("start error = %v", err)
	}
	if _, err := service.Stop(context.Background(), "agent-a", "agent-a__BAD__x"); !errors.Is(err, ErrBadInstance) {
		t.Fatalf("stop error = %v", err)
	}
}

func TestHandlerMapsDomainAndAdapterErrors(t *testing.T) {
	t.Run("domain error is bad request", func(t *testing.T) {
		handler := NewHandler(testService(&fakeController{}))
		response := httptest.NewRecorder()
		handler.Start(response, httptest.NewRequest(http.MethodPost, "/api/session/start?c=evil", nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", response.Code)
		}
	})

	t.Run("adapter error is server error with output", func(t *testing.T) {
		controller := &fakeController{run: func(action, _ string) (string, error) {
			if action == "start" {
				return "boom", errors.New("failed")
			}
			return "", nil
		}}
		handler := NewHandler(testService(controller))
		response := httptest.NewRecorder()
		handler.Start(response, httptest.NewRequest(http.MethodPost, "/api/session/start?c=agent-a&slot=work", nil))
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "boom") {
			t.Fatalf("status = %d body = %q", response.Code, response.Body.String())
		}
	})
}

func TestStatusHandlerShape(t *testing.T) {
	controller := &fakeController{run: func(action, _ string) (string, error) {
		if action == "sessions" {
			return "agent-a inactive", nil
		}
		return "", nil
	}}
	response := httptest.NewRecorder()
	NewHandler(testService(controller)).Status(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var states []AgentState
	if err := json.Unmarshal(response.Body.Bytes(), &states); err != nil || len(states) != 1 {
		t.Fatalf("states = %+v, err = %v", states, err)
	}
	// Wire contract the UI consumes for executor icons: the JSON key is "executor".
	if body := response.Body.String(); !strings.Contains(body, `"executor":"claude"`) {
		t.Fatalf("status body missing executor field: %s", body)
	}
}
