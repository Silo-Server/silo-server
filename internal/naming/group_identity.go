package naming

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"
)

const ContentGroupKeyVersion = 1

type GroupIdentity struct {
	GroupKeyVersion    int
	ContentGroupKey    string
	ObservedRootPath   string
	BaseTitle          string
	BaseYear           int
	BaseType           string
	Confidence         string
	TmdbID             string
	ImdbID             string
	TvdbID             string
	State              string
	EvidenceJSON       []byte
	RepresentativePath string
}

// InferGroupIdentity derives a logical content-group identity from a media
// file path plus the scanner's root assignment.
func InferGroupIdentity(filePath string, libraryType string, assignment RootAssignment) GroupIdentity {
	cleanFilePath := filepath.Clean(filePath)
	group := GroupIdentity{
		GroupKeyVersion:    ContentGroupKeyVersion,
		ObservedRootPath:   deriveObservedRootPath(cleanFilePath, assignment.RootPath),
		BaseType:           assignment.InferredType,
		Confidence:         "low",
		State:              "resolved",
		RepresentativePath: cleanFilePath,
	}

	if group.BaseType == "" {
		ctx := ResolvePathContext(cleanFilePath, libraryType)
		group.BaseType = "movie"
		if ctx != nil && ctx.Type != "" {
			group.BaseType = ctx.Type
		}
	}

	// Explicit structured provider IDs (e.g. {tmdb-694938}) anchor the group's
	// identity regardless of how folder and file titles relate, so title
	// conflicts must not mark the group ambiguous. Renamed releases inside a
	// tagged folder ("Override (2021) {tmdb-694938}" containing "R.I.A.
	// (2021).mkv") are the common case.
	idAnchored := hasStructuredIDAnchor(cleanFilePath, group.ObservedRootPath)

	if group.BaseType == "series" {
		populateSeriesGroupIdentity(cleanFilePath, libraryType, assignment, idAnchored, &group)
	} else {
		populateMovieGroupIdentity(cleanFilePath, assignment, idAnchored, &group)
	}

	if ids := ParseFolderIDs(filepath.Base(group.ObservedRootPath)); ids != nil {
		group.TmdbID = ids.TmdbID
		group.ImdbID = ids.ImdbID
		group.TvdbID = ids.TvdbID
	}
	if group.TmdbID == "" || group.ImdbID == "" || group.TvdbID == "" {
		if ids := ParseStructuredFolderIDs(strings.TrimSuffix(filepath.Base(cleanFilePath), filepath.Ext(cleanFilePath))); ids != nil {
			if group.TmdbID == "" {
				group.TmdbID = ids.TmdbID
			}
			if group.ImdbID == "" {
				group.ImdbID = ids.ImdbID
			}
			if group.TvdbID == "" {
				group.TvdbID = ids.TvdbID
			}
		}
	}

	if group.ContentGroupKey == "" {
		group.ContentGroupKey = isolatedGroupKey(group.BaseType, group.ObservedRootPath, cleanFilePath)
	}

	return group
}

func populateMovieGroupIdentity(filePath string, assignment RootAssignment, idAnchored bool, group *GroupIdentity) {
	parentDir := filepath.Dir(filePath)
	parentTitle, parentYear, parentTrusted := parseInferFolderTitleYear(filepath.Base(group.ObservedRootPath))
	parentTitle = StripComparisonSafeEditionSuffix(parentTitle)
	baseNoExt := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	stem := parseInferMovieStem(baseNoExt, parentTitle, parentYear)

	baseTitle := ""
	baseYear := 0
	confidence := "low"
	state := "resolved"
	reasons := []string{}

	if parentTrusted && parentTitle != "" {
		baseTitle = parentTitle
		baseYear = parentYear
		confidence = "high"
	}

	if stem.Title != "" {
		switch relation := classifyTitleRelation(parentTitle, stem.Title); {
		case parentTrusted && relation == titleRelationEquivalent:
			if stem.Year != 0 && baseYear == 0 {
				baseYear = stem.Year
			}
		case parentTrusted && relation == titleRelationSoft:
			reasons = append(reasons, "variant_suffix")
		case parentTrusted && relation == titleRelationHard:
			if idAnchored {
				reasons = append(reasons, "unrelated_title_resolved_by_provider_ids")
			} else {
				state = "ambiguous"
				reasons = append(reasons, "unrelated_title")
			}
		default:
			if baseTitle == "" {
				baseTitle = StripComparisonSafeEditionSuffix(stem.Title)
				baseYear = stem.Year
				confidence = stem.Confidence
			}
		}
	}

	if baseTitle == "" {
		baseTitle = StripComparisonSafeEditionSuffix(assignment.Title)
		baseYear = assignment.Year
		if baseTitle != "" {
			confidence = "medium"
		}
	}
	if baseTitle == "" && parentTitle != "" {
		baseTitle = parentTitle
		baseYear = parentYear
	}

	if assignment.HasEpisodePattern && !assignment.HasSeasonStructure && !parentTrusted && !idAnchored {
		state = "ambiguous"
		reasons = append(reasons, "episode_pattern")
	}

	group.BaseTitle = baseTitle
	group.BaseYear = baseYear
	group.Confidence = normalizeIdentityConfidence(confidence)
	group.State = state
	group.ContentGroupKey = makeContentGroupKey("movie", baseTitle, baseYear, parentDir, filePath)
	group.EvidenceJSON, _ = json.Marshal(map[string]any{
		"parent_title":      parentTitle,
		"parent_year":       parentYear,
		"parent_trusted":    parentTrusted,
		"stem_title":        stem.Title,
		"stem_year":         stem.Year,
		"stem_remainder":    stem.Remainder,
		"confidence":        group.Confidence,
		"observed_root":     group.ObservedRootPath,
		"reasons":           reasons,
		"has_episode_shape": assignment.HasEpisodePattern,
	})
}

func populateSeriesGroupIdentity(filePath string, libraryType string, assignment RootAssignment, idAnchored bool, group *GroupIdentity) {
	ctx := ResolvePathContext(filePath, libraryType)
	title := assignment.Title
	year := assignment.Year
	state := "resolved"
	reasons := []string{}
	if ctx != nil {
		if ctx.Title != "" {
			title = ctx.Title
		}
		if ctx.Year != 0 {
			year = ctx.Year
		}
	}
	if title == "" {
		folderTitle, folderYear, _ := parseInferFolderTitleYear(filepath.Base(group.ObservedRootPath))
		title = folderTitle
		year = folderYear
	}
	observedTitle, observedYear, observedTrusted := parseInferFolderTitleYear(filepath.Base(group.ObservedRootPath))
	if observedTrusted && observedTitle != "" && title != "" {
		switch classifyTitleRelation(observedTitle, title) {
		case titleRelationSoft:
			reasons = append(reasons, "series_alias")
		case titleRelationHard:
			if idAnchored {
				reasons = append(reasons, "series_title_conflict_resolved_by_provider_ids")
			} else {
				state = "ambiguous"
				reasons = append(reasons, "series_title_conflict")
			}
		}
		if year == 0 {
			year = observedYear
		}
	}
	if assignment.Title != "" && title != "" {
		switch classifyTitleRelation(assignment.Title, title) {
		case titleRelationSoft:
			reasons = append(reasons, "series_assignment_soft_conflict")
		case titleRelationHard:
			if idAnchored {
				reasons = append(reasons, "series_assignment_conflict_resolved_by_provider_ids")
			} else {
				state = "ambiguous"
				reasons = append(reasons, "series_assignment_conflict")
			}
		}
	}

	confidence := "low"
	if title != "" {
		confidence = "medium"
	}
	if ctx != nil && ctx.RootPath != "" && ctx.HasSeasonStructure {
		confidence = "high"
	}
	if assignment.HasEpisodePattern && (assignment.HasSeasonStructure || (ctx != nil && ctx.HasSeasonStructure)) && state != "ambiguous" {
		confidence = "high"
	}
	if title == "" && assignment.HasEpisodePattern && !idAnchored {
		state = "ambiguous"
		reasons = append(reasons, "series_identity_missing")
	}

	group.BaseTitle = title
	group.BaseYear = year
	group.Confidence = normalizeIdentityConfidence(confidence)
	group.State = state
	group.ContentGroupKey = makeContentGroupKey("series", title, year, group.ObservedRootPath, filePath)
	group.EvidenceJSON, _ = json.Marshal(map[string]any{
		"title":               title,
		"year":                year,
		"observed_root":       group.ObservedRootPath,
		"observed_title":      observedTitle,
		"observed_year":       observedYear,
		"observed_trusted":    observedTrusted,
		"has_season_struct":   assignment.HasSeasonStructure,
		"has_episode_pattern": assignment.HasEpisodePattern,
		"reasons":             reasons,
	})
}

// hasStructuredIDAnchor reports whether the observed root folder or the file
// name carries explicit provider IDs — structured tags (e.g. {tmdb-694938},
// [tvdbid-81189]) or an unambiguous tt-prefixed IMDb id ("Eggs Run (2021)
// tt8049994"). ParseFolderIDs returns only such explicit evidence, which is
// strong enough to anchor identity over a title conflict.
func hasStructuredIDAnchor(filePath, observedRootPath string) bool {
	if ParseFolderIDs(filepath.Base(observedRootPath)) != nil {
		return true
	}
	baseNoExt := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	return ParseFolderIDs(baseNoExt) != nil
}

func deriveObservedRootPath(filePath, candidateRoot string) string {
	cleanFilePath := filepath.Clean(filePath)
	cleanRoot := filepath.Clean(candidateRoot)
	if cleanRoot != "" {
		if rel, err := filepath.Rel(cleanRoot, cleanFilePath); err == nil {
			if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				return cleanRoot
			}
		}
	}
	return filepath.Dir(cleanFilePath)
}

func normalizeIdentityConfidence(value string) string {
	switch value {
	case "high", "medium":
		return value
	default:
		return "low"
	}
}

func makeContentGroupKey(contentType, title string, year int, observedRootPath, filePath string) string {
	comparable := normalizeGroupComparable(title)
	if comparable == "" {
		return isolatedGroupKey(contentType, observedRootPath, filePath)
	}
	return fmt.Sprintf("v%d|%s|%s|%04d", ContentGroupKeyVersion, contentType, comparable, year)
}

func isolatedGroupKey(contentType, observedRootPath, filePath string) string {
	seed := filepath.Clean(observedRootPath)
	if seed == "" {
		seed = filepath.Clean(filePath)
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(seed))
	return fmt.Sprintf("v%d|%s|isolated|%x", ContentGroupKeyVersion, contentType, hash.Sum64())
}

func normalizeGroupComparable(title string) string {
	tokens := normalizeInferTokens(StripComparisonSafeEditionSuffix(title))
	if len(tokens) == 0 {
		return ""
	}
	if len(tokens) >= 4 && looksLikeInitialism(tokens[0], tokens[1:]) {
		tokens = tokens[1:]
	}
	return strings.Join(tokens, " ")
}

func looksLikeInitialism(token string, rest []string) bool {
	if len(token) < 2 || len(rest) < 2 {
		return false
	}
	var initials strings.Builder
	for _, part := range rest {
		if part == "" {
			continue
		}
		initials.WriteByte(part[0])
	}
	return token == initials.String()
}

type titleRelation string

const (
	titleRelationEquivalent titleRelation = "equivalent"
	titleRelationSoft       titleRelation = "soft"
	titleRelationHard       titleRelation = "hard"
)

func classifyTitleRelation(left, right string) titleRelation {
	if normalizeGroupComparable(left) == "" || normalizeGroupComparable(right) == "" {
		return titleRelationEquivalent
	}
	if normalizeGroupComparable(left) == normalizeGroupComparable(right) {
		return titleRelationEquivalent
	}

	leftTokens := strings.Fields(normalizeGroupComparable(left))
	rightTokens := strings.Fields(normalizeGroupComparable(right))
	if variantSuffixOnly(leftTokens, rightTokens) || variantSuffixOnly(rightTokens, leftTokens) {
		return titleRelationSoft
	}
	return titleRelationHard
}

func variantSuffixOnly(shorter, longer []string) bool {
	if len(shorter) == 0 || len(longer) <= len(shorter) {
		return false
	}
	for i := range shorter {
		if shorter[i] != longer[i] {
			return false
		}
	}
	for _, token := range longer[len(shorter):] {
		if inferEditionTokenKey(token) != "" {
			continue
		}
		switch token {
		case "version", "rated", "r", "super", "sized", "aka":
			continue
		default:
			return false
		}
	}
	return true
}
