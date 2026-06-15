package branding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSnapshot(name string) Snapshot {
	return Snapshot{ServerName: name, assets: map[AssetKind]string{}}
}

func TestRenderIndexHTMLReplacesTitle(t *testing.T) {
	in := []byte(`<html><head><title>Silo</title></head><body></body></html>`)
	out := string(RenderIndexHTML(in, newSnapshot("Acme Media")))
	if !strings.Contains(out, "<title>Acme Media</title>") {
		t.Fatalf("title not replaced: %q", out)
	}
	if strings.Contains(out, "<title>Silo</title>") {
		t.Fatalf("default title still present: %q", out)
	}
}

func TestRenderIndexHTMLEscapesTitle(t *testing.T) {
	in := []byte(`<title>Silo</title></head>`)
	out := string(RenderIndexHTML(in, newSnapshot(`A&B<script>`)))
	if strings.Contains(out, "<script>") {
		t.Fatalf("title not escaped: %q", out)
	}
	if !strings.Contains(out, "A&amp;B&lt;script&gt;") {
		t.Fatalf("expected escaped title, got: %q", out)
	}
}

func TestRenderIndexHTMLRewritesFaviconWhenSet(t *testing.T) {
	in := []byte(indexFaviconLink + "</head>")
	snap := Snapshot{ServerName: "X", assets: map[AssetKind]string{KindFavicon: "abc123.png"}}
	out := string(RenderIndexHTML(in, snap))
	if !strings.Contains(out, `href="/api/v1/branding/assets/favicon?v=abc123.png"`) {
		t.Fatalf("favicon not rewritten: %q", out)
	}
}

func TestRenderIndexHTMLKeepsDefaultFaviconWhenUnset(t *testing.T) {
	in := []byte(indexFaviconLink + "</head>")
	out := string(RenderIndexHTML(in, newSnapshot("X")))
	if !strings.Contains(out, indexFaviconLink) {
		t.Fatalf("default favicon link should be preserved: %q", out)
	}
}

func TestRenderIndexHTMLInjectsThemeColorOnlyWhenAccentSet(t *testing.T) {
	in := []byte("<head></head>")
	if out := string(RenderIndexHTML(in, newSnapshot("X"))); strings.Contains(out, "theme-color") {
		t.Fatalf("theme-color should not be injected without accent: %q", out)
	}
	snap := newSnapshot("X")
	snap.AccentColor = "#5bc39d"
	out := string(RenderIndexHTML(in, snap))
	if !strings.Contains(out, `<meta name="theme-color" content="#5bc39d" />`) {
		t.Fatalf("theme-color meta not injected: %q", out)
	}
}

// TestRenderIndexHTMLAgainstRealShell guards against web/index.html drifting
// away from the literals RenderIndexHTML depends on.
func TestRenderIndexHTMLAgainstRealShell(t *testing.T) {
	path := filepath.Join("..", "..", "web", "index.html")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("index.html not available: %v", err)
	}
	if !strings.Contains(string(data), "<title>Silo</title>") {
		t.Fatalf("web/index.html no longer contains the expected <title>Silo</title>; update RenderIndexHTML")
	}
	if !strings.Contains(string(data), indexFaviconLink) {
		t.Fatalf("web/index.html no longer contains the expected favicon link %q; update indexFaviconLink", indexFaviconLink)
	}
	snap := Snapshot{ServerName: "Acme", assets: map[AssetKind]string{KindFavicon: "f00.png"}}
	out := string(RenderIndexHTML(data, snap))
	if !strings.Contains(out, "<title>Acme</title>") {
		t.Fatalf("title not replaced in real shell")
	}
	if !strings.Contains(out, "/api/v1/branding/assets/favicon?v=f00.png") {
		t.Fatalf("favicon not rewritten in real shell")
	}
}

func TestRenderManifestDefaults(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(RenderManifest(newSnapshot("Acme TV")), &m); err != nil {
		t.Fatalf("invalid manifest JSON: %v", err)
	}
	if m["name"] != "Acme TV" || m["short_name"] != "Acme TV" {
		t.Fatalf("unexpected name: %v / %v", m["name"], m["short_name"])
	}
	if m["theme_color"] != DefaultThemeColor {
		t.Fatalf("expected default theme color, got %v", m["theme_color"])
	}
	icons, _ := m["icons"].([]any)
	if len(icons) != 3 {
		t.Fatalf("expected 3 default icons, got %d", len(icons))
	}
	first, _ := icons[0].(map[string]any)
	if !strings.HasPrefix(first["src"].(string), "/web-app-icon") {
		t.Fatalf("expected bundled icon default, got %v", first["src"])
	}
}

func TestRenderManifestUsesCustomMark(t *testing.T) {
	snap := Snapshot{ServerName: "Acme", AccentColor: "#112233", assets: map[AssetKind]string{KindMark: "m1.webp"}}
	var m map[string]any
	if err := json.Unmarshal(RenderManifest(snap), &m); err != nil {
		t.Fatalf("invalid manifest JSON: %v", err)
	}
	if m["theme_color"] != "#112233" {
		t.Fatalf("expected accent theme color, got %v", m["theme_color"])
	}
	icons, _ := m["icons"].([]any)
	first, _ := icons[0].(map[string]any)
	if !strings.Contains(first["src"].(string), "/api/v1/branding/assets/mark?v=m1.webp") {
		t.Fatalf("expected custom mark icon URL, got %v", first["src"])
	}
}
