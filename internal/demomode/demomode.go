// Package demomode reads the demo-mode server settings (demo.enabled and
// demo.account_id) for every surface that enforces demo mode, remembering
// the last value it read of each so a failed read does not change what the
// server enforces.
package demomode

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// SettingKey is the server setting that runs the server as a public demo,
// whose visitors share one account that is not an administrator.
const SettingKey = "demo.enabled"

// AccountSettingKey names the demo's shared account by its account ID, or
// is empty. While demo mode is on, the per-name limit on failed password
// sign-ins does not apply to that account's sign-in names (auth.DemoSignIn).
const AccountSettingKey = "demo.account_id"

// OnValue is the only stored value that turns demo mode on. Every reader
// compares the raw stored value with it, untrimmed.
const OnValue = "true"

// Reader reads one server setting.
type Reader interface {
	Get(ctx context.Context, key string) (string, error)
}

// Remembered values of SettingKey.
const (
	unknown int32 = iota
	off
	on
)

// Setting reads SettingKey and AccountSettingKey through a settings store on
// every call and remembers the last value it read of each. When a read fails
// it answers that value, so a server that is not a demo keeps working
// through a database hiccup and a demo server keeps refusing what it
// refuses. Only a process that has never read the setting returns the
// error, and its callers fail closed. Each process (each API replica)
// remembers its own values; a change one replica has not read yet is picked
// up by its next successful read. It is safe for concurrent use. Other keys
// pass through to the store.
type Setting struct {
	store   Reader
	last    atomic.Int32
	account atomic.Pointer[string]
}

// New returns a Setting reading through store.
func New(store Reader) *Setting {
	return &Setting{store: store}
}

// Get answers key from the store. For SettingKey and AccountSettingKey a
// failed read is logged, unless the request itself was canceled, and
// answered with the last value read, or returned when there is none.
func (s *Setting) Get(ctx context.Context, key string) (string, error) {
	value, err := s.store.Get(ctx, key)
	switch key {
	case SettingKey:
		return s.enabled(ctx, value, err)
	case AccountSettingKey:
		return s.accountID(ctx, value, err)
	}
	return value, err
}

func (s *Setting) enabled(ctx context.Context, value string, err error) (string, error) {
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

func (s *Setting) accountID(ctx context.Context, value string, err error) (string, error) {
	if err == nil {
		s.account.Store(&value)
		return value, nil
	}
	if ctx.Err() == nil {
		slog.WarnContext(ctx, "reading the demo account setting failed", "component", "demomode", "error", err)
	}
	if last := s.account.Load(); last != nil {
		return *last, nil
	}
	return "", err
}
