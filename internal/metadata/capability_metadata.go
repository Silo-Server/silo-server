package metadata

// What a provider's metadata_provider.v1 capability declares in its
// metadata: the content levels it serves, default priorities and enablement,
// lookup and rating sources, and the chain placement those give a new
// provider.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// providerSupportsLevel reports whether a metadata provider should participate
// in the chain-less global fallback for contentLevel. A provider that declares
// a non-empty default_priority map is treated as enumerating the content levels
// it supports: it is eligible only for levels present in that map with a
// positive priority. A provider that declares no default_priority makes no
// claim and stays eligible for every level (legacy behavior), ranked last.
//
// This is what keeps a single-purpose provider (e.g. an audiobook metadata
// provider declaring only {"audiobook": N}) from being pulled into video
// content levels when a library has no enabled chain entry for that level.
func providerSupportsLevel(metadataJSON []byte, contentLevel string) bool {
	levels, declared := declaredPriorityLevels(metadataJSON)
	if !declared {
		return true
	}
	return levels[contentLevel] > 0
}

// providerEligibleForFallback reports whether a capability may participate in
// the chain-less fallback for contentLevel. The fallback runs not only for
// chain-less libraries but whenever a level's enabled-filter yields zero
// entries, so a capability that opts out of being enabled by default
// (default_enabled=false, e.g. a specialist plugin) must not activate itself
// through it. The built-in NFO provider is enabled by default, so a
// chain-less library reads local metadata. Absent or unparseable metadata
// defaults to eligible, preserving legacy behavior.
func providerEligibleForFallback(metadataJSON []byte, contentLevel string) bool {
	return providerSupportsLevel(metadataJSON, contentLevel) && extractDefaultEnabled(metadataJSON)
}

// lookupCapabilityMetadata returns the raw plugin_capabilities.metadata JSON for
// a provider's exact metadata_provider.v1 capability, or nil if absent.
func lookupCapabilityMetadata(ctx context.Context, pool *pgxpool.Pool, pluginInstallationID int, capabilityID string) []byte {
	metadataJSON, _ := lookupCapabilityMetadataChecked(ctx, pool, pluginInstallationID, capabilityID)
	return metadataJSON
}

// lookupCapabilityMetadataChecked is lookupCapabilityMetadata that tells a
// capability with no metadata row (nil, nil: the fallback placement applies)
// from a query that failed (an error: no placement is known).
func lookupCapabilityMetadataChecked(ctx context.Context, pool *pgxpool.Pool, pluginInstallationID int, capabilityID string) ([]byte, error) {
	var metadataJSON []byte
	err := pool.QueryRow(ctx,
		`SELECT metadata FROM plugin_capabilities
		 WHERE plugin_installation_id = $1
		   AND capability_id = $2
		   AND capability_type = 'metadata_provider.v1'`,
		pluginInstallationID,
		capabilityID,
	).Scan(&metadataJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading capability metadata (install=%d, cap=%s): %w", pluginInstallationID, capabilityID, err)
	}
	return metadataJSON, nil
}

// LookupDefaultPriority queries plugin_capabilities for a provider's declared
// default_priority at the given content level. Returns 0 if not found.
func LookupDefaultPriority(ctx context.Context, pool *pgxpool.Pool, pluginInstallationID int, capabilityID, contentLevel string) int {
	return extractDefaultPriority(lookupCapabilityMetadata(ctx, pool, pluginInstallationID, capabilityID), contentLevel)
}

// extractDefaultPriority parses the default_priority for a content level from
// capability metadata JSON.
func extractDefaultPriority(metadataJSON []byte, contentLevel string) int {
	levels, _ := declaredPriorityLevels(metadataJSON)
	if v, ok := levels[contentLevel]; ok && v > 0 {
		return int(v)
	}
	return 0
}

// extractDefaultEnabled reports whether a provider should be enabled by default
// when a chain is first seeded for a new library. It defaults to true when the
// flag is absent or unparseable, so every plugin predating this flag keeps its
// original seeded-enabled behavior. A specialist provider (e.g. a sports
// metadata source that should not compete with general providers on every
// library) sets metadata.default_enabled=false to be seeded installed-but-off,
// leaving it one click away for users who want it. The flag lives in the same
// "metadata" envelope as default_priority.
func extractDefaultEnabled(metadataJSON []byte) bool {
	raw, ok := capabilityMetadataField(metadataJSON, "default_enabled")
	if !ok {
		return true
	}
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil {
		return true
	}
	return enabled
}

// extractLookupProviderIDs parses a capability's lookup_provider_ids: the
// provider-ID keys an enrichment-only provider can look an item up by when it
// never assigns an ID of its own. The MDBList plugin, for one, declares
// ["imdb", "tmdb"] and is called whenever the item carries either.
//
// The list means "any one of these", unlike the markers capability's
// required_external_ids, which means "all of these". Keys are trimmed,
// lowercased (provider-ID keys are lowercase throughout the host) and
// deduplicated. Anything other than a JSON array of strings yields nil, which
// leaves the provider gated on its own ID as before.
func extractLookupProviderIDs(metadataJSON []byte) []string {
	raw, ok := capabilityMetadataField(metadataJSON, "lookup_provider_ids")
	if !ok {
		return nil
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil
	}
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// maxBulkLookupLimit caps a declared bulk_lookup_limit, so a plugin cannot ask
// the host for unbounded concurrency.
const maxBulkLookupLimit = 200

// extractBulkLookupLimit parses a capability's bulk_lookup_limit: the number of
// concurrent GetMetadata calls the provider combines into one upstream request.
// Declaring it opts an enrichment-only provider (one with lookup_provider_ids)
// into the bulk enrichment pass, which keeps that many lookups in flight. It
// also promises that the provider reports a spent quota, an outage or a
// missing credential as an error status rather than an empty item, because the
// pass records an empty item as "nothing to find".
//
// Anything other than a positive JSON integer yields 0 (not opted in); larger
// values are capped at maxBulkLookupLimit.
func extractBulkLookupLimit(metadataJSON []byte) int {
	raw, ok := capabilityMetadataField(metadataJSON, "bulk_lookup_limit")
	if !ok {
		return 0
	}
	var limit int
	if err := json.Unmarshal(raw, &limit); err != nil || limit <= 0 {
		return 0
	}
	return min(limit, maxBulkLookupLimit)
}

// maxDeclaredRatingSources caps how many rating sources one capability may
// declare, so a plugin cannot crowd a title page.
const maxDeclaredRatingSources = 8

// maxRatingSourceNameRunes caps a declared source's name, the mark clients
// show next to its score.
const maxRatingSourceNameRunes = 24

// maxRatingSourceLabelRunes caps a declared source's label, its full name in
// the administrator's list; a longer one falls back to the name.
const maxRatingSourceLabelRunes = 60

// extractRatingSources parses a capability's rating_sources: the ratings it
// reports under ratings.sources beyond Silo's own IMDb and TMDB. Each entry is
// {"id", "name", "label", "scale", "percent"}:
//
//	"rating_sources": [{"id": "kinopoisk", "name": "Kinopoisk", "scale": 10}]
//
// id is a source name (see models.ValidRatingSourceID, compared lowercased)
// other than imdb or tmdb; rt_critic and rt_audience name the Rotten Tomatoes
// scores stored in their own columns. name is the plain-text mark clients show
// next to the score, label an optional longer name for the administrator's
// list (name when absent), scale the top of the source's own scale (10 shows
// a stored 72 as 7.2), and percent shows the score as a percentage, which
// makes the scale 100 whatever the entry says. rt_critic and rt_audience are
// always percentages. An entry
// that breaks any of these is dropped on its own; duplicates keep the first;
// at most maxDeclaredRatingSources are kept. Declared sources stay hidden until
// an administrator turns them on.
func extractRatingSources(metadataJSON []byte) []models.RatingSourceDefinition {
	raw, ok := capabilityMetadataField(metadataJSON, "rating_sources")
	if !ok {
		return nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	var out []models.RatingSourceDefinition
	seen := make(map[string]struct{}, len(entries))
	for _, rawEntry := range entries {
		var entry struct {
			ID      string  `json:"id"`
			Name    string  `json:"name"`
			Label   string  `json:"label"`
			Scale   float64 `json:"scale"`
			Percent bool    `json:"percent"`
		}
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(entry.ID))
		name := strings.TrimSpace(entry.Name)
		if !models.ValidRatingSourceID(id) || models.IsBuiltinRatingSource(id) {
			continue
		}
		if name == "" || utf8.RuneCountInString(name) > maxRatingSourceNameRunes {
			continue
		}
		// Rotten Tomatoes scores fill the rating_rt_* columns, which hold a
		// percentage that poster badges show as is.
		if id == models.RatingSourceRTCritic || id == models.RatingSourceRTAudience {
			entry.Percent = true
		}
		if entry.Percent {
			entry.Scale = 100
		}
		if math.IsNaN(entry.Scale) || entry.Scale <= 0 || entry.Scale > 100 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		label := strings.TrimSpace(entry.Label)
		if label == "" || utf8.RuneCountInString(label) > maxRatingSourceLabelRunes {
			label = name
		}
		out = append(out, models.RatingSourceDefinition{Source: id, Name: name, Label: label, Scale: entry.Scale, Percent: entry.Percent})
		if len(out) == maxDeclaredRatingSources {
			break
		}
	}
	return out
}

// capabilityMetadataField reads one plugin-declared field from capability
// metadata JSON. The field may sit at the top level or inside the "metadata"
// envelope the SDK wraps plugin-declared fields in; the top level wins.
func capabilityMetadataField(metadataJSON []byte, key string) (json.RawMessage, bool) {
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metadataJSON, &meta); err != nil {
		return nil, false
	}
	if raw, ok := meta[key]; ok {
		return raw, true
	}
	innerRaw, ok := meta["metadata"]
	if !ok {
		return nil, false
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(innerRaw, &inner); err != nil {
		return nil, false
	}
	raw, ok := inner[key]
	return raw, ok
}

// SeedPlacement captures how a provider's manifest wants it placed when a chain
// is first seeded for a content level: whether it handles the level at all, its
// declared priority, and whether it should be enabled by default.
type SeedPlacement struct {
	SupportsLevel   bool
	DefaultPriority int
	DefaultEnabled  bool
}

// LookupSeedPlacement resolves a provider's seed placement for one content level
// from its capability manifest with a single metadata fetch. SupportsLevel uses
// the same rule as the chain-less fallback (providerSupportsLevel): a provider
// that enumerates levels is eligible only for the ones it lists; a provider that
// declares none stays eligible everywhere.
func LookupSeedPlacement(ctx context.Context, pool *pgxpool.Pool, pluginInstallationID int, capabilityID, contentLevel string) SeedPlacement {
	return seedPlacementFrom(lookupCapabilityMetadata(ctx, pool, pluginInstallationID, capabilityID), contentLevel)
}

// LookupSeedPlacementChecked is LookupSeedPlacement that returns a failed
// metadata query instead of the fallback placement, so a caller never
// persists a chain computed from a lookup that did not happen. A capability
// without metadata still gets the fallback.
func LookupSeedPlacementChecked(ctx context.Context, pool *pgxpool.Pool, pluginInstallationID int, capabilityID, contentLevel string) (SeedPlacement, error) {
	md, err := lookupCapabilityMetadataChecked(ctx, pool, pluginInstallationID, capabilityID)
	if err != nil {
		return SeedPlacement{}, err
	}
	return seedPlacementFrom(md, contentLevel), nil
}

func seedPlacementFrom(md []byte, contentLevel string) SeedPlacement {
	return SeedPlacement{
		SupportsLevel:   providerSupportsLevel(md, contentLevel),
		DefaultPriority: extractDefaultPriority(md, contentLevel),
		DefaultEnabled:  extractDefaultEnabled(md),
	}
}

// declaredPriorityLevels parses a capability's default_priority map. The map may
// sit at the top level or inside a "metadata" envelope (plugin capability
// metadata wraps plugin-declared fields in a "metadata" sub-object). The second
// return value is true only when a non-empty map was found, i.e. the provider
// explicitly enumerates the content levels it supports.
func declaredPriorityLevels(metadataJSON []byte) (map[string]float64, bool) {
	dpRaw, ok := capabilityMetadataField(metadataJSON, "default_priority")
	if !ok {
		return nil, false
	}
	var dpMap map[string]float64
	if err := json.Unmarshal(dpRaw, &dpMap); err != nil {
		return nil, false
	}
	return dpMap, len(dpMap) > 0
}
