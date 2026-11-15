package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
)

// The admin Refresh Metadata actions leave a matched audiobook's match to the
// audiobook enricher: a full library refresh keeps the matched book's title,
// credits, provider IDs and content ID and makes the unmatched book due for
// the next sweep; an item's quick refresh fetches the matched record again in
// place; a reset, and a complete refresh, clear and re-match the book under
// the same content ID. The general metadata refresher, which knows none of
// the audiobook rules, is never called. The stub ffprobe supplies the tags.
func TestAudiobookAdminRefreshKeepsMatchDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	f := newRefreshFixture(t, dsn)
	t.Run("full library refresh waits for an active library scan", f.checkActiveScanBlocksRefresh)
	t.Run("full library refresh", f.checkFullLibraryRefresh)
	t.Run("item quick refresh", f.checkQuickRefresh)
	t.Run("a refresh claim keeps the sweep away", f.checkClaimExcludesSweep)
	t.Run("curator outside the book's libraries", f.checkCuratorScope)
	t.Run("reset whose scan is canceled", f.checkFailedResetScan)
	t.Run("reset retires a cached provider poster", f.checkResetRetiresPoster)
	for _, mode := range []adminjob.ItemRefreshMode{adminjob.ItemRefreshModeReset, adminjob.ItemRefreshModeComplete} {
		t.Run("item "+string(mode)+" refresh", func(t *testing.T) { f.checkResetRefresh(t, mode) })
	}
	f.checkIdentity(t)
}

// refreshFixture is an audiobook library of unchanged books after one
// enrichment sweep matched one of them.
type refreshFixture struct {
	pool         *pgxpool.Pool
	folder       *models.MediaFolder
	probeLog     string
	allParts     []string
	matchedPart  string
	matchedID    string
	unmatchedIDs []string
	userID       int
	provider     *refreshTestProvider
	enricher     *Enricher
	ffprobe      string
	general      *refreshTestGeneralRefresher
	ingester     *libraryingest.Executor
	resolver     *adminjob.ItemRefreshResolver
	scanRuns     *scanqueue.Repository
	library      *adminjob.LibraryRefreshExecutor
	item         *adminjob.ItemRefreshExecutor
}

var refreshTestMatch = refreshTestBook{
	title:   "The Martian",
	credits: []string{"7 Andy Weir", "8 R. C. Bray"},
	ids:     map[string]string{"audible": "B00B5HZGUG"},
	outcome: "success",
}

func newRefreshFixture(t *testing.T, dsn string) *refreshFixture {
	t.Helper()
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := &refreshFixture{pool: pool, provider: &refreshTestProvider{overview: "First overview"}, general: &refreshTestGeneralRefresher{}}

	root, ffprobe, unmatchedParts := f.writeBooks(t)
	f.ffprobe = ffprobe
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name) VALUES ('audiobooks', $1) RETURNING id
	`, fmt.Sprintf("ab-refresh-%d", time.Now().UnixNano())).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1)`, folderID)
		_, _ = pool.Exec(bg, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(bg, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	f.folder = &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}, Enabled: true}
	files := scanner.NewFileRepository(pool)
	f.ingester = libraryingest.NewExecutor(scanner.NewScanner(files, ffprobe, nil, 2, false, time.Hour),
		refreshTestMatcher{}, nil, nil, nil, nil)
	if _, err := f.ingester.IngestFolder(ctx, f.folder); err != nil {
		t.Fatal(err)
	}

	items := catalog.NewItemRepository(pool)
	e := NewEnricher(pool, metadata.NewChainRepository(pool), nil, items,
		catalog.NewPersonRepository(pool), catalog.NewProviderIDRepository(pool))
	e.workers = 1
	f.enricher = e
	e.resolveProviders = func(context.Context, int, string) ([]metadata.Provider, error) {
		return []metadata.Provider{f.provider}, nil
	}
	if _, err := e.Run(ctx); err != nil {
		t.Fatal(err)
	}

	f.matchedID = bookContentID(t, ctx, pool, f.matchedPart)
	for _, p := range unmatchedParts {
		f.unmatchedIDs = append(f.unmatchedIDs, bookContentID(t, ctx, pool, p))
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (role, username) VALUES ('user', $1) RETURNING id`,
		fmt.Sprintf("ab-refresh-%d", folderID)).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, f.userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds)
		VALUES ($1, 'p', $2, 30, 60)`, f.userID, f.matchedID); err != nil {
		t.Fatal(err)
	}
	if got := readRefreshTestBook(t, ctx, pool, f.matchedID); !got.sameMatch(refreshTestMatch) || got.overview != "First overview" {
		t.Fatalf("after the sweep, matched book = %+v, want %+v", got, refreshTestMatch)
	}
	for _, id := range f.unmatchedIDs {
		if got := readRefreshTestBook(t, ctx, pool, id); got.outcome != "no_match" || !got.parked {
			t.Fatalf("after the sweep, unmatched book %s outcome %q parked %v, want a parked no_match", id, got.outcome, got.parked)
		}
	}

	folderRepo := refreshTestFolderRepo{f.folder}
	f.resolver = adminjob.NewItemRefreshResolver(items, nil, nil, folderRepo, files)
	f.scanRuns = scanqueue.NewRepository(pool)
	f.library = adminjob.NewLibraryRefreshExecutor(adminjob.NewPGLibraryRefreshItemLister(pool),
		folderRepo, f.resolver, f.ingester, f.scanRuns, f.general, nil, nil)
	f.library.SetAudiobookEnricher(e)
	f.item = adminjob.NewItemRefreshExecutor(folderRepo, files, nil, nil, nil, nil, nil, f.ingester, f.scanRuns, f.general, nil, nil)
	f.item.SetAudiobookEnricher(e)
	return f
}

// writeBooks writes the library's books and a stub ffprobe that tags them and
// logs every read, so the test sees which books a scan read again.
func (f *refreshFixture) writeBooks(t *testing.T) (root, ffprobe string, unmatchedParts []string) {
	t.Helper()
	root = t.TempDir()
	f.matchedPart = filepath.Join(root, "martian", "part1.mp3")
	// The first unmatched book has no author tag, so the audiobook rules
	// leave it unmatched rather than match it by title alone. The provider
	// does not know the other two.
	unmatchedParts = []string{
		filepath.Join(root, "kings", "part1.mp3"),
		filepath.Join(root, "hail-mary", "part1.mp3"),
		filepath.Join(root, "artemis", "part1.mp3"),
	}
	f.allParts = append([]string{f.matchedPart}, unmatchedParts...)
	for _, p := range f.allParts {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tools := t.TempDir()
	ffprobe = filepath.Join(tools, "ffprobe")
	f.probeLog = filepath.Join(tools, "probes.log")
	writeRefreshTestTool(t, ffprobe, `#!/bin/sh
for last; do :; done
echo "$last" >> '`+f.probeLog+`'
case "$last" in
*martian*) tags='"title":"The Martian","artist":"Andy Weir","narrator":"R. C. Bray"' ;;
*hail-mary*) tags='"title":"Project Hail Mary","artist":"Andy Weir"' ;;
*artemis*) tags='"title":"Artemis","artist":"Andy Weir"' ;;
*) tags='"title":"The Way of Kings"' ;;
esac
cat <<JSON
{"format":{"format_name":"mp3","duration":"60.0","size":"1","bit_rate":"64000","tags":{$tags}},"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3","channels":2}],"chapters":[]}
JSON
`)

	return root, ffprobe, unmatchedParts
}

func (f *refreshFixture) fullRefresh(t *testing.T, progress func(current, total int, message string)) (*adminjob.LibraryRefreshResult, error) {
	return f.library.Execute(t.Context(), adminjob.LibraryRefreshRequest{LibraryID: f.folder.ID, Mode: adminjob.LibraryRefreshModeFull}, progress)
}

// A full refresh does not start a second library scan beside an active one.
func (f *refreshFixture) checkActiveScanBlocksRefresh(t *testing.T) {
	run, created, err := f.scanRuns.Create(t.Context(), scanqueue.CreateInput{LibraryID: f.folder.ID, Mode: scanqueue.ModeLibrary, Trigger: "manual"})
	if err != nil || !created {
		t.Fatalf("queueing a library scan: created %v, err %v", created, err)
	}
	_, err = f.fullRefresh(t, nil)
	if _, _, cancelErr := f.scanRuns.MarkCancelled(t.Context(), run.ID); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if !errors.Is(err, adminjob.ErrLibraryScanInProgress) {
		t.Fatalf("Execute while a library scan is queued = %v, want ErrLibraryScanInProgress", err)
	}
}

// One scan run reads every unchanged book again and reports its progress;
// the matched book keeps its match and the unmatched ones become due. An
// ordinary scan right after skips them all again.
func (f *refreshFixture) checkFullLibraryRefresh(t *testing.T) {
	f.provider.overview = "Library refresh overview"
	truncateProbeLog(t, f.probeLog)
	var progressed bool
	result, err := f.fullRefresh(t, func(_, total int, _ string) { progressed = progressed || total > 0 })
	if err != nil {
		t.Fatal(err)
	}
	if got, want := probedParts(t, f.probeLog), sortedCopy(f.allParts); !slices.Equal(got, want) {
		t.Errorf("books read = %v, want every book %v", got, want)
	}
	var runs int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM scan_runs WHERE media_folder_id = $1 AND trigger = 'admin_library_refresh'`, f.folder.ID).Scan(&runs); err != nil || runs != 1 {
		t.Errorf("refresh scan runs = %d (err %v), want 1", runs, err)
	}
	if !progressed {
		t.Error("the job reported none of the scan's progress")
	}
	if result.PipelineOK != len(f.allParts) || result.PipelineFailed != 0 || result.AudiobooksQueued != len(f.unmatchedIDs) {
		t.Errorf("read %d failed %d queued %d, want %d, 0, %d", result.PipelineOK, result.PipelineFailed, result.AudiobooksQueued, len(f.allParts), len(f.unmatchedIDs))
	}
	if got := readRefreshTestBook(t, t.Context(), f.pool, f.matchedID); !got.sameMatch(refreshTestMatch) || got.overview != "First overview" {
		t.Errorf("matched book = %+v, want %+v with its overview untouched", got, refreshTestMatch)
	}
	for _, id := range f.unmatchedIDs {
		if got := readRefreshTestBook(t, t.Context(), f.pool, id); got.outcome != "" || got.parked || len(got.ids) != 0 {
			t.Errorf("unmatched book %s outcome %q parked %v ids %v, want it due with no match", id, got.outcome, got.parked, got.ids)
		}
	}

	truncateProbeLog(t, f.probeLog)
	if _, err := f.ingester.IngestFolder(t.Context(), f.folder); err != nil {
		t.Fatal(err)
	}
	if got := probedParts(t, f.probeLog); len(got) != 0 {
		t.Errorf("an ordinary scan read %v again, want none", got)
	}
}

func (f *refreshFixture) refreshItem(t *testing.T, mode adminjob.ItemRefreshMode) {
	t.Helper()
	result, err := f.tryRefreshItem(t, mode, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.RefreshContentID != f.matchedID {
		t.Errorf("refresh content ID = %s, want %s", result.RefreshContentID, f.matchedID)
	}
}

func (f *refreshFixture) tryRefreshItem(t *testing.T, mode adminjob.ItemRefreshMode, scope *access.MetadataCurationScope) (*adminjob.ItemRefreshResult, error) {
	t.Helper()
	return f.tryRefreshItemCtx(t, t.Context(), mode, scope)
}

func (f *refreshFixture) tryRefreshItemCtx(t *testing.T, ctx context.Context, mode adminjob.ItemRefreshMode, scope *access.MetadataCurationScope) (*adminjob.ItemRefreshResult, error) {
	t.Helper()
	req, err := f.resolver.ResolveWithMode(t.Context(), f.matchedID, mode)
	if err != nil {
		t.Fatal(err)
	}
	req.CurationScope = scope
	return f.item.Execute(ctx, *req, nil)
}

// While an admin refresh holds a book's claim, the sweep does not claim it.
func (f *refreshFixture) checkClaimExcludesSweep(t *testing.T) {
	unmatched := f.unmatchedIDs[0]
	token, err := f.enricher.ClaimRefresh(t.Context(), unmatched)
	if err != nil || token == "" {
		t.Fatalf("ClaimRefresh = %q, %v", token, err)
	}
	defer f.enricher.ReleaseRefresh(t.Context(), unmatched, token)
	claimed, err := f.enricher.claimBatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range claimed {
		f.enricher.releaseClaim(t.Context(), item)
		if item.ContentID == unmatched {
			t.Error("the sweep claimed a book an admin refresh holds")
		}
	}
}

// A delegated curator refreshes or resets a book only while every library
// holding it is in the curator's scope, so the book is left as it was.
func (f *refreshFixture) checkCuratorScope(t *testing.T) {
	scope := &access.MetadataCurationScope{LibraryIDs: []int{f.folder.ID}}
	f.provider.overview = "Curator overview"
	if _, err := f.tryRefreshItem(t, adminjob.ItemRefreshModeQuick, scope); err != nil {
		t.Fatalf("a refresh inside the curator's scope: %v", err)
	}
	var other int
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO media_folders (type, name) VALUES ('audiobooks', $1) RETURNING id`,
		fmt.Sprintf("ab-refresh-other-%d", time.Now().UnixNano())).Scan(&other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM media_item_libraries WHERE media_folder_id = $1`, other)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, other)
	})
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, f.matchedID, other); err != nil {
		t.Fatal(err)
	}
	before := readRefreshTestBook(t, t.Context(), f.pool, f.matchedID)
	f.provider.overview = "Out of scope overview"
	for _, mode := range []adminjob.ItemRefreshMode{adminjob.ItemRefreshModeQuick, adminjob.ItemRefreshModeReset} {
		if _, err := f.tryRefreshItem(t, mode, scope); !errors.Is(err, access.ErrMetadataCurationScope) {
			t.Errorf("%s refresh of a book in another library = %v, want ErrMetadataCurationScope", mode, err)
		}
		if got := readRefreshTestBook(t, t.Context(), f.pool, f.matchedID); !got.sameMatch(before) || got.overview != before.overview {
			t.Errorf("after a refused %s refresh, book = %+v, want %+v", mode, got, before)
		}
	}
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM media_item_libraries WHERE media_folder_id = $1`, other); err != nil {
		t.Fatal(err)
	}
}

// A reset whose scan is canceled before it reads the book fails and leaves
// the book's match, credits, and locks as they were.
func (f *refreshFixture) checkFailedResetScan(t *testing.T) {
	if _, err := f.pool.Exec(t.Context(), `UPDATE media_items SET locked_fields = '{0}' WHERE content_id = $1`, f.matchedID); err != nil {
		t.Fatal(err)
	}
	working, err := os.ReadFile(f.ffprobe)
	if err != nil {
		t.Fatal(err)
	}
	before := readRefreshTestBook(t, t.Context(), f.pool, f.matchedID)
	// The probe outlasts the job, which is canceled while the scan reads.
	writeRefreshTestTool(t, f.ffprobe, "#!/bin/sh\nsleep 3\n")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	_, err = f.tryRefreshItemCtx(t, ctx, adminjob.ItemRefreshModeReset, nil)
	cancel()
	writeRefreshTestTool(t, f.ffprobe, string(working))
	if err == nil {
		t.Fatal("a reset whose scan could not read the book succeeded")
	}
	got := readRefreshTestBook(t, t.Context(), f.pool, f.matchedID)
	var locked []int
	if err := f.pool.QueryRow(t.Context(), `SELECT locked_fields FROM media_items WHERE content_id = $1`, f.matchedID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if !got.sameMatch(before) || got.overview != before.overview || !slices.Equal(locked, []int{0}) {
		t.Errorf("after a failed reset, book = %+v locks %v, want %+v with its lock", got, locked, before)
	}
	var leased bool
	if err := f.pool.QueryRow(t.Context(), `SELECT COALESCE(lease_until > now(), false) FROM audiobook_enrichment_state WHERE content_id = $1`, f.matchedID).Scan(&leased); err != nil || leased {
		t.Errorf("claim after a failed reset: leased %v (err %v), want released", leased, err)
	}
}

// Clearing a cached provider poster queues its revision for collection in the
// reset's own transaction.
func (f *refreshFixture) checkResetRetiresPoster(t *testing.T) {
	cached := fmt.Sprintf("audiobook-metadata/audiobooks/%s/poster/original.test.webp", f.matchedID)
	if _, err := f.pool.Exec(t.Context(), `
		UPDATE media_items SET poster_path = $2, poster_source_path = 'https://images.example.test/cover.jpg'
		WHERE content_id = $1`, f.matchedID, cached); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, cached)
	})
	f.provider.overview = string(adminjob.ItemRefreshModeReset) + " overview"
	f.refreshItem(t, adminjob.ItemRefreshModeReset)
	var queued bool
	if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM artwork_revision_gc_candidates WHERE original_path = $1)`, cached).Scan(&queued); err != nil || !queued {
		t.Errorf("displaced poster queued for collection = %v (err %v), want true", queued, err)
	}
}

// A quick refresh fetches the matched record again without searching.
func (f *refreshFixture) checkQuickRefresh(t *testing.T) {
	f.provider.overview = "Quick refresh overview"
	searches := f.provider.searches
	f.refreshItem(t, adminjob.ItemRefreshModeQuick)
	if got := readRefreshTestBook(t, t.Context(), f.pool, f.matchedID); !got.sameMatch(refreshTestMatch) || got.overview != "Quick refresh overview" {
		t.Errorf("matched book = %+v, want %+v with the refetched overview", got, refreshTestMatch)
	}
	if f.provider.searches != searches {
		t.Errorf("searches = %d, want %d: a matched book's refresh fetches its record without searching", f.provider.searches, searches)
	}
}

// A reset, and a complete refresh, clear the match and the locked edit and
// match the book again from its tags under the same content ID.
func (f *refreshFixture) checkResetRefresh(t *testing.T, mode adminjob.ItemRefreshMode) {
	if _, err := f.pool.Exec(t.Context(), `UPDATE media_items SET title = 'Edited', locked_fields = '{0}' WHERE content_id = $1`, f.matchedID); err != nil {
		t.Fatal(err)
	}
	f.provider.overview = string(mode) + " overview"
	f.refreshItem(t, mode)
	if got := readRefreshTestBook(t, t.Context(), f.pool, f.matchedID); !got.sameMatch(refreshTestMatch) || got.overview != string(mode)+" overview" {
		t.Errorf("matched book = %+v, want %+v re-matched from its tags", got, refreshTestMatch)
	}
}

// No action called the general refresher or changed a book's identity.
func (f *refreshFixture) checkIdentity(t *testing.T) {
	if len(f.general.calls) > 0 {
		t.Errorf("general metadata refresher called: %v", f.general.calls)
	}
	ids := append([]string{f.matchedID}, f.unmatchedIDs...)
	for i, part := range f.allParts {
		if got := bookContentID(t, t.Context(), f.pool, part); got != ids[i] {
			t.Errorf("%s now links %s, want %s", part, got, ids[i])
		}
		var libraries int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM media_item_libraries WHERE content_id = $1`, ids[i]).Scan(&libraries); err != nil || libraries != 1 {
			t.Errorf("%s library memberships = %d (err %v), want 1", ids[i], libraries, err)
		}
	}
	var position float64
	if err := f.pool.QueryRow(t.Context(), `SELECT position_seconds FROM user_watch_progress WHERE user_id = $1 AND media_item_id = $2`, f.userID, f.matchedID).Scan(&position); err != nil || position != 30 {
		t.Errorf("progress = %v (err %v), want 30 on the same item", position, err)
	}
}

func truncateProbeLog(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// probedParts returns the distinct files the stub ffprobe read, sorted.
func probedParts(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for line := range strings.Lines(string(data)) {
		if line = strings.TrimSpace(line); line != "" && !slices.Contains(parts, line) {
			parts = append(parts, line)
		}
	}
	slices.Sort(parts)
	return parts
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func writeRefreshTestTool(t *testing.T, path, script string) {
	t.Helper()
	// Hold ForkLock so a concurrent fork cannot inherit the open write
	// descriptor and make exec fail with ETXTBSY.
	syscall.ForkLock.Lock()
	err := os.WriteFile(path, []byte(script), 0o755)
	syscall.ForkLock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func bookContentID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, path string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `SELECT content_id FROM media_files WHERE file_path = $1 AND missing_since IS NULL`, path).Scan(&id); err != nil {
		t.Fatalf("reading the content ID of %s: %v", path, err)
	}
	return id
}

type refreshTestBook struct {
	title    string
	overview string
	credits  []string
	ids      map[string]string
	outcome  string
	parked   bool
}

func (b refreshTestBook) sameMatch(want refreshTestBook) bool {
	return b.title == want.title && slices.Equal(b.credits, want.credits) &&
		maps.Equal(b.ids, want.ids) && b.outcome == want.outcome
}

func readRefreshTestBook(t *testing.T, ctx context.Context, pool *pgxpool.Pool, contentID string) refreshTestBook {
	t.Helper()
	var b refreshTestBook
	if err := pool.QueryRow(ctx, `
		SELECT mi.title, COALESCE(mi.overview, ''),
		       ARRAY(SELECT ip.kind::text || ' ' || p.name FROM item_people ip JOIN people p ON p.id = ip.person_id
		             WHERE ip.content_id = mi.content_id ORDER BY ip.kind, ip.sort_order),
		       COALESCE(s.outcome, ''), COALESCE(s.next_attempt_at > now(), false)
		FROM media_items mi
		LEFT JOIN audiobook_enrichment_state s ON s.content_id = mi.content_id
		WHERE mi.content_id = $1
	`, contentID).Scan(&b.title, &b.overview, &b.credits, &b.outcome, &b.parked); err != nil {
		t.Fatalf("reading audiobook %s: %v", contentID, err)
	}
	b.ids = map[string]string{}
	rows, err := pool.Query(ctx, `SELECT provider, provider_id FROM media_item_provider_ids WHERE content_id = $1`, contentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var provider, id string
		if err := rows.Scan(&provider, &id); err != nil {
			t.Fatal(err)
		}
		b.ids[provider] = id
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b
}

// refreshTestProvider knows one book. It answers a search for its title and a
// metadata request for its ID; its overview changes between refreshes.
type refreshTestProvider struct {
	overview string
	searches int
}

func (p *refreshTestProvider) Slug() string       { return "audiobook-stub" }
func (p *refreshTestProvider) Name() string       { return "Audiobook stub" }
func (p *refreshTestProvider) ForTypes() []string { return []string{"audiobook"} }

func (p *refreshTestProvider) Search(_ context.Context, q metadata.SearchQuery) ([]metadata.SearchResult, error) {
	p.searches++
	if !strings.EqualFold(q.Title, "The Martian") {
		return nil, nil
	}
	return []metadata.SearchResult{{Name: "The Martian", ProviderIDs: map[string]string{"audible": "B00B5HZGUG"}}}, nil
}

func (p *refreshTestProvider) GetMetadata(_ context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	if req.ProviderIDs["audible"] != "B00B5HZGUG" {
		return nil, nil
	}
	return &metadata.MetadataResult{
		HasMetadata: true,
		Title:       "The Martian (Unabridged)",
		Overview:    p.overview,
		ProviderIDs: map[string]string{"audible": "B00B5HZGUG"},
		People: []models.ItemPerson{
			{Person: models.Person{Name: "Andy Weir"}, Kind: models.PersonKindAuthor},
			{Person: models.Person{Name: "R. C. Bray"}, Kind: models.PersonKindNarrator},
		},
	}, nil
}

// refreshTestGeneralRefresher stands in for the general metadata service and
// records every call, none of which an audiobook refresh may make.
type refreshTestGeneralRefresher struct{ calls []string }

func (r *refreshTestGeneralRefresher) RefreshItem(_ context.Context, id string) error {
	r.calls = append(r.calls, "RefreshItem "+id)
	return nil
}

func (r *refreshTestGeneralRefresher) RefreshItemForLibrary(_ context.Context, id string, _ int) error {
	r.calls = append(r.calls, "RefreshItemForLibrary "+id)
	return nil
}

func (r *refreshTestGeneralRefresher) RefreshTargetForLibrary(_ context.Context, _, id string, _ int) error {
	r.calls = append(r.calls, "RefreshTargetForLibrary "+id)
	return nil
}

func (r *refreshTestGeneralRefresher) ResetTargetForLibrary(_ context.Context, _, id string, _ int) error {
	r.calls = append(r.calls, "ResetTargetForLibrary "+id)
	return nil
}

type refreshTestFolderRepo struct{ folder *models.MediaFolder }

func (r refreshTestFolderRepo) GetByID(context.Context, int) (*models.MediaFolder, error) {
	return r.folder, nil
}

// refreshTestMatcher stands in for the movie and series matcher, which an
// audiobook library does not use.
type refreshTestMatcher struct{}

func (refreshTestMatcher) ProcessBatchByFolderAndPathPrefix(context.Context, int, string, time.Time) (int, error) {
	return 0, nil
}

func (refreshTestMatcher) ProcessAllByFolderAndPathPrefix(context.Context, int, string, time.Time) (int, error) {
	return 0, nil
}

func (refreshTestMatcher) RetryUnmatchedItemsByFolderAndPathPrefix(context.Context, int, string) (int, int, error) {
	return 0, 0, nil
}
