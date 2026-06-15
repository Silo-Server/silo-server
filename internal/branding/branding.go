// Package branding is the single source of truth for server white-labeling:
// server name, custom logos (wordmark + mark), favicon, login background, and
// the derived browser tab title, web app manifest, and theme color.
//
// It is intentionally a leaf package (it depends only on imageutil and the
// s3client value type via small interfaces) so that both internal/server (which
// templates index.html and serves the manifest/favicon) and
// internal/api/handlers (which exposes the public read + admin upload endpoints)
// can depend on it without an import cycle.
package branding

import "errors"

// AssetKind identifies an uploadable branding image.
type AssetKind string

const (
	// KindWordmark is the wide logo shown in the expanded sidebar.
	KindWordmark AssetKind = "wordmark"
	// KindMark is the square icon shown in the collapsed sidebar and PWA install.
	KindMark AssetKind = "mark"
	// KindFavicon is the browser tab icon. Served as-is (no WebP re-encode) so
	// Safari and mobile browsers keep working.
	KindFavicon AssetKind = "favicon"
	// KindLoginBg is the background image for the auth pages.
	KindLoginBg AssetKind = "login_bg"
)

// Scalar branding settings keys (stored in the server_settings table). Asset
// keys live on each assetSpec.
const (
	KeyServerName    = "branding.server_name"
	KeyLoginSubtitle = "branding.login_subtitle"
	KeyAccentColor   = "branding.accent_color"
	KeyDefaultTheme  = "branding.default_theme"
)

// Defaults applied when a branding setting is unset. ServerName/LoginSubtitle
// mirror the frontend's hardcoded fallbacks so behavior is unchanged out of the
// box.
const (
	DefaultServerName    = "Silo"
	DefaultLoginSubtitle = "Sign in with an existing account."
	// DefaultThemeColor is used for the PWA manifest theme/background color when
	// no accent color is configured.
	DefaultThemeColor = "#0b0b0f"
)

// assetURLBase is the public, stable path prefix for serving branding assets.
// Assets are addressed by content ref (?v=<hash><ext>) for immutable caching.
const assetURLBase = "/api/v1/branding/assets/"

// AssetContentSecurityPolicy hardens every served branding asset response.
//
// SECURITY: the favicon accepts SVG, a valid favicon format that can embed
// <script> and on* handlers. Served on the app origin and navigated to
// directly, such an SVG would otherwise execute in the viewer's session
// (stored XSS) — X-Content-Type-Options alone does not stop a correctly-typed
// SVG document from running scripts. `sandbox` (no allow-tokens) forces an
// opaque origin with scripting disabled, and `default-src 'none'` blocks all
// fetches; the asset still renders fine as an <img>/<link rel="icon">
// subresource (where this policy does not apply). Harmless for raster images.
const AssetContentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

// Errors returned by Service. Handlers map these to HTTP status codes.
var (
	// ErrStorageUnavailable indicates S3 is not configured; branding image
	// upload/serving is unavailable but text branding still works.
	ErrStorageUnavailable = errors.New("branding: asset storage is not configured")
	// ErrAssetNotConfigured indicates no custom asset of the requested kind is set.
	ErrAssetNotConfigured = errors.New("branding: asset not configured")
	// ErrInvalidKind indicates an unknown asset kind.
	ErrInvalidKind = errors.New("branding: invalid asset kind")
	// ErrUnsupportedImage indicates the uploaded file is not an accepted image type.
	ErrUnsupportedImage = errors.New("branding: unsupported image type")
)

// IsValidKind reports whether s names a known asset kind.
func IsValidKind(s string) bool {
	_, ok := assetSpecs[AssetKind(s)]
	return ok
}
