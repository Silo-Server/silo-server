package metadata

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/contentid"
)

// dropIDsNotRestatedByHints makes a local provider's ids replace the
// folder- and filename-tag ids as a set: given the hint ids that took effect
// (won), every unprotected trusted id they do not name is removed from
// accumulatedIDs and returned. Without this, a leftover tag id stays a
// trusted hint next to the NFO's ids, and the NFO's own title then fails
// trusted-id selection. Protected keys (stored durable ids) are kept.
func dropIDsNotRestatedByHints(
	ctx context.Context,
	won map[string]string,
	accumulatedIDs map[string]string,
	protected map[string]bool,
) []string {
	if len(won) == 0 {
		return nil
	}
	var dropped []string
	for _, key := range trustedSearchIDKeys {
		previous := strings.TrimSpace(accumulatedIDs[key])
		if won[key] != "" || protected[key] || previous == "" {
			continue
		}
		slog.InfoContext(ctx, "metadata: local identity hints replace a hint id they do not restate", "component", "metadata",
			"key", key, "dropped", previous)
		delete(accumulatedIDs, key)
		dropped = append(dropped, key)
	}
	return dropped
}

// unprotectedTrustedIDs returns the trusted ids a local provider's hints may
// replace: the folder- and filename-tag ids, not the stored ones (whose keys
// are in stored).
func unprotectedTrustedIDs(ids, stored map[string]string) map[string]string {
	out := make(map[string]string, len(trustedSearchIDKeys))
	for _, key := range trustedSearchIDKeys {
		if _, isStored := stored[key]; isStored {
			continue
		}
		if value := strings.TrimSpace(ids[key]); value != "" {
			out[key] = value
		}
	}
	return out
}

// staleLocalHints returns the ids the local providers declare that are
// recorded stale (a dead NFO id seen on an earlier attempt). It asks them
// only when stale ids are on record for the item.
func staleLocalHints(ctx context.Context, chain []Provider, query SearchQuery, stale providerIDValueSet) map[string]string {
	if len(stale) == 0 {
		return nil
	}
	var out map[string]string
	for _, p := range chain {
		hinter, ok := p.(IdentityHintProvider)
		if !ok {
			continue
		}
		for key, value := range hinter.IdentityHints(ctx, query) {
			if _, dead := stale[key][normalizeProviderIDComparisonValue(key, value)]; !dead {
				continue
			}
			if out == nil {
				out = make(map[string]string)
			}
			out[key] = value
		}
	}
	return out
}

// dropStaleHints takes the stale local hint ids out of won: they are no
// longer hints, so a retry does not query them again, and the tag id each
// would replace stays. It returns the keys whose tag id stays.
func dropStaleHints(won, accumulatedIDs, tagIDs, staleHints map[string]string) []string {
	var kept []string
	for key, value := range staleHints {
		delete(won, key)
		if tag := tagIDs[key]; tag != "" {
			accumulatedIDs[key] = tag
			kept = append(kept, key)
		} else if accumulatedIDs[key] == value {
			delete(accumulatedIDs, key)
		}
	}
	return kept
}

// recordVerifiedIDs adds to verified each hint id that a remote provider's
// result restates, compared as candidate ids are (key case, IMDb case), and
// returns verified.
func recordVerifiedIDs(verified, resultIDs, hints map[string]string) map[string]string {
	if len(hints) == 0 {
		return verified
	}
	resultIDs = sanitizeCandidateProviderIDs(resultIDs)
	for key, value := range hints {
		if value == "" || resultIDs[key] != value {
			continue
		}
		if verified == nil {
			verified = make(map[string]string, len(hints))
		}
		verified[key] = value
	}
	return verified
}

// anyProviderIDIn reports whether set holds one of the given provider id
// values, such as a verified NFO id a provider then answered 404 for.
func anyProviderIDIn(set providerIDValueSet, ids map[string]string) bool {
	for key, value := range ids {
		if set.has(key, value) {
			return true
		}
	}
	return false
}

// chainChecksIDs reports whether chain holds a search provider that can
// look the NFO's ids up: one other than the local provider and other than an
// enrichment provider, which only looks items up by ids it is given.
func chainChecksIDs(chain []Provider) bool {
	for _, p := range chain {
		if _, local := p.(IdentityHintProvider); local || isEnrichmentProvider(p) {
			continue
		}
		if _, ok := p.(SearchProvider); ok {
			return true
		}
	}
	return false
}

// anyProviderIDKept reports whether ids still holds one of the given
// provider id values.
func anyProviderIDKept(ids, values map[string]string) bool {
	for key, value := range values {
		if value != "" && strings.TrimSpace(ids[key]) == value {
			return true
		}
	}
	return false
}

// clearHintIDs removes dropped ids from a MatchHints copy so trusted-id
// selection no longer anchors on them.
func clearHintIDs(hints *MatchHints, keys []string) {
	for _, key := range keys {
		switch key {
		case contentid.ProviderTMDB:
			hints.TmdbID = ""
		case contentid.ProviderTVDB:
			hints.TvdbID = ""
		case contentid.ProviderIMDB:
			hints.ImdbID = ""
		}
	}
}
