package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// A book page's series row shows the viewing profile's own state on each
// entry: finished, partway, or untouched. Another profile's listening never
// shows through, a book the profile hid from history shows no state until it
// is read again, and a browse card names a book's authors once each, in
// credit order.
func TestBookPageRelatedStateAndCardAuthorsPostgres(t *testing.T) {
	ctx := t.Context()
	pool := newTestPool(t)
	p := fmt.Sprintf("book-related-%d", time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var folder, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('audiobooks',$1,true) RETURNING id`, p).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, p).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM media_files WHERE media_folder_id=$1`, folder)
		_, _ = pool.Exec(bg, `DELETE FROM item_people WHERE content_id LIKE $1`, p+"-%")
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id LIKE $1`, p+"-%")
		_, _ = pool.Exec(bg, `DELETE FROM people WHERE name LIKE $1`, p+"-%")
		_, _ = pool.Exec(bg, `DELETE FROM media_folders WHERE id=$1`, folder)
	})
	viewer, other := p+"-viewer", p+"-other"
	exec(`INSERT INTO user_profiles (id,user_id,name) VALUES ($1,$3,'Viewer'), ($2,$3,'Other')`, viewer, other, userID)

	finished, partway, untouched, hidden := p+"-1", p+"-2", p+"-3", p+"-4"
	for i, id := range []string{finished, partway, untouched, hidden} {
		exec(`INSERT INTO media_items (content_id,type,title,poster_thumbhash) VALUES ($1,'audiobook',$1,$2)`, id, id+"-hash")
		exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, id, folder)
		exec(`INSERT INTO audiobook_series (content_id,series_name,series_index) VALUES ($1,$2,$3)`, id, p+"-series", i+1)
	}
	// Two parts count toward the listening time; a missing file does not.
	exec(`INSERT INTO media_files (content_id,media_folder_id,file_path,duration,missing_since) VALUES
		($1,$2,$3,600,NULL), ($1,$2,$4,900,NULL), ($1,$2,$5,5000,NOW())`,
		partway, folder, "/"+partway+"/1.m4b", "/"+partway+"/2.m4b", "/"+partway+"/gone.m4b")
	exec(`INSERT INTO user_watch_progress (user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed) VALUES
		($1,$2,$3,0,1000,true), ($1,$2,$4,300,1500,false), ($1,$5,$6,100,1000,false)`,
		userID, viewer, finished, partway, other, untouched)
	// Finished, then hidden from history: neither the progress row nor the
	// history row counts until the profile listens again.
	before := time.Now().Add(-2 * time.Hour)
	exec(`INSERT INTO user_watch_progress (user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed,updated_at) VALUES ($1,$2,$3,0,1000,true,$4)`,
		userID, viewer, hidden, before)
	exec(`INSERT INTO user_watch_history (id,user_id,profile_id,media_item_id,watched_at,completed) VALUES ($1,$2,$3,$4,$5,true)`,
		hidden, userID, viewer, hidden, before)
	exec(`INSERT INTO user_history_hidden_items (user_id,profile_id,media_item_id,hidden_before) VALUES ($1,$2,$3,$4)`,
		userID, viewer, hidden, before.Add(time.Hour))
	// Credit order, not name order, decides which authors a card names.
	person := time.Now().UnixNano() % 1_000_000_000_000
	exec(`INSERT INTO people (id,name) VALUES ($1,$2), ($1+1,$3)`, person, p+"-zed", p+"-amy")
	exec(`INSERT INTO item_people (id,content_id,person_id,kind,sort_order) VALUES ($1,$2,$1,$3,0), ($1+1,$2,$1+1,$3,1)`,
		person, finished, int(models.PersonKindAuthor))

	svc := &DetailService{itemRepo: NewItemRepository(pool), userStoreProvider: pgstore.NewPostgresProvider(pool)}
	ext := svc.buildAudiobookExtension(ctx, &models.MediaItem{ContentID: finished, Type: "audiobook"}, nil, nil,
		AccessFilter{UserID: userID, ProfileID: viewer})
	if ext.Series == nil {
		t.Fatal("series row missing")
	}
	type state struct {
		played    bool
		ratio     float64
		duration  int
		thumbhash string
	}
	entryStates := func(group *AudiobookSeriesGroup) map[string]state {
		got := map[string]state{}
		for _, e := range group.Entries {
			s := state{played: e.Played, duration: e.DurationSeconds, thumbhash: e.PosterThumbhash}
			if e.ProgressRatio != nil {
				s.ratio = *e.ProgressRatio
			}
			got[e.ContentID] = s
		}
		return got
	}
	want := map[string]state{
		finished:  {played: true, thumbhash: finished + "-hash"},
		partway:   {ratio: 0.2, duration: 1500, thumbhash: partway + "-hash"},
		untouched: {thumbhash: untouched + "-hash"},
		hidden:    {thumbhash: hidden + "-hash"},
	}
	if got := entryStates(ext.Series); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("series entries = %v, want %v", got, want)
	}

	// Ebook reading progress follows the same history cutoff: a book read to
	// the end and then hidden shows nothing, and one read again after the
	// hide shows its new progress.
	readHidden, readAgain := p+"-e1", p+"-e2"
	for i, id := range []string{readHidden, readAgain} {
		var file int
		exec(`INSERT INTO media_items (content_id,type,title) VALUES ($1,'ebook',$1)`, id)
		exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, id, folder)
		exec(`INSERT INTO ebook_series (content_id,series_name,series_index) VALUES ($1,$2,$3)`, id, p+"-ebooks", i+1)
		if err := pool.QueryRow(ctx, `INSERT INTO media_files (content_id,media_folder_id,file_path) VALUES ($1,$2,$3) RETURNING id`,
			id, folder, "/"+id+".epub").Scan(&file); err != nil {
			t.Fatal(err)
		}
		progress, updated := 1.0, before
		if id == readAgain {
			progress, updated = 0.5, time.Now()
		}
		exec(`INSERT INTO ebook_reader_progress (user_id,profile_id,content_id,file_id,location,progress,updated_at) VALUES ($1,$2,$3,$4,'',$5,$6)`,
			userID, viewer, id, file, progress, updated)
		exec(`INSERT INTO user_history_hidden_items (user_id,profile_id,media_item_id,hidden_before) VALUES ($1,$2,$3,$4)`,
			userID, viewer, id, before.Add(time.Hour))
	}
	ebook := svc.buildEbookExtension(ctx, &models.MediaItem{ContentID: readHidden, Type: "ebook"}, nil,
		AccessFilter{UserID: userID, ProfileID: viewer})
	if ebook.Series == nil {
		t.Fatal("ebook series row missing")
	}
	wantEbooks := map[string]state{readHidden: {}, readAgain: {ratio: 0.5}}
	if got := entryStates(ebook.Series); fmt.Sprint(got) != fmt.Sprint(wantEbooks) {
		t.Errorf("ebook series entries = %v, want %v", got, wantEbooks)
	}

	authors, err := ListBookAuthors(ctx, pool, []string{finished, partway, finished}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || !slices.Equal(authors[finished], []string{p + "-zed", p + "-amy"}) {
		t.Errorf("authors = %v, want only %s: [%s-zed %s-amy]", authors, finished, p, p)
	}
}

func TestSortAudiobookMediaFilesPreservesPresentationPartOrder(t *testing.T) {
	files := []*models.MediaFile{
		{ID: 30, FilePath: "/books/book/03.mp3", PresentationPartIndex: 3},
		{ID: 10, FilePath: "/books/book/01.mp3", PresentationPartIndex: 1},
		{ID: 20, FilePath: "/books/book/02.mp3", PresentationPartIndex: 2},
	}

	sortAudiobookMediaFiles(files)

	got := []int{files[0].ID, files[1].ID, files[2].ID}
	want := []int{10, 20, 30}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted IDs = %v, want %v", got, want)
		}
	}
}

func TestSortAudiobookMediaFilesFallsBackToPath(t *testing.T) {
	files := []*models.MediaFile{
		{ID: 20, FilePath: "/books/book/02.mp3"},
		{ID: 10, FilePath: "/books/book/01.mp3"},
	}

	sortAudiobookMediaFiles(files)

	got := []int{files[0].ID, files[1].ID}
	want := []int{10, 20}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted IDs = %v, want %v", got, want)
		}
	}
}

type recordingCatalogImageResolver struct {
	path    string
	variant string
}

func (r *recordingCatalogImageResolver) ResolveImageURL(_ context.Context, path string, variant string) string {
	r.path = path
	r.variant = variant
	return "resolved://" + path
}

func (r *recordingCatalogImageResolver) ResolveImageURLs(_ context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = r.ResolveImageURL(context.Background(), path, variant)
	}
	return out
}
