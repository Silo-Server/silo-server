package historyimport

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// fakeSeriesDrops is a SeriesDropStore over maps. Drops it imports are
// recorded in imported; deleted holds the ones taken back. activityAtWrite is
// playback that saves while a drop is written, seen only by later reads.
type fakeSeriesDrops struct {
	seriesOf        map[string]string // episode item ID -> series ID
	dropped         map[string]catalog.DroppedSeries
	activity        map[string]time.Time
	activityAtWrite map[string]time.Time
	resolveErr      error

	resolveCalls int
	imported     []importedDrop
	deleted      []importedDrop
}

type importedDrop struct {
	seriesID  string
	droppedAt time.Time
	observed  *time.Time
}

func (f *fakeSeriesDrops) EpisodeSeriesIDs(_ context.Context, episodeIDs []string) (map[string]string, error) {
	f.resolveCalls++
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	out := make(map[string]string)
	for _, id := range episodeIDs {
		if seriesID, ok := f.seriesOf[id]; ok {
			out[id] = seriesID
		}
	}
	return out, nil
}

func (f *fakeSeriesDrops) ListDropped(_ context.Context, _ int, _ string, seriesIDs []string) ([]catalog.DroppedSeries, error) {
	var out []catalog.DroppedSeries
	for _, id := range seriesIDs {
		if drop, ok := f.dropped[id]; ok {
			out = append(out, drop)
		}
	}
	return out, nil
}

func (f *fakeSeriesDrops) LatestActivity(_ context.Context, _ int, _ string, seriesIDs []string) (map[string]time.Time, error) {
	out := make(map[string]time.Time)
	for _, id := range seriesIDs {
		if at, ok := f.activity[id]; ok {
			out[id] = at
		}
	}
	return out, nil
}

func (f *fakeSeriesDrops) ImportDrop(_ context.Context, _ int, _ string, seriesID string, droppedAt time.Time, observed *time.Time) (bool, error) {
	f.imported = append(f.imported, importedDrop{seriesID: seriesID, droppedAt: droppedAt, observed: observed})
	if at, ok := f.activityAtWrite[seriesID]; ok {
		if f.activity == nil {
			f.activity = make(map[string]time.Time)
		}
		f.activity[seriesID] = at
	}
	return true, nil
}

func (f *fakeSeriesDrops) DeleteIfUnchanged(_ context.Context, _ int, _ string, seriesID string, observed time.Time) (bool, error) {
	f.deleted = append(f.deleted, importedDrop{seriesID: seriesID, droppedAt: observed})
	return true, nil
}

// importedIDs are the series whose imported drop was kept.
func (f *fakeSeriesDrops) importedIDs() []string {
	ids := make([]string, 0, len(f.imported))
	for _, drop := range f.imported {
		if !slices.ContainsFunc(f.deleted, func(d importedDrop) bool { return d.seriesID == drop.seriesID }) {
			ids = append(ids, drop.seriesID)
		}
	}
	slices.Sort(ids)
	return ids
}

// fakeNextUp surfaces one episode for each series in surfaced.
type fakeNextUp struct {
	surfaced map[string]bool
	queries  []catalog.NextUpQuery
}

func (f *fakeNextUp) ListNextUp(_ context.Context, q catalog.NextUpQuery) ([]catalog.NextUpResult, error) {
	f.queries = append(f.queries, q)
	if f.surfaced[q.SeriesID] {
		return []catalog.NextUpResult{{ContentID: q.SeriesID + "-next", SeriesID: q.SeriesID}}, nil
	}
	return nil, nil
}

func (f *fakeNextUp) queried() []string {
	ids := make([]string, 0, len(f.queries))
	for _, q := range f.queries {
		ids = append(ids, q.SeriesID)
	}
	slices.Sort(ids)
	return ids
}

var reconcileNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func reconcileService(drops *fakeSeriesDrops, nextUp *fakeNextUp, matcherRepo *matcherRepoStub) *Service {
	if matcherRepo == nil {
		matcherRepo = &matcherRepoStub{}
	}
	return &Service{
		matcher: NewMatcher(matcherRepo), seriesDrops: drops, nextUp: nextUp,
		now: func() time.Time { return reconcileNow },
	}
}

// surfaceAll makes every series Silo knows show up in Next Up.
func surfaceAll(drops *fakeSeriesDrops) *fakeNextUp {
	surfaced := make(map[string]bool)
	for _, seriesID := range drops.seriesOf {
		surfaced[seriesID] = true
	}
	return &fakeNextUp{surfaced: surfaced}
}

func TestReconcileContinueWatchingDropsUnfinishedShowsTheRowLeavesOut(t *testing.T) {
	t.Parallel()

	played := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	drops := &fakeSeriesDrops{seriesOf: map[string]string{
		"hidden-1": "s-hidden", "listed-1": "s-listed", "copy-1": "s-copy-4k",
		"finished-1": "s-finished", "silo-ahead-1": "s-silo-ahead",
	}}
	nextUp := surfaceAll(drops)
	// The listed show is in Silo twice; the run imported the 4K copy, and
	// Emby listed its other copy, recognized by TMDB ID.
	matcherRepo := &matcherRepoStub{mediaByExternal: map[string][]mediaLookupRow{
		"series:tmdb_id:154385": {{ContentID: "s-copy"}, {ContentID: "s-copy-4k"}},
	}}
	service := reconcileService(drops, nextUp, matcherRepo)

	row := ContinueWatchingRow{
		SourceSeriesIDs: map[string]bool{"src-listed": true, "src-copy": true},
		Series:          []Record{{ExternalID: "src-copy", Kind: KindSeries, TMDBID: "154385"}},
		IncludesNextUp:  true,
		// Finished at Emby: the finished show, and the show Silo has a newer
		// season of.
		UnfinishedSourceSeries: map[string]bool{"src-hidden": true, "src-listed": true, "src-copy-4k": true},
	}
	episodes := []importedEpisode{
		{itemID: "hidden-1", sourceSeriesID: "src-hidden", at: played},
		{itemID: "listed-1", sourceSeriesID: "src-listed", at: played},
		{itemID: "copy-1", sourceSeriesID: "src-copy-4k", at: played},
		{itemID: "finished-1", sourceSeriesID: "src-finished", at: played},
		{itemID: "silo-ahead-1", sourceSeriesID: "src-silo-ahead", at: played},
	}

	dropped, err := service.reconcileContinueWatching(context.Background(), 7, "profile-1", row, episodes)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if dropped != 1 || !slices.Equal(drops.importedIDs(), []string{"s-hidden"}) {
		t.Fatalf("dropped %d %v, want only s-hidden", dropped, drops.importedIDs())
	}
	// Dated at the run, so the next playback ends it and watch-provider sync
	// sees a current drop.
	if got := drops.imported[0]; !got.droppedAt.Equal(reconcileNow) || got.observed != nil {
		t.Fatalf("drop = %+v, want dated %v with no observed row", got, reconcileNow)
	}
	// Listed and finished shows are settled without a Next Up lookup.
	if got := nextUp.queried(); !slices.Equal(got, []string{"s-hidden"}) {
		t.Fatalf("next-up lookups = %v, want only the hideable show", got)
	}
	for _, q := range nextUp.queries {
		if q.UserID != 7 || q.ProfileID != "profile-1" || q.Limit != 1 || !q.EnableResumable {
			t.Fatalf("next-up query = %+v, want a series-scoped lookup including resumable episodes", q)
		}
	}
}

// Emby can keep Next Up out of the row; then a show between episodes is
// missing from it whether or not it was hidden, and only shows with an
// episode in progress can be judged.
func TestReconcileContinueWatchingWithoutNextUpJudgesOnlyShowsInProgress(t *testing.T) {
	t.Parallel()

	played := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	drops := &fakeSeriesDrops{seriesOf: map[string]string{"between-1": "s-between", "paused-1": "s-paused"}}
	service := reconcileService(drops, surfaceAll(drops), nil)

	row := ContinueWatchingRow{
		SourceSeriesIDs:        map[string]bool{},
		UnfinishedSourceSeries: map[string]bool{"src-between": true, "src-paused": true},
	}
	episodes := []importedEpisode{
		{itemID: "between-1", sourceSeriesID: "src-between", at: played},
		{itemID: "paused-1", sourceSeriesID: "src-paused", at: played, inProgress: true},
	}
	if _, err := service.reconcileContinueWatching(context.Background(), 7, "p", row, episodes); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := drops.importedIDs(); !slices.Equal(got, []string{"s-paused"}) {
		t.Fatalf("dropped %v, want only the show in progress", got)
	}
}

// A listed show that matches nothing the run imported could be a copy of
// any imported show, so nothing is dropped.
func TestReconcileContinueWatchingStopsAtAnUnidentifiedListedShow(t *testing.T) {
	t.Parallel()

	played := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	drops := &fakeSeriesDrops{seriesOf: map[string]string{"hidden-1": "s-hidden"}}
	service := reconcileService(drops, surfaceAll(drops), nil)

	row := ContinueWatchingRow{
		SourceSeriesIDs:        map[string]bool{"src-unknown": true},
		Series:                 []Record{{ExternalID: "src-unknown", Kind: KindSeries}},
		IncludesNextUp:         true,
		UnfinishedSourceSeries: map[string]bool{"src-hidden": true},
	}
	episodes := []importedEpisode{{itemID: "hidden-1", sourceSeriesID: "src-hidden", at: played}}
	if _, err := service.reconcileContinueWatching(context.Background(), 7, "p", row, episodes); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(drops.imported) != 0 {
		t.Fatalf("dropped %v with an unidentified show in the row, want none", drops.importedIDs())
	}
}

// A listed show Silo doesn't have, recognizable by its provider IDs, hides
// nothing and doesn't stop the pass.
func TestReconcileContinueWatchingIgnoresListedShowsSiloLacks(t *testing.T) {
	t.Parallel()

	played := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	drops := &fakeSeriesDrops{seriesOf: map[string]string{"hidden-1": "s-hidden"}}
	service := reconcileService(drops, surfaceAll(drops), nil)

	row := ContinueWatchingRow{
		SourceSeriesIDs:        map[string]bool{"src-elsewhere": true},
		Series:                 []Record{{ExternalID: "src-elsewhere", Kind: KindSeries, TMDBID: "999999"}},
		IncludesNextUp:         true,
		UnfinishedSourceSeries: map[string]bool{"src-hidden": true},
	}
	episodes := []importedEpisode{{itemID: "hidden-1", sourceSeriesID: "src-hidden", at: played}}
	if _, err := service.reconcileContinueWatching(context.Background(), 7, "p", row, episodes); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := drops.importedIDs(); !slices.Equal(got, []string{"s-hidden"}) {
		t.Fatalf("dropped %v, want s-hidden", got)
	}
}

// Emby leaves out a show whose paused episode is older than a later finished
// one, so only a show whose newest play is in progress counts as in progress.
func TestReconcileContinueWatchingJudgesProgressByTheNewestPlay(t *testing.T) {
	t.Parallel()

	older := time.Date(2026, 9, 1, 21, 0, 0, 0, time.UTC)
	newer := older.Add(24 * time.Hour)
	drops := &fakeSeriesDrops{seriesOf: map[string]string{
		"stale-paused": "s-stale", "stale-finished": "s-stale",
		"paused-old": "s-paused", "paused-new": "s-paused",
	}}
	service := reconcileService(drops, surfaceAll(drops), nil)

	// No Next Up in the row: only shows in progress can be judged.
	row := ContinueWatchingRow{SourceSeriesIDs: map[string]bool{}}
	episodes := []importedEpisode{
		{itemID: "stale-finished", sourceSeriesID: "src-stale", at: newer},
		{itemID: "stale-paused", sourceSeriesID: "src-stale", at: older, inProgress: true},
		{itemID: "paused-new", sourceSeriesID: "src-paused", at: newer, inProgress: true},
		{itemID: "paused-old", sourceSeriesID: "src-paused", at: older},
	}
	if _, err := service.reconcileContinueWatching(context.Background(), 7, "p", row, episodes); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := drops.importedIDs(); !slices.Equal(got, []string{"s-paused"}) {
		t.Fatalf("dropped %v, want only the show paused at its newest play", got)
	}
}

// A play stamped after this server's clock comes from a source clock that
// runs ahead; a drop can be dated neither before nor after it safely, so the
// show waits for a later import.
func TestReconcileContinueWatchingSkipsShowsPlayedAfterTheServerClock(t *testing.T) {
	t.Parallel()

	drops := &fakeSeriesDrops{seriesOf: map[string]string{"ep": "s-1"}}
	service := reconcileService(drops, surfaceAll(drops), nil)

	row := ContinueWatchingRow{SourceSeriesIDs: map[string]bool{}, IncludesNextUp: true, UnfinishedSourceSeries: map[string]bool{"src": true}}
	episodes := []importedEpisode{{itemID: "ep", sourceSeriesID: "src", at: reconcileNow.Add(10 * time.Minute)}}
	if _, err := service.reconcileContinueWatching(context.Background(), 7, "p", row, episodes); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(drops.imported) != 0 {
		t.Fatalf("drops = %+v, want none for a future-stamped play", drops.imported)
	}
}

// Playback that saves while the drop is written is newer than the last
// imported play, so the drop is taken back.
func TestReconcileContinueWatchingTakesBackADropRacedByPlayback(t *testing.T) {
	t.Parallel()

	played := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	drops := &fakeSeriesDrops{
		seriesOf:        map[string]string{"raced-1": "s-raced", "hidden-1": "s-hidden"},
		activityAtWrite: map[string]time.Time{"s-raced": reconcileNow.Add(-time.Millisecond)},
	}
	service := reconcileService(drops, surfaceAll(drops), nil)

	row := ContinueWatchingRow{SourceSeriesIDs: map[string]bool{}, IncludesNextUp: true, UnfinishedSourceSeries: map[string]bool{"src-raced": true, "src-hidden": true}}
	episodes := []importedEpisode{
		{itemID: "raced-1", sourceSeriesID: "src-raced", at: played},
		{itemID: "hidden-1", sourceSeriesID: "src-hidden", at: played},
	}
	dropped, err := service.reconcileContinueWatching(context.Background(), 7, "p", row, episodes)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if dropped != 1 || !slices.Equal(drops.importedIDs(), []string{"s-hidden"}) {
		t.Fatalf("kept %d %v, want only s-hidden", dropped, drops.importedIDs())
	}
	if len(drops.deleted) != 1 || drops.deleted[0].seriesID != "s-raced" || !drops.deleted[0].droppedAt.Equal(reconcileNow) {
		t.Fatalf("taken back = %+v, want s-raced fenced on its drop time", drops.deleted)
	}
}

// A listed show is recognized by its primary provider ID; a stale secondary
// ID that names another series doesn't mark that one as listed.
func TestReconcileContinueWatchingListsByThePrimaryProviderID(t *testing.T) {
	t.Parallel()

	played := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	drops := &fakeSeriesDrops{seriesOf: map[string]string{"listed-1": "s-listed", "hidden-1": "s-hidden"}}
	matcherRepo := &matcherRepoStub{mediaByExternal: map[string][]mediaLookupRow{
		"series:tmdb_id:100": {{ContentID: "s-listed"}},
		// Stale TVDB ID on the listed Emby series, naming the hidden show.
		"series:tvdb_id:200": {{ContentID: "s-hidden"}},
	}}
	service := reconcileService(drops, surfaceAll(drops), matcherRepo)

	row := ContinueWatchingRow{
		SourceSeriesIDs:        map[string]bool{"src-listed-copy": true},
		Series:                 []Record{{ExternalID: "src-listed-copy", Kind: KindSeries, TMDBID: "100", TVDBID: "200", PreferTMDB: true}},
		IncludesNextUp:         true,
		UnfinishedSourceSeries: map[string]bool{"src-listed": true, "src-hidden": true},
	}
	episodes := []importedEpisode{
		{itemID: "listed-1", sourceSeriesID: "src-listed", at: played},
		{itemID: "hidden-1", sourceSeriesID: "src-hidden", at: played},
	}
	if _, err := service.reconcileContinueWatching(context.Background(), 7, "p", row, episodes); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := drops.importedIDs(); !slices.Equal(got, []string{"s-hidden"}) {
		t.Fatalf("dropped %v, want s-hidden despite the stale secondary ID", got)
	}
}

// Episodes are resolved to their series in one batch.
func TestReconcileContinueWatchingResolvesEpisodesInOneBatch(t *testing.T) {
	t.Parallel()

	played := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	drops := &fakeSeriesDrops{seriesOf: map[string]string{"a": "s-1", "b": "s-1", "c": "s-2"}}
	service := reconcileService(drops, surfaceAll(drops), nil)

	row := ContinueWatchingRow{SourceSeriesIDs: map[string]bool{}, IncludesNextUp: true, UnfinishedSourceSeries: map[string]bool{"src-1": true, "src-2": true}}
	episodes := []importedEpisode{
		{itemID: "a", sourceSeriesID: "src-1", at: played},
		{itemID: "b", sourceSeriesID: "src-1", at: played},
		{itemID: "a", sourceSeriesID: "src-1", at: played},
		{itemID: "c", sourceSeriesID: "src-2", at: played},
	}
	if _, err := service.reconcileContinueWatching(context.Background(), 7, "p", row, episodes); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if drops.resolveCalls != 1 {
		t.Fatalf("resolve calls = %d, want 1", drops.resolveCalls)
	}
	if got := drops.importedIDs(); !slices.Equal(got, []string{"s-1", "s-2"}) {
		t.Fatalf("dropped %v, want both shows", got)
	}
}

// One source series can span several Silo series; listing it lists them all.
func TestReconcileContinueWatchingListsEverySeriesOfASourceShow(t *testing.T) {
	t.Parallel()

	played := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	drops := &fakeSeriesDrops{seriesOf: map[string]string{"s1e1": "s-part-1", "s2e1": "s-part-2"}}
	service := reconcileService(drops, surfaceAll(drops), nil)

	row := ContinueWatchingRow{
		SourceSeriesIDs:        map[string]bool{"src-show": true},
		IncludesNextUp:         true,
		UnfinishedSourceSeries: map[string]bool{"src-show": true},
	}
	episodes := []importedEpisode{
		{itemID: "s1e1", sourceSeriesID: "src-show", at: played},
		{itemID: "s2e1", sourceSeriesID: "src-show", at: played},
	}
	if _, err := service.reconcileContinueWatching(context.Background(), 7, "p", row, episodes); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(drops.imported) != 0 {
		t.Fatalf("dropped %v, want neither part of the listed show", drops.importedIDs())
	}
}

func TestReconcileContinueWatchingKeepsTheProfilesOwnChoices(t *testing.T) {
	t.Parallel()

	imported := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	before := imported.Add(-time.Hour)
	after := imported.Add(time.Hour)
	drops := &fakeSeriesDrops{
		seriesOf: map[string]string{"a": "s-watched-here", "b": "s-dropped", "c": "s-redropped", "d": "s-ended", "e": "s-undone"},
		dropped: map[string]catalog.DroppedSeries{
			"s-dropped":   {SeriesID: "s-dropped", DroppedAt: before, Active: true},
			"s-redropped": {SeriesID: "s-redropped", DroppedAt: after, Active: true},
			// Ended before the imported play: the source's newer hide wins.
			"s-ended": {SeriesID: "s-ended", DroppedAt: before, Active: false},
			// Dropped and watched again after the imported play: the profile
			// already undid a newer drop, which stands.
			"s-undone": {SeriesID: "s-undone", DroppedAt: after, Active: false},
		},
		// Watched in Silo after the source's last play.
		activity: map[string]time.Time{"s-watched-here": after, "s-ended": imported},
	}
	service := reconcileService(drops, surfaceAll(drops), nil)

	row := ContinueWatchingRow{SourceSeriesIDs: map[string]bool{}, IncludesNextUp: true, UnfinishedSourceSeries: map[string]bool{}}
	var episodes []importedEpisode
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		row.UnfinishedSourceSeries["src-"+id] = true
		episodes = append(episodes, importedEpisode{itemID: id, sourceSeriesID: "src-" + id, at: imported})
	}
	dropped, err := service.reconcileContinueWatching(context.Background(), 7, "profile-1", row, episodes)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if dropped != 1 || !slices.Equal(drops.importedIDs(), []string{"s-ended"}) {
		t.Fatalf("dropped %d %v, want only s-ended re-dropped", dropped, drops.importedIDs())
	}
	if got := drops.imported[0]; got.observed == nil || !got.observed.Equal(before) || !got.droppedAt.Equal(reconcileNow) {
		t.Fatalf("re-drop = %+v, want dated %v replacing the ended drop of %v", got, reconcileNow, before)
	}
}

// An undated import is stamped at the epoch, so any dated Silo activity
// keeps the show; without any, the drop is still dated at the run.
func TestReconcileContinueWatchingDatesDropsOfUndatedImportsAtTheRun(t *testing.T) {
	t.Parallel()

	drops := &fakeSeriesDrops{seriesOf: map[string]string{"ep": "s-1", "ep2": "s-2"}, activity: map[string]time.Time{"s-2": time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}}
	service := reconcileService(drops, surfaceAll(drops), nil)

	row := ContinueWatchingRow{SourceSeriesIDs: map[string]bool{}, IncludesNextUp: true, UnfinishedSourceSeries: map[string]bool{"src": true, "src2": true}}
	episodes := []importedEpisode{
		newImportedEpisode("ep", Record{Kind: KindEpisode, SourceSeriesID: "src", Played: true}),
		newImportedEpisode("ep2", Record{Kind: KindEpisode, SourceSeriesID: "src2", Played: true}),
	}
	if _, err := service.reconcileContinueWatching(context.Background(), 7, "p", row, episodes); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(drops.imported) != 1 || drops.imported[0].seriesID != "s-1" || !drops.imported[0].droppedAt.Equal(reconcileNow) {
		t.Fatalf("drops = %+v, want s-1 alone, dated at the run", drops.imported)
	}
}

func TestNewImportedEpisodeMarksEpisodesInProgress(t *testing.T) {
	t.Parallel()

	if !newImportedEpisode("ep", Record{PositionSeconds: 120}).inProgress {
		t.Fatal("episode with a resume point not in progress")
	}
	if newImportedEpisode("ep", Record{PositionSeconds: 120, Played: true}).inProgress {
		t.Fatal("played episode in progress")
	}
	if newImportedEpisode("ep", Record{}).inProgress {
		t.Fatal("episode without a resume point in progress")
	}
}

func TestReconcileContinueWatchingDoesNothingWithoutItsStores(t *testing.T) {
	t.Parallel()

	episodes := []importedEpisode{{itemID: "ep", sourceSeriesID: "src", at: time.Now()}}
	dropped, err := (&Service{}).reconcileContinueWatching(context.Background(), 7, "p", ContinueWatchingRow{}, episodes)
	if err != nil || dropped != 0 {
		t.Fatalf("reconcile without stores = %d, %v; want 0, nil", dropped, err)
	}
}

func TestReconcileContinueWatchingStopsOnStoreErrors(t *testing.T) {
	t.Parallel()

	failure := errors.New("database unavailable")
	drops := &fakeSeriesDrops{resolveErr: failure}
	service := reconcileService(drops, &fakeNextUp{}, nil)
	episodes := []importedEpisode{{itemID: "ep", sourceSeriesID: "src", at: time.Now()}}
	if _, err := service.reconcileContinueWatching(context.Background(), 7, "p", ContinueWatchingRow{}, episodes); !errors.Is(err, failure) {
		t.Fatalf("err = %v, want %v", err, failure)
	}
	if len(drops.imported) != 0 {
		t.Fatalf("drops after an error = %v, want none", drops.importedIDs())
	}
}

// rowProvider is a Provider reporting a fixed row.
type rowProvider struct {
	row ContinueWatchingRow
	ok  bool
}

func (rowProvider) Fetch(context.Context) ([]Record, []string, error) { return nil, nil, nil }

func (p rowProvider) ContinueWatchingRow() (ContinueWatchingRow, bool) { return p.row, p.ok }

// Queued runs wrap their provider for private network access; the run reads
// the row through that wrapper, so it must pass the row on.
func TestPrivateNetworkProviderPassesTheRowThrough(t *testing.T) {
	t.Parallel()

	want := ContinueWatchingRow{SourceSeriesIDs: map[string]bool{"lasso": true}}
	var wrapped Provider = privateNetworkProvider{rowProvider{row: want, ok: true}}
	reporter, ok := wrapped.(ContinueWatchingRowReporter)
	if !ok {
		t.Fatal("wrapped provider does not report a row")
	}
	if row, ok := reporter.ContinueWatchingRow(); !ok || !row.SourceSeriesIDs["lasso"] {
		t.Fatalf("wrapped row = %+v, %v; want the inner provider's row", row, ok)
	}

	// The wrapped Emby provider, as queued runs build it.
	if _, ok := Provider(privateNetworkProvider{NewEmbyProvider(nil, embyLocalAuth{})}).(ContinueWatchingRowReporter); !ok {
		t.Fatal("wrapped Emby provider does not report a row")
	}

	// A provider without a row reports none through the wrapper.
	if _, ok := (privateNetworkProvider{staticWatchlistProvider{}}).ContinueWatchingRow(); ok {
		t.Fatal("wrapped provider without a row reported one")
	}
}
