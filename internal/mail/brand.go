package mail

import (
	"bytes"
	"context"
	_ "embed"
	"image"
	_ "image/png"
	"log/slog"
	"math"
	"strings"
	"sync"

	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// Email branding follows the web sidebar (web/src/components/SiloBrand.tsx):
// the header shows the server's uploaded wordmark, else Silo's own, with the
// server name as alt text. The logo travels inside the message as an inline PNG rather than a
// link: many clients block remote images, the recipient may not be able to
// reach the server, and uploaded logos are stored as WebP, which Outlook does
// not render.

// defaultWordmarkPNG is a copy of web/public/silo-wordmark-sidebar.png; keep
// the two in step when the Silo wordmark changes.
//
//go:embed assets/silo-wordmark.png
var defaultWordmarkPNG []byte

const (
	logoContentID = "silo-logo"
	// The header logo fits within this box, in CSS pixels. Custom logos are
	// encoded at up to twice the size so they stay sharp on dense screens.
	logoMaxWidth  = 240
	logoMaxHeight = 48
)

var defaultLogo = mustDefaultLogo()

// Brand is the branding one email renders with. The zero value renders as
// Silo's default branding.
type Brand struct {
	// Name is the server name, shown as the logo's alt text.
	Name string
	logo brandLogo
}

type brandLogo struct {
	png []byte
	// width and height are the display size in CSS pixels.
	width, height int
}

// DefaultBrand is Silo's own branding.
func DefaultBrand() Brand {
	return Brand{Name: branding.DefaultServerName, logo: defaultLogo}
}

// InlineImages returns the images an email rendered with this brand
// references; set them as Message.Inline.
func (b Brand) InlineImages() []InlineImage {
	b = b.withDefaults()
	return []InlineImage{{
		ContentID:   logoContentID,
		Filename:    "logo.png",
		ContentType: "image/png",
		Data:        b.logo.png,
	}}
}

func (b Brand) withDefaults() Brand {
	if strings.TrimSpace(b.Name) == "" {
		b.Name = branding.DefaultServerName
	}
	if len(b.logo.png) == 0 {
		b.logo = defaultLogo
	}
	return b
}

// BrandSource supplies the server's branding. *branding.Service satisfies it.
type BrandSource interface {
	Load(ctx context.Context) branding.Snapshot
	GetAsset(ctx context.Context, kind branding.AssetKind) (data []byte, contentType, ref string, err error)
}

// BrandLoader resolves the Brand for outgoing email. It is safe for
// concurrent use; share one per process so the converted logo is cached once.
type BrandLoader struct {
	source BrandSource

	mu      sync.Mutex
	logoRef string
	logo    brandLogo
}

// NewBrandLoader creates a loader over source. A nil source, or a nil
// loader, yields DefaultBrand.
func NewBrandLoader(source BrandSource) *BrandLoader {
	return &BrandLoader{source: source}
}

// Load reads the current branding. It never fails: an unreadable or
// unconvertible custom logo is logged and replaced by the Silo wordmark, so
// branding problems cannot stop an email from going out.
func (l *BrandLoader) Load(ctx context.Context) Brand {
	if l == nil || l.source == nil {
		return DefaultBrand()
	}
	snap := l.source.Load(ctx)
	brand := Brand{Name: snap.ServerName}
	if ref := snap.AssetRef(branding.KindWordmark); ref != "" {
		brand.logo = l.customLogo(ctx, ref)
	}
	return brand.withDefaults()
}

// customLogo returns the email-ready PNG of the wordmark with the given ref.
// Refs are content hashes, so a cached conversion never goes stale.
func (l *BrandLoader) customLogo(ctx context.Context, ref string) brandLogo {
	l.mu.Lock()
	if l.logoRef == ref {
		logo := l.logo
		l.mu.Unlock()
		return logo
	}
	l.mu.Unlock()

	data, _, storedRef, err := l.source.GetAsset(ctx, branding.KindWordmark)
	if err != nil {
		slog.WarnContext(ctx, "email: custom wordmark unavailable; using the Silo wordmark", "ref", ref, "error", err)
		return brandLogo{}
	}
	png, width, height, err := imageutil.EncodePNGWithin(data, 2*logoMaxWidth, 2*logoMaxHeight)
	if err != nil {
		slog.WarnContext(ctx, "email: custom wordmark could not be converted; using the Silo wordmark", "ref", storedRef, "error", err)
		return brandLogo{}
	}
	logo := brandLogo{png: png}
	logo.width, logo.height = logoDisplaySize(width, height)

	l.mu.Lock()
	l.logoRef, l.logo = storedRef, logo
	l.mu.Unlock()
	return logo
}

func mustDefaultLogo() brandLogo {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(defaultWordmarkPNG))
	if err != nil {
		panic("mail: embedded Silo wordmark is not a valid PNG: " + err.Error())
	}
	logo := brandLogo{png: defaultWordmarkPNG}
	logo.width, logo.height = logoDisplaySize(cfg.Width, cfg.Height)
	return logo
}

// logoDisplaySize scales an image down to fit the header logo box, keeping
// its aspect ratio. Small images are shown at their own size.
func logoDisplaySize(width, height int) (int, int) {
	if width <= 0 || height <= 0 {
		return logoMaxWidth, logoMaxHeight
	}
	scale := math.Min(1, math.Min(
		float64(logoMaxWidth)/float64(width),
		float64(logoMaxHeight)/float64(height)))
	return max(1, int(math.Round(float64(width)*scale))),
		max(1, int(math.Round(float64(height)*scale)))
}
