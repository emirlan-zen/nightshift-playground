package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	for _, key := range []string{
		"LISTEN", "HOME", "COMPANIES", "RC_WRAPPER", "ACCESS_TEAM_DOMAIN",
		"ACCESS_AUD", "ALLOWED_EMAILS", "ACCESS_INSECURE_TRUST_HEADER",
		"NIGHTSHIFT_DEV", "LOG_LEVEL", "LOG_FORMAT", "LOG_ADD_SOURCE",
	} {
		t.Setenv(key, "")
	}

	cfg := Load()
	if cfg.Listen != "0.0.0.0:8787" || cfg.Home != "/home/agent" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0] != "playground" {
		t.Fatalf("agents = %v", cfg.Agents)
	}
	if cfg.LogFormat != "json" || cfg.LogLevel != "info" {
		t.Fatalf("logging defaults = %s/%s", cfg.LogFormat, cfg.LogLevel)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("LISTEN", "127.0.0.1:9999")
	t.Setenv("HOME", "/tmp/nightshift")
	t.Setenv("COMPANIES", "playground agent-b")
	t.Setenv("ACCESS_TEAM_DOMAIN", "team.cloudflareaccess.com/")
	t.Setenv("ACCESS_INSECURE_TRUST_HEADER", "1")
	t.Setenv("NIGHTSHIFT_DEV", "true")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_ADD_SOURCE", "true")
	t.Setenv("LOG_FORMAT", "")

	cfg := Load()
	if cfg.Listen != "127.0.0.1:9999" || cfg.Home != "/tmp/nightshift" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if len(cfg.Agents) != 2 || cfg.Agents[1] != "agent-b" {
		t.Fatalf("agents = %v", cfg.Agents)
	}
	if cfg.AccessTeamDomain != "team.cloudflareaccess.com" || !cfg.TrustAccessHeader {
		t.Fatalf("access config = %+v", cfg)
	}
	if !cfg.DevMode || cfg.LogFormat != "text" || cfg.LogLevel != "debug" || !cfg.LogAddSource {
		t.Fatalf("runtime config = %+v", cfg)
	}
}
