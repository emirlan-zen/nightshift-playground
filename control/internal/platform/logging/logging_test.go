package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewJSONHonorsLevelAndAttributes(t *testing.T) {
	var out bytes.Buffer
	logger, err := New(&out, Config{Level: "info", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("hidden")
	logger.Info("run.started", "agent", "playground", "run_id", "run-1")

	var event map[string]any
	if err := json.Unmarshal(out.Bytes(), &event); err != nil {
		t.Fatalf("log is not JSON: %v: %s", err, out.String())
	}
	if event["msg"] != "run.started" || event["agent"] != "playground" || event["run_id"] != "run-1" {
		t.Fatalf("event = %#v", event)
	}
}

func TestNewText(t *testing.T) {
	var out bytes.Buffer
	logger, err := New(&out, Config{Level: "warn", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	logger.Log(t.Context(), slog.LevelWarn, "auth.degraded", "component", "access")
	if got := out.String(); !strings.Contains(got, "msg=auth.degraded") || !strings.Contains(got, "component=access") {
		t.Fatalf("log = %q", got)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, Config{Level: "loud", Format: "json"}); err == nil {
		t.Fatal("expected invalid level error")
	}
	if _, err := New(&bytes.Buffer{}, Config{Level: "info", Format: "xml"}); err == nil {
		t.Fatal("expected invalid format error")
	}
}
