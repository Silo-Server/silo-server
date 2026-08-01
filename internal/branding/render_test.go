package branding

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

type publicAssetURLSettings struct {
	values map[string]string
}

func (s publicAssetURLSettings) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s publicAssetURLSettings) Set(context.Context, string, string) error { return nil }

type publicAssetURLStore struct {
	url string
	err error
}

type byteOnlyAssetStore struct{}

func (*byteOnlyAssetStore) PutObject(context.Context, string, string, []byte) error { return nil }
func (*byteOnlyAssetStore) GetObject(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (*byteOnlyAssetStore) Bucket() string { return "public-assets" }

var _ AssetStore = (*byteOnlyAssetStore)(nil)

func (*publicAssetURLStore) PutObject(context.Context, string, string, []byte) error { return nil }
func (*publicAssetURLStore) GetObject(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (*publicAssetURLStore) Bucket() string { return "public-assets" }
func (s *publicAssetURLStore) PresignGetURL(context.Context, string, string, time.Duration) (string, error) {
	return s.url, s.err
}

var _ PublicAssetURLProvider = (*publicAssetURLStore)(nil)

func TestPublicAssetURLPrefersMarkThenWordmark(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		wantURL string
		wantTag string
	}{
		{
			name: "mark",
			values: map[string]string{
				"branding.mark_ref":     "mark.webp",
				"branding.wordmark_ref": "wordmark.webp",
			},
			wantURL: "https://garage.example.test/branding/mark/mark.webp",
			wantTag: "mark.webp",
		},
		{
			name: "wordmark fallback",
			values: map[string]string{
				"branding.wordmark_ref": "wordmark.webp",
			},
			wantURL: "https://garage.example.test/branding/wordmark/wordmark.webp",
			wantTag: "wordmark.webp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(publicAssetURLSettings{values: tt.values}, &publicAssetURLStore{url: tt.wantURL})
			gotURL, gotTag := svc.PublicAssetURL(context.Background())
			if gotURL != tt.wantURL || gotTag != tt.wantTag {
				t.Fatalf("PublicAssetURL() = (%q, %q), want (%q, %q)", gotURL, gotTag, tt.wantURL, tt.wantTag)
			}
		})
	}
}

func TestPublicAssetURLRejectsUnstableOrUnsafeURLs(t *testing.T) {
	overlong := "https://garage.example.test/" + strings.Repeat("a", 4096)
	tests := []struct {
		name string
		url  string
		err  error
	}{
		{name: "http", url: "http://garage.example.test/branding/mark/mark.webp"},
		{name: "relative", url: "/branding/mark/mark.webp"},
		{name: "userinfo", url: "https://user:pass@garage.example.test/branding/mark/mark.webp"},
		{name: "fragment", url: "https://garage.example.test/branding/mark/mark.webp#asset"},
		{name: "generic query", url: "https://garage.example.test/branding/mark/mark.webp?download=1"},
		{name: "aws presigned", url: "https://garage.example.test/branding/mark/mark.webp?X-Amz-Expires=900&X-Amz-Signature=secret"},
		{name: "finite token", url: "https://garage.example.test/branding/mark/mark.webp?token=1234-signature"},
		{name: "overlong", url: overlong},
		{name: "storage error", err: errors.New("storage unavailable")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(
				publicAssetURLSettings{values: map[string]string{"branding.mark_ref": "mark.webp"}},
				&publicAssetURLStore{url: tt.url, err: tt.err},
			)
			gotURL, gotTag := svc.PublicAssetURL(context.Background())
			if gotURL != "" || gotTag != "" {
				t.Fatalf("PublicAssetURL() = (%q, %q), want empty metadata", gotURL, gotTag)
			}
		})
	}
}

func TestPublicAssetURLReturnsEmptyWithoutConfiguredBrandingOrStorage(t *testing.T) {
	tests := []struct {
		name     string
		settings publicAssetURLSettings
		store    AssetStore
	}{
		{name: "no branding", settings: publicAssetURLSettings{values: map[string]string{}}, store: &publicAssetURLStore{}},
		{name: "no storage", settings: publicAssetURLSettings{values: map[string]string{"branding.mark_ref": "mark.webp"}}},
		{
			name:     "byte storage without URL capability",
			settings: publicAssetURLSettings{values: map[string]string{"branding.mark_ref": "mark.webp"}},
			store:    &byteOnlyAssetStore{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotTag := NewService(tt.settings, tt.store).PublicAssetURL(context.Background())
			if gotURL != "" || gotTag != "" {
				t.Fatalf("PublicAssetURL() = (%q, %q), want empty metadata", gotURL, gotTag)
			}
		})
	}
}

func TestPublicAssetURLUsesOnlyConfiguredPublicS3Origin(t *testing.T) {
	settings := publicAssetURLSettings{values: map[string]string{"branding.mark_ref": "mark.webp"}}
	newStore := func(urlAuth string) *s3client.Client {
		return s3client.NewClient(s3client.BucketConfig{
			Endpoint:       "https://garage-api.example.test",
			PublicEndpoint: "https://assets.example.test/tenant",
			Region:         "us-east-1",
			Bucket:         "public-assets",
			KeyPrefix:      "silo/site-1",
			AccessKey:      "test",
			SecretKey:      "test",
			PathStyle:      true,
			URLAuth:        urlAuth,
		})
	}

	t.Run("public", func(t *testing.T) {
		gotURL, gotTag := NewService(settings, newStore(s3client.URLAuthPublic)).PublicAssetURL(context.Background())
		wantURL := "https://assets.example.test/tenant/silo/site-1/branding/mark/mark.webp"
		if gotURL != wantURL || gotTag != "mark.webp" {
			t.Fatalf("PublicAssetURL() = (%q, %q), want (%q, %q)", gotURL, gotTag, wantURL, "mark.webp")
		}
	})

	t.Run("presigned", func(t *testing.T) {
		gotURL, gotTag := NewService(settings, newStore(s3client.URLAuthPresigned)).PublicAssetURL(context.Background())
		if gotURL != "" || gotTag != "" {
			t.Fatalf("PublicAssetURL() = (%q, %q), want empty metadata", gotURL, gotTag)
		}
	})
}

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
