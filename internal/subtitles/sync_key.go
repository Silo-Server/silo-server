package subtitles

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// A sync key names one subtitle of a media file whose timing can be
// corrected: a stored subtitle ("stored-{id}") or a sidecar file
// ("external-{path key}"). Clients read it from the playback inventory and
// treat it as opaque.
const (
	syncKeyStoredPrefix   = "stored-"
	syncKeyExternalPrefix = "external-"
)

var externalPathKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ExternalPathKey identifies a sidecar without exposing its filesystem path:
// the hex SHA-256 of the path. Playback URLs pin sidecars with it.
func ExternalPathKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

// StoredSyncKey is the sync key of a stored subtitle.
func StoredSyncKey(id int) string { return syncKeyStoredPrefix + strconv.Itoa(id) }

// ExternalSyncKey is the sync key of the sidecar at path.
func ExternalSyncKey(path string) string { return syncKeyExternalPrefix + ExternalPathKey(path) }

// ParseSyncKey returns the stored subtitle ID or the sidecar path key a sync
// key names; ok is false for anything else.
func ParseSyncKey(key string) (storedID int, externalPathKey string, ok bool) {
	if raw, found := strings.CutPrefix(key, syncKeyStoredPrefix); found {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 || strconv.Itoa(id) != raw {
			return 0, "", false
		}
		return id, "", true
	}
	if raw, found := strings.CutPrefix(key, syncKeyExternalPrefix); found && externalPathKeyPattern.MatchString(raw) {
		return 0, raw, true
	}
	return 0, "", false
}

// SyncTarget names a subtitle of a media file whose timing can be corrected:
// a stored subtitle or a sidecar file.
type SyncTarget struct {
	MediaFileID int
	// StoredID is a stored subtitle's ID; 0 for a sidecar.
	StoredID int
	// ExternalPath is a sidecar's path; empty for a stored subtitle.
	ExternalPath string
}

// Key is the target's sync key.
func (t SyncTarget) Key() string {
	if t.StoredID > 0 {
		return StoredSyncKey(t.StoredID)
	}
	return ExternalSyncKey(t.ExternalPath)
}
