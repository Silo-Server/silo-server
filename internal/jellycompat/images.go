package jellycompat

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

var compatImageProxyUserAgentSubstrings = []string{
	"infuse",
}

const compatImageRouteCacheControl = "private, no-store, no-cache, max-age=0, s-maxage=0, must-revalidate"
const compatImageProxyTagSuffix = "-p"
const compatImageProxyRoutePrefix = "__jellycompat_image_proxy__:"

type compatImageProxyRouteContextKey struct{}

var compatImageProxyHeaders = []string{
	"Accept-Ranges",
	"Content-Length",
	"Content-Type",
	"ETag",
	"Last-Modified",
}

func shouldProxyCompatImageRequest(r *http.Request) bool {
	return isCompatImageProxyClientRequest(r) ||
		isCompatImageProxyRouteRequest(r) ||
		isCompatImageProxyTag(r.URL.Query().Get("tag"))
}

func isCompatImageProxyClientRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	clientText := strings.ToLower(strings.Join([]string{
		r.UserAgent(),
		r.Header.Get("X-Emby-Authorization"),
	}, " "))
	for _, substring := range compatImageProxyUserAgentSubstrings {
		if strings.Contains(clientText, substring) {
			return true
		}
	}
	return false
}

func compatImageProxyTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" || isCompatImageProxyTag(tag) {
		return tag
	}
	return tag + compatImageProxyTagSuffix
}

func isCompatImageProxyTag(tag string) bool {
	return strings.HasSuffix(strings.TrimSpace(tag), compatImageProxyTagSuffix)
}

func canonicalCompatImageTag(tag string) string {
	tag = strings.TrimSpace(tag)
	return strings.TrimSuffix(tag, compatImageProxyTagSuffix)
}

func compatImageProxyRouteID(codec *ResourceIDCodec, routeID string) string {
	routeID = strings.TrimSpace(routeID)
	if codec == nil || routeID == "" {
		return routeID
	}
	if canonical, ok := canonicalCompatImageRouteID(codec, routeID); ok {
		return compatImageProxyRouteID(codec, canonical)
	}
	if decoded, ok := decodePackedCompatRouteID(routeID); ok {
		return encodeNumericCompatImageProxyRouteID(decoded)
	}
	return codec.EncodeStringID(EncodedIDItem, compatImageProxyRoutePrefix+routeID)
}

func canonicalCompatImageRouteID(codec *ResourceIDCodec, routeID string) (string, bool) {
	if canonical, ok := decodeNumericCompatImageProxyRouteID(routeID); ok {
		return canonical, true
	}
	if codec == nil {
		return routeID, false
	}
	value, err := codec.DecodeStringID(EncodedIDItem, strings.TrimSpace(routeID))
	if err != nil || !strings.HasPrefix(value, compatImageProxyRoutePrefix) {
		return routeID, false
	}
	canonical := strings.TrimSpace(strings.TrimPrefix(value, compatImageProxyRoutePrefix))
	if canonical == "" {
		return routeID, false
	}
	return canonical, true
}

func encodeNumericCompatImageProxyRouteID(decoded DecodedID) string {
	var raw [16]byte
	raw[0] = byte(EncodedIDImageProxy)
	raw[1] = byte(decoded.Type)
	binary.BigEndian.PutUint64(raw[8:], decoded.Value)
	return uuid.UUID(raw).String()
}

func decodeNumericCompatImageProxyRouteID(routeID string) (string, bool) {
	parsed, err := uuid.Parse(strings.TrimSpace(routeID))
	if err != nil || EncodedIDType(parsed[0]) != EncodedIDImageProxy {
		return "", false
	}
	kind := EncodedIDType(parsed[1])
	if !isPackedCompatRouteIDType(kind) || !zeroUUIDBytes(parsed[2:8]) {
		return "", false
	}
	return EncodeNumericID(kind, binary.BigEndian.Uint64(parsed[8:])).String(), true
}

func decodePackedCompatRouteID(routeID string) (DecodedID, bool) {
	parsed, err := uuid.Parse(strings.TrimSpace(routeID))
	if err != nil {
		return DecodedID{}, false
	}
	kind := EncodedIDType(parsed[0])
	if !isPackedCompatRouteIDType(kind) || !zeroUUIDBytes(parsed[1:8]) {
		return DecodedID{}, false
	}
	return DecodedID{Type: kind, Value: binary.BigEndian.Uint64(parsed[8:])}, true
}

func isPackedCompatRouteIDType(kind EncodedIDType) bool {
	switch kind {
	case EncodedIDLibrary,
		EncodedIDItem,
		EncodedIDMediaSource,
		EncodedIDSeason,
		EncodedIDPlaySession,
		EncodedIDGenre,
		EncodedIDStudio,
		EncodedIDPerson:
		return true
	default:
		return false
	}
}

func zeroUUIDBytes(values []byte) bool {
	for _, value := range values {
		if value != 0 {
			return false
		}
	}
	return true
}

func withCompatImageProxyRouteRequest(r *http.Request) *http.Request {
	if r == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), compatImageProxyRouteContextKey{}, true))
}

func isCompatImageProxyRouteRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	forceProxy, _ := r.Context().Value(compatImageProxyRouteContextKey{}).(bool)
	return forceProxy
}

func copyConditionalImageRequestHeaders(dst, src http.Header) {
	for _, key := range []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		if value := src.Values(key); len(value) > 0 {
			dst[key] = append([]string(nil), value...)
		}
	}
}

func proxyImage(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		copyImageProxyHeaders(w.Header(), resp.Header)
		setCompatImageRouteNoStore(w.Header())
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		setCompatImageRouteNoStore(w.Header())
		writeError(w, http.StatusBadGateway, "UpstreamError", "Failed to load image")
		return
	}

	copyImageProxyHeaders(w.Header(), resp.Header)
	setCompatImageRouteNoStore(w.Header())
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func copyImageProxyHeaders(dst, src http.Header) {
	for _, key := range compatImageProxyHeaders {
		if value := src.Values(key); len(value) > 0 {
			dst[key] = append([]string(nil), value...)
		}
	}
}

func setCompatImageRouteNoStore(header http.Header) {
	header.Set("Cache-Control", compatImageRouteCacheControl)
	header.Set("CDN-Cache-Control", "private, no-store, no-cache, max-age=0")
	header.Set("Expires", "Thu, 01 Jan 1970 00:00:00 GMT")
	header.Set("Pragma", "no-cache")
	header.Set("Surrogate-Control", "no-store")
	header.Set("X-Accel-Expires", "0")
	header.Add("Vary", "User-Agent")
}

// avatarPaletteSize bounds the number of distinct placeholder avatars. The
// route is anonymous, so per-request PNG generation would be a CPU DoS amplifier
// (a varying {id} flood defeats Cache-Control). Instead we precompute a small
// fixed palette once and select by hashing the id — zero per-request generation,
// bounded memory, while retaining per-id color variety.
const avatarPaletteSize = 16

// avatarPalette holds the precomputed placeholder PNGs, one per slot. Building
// it at package init means a panic here (image encode failure) is impossible to
// miss and never reaches a request.
var avatarPalette = buildAvatarPalette()

func buildAvatarPalette() [avatarPaletteSize][]byte {
	var palette [avatarPaletteSize][]byte
	for i := range palette {
		// Seed each slot with a distinct value so the colors are spread out;
		// reuse the same sha1-fill drawing as the original per-id generator.
		pngBytes, err := renderPlaceholderAvatarPNG(fmt.Sprintf("avatar-slot-%d", i))
		if err != nil {
			panic(fmt.Sprintf("jellycompat: build avatar palette slot %d: %v", i, err))
		}
		palette[i] = pngBytes
	}
	return palette
}

// avatarPaletteIndex maps an id to a stable palette slot via the first byte of
// its sha1 digest, so a given id always resolves to the same avatar.
func avatarPaletteIndex(id string) int {
	sum := sha1.Sum([]byte(id))
	return int(sum[0]) % avatarPaletteSize
}

func renderPlaceholderAvatarPNG(seed string) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 128, 128))
	sum := sha1.Sum([]byte(seed))
	fill := color.RGBA{R: sum[0], G: sum[1], B: sum[2], A: 255}
	for y := range 128 {
		for x := range 128 {
			img.Set(x, y, fill)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
