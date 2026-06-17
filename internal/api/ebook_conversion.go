package api

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/ebookconvert"
)

// ebookKindleConversionSettingKey is the admin flag that gates Kindle->EPUB
// conversion. Off by default; toggled via PUT /admin/settings/{key}.
const ebookKindleConversionSettingKey = "ebook.kindle_conversion_enabled"

// buildEbookConversion wires the in-process Kindle->EPUB converter for the read
// handler. It compiles the embedded WASM module once at startup; if that fails
// the feature stays off (returns nil) and the raw original is served as before.
// The admin flag is read with a short TTL (see ebookFlagPredicate), so it can be
// toggled without a restart yet does not hit the DB on every read.
//
// The Converter (a wazero runtime, ~no external resources — per-conversion
// scratch dirs are cleaned as they go) is intentionally owned for the process
// lifetime: it is not Close()d on shutdown because process exit reclaims it and
// there is nothing to flush. Memory is bounded per conversion by
// ebookconvert.DefaultMaxMemoryPages × DefaultConcurrency (see the design doc's
// resource notes); tune via ebookconvert.Options if the deployment is tight.
func buildEbookConversion(deps Dependencies, settings catalog.SettingsStore) *handlers.EbookConversion {
	if settings == nil {
		return nil
	}
	converter, err := ebookconvert.NewConverter(context.Background(), ebookconvert.Options{})
	if err != nil {
		slog.Warn("ebook Kindle->EPUB conversion unavailable: converter init failed", "error", err)
		return nil
	}
	cache, err := ebookconvert.NewCache(converter, ebookconvert.CacheOptions{Dir: ebookConversionCacheDir(deps.CurrentConfig())})
	if err != nil {
		slog.Warn("ebook Kindle->EPUB conversion unavailable: cache init failed", "error", err)
		_ = converter.Close(context.Background())
		return nil
	}
	slog.Info("ebook Kindle->EPUB conversion ready (admin-flag gated)", "setting", ebookKindleConversionSettingKey)
	return &handlers.EbookConversion{
		Converter: cache,
		Enabled:   ebookFlagPredicate(settings, ebookKindleConversionSettingKey),
	}
}

// ebookFlagPredicate returns a cached boolean reader for a server setting. The
// read path (every ebook read + the capability endpoint, which clients may
// poll) would otherwise issue a DB query per request; a short TTL collapses
// bursts to one query while keeping admin toggles effectively instant. On a read
// error it keeps the last known value rather than flapping the feature off.
func ebookFlagPredicate(settings catalog.SettingsStore, key string) func(context.Context) bool {
	const ttl = 3 * time.Second
	var (
		mu      sync.Mutex
		value   bool
		fetched time.Time
		hasVal  bool
	)
	return func(ctx context.Context) bool {
		mu.Lock()
		defer mu.Unlock()
		if hasVal && time.Since(fetched) < ttl {
			return value
		}
		v, err := settings.Get(ctx, key)
		if err != nil {
			return value // keep last known (false until the first successful read)
		}
		value = isTruthySetting(v)
		fetched = time.Now()
		hasVal = true
		return value
	}
}

// ebookConversionCacheDir derives the converted-EPUB cache directory, a sibling
// of the transcode dir so it lives alongside other derived media.
func ebookConversionCacheDir(cfg *config.Config) string {
	base := os.TempDir()
	if cfg != nil && strings.TrimSpace(cfg.Playback.TranscodeDir) != "" {
		base = filepath.Dir(cfg.Playback.TranscodeDir)
	}
	return filepath.Join(base, "silo-ebook-epub")
}

func isTruthySetting(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}
