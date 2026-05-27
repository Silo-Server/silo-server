package jellycompat

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

const imageTagSignatureDomain = "silo:jellycompat:image-tag:v1"

type imageTagSigner struct {
	secret []byte
}

func newImageTagSigner(secret string) *imageTagSigner {
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
	actual = strings.TrimSpace(actual)
	expected := s.Tag(seed, fallbackURL)
	if expected == "" || actual == "" || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}
