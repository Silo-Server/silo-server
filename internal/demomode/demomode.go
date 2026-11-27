// Package demomode reads the demo.enabled server setting for every surface
// that enforces demo mode, remembering the last value it read so a failed
// read does not change what the server enforces.
package demomode

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
)

// SettingKey is the server setting that runs the server as a public demo,
// whose visitors share one account that is not an administrator.
const SettingKey = "demo.enabled"

// UsernameKey is the server setting naming the account, not an
// administrator, that public demo sign-in (POST /api/v2/auth/demo) signs
// visitors in to. Like SettingKey it is set only by the operator of a public
// demo; on any other server it is unset and demo sign-in is refused.
const UsernameKey = "demo.username"

// OnValue is the only stored value that turns demo mode on. Every reader
// compares the raw stored value with it, untrimmed.
const OnValue = "true"

// DemoAccount reads whether demo mode is on and, while it is, which account
// public demo sign-in uses: demo.username, trimmed, or "" when it is not set.
// A failed read is returned, never guessed.
func DemoAccount(ctx context.Context, store Reader) (username string, on bool, err error) {
	if store == nil {
		return "", false, nil
	}
	enabled, err := store.Get(ctx, SettingKey)
	if err != nil || enabled != OnValue {
		return "", false, err
	}
	username, err = store.Get(ctx, UsernameKey)
	if err != nil {
		return "", true, err
	}
	return strings.TrimSpace(username), true, nil
}

// Reader reads one server setting.
type Reader interface {
	Get(ctx context.Context, key string) (string, error)
}

// Remembered values of the setting.
const (
	unknown int32 = iota
	off
	on
)

// Setting reads SettingKey through a settings store on every call and
// remembers the last value it read. When a read fails it answers that value,
// so a server that is not a demo keeps working through a database hiccup
// and a demo server keeps refusing what it refuses. Only a process that has
// never read the setting returns the error, and its callers fail closed.
// Each process (each API replica) remembers its own value; a change one
// replica has not read yet is picked up by its next successful read.
// It is safe for concurrent use. Other keys pass through to the store.
type Setting struct {
	store Reader
	last  atomic.Int32
}

// New returns a Setting reading through store.
func New(store Reader) *Setting {
	return &Setting{store: store}
}

// Get answers key from the store. For SettingKey a failed read is logged,
// unless the request itself was canceled, and answered with the last value
// read, or returned when there is none.
func (s *Setting) Get(ctx context.Context, key string) (string, error) {
	value, err := s.store.Get(ctx, key)
	if key != SettingKey {
		return value, err
	}
	if err == nil {
		if value == OnValue {
			s.last.Store(on)
		} else {
			s.last.Store(off)
		}
		return value, nil
	}
	// A request its client abandoned cancels the read; that is not a failing
	// store, so it is not logged as one.
	if ctx.Err() == nil {
		slog.WarnContext(ctx, "reading the demo mode setting failed", "component", "demomode", "error", err)
	}
	switch s.last.Load() {
	case on:
		return OnValue, nil
	case off:
		return "", nil
	}
	return "", err
}
