package translation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/models"
)

// --- fakes ---

type fakeRepo struct {
	mu        sync.Mutex
	nextID    int64
	jobs      map[int64]*Job
	completed chan int64
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{jobs: map[int64]*Job{}, completed: make(chan int64, 16)}
}

func (r *fakeRepo) InsertJob(_ context.Context, job *Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	job.ID = r.nextID
	cp := *job
	r.jobs[job.ID] = &cp
	return nil
}

func (r *fakeRepo) GetJob(_ context.Context, id int64) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		cp := *j
		return &cp, nil
	}
	return nil, nil
}

func (r *fakeRepo) GetActiveJobByIdempotencyKey(_ context.Context, key string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.IdempotencyKey == key && !j.Status.Terminal() {
			cp := *j
			return &cp, nil
		}
	}
	return nil, nil
}

func (r *fakeRepo) ListJobsByContent(_ context.Context, contentID string) ([]Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Job
	// Newest first, matching the SQL implementation's ORDER BY created_at DESC.
	for id := r.nextID; id >= 1; id-- {
		if j, ok := r.jobs[id]; ok && j.ContentID == contentID {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (r *fakeRepo) UpdateProgress(_ context.Context, id int64, status JobStatus, progress float64, message string, done, total int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok && !j.Status.Terminal() {
		j.Status, j.Progress, j.ProgressMessage, j.FieldsDone, j.FieldsTotal = status, progress, message, done, total
	}
	return nil
}

func (r *fakeRepo) CompleteJob(_ context.Context, id int64, message string, done, total int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok && !j.Status.Terminal() {
		j.Status, j.Progress, j.ProgressMessage, j.FieldsDone, j.FieldsTotal = jobrunner.StatusCompleted, 1, message, done, total
	}
	select {
	case r.completed <- id:
	default:
	}
	return nil
}

func (r *fakeRepo) FailJob(_ context.Context, id int64, status JobStatus, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok && !j.Status.Terminal() {
		j.Status, j.ErrorMessage = status, message
	}
	select {
	case r.completed <- id:
	default:
	}
	return nil
}

func (r *fakeRepo) Heartbeat(context.Context, int64) error { return nil }
func (r *fakeRepo) ResetStaleJobs(context.Context, time.Time, string) (int64, error) {
	return 0, nil
}

func (r *fakeRepo) job(id int64) Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.jobs[id]
}

type fakeContent struct {
	item         *ItemText
	seasons      []ChildText
	episodes     []ChildText
	missing      int
	libraryItems []string
}

func (c *fakeContent) ItemText(_ context.Context, contentID string) (*ItemText, error) {
	if c.item == nil || contentID != c.item.ContentID {
		return nil, nil
	}
	return c.item, nil
}
func (c *fakeContent) SeasonTexts(context.Context, string) ([]ChildText, error) {
	return c.seasons, nil
}
func (c *fakeContent) EpisodeTexts(context.Context, string) ([]ChildText, error) {
	return c.episodes, nil
}
func (c *fakeContent) SeasonByID(context.Context, string) (*ChildText, string, error) {
	if len(c.seasons) == 0 {
		return nil, "", nil
	}
	return &c.seasons[0], c.item.ContentID, nil
}
func (c *fakeContent) EpisodeByID(context.Context, string) (*ChildText, string, error) {
	if len(c.episodes) == 0 {
		return nil, "", nil
	}
	return &c.episodes[0], c.item.ContentID, nil
}
func (c *fakeContent) CountMissingFields(context.Context, string, string) (int, error) {
	return c.missing, nil
}
func (c *fakeContent) LibraryItemsMissing(context.Context, int, string) ([]string, error) {
	return c.libraryItems, nil
}

type aiWrite struct {
	kind      TargetKind
	contentID string
	overview  string
	tagline   string
	force     bool
}

type fakeLocs struct {
	mu           sync.Mutex
	itemLoc      *models.MediaItemLocalization
	seasonLocs   map[string]*models.SeasonLocalization
	episodeLocs  map[string]*models.EpisodeLocalization
	writes       []aiWrite
	failOnUpsert bool
}

func (l *fakeLocs) ItemLocalization(context.Context, string, string) (*models.MediaItemLocalization, error) {
	return l.itemLoc, nil
}

func (l *fakeLocs) SeasonLocalizations(context.Context, []string, string) (map[string]*models.SeasonLocalization, error) {
	if l.seasonLocs == nil {
		return map[string]*models.SeasonLocalization{}, nil
	}
	return l.seasonLocs, nil
}

func (l *fakeLocs) EpisodeLocalizations(context.Context, []string, string) (map[string]*models.EpisodeLocalization, error) {
	if l.episodeLocs == nil {
		return map[string]*models.EpisodeLocalization{}, nil
	}
	return l.episodeLocs, nil
}

func (l *fakeLocs) UpsertItemAI(_ context.Context, contentID, _ string, overview, tagline *string, force bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failOnUpsert {
		return fmt.Errorf("boom")
	}
	w := aiWrite{kind: TargetItem, contentID: contentID, force: force}
	if overview != nil {
		w.overview = *overview
	}
	if tagline != nil {
		w.tagline = *tagline
	}
	l.writes = append(l.writes, w)
	return nil
}

func (l *fakeLocs) UpsertSeasonAI(_ context.Context, contentID, _ string, overview string, force bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failOnUpsert {
		return fmt.Errorf("boom")
	}
	l.writes = append(l.writes, aiWrite{kind: TargetSeason, contentID: contentID, overview: overview, force: force})
	return nil
}

func (l *fakeLocs) UpsertEpisodeAI(_ context.Context, contentID, _ string, overview string, force bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failOnUpsert {
		return fmt.Errorf("boom")
	}
	l.writes = append(l.writes, aiWrite{kind: TargetEpisode, contentID: contentID, overview: overview, force: force})
	return nil
}

func (l *fakeLocs) allWrites() []aiWrite {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]aiWrite(nil), l.writes...)
}

// upperChat "translates" by upper-casing each indexed value; counts calls.
type upperChat struct {
	mu    sync.Mutex
	calls int
}

func (c *upperChat) fn(_ context.Context, _ string, user string) (string, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	start := strings.IndexByte(user, '{')
	var m map[string]string
	if err := json.Unmarshal([]byte(user[start:]), &m); err != nil {
		return "", err
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = strings.ToUpper(v)
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (c *upperChat) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// --- helpers ---

func testService(t *testing.T, repo *fakeRepo, content *fakeContent, locs *fakeLocs, chat *upperChat) *Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := Config{Enabled: true, Configured: true, ChatModel: "test-model", OnView: "auto"}
	return NewService(ctx, cfg, repo, content, locs, chat.fn, jobrunner.NewSemaphore(1), nil)
}

func waitDone(t *testing.T, repo *fakeRepo) int64 {
	t.Helper()
	select {
	case id := <-repo.completed:
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("job never reached a terminal state")
		return 0
	}
}

func seriesContent() *fakeContent {
	return &fakeContent{
		item: &ItemText{
			ContentID: "series1", Type: "series", Title: "Test Show", Year: 2020,
			Overview: "A show.", Tagline: "Watch it.", DefaultLanguage: "en",
		},
		seasons: []ChildText{
			{ContentID: "sea1", SeasonNumber: 1, Overview: "Season one."},
			{ContentID: "sea2", SeasonNumber: 2, Overview: ""}, // no base text
		},
		episodes: []ChildText{
			{ContentID: "ep1", SeasonNumber: 1, EpisodeNumber: 1, Overview: "Pilot episode."},
			{ContentID: "ep2", SeasonNumber: 1, EpisodeNumber: 2, Overview: "Second episode."},
		},
	}
}

// --- tests ---

func TestSeriesJobExpandsChildrenAndPersists(t *testing.T) {
	repo, content, locs, chat := newFakeRepo(), seriesContent(), &fakeLocs{}, &upperChat{}
	svc := testService(t, repo, content, locs, chat)

	job, err := svc.Enqueue(context.Background(), JobRequest{
		ContentID: "series1", TargetLanguage: "fr", IncludeChildren: true,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitDone(t, repo)

	final := repo.job(job.ID)
	if final.Status != jobrunner.StatusCompleted {
		t.Fatalf("status = %s (%s)", final.Status, final.ErrorMessage)
	}
	// item overview + tagline + 1 season with text + 2 episodes = 5 fields.
	if final.FieldsTotal != 5 || final.FieldsDone != 5 {
		t.Errorf("fields = %d/%d, want 5/5", final.FieldsDone, final.FieldsTotal)
	}
	writes := locs.allWrites()
	if len(writes) != 5 {
		t.Fatalf("writes = %d, want 5: %+v", len(writes), writes)
	}
	if writes[0].kind != TargetItem || writes[0].overview != "A SHOW." {
		t.Errorf("first write = %+v", writes[0])
	}
	var sawSeason, sawTagline bool
	for _, w := range writes {
		if w.kind == TargetSeason && w.contentID == "sea1" && w.overview == "SEASON ONE." {
			sawSeason = true
		}
		if w.kind == TargetItem && w.tagline == "WATCH IT." {
			sawTagline = true
		}
		if w.contentID == "sea2" {
			t.Error("season without base text was translated")
		}
	}
	if !sawSeason || !sawTagline {
		t.Errorf("missing expected writes: %+v", writes)
	}
}

func TestSkipIfFilledShortCircuitsWithoutModelCalls(t *testing.T) {
	repo, content, chat := newFakeRepo(), seriesContent(), &upperChat{}
	locs := &fakeLocs{
		itemLoc: &models.MediaItemLocalization{
			Overview: "Déjà traduit.", OverviewSource: "provider",
			Tagline: "Déjà.", TaglineSource: "ai",
		},
		seasonLocs: map[string]*models.SeasonLocalization{
			"sea1": {Overview: "Saison.", OverviewSource: "ai"},
		},
		episodeLocs: map[string]*models.EpisodeLocalization{
			"ep1": {Overview: "Pilote.", OverviewSource: "provider"},
			"ep2": {Overview: "Deux.", OverviewSource: "manual"},
		},
	}
	svc := testService(t, repo, content, locs, chat)

	job, err := svc.Enqueue(context.Background(), JobRequest{
		ContentID: "series1", TargetLanguage: "fr", IncludeChildren: true,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitDone(t, repo)

	final := repo.job(job.ID)
	if final.Status != jobrunner.StatusCompleted || final.FieldsTotal != 0 {
		t.Fatalf("job = %+v, want completed with 0 fields", final)
	}
	if chat.callCount() != 0 {
		t.Errorf("model called %d times for a fully translated item", chat.callCount())
	}
}

func TestForceRetranslatesProviderAndAIButNeverManual(t *testing.T) {
	repo, content, chat := newFakeRepo(), seriesContent(), &upperChat{}
	locs := &fakeLocs{
		itemLoc: &models.MediaItemLocalization{
			Overview: "Vieux.", OverviewSource: "provider",
			Tagline: "Manuel.", TaglineSource: "manual",
		},
		episodeLocs: map[string]*models.EpisodeLocalization{
			"ep1": {Overview: "IA.", OverviewSource: "ai"},
			"ep2": {Overview: "Manuel.", OverviewSource: "manual"},
		},
	}
	svc := testService(t, repo, content, locs, chat)

	job, err := svc.Enqueue(context.Background(), JobRequest{
		ContentID: "series1", TargetLanguage: "fr", IncludeChildren: true, Force: true,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitDone(t, repo)

	final := repo.job(job.ID)
	// item overview (provider, forced) + sea1 + ep1 (ai, forced) = 3;
	// tagline and ep2 are manual and stay untouched even with force.
	if final.FieldsTotal != 3 {
		t.Fatalf("fields_total = %d, want 3", final.FieldsTotal)
	}
	if final.Status != jobrunner.StatusCompleted || final.FieldsDone != 3 {
		t.Fatalf("completed job = %+v, want 3 fields done", final)
	}
	writes := locs.allWrites()
	if len(writes) != 3 {
		t.Fatalf("writes = %+v, want 3", writes)
	}
	for _, w := range writes {
		if !w.force {
			t.Errorf("write without force flag: %+v", w)
		}
		if w.tagline != "" || w.contentID == "ep2" {
			t.Errorf("manual field was translated: %+v", w)
		}
	}
}

func TestPersistFailureFailsJob(t *testing.T) {
	repo, content, chat := newFakeRepo(), seriesContent(), &upperChat{}
	locs := &fakeLocs{failOnUpsert: true}
	svc := testService(t, repo, content, locs, chat)

	job, err := svc.Enqueue(context.Background(), JobRequest{
		ContentID: "series1", TargetLanguage: "fr", IncludeChildren: true,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitDone(t, repo)

	final := repo.job(job.ID)
	if final.Status != jobrunner.StatusFailed || !strings.Contains(final.ErrorMessage, "boom") {
		t.Fatalf("job = %+v, want failed with persist error", final)
	}
}

func TestEnqueueDeduplicatesActiveJobs(t *testing.T) {
	repo, content, locs, chat := newFakeRepo(), seriesContent(), &fakeLocs{}, &upperChat{}
	// Block the runner so the first job stays active during the second enqueue.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sem := jobrunner.NewSemaphore(1)
	sem <- struct{}{} // occupy the only slot
	cfg := Config{Enabled: true, Configured: true, ChatModel: "test-model"}
	svc := NewService(ctx, cfg, repo, content, locs, chat.fn, sem, nil)

	first, err := svc.Enqueue(context.Background(), JobRequest{ContentID: "series1", TargetLanguage: "fr"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	second, err := svc.Enqueue(context.Background(), JobRequest{ContentID: "series1", TargetLanguage: "fr"})
	if err != nil {
		t.Fatalf("Enqueue dup: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("duplicate enqueue created job %d, want %d", second.ID, first.ID)
	}
	<-sem // release so cleanup can proceed
}

func TestAutoEnqueueSkipsWhenNothingMissing(t *testing.T) {
	repo, content, locs, chat := newFakeRepo(), seriesContent(), &fakeLocs{}, &upperChat{}
	content.missing = 0
	svc := testService(t, repo, content, locs, chat)

	svc.AutoEnqueue(context.Background(), "series1", "fr")
	if len(repo.jobs) != 0 {
		t.Errorf("AutoEnqueue created a job with nothing missing")
	}

	content.missing = 3
	svc.AutoEnqueue(context.Background(), "series1", "fr")
	if len(repo.jobs) != 1 {
		t.Errorf("AutoEnqueue did not create a job with fields missing")
	}
	waitDone(t, repo)
}

func TestEnqueueValidatesInput(t *testing.T) {
	repo, content, locs, chat := newFakeRepo(), seriesContent(), &fakeLocs{}, &upperChat{}
	svc := testService(t, repo, content, locs, chat)

	if _, err := svc.Enqueue(context.Background(), JobRequest{ContentID: "x", TargetLanguage: "definitely-not-a-language"}); err == nil {
		t.Error("invalid language accepted")
	}
	if _, err := svc.Enqueue(context.Background(), JobRequest{TargetLanguage: "fr"}); err == nil {
		t.Error("missing content id accepted")
	}
	if _, err := svc.Enqueue(context.Background(), JobRequest{ContentID: "x", TargetKind: "bogus", TargetLanguage: "fr"}); err == nil {
		t.Error("bogus target kind accepted")
	}

	disabled := NewService(context.Background(), Config{}, repo, content, locs, chat.fn, nil, nil)
	if _, err := disabled.Enqueue(context.Background(), JobRequest{ContentID: "x", TargetLanguage: "fr"}); err != ErrNotConfigured {
		t.Errorf("disabled service err = %v, want ErrNotConfigured", err)
	}
}

func TestRequestOnViewCooldownAndGating(t *testing.T) {
	repo, content, locs, chat := newFakeRepo(), seriesContent(), &fakeLocs{}, &upperChat{}
	svc := testService(t, repo, content, locs, chat)

	// A recently failed job for the same target+language suppresses re-enqueue.
	failed := &Job{
		ContentID: "series1", TargetKind: TargetItem, TargetLanguage: "fr",
		Status: jobrunner.StatusFailed, UpdatedAt: time.Now().Add(-time.Minute),
	}
	if err := repo.InsertJob(context.Background(), failed); err != nil {
		t.Fatalf("seed failed job: %v", err)
	}
	repo.mu.Lock()
	repo.jobs[failed.ID].Status = jobrunner.StatusFailed
	repo.jobs[failed.ID].UpdatedAt = time.Now().Add(-time.Minute)
	repo.mu.Unlock()

	job, err := svc.RequestOnView(context.Background(), TargetItem, "series1", "fr", nil)
	if err != nil {
		t.Fatalf("RequestOnView: %v", err)
	}
	if job.ID != failed.ID || job.Status != jobrunner.StatusFailed {
		t.Fatalf("cooldown not applied: got job %d status %s", job.ID, job.Status)
	}

	// Once the failure has aged past the cooldown, a new job is enqueued.
	repo.mu.Lock()
	repo.jobs[failed.ID].UpdatedAt = time.Now().Add(-2 * onViewFailureCooldown)
	repo.mu.Unlock()
	job, err = svc.RequestOnView(context.Background(), TargetItem, "series1", "fr", nil)
	if err != nil {
		t.Fatalf("RequestOnView after cooldown: %v", err)
	}
	if job.ID == failed.ID {
		t.Fatal("stale failed job returned after cooldown expired")
	}
	waitDone(t, repo)

	// A different language is unaffected by the failure.
	job2, err := svc.RequestOnView(context.Background(), TargetItem, "series1", "de", nil)
	if err != nil || job2.ID == failed.ID {
		t.Fatalf("other-language request blocked: job=%v err=%v", job2, err)
	}
	waitDone(t, repo)

	// OnView off (zero-value config) refuses viewer requests outright.
	off := NewService(context.Background(), Config{Enabled: true, Configured: true, ChatModel: "m"}, repo, content, locs, chat.fn, nil, nil)
	if _, err := off.RequestOnView(context.Background(), TargetItem, "series1", "fr", nil); err != ErrNotConfigured {
		t.Errorf("on_view=off err = %v, want ErrNotConfigured", err)
	}
}

func TestRequestOnViewUsesRequestedTargetKind(t *testing.T) {
	repo, content, locs, chat := newFakeRepo(), seriesContent(), &fakeLocs{}, &upperChat{}
	svc := testService(t, repo, content, locs, chat)

	seasonJob, err := svc.RequestOnView(context.Background(), TargetSeason, "sea1", "fr", nil)
	if err != nil {
		t.Fatalf("RequestOnView season: %v", err)
	}
	waitDone(t, repo)
	if final := repo.job(seasonJob.ID); final.TargetKind != TargetSeason || final.IncludeChildren {
		t.Fatalf("season job = %+v, want target season without children", final)
	}

	episodeJob, err := svc.RequestOnView(context.Background(), TargetEpisode, "ep1", "fr", nil)
	if err != nil {
		t.Fatalf("RequestOnView episode: %v", err)
	}
	waitDone(t, repo)
	if final := repo.job(episodeJob.ID); final.TargetKind != TargetEpisode || final.IncludeChildren {
		t.Fatalf("episode job = %+v, want target episode without children", final)
	}

	writes := locs.allWrites()
	var sawSeason, sawEpisode bool
	for _, write := range writes {
		if write.kind == TargetSeason && write.contentID == "sea1" && write.overview == "SEASON ONE." {
			sawSeason = true
		}
		if write.kind == TargetEpisode && write.contentID == "ep1" && write.overview == "PILOT EPISODE." {
			sawEpisode = true
		}
	}
	if !sawSeason || !sawEpisode {
		t.Fatalf("missing target-kind writes: %+v", writes)
	}
}

func TestLibraryPrewarmTranslatesEachMissingItem(t *testing.T) {
	repo, content, locs, chat := newFakeRepo(), seriesContent(), &fakeLocs{}, &upperChat{}
	// "removed" disappeared after the walk listed it and is skipped.
	content.libraryItems = []string{"removed", "series1"}
	svc := testService(t, repo, content, locs, chat)

	job, err := svc.EnqueueLibrary(context.Background(), 7, "fr", nil)
	if err != nil {
		t.Fatalf("EnqueueLibrary: %v", err)
	}
	if job.TargetKind != TargetLibrary || job.ContentID != "library:7" || job.Force || !job.IncludeChildren {
		t.Fatalf("job = %+v, want an unforced library job for library:7", job)
	}
	waitDone(t, repo)

	final := repo.job(job.ID)
	if final.Status != jobrunner.StatusCompleted || final.FieldsDone != 2 || final.FieldsTotal != 2 {
		t.Fatalf("final = %+v, want completed with 2 of 2 items", final)
	}
	// The series' overview and tagline, season 1, and both episodes.
	if got := len(locs.allWrites()); got != 5 {
		t.Fatalf("writes = %d (%+v), want 5", got, locs.allWrites())
	}
	for _, write := range locs.allWrites() {
		if write.force {
			t.Fatalf("library prewarm forced a write: %+v", write)
		}
	}

	// A repeat while nothing is running starts a new job; a prewarm request
	// cannot ask for force.
	if _, err := svc.Enqueue(context.Background(), JobRequest{TargetKind: TargetLibrary, ContentID: "library:x", TargetLanguage: "fr"}); err == nil {
		t.Fatal("Enqueue accepted an invalid library target")
	}
	forced, err := svc.Enqueue(context.Background(), JobRequest{TargetKind: TargetLibrary, ContentID: "library:7", TargetLanguage: "de", Force: true})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if forced.Force {
		t.Fatalf("library job kept force: %+v", forced)
	}
	waitDone(t, repo)
}

func TestLibraryPrewarmStopsOnProviderFailure(t *testing.T) {
	repo, content, locs := newFakeRepo(), seriesContent(), &fakeLocs{}
	content.libraryItems = []string{"series1", "series1"}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	failing := func(context.Context, string, string) (string, error) {
		return "", fmt.Errorf("quota exceeded")
	}
	cfg := Config{Enabled: true, Configured: true, ChatModel: "test-model"}
	svc := NewService(ctx, cfg, repo, content, locs, failing, jobrunner.NewSemaphore(1), nil)

	job, err := svc.EnqueueLibrary(context.Background(), 7, "fr", nil)
	if err != nil {
		t.Fatalf("EnqueueLibrary: %v", err)
	}
	waitDone(t, repo)
	final := repo.job(job.ID)
	if final.Status != jobrunner.StatusFailed || !strings.Contains(final.ErrorMessage, "quota exceeded") {
		t.Fatalf("final = %+v, want failed with the provider error", final)
	}
	if final.FieldsDone != 0 {
		t.Fatalf("progress = %d items, want the first item to stop the job", final.FieldsDone)
	}
}

// A cancel recorded by another node stops the prewarm before its next item.
func TestLibraryPrewarmStopsWhenCanceledElsewhere(t *testing.T) {
	repo, content, locs := newFakeRepo(), seriesContent(), &fakeLocs{}
	content.libraryItems = []string{"series1", "series1", "series1"}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var calls atomic.Int32
	jobIDs := make(chan int64, 1)
	started := make(chan struct{})
	var jobID int64
	chat := func(_ context.Context, _ string, user string) (string, error) {
		if calls.Add(1) == 1 {
			defer close(started)
			jobID = <-jobIDs
			// The first item is in flight when another node cancels the job.
			repo.mu.Lock()
			repo.jobs[jobID].Status = jobrunner.StatusCancelled
			repo.mu.Unlock()
		}
		return (&upperChat{}).fn(ctx, "", user)
	}
	cfg := Config{Enabled: true, Configured: true, ChatModel: "test-model"}
	sem := jobrunner.NewSemaphore(1)
	sem <- struct{}{} // hold the job until its ID is known
	svc := NewService(ctx, cfg, repo, content, locs, chat, sem, nil)
	job, err := svc.EnqueueLibrary(context.Background(), 7, "fr", nil)
	if err != nil {
		t.Fatalf("EnqueueLibrary: %v", err)
	}
	jobIDs <- job.ID
	<-sem
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the prewarm never called the provider")
	}

	// The runner frees its slot when the job returns.
	select {
	case sem <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled prewarm kept running")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want the prewarm to stop after the item in flight", got)
	}
	if got := repo.job(job.ID).Status; got != jobrunner.StatusCancelled {
		t.Fatalf("status = %s, want it left canceled", got)
	}
}
