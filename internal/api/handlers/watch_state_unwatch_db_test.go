package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks"
	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// A watched episode whose file was deleted has no library membership, so the
// episode listings leave it out. Marking its series or season unwatched must
// still clear it: its completed progress otherwise keeps anchoring Next Up on
// the series. File-less episodes nobody watched stay out of the unwatch.
func TestParentUnwatchClearsEpisodesWithoutLibraryFilePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	for _, parent := range []string{watchstate.ParentSeries, watchstate.ParentSeason} {
		t.Run(parent, func(t *testing.T) {
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, err := pool.Exec(ctx, sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			prefix := fmt.Sprintf("parent-unwatch-%s-%d", parent, time.Now().UnixNano())
			series := prefix + "-series"
			season := prefix + "-season-1"
			deleted1, deleted2, onDisk, neverWatched := prefix+"-s01e01", prefix+"-s01e02", prefix+"-s01e03", prefix+"-s01e04"
			var folder, userID int
			if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, prefix).Scan(&folder); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id=$1`, folder)
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, series)
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder)
			})
			exec(`INSERT INTO media_items (content_id,type,title) VALUES ($1,'series','Unwatch Series')`, series)
			exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, series, folder)
			if err := catalog.NewSeasonRepository(pool).Upsert(ctx, &models.Season{ContentID: season, SeriesID: series, SeasonNumber: 1, Title: "Season 1", DefaultMetadataLanguage: "en"}); err != nil {
				t.Fatal(err)
			}
			exec(`INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title) VALUES
				($1,$5,$6,1,1,'Deleted one'), ($2,$5,$6,1,2,'Deleted two'), ($3,$5,$6,1,3,'On disk'), ($4,$5,$6,1,4,'Never watched')`,
				deleted1, deleted2, onDisk, neverWatched, series, season)
			exec(`INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES ($1,$2)`, onDisk, folder)
			exec(`INSERT INTO media_files (episode_id,media_folder_id,file_path) VALUES ($1,$2,$3)`, onDisk, folder, "/"+onDisk)
			profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
			exec(`INSERT INTO user_profiles (id,user_id,name) VALUES ($1,$2,'Unwatch fixture')`, profileID, userID)

			provider := pgstore.NewPostgresProvider(pool)
			episodes := catalog.NewEpisodeRepository(pool)
			hub := evt.NewHub("test", nil)
			events, unsubscribe := hub.Subscribe()
			t.Cleanup(unsubscribe)
			handler := &ItemsHandler{
				itemRepo:    catalog.NewItemRepository(pool),
				seasonRepo:  catalog.NewSeasonRepository(pool),
				episodeRepo: episodes,
				watchState:  watchstate.NewService(provider),
			}
			handler.EventsHub = hub
			// The watched event names a season's or episode's series, so a
			// client that hasn't cached the item can refresh the series (#796).
			eventSeries := func() string {
				t.Helper()
				select {
				case env := <-events:
					var p userStateEventPayload
					if err := json.Unmarshal(env.Data, &p); err != nil {
						t.Fatal(err)
					}
					return p.SeriesID
				default:
					t.Fatal("no watched event")
					return ""
				}
			}
			nextUp := catalog.NewNextUpRepository(pool, provider)
			listNextUp := func() []catalog.NextUpResult {
				t.Helper()
				results, err := nextUp.ListNextUp(ctx, catalog.NextUpQuery{UserID: userID, ProfileID: profileID, SeriesID: series, Limit: 5})
				if err != nil {
					t.Fatal(err)
				}
				return results
			}

			// The two episodes were watched while their files existed; the
			// scanner then dropped their library membership.
			if err := handler.markLeafTargetsWatched(ctx, userID, profileID, []watchedLeafTarget{{ContentID: deleted1}, {ContentID: deleted2}}, false); err != nil {
				t.Fatal(err)
			}
			if got := listNextUp(); len(got) != 1 || got[0].ContentID != onDisk {
				t.Fatalf("next up before unwatch = %+v, want %s", got, onDisk)
			}

			target := series
			if parent == watchstate.ParentSeason {
				target = season
			}
			view, err := handler.SetWatchedState(ctx, userID, profileID, target, false, false, catalog.AccessFilter{})
			if err != nil {
				t.Fatalf("SetWatchedState: %v", err)
			}
			if view.Type != parent || view.AffectedCount != 3 {
				t.Fatalf("view = %+v, want %s with the on-disk episode and the two watched deleted ones", view, parent)
			}
			wantSeries := series // a season names its series
			if parent == watchstate.ParentSeries {
				wantSeries = "" // the event's item is the series itself
			}
			if got := eventSeries(); got != wantSeries {
				t.Fatalf("watched event series = %q, want %q", got, wantSeries)
			}
			if got := listNextUp(); len(got) != 0 {
				t.Fatalf("next up after unwatch = %+v, want the series gone", got)
			}
			store, err := provider.ForUser(ctx, userID)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{deleted1, deleted2} {
				progress, err := store.GetProgress(ctx, profileID, id)
				if err != nil {
					t.Fatal(err)
				}
				if progress != nil {
					t.Fatalf("%s progress after unwatch = %+v, want cleared", id, progress)
				}
			}
			var hiddenNeverWatched int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_history_hidden_items WHERE user_id=$1 AND media_item_id=$2`, userID, neverWatched).Scan(&hiddenNeverWatched); err != nil {
				t.Fatal(err)
			}
			if hiddenNeverWatched != 0 {
				t.Fatal("unwatch hid the never-watched file-less episode; want it left out")
			}
			if _, err := handler.SetWatchedState(ctx, userID, profileID, onDisk, true, false, catalog.AccessFilter{}); err != nil {
				t.Fatalf("mark episode watched: %v", err)
			}
			if got := eventSeries(); got != series {
				t.Fatalf("episode watched event series = %q, want %q", got, series)
			}
		})
	}
}

// Marking an audiobook listened (issue #1253) completes it like a movie: one
// manual play sized to the whole book, which is the sum of its files rather
// than the first file or the catalog runtime, and a listen in progress ends.
// Unmarking clears it, and a book outside the viewer's libraries stays not
// found.
func TestAudiobookMarkListenedPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := newAudiobookMarkFixture(t, pool)
	handler, book := f.handler, f.book
	mark := func(played bool, filter catalog.AccessFilter) (WatchedStateView, error) {
		return handler.SetWatchedState(ctx, f.userID, f.profileID, book, played, played, filter)
	}
	listen := func(at time.Time) {
		t.Helper()
		if _, err := handler.watchState.RecordPlaybackStop(ctx, f.userID, f.profileID, book, 4200, 1200, at, userstore.VersionHints{}, userstore.ProgressThresholds{}); err != nil {
			t.Fatal(err)
		}
	}

	var apiErr *APIError
	if _, err := mark(true, catalog.AccessFilter{AllowedLibraryIDs: []int{f.otherFolder}}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("mark outside the viewer's libraries = %v, want 404", err)
	}
	// Clearing a resume position does not cover audiobooks.
	if err := handler.ClearResumePosition(ctx, f.userID, f.profileID, book, catalog.AccessFilter{}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("clear resume position on an audiobook = %v, want 404", err)
	}

	// A first listen stopped twenty minutes in, then the profile marks it.
	listen(time.Now().UTC().Add(-time.Hour))
	view, err := mark(true, catalog.AccessFilter{})
	if err != nil || view.Type != "audiobook" || view.AffectedCount != 1 || !view.Played {
		t.Fatalf("mark listened = %+v, %v; want one audiobook marked", view, err)
	}
	f.requireFinished(t, "after mark")
	if manual := f.manualPlays(t); len(manual) != 1 || manual[0].MediaItemID != book || manual[0].DurationSeconds != 4200 {
		t.Fatalf("manual history = %+v, want one play of the 4200 s book", manual)
	}

	// A re-listen in progress, marked listened like a movie rewatch, records
	// a second play and ends the re-listen.
	listen(time.Now().UTC())
	if _, err := mark(true, catalog.AccessFilter{}); err != nil {
		t.Fatalf("mark re-listen: %v", err)
	}
	f.requireFinished(t, "after re-listen mark")
	if manual := f.manualPlays(t); len(manual) != 2 {
		t.Fatalf("manual history after re-listen mark = %+v, want two plays", manual)
	}

	if _, err := mark(false, catalog.AccessFilter{}); err != nil {
		t.Fatalf("mark unlistened: %v", err)
	}
	if progress, err := f.store.GetProgress(ctx, f.profileID, book); err != nil || progress != nil {
		t.Fatalf("progress after unmark = %+v, %v; want cleared", progress, err)
	}
}

// Marking and resetting cross library access, file lookup, and ABS boundaries.
func TestAudiobookMarkListenedBoundariesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	t.Run("duration uses accessible copies", func(t *testing.T) {
		f := newAudiobookMarkFixture(t, pool)
		var allowed int
		if err := pool.QueryRow(t.Context(), `SELECT media_folder_id FROM media_item_libraries WHERE content_id=$1`, f.book).Scan(&allowed); err != nil {
			t.Fatal(err)
		}
		// The allowed recording loses a part; a complete copy remains in a
		// library this profile cannot open.
		if _, err := pool.Exec(t.Context(), `UPDATE media_files SET missing_since=now() WHERE content_id=$1 AND duration IN (1800,4190)`, f.book); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, f.book, f.otherFolder); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO media_files(content_id,media_folder_id,canonical_root_path,file_path,file_size,duration)
			VALUES($1,$2,'/denied','/denied/book.m4b',1,4200)`, f.book, f.otherFolder); err != nil {
			t.Fatal(err)
		}
		filter := catalog.AccessFilter{AllowedLibraryIDs: []int{allowed}}
		if _, err := f.handler.SetWatchedState(t.Context(), f.userID, f.profileID, f.book, true, true, filter); err != nil {
			t.Fatal(err)
		}
		if history := f.manualPlays(t); len(history) != 1 || history[0].DurationSeconds != 2400 {
			t.Fatalf("manual history = %+v; want the accessible 2400-second recording", history)
		}
	})

	t.Run("file lookup failure writes no completion", func(t *testing.T) {
		f := newAudiobookMarkFixture(t, pool)
		f.handler.fileRepo = &fakeEbookFileProvider{err: errors.New("file query failed")}
		_, err := f.handler.SetWatchedState(t.Context(), f.userID, f.profileID, f.book, true, true, catalog.AccessFilter{})
		if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusInternalServerError {
			t.Fatalf("mark after file query failure = %v; want 500", err)
		}
		if progress, err := f.store.GetProgress(t.Context(), f.profileID, f.book); err != nil || progress != nil {
			t.Fatalf("progress after failed mark = %+v, %v; want none", progress, err)
		}
		if history := f.manualPlays(t); len(history) != 0 {
			t.Fatalf("history after failed mark = %+v; want none", history)
		}
		// Clearing listened state does not need the recording's duration.
		if _, err := f.handler.SetWatchedState(t.Context(), f.userID, f.profileID, f.book, false, false, catalog.AccessFilter{}); err != nil {
			t.Fatalf("unmark with unavailable files: %v", err)
		}
	})

	t.Run("ABS reset clears native completion and keeps history", func(t *testing.T) {
		f := newAudiobookMarkFixture(t, pool)
		if _, err := f.handler.SetWatchedState(t.Context(), f.userID, f.profileID, f.book, true, true, catalog.AccessFilter{}); err != nil {
			t.Fatal(err)
		}
		absStore := &audiobooks.ABSProgressStore{Pool: pool}
		if err := absStore.DeleteProgress(t.Context(), strconv.Itoa(f.userID), f.profileID, f.book); err != nil {
			t.Fatal(err)
		}
		if progress, err := absStore.GetProgress(t.Context(), strconv.Itoa(f.userID), f.profileID, f.book); err != nil || progress != nil {
			t.Fatalf("ABS progress after reset = %+v, %v; want none", progress, err)
		}
		if progress, err := userstore.GetProgressWithCompletedHistory(t.Context(), f.store, f.profileID, f.book); err != nil || (progress != nil && progress.Completed) {
			t.Fatalf("native progress after ABS reset = %+v, %v; want unlistened", progress, err)
		}
		if history := f.manualPlays(t); len(history) != 1 {
			t.Fatalf("history after reset = %+v; want the retained play", history)
		}
	})
	t.Run("ABS progress stays visible after a future history cutoff", func(t *testing.T) {
		for _, finished := range []bool{false, true} {
			t.Run(strconv.FormatBool(finished), func(t *testing.T) {
				testABSProgressAfterFutureHistoryReset(t, pool, finished)
			})
		}
	})
	t.Run("ABS report waits for concurrent reset", func(t *testing.T) {
		testABSReportDuringReset(t, pool, dsn)
	})
}

func testABSReportDuringReset(t *testing.T, pool *pgxpool.Pool, dsn string) {
	t.Helper()
	f := newAudiobookMarkFixture(t, pool)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := f.handler.SetWatchedState(ctx, f.userID, f.profileID, f.book, true, true, catalog.AccessFilter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_watch_history SET watched_at=now()+interval '1 day'
		WHERE user_id=$1 AND profile_id=$2 AND media_item_id=$3`, f.userID, f.profileID, f.book); err != nil {
		t.Fatal(err)
	}
	store := func(name string) *audiobooks.ABSProgressStore {
		t.Helper()
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["application_name"] = name
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return &audiobooks.ABSProgressStore{Pool: p}
	}
	resetName, reportName := "reset-"+f.book, "report-"+f.book
	resetStore, reportStore := store(resetName), store(reportName)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(ctx, `SELECT 1 FROM user_watch_progress
		WHERE user_id=$1 AND profile_id=$2 AND media_item_id=$3 FOR UPDATE`, f.userID, f.profileID, f.book); err != nil {
		t.Fatal(err)
	}
	uid := strconv.Itoa(f.userID)
	resetDone, reportDone := make(chan error, 1), make(chan error, 1)
	go func() { resetDone <- resetStore.DeleteProgress(ctx, uid, f.profileID, f.book) }()
	waitForABSDatabaseLock(t, ctx, pool, resetName)
	go func() {
		reportDone <- reportStore.UpsertProgress(ctx, abs.ProgressRow{
			UserID: uid, ProfileID: f.profileID, ContentID: f.book, CurrentSeconds: 120, DurationSeconds: 4200,
		})
	}()
	waitForABSDatabaseLock(t, ctx, pool, reportName)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan error{resetDone, reportDone} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	native, nativeErr := userstore.GetProgressWithCompletedHistory(ctx, f.store, f.profileID, f.book)
	progress, progressErr := reportStore.GetProgress(ctx, uid, f.profileID, f.book)
	if nativeErr != nil || progressErr != nil || native == nil || progress == nil || native.PositionSeconds != 120 || progress.CurrentSeconds != 120 {
		t.Fatalf("after concurrent reset/report: native=%+v (%v), ABS=%+v (%v); want both at 120", native, nativeErr, progress, progressErr)
	}
}

func waitForABSDatabaseLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, applicationName string) {
	t.Helper()
	for tick := time.Tick(time.Millisecond); ; {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE application_name=$1 AND wait_event_type='Lock')`, applicationName).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick:
		}
	}
}

func testABSProgressAfterFutureHistoryReset(t *testing.T, pool *pgxpool.Pool, finished bool) {
	t.Helper()
	f := newAudiobookMarkFixture(t, pool)
	ctx := t.Context()
	if _, err := f.handler.SetWatchedState(ctx, f.userID, f.profileID, f.book, true, true, catalog.AccessFilter{}); err != nil {
		t.Fatal(err)
	}
	// Imported history can be ahead of the server clock. Reset must cover
	// that play while allowing new ABS reports and session ticks to be seen.
	if _, err := pool.Exec(ctx, `UPDATE user_watch_history SET watched_at=$4
		WHERE user_id=$1 AND profile_id=$2 AND media_item_id=$3`,
		f.userID, f.profileID, f.book, time.Now().UTC().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	absStore := &audiobooks.ABSProgressStore{Pool: pool}
	uid := strconv.Itoa(f.userID)
	if err := absStore.DeleteProgress(ctx, uid, f.profileID, f.book); err != nil {
		t.Fatal(err)
	}
	if err := absStore.UpsertProgress(ctx, abs.ProgressRow{
		UserID: uid, ProfileID: f.profileID, ContentID: f.book,
		CurrentSeconds: 120, DurationSeconds: 4200, IsFinished: finished,
	}); err != nil {
		t.Fatal(err)
	}
	check := func(when string, position float64) {
		t.Helper()
		native, err := userstore.GetProgressWithCompletedHistory(ctx, f.store, f.profileID, f.book)
		if err != nil || native == nil || native.Completed != finished || native.PositionSeconds != position {
			t.Fatalf("native progress %s = %+v, %v; want position %v, completed %v", when, native, err, position, finished)
		}
		progress, err := absStore.GetProgress(ctx, uid, f.profileID, f.book)
		if err != nil || progress == nil || progress.IsFinished != finished || progress.CurrentSeconds != position {
			t.Fatalf("ABS progress %s = %+v, %v; want position %v, completed %v", when, progress, err, position, finished)
		}
	}
	position := 120.0
	if finished {
		position = 0
	}
	check("after report", position)
	if err := absStore.UpdateProgressPosition(ctx, uid, f.profileID, f.book, 180); err != nil {
		t.Fatal(err)
	}
	if !finished {
		position = 180
	}
	check("after session tick", position)
}

type audiobookMarkFixture struct {
	handler     *ItemsHandler
	store       userstore.UserStore
	book        string
	profileID   string
	userID      int
	otherFolder int
}

// newAudiobookMarkFixture seeds a two-file audiobook with a second one-file
// copy in one library and an empty second library, plus a profile to mark it
// for.
func newAudiobookMarkFixture(t *testing.T, pool *pgxpool.Pool) audiobookMarkFixture {
	t.Helper()
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("audiobook-mark-%d", time.Now().UnixNano())
	f := audiobookMarkFixture{book: prefix + "-book"}
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('audiobooks',$1,true) RETURNING id`, prefix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('audiobooks',$1,true) RETURNING id`, prefix+"-other").Scan(&f.otherFolder); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, prefix).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id=$1`, f.userID)
		_, _ = pool.Exec(bg, `DELETE FROM media_files WHERE content_id=$1`, f.book)
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id=$1`, f.book)
		_, _ = pool.Exec(bg, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{folder, f.otherFolder})
	})
	// The catalog runtime is deliberately wrong (one minute): the book's real
	// length is its two files, 30 and 40 minutes. A second copy of the same
	// recording in the same library, one 4190-second file, is not a third part.
	exec(`INSERT INTO media_items (content_id,type,title,runtime) VALUES ($1,'audiobook','Mark Listened Book',1)`, f.book)
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, f.book, folder)
	m4b, mp3 := "/"+prefix+"/m4b", "/"+prefix+"/mp3"
	exec(`INSERT INTO media_files (content_id,media_folder_id,canonical_root_path,file_path,file_size,duration)
		VALUES ($1,$2,$3,$4,1,1800), ($1,$2,$3,$5,1,2400), ($1,$2,$6,$7,1,4190)`,
		f.book, folder, m4b, m4b+"/part1.m4b", m4b+"/part2.m4b", mp3, mp3+"/book.mp3")
	f.profileID = fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	exec(`INSERT INTO user_profiles (id,user_id,name) VALUES ($1,$2,'Listener')`, f.profileID, f.userID)

	provider := pgstore.NewPostgresProvider(pool)
	f.handler = &ItemsHandler{
		itemRepo:    catalog.NewItemRepository(pool),
		seasonRepo:  catalog.NewSeasonRepository(pool),
		episodeRepo: catalog.NewEpisodeRepository(pool),
		fileRepo:    scanner.NewFileRepository(pool),
		watchState:  watchstate.NewService(provider),
	}
	store, err := provider.ForUser(ctx, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	f.store = store
	return f
}

func (f audiobookMarkFixture) requireFinished(t *testing.T, when string) {
	t.Helper()
	progress, err := f.store.GetProgress(t.Context(), f.profileID, f.book)
	if err != nil || progress == nil || !progress.Completed || progress.PositionSeconds != 0 {
		t.Fatalf("progress %s = %+v, %v; want completed with no resume position", when, progress, err)
	}
}

func (f audiobookMarkFixture) manualPlays(t *testing.T) []userstore.WatchHistoryEntry {
	t.Helper()
	history, err := f.store.ListHistory(t.Context(), f.profileID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var manual []userstore.WatchHistoryEntry
	for _, entry := range history {
		if entry.Source == userstore.WatchHistorySourceManual {
			manual = append(manual, entry)
		}
	}
	return manual
}
