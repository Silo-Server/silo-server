package metadata

import (
	"strings"
	"testing"
	"time"
)

func TestBetterCanonicalCandidatePrefersActiveFilesThenLibrariesThenAgeThenID(t *testing.T) {
	now := time.Now()
	base := canonicalCandidate{ContentID: "b", CreatedAt: now, ActiveFiles: 0, LibraryCount: 2}

	if !betterCanonicalCandidate(canonicalCandidate{ContentID: "a", CreatedAt: now, ActiveFiles: 1, LibraryCount: 0}, base) {
		t.Fatal("expected active files to win over library count")
	}
	if !betterCanonicalCandidate(canonicalCandidate{ContentID: "a", CreatedAt: now, ActiveFiles: 0, LibraryCount: 3}, base) {
		t.Fatal("expected larger library count to win")
	}
	if !betterCanonicalCandidate(canonicalCandidate{ContentID: "a", CreatedAt: now.Add(-time.Hour), ActiveFiles: 0, LibraryCount: 2}, base) {
		t.Fatal("expected older created_at to win")
	}
	if !betterCanonicalCandidate(canonicalCandidate{ContentID: "a", CreatedAt: now, ActiveFiles: 0, LibraryCount: 2}, base) {
		t.Fatal("expected lexicographically smaller content_id to win")
	}
}

func TestProviderEntriesFromDriftUsesDurableProvidersOnly(t *testing.T) {
	entries := providerEntriesFromDrift(providerIDDriftRow{
		ContentID: "item-1",
		ItemType:  "movie",
		TMDBID:    " 603 ",
		TVDBID:    "",
		IMDbID:    "tt0083658",
	})
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0] != (providerIDEntry{Provider: "tmdb", ProviderID: "603"}) {
		t.Fatalf("entries[0] = %#v", entries[0])
	}
	if entries[1] != (providerIDEntry{Provider: "imdb", ProviderID: "tt0083658"}) {
		t.Fatalf("entries[1] = %#v", entries[1])
	}
}

func TestMaxPlaceholder(t *testing.T) {
	// maxPlaceholder must read the FULL placeholder number, not a substring:
	// "$20" is placeholder 20, never "$2". This is the property the old
	// strings.Contains(sql, "$2") heuristic got wrong.
	cases := []struct {
		sql  string
		want int
	}{
		{`DELETE FROM media_item_provider_ids WHERE content_id = $1`, 1},
		{`UPDATE media_files SET content_id = $2 WHERE content_id = $1`, 2},
		{`INSERT INTO t SELECT $2 FROM s WHERE content_id = $1`, 2},
		{`SELECT $1, $2, $1`, 2}, // repeated placeholders count once
		{`SELECT $20 FROM t`, 20},
		{`SELECT $12 FROM t`, 12},
		{`DELETE FROM t WHERE x = 'literal'`, 0},
	}
	for _, tc := range cases {
		if got := maxPlaceholder(tc.sql); got != tc.want {
			t.Errorf("maxPlaceholder(%q) = %d, want %d", tc.sql, got, tc.want)
		}
	}
}

func TestMergeStepArgsMatchesPlaceholderArity(t *testing.T) {
	const sourceID = "src"
	const canonicalID = "canon"

	// A step that only references $1 must receive exactly one argument.
	// Passing canonicalID as an unused $2 makes pgx reject the Exec with
	// "mismatched param and argument count" under the default
	// QueryExecModeCacheStatement, aborting the whole merge transaction.
	onlySource := mergeStepArgs(`DELETE FROM media_item_provider_ids WHERE content_id = $1`, sourceID, canonicalID)
	if len(onlySource) != 1 || onlySource[0] != sourceID {
		t.Fatalf("mergeStepArgs($1-only) = %#v, want [%q]", onlySource, sourceID)
	}

	// A step that references $2 must receive both arguments in order.
	both := mergeStepArgs(`UPDATE media_files SET content_id = $2 WHERE content_id = $1`, sourceID, canonicalID)
	if len(both) != 2 || both[0] != sourceID || both[1] != canonicalID {
		t.Fatalf("mergeStepArgs($1+$2) = %#v, want [%q %q]", both, sourceID, canonicalID)
	}

	// $2 may appear before $1 (e.g. INSERT ... SELECT $2 ...). Args are still
	// positional [source, canonical] regardless of textual order.
	reversed := mergeStepArgs(`INSERT INTO t SELECT $2 FROM s WHERE content_id = $1`, sourceID, canonicalID)
	if len(reversed) != 2 || reversed[0] != sourceID || reversed[1] != canonicalID {
		t.Fatalf("mergeStepArgs(reversed) = %#v, want [%q %q]", reversed, sourceID, canonicalID)
	}
}

func TestMergeStepPlaceholdersAreBounded(t *testing.T) {
	// Every merge step must bind $1 (the source) and may bind $2 (the
	// canonical). A step binding $3+ (or none) cannot be satisfied by the two
	// IDs the loop passes and would abort the merge at runtime; this guard —
	// checking the actual placeholder bound rather than re-deriving the arg
	// count from the same predicate the helper uses — catches such a step.
	for _, step := range mediaItemMergeSteps {
		n := maxPlaceholder(step.sql)
		if n < 1 || n > 2 {
			t.Errorf("merge step %q binds %d placeholders; merge supports only $1 and $2", step.name, n)
		}
		if !strings.Contains(step.sql, "$1") {
			t.Errorf("merge step %q does not bind $1 (source)", step.name)
		}
	}
}

func TestMergeStepsPreserveEbookReaderProgress(t *testing.T) {
	var hasMerge bool
	var hasDelete bool
	for _, step := range mediaItemMergeSteps {
		if strings.Contains(step.sql, "ebook_reader_progress") && strings.Contains(step.sql, "INSERT INTO") {
			hasMerge = true
		}
		if strings.Contains(step.sql, "ebook_reader_progress") && strings.Contains(step.sql, "DELETE FROM") {
			hasDelete = true
		}
	}
	if !hasMerge {
		t.Fatal("media item merge steps should merge ebook_reader_progress rows")
	}
	if !hasDelete {
		t.Fatal("media item merge steps should delete source ebook_reader_progress rows")
	}
}

func TestBothCandidatesHaveContentDetectsRealUserData(t *testing.T) {
	withFiles := canonicalCandidate{ContentID: "a", ActiveFiles: 2}
	withLibraries := canonicalCandidate{ContentID: "b", LibraryCount: 1}
	skeleton := canonicalCandidate{ContentID: "c"}

	if !bothCandidatesHaveContent(withFiles, withLibraries) {
		t.Fatal("expected both-content when one has files and the other has library memberships")
	}
	if !bothCandidatesHaveContent(withFiles, withFiles) {
		t.Fatal("expected both-content when both sides have files")
	}
	if bothCandidatesHaveContent(withFiles, skeleton) {
		t.Fatal("did not expect both-content when one side is a skeleton")
	}
	if bothCandidatesHaveContent(skeleton, skeleton) {
		t.Fatal("did not expect both-content when both sides are skeletons")
	}
}
