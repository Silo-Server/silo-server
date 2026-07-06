package naming

import (
	"encoding/json"
	"fmt"
	"strings"
)

// IdentityOverride carries operator-forced identity values applied to a single
// file's inferred group identity (see models.MediaIdentityOverride). Empty
// fields leave the inferred value in place.
type IdentityOverride struct {
	ForcedType   string
	ForcedTitle  string
	ForcedYear   int
	ForcedTmdbID string
	ForcedImdbID string
	ForcedTvdbID string
}

func (o IdentityOverride) isZero() bool {
	return o.ForcedType == "" && o.ForcedTitle == "" && o.ForcedYear == 0 &&
		o.ForcedTmdbID == "" && o.ForcedImdbID == "" && o.ForcedTvdbID == ""
}

// ApplyIdentityOverride forces operator-provided identity values onto an
// inferred group identity and re-derives its ContentGroupKey so the file lands
// in its own group even when its parsed title+year collides with a neighbor.
// A forced provider ID acts like a structured name tag: it anchors identity,
// clears ambiguity, and becomes part of the group key. It reports whether the
// identity was changed.
func ApplyIdentityOverride(identity *GroupIdentity, override IdentityOverride) bool {
	if identity == nil || override.isZero() {
		return false
	}

	if forcedType := strings.TrimSpace(override.ForcedType); forcedType != "" {
		identity.BaseType = forcedType
	}
	if forcedTitle := strings.TrimSpace(override.ForcedTitle); forcedTitle != "" {
		identity.BaseTitle = forcedTitle
	}
	if override.ForcedYear > 0 {
		identity.BaseYear = override.ForcedYear
	}
	if forced := strings.TrimSpace(override.ForcedTmdbID); forced != "" {
		identity.TmdbID = forced
	}
	if forced := strings.TrimSpace(override.ForcedImdbID); forced != "" {
		identity.ImdbID = forced
	}
	if forced := strings.TrimSpace(override.ForcedTvdbID); forced != "" {
		identity.TvdbID = forced
	}

	identity.Confidence = "high"
	identity.State = "resolved"
	identity.Overridden = true
	identity.ContentGroupKey = overriddenGroupKey(identity, override)
	identity.EvidenceJSON = mergeOverrideEvidence(identity.EvidenceJSON, override)
	return true
}

// overriddenGroupKey derives the group key for an overridden identity. A
// forced provider ID yields an anchored key so files forced to the same title
// always share a group and files forced apart never do. Without a forced
// provider ID the key falls back to the normal title+year derivation over the
// forced values.
func overriddenGroupKey(identity *GroupIdentity, override IdentityOverride) string {
	if anchored := anchoredGroupKey(
		identity.GroupKeyVersion,
		identity.BaseType,
		override.ForcedTmdbID,
		override.ForcedImdbID,
		override.ForcedTvdbID,
	); anchored != "" {
		return anchored
	}
	return makeContentGroupKey(identity.BaseType, identity.BaseTitle, identity.BaseYear, identity.ObservedRootPath, identity.RepresentativePath)
}

// anchoredGroupKey derives a provider-anchored content-group key, or "" when
// no usable provider ID is present. Provider precedence mirrors
// internal/contentid: movies tmdb→imdb→tvdb, series tvdb→tmdb→imdb, so two
// derivations that see the same ID set always pick the same anchor.
func anchoredGroupKey(version int, contentType, tmdbID, imdbID, tvdbID string) string {
	const (
		providerTmdb = "tmdb"
		providerImdb = "imdb"
		providerTvdb = "tvdb"
	)
	provider, id := "", ""
	tmdb := strings.TrimSpace(tmdbID)
	imdb := strings.TrimSpace(imdbID)
	tvdb := strings.TrimSpace(tvdbID)
	if contentType == "series" {
		switch {
		case tvdb != "":
			provider, id = providerTvdb, tvdb
		case tmdb != "":
			provider, id = providerTmdb, tmdb
		case imdb != "":
			provider, id = providerImdb, imdb
		}
	} else {
		switch {
		case tmdb != "":
			provider, id = providerTmdb, tmdb
		case imdb != "":
			provider, id = providerImdb, imdb
		case tvdb != "":
			provider, id = providerTvdb, tvdb
		}
	}
	if provider == "" {
		return ""
	}
	return fmt.Sprintf("v%d|%s|anchor|%s-%s", version, contentType, provider, strings.ToLower(id))
}

func mergeOverrideEvidence(evidence []byte, override IdentityOverride) []byte {
	fields := map[string]any{}
	if len(evidence) > 0 {
		_ = json.Unmarshal(evidence, &fields)
	}
	fields["identity_override"] = map[string]any{
		"forced_type":    override.ForcedType,
		"forced_title":   override.ForcedTitle,
		"forced_year":    override.ForcedYear,
		"forced_tmdb_id": override.ForcedTmdbID,
		"forced_imdb_id": override.ForcedImdbID,
		"forced_tvdb_id": override.ForcedTvdbID,
	}
	merged, err := json.Marshal(fields)
	if err != nil {
		return evidence
	}
	return merged
}
