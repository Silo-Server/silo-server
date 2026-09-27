package watchsync

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.July, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		value  string
		want   time.Duration
		wantOK bool
	}{
		{"", 0, false},
		{"   ", 0, false},
		{"garbage", 0, false},
		{"-5", 0, false},
		{"1.5", 0, false},
		{"0", 0, true},
		{"7", 7 * time.Second, true},
		{" 30 ", 30 * time.Second, true},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
	}
	for _, tc := range cases {
		got, ok := ParseRetryAfter(tc.value, now)
		if got != tc.want || ok != tc.wantOK {
			t.Fatalf("ParseRetryAfter(%q) = %s, %t; want %s, %t", tc.value, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestParseRetryAfterSaturatesHugeDelays(t *testing.T) {
	got, ok := ParseRetryAfter("99999999999999999", time.Now())
	if !ok || got <= 0 {
		t.Fatalf("ParseRetryAfter(huge) = %s, %t; want a positive saturated duration", got, ok)
	}
}

func TestSleepContextReturnsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("SleepContext error = %v, want context.Canceled", err)
	}
	if err := SleepContext(context.Background(), 0); err != nil {
		t.Fatalf("SleepContext(0) = %v, want nil", err)
	}
}

func TestCredentialLimiterPacesEachCredentialSeparately(t *testing.T) {
	// One request per hour makes any second request for the same credential
	// block, without the test waiting for real time to pass.
	limiter := NewCredentialLimiter(time.Hour, 1)

	if err := limiter.Wait(context.Background(), "token-a"); err != nil {
		t.Fatalf("first wait for token-a: %v", err)
	}
	if err := limiter.Wait(context.Background(), "token-b"); err != nil {
		t.Fatalf("token-b must not wait behind token-a: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.Wait(ctx, "token-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("second wait for token-a = %v, want context.Canceled", err)
	}

	deadline, cancelDeadline := context.WithTimeout(context.Background(), time.Minute)
	defer cancelDeadline()
	if err := limiter.Wait(deadline, "token-a"); err == nil {
		t.Fatal("second wait for token-a returned before its next slot")
	}
}

func TestCredentialLimiterDropsIdleCredentials(t *testing.T) {
	clock := time.Now()
	limiter := NewCredentialLimiter(time.Second, 1)
	limiter.now = func() time.Time { return clock }

	if err := limiter.Wait(context.Background(), "rotated-token"); err != nil {
		t.Fatalf("wait: %v", err)
	}
	clock = clock.Add(credentialLimiterIdleTTL + time.Second)
	if err := limiter.Wait(context.Background(), "current-token"); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if len(limiter.limiters) != 1 {
		t.Fatalf("got %d limiters after sweep, want 1", len(limiter.limiters))
	}
}

func TestLimiterWaitErrorDefersRefusalsButKeepsCancellation(t *testing.T) {
	limiter := NewCredentialLimiter(time.Hour, 1)
	if err := limiter.Wait(context.Background(), "token"); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := LimiterWaitError(deadline, "trakt", time.Second, limiter.Wait(deadline, "token"))
	if limited, ok := AsRateLimited(err); !ok || limited.RetryAfter != time.Second || limited.Provider != "trakt" {
		t.Fatalf("refusal = %v, want a one-second RateLimitedError", err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	err = LimiterWaitError(canceled, "trakt", time.Second, limiter.Wait(canceled, "token"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait = %v, want context.Canceled", err)
	}
	if LimiterWaitError(context.Background(), "trakt", time.Second, nil) != nil {
		t.Fatal("a successful wait must stay nil")
	}
}
