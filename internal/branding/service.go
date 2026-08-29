package branding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// AssetStore is the subset of the canonical artwork store used for branding
// asset bytes. Addressing objects by logical key rather than by bucket is what
// makes branding work on a filesystem store as well as on S3.
type AssetStore interface {
	WriteImmutable(ctx context.Context, key string, data []byte, meta artworkstore.ObjectMetadata) error
	Open(ctx context.Context, key string) (*artworkstore.Object, error)
	Stat(ctx context.Context, key string) (artworkstore.ObjectInfo, error)
}

// Service is the single source of truth for branding. It assembles a Snapshot
// from settings, processes/stores uploaded assets, and streams them back.
//
// The store is optional: without artwork storage it is nil and asset
// upload/serving returns ErrStorageUnavailable, while text branding (name,
// subtitle, accent, default theme) keeps working.
type Service struct {
	settings SettingsStore
	store    AssetStore
}

// NewService constructs a branding Service. Pass a nil store when no artwork
// store is available: text branding keeps working while asset upload/serve
// returns ErrStorageUnavailable. Callers holding a concrete store pointer must
// pass a nil AssetStore when that pointer is nil (rather than the typed-nil
// pointer) to avoid the typed-nil interface trap — see the construction in
// cmd/silo.
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

	out, serveType, ext, err := spec.process(data, declaredType)
	if err != nil {
		return "", err
	}

	// Content-address by the hash of the *stored* bytes: the ref then truly
	// identifies what is served, so the ?v=<ref> cache-buster changes exactly
	// when the served content changes (and identical outputs dedupe).
	//
	// Branding keys deliberately keep this layout rather than moving into the
	// artwork/v1 upload namespace. They are already immutable and
	// content-addressed, they are served from bytes by this server rather than
	// resolved to a client-fetchable URL, and every installation with custom
	// branding already has a ref in this form recorded in settings.
	sum := sha256.Sum256(out)
	ref := hex.EncodeToString(sum[:])[:16] + ext
	key := spec.storePrefix + "/" + ref

	if err := s.store.WriteImmutable(ctx, key, out, artworkstore.ObjectMetadata{MediaType: serveType}); err != nil {
		return "", err
	}
	if err := s.settings.Set(ctx, spec.settingKey, ref); err != nil {
		return "", err
	}
	return ref, nil
}

// DeleteAsset clears the custom asset of the given kind. The stored object is
// left in place (orphaned objects are cheap and avoid concurrent-reader races);
// the empty settings value is what deactivates it.
//
// TODO(artwork-inventory): branding refs live in server_settings, which the
// artwork revision collector's reference union does not cover, so a replaced or
// deleted branding object is never reclaimed. Four objects per installation is
// a bounded leak; the accounting and safe-purge work is where this surface
// gains a real lifecycle.
func (s *Service) DeleteAsset(ctx context.Context, kind AssetKind) error {
	spec, ok := assetSpecs[kind]
	if !ok {
		return ErrInvalidKind
	}
	return s.settings.Set(ctx, spec.settingKey, "")
}

// GetAsset fetches the bytes of the current custom asset of the given kind.
// Returns ErrAssetNotConfigured when none is set or the stored object is
// missing or invalid, and ErrStorageUnavailable when no artwork store is
// available.
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
	object, err := s.store.Open(ctx, spec.storePrefix+"/"+ref)
	if err != nil {
		if errors.Is(err, artworkstore.ErrNotFound) || errors.Is(err, artworkstore.ErrInvalidKey) {
			return nil, "", "", ErrAssetNotConfigured
		}
		return nil, "", "", err
	}
	defer func() { _ = object.Close() }()
	if object.Info.SizeBytes > spec.maxBytes {
		return nil, "", "", fmt.Errorf(
			"%w: stored %s asset is %d bytes (limit %d)",
			ErrAssetNotConfigured, kind, object.Info.SizeBytes, spec.maxBytes,
		)
	}
	// Bound the read at the kind's own upload limit: a stored object larger
	// than that was not written by this service.
	data, err = io.ReadAll(io.LimitReader(object.Body, spec.maxBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if int64(len(data)) > spec.maxBytes {
		return nil, "", "", fmt.Errorf(
			"%w: stored %s asset exceeds the %d-byte limit",
			ErrAssetNotConfigured, kind, spec.maxBytes,
		)
	}
	// The served content type comes from the ref's extension rather than from
	// the store: it is what the upload path decided, and it must not vary with
	// whatever a backend chose to record alongside the bytes.
	return data, contentTypeForExt(path.Ext(ref)), ref, nil
}

// ReconcileMissingAssets clears the ref of every configured branding asset
// whose stored object no longer exists (e.g. after the artwork backend changed
// without migrating data), so the UI falls back to the built-in defaults
// instead of serving broken images. Returns how many configured assets were
// checked and how many of those were cleared.
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
		key := spec.storePrefix + "/" + ref
		_, statErr := s.store.Stat(ctx, key)
		switch {
		case statErr == nil:
		case errors.Is(statErr, artworkstore.ErrNotFound), errors.Is(statErr, artworkstore.ErrInvalidKey):
			if setErr := s.settings.Set(ctx, spec.settingKey, ""); setErr != nil {
				return checked, cleared, setErr
			}
			cleared++
			slog.Warn("branding: cleared asset whose stored object is missing", "kind", kind, "key", key)
		default:
			return checked, cleared, statErr
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
