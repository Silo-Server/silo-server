package userstore

import "strings"

// AvatarUploadPrefix marks a Profile.Avatar that names an uploaded image: the
// rest of the reference is the original's blob key in the operational store.
const AvatarUploadPrefix = "upload:"

// IsUploadedAvatarRef reports whether a Profile.Avatar names an uploaded image.
func IsUploadedAvatarRef(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), AvatarUploadPrefix)
}

// UploadedAvatarDisplayKey returns the blob key of the 256px variant clients
// display for an uploaded avatar reference, and false for any other reference.
func UploadedAvatarDisplayKey(ref string) (string, bool) {
	if !IsUploadedAvatarRef(ref) {
		return "", false
	}
	return AvatarDisplayKey(strings.TrimPrefix(strings.TrimSpace(ref), AvatarUploadPrefix)), true
}

// AvatarDisplayKey returns the display variant's key beside an uploaded
// avatar's original key.
func AvatarDisplayKey(originalKey string) string {
	if strings.HasSuffix(originalKey, "/original.webp") {
		return strings.TrimSuffix(originalKey, "/original.webp") + "/w256.webp"
	}
	return strings.TrimRight(originalKey, "/") + "/w256.webp"
}
