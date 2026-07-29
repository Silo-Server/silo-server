package metadata

import (
	"context"
	"strings"
)

func (s *MetadataService) trustedIDTypeMismatchCandidates(
	ctx context.Context,
	hints *MatchHints,
	providerIDs map[string]string,
	language string,
	resolveChain chainResolver,
) []MatchCandidate {
	if hints == nil || !trustedHintIDsPresent(hints) || resolveChain == nil {
		return nil
	}

	var oppositeType string
	switch strings.ToLower(strings.TrimSpace(hints.Type)) {
	case matchContentTypeMovie:
		oppositeType = matchContentTypeSeries
	case matchContentTypeSeries:
		oppositeType = matchContentTypeMovie
	default:
		return nil
	}

	chain, err := resolveChain(providerChainContentLevel(oppositeType))
	if err != nil {
		return nil
	}
	query := SearchQuery{
		ContentType:               oppositeType,
		ProviderIDs:               copyMap(providerIDs),
		Language:                  language,
		FilePath:                  hints.FilePath,
		RepresentativeFilePath:    hints.RepresentativeFilePath,
		ObservedRootPath:          hints.ObservedRootPath,
		AllGroupFilePaths:         append([]string(nil), hints.AllGroupFilePaths...),
		PrimarySidecarSearchPaths: append([]string(nil), hints.PrimarySidecarSearchPaths...),
	}

	results := make([]SearchResult, 0)
	for _, provider := range chain {
		searchProvider, ok := provider.(SearchProvider)
		if !ok {
			continue
		}
		providerResults, searchErr := searchProvider.Search(ctx, query)
		if searchErr != nil {
			// This is optional diagnosis. A provider outage must not turn a
			// deterministic same-type miss into a transient queue failure.
			continue
		}
		for _, result := range providerResults {
			candidate := MatchCandidate{ProviderIDs: result.ProviderIDs}
			if candidateMatchesTrustedIDs(hints, candidate) {
				results = append(results, result)
			}
		}
	}
	return NormalizeCandidatesForLanguage(results, oppositeType, language)
}
