package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// infuseCollectionQuery is the query Infuse sends when opening a collection.
const infuseCollectionQuery = "excludeLocationTypes=Virtual" +
	"&fields=DateCreated,Etag,Genres,MediaSources,AlternateMediaSources,Overview,ParentId,Path,ProviderIds,SortName,RecursiveItemCount,ChildCount" +
	"&limit=50&startIndex=0"

// detailLibrariesContentService serves a fixed library list and a fixed
// detail per content ID.
type detailLibrariesContentService struct {
	librariesContentService
	details     map[string]*upstreamItemDetail
	detailCalls int
}

func (s *detailLibrariesContentService) GetItemDetailsByIDs(_ context.Context, _ *Session, contentIDs []string, _ *int) (map[string]*upstreamItemDetail, error) {
	s.detailCalls++
	out := make(map[string]*upstreamItemDetail, len(contentIDs))
	for _, id := range contentIDs {
		if detail, ok := s.details[id]; ok {
			d := *detail
			out[id] = &d
		}
	}
	return out, nil
}

func playableDetail(contentID, itemType string, fileID int) *upstreamItemDetail {
	return &upstreamItemDetail{
		ContentID: contentID,
		Type:      itemType,
		Title:     contentID,
		Versions: []catalog.FileVersion{{
			FileID:    fileID,
			FilePath:  "/media/" + contentID + ".mkv",
			Container: "mkv",
		}},
	}
}

func newDetailCollectionsHandler(t *testing.T, collections *fakeCollectionSource, itemRepo itemRepoForBatchLoader, details map[string]*upstreamItemDetail) (*ItemsHandler, *detailLibrariesContentService) {
	t.Helper()
	h := newCollectionsTestHandler(collections, nil, itemRepo)
	content := &detailLibrariesContentService{
		librariesContentService: librariesContentService{
			libraries: []upstreamUserLibrary{{ID: 1, Name: "Media", Type: "movies"}},
		},
		details: details,
	}
	h.content = content
	return h, content
}

// newSmartEpisodeFixture builds an episode-scoped smart collection whose query
// returns episodes in the given order. Every episode belongs to series s-1
// ("Show"), has a file, and has a playable detail.
func newSmartEpisodeFixture(t *testing.T, collectionID string, episodes ...*models.Episode) (*ItemsHandler, *fakeSmartExecutor, *detailLibrariesContentService, string) {
	t.Helper()
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: collectionID, LibraryID: 1, Title: "Episodes", Visibility: "visible", CollectionType: "smart", ItemCount: len(episodes), QueryDefinition: json.RawMessage(`{"media_scope":"episode"}`)},
		},
	}
	exec := &fakeSmartExecutor{}
	episodeRepo := &countingEpisodeRepo{episodesByID: map[string]*models.Episode{}, hasFilesByID: map[string]bool{}}
	details := map[string]*upstreamItemDetail{}
	for i, episode := range episodes {
		episode.SeriesID = "s-1"
		exec.items = append(exec.items, &models.MediaItem{ContentID: episode.ContentID, Type: "episode", Title: episode.Title})
		episodeRepo.episodesByID[episode.ContentID] = episode
		episodeRepo.hasFilesByID[episode.ContentID] = true
		detail := playableDetail(episode.ContentID, "episode", i+1)
		detail.Title = episode.Title
		detail.SeriesID = "s-1"
		detail.SeriesTitle = "Show"
		details[episode.ContentID] = detail
	}
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{
		"s-1": {ContentID: "s-1", Type: "series", Title: "Show"},
	}}
	h, content := newDetailCollectionsHandler(t, collections, itemRepo, details)
	h.queryExecutor = exec
	h.episodeRepo = episodeRepo
	return h, exec, content, h.codec.EncodeStringID(EncodedIDCollection, collectionID)
}

// Infuse opens a collection with Fields=MediaSources,Path and treats children
// without them as unplayable ("Empty Folder"). BoxSet children must get the
// same detail upgrade the library-parent browse path applies.
func TestHandleItems_BoxSetMovieChildrenHonorDetailFields(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible"},
		},
		items: map[string][]*models.LibraryCollectionItem{
			"101": {
				{CollectionID: "101", MediaItemID: "m-2", Position: 0},
				{CollectionID: "101", MediaItemID: "m-1", Position: 1},
			},
		},
	}
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{
		"m-1": {ContentID: "m-1", Type: "movie", Title: "Iron Man"},
		"m-2": {ContentID: "m-2", Type: "movie", Title: "Captain America"},
	}}
	h, content := newDetailCollectionsHandler(t, collections, itemRepo, map[string]*upstreamItemDetail{
		"m-1": playableDetail("m-1", "movie", 1),
		"m-2": playableDetail("m-2", "movie", 2),
	})

	parentID := h.codec.EncodeStringID(EncodedIDCollection, "101")
	result := performItemsRequest(t, h, "/Items?"+infuseCollectionQuery+"&parentId="+parentID)

	if len(result.Items) != 2 {
		t.Fatalf("expected 2 children, got %d", len(result.Items))
	}
	if content.detailCalls != 1 {
		t.Fatalf("expected one batched detail fetch, got %d", content.detailCalls)
	}
	for i, wantID := range []string{"m-2", "m-1"} {
		item := result.Items[i]
		if item.ParentID != parentID {
			t.Fatalf("item %d: expected ParentId %s, got %s", i, parentID, item.ParentID)
		}
		if len(item.MediaSources) != 1 {
			t.Fatalf("item %d: expected 1 MediaSource, got %d", i, len(item.MediaSources))
		}
		if wantPath := "/media/" + wantID + ".mkv"; item.Path != wantPath {
			t.Fatalf("item %d: expected Path %q, got %q", i, wantPath, item.Path)
		}
	}
}

// A TV collection holds series. The detail upgrade must keep them browsable
// folders with their season counts, as the library-parent listing does.
func TestHandleItems_BoxSetSeriesChildrenHonorDetailFields(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "102", LibraryID: 1, Title: "Star Trek", Visibility: "visible"},
		},
		items: map[string][]*models.LibraryCollectionItem{
			"102": {{CollectionID: "102", MediaItemID: "s-1", Position: 0}},
		},
	}
	seasons := 7
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{
		"s-1": {ContentID: "s-1", Type: "series", Title: "Deep Space Nine", SeasonCount: &seasons},
	}}
	h, content := newDetailCollectionsHandler(t, collections, itemRepo, map[string]*upstreamItemDetail{
		"s-1": {ContentID: "s-1", Type: "series", Title: "Deep Space Nine", SeasonCount: &seasons},
	})

	parentID := h.codec.EncodeStringID(EncodedIDCollection, "102")
	result := performItemsRequest(t, h, "/Items?"+infuseCollectionQuery+"&parentId="+parentID)

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 child, got %d", len(result.Items))
	}
	if content.detailCalls != 1 {
		t.Fatalf("expected one batched detail fetch, got %d", content.detailCalls)
	}
	item := result.Items[0]
	if item.Type != "Series" || !item.IsFolder || item.ChildCount != seasons {
		t.Fatalf("expected a Series folder with %d children, got Type=%q IsFolder=%v ChildCount=%d", seasons, item.Type, item.IsFolder, item.ChildCount)
	}
	if item.ParentID != parentID {
		t.Fatalf("expected ParentId %s, got %s", parentID, item.ParentID)
	}
}

// Episode members come from episode-scoped smart collections and live in the
// episodes table, not media_items. They must resolve (not silently drop to an
// empty page), carry their series and season context, and honor detail Fields.
func TestHandleItems_BoxSetEpisodeChildrenResolveWithDetailFields(t *testing.T) {
	h, _, content, parentID := newSmartEpisodeFixture(t, "203",
		&models.Episode{ContentID: "e-2", SeasonID: "season-4", SeasonNumber: 4, EpisodeNumber: 3, Title: "The Visitor"},
		&models.Episode{ContentID: "e-1", SeasonID: "season-1", SeasonNumber: 1, EpisodeNumber: 1, Title: "Emissary"},
	)

	// List-level request: episodes resolve in the smart query's order.
	listResult := performItemsRequest(t, h, "/Items?parentId="+parentID)
	assertNames(t, listResult.Items, "The Visitor", "Emissary")
	if listResult.TotalRecordCount != 2 {
		t.Fatalf("expected TotalRecordCount 2, got %d", listResult.TotalRecordCount)
	}
	if content.detailCalls != 0 {
		t.Fatalf("expected no detail fetch without detail Fields, got %d", content.detailCalls)
	}

	// Infuse's request: episodes are playable and keep their context.
	result := performItemsRequest(t, h, "/Items?"+infuseCollectionQuery+"&parentId="+parentID)
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 episode children, got %d", len(result.Items))
	}
	seriesID := h.codec.EncodeStringID(EncodedIDItem, "s-1")
	for i, want := range []struct{ id, seasonID string }{{"e-2", "season-4"}, {"e-1", "season-1"}} {
		item := result.Items[i]
		if item.Type != "Episode" || item.ParentID != parentID {
			t.Fatalf("item %d: expected an Episode under %s, got Type=%q ParentId=%s", i, parentID, item.Type, item.ParentID)
		}
		if item.SeriesID != seriesID || item.SeriesName != "Show" {
			t.Fatalf("item %d: expected series %s/Show, got %s/%s", i, seriesID, item.SeriesID, item.SeriesName)
		}
		if wantSeason := h.codec.EncodeStringID(EncodedIDSeason, want.seasonID); item.SeasonID != wantSeason {
			t.Fatalf("item %d: expected SeasonId %s, got %s", i, wantSeason, item.SeasonID)
		}
		if len(item.MediaSources) != 1 {
			t.Fatalf("item %d: expected 1 MediaSource, got %d", i, len(item.MediaSources))
		}
		if wantPath := "/media/" + want.id + ".mkv"; item.Path != wantPath {
			t.Fatalf("item %d: expected Path %q, got %q", i, wantPath, item.Path)
		}
	}
}

// seriesEpisodeRepo serves whole-series episode listings for the Play all
// expansion on top of the fallback-hydration fake.
type seriesEpisodeRepo struct {
	countingEpisodeRepo
	bySeries           map[string][]*models.Episode
	listBySeriesIDsArg []string
}

func (r *seriesEpisodeRepo) ListBySeriesIDs(_ context.Context, seriesIDs []string) (map[string][]*models.Episode, error) {
	r.listBySeriesIDsArg = append([]string(nil), seriesIDs...)
	out := make(map[string][]*models.Episode, len(seriesIDs))
	for _, id := range seriesIDs {
		out[id] = r.bySeries[id]
	}
	return out, nil
}

// newPlayAllFixture builds a stored collection [movie m-1, series s-1, movie
// m-2]. Series s-1 has S1E1, S1E2, a special S0E1 and S2E1, stored out of
// order, plus S1E3 with no available file.
func newPlayAllFixture(t *testing.T) (*ItemsHandler, string) {
	t.Helper()
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "301", LibraryID: 1, Title: "Mixed", Visibility: "visible"},
		},
		items: map[string][]*models.LibraryCollectionItem{
			"301": {
				{CollectionID: "301", MediaItemID: "m-1", Position: 0},
				{CollectionID: "301", MediaItemID: "s-1", Position: 1},
				{CollectionID: "301", MediaItemID: "m-2", Position: 2},
			},
		},
	}
	seasons := 2
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{
		"m-1": {ContentID: "m-1", Type: "movie", Title: "Movie One"},
		"m-2": {ContentID: "m-2", Type: "movie", Title: "Movie Two"},
		"s-1": {ContentID: "s-1", Type: "series", Title: "Show", SeasonCount: &seasons},
	}}
	episodes := []*models.Episode{
		{ContentID: "e-201", SeriesID: "s-1", SeasonID: "sn-2", SeasonNumber: 2, EpisodeNumber: 1, Title: "S2E1"},
		{ContentID: "e-001", SeriesID: "s-1", SeasonID: "sn-0", SeasonNumber: 0, EpisodeNumber: 1, Title: "Special"},
		{ContentID: "e-102", SeriesID: "s-1", SeasonID: "sn-1", SeasonNumber: 1, EpisodeNumber: 2, Title: "S1E2"},
		{ContentID: "e-101", SeriesID: "s-1", SeasonID: "sn-1", SeasonNumber: 1, EpisodeNumber: 1, Title: "S1E1"},
		{ContentID: "e-103", SeriesID: "s-1", SeasonID: "sn-1", SeasonNumber: 1, EpisodeNumber: 3, Title: "S1E3 missing"},
	}
	episodeRepo := &seriesEpisodeRepo{
		countingEpisodeRepo: countingEpisodeRepo{
			episodesByID: map[string]*models.Episode{},
			hasFilesByID: map[string]bool{"e-201": true, "e-001": true, "e-102": true, "e-101": true},
		},
		bySeries: map[string][]*models.Episode{"s-1": episodes},
	}
	details := map[string]*upstreamItemDetail{
		"m-1": playableDetail("m-1", "movie", 1),
		"m-2": playableDetail("m-2", "movie", 2),
	}
	details["m-1"].Title = "Movie One"
	details["m-2"].Title = "Movie Two"
	for i, episode := range episodes {
		episodeRepo.episodesByID[episode.ContentID] = episode
		detail := playableDetail(episode.ContentID, "episode", 100+i)
		detail.Title = episode.Title
		detail.SeriesID = "s-1"
		detail.SeriesTitle = "Show"
		details[episode.ContentID] = detail
	}
	h, _ := newDetailCollectionsHandler(t, collections, itemRepo, details)
	h.episodeRepo = episodeRepo
	return h, h.codec.EncodeStringID(EncodedIDCollection, "301")
}

func itemNames(items []baseItemDTO) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func assertNames(t *testing.T, got []baseItemDTO, want ...string) {
	t.Helper()
	names := itemNames(got)
	if len(names) != len(want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, names)
		}
	}
}

// jellyfin-web Play all on a collection: series expand to their playable
// episodes in collection order, regular seasons first, specials last, and
// episodes without a file are skipped.
func TestHandleItems_BoxSetPlayAllExpandsSeries(t *testing.T) {
	h, parentID := newPlayAllFixture(t)
	result := performItemsRequest(t, h, "/Users/u/Items?ParentId="+parentID+
		"&Filters=IsNotFolder&Recursive=true&MediaTypes=Audio,Video&Fields=Chapters,MediaSources,Trickplay"+
		"&ExcludeLocationTypes=Virtual&EnableTotalRecordCount=false&CollapseBoxSetItems=false&Limit=300")

	assertNames(t, result.Items, "Movie One", "S1E1", "S1E2", "S2E1", "Special", "Movie Two")
	if result.TotalRecordCount != 6 {
		t.Fatalf("expected TotalRecordCount 6, got %d", result.TotalRecordCount)
	}
	seriesID := h.codec.EncodeStringID(EncodedIDItem, "s-1")
	for _, item := range result.Items {
		if item.IsFolder {
			t.Fatalf("expected only leaves, got folder %q", item.Name)
		}
		if len(item.MediaSources) != 1 {
			t.Fatalf("%q: expected 1 MediaSource, got %d", item.Name, len(item.MediaSources))
		}
		if item.Type == "Episode" && (item.SeriesID != seriesID || item.SeasonID == "") {
			t.Fatalf("%q: expected series and season context, got SeriesId=%q SeasonId=%q", item.Name, item.SeriesID, item.SeasonID)
		}
	}
}

// Android TV and Wholphin play a collection with Recursive=true and an item
// type filter instead of IsNotFolder; the filter also narrows the leaves.
func TestHandleItems_BoxSetPlayAllByItemTypes(t *testing.T) {
	h, parentID := newPlayAllFixture(t)
	both := performItemsRequest(t, h, "/Items?ParentId="+parentID+
		"&Recursive=true&IncludeItemTypes=Episode,Movie,Video&IsMissing=false&Limit=150&Fields=MediaSources,MediaStreams,Path,Chapters")
	assertNames(t, both.Items, "Movie One", "S1E1", "S1E2", "S2E1", "Special", "Movie Two")

	episodesOnly := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&Recursive=true&IncludeItemTypes=Episode")
	assertNames(t, episodesOnly.Items, "S1E1", "S1E2", "S2E1", "Special")
}

// Shuffle sends SortBy=Random: the same leaves come back in any order.
func TestHandleItems_BoxSetShuffleReturnsAllLeaves(t *testing.T) {
	h, parentID := newPlayAllFixture(t)
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID+
		"&Filters=IsNotFolder&Recursive=true&MediaTypes=Audio,Video&SortBy=Random&Limit=300")
	got := map[string]bool{}
	for _, name := range itemNames(result.Items) {
		got[name] = true
	}
	for _, want := range []string{"Movie One", "S1E1", "S1E2", "S2E1", "Special", "Movie Two"} {
		if !got[want] {
			t.Fatalf("expected shuffled leaves to include %q, got %v", want, itemNames(result.Items))
		}
	}
	if len(result.Items) != 6 || result.TotalRecordCount != 6 {
		t.Fatalf("expected 6 of 6 leaves, got %d of %d", len(result.Items), result.TotalRecordCount)
	}
}

// Paging applies to the expanded leaves, and the total counts all of them.
func TestHandleItems_BoxSetPlayAllPages(t *testing.T) {
	h, parentID := newPlayAllFixture(t)
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&Filters=IsNotFolder&Recursive=true&StartIndex=2&Limit=3")
	assertNames(t, result.Items, "S1E2", "S2E1", "Special")
	if result.TotalRecordCount != 6 || result.StartIndex != 2 {
		t.Fatalf("expected total 6 at start 2, got %d at %d", result.TotalRecordCount, result.StartIndex)
	}
}

// Recursive listings that don't ask for leaves keep the member list, so
// collection pages (Streamyfin, Kodi browse) don't fill with episodes.
func TestHandleItems_BoxSetRecursiveWithoutLeafFilterListsMembers(t *testing.T) {
	h, parentID := newPlayAllFixture(t)
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&Recursive=true")
	assertNames(t, result.Items, "Movie One", "Show", "Movie Two")
	if !result.Items[1].IsFolder {
		t.Fatal("expected the series member to stay a folder")
	}
}

// Play all on an episode-scoped smart collection pages its episodes in SQL.
func TestHandleItems_SmartEpisodeBoxSetPlayAll(t *testing.T) {
	h, exec, _, parentID := newSmartEpisodeFixture(t, "204",
		&models.Episode{ContentID: "e-2", SeasonNumber: 1, EpisodeNumber: 2, Title: "Pilot B"},
		&models.Episode{ContentID: "e-1", SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot A"},
	)
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&Filters=IsNotFolder&Recursive=true&Fields=MediaSources&StartIndex=1&Limit=1")
	assertNames(t, result.Items, "Pilot A")
	if result.TotalRecordCount != 2 || len(result.Items[0].MediaSources) != 1 {
		t.Fatalf("expected a playable episode of 2, got total %d with %d sources", result.TotalRecordCount, len(result.Items[0].MediaSources))
	}
	// Members of an episode-scoped query are all leaves, so the page comes
	// straight from SQL instead of resolving the whole membership.
	if exec.previewPageCalls != 1 || exec.previewCalls != 0 || exec.gotPageOffset != 1 || exec.gotPageLimit != 1 {
		t.Fatalf("expected one SQL page at offset 1 limit 1, got page calls=%d preview calls=%d offset=%d limit=%d",
			exec.previewPageCalls, exec.previewCalls, exec.gotPageOffset, exec.gotPageLimit)
	}
	if typed := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&Filters=IsNotFolder&Recursive=true&IncludeItemTypes=Movie"); len(typed.Items) != 0 {
		t.Fatalf("expected no movies in an episode-scoped collection, got %d", len(typed.Items))
	}

	// Shuffle resolves the whole membership and returns every episode.
	shuffled := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&Filters=IsNotFolder&Recursive=true&SortBy=Random")
	if len(shuffled.Items) != 2 || shuffled.TotalRecordCount != 2 {
		t.Fatalf("expected 2 of 2 shuffled episodes, got %d of %d", len(shuffled.Items), shuffled.TotalRecordCount)
	}
}

// Recursive requests that only exclude types, or that also ask for series,
// are listings rather than Play all, so they keep the member list.
func TestHandleItems_BoxSetRecursiveListingsStayMembers(t *testing.T) {
	h, parentID := newPlayAllFixture(t)
	for _, q := range []string{
		"&Recursive=true&ExcludeItemTypes=Season",
		"&Recursive=true&IncludeItemTypes=Movie,Series,Episode",
	} {
		query := parseItemsQuery(httptest.NewRequest("GET", "/Items?ParentId="+parentID+q, nil), h.codec)
		if query.wantsCollectionLeaves() {
			t.Fatalf("%s: expected a member listing, not Play all", q)
		}
	}
}

// Stored collections reference media_items only, so a member hidden from the
// session must not trigger an episode lookup.
func TestHandleItems_StoredBoxSetHiddenMemberSkipsEpisodeLookup(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "103", LibraryID: 1, Title: "Partly Hidden", Visibility: "visible"},
		},
		items: map[string][]*models.LibraryCollectionItem{
			"103": {
				{CollectionID: "103", MediaItemID: "m-1", Position: 0},
				{CollectionID: "103", MediaItemID: "m-hidden", Position: 1},
			},
		},
	}
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{
		"m-1": {ContentID: "m-1", Type: "movie", Title: "Visible"},
	}}
	episodeRepo := &countingEpisodeRepo{}
	h, _ := newDetailCollectionsHandler(t, collections, itemRepo, nil)
	h.episodeRepo = episodeRepo

	parentID := h.codec.EncodeStringID(EncodedIDCollection, "103")
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID)
	assertNames(t, result.Items, "Visible")
	if episodeRepo.getByIDsCalls != 0 {
		t.Fatalf("expected no episode lookup for a stored collection, got %d", episodeRepo.getByIDsCalls)
	}
}

// Clients such as Swiftfin, Findroid, and Kodi send SortBy when listing a
// collection. Catalog browse cannot see episodes, so an episode-scoped smart
// collection sorts its own members, and a type filter excluding Episode is empty.
func TestHandleItems_SmartEpisodeBoxSetHonorsSortBy(t *testing.T) {
	day := func(d int) *time.Time { v := time.Date(2020, 1, d, 0, 0, 0, 0, time.UTC); return &v }
	h, _, _, parentID := newSmartEpisodeFixture(t, "206",
		&models.Episode{ContentID: "e-b", SeasonNumber: 1, EpisodeNumber: 2, Title: "Bravo", AirDate: day(1)},
		&models.Episode{ContentID: "e-c", SeasonNumber: 1, EpisodeNumber: 3, Title: "Charlie", AirDate: day(2)},
		&models.Episode{ContentID: "e-a", SeasonNumber: 1, EpisodeNumber: 1, Title: "Alpha", AirDate: day(3)},
	)

	byName := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&SortBy=SortName&SortOrder=Ascending")
	assertNames(t, byName.Items, "Alpha", "Bravo", "Charlie")
	if byName.TotalRecordCount != 3 {
		t.Fatalf("expected TotalRecordCount 3, got %d", byName.TotalRecordCount)
	}

	byDate := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&SortBy=PremiereDate&SortOrder=Descending&StartIndex=1&Limit=2")
	assertNames(t, byDate.Items, "Charlie", "Bravo")

	movies := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&SortBy=SortName&IncludeItemTypes=Movie")
	if len(movies.Items) != 0 {
		t.Fatalf("expected no movies in an episode-scoped collection, got %v", itemNames(movies.Items))
	}

	// Without an episode repository the sort cannot run, but the collection
	// keeps its members instead of reporting itself empty.
	h.episodeRepo = nil
	unsorted := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&SortBy=SortName")
	if unsorted.TotalRecordCount != 3 {
		t.Fatalf("expected TotalRecordCount 3 without an episode repository, got %d", unsorted.TotalRecordCount)
	}
}

// A type filter or MediaTypes without SortBy must not reorder an episode
// collection, and a descending sort keeps tied episodes in collection order.
func TestHandleItems_SmartEpisodeBoxSetFiltersKeepOrder(t *testing.T) {
	day := func(d int) *time.Time { v := time.Date(2020, 1, d, 0, 0, 0, 0, time.UTC); return &v }
	h, _, _, parentID := newSmartEpisodeFixture(t, "207",
		&models.Episode{ContentID: "e-1", SeasonNumber: 1, EpisodeNumber: 1, Title: "First", AirDate: day(1)},
		&models.Episode{ContentID: "e-2", SeasonNumber: 1, EpisodeNumber: 2, Title: "Second", AirDate: day(2)},
		&models.Episode{ContentID: "e-3", SeasonNumber: 1, EpisodeNumber: 3, Title: "Third", AirDate: day(2)},
	)

	typed := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&IncludeItemTypes=Episode")
	assertNames(t, typed.Items, "First", "Second", "Third")

	audio := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&IncludeItemTypes=Episode&MediaTypes=Audio")
	if len(audio.Items) != 0 {
		t.Fatalf("expected no audio items, got %v", itemNames(audio.Items))
	}

	byDateDesc := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&SortBy=PremiereDate&SortOrder=Descending")
	assertNames(t, byDateDesc.Items, "Second", "Third", "First")
}

// Play all detection reads the same type aliases the item-type filter does.
func TestParseItemsQuery_CollectionLeafTypeAliases(t *testing.T) {
	codec := NewResourceIDCodec()
	for q, want := range map[string]bool{
		"Recursive=true&IncludeItemTypes=Episodes":         true,
		"Recursive=true&IncludeItemTypes=Episode,TvShows":  false,
		"Recursive=true&IncludeItemTypes=Episode,Seasons":  false,
		"Recursive=true&IncludeItemTypes=Movie,Episode":    true,
		"Recursive=false&IncludeItemTypes=Episode":         false,
		"Recursive=true&Filters=IsNotFolder":               true,
		"Recursive=true&Filters=IsNotFolder&IsPlayed=true": false,
	} {
		query := parseItemsQuery(httptest.NewRequest("GET", "/Items?"+q, nil), codec)
		if got := query.wantsCollectionLeaves(); got != want {
			t.Fatalf("%s: wantsCollectionLeaves = %v, want %v", q, got, want)
		}
	}
}
