package audiobooks

import (
	"context"
	"errors"
	"testing"
)

// loginSettingsStore serves one stored value (or error) for every key.
type loginSettingsStore struct {
	value string
	err   error
}

func (s loginSettingsStore) Get(context.Context, string) (string, error) { return s.value, s.err }
func (loginSettingsStore) Set(context.Context, string, string) error     { return nil }
func (loginSettingsStore) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestStandaloneLoginEnabledReadsGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored string
		want   bool
	}{
		{"unset defaults on", "", true},
		{"gated off", "true", false},
		{"explicitly on", "false", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &ABSConfigProvider{Settings: loginSettingsStore{value: tc.stored}}
			got, err := provider.StandaloneLoginEnabled(t.Context())
			if err != nil {
				t.Fatalf("StandaloneLoginEnabled: %v", err)
			}
			if got != tc.want {
				t.Fatalf("StandaloneLoginEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// A settings read failure must not reopen login an operator turned off.
func TestStandaloneLoginEnabledReturnsReadError(t *testing.T) {
	wantErr := errors.New("settings unavailable")
	provider := &ABSConfigProvider{Settings: loginSettingsStore{err: wantErr}}
	enabled, err := provider.StandaloneLoginEnabled(t.Context())
	if !errors.Is(err, wantErr) {
		t.Fatalf("StandaloneLoginEnabled error = %v, want %v", err, wantErr)
	}
	if enabled {
		t.Fatal("StandaloneLoginEnabled reported enabled on a failed settings read")
	}
}
