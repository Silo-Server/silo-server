package downloads

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestLocalDownloadBudgetFailureReturnsCommittedError(t *testing.T) {
	shortenBudgetGrace(t)
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	if err := os.WriteFile(path, []byte("synthetic media"), 0600); err != nil {
		t.Fatal(err)
	}
	bm := unknownSharedManager(t, func(context.Context) (int64, int64, error) {
		return 0, 0, errors.New("budget unavailable")
	})
	svc := &Service{bandwidth: bm}
	w := httptest.NewRecorder()
	err := svc.serveLocalFile(context.Background(), w, httptest.NewRequest("GET", "/file", nil), path, 2)
	if !errors.Is(err, ErrResponseCommitted) || w.Body.Len() != 0 {
		t.Fatalf("error %v; exposed %d bytes", err, w.Body.Len())
	}
}

func TestThrottledReaderDoesNotExposeUnchargedBytes(t *testing.T) {
	shortenBudgetGrace(t)
	bm := unknownSharedManager(t, func(context.Context) (int64, int64, error) { return 0, 0, errors.New("budget unavailable") })
	reader := bm.ThrottledStreamReader(context.Background(), bytes.NewReader([]byte("private media")), 2)
	buffer := make([]byte, 100)
	if n, err := reader.Read(buffer); n != 0 || err == nil {
		t.Fatalf("read %d bytes, error %v", n, err)
	}
}

func TestSharedBandwidthBudgetRedis(t *testing.T) {
	url := os.Getenv("SILO_TEST_REDIS_URL")
	if url == "" {
		t.Skip("SILO_TEST_REDIS_URL is not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	defer func() { _ = client.Close() }()
	ctx := context.Background()
	prefix := fmt.Sprintf("test:downloads:{%d}", time.Now().UnixNano())
	global, account := prefix+":global", prefix+":account"
	defer client.Del(ctx, global, account, prefix+":other")
	debit := func(user string, rate int64) int64 {
		t.Helper()
		delay, err := sharedBandwidthScript.Run(ctx, client, []string{global, user}, rate, 0, 32768).Int64()
		if err != nil {
			t.Fatal(err)
		}
		return delay
	}
	if got := debit(account, 131072); got != 0 {
		t.Fatalf("first chunk delay %d", got)
	}
	// A second replica/account must use the same exhausted server budget.
	if got := debit(prefix+":other", 131072); got < 200000 {
		t.Fatalf("independent replica allowance: delay %d", got)
	}
	// Editing the rate preserves the charged balance rather than granting a
	// fresh burst. Re-instantiating a script/manager does not reset Redis state.
	if got := debit(account, 65536); got < 400000 {
		t.Fatalf("rate edit refilled bucket: delay %d", got)
	}
}

func shortenBudgetGrace(t *testing.T) {
	t.Helper()
	grace, retry := sharedBudgetGrace, sharedBudgetRetry
	sharedBudgetGrace, sharedBudgetRetry = 200*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { sharedBudgetGrace, sharedBudgetRetry = grace, retry })
}

// unreachableRedis is a client whose every command fails quickly.
func unreachableRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// unknownSharedManager is a shared-mode manager that has never read its rates.
func unknownSharedManager(t *testing.T, rates func(context.Context) (int64, int64, error)) *BandwidthManager {
	t.Helper()
	bm := NewBandwidthManager(0, 0)
	bm.SetSharedBudget(unreachableRedis(t), rates)
	bm.ratesKnown = false
	return bm
}

func TestSharedBudgetReadsSettingsOncePerTTL(t *testing.T) {
	reads := 0
	bm := unknownSharedManager(t, func(context.Context) (int64, int64, error) {
		reads++
		return 0, 125000, nil
	})
	for range 100 {
		if _, _, err := bm.currentRates(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Fatalf("settings read %d times for one cached window", reads)
	}
}

func TestUnlimitedTransferUsesTheRawReader(t *testing.T) {
	bm := NewBandwidthManager(0, 0)
	bm.SetSharedBudget(unreachableRedis(t), func(context.Context) (int64, int64, error) { return 0, 0, nil })
	raw := bytes.NewReader([]byte("media"))
	if got := bm.ThrottledReader(context.Background(), raw, 2); got != raw {
		t.Fatalf("unlimited transfer wrapped as %T", got)
	}
}

func TestWithoutRedisLimitsStayInProcess(t *testing.T) {
	bm := NewBandwidthManager(0, 4*1024*1024)
	bm.SetSharedBudget(nil, func(context.Context) (int64, int64, error) { return 0, 0, errors.New("unused") })
	reader := bm.ThrottledStreamReader(context.Background(), bytes.NewReader(make([]byte, 64*1024)), 2)
	if got, err := io.ReadAll(reader); err != nil || len(got) != 64*1024 {
		t.Fatalf("read %d bytes, error %v", len(got), err)
	}
}

func TestBandwidthChunkScalesWithTheStrictestRate(t *testing.T) {
	for _, tc := range []struct {
		server, user int64
		want         int
	}{
		{0, 0, minBandwidthChunk},
		{125000, 0, minBandwidthChunk},
		{0, 125000000, maxBandwidthChunk},
		{12500000, 125000000, 12500000 / 40},
	} {
		if got := bandwidthChunk(tc.server, tc.user); got != tc.want {
			t.Fatalf("chunk(%d, %d) = %d, want %d", tc.server, tc.user, got, tc.want)
		}
		if limit := min(nonZero(tc.server), nonZero(tc.user)); limit > 0 && int64(bandwidthChunk(tc.server, tc.user)) > max(limit/4, 32*1024) {
			t.Fatalf("chunk above burst for %d/%d", tc.server, tc.user)
		}
	}
}

func nonZero(v int64) int64 {
	if v == 0 {
		return 1 << 62
	}
	return v
}

func TestSharedBudgetRetriesTransientSettingsFailure(t *testing.T) {
	shortenBudgetGrace(t)
	failures := 3
	bm := unknownSharedManager(t, func(context.Context) (int64, int64, error) {
		if failures > 0 {
			failures--
			return 0, 0, errors.New("database restarting")
		}
		return 0, 0, nil
	})
	if err := bm.wait(context.Background(), 2, 1024); err != nil {
		t.Fatalf("wait after transient failures: %v", err)
	}
}

func TestSharedBudgetKeepsLastKnownRatesWhenSettingsFail(t *testing.T) {
	fail := false
	bm := unknownSharedManager(t, func(context.Context) (int64, int64, error) {
		if fail {
			return 0, 0, errors.New("database unavailable")
		}
		return 0, 125000, nil
	})
	if server, user, err := bm.currentRates(context.Background()); err != nil || server != 0 || user != 125000 {
		t.Fatalf("initial rates %d/%d, error %v", server, user, err)
	}
	fail = true
	bm.mu.Lock()
	bm.ratesAt = time.Now().Add(-bandwidthRatesTTL)
	bm.mu.Unlock()
	if server, user, err := bm.currentRates(context.Background()); err != nil || server != 0 || user != 125000 {
		t.Fatalf("rates after failed refresh %d/%d, error %v", server, user, err)
	}
}

func TestSharedBudgetFailsClosedAfterGrace(t *testing.T) {
	shortenBudgetGrace(t)
	bm := NewBandwidthManager(0, 125000)
	bm.SetSharedBudget(unreachableRedis(t), func(context.Context) (int64, int64, error) { return 0, 125000, nil })
	reader := bm.ThrottledStreamReader(context.Background(), bytes.NewReader([]byte("private media")), 2)
	buffer := make([]byte, 100)
	if n, err := reader.Read(buffer); n != 0 || err == nil {
		t.Fatalf("capped read with Redis down exposed %d bytes, error %v", n, err)
	}
}
