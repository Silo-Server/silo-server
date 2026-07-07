package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMergeVideosAccumulatesAndDedupes(t *testing.T) {
	target := &MetadataResult{Videos: []RemoteVideo{
		{Provider: "tmdb", ProviderKey: "a1", Kind: models.ExtraKindTrailer, Site: "youtube", SiteKey: "yt-1"},
	}}
	source := &MetadataResult{Videos: []RemoteVideo{
		// Same provider video id — dropped.
		{Provider: "tmdb", ProviderKey: "a1", Kind: models.ExtraKindTrailer, Site: "youtube", SiteKey: "yt-1"},
		// Different provider, same hosted video — dropped.
		{Provider: "tvdb", ProviderKey: "b7", Kind: models.ExtraKindTrailer, Site: "YouTube", SiteKey: "yt-1"},
		// Genuinely new — kept.
		{Provider: "tvdb", ProviderKey: "b8", Kind: models.ExtraKindClip, Site: "youtube", SiteKey: "yt-2"},
	}}

	MergeMetadata(source, target, nil, MergeFillEmpty)

	if len(target.Videos) != 2 {
		t.Fatalf("got %d videos, want 2: %+v", len(target.Videos), target.Videos)
	}
	if target.Videos[1].ProviderKey != "b8" {
		t.Fatalf("expected new video appended, got %+v", target.Videos[1])
	}
}

func TestMergeVideosRespectsFieldLock(t *testing.T) {
	target := &MetadataResult{}
	source := &MetadataResult{Videos: []RemoteVideo{
		{Provider: "tmdb", ProviderKey: "a1", Kind: models.ExtraKindTrailer, Site: "youtube", SiteKey: "yt-1"},
	}}

	MergeMetadata(source, target, []MetadataField{FieldVideos}, MergeReplaceUnlocked)

	if len(target.Videos) != 0 {
		t.Fatalf("locked FieldVideos must block merge, got %+v", target.Videos)
	}
}

func TestFilterVideosByKinds(t *testing.T) {
	videos := []RemoteVideo{
		{ProviderKey: "1", Kind: models.ExtraKindTrailer},
		{ProviderKey: "2", Kind: models.ExtraKindBloopers},
	}
	// nil allow-list = everything.
	if got := filterVideosByKinds(videos, nil); len(got) != 2 {
		t.Fatalf("nil allow-list should pass all, got %d", len(got))
	}
	// Empty allow-list = nothing (remote videos disabled).
	if got := filterVideosByKinds(videos, map[models.ExtraKind]bool{}); len(got) != 0 {
		t.Fatalf("empty allow-list should drop all, got %d", len(got))
	}
	allowed := map[models.ExtraKind]bool{models.ExtraKindTrailer: true}
	got := filterVideosByKinds(videos, allowed)
	if len(got) != 1 || got[0].Kind != models.ExtraKindTrailer {
		t.Fatalf("kind filter failed: %+v", got)
	}
}

func TestItemVideosFromRemoteOrdering(t *testing.T) {
	rows := itemVideosFromRemote("movie-tmdb-1", []RemoteVideo{
		{ProviderKey: "c", Kind: models.ExtraKindClip},
		{ProviderKey: "t2", Kind: models.ExtraKindTrailer, IsOfficial: false},
		{ProviderKey: "t1", Kind: models.ExtraKindTrailer, IsOfficial: true},
	})
	if len(rows) != 3 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].ProviderKey != "t1" || rows[1].ProviderKey != "t2" || rows[2].ProviderKey != "c" {
		t.Fatalf("unexpected order: %s, %s, %s", rows[0].ProviderKey, rows[1].ProviderKey, rows[2].ProviderKey)
	}
	for i, row := range rows {
		if row.SortOrder != i {
			t.Fatalf("row %d has sort order %d", i, row.SortOrder)
		}
	}
}
