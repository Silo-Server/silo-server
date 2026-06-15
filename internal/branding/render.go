package branding

import (
	"encoding/json"
	"html"
	"strings"
)

// Snapshot is an immutable view of the current branding configuration. It is
// the input to HTML templating, manifest generation, and the public API
// response.
type Snapshot struct {
	ServerName    string
	LoginSubtitle string
	AccentColor   string // "" when unset
	DefaultTheme  string // "" when unset

	assets map[AssetKind]string // kind -> content ref ("<hash><ext>")
}

// AssetRef returns the stored content ref for a kind, or "" when no custom
// asset is set.
func (s Snapshot) AssetRef(kind AssetKind) string { return s.assets[kind] }

// HasAsset reports whether a custom asset of the given kind is configured.
func (s Snapshot) HasAsset(kind AssetKind) bool { return s.assets[kind] != "" }

// AssetURL returns the stable public URL for a custom asset, or "" when unset.
// The content ref is carried as ?v= so the asset can be cached immutably.
func (s Snapshot) AssetURL(kind AssetKind) string {
	return AssetURLFor(kind, s.assets[kind])
}

// AssetURLFor builds the stable public URL for a kind given its content ref.
// Returns "" when ref is empty.
func AssetURLFor(kind AssetKind, ref string) string {
	if ref == "" {
		return ""
	}
	return assetURLBase + string(kind) + "?v=" + ref
}

// ThemeColor returns the color used for the PWA manifest and the theme-color
// meta tag: the configured accent color, or the default when unset.
func (s Snapshot) ThemeColor() string {
	if s.AccentColor != "" {
		return s.AccentColor
	}
	return DefaultThemeColor
}

// Favicon link literal as it appears in web/index.html. Kept in sync with that
// file; RenderIndexHTML rewrites it to a custom favicon when one is set.
const indexFaviconLink = `<link rel="icon" href="/favicon.ico" sizes="any" />`

// RenderIndexHTML injects branding into the SPA shell: the browser tab title
// and, when configured, the custom favicon link and a theme-color meta tag.
// Favicon/manifest paths themselves are served dynamically by the frontend
// handler, so only the title and the cache-bustable favicon href are rewritten
// here. Replacements that don't match are no-ops, so a build that changes the
// shell degrades gracefully to the bundled defaults.
func RenderIndexHTML(index []byte, snap Snapshot) []byte {
	out := string(index)

	out = strings.Replace(out,
		"<title>Silo</title>",
		"<title>"+html.EscapeString(snap.ServerName)+"</title>",
		1,
	)

	if u := snap.AssetURL(KindFavicon); u != "" {
		out = strings.Replace(out,
			indexFaviconLink,
			`<link rel="icon" href="`+html.EscapeString(u)+`" sizes="any" />`,
			1,
		)
	}

	if snap.AccentColor != "" {
		meta := `<meta name="theme-color" content="` + html.EscapeString(snap.AccentColor) + `" />`
		out = strings.Replace(out, "</head>", "  "+meta+"\n  </head>", 1)
	}

	return []byte(out)
}

type manifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose,omitempty"`
}

type webManifest struct {
	Name            string         `json:"name"`
	ShortName       string         `json:"short_name"`
	Icons           []manifestIcon `json:"icons"`
	ThemeColor      string         `json:"theme_color"`
	BackgroundColor string         `json:"background_color"`
	Display         string         `json:"display"`
	StartURL        string         `json:"start_url"`
}

// RenderManifest builds the dynamic web app manifest from the snapshot. When a
// custom mark is set its WebP is advertised at both common sizes; otherwise the
// bundled PNG icon set is used.
func RenderManifest(snap Snapshot) []byte {
	var icons []manifestIcon
	if u := snap.AssetURL(KindMark); u != "" {
		icons = []manifestIcon{
			{Src: u, Sizes: "192x192", Type: "image/webp"},
			{Src: u, Sizes: "512x512", Type: "image/webp"},
			{Src: u, Sizes: "512x512", Type: "image/webp", Purpose: "maskable"},
		}
	} else {
		icons = []manifestIcon{
			{Src: "/web-app-icon-192.png", Sizes: "192x192", Type: "image/png"},
			{Src: "/web-app-icon-512.png", Sizes: "512x512", Type: "image/png"},
			{Src: "/maskable-icon-512.png", Sizes: "512x512", Type: "image/png", Purpose: "maskable"},
		}
	}

	m := webManifest{
		Name:            snap.ServerName,
		ShortName:       snap.ServerName,
		Icons:           icons,
		ThemeColor:      snap.ThemeColor(),
		BackgroundColor: snap.ThemeColor(),
		Display:         "standalone",
		StartURL:        "/",
	}
	// Marshaling a fixed struct cannot fail.
	b, _ := json.Marshal(m)
	return b
}
