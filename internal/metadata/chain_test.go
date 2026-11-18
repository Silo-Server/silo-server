package metadata

// Capability metadata as stored in plugin_capabilities.metadata: plugin-declared
// fields are wrapped in a "metadata" envelope.
const (
	audiobookCapMetadata = `{"metadata":{"default_priority":{"audiobook":2}},"display_name":"Audiobook Metadata"}`
	tmdbCapMetadata      = `{"metadata":{"default_priority":{"movie":2,"season":3,"series":3,"episode":3}},"display_name":"TMDB"}`
)
