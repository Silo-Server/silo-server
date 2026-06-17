package api

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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
// The admin flag is read per request, so it can be toggled without a restart.
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
		Enabled: func(ctx context.Context) bool {
			v, _ := settings.Get(ctx, ebookKindleConversionSettingKey)
			return isTruthySetting(v)
		},
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
