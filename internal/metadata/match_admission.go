package metadata

import (
	"context"
	"strings"
)

// SearchMatchAdmissionStatus explains why a provider candidate was or was not
// admitted into an enrichment run.
type SearchMatchAdmissionStatus string

const (
	SearchMatchAccepted             SearchMatchAdmissionStatus = "accepted"
	SearchMatchNoCredibleMatch      SearchMatchAdmissionStatus = "no_credible_match"
	SearchMatchProviderDisagreement SearchMatchAdmissionStatus = "provider_disagreement"
	SearchMatchProviderIDConflict   SearchMatchAdmissionStatus = "provider_id_conflict"
	SearchMatchNoUsableProviderIDs  SearchMatchAdmissionStatus = "no_usable_provider_ids"
)

// ProviderIDConflict identifies either a durable identity owned by another
// item or a candidate that contradicts this item's existing provider ID.
type ProviderIDConflict struct {
	Provider           string
	ProviderID         string
	ExistingProviderID string
	OwnedBy            string
}

// ProviderIDAdmissionRequest contains the shared durable-identity inputs used
// for both search candidates and metadata responses.
type ProviderIDAdmissionRequest struct {
	CandidateProviderIDs map[string]string
	ExistingProviderIDs  map[string]string
	Owner                ProviderIDOwnerLookup
	ItemType             string
	ContentID            string
}

// ProviderIDAdmission contains only identities that are safe to merge. A
// candidate already present on the current item counts as usable but is not
// repeated in ProviderIDs.
type ProviderIDAdmission struct {
	ProviderIDs         map[string]string
	Conflicts           []ProviderIDConflict
	HasUsableIdentity   bool
	ContradictsExisting bool
}

// SearchMatchAdmissionRequest contains the shared policy inputs used by book
// enrichers when admitting one provider's search response.
type SearchMatchAdmissionRequest struct {
	WantTitle           string
	WantYear            int
	Results             []SearchResult
	AgreedTitle         string
	ExistingProviderIDs map[string]string
	Owner               ProviderIDOwnerLookup
	ItemType            string
	ContentID           string
	// NoAuthor marks an item that names no author. The author check after
	// GetMetadata then passes by default, so only the title vouches for a
	// candidate, and word overlap alone is not credible: "Raw" for "Raw
	// Stream" scores 0.67 (#3161). A candidate is considered only when its
	// title alone identifies the work (titleAloneIdentifies) or it carries a
	// provider ID the item already has.
	NoAuthor bool
}

// SearchMatchAdmission is the result of applying title credibility,
// cross-provider agreement, and durable-ID ownership checks atomically.
type SearchMatchAdmission struct {
	Status       SearchMatchAdmissionStatus
	MatchedTitle string
	AgreedTitle  string
	ProviderIDs  map[string]string
	Conflicts    []ProviderIDConflict
}

// AdmitSearchMatch centralizes candidate admission for audiobook, ebook, and
// manga enrichment. Provider IDs are staged and returned only after every
// ownership lookup succeeds, so a lookup failure cannot partially admit a
// candidate.
func AdmitSearchMatch(ctx context.Context, req SearchMatchAdmissionRequest) (SearchMatchAdmission, error) {
	results := req.Results
	if req.NoAuthor {
		results = titleOnlyCandidates(req.WantTitle, req.ExistingProviderIDs, results)
	}
	selection, ok := selectBestMatchYear(req.WantTitle, req.WantYear, results)
	if !ok {
		return SearchMatchAdmission{Status: SearchMatchNoCredibleMatch}, nil
	}

	result := SearchMatchAdmission{
		MatchedTitle: selection.matchedTitle,
		AgreedTitle:  req.AgreedTitle,
	}
	if req.AgreedTitle != "" && !AgreesWith(req.AgreedTitle, selection.matchedTitle) {
		result.Status = SearchMatchProviderDisagreement
		return result, nil
	}

	identity, err := AdmitProviderIDs(ctx, ProviderIDAdmissionRequest{
		CandidateProviderIDs: selection.result.ProviderIDs,
		ExistingProviderIDs:  req.ExistingProviderIDs,
		Owner:                req.Owner,
		ItemType:             req.ItemType,
		ContentID:            req.ContentID,
	})
	if err != nil {
		return SearchMatchAdmission{}, err
	}
	if identity.ContradictsExisting {
		result.Status = SearchMatchProviderIDConflict
		return result, nil
	}
	result.Conflicts = identity.Conflicts

	if !identity.HasUsableIdentity {
		result.Status = SearchMatchNoUsableProviderIDs
		return result, nil
	}
	result.Status = SearchMatchAccepted
	result.ProviderIDs = identity.ProviderIDs
	if result.AgreedTitle == "" {
		result.AgreedTitle = selection.matchedTitle
	}
	return result, nil
}

// titleOnlyCandidates keeps the results an item with no author can be matched
// to. They are filtered before selection, so a credible lower-scoring result
// is still chosen when the top score belongs to one that isn't.
func titleOnlyCandidates(want string, existing map[string]string, results []SearchResult) []SearchResult {
	kept := make([]SearchResult, 0, len(results))
	for _, r := range results {
		if sharesProviderID(existing, r.ProviderIDs) || resultTitleIdentifies(want, r) {
			kept = append(kept, r)
		}
	}
	return kept
}

func resultTitleIdentifies(want string, r SearchResult) bool {
	name := r.Name
	if strings.TrimSpace(name) == "" {
		name = r.OriginalTitle
	}
	if titleAloneIdentifies(want, name) {
		return true
	}
	for _, alias := range r.TitleAliases {
		if titleAloneIdentifies(want, alias.Title) {
			return true
		}
	}
	return false
}

// sharesProviderID reports whether candidate carries an ID the item already
// holds under the same provider, which identifies the work without the title.
func sharesProviderID(existing, candidate map[string]string) bool {
	for provider, id := range candidate {
		provider, id = strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(id)
		if provider == "" || id == "" {
			continue
		}
		for have, haveID := range existing {
			if strings.ToLower(strings.TrimSpace(have)) == provider && strings.TrimSpace(haveID) == id {
				return true
			}
		}
	}
	return false
}

// AdmitProviderIDs stages new identities only after every ownership lookup
// succeeds. Known conflicts are quarantined, and lookup failures return no
// partial result. This function is deliberately reused after GetMetadata:
// providers often repeat all cross-IDs there, including IDs rejected during
// search, and merging the raw response would reintroduce the conflict.
func AdmitProviderIDs(ctx context.Context, req ProviderIDAdmissionRequest) (ProviderIDAdmission, error) {
	result := ProviderIDAdmission{ProviderIDs: make(map[string]string)}
	existing := make(map[string]string, len(req.ExistingProviderIDs))
	for provider, providerID := range req.ExistingProviderIDs {
		provider = strings.ToLower(strings.TrimSpace(provider))
		providerID = strings.TrimSpace(providerID)
		if provider != "" && providerID != "" {
			existing[provider] = providerID
		}
	}

	for provider, providerID := range req.CandidateProviderIDs {
		provider = strings.ToLower(strings.TrimSpace(provider))
		providerID = strings.TrimSpace(providerID)
		if provider == "" || providerID == "" {
			continue
		}
		if current, exists := existing[provider]; exists {
			if current == providerID {
				result.HasUsableIdentity = true
			} else {
				result.ContradictsExisting = true
				result.Conflicts = append(result.Conflicts, ProviderIDConflict{
					Provider: provider, ProviderID: providerID,
					ExistingProviderID: current, OwnedBy: req.ContentID,
				})
			}
			continue
		}
		if req.Owner != nil {
			ownedBy, err := req.Owner.FindContentIDByProviderIDs(
				ctx,
				map[string]string{provider: providerID},
				req.ItemType,
				req.ContentID,
			)
			if err != nil {
				return ProviderIDAdmission{}, err
			}
			if ownedBy != "" {
				result.Conflicts = append(result.Conflicts, ProviderIDConflict{
					Provider: provider, ProviderID: providerID, OwnedBy: ownedBy,
				})
				continue
			}
		}
		result.ProviderIDs[provider] = providerID
		result.HasUsableIdentity = true
	}
	return result, nil
}
