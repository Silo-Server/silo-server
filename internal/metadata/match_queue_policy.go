package metadata

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	matchFailureSecretPattern = regexp.MustCompile(`(?i)("?(?:api[_-]?key|access[_-]?token|token|authorization|password)"?)\s*[:=]\s*"?([^"&,;\s}]+)"?`)
	matchFailureBearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[^&,;\s]+`)
	matchFailureSecretKey     = regexp.MustCompile(`(?i)^(?:api[_-]?key|access[_-]?token|token|authorization|password)$`)
)

const (
	movieMatcherRevision       = 10
	seriesMatcherRevision      = 10
	movieQueueRetryDelay       = 15 * time.Second
	seriesRootQueueQuietWindow = 10 * time.Second
	seriesRootQueueRetryDelay  = 30 * time.Second

	// A claim contains at most one job per available worker. Keep the lease long
	// enough that a second server does not reclaim a healthy provider call; a
	// crashed process can strand only that worker-sized in-flight window.
	// Workers explicitly release unfinished leases on healthy cancellation.
	matchQueueClaimLease = 2 * time.Hour

	// Transient provider failures use capped exponential backoff. Deterministic
	// outcomes use the fixed one-hour/24-hour/park schedule.
	matchQueueRetryMaxDelay      = 24 * time.Hour
	matchQueueBackoffMaxExponent = 16
)

// MatchQueueStateCounts is the pending/parked aggregate for one library.
type MatchQueueStateCounts struct {
	Pending int
	Parked  int
}

// matchQueueBackoffExpr returns the SQL expression both match queues use to
// schedule the next transient retry. attempt_count is incremented at claim.
func matchQueueBackoffExpr(basePlaceholder, maxPlaceholder string) string {
	return fmt.Sprintf(
		"NOW() + LEAST(%s::interval * power(2::float8, LEAST(attempt_count, %d)), %s::interval)",
		basePlaceholder, matchQueueBackoffMaxExponent, maxPlaceholder,
	)
}

// matchQueueInputFingerprintSQL returns a deterministic SQL expression for
// inputs that can change a result without changing the queue key. Arguments
// are internal SQL expressions selected by repository code, never user input.
func matchQueueInputFingerprintSQL(pathExpression, typeExpression, folderIDExpression, languageExpression string, matcherRevision int) string {
	return fmt.Sprintf(`md5(%s || '|' || COALESCE(%s, '') || '|' || COALESCE(%s, '') || '|%d|' || COALESCE((
		SELECT string_agg(
			chain.priority::text || ':' || installation.id::text || ':' || installation.plugin_id || ':' ||
			chain.capability_id || ':' || chain.content_level || ':' ||
			chain.enabled::text || ':' || installation.enabled::text || ':' || installation.version || ':' ||
			COALESCE((
				SELECT string_agg(config.config_key || ':' || config.updated_at::text, ',' ORDER BY config.config_key)
				FROM plugin_runtime_configs config
				WHERE config.plugin_installation_id = installation.id
			), ''),
			'|' ORDER BY chain.priority, installation.id, chain.capability_id, chain.content_level
		)
		FROM library_provider_chains chain
		JOIN plugin_installations installation ON installation.id = chain.plugin_installation_id
		WHERE chain.media_folder_id = %s
		  AND chain.capability_type = 'metadata_provider.v1'
	), ''))`, pathExpression, typeExpression, languageExpression, matcherRevision, folderIDExpression)
}

// seriesMatchQueueInputFingerprintSQL includes the active file-path set because
// episode validation derives both coordinates and episode-title evidence from
// those paths. Adding, removing, or renaming an episode wakes a parked match.
func seriesMatchQueueInputFingerprintSQL(rootExpression, folderIDExpression, languageExpression string) string {
	shapeExpression := fmt.Sprintf(`(COALESCE(%s, '') || '|shape:' || COALESCE((
		SELECT md5(string_agg(
			md5(shape_file.file_path), '' ORDER BY shape_file.file_path
		))
		FROM media_files shape_file
		WHERE shape_file.media_folder_id = %s
		  AND shape_file.observed_root_path = %s
		  AND shape_file.missing_since IS NULL
		  AND shape_file.extra_id IS NULL
	), ''))`, rootExpression, folderIDExpression, rootExpression)
	return matchQueueInputFingerprintSQL(shapeExpression, "'series'", folderIDExpression, languageExpression, seriesMatcherRevision)
}

func boundedMatchFailureMessage(message string) string {
	message = matchFailureBearerPattern.ReplaceAllString(strings.TrimSpace(message), "Bearer [redacted]")
	message = matchFailureSecretPattern.ReplaceAllString(message, "$1=[redacted]")
	runes := []rune(message)
	if len(runes) > 1000 {
		message = string(runes[:1000])
	}
	return message
}

func truncateMatchFailureField(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func normalizeMatchFailureKind(kind MatchOutcome) MatchOutcome {
	normalized := MatchOutcome(strings.ToLower(strings.TrimSpace(string(kind))))
	switch normalized {
	case MatchOutcomeNoCandidates,
		MatchOutcomeCandidateRejected,
		MatchOutcomeTrustedIDConflict,
		MatchOutcomeTrustedIDTypeMismatch,
		MatchOutcomeMetadataEmpty,
		MatchOutcomeProviderTransient,
		MatchOutcomeProviderPermanent:
		return normalized
	default:
		return MatchOutcomeProviderTransient
	}
}

// boundedMatchDecision makes the persistence boundary explicit. Provider IDs,
// titles, and reasons must not be able to grow queue rows without limit.
func boundedMatchDecision(decision *MatchDecision) *MatchDecision {
	if decision == nil {
		return nil
	}
	bounded := &MatchDecision{
		Outcome:        MatchOutcome(truncateMatchFailureField(string(decision.Outcome), 64)),
		CandidateCount: decision.CandidateCount,
		Threshold:      decision.Threshold,
	}
	for _, candidate := range decision.TopCandidates {
		if len(bounded.TopCandidates) == 3 {
			break
		}
		copyCandidate := MatchDecisionCandidate{
			Title:        truncateMatchFailureField(candidate.Title, 256),
			MatchedTitle: truncateMatchFailureField(candidate.MatchedTitle, 256),
			Year:         candidate.Year,
			Score:        candidate.Score,
		}
		if len(candidate.ProviderIDs) > 0 {
			copyCandidate.ProviderIDs = make(map[string]string, min(len(candidate.ProviderIDs), 8))
			keys := make([]string, 0, len(candidate.ProviderIDs))
			for key := range candidate.ProviderIDs {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if len(copyCandidate.ProviderIDs) == 8 {
					break
				}
				if matchFailureSecretKey.MatchString(strings.TrimSpace(key)) {
					continue
				}
				value := candidate.ProviderIDs[key]
				copyCandidate.ProviderIDs[truncateMatchFailureField(key, 64)] = truncateMatchFailureField(value, 256)
			}
		}
		for _, source := range candidate.Sources {
			if len(copyCandidate.Sources) == 8 {
				break
			}
			copyCandidate.Sources = append(copyCandidate.Sources, truncateMatchFailureField(source, 64))
		}
		for _, reason := range candidate.Reasons {
			if len(copyCandidate.Reasons) == 8 {
				break
			}
			copyCandidate.Reasons = append(copyCandidate.Reasons, truncateMatchFailureField(reason, 128))
		}
		bounded.TopCandidates = append(bounded.TopCandidates, copyCandidate)
	}
	return bounded
}
