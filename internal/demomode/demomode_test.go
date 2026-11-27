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

type mapReader struct {
	vals map[string]string
	err  map[string]error
}

func (m mapReader) Get(_ context.Context, key string) (string, error) {
	if err := m.err[key]; err != nil {
		return "", err
	}
	return m.vals[key], nil
}

func TestDemoAccount(t *testing.T) {
	ctx := context.Background()
	if u, on, err := DemoAccount(ctx, nil); err != nil || on || u != "" {
		t.Fatalf("nil store = (%q, %v, %v), want empty/false/nil", u, on, err)
	}
	if u, on, err := DemoAccount(ctx, mapReader{vals: map[string]string{SettingKey: "false", UsernameKey: "demo"}}); err != nil || on || u != "" {
		t.Fatalf("disabled = (%q, %v, %v), want empty/false/nil", u, on, err)
	}
	if u, on, err := DemoAccount(ctx, mapReader{vals: map[string]string{SettingKey: OnValue, UsernameKey: "   "}}); err != nil || on || u != "" {
		t.Fatalf("blank username = (%q, %v, %v), want empty/false/nil", u, on, err)
	}
	if u, on, err := DemoAccount(ctx, mapReader{vals: map[string]string{SettingKey: OnValue, UsernameKey: "  demo  "}}); err != nil || !on || u != "demo" {
		t.Fatalf("enabled = (%q, %v, %v), want demo/true/nil", u, on, err)
	}
	storeErr := errors.New("db down")
	if _, on, err := DemoAccount(ctx, mapReader{err: map[string]error{SettingKey: storeErr}}); !errors.Is(err, storeErr) || on {
		t.Fatalf("enabled error = (%v, %v), want false/%v", on, err, storeErr)
	}
	if _, on, err := DemoAccount(ctx, mapReader{vals: map[string]string{SettingKey: OnValue}, err: map[string]error{UsernameKey: storeErr}}); !errors.Is(err, storeErr) || on {
		t.Fatalf("username error = (%v, %v), want false/%v", on, err, storeErr)
	}
}
