package jellycompat

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Generated poster geometry. Collection/BoxSet artwork is a 2:3 portrait poster
// in Jellyfin; clients down-scale to whatever fill size they request (the
// observed 360x360 probe among them), so one fixed render serves every size.
const (
	generatedPosterWidth  = 400
	generatedPosterHeight = 600
)

// generatedPosterFace is the parsed font face used for poster captions. Parsed
// once at init (gobold is embedded), so a parse failure here is impossible to
// miss and never reaches a request.
var generatedPosterFont = mustParseGeneratedPosterFont()

func mustParseGeneratedPosterFont() *opentype.Font {
	f, err := opentype.Parse(gobold.TTF)
	if err != nil {
		panic(fmt.Sprintf("jellycompat: parse poster font: %v", err))
	}
	return f
}

// generatedPosterCache memoizes rendered posters by caption text. The render
// routes are gated by a signed tag or an authenticated, visibility-checked
// session, so the keyspace is bounded by the real collection set rather than
// arbitrary caller input; a simple cap with reset guards against unbounded
// growth without the complexity of an LRU.
var (
	generatedPosterCacheMu sync.Mutex
	generatedPosterCache   = map[string][]byte{}
)

const generatedPosterCacheCap = 1024

// collectionsViewCaption is the caption rendered on the synthetic Collections
// library tile.
const collectionsViewCaption = "Collections"

// generatedPosterSeed builds the stable artwork-key surrogate used in image tag
// seeds for collections (and the Collections view) that fall back to a generated
// poster. Keeping it in one place ensures the DTO signer and the image handler
// derive the same tag.
func generatedPosterSeed(caption string) string {
	return "generated-poster:v1:" + strings.TrimSpace(caption)
}

// generatedCollectionPoster returns the PNG bytes for a gradient poster
// captioned with text, rendering and caching it on first use.
func generatedCollectionPoster(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "Collection"
	}

	generatedPosterCacheMu.Lock()
	if cached, ok := generatedPosterCache[text]; ok {
		generatedPosterCacheMu.Unlock()
		return cached, nil
	}
	generatedPosterCacheMu.Unlock()

	pngBytes, err := renderCollectionPosterPNG(text)
	if err != nil {
		return nil, err
	}

	generatedPosterCacheMu.Lock()
	if len(generatedPosterCache) >= generatedPosterCacheCap {
		generatedPosterCache = map[string][]byte{}
	}
	generatedPosterCache[text] = pngBytes
	generatedPosterCacheMu.Unlock()
	return pngBytes, nil
}

// renderCollectionPosterPNG draws a diagonal gradient (hue derived from the
// caption so each collection gets a stable, distinct backdrop) with the caption
// centered in white text and a black outline for legibility on any background.
func renderCollectionPosterPNG(text string) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, generatedPosterWidth, generatedPosterHeight))
	drawPosterGradient(img, text)

	if err := drawPosterCaption(img, text); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawPosterGradient(img *image.RGBA, seed string) {
	hue := float64(posterSeedHue(seed))
	top := hslToRGB(hue, 0.32, 0.28)
	bottom := hslToRGB(math.Mod(hue+28, 360), 0.30, 0.10)

	b := img.Bounds()
	w := float64(b.Dx())
	h := float64(b.Dy())
	denom := w + h
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			t := (float64(x) + float64(y)) / denom
			img.SetRGBA(x, y, color.RGBA{
				R: lerp(top.R, bottom.R, t),
				G: lerp(top.G, bottom.G, t),
				B: lerp(top.B, bottom.B, t),
				A: 255,
			})
		}
	}
}

func drawPosterCaption(img *image.RGBA, text string) error {
	const (
		fontSize  = 44.0
		outline   = 2
		lineSpace = 1.25
	)
	face, err := opentype.NewFace(generatedPosterFont, &opentype.FaceOptions{
		Size: fontSize,
		DPI:  72,
	})
	if err != nil {
		return err
	}
	defer face.Close()

	metrics := face.Metrics()
	lineHeight := int(math.Round(float64(metrics.Height.Round()) * lineSpace))
	margin := generatedPosterWidth / 10
	maxLineWidth := generatedPosterWidth - 2*margin

	lines := wrapPosterText(face, text, fixed.I(maxLineWidth))
	blockHeight := lineHeight * len(lines)
	baselineTop := (generatedPosterHeight-blockHeight)/2 + metrics.Ascent.Round()

	drawer := &font.Drawer{Dst: img, Face: face}
	for i, line := range lines {
		lineWidth := drawer.MeasureString(line)
		x := (fixed.I(generatedPosterWidth) - lineWidth) / 2
		y := fixed.I(baselineTop + i*lineHeight)

		// Outline: stamp the glyphs in black around the target before the
		// white fill so the caption stays legible over any gradient.
		drawer.Src = image.NewUniform(color.RGBA{A: 255})
		for dy := -outline; dy <= outline; dy++ {
			for dx := -outline; dx <= outline; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				drawer.Dot = fixed.Point26_6{X: x + fixed.I(dx), Y: y + fixed.I(dy)}
				drawer.DrawString(line)
			}
		}
		drawer.Src = image.NewUniform(color.RGBA{R: 255, G: 255, B: 255, A: 255})
		drawer.Dot = fixed.Point26_6{X: x, Y: y}
		drawer.DrawString(line)
	}
	return nil
}

// wrapPosterText greedily wraps text to fit maxWidth, splitting on spaces. A
// single word wider than the line is kept whole (the face down-scales visually
// only via client fill, so over-wide words are accepted rather than truncated).
func wrapPosterText(face font.Face, text string, maxWidth fixed.Int26_6) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}
	drawer := &font.Drawer{Face: face}
	lines := make([]string, 0, 4)
	current := words[0]
	for _, word := range words[1:] {
		candidate := current + " " + word
		if drawer.MeasureString(candidate) <= maxWidth {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}
	lines = append(lines, current)
	return lines
}

func posterSeedHue(seed string) int {
	sum := sha1.Sum([]byte(seed))
	return (int(sum[0])<<8 | int(sum[1])) % 360
}

func lerp(a, b uint8, t float64) uint8 {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return uint8(math.Round(float64(a) + (float64(b)-float64(a))*t))
}

// hslToRGB converts an HSL color (h in [0,360), s and l in [0,1]) to RGB.
func hslToRGB(h, s, l float64) color.RGBA {
	c := (1 - math.Abs(2*l-1)) * s
	hp := h / 60
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r, g, b float64
	switch {
	case hp < 1:
		r, g, b = c, x, 0
	case hp < 2:
		r, g, b = x, c, 0
	case hp < 3:
		r, g, b = 0, c, x
	case hp < 4:
		r, g, b = 0, x, c
	case hp < 5:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	m := l - c/2
	return color.RGBA{
		R: uint8(math.Round((r + m) * 255)),
		G: uint8(math.Round((g + m) * 255)),
		B: uint8(math.Round((b + m) * 255)),
		A: 255,
	}
}
