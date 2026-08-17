package control

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"nightshift/control/internal/access"
	"nightshift/control/internal/agent"
	"nightshift/control/internal/focus"
	"nightshift/control/internal/machine"
	"nightshift/control/internal/session"
)

type noopController struct{}

func (noopController) Run(context.Context, string, string) (string, error) { return "", nil }

func testRouteHandlers(t *testing.T, logger *slog.Logger) routeHandlers {
	t.Helper()
	return routeHandlers{
		sessions: session.NewHandler(session.NewService(agent.NewRegistry(nil), noopController{}, logger)),
		focus:    focus.NewHandler(focus.NewStore(t.TempDir(), logger)),
		machine:  machine.NewHandler(machine.NewService(func() machine.Vitals { return machine.Vitals{OK: true} })),
	}
}

func TestRouterFailsClosedWithoutAccessConfig(t *testing.T) {
	rr := httptest.NewRecorder()
	logger := discardLogger()
	newRouter(logger, access.New(access.Config{}), testRouteHandlers(t, logger)).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if rr.Header().Get("X-Request-ID") == "" {
		t.Fatal("request id missing")
	}
}

func TestRouterServesAPIsInDevMode(t *testing.T) {
	oldHome := home
	t.Cleanup(func() { home = oldHome })
	home = t.TempDir()

	rr := httptest.NewRecorder()
	logger := discardLogger()
	newRouter(logger, access.New(access.Config{DevMode: true, DevEmail: devEmail}), testRouteHandlers(t, logger)).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/focus", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
