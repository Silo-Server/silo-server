package main

import (
	"log/slog"
	"os"
	"strconv"
	"strings"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

const (
	eventSocketsPerAccountEnv     = "SILO_EVENTS_MAX_SOCKETS_PER_ACCOUNT"
	eventSocketsPerProcessEnv     = "SILO_EVENTS_MAX_SOCKETS_PER_PROCESS"
	defaultEventSocketsPerAccount = 64
	defaultEventSocketsPerProcess = 2048
)

func eventSocketLimit(value, variable string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if limit, err := strconv.Atoi(value); err == nil && limit > 0 {
		return limit
	}
	// Environment input can contain secrets or control characters. Only name
	// the setting and the safe fallback, never the rejected value or parse error.
	slog.Warn("invalid event socket threshold; using default", "env", variable, "default", fallback)
	return fallback
}

func newEventSocketAdmission() (*evt.ClientSocketAdmission, error) {
	perAccount := eventSocketLimit(os.Getenv(eventSocketsPerAccountEnv), eventSocketsPerAccountEnv, defaultEventSocketsPerAccount)
	perProcess := eventSocketLimit(os.Getenv(eventSocketsPerProcessEnv), eventSocketsPerProcessEnv, defaultEventSocketsPerProcess)
	// Enforcement requires a separate reviewed rollout after ordinary-use
	// observation and compatible client releases. No environment toggle enables it.
	admission, err := evt.NewClientSocketAdmission(perAccount, perProcess, evt.ClientSocketObserve)
	if err != nil {
		return nil, err
	}
	slog.Info("event socket admission observes thresholds without enforcing quotas", "mode", evt.ClientSocketObserve, "per_account", perAccount, "per_process", perProcess)
	return admission, nil
}
