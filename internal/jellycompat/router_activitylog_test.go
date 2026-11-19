package jellycompat

import (
	"context"
	"net/http"
	"sync"

	"github.com/Silo-Server/silo-server/internal/activitylog"
)

type activityCapture struct {
	mu      sync.Mutex
	entries []activitylog.LogEntry
}

func (c *activityCapture) Write(entry activitylog.LogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, entry)
}

func (c *activityCapture) Close() error { return nil }

func (c *activityCapture) take() []activitylog.LogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.entries
	c.entries = nil
	return out
}

// activityLoginResolver accepts one password and signs in to account 42.
type activityLoginResolver struct{}

func (activityLoginResolver) Resolve(_ context.Context, username, password, _, _ string) (*Session, error) {
	if password != "right" {
		return nil, &HTTPError{StatusCode: http.StatusUnauthorized, Message: "invalid credentials"}
	}
	return &Session{Token: "login-token", Username: username, StreamAppUserID: 42, ProfileID: "p1", PseudoUserID: PseudoUserID(42, "p1")}, nil
}
