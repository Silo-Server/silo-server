package catalog

import (
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
)

const LibraryCollectionVisibilityVisible = "visible"

// AccessFilter captures effective viewer access constraints for catalog reads.
type AccessFilter struct {
	AllowedLibraryIDs     []int
	AllowedContentIDs     []string
	DisabledLibraryIDs    []int // libraries whose membership globally hides an item
	PresentationLibraryID *int
	// ScopeFilesToLibrary limits file versions to PresentationLibraryID when
	// both are set. Callers opt in per read from the server setting
	// catalog.scope_versions_to_library; playback and watch-together reads never
	// set it, so an item always plays from its full accessible version list.
	ScopeFilesToLibrary  bool
	PresentationLanguage string
	// ProfilePreferredLanguage is the viewer profile's preferred metadata
	// language. Presentation language resolves: explicit PresentationLanguage
	// → ProfilePreferredLanguage → the library's metadata_language.
	ProfilePreferredLanguage string
	// MetadataLanguageOverrides maps an item's original language to a target
	// language before ProfilePreferredLanguage is used as the fallback.
	MetadataLanguageOverrides map[string]string
	// PresentationOriginalLanguage supplies a parent series' original language
	// while localizing season and episode rows, which do not duplicate it.
	PresentationOriginalLanguage string
	// MaturityLimits are the viewer's content-rating ceiling, unrated-title
	// policy, and advisory-age limit, copied from access.Scope as one value.
	// Code that narrows a filter to its maturity restrictions copies this
	// whole field (AccessFilter{MaturityLimits: f.MaturityLimits}) so no limit
	// can be dropped along the way. ApplyMaturityLimits turns it into SQL.
	access.MaturityLimits
	MaxPlaybackQuality string
	SelectedFileID     int
	UserID             int
	ProfileID          string
	// DeviceID identifies the requesting client for device-scoped setting
	// resolution. It does not participate in catalog access control.
	DeviceID string
	// ImageSize is the artwork size the client asked for on this request, and
	// like DeviceID it does not participate in access control. It rides here
	// because detail building fans out through a dozen helpers that already
	// carry the filter, and every artwork URL in one response has to agree on a
	// size. Unset means the caller expressed no preference, and the per-context
	// defaults apply. See internal/imagesize.
	ImageSize imagesize.Size
	// NamePrefix, when non-empty, restricts results to items whose
	// LOWER(COALESCE(NULLIF(BTRIM(sort_title),''), title)) starts with the
	// given (case-insensitive) prefix. Pushed into the SQL WHERE clause so
	// the predicate can use idx_media_items_sort_key.
	NamePrefix string
	// ExcludedMediaTypes lists media_items.type values the viewer's surface
	// never exposes (e.g. the Jellyfin compat layer excludes "audiobook" and
	// "podcast" — they're served by the ABS-compat API instead). Applied by
	// every query builder that consumes an AccessFilter.
	ExcludedMediaTypes []string
}

// CanAccessLibraryCollection reports whether a visible server collection is
// reachable through at least one library in the viewer's effective scope.
// Collections without explicit library scope retain their legacy unrestricted
// visibility, but restricted viewers cannot address them by ID.
func CanAccessLibraryCollection(collection *models.LibraryCollection, filter AccessFilter) bool {
	if collection == nil || collection.Visibility != LibraryCollectionVisibilityVisible {
		return false
	}
	if len(collection.LibraryIDs) == 0 {
		return filter.AllowedLibraryIDs == nil && len(filter.DisabledLibraryIDs) == 0
	}
	for _, libraryID := range collection.LibraryIDs {
		if filter.AllowedLibraryIDs != nil && !intInSlice(libraryID, filter.AllowedLibraryIDs) {
			continue
		}
		if intInSlice(libraryID, filter.DisabledLibraryIDs) {
			continue
		}
		return true
	}
	return false
}

func applyAccessFilter(alias string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	ApplyMaturityLimits(alias, filter, conditions, args, argIdx)
	if len(filter.ExcludedMediaTypes) > 0 {
		*conditions = append(*conditions, fmt.Sprintf("NOT (%s.type = ANY($%d))", alias, *argIdx))
		*args = append(*args, filter.ExcludedMediaTypes)
		*argIdx = *argIdx + 1
	}
}

// contentRatingCeilingSQL renders a maturity ceiling as a SQL condition over
// alias, comparing the stored minimum age against the value bound at
// placeholder argIdx.
//
// The comparison is against the stored minimum age, never the display string:
// content_rating is free text written verbatim by whichever provider won the
// merge ("15", "FSK 16", "DE:16", "tv-ma"), and only the age makes those
// comparable with a "PG-13" ceiling. media_items.content_rating_age and
// episode_catalog_entries.content_rating_age hold the age that
// access.Normalize resolved when the rating was written.
//
// A title whose rating carries no age is governed by the server setting
// access.unrated_content, which reaches the filter as AllowUnratedContent.
func contentRatingCeilingSQL(alias string, allowUnrated bool, argIdx int) string {
	column := alias + ".content_rating_age"
	if allowUnrated {
		return fmt.Sprintf("(%s IS NULL OR %s <= $%d)", column, column, argIdx)
	}
	return fmt.Sprintf("(%s IS NOT NULL AND %s <= $%d)", column, column, argIdx)
}

// advisoryAgeLimitSQL renders an advisory-age limit as a SQL condition over
// alias. By default a title with no advisory age passes: advisory coverage is
// partial (the provider that supplies it is rate limited), so a missing
// advisory means "not looked up yet", and the content-rating ceiling alone
// decides such a title. A profile that requires an advisory age fails closed
// instead and sees only titles rated at or under the limit, the same shape
// contentRatingCeilingSQL gives an unrated title under a ceiling.
// media_items.advisory_age and its episode_catalog_entries copy hold the age.
func advisoryAgeLimitSQL(alias string, hideUnadvised bool, argIdx int) string {
	column := alias + ".advisory_age"
	if hideUnadvised {
		return fmt.Sprintf("(%s IS NOT NULL AND %s <= $%d)", column, column, argIdx)
	}
	return fmt.Sprintf("(%s IS NULL OR %s <= $%d)", column, column, argIdx)
}

// ApplyMaturityLimits appends the filter's maturity limits for alias — the
// content-rating ceiling and the advisory-age limit, ANDed — binding each limit
// as an argument. It is the single place those limits become SQL, so every
// catalog read, including query builders outside this package, enforces the
// same predicates. alias must expose content_rating_age and advisory_age
// (media_items, or the episode_catalog_entries read model). A filter with no
// limits appends nothing.
func ApplyMaturityLimits(alias string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	limits := filter.MaturityLimits
	// access.HasCeiling, not a trimmed emptiness test: a stored " " is a set
	// ceiling nothing resolves under, so it falls through to the fail-closed
	// branch below instead of silently lifting the ceiling.
	if access.HasCeiling(limits.MaxContentRating) {
		ceilingAge, ok := access.AgeForCeiling(limits.MaxContentRating)
		if !ok {
			// A ceiling that resolves to no age (unrated, or unrecognized) is
			// unusable, and an unusable parental control fails closed.
			*conditions = append(*conditions, "1 = 0")
			return
		}
		*conditions = append(*conditions, contentRatingCeilingSQL(alias, limits.AllowUnratedContent, *argIdx))
		*args = append(*args, *ceilingAge)
		*argIdx = *argIdx + 1
	}
	if limits.MaxAdvisoryAge > 0 {
		*conditions = append(*conditions, advisoryAgeLimitSQL(alias, limits.HidesUnadvised(), *argIdx))
		*args = append(*args, limits.MaxAdvisoryAge)
		*argIdx = *argIdx + 1
	}
}

// libraryAccessConditions returns the per-item library allow/deny predicates
// gating keyColumn's membership rows in media_item_libraries (e.g.
// "mi.content_id", or "e.series_id" for episode access resolved through the
// parent series). allowedIdx and disabledIdx are 1-based SQL placeholder
// positions for the allowed/disabled folder-ID arrays; 0 means that restriction
// is not active.
//
// The predicates are independent EXISTS / NOT EXISTS subqueries — never
// allow/deny checks against one joined membership row. The single-join form
// leaks: an item linked to BOTH a passing library and a disabled one satisfies
// the predicates via the passing row (audit 2026-05-01 §3.3; review finding C3).
//
// When only a disabled list is active, positive membership is still required:
// orphan items (no media_item_libraries link — mid-scan, stale rows from a
// removed library, or metadata-refresh inserts not yet linked) must not become
// visible to a restricted viewer through a vacuous NOT EXISTS. When an allowed
// list is active it already implies membership, so no extra EXISTS is added.
func libraryAccessConditions(keyColumn string, allowedIdx, disabledIdx int) []string {
	var conditions []string
	if allowedIdx > 0 {
		conditions = append(conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s AND mil.media_folder_id = ANY($%d))",
			keyColumn, allowedIdx))
	}
	if disabledIdx > 0 {
		if allowedIdx == 0 {
			conditions = append(conditions, fmt.Sprintf(
				"EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s)",
				keyColumn))
		}
		conditions = append(conditions, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s AND mil.media_folder_id = ANY($%d))",
			keyColumn, disabledIdx))
	}
	return conditions
}

// appendLibraryAccessConditions binds the filter's library restrictions as args
// and appends the matching libraryAccessConditions predicates for keyColumn.
func appendLibraryAccessConditions(keyColumn string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	var allowedIdx, disabledIdx int
	if filter.AllowedLibraryIDs != nil {
		*args = append(*args, filter.AllowedLibraryIDs)
		allowedIdx = *argIdx
		*argIdx = *argIdx + 1
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		*args = append(*args, filter.DisabledLibraryIDs)
		disabledIdx = *argIdx
		*argIdx = *argIdx + 1
	}
	*conditions = append(*conditions, libraryAccessConditions(keyColumn, allowedIdx, disabledIdx)...)
}

// ApplyLibraryAccessFilter appends the canonical item-level library allow/deny
// predicates for keyColumn. It is exported for query builders outside the
// catalog package that must enforce the same dual-membership invariant.
func ApplyLibraryAccessFilter(keyColumn string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	appendLibraryAccessConditions(keyColumn, filter, conditions, args, argIdx)
}

// episodeParentSeriesIDExpr resolves an episode row to its parent series for
// listing queries that do not already project series_id. The subquery is a PK
// lookup on episodes.content_id.
func episodeParentSeriesIDExpr(episodeIDExpr string) string {
	return fmt.Sprintf("(SELECT e_parent.series_id FROM episodes e_parent WHERE e_parent.content_id = %s)", episodeIDExpr)
}

// appendEpisodeParentLibraryAccess applies the series-level dual-membership
// predicates that detail and playback already enforce via
// EnsureAccessible(series_id). Episode file membership (episode_libraries) can
// diverge from series membership for multi-folder shows; listing without this
// check surfaces episodes of a globally hidden series as unopenable tiles.
// Callers that already join episodes or a read model should pass the projected
// series ID directly to avoid a redundant correlated lookup.
func appendEpisodeParentLibraryAccess(seriesIDExpr string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	appendLibraryAccessConditions(seriesIDExpr, filter, conditions, args, argIdx)
}

func appendEpisodeParentLibraryAccessByEpisodeID(episodeIDExpr string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	appendEpisodeParentLibraryAccess(episodeParentSeriesIDExpr(episodeIDExpr), filter, conditions, args, argIdx)
}

// ApplySectionAccessFilter applies non-library access constraints to section queries.
func ApplySectionAccessFilter(alias string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	applyAccessFilter(alias, filter, conditions, args, argIdx)
}

// FileAllowedByAccess reports whether a media file fits within the viewer's
// effective access policy.
func FileAllowedByAccess(file *models.MediaFile, filter AccessFilter) bool {
	if file == nil {
		return false
	}
	if filter.AllowedLibraryIDs != nil && !intInSlice(file.MediaFolderID, filter.AllowedLibraryIDs) {
		return false
	}
	if len(filter.DisabledLibraryIDs) > 0 && intInSlice(file.MediaFolderID, filter.DisabledLibraryIDs) {
		return false
	}
	return access.QualityAllowed(file.Resolution, filter.MaxPlaybackQuality)
}

func intInSlice(value int, values []int) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// FilterMediaFilesByAccess drops file versions the viewer cannot access —
// the FileAllowedByAccess predicate — and, when the read opted in through
// ScopeFilesToLibrary, versions stored outside the presentation library. The
// library scope is Go-only: MediaFileAccessSQL does not mirror it because no
// SQL caller sets a presentation library.
func FilterMediaFilesByAccess(files []*models.MediaFile, filter AccessFilter) []*models.MediaFile {
	scopeLibrary := filter.ScopeFilesToLibrary && filter.PresentationLibraryID != nil
	unrestricted := filter.AllowedLibraryIDs == nil &&
		!scopeLibrary &&
		len(filter.DisabledLibraryIDs) == 0 &&
		strings.TrimSpace(filter.MaxPlaybackQuality) == ""
	if len(files) == 0 || unrestricted {
		return files
	}

	filtered := make([]*models.MediaFile, 0, len(files))
	for _, file := range files {
		if FileAllowedByAccess(file, filter) &&
			(!scopeLibrary || file.MediaFolderID == *filter.PresentationLibraryID) {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

// SQLTrimSpaceChars is a PostgreSQL E-string holding exactly the runes Go's
// strings.TrimSpace strips: ASCII whitespace plus every rune with the Unicode
// White_Space property. Pass it as BTRIM's second argument wherever SQL has to
// trim a value the way Go would, so a resolution such as "\u00a02160p" ranks
// the same on both sides instead of falling through to the ELSE branch.
//
// Use octal \013 for vertical tab; PostgreSQL treats \v as a literal v. The
// \uXXXX escapes need a UTF-8 database, which Silo requires anyway.
const SQLTrimSpaceChars = `E' \t\n\013\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004` +
	`\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000'`

// MediaFileQualityCeilingSQL renders the playback-quality ceiling as a SQL
// condition over the given media_files alias, or "" when the filter sets no
// ceiling. It mirrors access.QualityAllowed, trimming whitespace the way Go
// does (see SQLTrimSpaceChars).
func MediaFileQualityCeilingSQL(alias string, maxPlaybackQuality string) string {
	quality := access.NormalizePlaybackQuality(maxPlaybackQuality)
	if quality == "" {
		return ""
	}
	maxRank := 3
	if quality == access.PlaybackQuality4K {
		maxRank = 4
	}
	return fmt.Sprintf(`CASE UPPER(BTRIM(COALESCE(%s.resolution, ''), %s))
		WHEN '480P' THEN 1 WHEN '720P' THEN 2 WHEN '1080P' THEN 3
		WHEN '2160P' THEN 4 WHEN '4320P' THEN 5 ELSE 0 END <= %d`, alias, SQLTrimSpaceChars, maxRank)
}

// MediaFileAccessSQL renders FileAllowedByAccess as SQL conditions over the
// given media_files alias, appending any bind values to args and numbering
// placeholders from the resulting argument positions. Callers must append the
// returned args in order.
//
// This is the SQL mirror of FileAllowedByAccess and must stay in step with it:
// it exists so queries that would otherwise ship every candidate file to Go can
// filter and reduce inside PostgreSQL instead.
func MediaFileAccessSQL(alias string, filter AccessFilter, args []any) ([]string, []any) {
	conditions := mediaFileAccessConditions(alias, filter, func(value any) int {
		args = append(args, value)
		return len(args)
	})
	return conditions, args
}

// mediaFileAccessConditions is MediaFileAccessSQL for callers that number
// placeholders themselves: bind records a value and returns its position.
func mediaFileAccessConditions(alias string, filter AccessFilter, bind func(any) int) []string {
	conditions := make([]string, 0, 3)
	if filter.AllowedLibraryIDs != nil {
		conditions = append(conditions, fmt.Sprintf("%s.media_folder_id = ANY($%d)", alias, bind(filter.AllowedLibraryIDs)))
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("NOT (%s.media_folder_id = ANY($%d))", alias, bind(filter.DisabledLibraryIDs)))
	}
	if ceiling := MediaFileQualityCeilingSQL(alias, filter.MaxPlaybackQuality); ceiling != "" {
		conditions = append(conditions, ceiling)
	}
	return conditions
}
