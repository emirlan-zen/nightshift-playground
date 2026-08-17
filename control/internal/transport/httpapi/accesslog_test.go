package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccessLogMutation(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/runs", func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) == "" {
			t.Error("request id missing from context")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})

	rr := httptest.NewRecorder()
	AccessLog(logger, mux).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runs", nil))
	if rr.Code != http.StatusCreated || rr.Header().Get("X-Request-ID") == "" {
		t.Fatalf("response = %d headers=%v", rr.Code, rr.Header())
	}

	event := decodeEvent(t, logs.Bytes())
	if event["msg"] != "http.request" || event["level"] != "INFO" {
		t.Fatalf("event = %#v", event)
	}
	if event["route"] != "POST /api/runs" || event["status"] != float64(http.StatusCreated) || event["bytes"] != float64(2) {
		t.Fatalf("event = %#v", event)
	}
}

func TestAccessLogPromotesFailures(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := AccessLog(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/fail", nil))

	event := decodeEvent(t, logs.Bytes())
	if event["level"] != "ERROR" || event["status"] != float64(http.StatusInternalServerError) {
		t.Fatalf("event = %#v", event)
	}
	if event["route"] != "unmatched" {
		t.Fatalf("unregistered route should not log a high-cardinality path: %#v", event)
	}
}

func decodeEvent(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatalf("decode log: %v: %s", err, body)
	}
	return event
}
