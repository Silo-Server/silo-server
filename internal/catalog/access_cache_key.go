package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
)

// WriteAccessScopeCacheKey appends every AccessFilter field that bounds WHICH
// rows a viewer may see to a cache key. It is the single, security-critical
// source of truth for serializing an access scope: every cache that shares
// entries across requests (sections resolved-list cache, editorial candidate
// cache, audiobook groups cache) MUST build its access-boundary key component
// through this method, so a new boundary field added to AccessFilter only has
// to be captured here — never in per-cache copies that can silently drift.
//
// Included: AllowedLibraryIDs, DisabledLibraryIDs, every MaturityLimits field
// (MaxContentRating, AllowUnratedContent, MaxAdvisoryAge, RequireAdvisoryAge),
// ExcludedMediaTypes,
// NamePrefix, AllowedContentIDs. AllowedLibraryIDs and
// AllowedContentIDs preserve the nil (unrestricted) vs empty (restrict to
// nothing) distinction the access layer branches on; AllowedContentIDs is
// hashed because the allow-list can be large.
//
// Deliberately excluded: UserID/ProfileID (identity, not scope — callers that
// key per-viewer entries add them separately), presentation/language fields
// (they change how rows are rendered, not which rows are visible), and
// playback-quality/file fields (file-level, not row-level). A cache whose
// stored value embeds rendered or per-profile state must add those fields
// itself on top of this scope component.
func (f AccessFilter) WriteAccessScopeCacheKey(b *strings.Builder) {
	b.WriteString("|accessible=")
	writeOptionalSortedIntsKey(b, f.AllowedLibraryIDs)

	b.WriteString("|disabled=")
	writeSortedIntsKey(b, f.DisabledLibraryIDs)

	b.WriteString("|rating=")
	b.WriteString(contentRatingCeilingCacheKey(f.MaxContentRating))

	// Part of the ceiling, not a separate preference: flipping
	// access.unrated_content changes which rows the same ceiling admits, so a
	// cached list warmed under one value must not be served under the other.
	b.WriteString("|unrated=")
	b.WriteString(strconv.FormatBool(f.AllowUnratedContent))

	// 0 is "no limit", the only value ApplyMaturityLimits skips.
	b.WriteString("|advisory=")
	b.WriteString(strconv.Itoa(f.MaxAdvisoryAge))

	// Keyed raw, like the unrated setting, not as HidesUnadvised: that can
	// only split an entry, never merge two scopes that render different SQL,
	// and it keeps every field independently visible to the drift guard in
	// access_cache_key_test.go. The resolvers already fold the flag to false
	// without a limit, so the extra split does not arise in practice.
	b.WriteString("|requireadvisory=")
	b.WriteString(strconv.FormatBool(f.RequireAdvisoryAge))

	b.WriteString("|excludedtypes=")
	writeSortedStringsKey(b, f.ExcludedMediaTypes)

	b.WriteString("|nameprefix=")
	b.WriteString(f.NamePrefix)

	b.WriteString("|allowedcontent=")
	b.WriteString(hashOptionalStringsKey(f.AllowedContentIDs))
}

// contentRatingCeilingCacheKey reduces a maturity ceiling to the three states
// ApplyMaturityLimits actually branches on: no ceiling, a ceiling that
// resolves to no age (deny everything), or a resolved age. Two filters that
// agree here always produce the same ceiling predicate.
//
// Keying the resolved state rather than the raw string is what BOUNDS the
// caches this feeds. MaxContentRating is not always a value Silo wrote: the
// jellycompat browse paths fold a client's MaxOfficialRating into it, and that
// is free text. "PG-13", "pg13", "PG 13", "US:PG-13" and "+13" all render the
// same SQL, so keying the string would let one client mint an unbounded number
// of process-global entries that each hold the same list.
func contentRatingCeilingCacheKey(ceiling string) string {
	if !access.HasCeiling(ceiling) {
		return "none"
	}
	age, ok := access.AgeForCeiling(ceiling)
	if !ok {
		return "blocked"
	}
	return "age" + strconv.Itoa(*age)
}

// writeOptionalSortedIntsKey encodes an int set preserving the nil vs empty
// distinction.
func writeOptionalSortedIntsKey(b *strings.Builder, values []int) {
	if values == nil {
		b.WriteString("<nil>")
		return
	}
	if len(values) == 0 {
		b.WriteString("<empty>")
		return
	}
	writeSortedIntsKey(b, values)
}

func writeSortedIntsKey(b *strings.Builder, values []int) {
	if len(values) == 0 {
		return
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	for i, value := range sorted {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(value))
	}
}

func writeSortedStringsKey(b *strings.Builder, values []string) {
	if len(values) == 0 {
		return
	}
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	for i, value := range sorted {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(value)
	}
}

// hashOptionalStringsKey returns a bounded, order-independent digest of a
// string allow-list, preserving the nil (unrestricted) vs empty (restrict to
// nothing) distinction.
func hashOptionalStringsKey(ids []string) string {
	if ids == nil {
		return "<nil>"
	}
	if len(ids) == 0 {
		return "<empty>"
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(sum[:])
}
