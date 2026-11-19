package downloads

import (
	"bytes"
	"context"
	"errors"
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
	err := svc.serveLocalFile(context.Background(), w, httptest.NewRequest("GET", "/file", nil), path, "", 2)
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

func TestInProcessLimitSurvivesALoweredBurst(t *testing.T) {
	bm := NewBandwidthManager(0, 50*1024*1024)
	limiter := bm.getUserLimiter(2)
	bm.Reload(0, 512*1024)
	if limiter.Burst() >= 1024*1024 {
		t.Fatalf("burst %d was not lowered", limiter.Burst())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := bm.wait(ctx, 2, 1024*1024); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("charge above the lowered burst failed: %v", err)
	}
}
