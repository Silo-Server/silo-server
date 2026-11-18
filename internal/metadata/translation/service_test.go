package translation

import (
	"context"
	"encoding/json"

	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	aitranslate "github.com/Silo-Server/silo-server/internal/ai/translate"
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
	itemErr      error
	libraryItems []string
}

func (c *fakeContent) ItemText(_ context.Context, contentID string) (*ItemText, error) {
	if c.itemErr != nil {
		return nil, c.itemErr
	}
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
func (c *fakeContent) SeasonEpisodeTexts(_ context.Context, seasonID string) ([]ChildText, error) {
	number := -1
	for _, season := range c.seasons {
		if season.ContentID == seasonID {
			number = season.SeasonNumber
		}
	}
	var out []ChildText
	for _, episode := range c.episodes {
		if episode.SeasonNumber == number {
			out = append(out, episode)
		}
	}
	return out, nil
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
	language  string
	overview  string
	tagline   string
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

func (l *fakeLocs) UpsertItemAI(_ context.Context, contentID, language string, overview, tagline *string, _, _ string, _ bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failOnUpsert {
		return fmt.Errorf("boom")
	}
	w := aiWrite{kind: TargetItem, contentID: contentID, language: language}
	if overview != nil {
		w.overview = *overview
	}
	if tagline != nil {
		w.tagline = *tagline
	}
	l.writes = append(l.writes, w)
	return nil
}

func (l *fakeLocs) UpsertSeasonAI(_ context.Context, contentID, language string, overview string, _ string, _ bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failOnUpsert {
		return fmt.Errorf("boom")
	}
	l.writes = append(l.writes, aiWrite{kind: TargetSeason, contentID: contentID, language: language, overview: overview})
	return nil
}

func (l *fakeLocs) UpsertEpisodeAI(_ context.Context, contentID, language string, overview string, _ string, _ bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failOnUpsert {
		return fmt.Errorf("boom")
	}
	l.writes = append(l.writes, aiWrite{kind: TargetEpisode, contentID: contentID, language: language, overview: overview})
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

// newService builds a service with the default config whose AI slot is sem.
func newService(t *testing.T, repo *fakeRepo, content *fakeContent, locs *fakeLocs, chat aitranslate.ChatFn, sem chan struct{}) *Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewService(ctx, Config{Enabled: true, Configured: true, ChatModel: "test-model"}, repo, content, locs, chat, sem, nil)
}

// queuedService builds a service whose only AI slot is taken, so every job it
// accepts stays queued.
func queuedService(t *testing.T, repo *fakeRepo, content *fakeContent, locs *fakeLocs, chat *upperChat) *Service {
	t.Helper()
	sem := jobrunner.NewSemaphore(1)
	sem <- struct{}{}
	svc := newService(t, repo, content, locs, chat.fn, sem)
	t.Cleanup(func() { <-sem })
	return svc
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

// AC4: a forced re-translation refreshes earlier AI text only. Provider and
// manual text is never replaced.
func TestForceRetranslatesAIButNeverProviderOrManual(t *testing.T) {
	repo, content, chat := newFakeRepo(), seriesContent(), &upperChat{}
	locs := &fakeLocs{
		itemLoc: &models.MediaItemLocalization{
			Overview: "Vieux.", OverviewSource: "provider",
			Tagline: "Manuel.", TaglineSource: "manual",
		},
		seasonLocs: map[string]*models.SeasonLocalization{
			"sea1": {Overview: "Fournisseur.", OverviewSource: "provider"},
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
	if final.Status != jobrunner.StatusCompleted || final.FieldsTotal != 1 || final.FieldsDone != 1 {
		t.Fatalf("job = %+v, want only the AI episode overview translated", final)
	}
	writes := locs.allWrites()
	if len(writes) != 1 || writes[0].kind != TargetEpisode || writes[0].contentID != "ep1" {
		t.Fatalf("writes = %+v, want only ep1", writes)
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

func TestLibraryPrewarmStopsOnProviderFailure(t *testing.T) {
	repo, content, locs := newFakeRepo(), seriesContent(), &fakeLocs{}
	content.libraryItems = []string{"series1", "series1", "series1", "series1"}
	var calls atomic.Int32
	failing := func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return "", fmt.Errorf("quota exceeded")
	}
	svc := newService(t, repo, content, locs, failing, jobrunner.NewSemaphore(1))

	job, err := svc.EnqueueLibrary(context.Background(), 7, "fr", nil)
	if err != nil {
		t.Fatalf("EnqueueLibrary: %v", err)
	}
	waitDone(t, repo)
	final := repo.job(job.ID)
	if final.Status != jobrunner.StatusFailed || !strings.Contains(final.ErrorMessage, "quota exceeded") {
		t.Fatalf("final = %+v, want failed with the provider error", final)
	}
	if got := calls.Load(); got != libraryMaxFailuresInRow {
		t.Fatalf("provider calls = %d, want the job to stop after %d failed items in a row", got, libraryMaxFailuresInRow)
	}
	if len(locs.allWrites()) != 0 {
		t.Fatalf("writes = %+v, want none", locs.allWrites())
	}
}

// Between items a prewarm hands its AI slot to a job queued behind it.
func TestLibraryPrewarmYieldsItsSlotBetweenItems(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo, content, locs := newFakeRepo(), seriesContent(), &fakeLocs{}
		content.libraryItems = []string{"series1", "series1"}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		release := make(chan struct{})
		var calls atomic.Int32
		chat := func(ctx context.Context, system, user string) (string, error) {
			if calls.Add(1) == 1 {
				<-release
			}
			return (&upperChat{}).fn(ctx, system, user)
		}
		cfg := Config{Enabled: true, Configured: true, ChatModel: "test-model"}
		sem := jobrunner.NewSemaphore(1)
		svc := NewService(ctx, cfg, repo, content, locs, chat, sem, nil)
		if _, err := svc.EnqueueLibrary(context.Background(), 7, "fr", nil); err != nil {
			t.Fatalf("EnqueueLibrary: %v", err)
		}
		synctest.Wait() // the prewarm holds the slot, translating its first item

		// Another job queues for the only slot while the first item translates.
		admitted := make(chan struct{})
		go func() {
			sem <- struct{}{}
			close(admitted)
		}()
		synctest.Wait()
		close(release)
		synctest.Wait()
		select {
		case <-admitted:
		default:
			t.Fatal("the queued job did not get the slot between items")
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("provider calls = %d, want the queued job admitted before the second item", got)
		}
		<-sem
		waitDone(t, repo)
		if got := calls.Load(); got != 2 {
			t.Fatalf("provider calls = %d, want the second item translated after the other job", got)
		}
	})
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

// A series-only on-view job must not swallow a later full-series request.
func TestEnqueueStartsAFullSeriesJobWhileASeriesOnlyJobRuns(t *testing.T) {
	repo, content, locs, chat := newFakeRepo(), seriesContent(), &fakeLocs{}, &upperChat{}
	svc := queuedService(t, repo, content, locs, chat)
	narrow, err := svc.Enqueue(context.Background(), JobRequest{ContentID: "series1", TargetLanguage: "fr"})
	if err != nil {
		t.Fatal(err)
	}
	full, err := svc.Enqueue(context.Background(), JobRequest{ContentID: "series1", TargetLanguage: "fr", IncludeChildren: true})
	if err != nil {
		t.Fatal(err)
	}
	if full.ID == narrow.ID || !full.IncludeChildren {
		t.Fatalf("full-series request got %+v, want a new job with children", full)
	}
}
