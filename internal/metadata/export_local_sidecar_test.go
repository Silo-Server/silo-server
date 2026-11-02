package metadata

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Helpers for the external local sidecar tests (local_sidecar_nfo_test.go),
// which import the real NFO provider and so live in package metadata_test.

// fakeLocalSidecarStore keeps fingerprints in memory and answers content
// listings from the harness's fake file, item, season and episode repos.
type fakeLocalSidecarStore struct {
	x *NFOSeriesHarness

	mu      sync.Mutex
	records map[string]localSidecarRecord
	// onLock, when set, runs once as the next item lock is taken, standing
	// in for another node that held the lock first.
	onLock func()
	// busy makes every item lock read as held by another check.
	busy    bool
	upserts int
	// onUpsert, when set, runs once before the next write is applied,
	// standing in for another check that stores first.
	onUpsert func()
}

func localSidecarRecordKey(folderID int, targetType, contentID string) string {
	return fmt.Sprintf("%d|%s|%s", folderID, targetType, contentID)
}

func (f *fakeLocalSidecarStore) ListScopeContents(_ context.Context, folderID int, scopePath string) ([]localSidecarContent, error) {
	scoped := make(map[string]bool)
	for _, file := range f.files(folderID) {
		if file.path == scopePath || strings.HasPrefix(file.path, strings.TrimSuffix(scopePath, "/")+"/") {
			scoped[file.contentID] = true
		}
	}
	return f.contents(folderID, func(contentID string) bool { return scoped[contentID] }), nil
}

func (f *fakeLocalSidecarStore) ListContents(_ context.Context, contentID string) ([]localSidecarContent, error) {
	folders := make(map[int]bool)
	for _, file := range f.files(0) {
		if file.contentID == contentID {
			folders[file.folderID] = true
		}
	}
	var out []localSidecarContent
	for folderID := range folders {
		out = append(out, f.contents(folderID, func(id string) bool { return id == contentID })...)
	}
	return out, nil
}

type fakeSidecarFile struct {
	folderID     int
	contentID    string
	path         string
	observedRoot string
}

func (f *fakeLocalSidecarStore) files(folderID int) []fakeSidecarFile {
	repo := f.x.h.fileRepo
	repo.mu.Lock()
	defer repo.mu.Unlock()
	var out []fakeSidecarFile
	for _, files := range repo.groupFiles {
		for _, file := range files {
			if file == nil || (folderID > 0 && file.MediaFolderID != folderID) {
				continue
			}
			out = append(out, fakeSidecarFile{
				folderID:     file.MediaFolderID,
				contentID:    repo.contentIDs[file.ID],
				path:         file.FilePath,
				observedRoot: file.ObservedRootPath,
			})
		}
	}
	return out
}

func (f *fakeLocalSidecarStore) contents(folderID int, include func(string) bool) []localSidecarContent {
	byID := make(map[string]*localSidecarContent)
	var order []string
	for _, file := range f.files(folderID) {
		if !include(file.contentID) {
			continue
		}
		item, err := f.x.h.itemRepo.GetByID(context.Background(), file.contentID)
		if err != nil || item.Status != "matched" {
			continue
		}
		content := byID[file.contentID]
		if content == nil {
			content = &localSidecarContent{FolderID: folderID, ContentID: file.contentID, Type: item.Type, File: file.path}
			byID[file.contentID] = content
			order = append(order, file.contentID)
		}
		content.Dirs = append(content.Dirs, filepath.Dir(file.path), file.observedRoot)
	}
	out := make([]localSidecarContent, 0, len(order))
	for _, id := range order {
		content := *byID[id]
		content.Dirs = compactUniqueFilePaths(content.Dirs)
		out = append(out, content)
	}
	return out
}

func (f *fakeLocalSidecarStore) Get(_ context.Context, folderID int, targetType string, contentIDs []string) (map[string]localSidecarRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]localSidecarRecord)
	for _, id := range contentIDs {
		if record, ok := f.records[localSidecarRecordKey(folderID, targetType, id)]; ok {
			out[id] = record
		}
	}
	return out, nil
}

func (f *fakeLocalSidecarStore) Upsert(ctx context.Context, records []localSidecarRecord) error {
	if err := ctx.Err(); err != nil {
		// The database rejects a write on a canceled context.
		return err
	}
	f.mu.Lock()
	onUpsert := f.onUpsert
	f.onUpsert = nil
	f.mu.Unlock()
	if onUpsert != nil {
		onUpsert()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts++
	for _, record := range records {
		key := localSidecarRecordKey(record.FolderID, record.TargetType, record.ContentID)
		stored, exists := f.records[key]
		switch {
		case record.Baseline && exists:
			continue
		case record.ifFingerprint != nil && (!exists || stored.Fingerprint != *record.ifFingerprint || stored.ItemID != record.ItemID):
			continue
		}
		record.Baseline, record.ifFingerprint = false, nil
		f.records[key] = record
	}
	return nil
}

func (f *fakeLocalSidecarStore) AdvanceFolderDigest(ctx context.Context, folderID int, contentID, fingerprint, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := localSidecarRecordKey(folderID, RefreshTargetItem, contentID)
	if record, ok := f.records[key]; ok && record.Fingerprint == fingerprint {
		record.FolderDigest = digest
		f.records[key] = record
	}
	return nil
}

func (f *fakeLocalSidecarStore) ContentIDOfFile(_ context.Context, folderID int, path string) (string, error) {
	for _, file := range f.files(folderID) {
		if file.path == path {
			return file.contentID, nil
		}
	}
	return "", nil
}

func (f *fakeLocalSidecarStore) LockItem(context.Context, int, string) (func(), bool, error) {
	f.mu.Lock()
	onLock, busy := f.onLock, f.busy
	f.onLock = nil
	f.mu.Unlock()
	if busy {
		return nil, false, nil
	}
	if onLock != nil {
		onLock()
	}
	return func() {}, true, nil
}

func (f *fakeLocalSidecarStore) SeriesChildren(_ context.Context, seriesID string) (map[int]string, map[episodeResultKey]string, error) {
	seasons := make(map[int]string)
	for _, season := range f.x.Seasons(seriesID) {
		seasons[season.SeasonNumber] = season.ContentID
	}
	episodes := make(map[episodeResultKey]string)
	for _, episode := range f.x.Episodes(seriesID) {
		episodes[episodeResultKey{episode.SeasonNumber, episode.EpisodeNumber}] = episode.ContentID
	}
	return seasons, episodes, nil
}

// UseLocalSidecars enables local sidecar detection with an in-memory store,
// and makes chain the provider chain of every content level of a library.
// Calling it again for another library keeps the stored records.
func (x *NFOSeriesHarness) UseLocalSidecars(folderID int, chain ...Provider) {
	s := x.h.service
	if s.localSidecars == nil {
		s.localSidecars = &fakeLocalSidecarStore{x: x, records: make(map[string]localSidecarRecord)}
	}
	s.chainCacheMu.Lock()
	defer s.chainCacheMu.Unlock()
	if s.chainCache == nil {
		s.chainCache = make(map[string]chainCacheEntry)
	}
	for _, level := range []string{"movie", "series", "season", "episode"} {
		s.chainCache[chainCacheKeyForTest(folderID, level)] = chainCacheEntry{
			providers: chain,
			expiresAt: time.Now().Add(time.Hour),
		}
	}
}

// SetLevelChain makes chain the provider chain of one content level of a
// library.
func (x *NFOSeriesHarness) SetLevelChain(folderID int, level string, chain ...Provider) {
	s := x.h.service
	s.chainCacheMu.Lock()
	defer s.chainCacheMu.Unlock()
	s.chainCache[chainCacheKeyForTest(folderID, level)] = chainCacheEntry{
		providers: chain,
		expiresAt: time.Now().Add(time.Hour),
	}
}

func chainCacheKeyForTest(folderID int, level string) string {
	return fmt.Sprintf("%d:%s", folderID, level)
}

// LocalSidecarFingerprint returns a target's stored fingerprint in a library,
// or "" when it has no record.
func (x *NFOSeriesHarness) LocalSidecarFingerprint(folderID int, targetType, contentID string) string {
	store := x.h.service.localSidecars.(*fakeLocalSidecarStore)
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.records[localSidecarRecordKey(folderID, targetType, contentID)].Fingerprint
}

// ForgetLocalSidecar drops a target's stored record, as if the target had
// appeared after its item was recorded.
func (x *NFOSeriesHarness) ForgetLocalSidecar(folderID int, targetType, contentID string) {
	store := x.h.service.localSidecars.(*fakeLocalSidecarStore)
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.records, localSidecarRecordKey(folderID, targetType, contentID))
}

// BeforeNextLocalSidecarLock runs fn once when a check next takes an item's
// lock, as if another node had held the lock and finished first.
func (x *NFOSeriesHarness) BeforeNextLocalSidecarLock(fn func()) {
	store := x.h.service.localSidecars.(*fakeLocalSidecarStore)
	store.mu.Lock()
	defer store.mu.Unlock()
	store.onLock = fn
}

// BeforeNextLocalSidecarUpsert runs fn once just before the next write of
// records is applied, as if another check had stored first.
func (x *NFOSeriesHarness) BeforeNextLocalSidecarUpsert(fn func()) {
	store := x.h.service.localSidecars.(*fakeLocalSidecarStore)
	store.mu.Lock()
	defer store.mu.Unlock()
	store.onUpsert = fn
}

// HoldLocalSidecarLocks makes every item lock read as held by another check
// while held is true.
func (x *NFOSeriesHarness) HoldLocalSidecarLocks(held bool) {
	store := x.h.service.localSidecars.(*fakeLocalSidecarStore)
	store.mu.Lock()
	defer store.mu.Unlock()
	store.busy = held
}

// LocalSidecarUpserts counts the store's upsert calls.
func (x *NFOSeriesHarness) LocalSidecarUpserts() int {
	store := x.h.service.localSidecars.(*fakeLocalSidecarStore)
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.upserts
}

// SetFolderLanguage gives a library a metadata language.
func (x *NFOSeriesHarness) SetFolderLanguage(folderID int, language string) {
	x.h.service.folderRepo = &fakeMetadataFolderRepo{folders: map[int]*models.MediaFolder{
		folderID: {ID: folderID, Type: "movies", Enabled: true, MetadataLanguage: language},
	}}
}

// OnNthFileListing runs fn when an item's files are listed for the n-th time
// from now, before that listing returns.
func (x *NFOSeriesHarness) OnNthFileListing(n int, fn func()) {
	x.h.fileRepo.mu.Lock()
	defer x.h.fileRepo.mu.Unlock()
	x.h.fileRepo.listHook, x.h.fileRepo.listHookIn = fn, n
}

// FailNthFileListing makes the n-th next listing of an item's files fail with
// err, and only that one.
func (x *NFOSeriesHarness) FailNthFileListing(n int, err error) {
	x.h.fileRepo.mu.Lock()
	defer x.h.fileRepo.mu.Unlock()
	x.h.fileRepo.listErr, x.h.fileRepo.listErrIn = err, n
}

// FailFileListings makes listing an item's files fail with err.
func (x *NFOSeriesHarness) FailFileListings(err error) {
	x.h.fileRepo.mu.Lock()
	defer x.h.fileRepo.mu.Unlock()
	x.h.fileRepo.listErr = err
}

// SeedMatchedItem seeds a matched item.
func (x *NFOSeriesHarness) SeedMatchedItem(item models.MediaItem) error {
	item.Status = "matched"
	for _, list := range []*[]string{&item.Studios, &item.Networks, &item.Countries, &item.Genres} {
		if *list == nil {
			*list = []string{}
		}
	}
	return x.h.itemRepo.Upsert(context.Background(), &item)
}

// RecordProcess replaces the metadata pipeline with a recorder, so a test can
// see which full refreshes ran without running them. With moveTo set, each
// refresh also moves the item to that content ID, as a re-anchor does.
func (x *NFOSeriesHarness) RecordProcess(moveTo ...string) func() []ProcessRequest {
	var mu sync.Mutex
	var requests []ProcessRequest
	x.h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		if len(moveTo) > 0 {
			repo := x.h.itemRepo
			repo.mu.Lock()
			if item := repo.items[req.ContentID]; item != nil {
				delete(repo.items, req.ContentID)
				item.ContentID = moveTo[0]
				repo.items[moveTo[0]] = item
			}
			repo.mu.Unlock()
			files := x.h.fileRepo
			files.mu.Lock()
			for fileID, contentID := range files.contentIDs {
				if contentID == req.ContentID {
					files.contentIDs[fileID] = moveTo[0]
				}
			}
			files.mu.Unlock()
		}
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	return func() []ProcessRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]ProcessRequest(nil), requests...)
	}
}

// identityChoiceItemRepo adds an admin's Identify choice to the fake item repo.
type identityChoiceItemRepo struct {
	*fakeItemRepo
	chosen map[string]bool
	err    error
}

func (r *identityChoiceItemRepo) IdentityChosen(_ context.Context, contentID string) (bool, error) {
	return r.chosen[contentID], r.err
}

func (r *identityChoiceItemRepo) MarkIdentityChosen(_ context.Context, contentID string) error {
	r.chosen[contentID] = true
	return nil
}

var _ metadataIdentityChoiceRepo = (*identityChoiceItemRepo)(nil)

// FailIdentityChoiceReads makes reading an item's Identify choice fail with
// err; nil restores it.
func (x *NFOSeriesHarness) FailIdentityChoiceReads(err error) {
	repo, ok := x.h.service.itemRepo.(*identityChoiceItemRepo)
	if !ok {
		repo = &identityChoiceItemRepo{fakeItemRepo: x.h.itemRepo, chosen: make(map[string]bool)}
		x.h.service.itemRepo = repo
	}
	repo.err = err
}

// failingObservedLocations fails every observed-location lookup.
type failingObservedLocations struct{ err error }

func (r failingObservedLocations) Get(context.Context, int, string) (*models.ObservedMediaLocation, error) {
	return nil, r.err
}

// FailObservedLocationLookups makes looking up a file's observed root fail
// with err; nil removes the observed-location repository again.
func (x *NFOSeriesHarness) FailObservedLocationLookups(err error) {
	if err == nil {
		x.h.service.observedLocationRepo = nil
		return
	}
	x.h.service.observedLocationRepo = failingObservedLocations{err: err}
}

// FailFolderLookups makes loading a library fail with err; nil restores it.
func (x *NFOSeriesHarness) FailFolderLookups(folderID int, err error) {
	repo, ok := x.h.service.folderRepo.(*fakeMetadataFolderRepo)
	if !ok {
		repo = &fakeMetadataFolderRepo{folders: map[int]*models.MediaFolder{
			folderID: {ID: folderID, Type: "movies", Enabled: true},
		}}
		x.h.service.folderRepo = repo
	}
	if err == nil {
		delete(repo.lookupErrs, folderID)
		return
	}
	if repo.lookupErrs == nil {
		repo.lookupErrs = make(map[int]error)
	}
	repo.lookupErrs[folderID] = err
}

// RecordDeadProviderID records a provider ID of an item as dead (a 404), as
// a refresh does.
func (x *NFOSeriesHarness) RecordDeadProviderID(contentID, provider, providerID string) {
	repo, ok := x.h.service.staleIDRepo.(*fakeStaleIDRepo)
	if !ok {
		repo = newFakeStaleIDRepo()
		x.h.service.staleIDRepo = repo
	}
	_ = repo.Upsert(context.Background(), contentID, provider, providerID)
}

// MarkIdentityChosen records that an admin chose an item's identity with
// Identify.
func (x *NFOSeriesHarness) MarkIdentityChosen(contentID string) {
	repo, ok := x.h.service.itemRepo.(*identityChoiceItemRepo)
	if !ok {
		repo = &identityChoiceItemRepo{fakeItemRepo: x.h.itemRepo, chosen: make(map[string]bool)}
		x.h.service.itemRepo = repo
	}
	repo.chosen[contentID] = true
}
