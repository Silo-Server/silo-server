// Package displayprefs owns the storage identity of Jellyfin
// DisplayPreferences blobs: the legacy user_settings key format they used to
// ride under, and the rules for moving each legacy row into the dedicated
// jellycompat_displayprefs table.
//
// Both database backends drive their data-copy migrations from this package so
// they cannot diverge on how a key parses or which rows move — the same shape
// internal/settingsmigrate gives the canonical settings backfill. The blobs
// themselves are opaque Jellyfin client JSON and are copied verbatim; nothing
// here decodes them.
package displayprefs

import (
	"fmt"
	"strings"
)

// NamespacePrefix marks every legacy user_settings row that belonged to the
// Jellyfin compatibility layer rather than to the user settings system. The
// move migration relocates or records every row under it, which is what lets
// the legacy settings API stop special-casing these keys.
const NamespacePrefix = "jellycompat:"

// legacyKeyPrefix is the DisplayPreferences handler's historical key format:
// legacyKeyPrefix + prefsID + ":" + client.
const legacyKeyPrefix = NamespacePrefix + "displayprefs:"

// LegacyKeyPattern is the SQL LIKE pattern matching every legacy jellycompat
// row. The prefix contains no LIKE wildcards, so no escaping is needed; both
// backends use it to find the rows to move and to delete them afterwards.
func LegacyKeyPattern() string {
	return NamespacePrefix + "%"
}

// LegacyKey reconstructs the user_settings key a blob was stored under. It is
// the exact format the handler wrote, kept here so a rollback re-creating
// legacy rows cannot drift from the parse below.
func LegacyKey(prefsID, client string) string {
	return legacyKeyPrefix + prefsID + ":" + client
}

// Blob is one DisplayPreferences document rehomed to the dedicated table.
type Blob struct {
	PrefsID string
	Client  string
	// Value is the stored Jellyfin client JSON, byte-for-byte.
	Value string
}

// Reject is a jellycompat-namespace row that does not parse as a
// DisplayPreferences blob. Only the legacy settings API's since-removed
// unknown-key carve-out could have written one; the migration records it for
// operator inspection rather than silently deleting it.
type Reject struct {
	Key    string
	Value  string
	Reason string
}

// PlanLegacyRow classifies one legacy user_settings row from the jellycompat
// namespace: exactly one of blob or reject is non-nil.
//
// The key splits at the LAST colon after the prefix. The writer always emitted
// prefsID + ":" + client, and Jellyfin client names ("emby", "jellyfin-web",
// possibly empty) do not contain colons, so the last colon is the separator
// even when the prefs id itself contains one. Distinct keys always yield
// distinct (prefsID, client) pairs — the key is recoverable as
// prefsID + ":" + client — so moved rows cannot collide.
func PlanLegacyRow(key, value string) (*Blob, *Reject) {
	remainder, ok := strings.CutPrefix(key, legacyKeyPrefix)
	if !ok {
		return nil, &Reject{
			Key: key, Value: value,
			Reason: fmt.Sprintf("jellycompat row is not a %s* key; only the removed legacy settings extension bag could have written it", legacyKeyPrefix),
		}
	}
	sep := strings.LastIndexByte(remainder, ':')
	if sep < 0 {
		return nil, &Reject{
			Key: key, Value: value,
			Reason: "displayprefs key has no id:client separator; the DisplayPreferences handler never wrote this shape",
		}
	}
	return &Blob{
		PrefsID: remainder[:sep],
		Client:  remainder[sep+1:],
		Value:   value,
	}, nil
}
