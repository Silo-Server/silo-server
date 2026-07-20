package userstore

// ProfileAllowedLibrary associates one profile with one allowed library.
type ProfileAllowedLibrary struct {
	ProfileID string
	LibraryID int
}

// AttachAllowedLibraries replaces each profile's AllowedLibraryIDs with the
// matching associations, preserving their input order.
func AttachAllowedLibraries(profiles []Profile, allowedLibraries []ProfileAllowedLibrary) {
	byProfile := make(map[string][]int, len(profiles))
	for _, allowedLibrary := range allowedLibraries {
		byProfile[allowedLibrary.ProfileID] = append(
			byProfile[allowedLibrary.ProfileID],
			allowedLibrary.LibraryID,
		)
	}
	for i := range profiles {
		profiles[i].AllowedLibraryIDs = byProfile[profiles[i].ID]
	}
}
