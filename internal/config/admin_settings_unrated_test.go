package config

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubSettingReader struct {
	value string
	err   error
}

func (s stubSettingReader) Get(context.Context, string) (string, error) { return s.value, s.err }

// TestNormalizeUnratedContentSetting pins the setting as a two-value enum: a
// parental control must not accept a typo and silently mean something else.
func TestNormalizeUnratedContentSetting(t *testing.T) {
	for _, raw := range []string{"hide", " hide ", "allow", "allow\n"} {
		got, err := NormalizeAdminSetting(AccessUnratedContentSettingKey, raw)
		if err != nil {
			t.Fatalf("NormalizeAdminSetting(%q) = %v", raw, err)
		}
		if got != AccessUnratedContentHide && got != AccessUnratedContentAllow {
			t.Fatalf("NormalizeAdminSetting(%q) = %q, want hide or allow", raw, got)
		}
	}
	for _, raw := range []string{"", "true", "show", "Hidden"} {
		if _, err := NormalizeAdminSetting(AccessUnratedContentSettingKey, raw); err == nil {
			t.Errorf("NormalizeAdminSetting(%q) accepted an unknown value", raw)
		}
	}
	if adminSettingDefaults[AccessUnratedContentSettingKey] != AccessUnratedContentHide {
		t.Errorf("default = %q, want the historical behavior of hiding unrated titles",
			adminSettingDefaults[AccessUnratedContentSettingKey])
	}
}

// TestUnratedContentPolicyFailsClosed: only an explicit "allow" loosens a
// ceiling. A read failure, an unset row, or a nil reader keeps titles hidden.
func TestUnratedContentPolicyFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		policy *UnratedContentPolicy
		want   bool
	}{
		{"allow", NewUnratedContentPolicy(stubSettingReader{value: "allow"}), true},
		{"allow with whitespace and case", NewUnratedContentPolicy(stubSettingReader{value: " Allow "}), true},
		{"hide", NewUnratedContentPolicy(stubSettingReader{value: "hide"}), false},
		{"unset", NewUnratedContentPolicy(stubSettingReader{}), false},
		{"read failure", NewUnratedContentPolicy(stubSettingReader{value: "allow", err: errors.New("boom")}), false},
		{"no reader", NewUnratedContentPolicy(nil), false},
		{"nil policy", nil, false},
	}
	for _, tc := range cases {
		if got := tc.policy.AllowUnratedContent(context.Background()); got != tc.want {
			t.Errorf("%s: AllowUnratedContent = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type countingSettingReader struct {
	value string
	err   error
	reads int
}

func (s *countingSettingReader) Get(context.Context, string) (string, error) {
	s.reads++
	return s.value, s.err
}

// TestUnratedContentPolicyCachesSuccessfulReads checks the policy does not hit
// the settings table on every scope resolution, still picks up a change once
// the TTL passes, and never caches a failed read.
func TestUnratedContentPolicyCachesSuccessfulReads(t *testing.T) {
	ctx := context.Background()
	reader := &countingSettingReader{value: "allow"}
	policy := NewUnratedContentPolicy(reader)
	now := time.Unix(1000, 0)
	policy.now = func() time.Time { return now }

	for range 2 {
		if !policy.AllowUnratedContent(ctx) {
			t.Fatal("want allow")
		}
	}
	if reader.reads != 1 {
		t.Fatalf("reads = %d within the TTL, want 1", reader.reads)
	}

	reader.value = "hide"
	now = now.Add(unratedContentCacheTTL)
	if policy.AllowUnratedContent(ctx) {
		t.Fatal("want hide once the TTL has passed")
	}

	reader.err = errors.New("boom")
	now = now.Add(unratedContentCacheTTL)
	if policy.AllowUnratedContent(ctx) {
		t.Fatal("a failed read must hide unrated titles")
	}
	reader.err = nil
	reader.value = "allow"
	reads := reader.reads
	if !policy.AllowUnratedContent(ctx) || reader.reads != reads+1 {
		t.Fatal("a failed read must not be cached")
	}
}

type blockingSettingReader struct {
	release chan struct{}
}

func (s blockingSettingReader) Get(ctx context.Context, _ string) (string, error) {
	select {
	case <-s.release:
		return "allow", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestUnratedContentPolicyDoesNotSerializeOnSlowReads checks a stalled
// settings read does not block other callers behind the cache lock: a caller
// whose context is canceled returns the fail-closed default while the first
// read is still outstanding.
func TestUnratedContentPolicyDoesNotSerializeOnSlowReads(t *testing.T) {
	reader := blockingSettingReader{release: make(chan struct{})}
	policy := NewUnratedContentPolicy(reader)

	done := make(chan bool)
	go func() { done <- policy.AllowUnratedContent(context.Background()) }()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	returned := make(chan bool)
	go func() { returned <- policy.AllowUnratedContent(canceled) }()
	select {
	case got := <-returned:
		if got {
			t.Fatal("a canceled read must hide unrated titles")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second caller blocked behind the stalled settings read")
	}

	close(reader.release)
	if !<-done {
		t.Fatal("want allow once the read completes")
	}
}
