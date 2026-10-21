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

func TestRenderIndexHTMLDoesNotStampADefaultTheme(t *testing.T) {
	in := []byte(`<!doctype html><html lang="en" data-theme="midnight-cinema"><head></head></html>`)
	out := string(RenderIndexHTML(in, newSnapshot("X")))
	if strings.Contains(out, "data-default-theme") {
		t.Fatalf("the shell has one theme and carries no default-theme attribute: %q", out)
	}
	if !strings.Contains(out, `<html lang="en" data-theme="midnight-cinema">`) {
		t.Fatalf("static data-theme should be left alone: %q", out)
	}
}

func TestRenderKeyIncludesWordmarkLight(t *testing.T) {
	snap := newSnapshot("Acme")
	baseline := snap.RenderKey()
	snap.assets[KindWordmarkLight] = "wordmark-light.webp"
	if got := snap.RenderKey(); got == baseline {
		t.Fatal("RenderKey did not change when wordmark_light ref changed")
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
	if !strings.Contains(out, "/api/v2/branding/assets/favicon?v=f00.png") {
		t.Fatalf("favicon not rewritten in real shell")
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
	if !strings.Contains(first["src"].(string), "/api/v2/branding/assets/mark?v=m1.webp") {
		t.Fatalf("expected custom mark icon URL, got %v", first["src"])
	}
}
