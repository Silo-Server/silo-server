package literaryworks

import (
	"maps"
	"slices"
)

const AutoLinkThreshold = 0.86

type MatchItem struct {
	ContentID   string
	Type        string
	Title       string
	Authors     []string
	Narrators   []string
	SeriesName  string
	SeriesIndex *float64
	ExternalIDs map[string]string
	// WorkIDs are the item's work id claims (media_item_work_ids) by key.
	// Unlike ExternalIDs, which name one edition, every edition of a book may
	// claim the same work.
	WorkIDs   map[string]WorkID
	Publisher string
	Year      int
}

// WorkID is one work id claim: the value and how the plugin reached the work
// from the edition (bookid evidence).
type WorkID struct {
	ID       string `json:"id"`
	Evidence string `json:"evidence"`
}

func ScoreCandidate(source, target MatchItem) Candidate {
	if source.ContentID == "" || target.ContentID == "" || source.ContentID == target.ContentID {
		return Candidate{Score: 0, Evidence: map[string]string{"reason": "same_or_missing_content_id"}}
	}
	evidence := map[string]string{}
	sourceAuthor := personKey(source.Authors)
	targetAuthor := personKey(target.Authors)
	titleMatch := normalizeKey(source.Title) != "" && normalizeKey(source.Title) == normalizeKey(target.Title)
	authorMatch := sourceAuthor != "" && sourceAuthor == targetAuthor
	seriesMatch := normalizeKey(source.SeriesName) != "" &&
		normalizeKey(source.SeriesName) == normalizeKey(target.SeriesName) &&
		source.SeriesIndex != nil && target.SeriesIndex != nil &&
		*source.SeriesIndex == *target.SeriesIndex

	if provider, id, ok := sharedExternalID(source.ExternalIDs, target.ExternalIDs); ok {
		evidence["external_id"] = provider + ":" + id
		return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.98, LinkSource: LinkExternalID, Evidence: evidence}
	}
	if seriesMatch && authorMatch {
		evidence["series"] = source.SeriesName
		evidence["author"] = sourceAuthor
		return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.92, LinkSource: LinkSeriesMatch, Evidence: evidence}
	}
	// A shared work id ranks below a shared edition id and series, and is
	// checked before the author conflict below: the claim outweighs differently
	// written author names. Its evidence is shown, not scored, since it covers
	// only the step from edition to work.
	if provider, sourceWork, targetWork, ok := sharedWorkID(source.WorkIDs, target.WorkIDs); ok {
		evidence["work_id"] = provider + ":" + sourceWork.ID
		evidence["source_work_evidence"] = sourceWork.Evidence
		evidence["target_work_evidence"] = targetWork.Evidence
		return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.9, LinkSource: LinkExternalID, Evidence: evidence}
	}
	if titleMatch && authorMatch {
		evidence["title"] = source.Title
		evidence["author"] = sourceAuthor
		return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.9, LinkSource: LinkMetadataMatch, Evidence: evidence}
	}
	if titleMatch && sourceAuthor != "" && targetAuthor != "" && sourceAuthor != targetAuthor {
		evidence["conflict"] = "author"
		return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.2, LinkSource: LinkMetadataMatch, Evidence: evidence}
	}
	return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.4, LinkSource: LinkMetadataMatch, Evidence: evidence}
}

func sharedExternalID(a, b map[string]string) (string, string, bool) {
	for provider, aID := range a {
		if aID == "" || provider == "asin" {
			continue
		}
		if bID := b[provider]; bID != "" && bID == aID {
			return provider, aID, true
		}
	}
	return "", "", false
}

// sharedWorkID returns the first work key, in sorted order, both items claim
// with the same value.
func sharedWorkID(a, b map[string]WorkID) (string, WorkID, WorkID, bool) {
	for _, provider := range slices.Sorted(maps.Keys(a)) {
		aID, bID := a[provider], b[provider]
		if aID.ID != "" && aID.ID == bID.ID {
			return provider, aID, bID, true
		}
	}
	return "", WorkID{}, WorkID{}, false
}
