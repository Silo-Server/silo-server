package playback

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
)

const (
	EmbeddedSubtitleStreamIndexParamV3 = "embedded_stream_index"
	ExternalSubtitleKeyParamV3         = "external_subtitle_key"
)

// ExternalSubtitlePathKeyV3 identifies a catalog sidecar without exposing its
// filesystem path in a URL. Authorization still comes from the playback
// session and source file; this key only prevents inventory-order drift.
func ExternalSubtitlePathKeyV3(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

func subtitleURLIdentityV3(rawURL, key string, fileID int) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Query().Get("file_id") != strconv.Itoa(fileID) {
		return ""
	}
	return parsed.Query().Get(key)
}
