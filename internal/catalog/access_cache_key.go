package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// WriteAccessScopeCacheKey appends every AccessFilter field that bounds WHICH
// rows a viewer may see to a cache key. It is the single, security-critical
// source of truth for serializing an access scope: every cache that shares
// entries across requests (sections resolved-list cache, editorial candidate
// cache, audiobook groups cache) MUST build its access-boundary key component
// through this method, so a new boundary field added to AccessFilter only has
// to be captured here — never in per-cache copies that can silently drift.
//
// Included: AllowedLibraryIDs, DisabledLibraryIDs, MaxContentRating,
// ExcludedMediaTypes, NamePrefix, AllowedContentIDs. AllowedLibraryIDs and
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
	b.WriteString(f.MaxContentRating)

	b.WriteString("|excludedtypes=")
	writeSortedStringsKey(b, f.ExcludedMediaTypes)

	b.WriteString("|nameprefix=")
	b.WriteString(f.NamePrefix)

	b.WriteString("|allowedcontent=")
	b.WriteString(hashOptionalStringsKey(f.AllowedContentIDs))
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
