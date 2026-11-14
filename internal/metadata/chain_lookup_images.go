package metadata

import "encoding/json"

// extractLookupImages parses a capability's lookup_images: true opens
// GetImages to the keys in lookup_provider_ids, as GetMetadata already is. An
// artwork source with no IDs of its own, fanart.tv for one, declares it so the
// host asks it for images by TMDB, IMDb or TVDB ID. Anything other than JSON
// true leaves images gated on the provider's own ID.
func extractLookupImages(metadataJSON []byte) bool {
	raw, ok := capabilityMetadataField(metadataJSON, "lookup_images")
	if !ok {
		return false
	}
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil {
		return false
	}
	return enabled
}
