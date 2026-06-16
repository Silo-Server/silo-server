package literaryworks

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
	Publisher   string
	Year        int
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
