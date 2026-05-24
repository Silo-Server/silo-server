package markers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	SettingMode         = "markers.mode"
	SettingLazyPlayback = "markers.lazy_playback"
)

type Mode string

const (
	ModeOff    Mode = "off"
	ModeLocal  Mode = "local"
	ModeOnline Mode = "online"
	ModeBoth   Mode = "both"
)

var ErrInvalidSetting = errors.New("invalid marker setting")

type Provider interface {
	ID() string
	FetchMarkers(ctx context.Context, req Request) (Result, error)
}

type ItemKind int

const (
	ItemKindEpisode ItemKind = iota + 1
	ItemKindMovie
)

type MarkerKind int

const (
	MarkerKindIntro MarkerKind = iota + 1
	MarkerKindCredits
	MarkerKindRecap
	MarkerKindPreview
)

// Canonical keys for Request.ExternalIDs. Providers consult these so we
// don't scatter raw "tmdb"/"imdb" string literals across the codebase.
const (
	ExternalIDKeyTMDB = "tmdb"
	ExternalIDKeyIMDB = "imdb"
	ExternalIDKeyTVDB = "tvdb"
)

type Request struct {
	Kind          ItemKind
	ExternalIDs   map[string]string
	SeasonNumber  int
	EpisodeNumber int
	Duration      time.Duration
}

type Result struct {
	SourceClass string
	ProviderID  string
	Algorithm   string
	Markers     []Marker
}

type Marker struct {
	Kind       MarkerKind
	Start      time.Duration
	End        time.Duration
	Confidence float64
}

type Registry struct {
	providers []Provider
	logger    *slog.Logger
}

func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{logger: logger}
}

func (r *Registry) Register(provider Provider) error {
	if provider == nil {
		return fmt.Errorf("marker provider is nil")
	}
	id := strings.TrimSpace(provider.ID())
	if id == "" {
		return fmt.Errorf("marker provider ID is required")
	}
	for _, existing := range r.providers {
		if existing.ID() == id {
			return fmt.Errorf("marker provider %q already registered", id)
		}
	}
	r.providers = append(r.providers, provider)
	return nil
}

func (r *Registry) Providers() []Provider {
	if r == nil || len(r.providers) == 0 {
		return nil
	}
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	return out
}

func (r *Registry) FetchFirstHit(ctx context.Context, req Request) (Result, bool, error) {
	if r == nil || len(r.providers) == 0 {
		return Result{}, false, nil
	}

	var lastErr error
	for _, provider := range r.providers {
		result, err := provider.FetchMarkers(ctx, req)
		if err != nil {
			lastErr = err
			r.logProviderError(provider.ID(), req, err)
			continue
		}
		if len(result.Markers) == 0 {
			continue
		}
		if strings.TrimSpace(result.ProviderID) == "" {
			result.ProviderID = provider.ID()
		}
		if strings.TrimSpace(result.SourceClass) == "" {
			result.SourceClass = models.MarkerSourceOnline
		}
		return result, true, nil
	}

	return Result{}, false, lastErr
}

func (r *Registry) logProviderError(providerID string, req Request, err error) {
	logger := slog.Default()
	if r != nil && r.logger != nil {
		logger = r.logger
	}
	logger.Warn("marker provider fetch failed",
		"provider", providerID,
		"kind", req.Kind,
		"external_ids", sanitizeExternalIDs(req.ExternalIDs),
		"error", err,
	)
}

func NormalizeMode(raw string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case ModeOff:
		return ModeOff
	case ModeOnline:
		return ModeOnline
	case ModeBoth:
		return ModeBoth
	case ModeLocal:
		return ModeLocal
	default:
		return ModeLocal
	}
}

func ShouldRunLocal(mode Mode) bool {
	return mode == ModeLocal || mode == ModeBoth
}

func NormalizeSetting(key, value string) (string, error) {
	switch key {
	case SettingMode:
		normalized := string(NormalizeMode(value))
		if normalized != strings.ToLower(strings.TrimSpace(value)) {
			return "", fmt.Errorf("%w: %s must be one of off, local, online, both", ErrInvalidSetting, SettingMode)
		}
		return normalized, nil
	case SettingLazyPlayback:
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized != "true" && normalized != "false" {
			return "", fmt.Errorf("%w: %s must be true or false", ErrInvalidSetting, SettingLazyPlayback)
		}
		return normalized, nil
	default:
		return "", fmt.Errorf("%w: unsupported marker setting %s", ErrInvalidSetting, key)
	}
}

func sanitizeExternalIDs(ids map[string]string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]string, len(ids))
	for key, value := range ids {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	return out
}
