package heroclips

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// keyRoot is the prefix blobgc owns for hero clips. Everything under it is
// derived and regenerable.
const keyRoot = "hero-clips/"

// contentIDHashLength is the hex length of the directory a clip sits in. Half a
// SHA-256 is far more than enough to keep content ids apart, and keeps a key
// short.
const contentIDHashLength = 32

// ClipKey names the object that holds one item's clip:
//
//	hero-clips/<content id hash>/<recipe>.mp4
//
// The item directory is a hash because a content id carries a colon
// ("movie:tmdb-447332"), which local storage refuses in a filename and a URL
// has to escape. The recipe is in the file name, so a recipe change writes a
// new object beside the old one, which blobgc then collects.
func ClipKey(contentID string, recipe Recipe) string {
	return ItemPrefix(contentID) + recipe.String() + ".mp4"
}

// ItemPrefix is the directory every clip of one item lives under, and the unit
// blobgc groups an item's clips by.
func ItemPrefix(contentID string) string {
	return keyRoot + contentIDDigest(contentID) + "/"
}

// IsClipKey reports whether a key is one of ours, which is how the orphan sweep
// decides whether a key is its business at all.
func IsClipKey(key string) bool {
	_, ok := clipKeyItemPrefix(key)
	return ok
}

// clipKeyItemPrefix returns the item directory of a clip key.
func clipKeyItemPrefix(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, keyRoot)
	if !ok {
		return "", false
	}
	dir, name, ok := strings.Cut(rest, "/")
	if !ok || len(dir) != contentIDHashLength || name == "" {
		return "", false
	}
	if !isHex(dir) || strings.ContainsRune(name, '/') {
		return "", false
	}
	return keyRoot + dir + "/", true
}

func contentIDDigest(contentID string) string {
	sum := sha256.Sum256([]byte(contentID))
	return hex.EncodeToString(sum[:contentIDHashLength/2])
}

func isHex(value string) bool {
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(value) > 0
}
