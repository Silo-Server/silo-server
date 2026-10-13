package mail

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/branding"
)

type memorySettings map[string]string

func (m memorySettings) Get(_ context.Context, key string) (string, error) { return m[key], nil }
func (m memorySettings) Set(_ context.Context, key, value string) error {
	m[key] = value
	return nil
}

// countingSource counts asset reads so tests can see the logo cache work.
type countingSource struct {
	*branding.Service
	reads int
	fail  error
}

func (c *countingSource) GetAsset(ctx context.Context, kind branding.AssetKind) ([]byte, string, string, error) {
	c.reads++
	if c.fail != nil {
		return nil, "", "", c.fail
	}
	return c.Service.GetAsset(ctx, kind)
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func brandedService(t *testing.T, settings memorySettings) *branding.Service {
	t.Helper()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := branding.NewService(settings, store)
	if _, err := svc.UploadAsset(context.Background(), branding.KindWordmark, testPNG(t, 640, 128), "image/png"); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestBrandLoaderDefaults(t *testing.T) {
	var nilLoader *BrandLoader
	for _, loader := range []*BrandLoader{nilLoader, NewBrandLoader(nil)} {
		brand := loader.Load(context.Background())
		if brand.Name != "Silo" || brand.AccentColor != "" || !bytes.Equal(brand.logo.png, defaultWordmarkPNG) {
			t.Fatalf("want default brand, got name=%q accent=%q", brand.Name, brand.AccentColor)
		}
	}
}

func TestBrandLoaderUsesCustomBranding(t *testing.T) {
	settings := memorySettings{branding.KeyServerName: "Harbor", branding.KeyAccentColor: "#F5A524"}
	source := &countingSource{Service: brandedService(t, settings)}
	loader := NewBrandLoader(source)
	ctx := context.Background()

	brand := loader.Load(ctx)
	if brand.Name != "Harbor" || brand.AccentColor != "#f5a524" {
		t.Fatalf("name=%q accent=%q", brand.Name, brand.AccentColor)
	}
	if brand.logo.width != 240 || brand.logo.height != 48 {
		t.Fatalf("display size = %dx%d, want 240x48", brand.logo.width, brand.logo.height)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(brand.logo.png))
	if err != nil || format != "png" || cfg.Width != 480 || cfg.Height != 96 {
		t.Fatalf("logo is not a 480x96 PNG: format=%q %dx%d err=%v", format, cfg.Width, cfg.Height, err)
	}
	images := brand.InlineImages()
	if len(images) != 1 || images[0].ContentID != logoContentID || images[0].ContentType != "image/png" {
		t.Fatalf("inline images = %+v", images)
	}

	loader.Load(ctx)
	if source.reads != 1 {
		t.Fatalf("asset read %d times, want 1 (cached by ref)", source.reads)
	}
}

func TestBrandLoaderFallsBackWhenLogoUnavailable(t *testing.T) {
	settings := memorySettings{branding.KeyServerName: "Harbor"}
	source := &countingSource{Service: brandedService(t, settings), fail: errors.New("bucket down")}
	brand := NewBrandLoader(source).Load(context.Background())
	if brand.Name != "Harbor" || !bytes.Equal(brand.logo.png, defaultWordmarkPNG) {
		t.Fatalf("want the Silo wordmark with the server name, got name=%q", brand.Name)
	}

	// A transient failure is not cached: the next email retries.
	source.fail = nil
	brand = NewBrandLoader(source).Load(context.Background())
	if bytes.Equal(brand.logo.png, defaultWordmarkPNG) {
		t.Fatal("custom logo not loaded after the store recovered")
	}
}

func TestNormalizeHexColor(t *testing.T) {
	for in, want := range map[string]string{
		"#F5A524":                "#f5a524",
		" #abc ":                 "#aabbcc",
		"f5a524":                 "",
		"#f5a52":                 "",
		"#ggg":                   "",
		"red":                    "",
		`#fff;background:url(x)`: "",
	} {
		if got := normalizeHexColor(in); got != want {
			t.Errorf("normalizeHexColor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildMessageEmbedsInlineImages(t *testing.T) {
	cfg := &smtpConfig{fromAddress: "silo@example.com", fromName: "Silo"}
	brand := DefaultBrand()
	message, err := buildMessage(cfg, Message{
		To:       []string{"user@example.com"},
		Subject:  "Hello",
		TextBody: "plain",
		HTMLBody: RenderLayout(LayoutOptions{Brand: brand, BodyHTML: "rich"}),
		Inline:   brand.InlineImages(),
	})
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	var rendered strings.Builder
	if _, err := message.WriteTo(&rendered); err != nil {
		t.Fatalf("render message: %v", err)
	}
	output := rendered.String()
	for _, want := range []string{
		"multipart/related", "multipart/alternative",
		"Content-Id: <silo-logo>", "Content-Disposition: inline", "Content-Type: image/png",
		`src=3D"cid:silo-logo"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("rendered message missing %q:\n%s", want, output[:min(len(output), 4000)])
		}
	}
}
