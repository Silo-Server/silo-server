package userstore

import "fmt"

// ProfileDisplayPrefsID is the Jellyfin DisplayPreferences key of a
// profile's preferences document id: the profile ID is length-prefixed so no
// profile's keys are a prefix of another's.
func ProfileDisplayPrefsID(profileID, id string) string {
	return ProfileDisplayPrefsPrefix(profileID) + id
}

// ProfileDisplayPrefsPrefix is the start every ProfileDisplayPrefsID of
// profileID shares, by which the store deletes a profile's preferences.
func ProfileDisplayPrefsPrefix(profileID string) string {
	return fmt.Sprintf("profile:%d:%s:", len(profileID), profileID)
}
