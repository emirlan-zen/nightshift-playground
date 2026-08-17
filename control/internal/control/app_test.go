package control

import "testing"

func TestNewApplicationRejectsInvalidLogging(t *testing.T) {
	cfg := runtimeConfig
	cfg.LogLevel = "extremely-loud"
	if _, err := newApplication(cfg); err == nil {
		t.Fatal("expected invalid logging configuration to fail startup")
	}
}
