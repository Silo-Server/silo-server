package branding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/url"
	"path"
	"time"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

// SettingsStore is the subset of the server settings repository the branding
// service needs.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// AssetStore is the subset of the S3 client used for branding asset bytes.
type AssetStore interface {
	PutObject(ctx context.Context, bucket, key string, data []byte) error
	GetObject(ctx context.Context, bucket, key string) ([]byte, error)
	Bucket() string
}

// PublicAssetURLProvider is an optional AssetStore capability for deriving
// configured storage URLs. Byte-only stores remain valid AssetStore
// implementations and fail closed when Complex-facing metadata is requested.
type PublicAssetURLProvider interface {
	PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error)
}

var _ PublicAssetURLProvider = (*s3client.Client)(nil)

const maxPublicAssetURLLength = 2048

// Service is the single source of truth for branding. It assembles a Snapshot
// from settings, processes/stores uploaded assets, and streams them back.
//
// The store is optional: when S3 is not configured it is nil and asset
// upload/serving returns ErrStorageUnavailable, while text branding (name,
// subtitle, accent, default theme) keeps working.
type Service struct {
	settings SettingsStore
	store    AssetStore
}

// NewService constructs a branding Service. Pass a nil store when S3 is not
// configured: text branding keeps working while asset upload/serve returns
// ErrStorageUnavailable. Callers holding a concrete *s3client.Client must pass
// a nil AssetStore when that client is nil (rather than the typed-nil pointer)
// to avoid the typed-nil interface trap — see the construction in cmd/silo.
func NewService(settings SettingsStore, store AssetStore) *Service {
	return &Service{settings: settings, store: store}
}

// HasStorage reports whether asset upload/serving is available.
func (s *Service) HasStorage() bool { return s != nil && s.store != nil }

// Load reads the current branding configuration. Per-key read errors are
// tolerated and fall back to defaults so the SPA always renders.
func (s *Service) Load(ctx context.Context) Snapshot {
	get := func(key string) string {
		v, _ := s.settings.Get(ctx, key)
		return v
	}
	snap := Snapshot{
		ServerName:    firstNonEmpty(get(KeyServerName), DefaultServerName),
		LoginSubtitle: firstNonEmpty(get(KeyLoginSubtitle), DefaultLoginSubtitle),
		AccentColor:   get(KeyAccentColor),
		DefaultTheme:  get(KeyDefaultTheme),
		assets:        make(map[AssetKind]string, len(assetSpecs)),
	}
	for kind, spec := range assetSpecs {
		if ref := get(spec.settingKey); ref != "" {
			snap.assets[kind] = ref
		}
	}
	return snap
}

// PublicAssetURL returns the stable public URL and content ref for the
// preferred Complex-facing logo (mark, then wordmark). The storage client is
// the sole URL source: request hosts and other browser-controlled origins are
// never consulted.
//
// Public URL mode returns an unsigned query-free URL regardless of the expiry
// argument. Presigned and finite-lived token modes return query parameters and
// are rejected here, as are malformed or non-HTTPS URLs.
func (s *Service) PublicAssetURL(ctx context.Context) (assetURL, etag string) {
	if s == nil || !s.HasStorage() {
		return "", ""
	}
	urlProvider, ok := s.store.(PublicAssetURLProvider)
	if !ok {
		return "", ""
	}

	for _, kind := range []AssetKind{KindMark, KindWordmark} {
		spec := assetSpecs[kind]
		ref, _ := s.settings.Get(ctx, spec.settingKey)
		if ref == "" {
			continue
		}

		key := spec.s3Prefix + "/" + ref
		candidate, err := urlProvider.PresignGetURL(ctx, s.store.Bucket(), key, time.Hour)
		if err != nil || !isSafePublicAssetURL(candidate) {
			return "", ""
		}
		return candidate, ref
	}

	return "", ""
}

func isSafePublicAssetURL(candidate string) bool {
	if candidate == "" || len(candidate) > maxPublicAssetURLLength {
		return false
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" {
		return false
	}
	return parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == ""
}

// UploadAsset validates, processes, and stores an uploaded branding image,
// recording its content ref in settings. It returns the new ref ("<hash><ext>").
func (s *Service) UploadAsset(ctx context.Context, kind AssetKind, data []byte, declaredType string) (string, error) {
	spec, ok := assetSpecs[kind]
	if !ok {
		return "", ErrInvalidKind
	}
	if !s.HasStorage() {
		return "", ErrStorageUnavailable
	}

	out, _, ext, err := spec.process(data, declaredType)
	if err != nil {
		return "", err
	}

	// Content-address by the hash of the *stored* bytes: the ref then truly
	// identifies what is served, so the ?v=<ref> cache-buster changes exactly
	// when the served content changes (and identical outputs dedupe).
	sum := sha256.Sum256(out)
	ref := hex.EncodeToString(sum[:])[:16] + ext
	key := spec.s3Prefix + "/" + ref

	if err := s.store.PutObject(ctx, s.store.Bucket(), key, out); err != nil {
		return "", err
	}
	if err := s.settings.Set(ctx, spec.settingKey, ref); err != nil {
		return "", err
	}
	return ref, nil
}

// DeleteAsset clears the custom asset of the given kind. The S3 object is left
// in place (orphaned objects are cheap and avoid concurrent-reader races); the
// empty settings value is what deactivates it.
func (s *Service) DeleteAsset(ctx context.Context, kind AssetKind) error {
	spec, ok := assetSpecs[kind]
	if !ok {
		return ErrInvalidKind
	}
	return s.settings.Set(ctx, spec.settingKey, "")
}

// GetAsset fetches the bytes of the current custom asset of the given kind.
// Returns ErrAssetNotConfigured when none is set, ErrStorageUnavailable when S3
// is absent, and ErrAssetNotConfigured when the object is missing in S3.
func (s *Service) GetAsset(ctx context.Context, kind AssetKind) (data []byte, contentType, ref string, err error) {
	spec, ok := assetSpecs[kind]
	if !ok {
		return nil, "", "", ErrInvalidKind
	}
	ref, _ = s.settings.Get(ctx, spec.settingKey)
	if ref == "" {
		return nil, "", "", ErrAssetNotConfigured
	}
	if !s.HasStorage() {
		return nil, "", "", ErrStorageUnavailable
	}
	key := spec.s3Prefix + "/" + ref
	data, err = s.store.GetObject(ctx, s.store.Bucket(), key)
	if err != nil {
		if errors.Is(err, s3client.ErrNotFound) {
			return nil, "", "", ErrAssetNotConfigured
		}
		return nil, "", "", err
	}
	return data, contentTypeForExt(path.Ext(ref)), ref, nil
}

// ReconcileMissingAssets clears the ref of every configured branding asset
// whose stored object no longer exists (e.g. after the public S3 provider
// changed without migrating data), so the UI falls back to the built-in
// defaults instead of serving broken images. Returns how many configured
// assets were checked and how many of those were cleared.
func (s *Service) ReconcileMissingAssets(ctx context.Context) (checked, cleared int, err error) {
	if s == nil || !s.HasStorage() {
		return 0, 0, nil
	}
	for kind, spec := range assetSpecs {
		ref, _ := s.settings.Get(ctx, spec.settingKey)
		if ref == "" {
			continue
		}
		checked++
		key := spec.s3Prefix + "/" + ref
		_, getErr := s.store.GetObject(ctx, s.store.Bucket(), key)
		switch {
		case getErr == nil:
		case errors.Is(getErr, s3client.ErrNotFound):
			if setErr := s.settings.Set(ctx, spec.settingKey, ""); setErr != nil {
				return checked, cleared, setErr
			}
			cleared++
			slog.Warn("branding: cleared asset whose stored object is missing", "kind", kind, "key", key)
		default:
			return checked, cleared, getErr
		}
	}
	return checked, cleared, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
