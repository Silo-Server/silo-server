package chapterthumbs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// hdrSettings is a settings reader a test can change while the backfill
// coordinator reads it.
type hdrSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *hdrSettings) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *hdrSettings) set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
}

// skipHDRRepo records the skipHDR flag of each width scan and reports the
// width complete.
type skipHDRRepo struct {
	testFileRepo
	scans chan bool
}

func (r *skipHDRRepo) ListChapterThumbnailsAtOtherWidths(_ context.Context, _ int, _ string, _ int, skipHDR bool) ([]*models.MediaFile, time.Time, error) {
	r.scans <- skipHDR
	return nil, time.Time{}, nil
}

// TestWidthBackfillFollowsTheHDRPolicy leaves HDR files out of width scans
// while the policy is disabled, since extraction skips them without a
// cooldown, and scans again when the policy changes.
func TestWidthBackfillFollowsTheHDRPolicy(t *testing.T) {
	repo := &skipHDRRepo{scans: make(chan bool, 4)}
	settings := &hdrSettings{values: map[string]string{
		config.PreviewImageWidthSettingKey: "320",
		chapterThumbnailHDRPolicySetting:   chapterThumbnailHDRPolicyDisabled,
	}}
	s := widthQueueService(repo)
	s.settings = settings
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.followPreviewWidthTicks(ctx, ticks)
	}()
	defer func() {
		cancel()
		<-done
	}()
	next := func() bool {
		t.Helper()
		select {
		case skip := <-repo.scans:
			return skip
		case <-time.After(5 * time.Second):
			t.Fatal("no width scan")
			return false
		}
	}

	if !next() {
		t.Fatal("first scan included HDR files while the HDR policy is disabled")
	}
	// A completed scan does not repeat while nothing changes.
	ticks <- time.Now()
	select {
	case <-repo.scans:
		t.Fatal("scanned again without a change")
	case <-time.After(50 * time.Millisecond):
	}
	settings.set(chapterThumbnailHDRPolicySetting, chapterThumbnailHDRPolicyBestEffort)
	ticks <- time.Now()
	if next() {
		t.Fatal("scan after enabling the HDR policy still skipped HDR files")
	}
}
