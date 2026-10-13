package jellycompat

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"
)

const imageTagSignatureDomain = "silo:jellycompat:image-tag:v1"

type imageTagSigner struct {
	secret []byte
}

func newImageTagSigner(secret string) *imageTagSigner {
	if strings.TrimSpace(secret) == "" {
		return nil
	}
	return &imageTagSigner{secret: []byte(secret)}
}

func (s *imageTagSigner) Tag(seed, fallbackURL string) string {
	if strings.TrimSpace(seed) == "" {
		return tagValue(fallbackURL)
	}
	if s == nil {
		return tagValue(seed)
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(imageTagSignatureDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(seed))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

func (s *imageTagSigner) Equal(seed, fallbackURL, actual string) bool {
	if s == nil {
		return false
	}
	actual = canonicalCompatImageTag(actual)
	expected := s.Tag(seed, fallbackURL)
	if expected == "" || actual == "" || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

// A collection collage's image tag is the collage key (16 hex digits) followed
// by a 16-hex-digit signature over that key and the BoxSet route: 32 hex
// digits, the shape of a native Jellyfin image tag. Every other compat tag is
// 16 hex digits, so the two never collide. The key in the tag lets a request
// that carries no session find the collage its tag was minted for.
const collageTagKeyLength = 16

func collageImageTagSeed(routeID, key string) string {
	return imageTagSeed(routeID, "Primary", compatCardImageSize, "collage:"+key, "", time.Time{})
}

func collageImageTag(signer *imageTagSigner, routeID, key string) string {
	return key + signer.Tag(collageImageTagSeed(routeID, key), "")
}

// verifiedCollageImageTag returns the collage key a signed collage tag names.
func verifiedCollageImageTag(signer *imageTagSigner, routeID, tag string) (string, bool) {
	tag = canonicalCompatImageTag(tag)
	if signer == nil || len(tag) != 2*collageTagKeyLength {
		return "", false
	}
	key := tag[:collageTagKeyLength]
	if !signer.Equal(collageImageTagSeed(routeID, key), "", tag[collageTagKeyLength:]) {
		return "", false
	}
	return key, true
}
