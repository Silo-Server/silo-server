package sections

import (
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestEffectiveFetchLibraryIDsUsesAllowedLibraries(t *testing.T) {
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{7, 8}}

	got := effectiveFetchLibraryIDs(nil, filter)

	if !slices.Equal(got, []int{7, 8}) {
		t.Fatalf("effective library IDs = %#v, want [7 8]", got)
	}
}

func TestEffectiveFetchLibraryIDsKeepsExplicitScope(t *testing.T) {
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{7, 8}}

	got := effectiveFetchLibraryIDs([]int{3}, filter)

	if !slices.Equal(got, []int{3}) {
		t.Fatalf("effective library IDs = %#v, want [3]", got)
	}
}

func TestEffectiveFetchLibraryIDsPreservesEmptyAllowedScope(t *testing.T) {
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{}}

	got := effectiveFetchLibraryIDs(nil, filter)

	if got == nil {
		t.Fatalf("effective library IDs = nil, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("effective library IDs = %#v, want empty slice", got)
	}
}

func TestApplyEpisodeTargetLibraryAccessRejectsAnyDisabledSeriesMembership(t *testing.T) {
	var conditions []string
	var args []any
	argIdx := 2

	ok := applyEpisodeTargetLibraryAccess(
		catalog.AccessFilter{DisabledLibraryIDs: []int{9}},
		nil,
		nil,
		&conditions,
		&args,
		&argIdx,
	)
	if !ok {
		t.Fatal("disabled-only access unexpectedly returned an empty scope")
	}

	where := strings.Join(conditions, " AND ")
	if !strings.Contains(where, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = si.content_id)") {
		t.Fatalf("episode hydration must require positive series membership, got %s", where)
	}
	if !strings.Contains(where, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = si.content_id AND mil.media_folder_id = ANY($2))") {
		t.Fatalf("episode hydration must reject any disabled series membership, got %s", where)
	}
	if len(args) != 1 || !slices.Equal(args[0].([]int), []int{9}) || argIdx != 3 {
		t.Fatalf("args = %v, argIdx = %d; want disabled list at $2 and next index 3", args, argIdx)
	}
}

func TestApplyEpisodeTargetLibraryAccessComposesAllowedAndDisabledMembership(t *testing.T) {
	var conditions []string
	var args []any
	argIdx := 1

	ok := applyEpisodeTargetLibraryAccess(
		catalog.AccessFilter{AllowedLibraryIDs: []int{7}, DisabledLibraryIDs: []int{9}},
		nil,
		nil,
		&conditions,
		&args,
		&argIdx,
	)
	if !ok {
		t.Fatal("allowed library unexpectedly returned an empty scope")
	}

	where := strings.Join(conditions, " AND ")
	if !strings.Contains(where, "mil.media_folder_id = ANY($1)") {
		t.Fatalf("episode hydration must require allowed series membership, got %s", where)
	}
	if !strings.Contains(where, "NOT EXISTS") || !strings.Contains(where, "mil.media_folder_id = ANY($2)") {
		t.Fatalf("episode hydration must independently reject disabled series membership, got %s", where)
	}
	if len(args) != 2 || argIdx != 3 {
		t.Fatalf("args = %v, argIdx = %d; want allowed and disabled lists with next index 3", args, argIdx)
	}
}
