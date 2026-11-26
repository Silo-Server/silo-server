package demomode

import (
	"context"
	"errors"
	"testing"
)

// failAfter answers value for its first read and fails every later one.
type failAfter struct {
	value string
	reads int
}

func (f *failAfter) Get(context.Context, string) (string, error) {
	f.reads++
	if f.reads > 1 {
		return "", errors.New("settings store unreachable")
	}
	return f.value, nil
}

// TestSettingRemembersLastRead: a failed read answers the last value read,
// so a server that is not a demo keeps working and a demo keeps refusing;
// only a process that never read the setting gets the error. Catches a
// failed read flipping what the server enforces.
func TestSettingRemembersLastRead(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, first, want string
	}{
		{"off then failure", "", ""},
		{"false then failure", "false", ""},
		{"on then failure", OnValue, OnValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(&failAfter{value: tc.first})
			if _, err := s.Get(ctx, SettingKey); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get(ctx, SettingKey)
			if err != nil || got != tc.want {
				t.Fatalf("after a failed read = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	never := New(&failAfter{reads: 1})
	if _, err := never.Get(ctx, SettingKey); err == nil {
		t.Fatal("a process that never read the setting answered without an error")
	}
}
