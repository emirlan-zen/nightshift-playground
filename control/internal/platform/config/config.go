// Package config loads process configuration from the environment.
package config

import (
	"os"
	"strconv"
	"strings"
)

const defaultAgents = "playground"

// Config is immutable process configuration. Runtime domain state belongs in
// ~/.nightshift and must not be added here.
type Config struct {
	Listen            string
	Home              string
	Agents            []string
	RCWrapper         string
	AccessTeamDomain  string
	AccessAudience    string
	AllowedEmails     string
	TrustAccessHeader bool
	DevMode           bool
	LogLevel          string
	LogFormat         string
	LogAddSource      bool
}

// Load reads Config once at process startup.
func Load() Config {
	dev := boolEnv("NIGHTSHIFT_DEV")
	format := value("LOG_FORMAT", "json")
	if dev && os.Getenv("LOG_FORMAT") == "" {
		format = "text"
	}
	return Config{
		Listen:            value("LISTEN", "0.0.0.0:8787"),
		Home:              value("HOME", "/home/agent"),
		Agents:            strings.Fields(value("COMPANIES", defaultAgents)),
		RCWrapper:         value("RC_WRAPPER", "/usr/local/bin/nightshift-rc"),
		AccessTeamDomain:  strings.TrimSuffix(os.Getenv("ACCESS_TEAM_DOMAIN"), "/"),
		AccessAudience:    os.Getenv("ACCESS_AUD"),
		AllowedEmails:     os.Getenv("ALLOWED_EMAILS"),
		TrustAccessHeader: os.Getenv("ACCESS_INSECURE_TRUST_HEADER") == "1",
		DevMode:           dev,
		LogLevel:          value("LOG_LEVEL", "info"),
		LogFormat:         format,
		LogAddSource:      boolEnv("LOG_ADD_SOURCE"),
	}
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func boolEnv(key string) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && v
}
