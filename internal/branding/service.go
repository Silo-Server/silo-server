package branding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"path"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

// SettingsStore is the subset of the server settings repository the branding
// service needs.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// AssetStore stores branding asset bytes in the selected artwork backend.
type AssetStore interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) (io.ReadCloser, artworkstore.ObjectInfo, error)
	Stat(context.Context, string) (artworkstore.ObjectInfo, error)
}

// Service is the single source of truth for branding. It assembles a Snapshot
// from settings, processes/stores uploaded assets, and streams them back.
type Service struct {
	settings SettingsStore
	store    AssetStore
}

func NewService(settings SettingsStore, store AssetStore) *Service {
	return &Service{settings: settings, store: store}
}

// HasStorage reports whether asset uploads can be served.
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

// UploadAsset validates, processes, and stores an uploaded branding image,
// recording its content ref in settings. It returns the new ref ("<hash><ext>").
func (s *Service) UploadAsset(ctx context.Context, kind AssetKind, data []byte, declaredType string) (string, error) {
	spec, ok := assetSpecs[kind]
	if !ok {
		return "", ErrInvalidKind
	}
	if s == nil || s.store == nil {
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

	if err := s.store.Put(ctx, key, out); err != nil {
		return "", err
	}
	if err := s.settings.Set(ctx, spec.settingKey, ref); err != nil {
		return "", err
	}
	return ref, nil
}

// DeleteAsset clears the custom asset of the given kind. The stored object is left
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
// Returns ErrAssetNotConfigured when none is set or the object is missing.
func (s *Service) GetAsset(ctx context.Context, kind AssetKind) (data []byte, contentType, ref string, err error) {
	spec, ok := assetSpecs[kind]
	if !ok {
		return nil, "", "", ErrInvalidKind
	}
	ref, _ = s.settings.Get(ctx, spec.settingKey)
	if ref == "" {
		return nil, "", "", ErrAssetNotConfigured
	}
	if s == nil || s.store == nil {
		return nil, "", "", ErrStorageUnavailable
	}
	key := spec.s3Prefix + "/" + ref
	var reader io.ReadCloser
	reader, _, err = s.store.Get(ctx, key)
	if err == nil {
		data, err = io.ReadAll(reader)
		_ = reader.Close()
	}
	if err != nil {
		if errors.Is(err, artworkstore.ErrNotFound) {
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
	if s == nil {
		return 0, 0, nil
	}
	if s.store == nil {
		return 0, 0, nil
	}
	for kind, spec := range assetSpecs {
		ref, _ := s.settings.Get(ctx, spec.settingKey)
		if ref == "" {
			continue
		}
		checked++
		key := spec.s3Prefix + "/" + ref
		_, getErr := s.store.Stat(ctx, key)
		switch {
		case getErr == nil:
		case errors.Is(getErr, artworkstore.ErrNotFound):
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
