package requests

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/requests/discoverrows"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// offlineTMDB fails the test on any call: a recommended row is read from
// stored data only.
type offlineTMDB struct{ t *testing.T }

func (f offlineTMDB) fail(method string) error {
	f.t.Errorf("a recommended row called TMDB %s", method)
	return errors.New("offline")
}

func (f offlineTMDB) SearchMedia(context.Context, string, string, int) (*tmdb.MediaPage, error) {
	return nil, f.fail("SearchMedia")
}

func (f offlineTMDB) ChartPage(context.Context, tmdb.ChartParams, int) (*tmdb.MediaPage, error) {
	return nil, f.fail("ChartPage")
}

func (f offlineTMDB) GetMediaDetail(context.Context, string, int) (*tmdb.MediaDetail, error) {
	return nil, f.fail("GetMediaDetail")
}

func (f offlineTMDB) DiscoverPage(context.Context, string, tmdb.DiscoverParams, int) (*tmdb.MediaPage, error) {
	return nil, f.fail("DiscoverPage")
}

func (f offlineTMDB) GetCertification(context.Context, string, int) (string, error) {
	return "", f.fail("GetCertification")
}

func (f offlineTMDB) GetExternalIDs(context.Context, string, int) (*tmdb.ExternalIDs, error) {
	return nil, f.fail("GetExternalIDs")
}

// profileCeilings is each profile's rating ceiling.
type profileCeilings map[string]string

func (profileCeilings) MaxPlaybackQuality(context.Context, int, string) (string, error) {
	return "", nil
}

func (c profileCeilings) MaxContentRating(_ context.Context, _ int, profileID string) (string, error) {
	return c[profileID], nil
}

const (
	recParent  = "parent"
	recSibling = "sibling"
	recTeen    = "teen"
	recBlocked = "blocked"
	// recOwnedOffset is the stored title the library also has.
	recOwnedOffset = 4
	// recTVDBOffset is the TVDB ID (from base) of a library series that has
	// no TMDB ID; the stored series recSeriesOffset carries it.
	recTVDBOffset   = 70
	recSeriesOffset = 60
)

// recRowScenario is one account with an unrestricted profile, a second
// profile with its own row, a PG-13 profile and a blocked one, over stored
// outside titles and the library. Its service reads with a TMDB that fails on
// any call.
type recRowScenario struct {
	t          *testing.T
	pool       *pgxpool.Pool
	svc        *Service
	userID     int
	base       int
	prefix     string
	seed       string
	order      []int // the stored row, best first
	externalOn atomic.Bool
}

func newRecRowScenario(t *testing.T) *recRowScenario {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := database.NewPool(t.Context(), config.DatabaseConfig{URL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.ClosePool(pool) })
	stamp := time.Now().UnixNano()
	s := &recRowScenario{t: t, pool: pool, base: 1_500_000_000 + int(stamp%1_000_000)*100, prefix: "recrow-" + strconv.FormatInt(stamp, 36) + "-"}
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username, role) VALUES($1, 'user') RETURNING id`, s.prefix).Scan(&s.userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, s.userID) })
	items := s.seedLibrary()
	repo := recommendations.NewRepo(pool)
	s.seedOutsideTitles(repo)

	s.externalOn.Store(true)
	reader := recommendations.NewReader(repo, nil, nil, nil).WithPersonalRows(true).
		WithExternalRecommendations(s.externalOn.Load)
	var layout []discoverrows.Row
	for _, row := range discoverrows.DefaultRows() {
		if row.Type == discoverrows.TypeRecommended || row.Type.IsBrand() {
			layout = append(layout, row)
		}
	}
	s.svc = NewService(&layoutStore{fakeStore: newFakeStore(), rows: layout}, offlineTMDB{t}, NewCatalogPresence(items))
	s.svc.SetRecommendations(reader)
	s.svc.SetEntitlementResolver(profileCeilings{recTeen: "PG-13"})
	s.svc.SetProfileStores(profileBlockStores{profiles: map[string]*userstore.Profile{
		recParent: {ID: recParent}, recSibling: {ID: recSibling}, recTeen: {ID: recTeen},
		recBlocked: {ID: recBlocked, RequestsBlocked: true},
	}})
	return s
}

func (s *recRowScenario) exec(sql string, args ...any) {
	s.t.Helper()
	if _, err := s.pool.Exec(s.t.Context(), sql, args...); err != nil {
		s.t.Fatal(err)
	}
}

// seedLibrary stores a seed title the viewers can see and a title the
// library has that the stored rows still name.
func (s *recRowScenario) seedLibrary() *catalog.ItemRepository {
	var library int
	if err := s.pool.QueryRow(s.t.Context(), `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, s.prefix).Scan(&library); err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, s.prefix+"%")
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, library)
	})
	items := catalog.NewItemRepository(s.pool)
	s.seed = s.prefix + "seed"
	for id, tmdbID := range map[string]int{s.seed: s.base, s.prefix + "owned": s.base + recOwnedOffset} {
		if err := items.Upsert(s.t.Context(), &models.MediaItem{ContentID: id, Type: "movie", Title: id, Status: "matched"}); err != nil {
			s.t.Fatal(err)
		}
		s.exec(`UPDATE media_items SET tmdb_id = $2 WHERE content_id = $1`, id, strconv.Itoa(tmdbID))
		s.exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library)
	}
	// A series the library knows by its TVDB ID only.
	tvdbOnly := s.prefix + "tvdb-only"
	if err := items.Upsert(s.t.Context(), &models.MediaItem{ContentID: tvdbOnly, Type: "series", Title: tvdbOnly, Status: "matched"}); err != nil {
		s.t.Fatal(err)
	}
	s.exec(`UPDATE media_items SET tmdb_id = '', imdb_id = '', tvdb_id = $2 WHERE content_id = $1`, tvdbOnly, strconv.Itoa(s.base+recTVDBOffset))
	s.exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, tvdbOnly, library)
	return items
}

// seedOutsideTitles stores PG, R, uncertified and owned titles, then 25 G
// titles for paging, and each profile's row: the sibling's names only the R
// title.
func (s *recRowScenario) seedOutsideTitles(repo *recommendations.Repo) {
	ctx := s.t.Context()
	certs := map[int]string{s.base + 1: "PG", s.base + 2: "R", s.base + 3: "", s.base + recOwnedOffset: "PG-13"}
	s.order = []int{s.base + 1, s.base + 2, s.base + 3, s.base + recOwnedOffset}
	for n := range 25 {
		certs[s.base+10+n] = "G"
		s.order = append(s.order, s.base+10+n)
	}
	titles := make([]recommendations.ExternalTitle, 0, len(s.order))
	keys := make([]recommendations.ExternalTitleKey, 0, len(s.order))
	for _, id := range s.order {
		key := recommendations.ExternalTitleKey{MediaType: recommendations.ExternalMediaMovie, TMDBID: id}
		keys = append(keys, key)
		titles = append(titles, recommendations.ExternalTitle{Key: key, Title: "Outside " + strconv.Itoa(id), ReleaseDate: "2025-03-01", USCertification: certs[id]})
	}
	// Two series: the first is the library's TVDB-only series, the second
	// is missing.
	for n, tvdbID := range []int{s.base + recTVDBOffset, 0} {
		key := recommendations.ExternalTitleKey{MediaType: recommendations.ExternalMediaSeries, TMDBID: s.base + recSeriesOffset + n}
		keys = append(keys, key)
		titles = append(titles, recommendations.ExternalTitle{Key: key, Title: "Outside series " + strconv.Itoa(n), USCertification: "TV-PG", TVDBID: tvdbID})
	}
	if err := repo.UpsertExternalTitles(ctx, titles); err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _, _ = repo.DeleteExternalTitles(context.Background(), keys) })
	store := func(profileID, mediaType string, ids []int) {
		var items []recommendations.ExternalRowItem
		for i, id := range ids {
			item := recommendations.ExternalRowItem{TMDBID: id, Score: float64(len(ids) - i), Reasons: []string{}}
			if id == s.base+1 {
				item.Reasons = []string{s.seed, s.prefix + "gone"}
			}
			items = append(items, item)
		}
		raw, err := json.Marshal(items)
		if err != nil {
			s.t.Fatal(err)
		}
		s.exec(`INSERT INTO external_recommendation_rows (user_id, profile_id, media_type, items, embedding_model, built_at)
			VALUES ($1, $2, $3, $4, 'test', now())`, s.userID, profileID, mediaType, raw)
	}
	for _, p := range []string{recParent, recTeen, recBlocked} {
		store(p, recommendations.ExternalMediaMovie, s.order)
	}
	store(recParent, recommendations.ExternalMediaSeries, []int{s.base + recSeriesOffset, s.base + recSeriesOffset + 1})
	store(recSibling, recommendations.ExternalMediaMovie, []int{s.base + 2})
}

func (s *recRowScenario) viewer(profileID string) Viewer {
	return Viewer{UserID: s.userID, ProfileID: profileID, Library: &catalog.AccessFilter{UserID: s.userID, ProfileID: profileID}}
}

func (s *recRowScenario) page(profileID string) *DiscoverLayoutPage {
	s.t.Helper()
	page, err := s.svc.DiscoverRows(s.t.Context(), s.viewer(profileID))
	if err != nil {
		s.t.Fatal(err)
	}
	return page
}

func (s *recRowScenario) seeAll(profileID string, n int) *DiscoverySection {
	s.t.Helper()
	section, err := s.svc.DiscoverRow(s.t.Context(), s.viewer(profileID), "recommended_movies", n)
	if err != nil {
		s.t.Fatal(err)
	}
	return section
}

func recMovieRow(page *DiscoverLayoutPage) *DiscoverySection {
	for i := range page.Sections {
		if page.Sections[i].Key == "recommended_movies" {
			return &page.Sections[i]
		}
	}
	return nil
}

func recInOrder(page *DiscoverLayoutPage, key string) bool {
	return slices.ContainsFunc(page.Order, func(r DiscoverRowRef) bool { return r.Key == key })
}

func recIDs(results []MediaResult) []int {
	out := make([]int, 0, len(results))
	for _, r := range results {
		out = append(out, r.TMDBID)
	}
	return out
}

func TestRecommendedDiscoverRowDatabase(t *testing.T) {
	s := newRecRowScenario(t)
	t.Run("parent", s.checkParent)
	t.Run("rating ceiling", s.checkCeiling)
	t.Run("own row per profile", s.checkSibling)
	t.Run("blocked or off", s.checkNoRow)
}

// The parent gets everything but the owned title: the Discover page shows the
// row's limit, the see all pages the rest 20 at a time.
func (s *recRowScenario) checkParent(t *testing.T) {
	want := slices.Concat(s.order[:3], s.order[4:])
	page := s.page(recParent)
	section := recMovieRow(page)
	if section == nil || !slices.Equal(recIDs(section.Results), want[:20]) || section.TotalResults != len(want) || section.TotalPages != 2 || section.NextPage != 0 {
		t.Fatalf("Discover row = %+v, want the first 20 of %v", section, want)
	}
	if first := section.Results[0]; len(first.Reasons) != 1 || first.Reasons[0] != (discoverrows.Reason{ContentID: s.seed, Title: s.seed}) ||
		first.Availability != AvailabilityMissing || !first.Request.Requestable {
		t.Fatalf("first title = %+v, want one visible reason, missing and requestable", first)
	}
	// The library has the first series under its TVDB ID only.
	if series, err := s.svc.DiscoverRow(t.Context(), s.viewer(recParent), "recommended_series", 1); err != nil ||
		!slices.Equal(recIDs(series.Results), []int{s.base + recSeriesOffset + 1}) {
		t.Fatalf("series row = %+v %v, want only the series the library lacks", series, err)
	}
	var seen []int
	for n := 1; n <= 3; n++ {
		p := s.seeAll(recParent, n)
		if p.Page != n || p.TotalPages != 2 || p.TotalResults != len(want) || p.NextPage != 0 {
			t.Fatalf("see all page %d = %+v", n, p)
		}
		seen = append(seen, recIDs(p.Results)...)
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("see all pages = %v, want %v", seen, want)
	}
}

// A PG-13 profile sees neither the R title nor the uncertified one.
func (s *recRowScenario) checkCeiling(t *testing.T) {
	p := s.seeAll(recTeen, 1)
	if got := recIDs(p.Results); !slices.Equal(got, slices.Concat(s.order[:1], s.order[4:23])) || p.TotalResults != 26 {
		t.Fatalf("PG-13 row = %v (%d titles), want the PG title and the G titles", got, p.TotalResults)
	}
}

// Another profile on the account gets its own row.
func (s *recRowScenario) checkSibling(t *testing.T) {
	page := s.page(recSibling)
	if section := recMovieRow(page); section == nil || !slices.Equal(recIDs(section.Results), []int{s.base + 2}) {
		t.Fatalf("sibling row = %+v, want its own single title", section)
	}
	if !recInOrder(page, "recommended_series") || slices.ContainsFunc(page.Sections, func(x DiscoverySection) bool { return x.Key == "recommended_series" }) {
		t.Fatalf("a row with no titles stays in row_order and out of items: %+v", page)
	}
}

// A blocked profile is refused Discover; with outside recommendations off no
// profile gets the row, and the status says so.
func (s *recRowScenario) checkNoRow(t *testing.T) {
	ctx := t.Context()
	if _, err := s.svc.DiscoverRows(ctx, s.viewer(recBlocked)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("blocked profile: %v, want ErrForbidden", err)
	}
	if status, err := s.svc.GetFeatureStatus(ctx, s.viewer(recParent)); err != nil || !status.PersonalDiscoverRowsSupported {
		t.Fatalf("status with the feature on = %+v %v", status, err)
	}
	s.externalOn.Store(false)
	page := s.page(recParent)
	if section := recMovieRow(page); section != nil || !recInOrder(page, "recommended_movies") {
		t.Fatalf("feature off: row %+v, order %+v; want no row, kept in row_order", section, page.Order)
	}
	if status, err := s.svc.GetFeatureStatus(ctx, s.viewer(recParent)); err != nil || status.PersonalDiscoverRowsSupported {
		t.Fatalf("status with the feature off = %+v %v", status, err)
	}
}
