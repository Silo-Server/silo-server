package access

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// settingStoreStub only answers the one method resolution reaches; the
// embedded nil interface panics on anything else, which is the point — this
// path must not touch the rest of the store.
type settingStoreStub struct {
	userstore.UserStore
	rows []userstore.SettingValue
	err  error
}

func (s settingStoreStub) ListSettingValuesForResolution(
	context.Context, userstore.SettingResolutionQuery,
) ([]userstore.SettingValue, error) {
	return s.rows, s.err
}

type capturingLogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r)
	h.mu.Unlock()
	return nil
}
func (h *capturingLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingLogHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

func captureLogs(t *testing.T) *capturingLogHandler {
	t.Helper()
	handler := &capturingLogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return handler
}

// TestPreferredMetadataLanguageLogsStoreFailures pins the operator signal: the
// value deliberately degrades to "" on a store failure, but before the cutover
// it rode on the already-loaded profile row where a load failure was a hard
// error. A silent degrade would make transient pool exhaustion — or a
// persistently broken query path — indistinguishable from "no preference".
func TestPreferredMetadataLanguageLogsStoreFailures(t *testing.T) {
	handler := captureLogs(t)

	store := settingStoreStub{err: errors.New("connection pool exhausted")}
	if got := PreferredMetadataLanguage(context.Background(), store, "profile-1"); got != "" {
		t.Fatalf("degraded value = %q, want \"\"", got)
	}

	records := handler.snapshot()
	if len(records) == 0 {
		t.Fatal("a store failure resolved to the default with no log output")
	}
	record := records[0]
	if record.Level < slog.LevelWarn {
		t.Errorf("logged at %v, want at least WARN", record.Level)
	}
	var loggedError, loggedProfile bool
	record.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "error":
			loggedError = strings.Contains(a.Value.String(), "connection pool exhausted")
		case "profile_id":
			loggedProfile = a.Value.String() == "profile-1"
		}
		return true
	})
	if !loggedError {
		t.Errorf("log %q does not carry the store error", record.Message)
	}
	if !loggedProfile {
		t.Errorf("log %q does not name the profile", record.Message)
	}
}

// TestPreferredMetadataLanguageStaysQuietWhenNothingIsStored: the healthy
// no-preference answer must not spam the log.
func TestPreferredMetadataLanguageStaysQuietWhenNothingIsStored(t *testing.T) {
	handler := captureLogs(t)

	if got := PreferredMetadataLanguage(context.Background(), settingStoreStub{}, "profile-1"); got != "" {
		t.Fatalf("no-preference value = %q, want \"\"", got)
	}
	if records := handler.snapshot(); len(records) != 0 {
		t.Errorf("healthy resolution logged %d records, want none", len(records))
	}
}
