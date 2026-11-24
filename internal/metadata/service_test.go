package metadata

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	testPosterPath     = "/poster.jpg"
	testBackdropPath   = "/backdrop.jpg"
	testTMDBProvider   = "tmdb"
	testTVDBProvider   = "tvdb"
	testIMDBProvider   = "imdb"
	testMetaDBProvider = "metadb"
)

// ---------------------------------------------------------------------------
// Fake repositories
// ---------------------------------------------------------------------------

// fakeItemRepo implements metadataItemRepo with an in-memory store.
type fakeItemRepo struct {
	mu    sync.Mutex
	items map[string]*models.MediaItem

	// replacedPeople records each ReplacePeople call by content ID.
	replacedPeople   map[string][]models.ItemPerson
	replacePeopleErr error

	// Trailer refresh cooldown state (metadataTrailerRefreshRepo).
	trailersRequestedAt    map[string]time.Time
	trailersRequestedAtErr error
	trailersClaims         int
	trailersClaimErr       error
	trailersClaimResult    *trailersClaimResult
	trailersReleases       int
	trailersReleased       chan struct{}
	trailersReleaseGate    chan struct{}
	now                    func() time.Time

	// referenced reports whether something links to an item, for
	// DeleteIfUnreferenced. Nil treats every item as unreferenced.
	referenced func(contentID string) bool

	// unmatchedIDs is what ListUnmatchedByFolderAndPathPrefix returns.
	unmatchedIDs []string

	// metadataUpdates counts UpdateMetadata calls.
	metadataUpdates int
}

// trailersClaimResult forces a fixed answer out of the cooldown gate, for the
// outcomes the in-memory model cannot reach on its own — notably the real
// repository's "lost the gate but the slot kept being freed" answer, which
// carries no timestamp.
type trailersClaimResult struct {
	claimed     bool
	requestedAt *time.Time
}

func newFakeItemRepo() *fakeItemRepo {
	return &fakeItemRepo{items: make(map[string]*models.MediaItem)}
}

func (r *fakeItemRepo) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if item, ok := r.items[contentID]; ok {
		cp := *item
		return &cp, nil
	}
	return nil, fmt.Errorf("%w: %s", catalog.ErrItemNotFound, contentID)
}

func (r *fakeItemRepo) GetByExternalID(_ context.Context, tmdbID, imdbID, tvdbID, itemType string) (*models.MediaItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.items {
		if item.Type != itemType {
			continue
		}
		if tmdbID != "" && item.TmdbID == tmdbID {
			cp := *item
			return &cp, nil
		}
		if imdbID != "" && item.ImdbID == imdbID {
			cp := *item
			return &cp, nil
		}
		if tvdbID != "" && item.TvdbID == tvdbID {
			cp := *item
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (r *fakeItemRepo) GetByTitleYearType(_ context.Context, title string, year int, itemType string) (*models.MediaItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.items {
		if item.Title == title && item.Year == year && item.Type == itemType {
			cp := *item
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (r *fakeItemRepo) Upsert(_ context.Context, item *models.MediaItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *item
	r.items[item.ContentID] = &cp
	return nil
}

// UpdateProvisionalArtwork applies the artwork columns of upd under the
// repository's condition: the item is still provisional, its images lock is as
// the caller read it, and each slot upd writes still has the source read.
func (r *fakeItemRepo) UpdateProvisionalArtwork(_ context.Context, read *models.MediaItem, upd *catalog.MetadataUpdate) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	contentID := read.ContentID
	item, ok := r.items[contentID]
	if !ok || !isProvisionalOwnershipStatus(item.Status) || artworkLocked(item) != artworkLocked(read) ||
		(upd.PosterSourcePath != nil && item.PosterSourcePath != read.PosterSourcePath) ||
		(upd.BackdropSourcePath != nil && item.BackdropSourcePath != read.BackdropSourcePath) ||
		(upd.LogoSourcePath != nil && item.LogoSourcePath != read.LogoSourcePath) {
		return false, nil
	}
	r.metadataUpdates++
	cp := *item
	for _, col := range []struct {
		dst *string
		val *string
	}{
		{&cp.PosterPath, upd.PosterPath}, {&cp.PosterSourcePath, upd.PosterSourcePath}, {&cp.PosterThumbhash, upd.PosterThumbhash},
		{&cp.BackdropPath, upd.BackdropPath}, {&cp.BackdropSourcePath, upd.BackdropSourcePath}, {&cp.BackdropThumbhash, upd.BackdropThumbhash},
		{&cp.LogoPath, upd.LogoPath}, {&cp.LogoSourcePath, upd.LogoSourcePath},
	} {
		if col.val != nil {
			*col.dst = *col.val
		}
	}
	r.items[contentID] = &cp
	return true, nil
}

func (r *fakeItemRepo) SetStatusUnlessMatched(_ context.Context, contentID, status string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[contentID]
	if !ok {
		return false, fmt.Errorf("%w: %s", catalog.ErrItemNotFound, contentID)
	}
	if strings.EqualFold(strings.TrimSpace(item.Status), "matched") {
		return false, nil
	}
	cp := *item
	cp.Status = status
	r.items[contentID] = &cp
	return true, nil
}

func (r *fakeItemRepo) InsertIfAbsent(_ context.Context, item *models.MediaItem) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[item.ContentID]; ok {
		return false, nil
	}
	cp := *item
	r.items[item.ContentID] = &cp
	return true, nil
}

func (r *fakeItemRepo) DeleteIfUnreferenced(_ context.Context, contentID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[contentID]; !ok || (r.referenced != nil && r.referenced(contentID)) {
		return false, nil
	}
	delete(r.items, contentID)
	return true, nil
}

func (r *fakeItemRepo) Delete(_ context.Context, contentID string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[contentID]; !ok {
		return nil, catalog.ErrItemNotFound
	}
	delete(r.items, contentID)
	return nil, nil
}

func (r *fakeItemRepo) IncrementRefreshFailure(_ context.Context, contentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[contentID]
	if !ok {
		return fmt.Errorf("item not found: %s", contentID)
	}
	cp := *item
	cp.RefreshFailures++
	r.items[contentID] = &cp
	return nil
}

func (r *fakeItemRepo) ReplacePeople(_ context.Context, contentID string, people []models.ItemPerson) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replacePeopleErr != nil {
		return r.replacePeopleErr
	}
	if r.replacedPeople == nil {
		r.replacedPeople = make(map[string][]models.ItemPerson)
	}
	r.replacedPeople[contentID] = append([]models.ItemPerson(nil), people...)
	return nil
}

func (r *fakeItemRepo) ListUnmatchedByFolderAndPathPrefix(_ context.Context, _ int, _ string, _ int) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.unmatchedIDs...), nil
}

// TryClaimTrailersRefresh mirrors the SQL gate in *catalog.ItemRepository: the
// claim succeeds only when no timestamp is stored or the stored one predates
// the cooldown window, and either way the caller reads back the timestamp now
// stored in the column.
func (r *fakeItemRepo) TryClaimTrailersRefresh(_ context.Context, contentID string, cooldown time.Duration) (bool, *time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[contentID]; !ok {
		return false, nil, catalog.ErrItemNotFound
	}
	now := time.Now().UTC()
	if r.now != nil {
		now = r.now()
	}
	if r.trailersClaimErr != nil {
		return false, nil, r.trailersClaimErr
	}
	if forced := r.trailersClaimResult; forced != nil {
		return forced.claimed, forced.requestedAt, nil
	}
	stored, ok := r.trailersRequestedAt[contentID]
	if !ok || stored.Before(now.Add(-cooldown)) {
		if r.trailersRequestedAt == nil {
			r.trailersRequestedAt = make(map[string]time.Time)
		}
		r.trailersRequestedAt[contentID] = now
		r.trailersClaims++
		claimed := now
		return true, &claimed, nil
	}
	blocked := stored
	return false, &blocked, nil
}

// ReleaseTrailersRefreshClaim mirrors the equality-guarded UPDATE: a slot that
// has since been re-claimed by a newer request is left alone.
//
// trailersReleaseGate, when set, holds the release until the test closes it,
// which lets a test interleave a newer claim with a late-arriving release.
func (r *fakeItemRepo) ReleaseTrailersRefreshClaim(_ context.Context, contentID string, claimedAt time.Time) error {
	r.mu.Lock()
	gate := r.trailersReleaseGate
	r.mu.Unlock()
	if gate != nil {
		<-gate
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.trailersReleases++
	if stored, ok := r.trailersRequestedAt[contentID]; ok && stored.Equal(claimedAt) {
		delete(r.trailersRequestedAt, contentID)
	}
	if r.trailersReleased != nil {
		close(r.trailersReleased)
		r.trailersReleased = nil
	}
	return nil
}

// TrailersRefreshRequestedAt reads back the stored claim the way the durable
// recovery path does, so a refresh that inherits a claim can release it on the
// same key the original request wrote.
func (r *fakeItemRepo) TrailersRefreshRequestedAt(_ context.Context, contentID string) (*time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[contentID]; !ok {
		return nil, catalog.ErrItemNotFound
	}
	if r.trailersRequestedAtErr != nil {
		return nil, r.trailersRequestedAtErr
	}
	if stored, ok := r.trailersRequestedAt[contentID]; ok {
		return &stored, nil
	}
	return nil, nil
}

// expectTrailersRelease arms a channel closed by the next
// ReleaseTrailersRefreshClaim, so a test can wait for the detached refresh's
// failure path instead of polling.
func (r *fakeItemRepo) expectTrailersRelease() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	released := make(chan struct{})
	r.trailersReleased = released
	return released
}

func (r *fakeItemRepo) trailersClaimCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trailersClaims
}

func (r *fakeItemRepo) trailersReleaseCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trailersReleases
}

// trailersStoredAt reports the timestamp currently stored for the item, or nil
// when the slot is free.
func (r *fakeItemRepo) trailersStoredAt(contentID string) *time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stored, ok := r.trailersRequestedAt[contentID]; ok {
		return &stored
	}
	return nil
}

type fakeRefreshDebtRepo struct {
	mu    sync.Mutex
	debts map[string]*models.MetadataRefreshDebt
}

func newFakeRefreshDebtRepo() *fakeRefreshDebtRepo {
	return &fakeRefreshDebtRepo{debts: make(map[string]*models.MetadataRefreshDebt)}
}

func fakeRefreshDebtKey(targetType, contentID string) string {
	targetType = NormalizeRefreshTargetType(targetType)
	if targetType == "" {
		return ""
	}
	if targetType == RefreshTargetItem {
		return contentID
	}
	return targetType + ":" + contentID
}

func (r *fakeRefreshDebtRepo) Get(ctx context.Context, contentID string) (*models.MetadataRefreshDebt, error) {
	return r.GetTarget(ctx, RefreshTargetItem, contentID)
}

func (r *fakeRefreshDebtRepo) GetTarget(_ context.Context, targetType, contentID string) (*models.MetadataRefreshDebt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	debt, ok := r.debts[fakeRefreshDebtKey(targetType, contentID)]
	if !ok {
		return nil, ErrRefreshDebtNotFound
	}
	cp := *debt
	return &cp, nil
}

func (r *fakeRefreshDebtRepo) UpsertDebt(ctx context.Context, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error {
	return r.UpsertTargetDebt(ctx, RefreshTargetItem, contentID, priority, reasonMask, nextRefreshAt)
}

func (r *fakeRefreshDebtRepo) UpsertTargetDebt(_ context.Context, targetType, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	targetType = NormalizeRefreshTargetType(targetType)
	key := fakeRefreshDebtKey(targetType, contentID)
	if key == "" || contentID == "" || reasonMask == 0 {
		return nil
	}
	r.debts[key] = &models.MetadataRefreshDebt{
		TargetType:    targetType,
		ContentID:     contentID,
		Priority:      priority,
		ReasonMask:    reasonMask,
		NextRefreshAt: nextRefreshAt,
	}
	return nil
}

// RequestDue mirrors the repository's merge semantics rather than overwriting:
// the real statement ORs the reason mask, keeps the greater priority and the
// earlier next_refresh_at. Callers reason about all three (a trailer request
// adds its reason to whatever debt an item already has, and must not push
// genuinely-due work out), so a fake that replaced the row would hide that.
// Like the statement, it leaves next_refresh_at alone while the row is leased
// or was attempted or refreshed within the cooldown.
func (r *fakeRefreshDebtRepo) RequestDue(
	_ context.Context,
	targetType string,
	contentID string,
	priority int,
	reasonMask int64,
	nextRefreshAt time.Time,
	cooldown time.Duration,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	targetType = NormalizeRefreshTargetType(targetType)
	key := fakeRefreshDebtKey(targetType, contentID)
	if key == "" || contentID == "" || reasonMask == 0 {
		return nil
	}
	if existing, ok := r.debts[key]; ok {
		now := time.Now()
		cooldownStart := now.Add(-max(cooldown, 0))
		existing.ReasonMask |= reasonMask
		existing.Priority = max(existing.Priority, priority)
		switch {
		case existing.LeaseExpiresAt != nil && !existing.LeaseExpiresAt.Before(now):
		case existing.LastAttemptAt != nil && existing.LastAttemptAt.After(cooldownStart):
		case existing.LastSuccessAt != nil && existing.LastSuccessAt.After(cooldownStart):
		case nextRefreshAt.Before(existing.NextRefreshAt):
			existing.NextRefreshAt = nextRefreshAt
		}
		return nil
	}
	r.debts[key] = &models.MetadataRefreshDebt{
		TargetType:    targetType,
		ContentID:     contentID,
		Priority:      priority,
		ReasonMask:    reasonMask,
		NextRefreshAt: nextRefreshAt,
	}
	return nil
}

func (r *fakeRefreshDebtRepo) MarkFailure(
	ctx context.Context,
	contentID string,
	priority int,
	reasonMask int64,
	nextRefreshAt time.Time,
	attemptCount int,
	lastError string,
) error {
	return r.MarkTargetFailure(ctx, RefreshTargetItem, contentID, priority, reasonMask, nextRefreshAt, attemptCount, lastError)
}

func (r *fakeRefreshDebtRepo) MarkTargetFailure(
	_ context.Context,
	targetType string,
	contentID string,
	priority int,
	reasonMask int64,
	nextRefreshAt time.Time,
	attemptCount int,
	lastError string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	targetType = NormalizeRefreshTargetType(targetType)
	key := fakeRefreshDebtKey(targetType, contentID)
	if key == "" || contentID == "" || reasonMask == 0 {
		return nil
	}
	attemptedAt := time.Now()
	if existing, ok := r.debts[key]; ok {
		attemptCount = max(attemptCount, existing.AttemptCount)
	}
	r.debts[key] = &models.MetadataRefreshDebt{
		TargetType:    targetType,
		ContentID:     contentID,
		Priority:      priority,
		ReasonMask:    reasonMask,
		NextRefreshAt: nextRefreshAt,
		LastAttemptAt: &attemptedAt,
		AttemptCount:  attemptCount,
		LastError:     lastError,
	}
	return nil
}

func (r *fakeRefreshDebtRepo) MarkSuccess(ctx context.Context, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error {
	return r.MarkTargetSuccess(ctx, RefreshTargetItem, contentID, priority, reasonMask, nextRefreshAt)
}

func (r *fakeRefreshDebtRepo) MarkTargetSuccess(_ context.Context, targetType, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	targetType = NormalizeRefreshTargetType(targetType)
	key := fakeRefreshDebtKey(targetType, contentID)
	if key == "" || contentID == "" {
		return nil
	}
	if reasonMask == 0 {
		delete(r.debts, key)
		return nil
	}
	succeededAt := time.Now()
	debt := &models.MetadataRefreshDebt{TargetType: targetType, ContentID: contentID, LastSuccessAt: &succeededAt}
	if existing, ok := r.debts[key]; ok {
		// Mirror the repository's upsert: a success leaves attempt_count and
		// last_attempt_at alone, so terminal give-up survives later syncs.
		debt.AttemptCount = existing.AttemptCount
		debt.LastAttemptAt = existing.LastAttemptAt
	}
	debt.Priority = priority
	debt.ReasonMask = reasonMask
	debt.NextRefreshAt = nextRefreshAt
	r.debts[key] = debt
	return nil
}

func (r *fakeRefreshDebtRepo) DeleteDebt(ctx context.Context, contentID string) error {
	return r.DeleteTargetDebt(ctx, RefreshTargetItem, contentID)
}

func (r *fakeRefreshDebtRepo) SnapshotEpisodeDebts(_ context.Context, _ string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := make(map[string]string)
	for _, debt := range r.debts {
		if debt.TargetType == RefreshTargetEpisode {
			versions[debt.ContentID] = fmt.Sprintf("%#v", debt)
		}
	}
	return versions, nil
}

func (r *fakeRefreshDebtRepo) DeleteEpisodeDebts(_ context.Context, contentIDs []string, versions map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range contentIDs {
		key := fakeRefreshDebtKey(RefreshTargetEpisode, id)
		if debt, ok := r.debts[key]; ok && versions[id] == fmt.Sprintf("%#v", debt) {
			delete(r.debts, key)
		}
	}
	return nil
}

func (r *fakeRefreshDebtRepo) DeleteTargetDebt(_ context.Context, targetType, contentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.debts, fakeRefreshDebtKey(targetType, contentID))
	return nil
}

func (r *fakeRefreshDebtRepo) RestartTerminalEpisodeDebt(_ context.Context, targetType, contentID string, attemptedBefore, nextRefreshAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	debt, ok := r.debts[fakeRefreshDebtKey(targetType, contentID)]
	if !ok {
		return nil
	}
	debt.ReasonMask |= RefreshDebtReasonEpisodeIncomplete
	now := time.Now()
	if debt.AttemptCount < refreshDebtEpisodeTerminalAttempts || debt.LastAttemptAt == nil || !debt.LastAttemptAt.Before(attemptedBefore) ||
		!debt.NextRefreshAt.After(now) || (debt.LeaseExpiresAt != nil && !debt.LeaseExpiresAt.Before(now)) {
		return nil
	}
	debt.Priority = max(debt.Priority, refreshDebtPriority(RefreshDebtReasonEpisodeIncomplete))
	debt.AttemptCount = 0
	debt.NextRefreshAt = nextRefreshAt
	return nil
}

// fakeFileRepo implements FileContentUpdater with an in-memory store.
type fakeFileRepo struct {
	mu                  sync.Mutex
	contentIDs          map[int]string    // fileID -> contentID
	rootContent         map[string]string // "folderID:rootPath" -> contentID
	groupContent        map[string]string // "folderID:version:key" -> contentID
	groupFiles          map[string][]*models.MediaFile
	matchStamps         map[int]time.Time // fileID -> match_attempted_at
	updateErrors        map[int]error
	afterUpdate         func(fileID int, contentID string)
	listErr             error
	listErrIn           int // when > 0, the listErrIn-th next GetByContentID fails with listErr
	listHook            func()
	listHookIn          int // when > 0, the listHookIn-th next GetByContentID runs listHook
	claimUnmatchedCalls int
	claimNonSeriesCalls int
	claimMixedCalls     int
}

func newFakeFileRepo() *fakeFileRepo {
	return &fakeFileRepo{
		contentIDs:   make(map[int]string),
		rootContent:  make(map[string]string),
		groupContent: make(map[string]string),
		groupFiles:   make(map[string][]*models.MediaFile),
		matchStamps:  make(map[int]time.Time),
		updateErrors: make(map[int]error),
	}
}

func (r *fakeFileRepo) UnlinkContentID(_ context.Context, fileID int, contentID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.contentIDs[fileID] != contentID {
		return false, nil
	}
	r.contentIDs[fileID] = ""
	return true, nil
}

func (r *fakeFileRepo) UpdateContentID(_ context.Context, fileID int, _ string, contentID string) error {
	r.mu.Lock()
	if err := r.updateErrors[fileID]; err != nil {
		r.mu.Unlock()
		return err
	}
	r.contentIDs[fileID] = contentID
	afterUpdate := r.afterUpdate
	r.mu.Unlock()
	if afterUpdate != nil {
		afterUpdate(fileID, contentID)
	}
	return nil
}

func (r *fakeFileRepo) ReplaceContentID(_ context.Context, oldContentID, newContentID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replaced := 0
	for fileID, contentID := range r.contentIDs {
		if contentID != oldContentID {
			continue
		}
		r.contentIDs[fileID] = newContentID
		replaced++
	}
	return replaced, nil
}

func (r *fakeFileRepo) FindContentIDByRootPath(_ context.Context, folderID int, rootPath, _ string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", folderID, rootPath)
	if cid, ok := r.rootContent[key]; ok {
		return cid, nil
	}
	return "", nil
}

func (r *fakeFileRepo) GetByContentID(_ context.Context, contentID string) ([]*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listHookIn > 0 {
		r.listHookIn--
		if r.listHookIn == 0 {
			r.listHook()
		}
	}
	if r.listErrIn > 0 {
		r.listErrIn--
		if r.listErrIn == 0 {
			err := r.listErr
			r.listErr = nil
			return nil, err
		}
	} else if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]*models.MediaFile, 0)
	for _, files := range r.groupFiles {
		for _, file := range files {
			if file == nil || r.contentIDs[file.ID] != contentID {
				continue
			}
			cp := *file
			cp.ContentID = contentID
			out = append(out, &cp)
		}
	}
	slices.SortFunc(out, func(a, b *models.MediaFile) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return out, nil
}

func (r *fakeFileRepo) FindContentIDByObservedRootPath(_ context.Context, folderID int, observedRootPath, _ string) (string, error) {
	return r.FindContentIDByRootPath(context.Background(), folderID, observedRootPath, "")
}

func (r *fakeFileRepo) FindContentIDByGroupKey(_ context.Context, folderID int, groupKeyVersion int, contentGroupKey, _ string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%d:%s", folderID, groupKeyVersion, contentGroupKey)
	if cid, ok := r.groupContent[key]; ok {
		return cid, nil
	}
	return "", nil
}

func (r *fakeFileRepo) ListByGroupKey(_ context.Context, folderID int, groupKeyVersion int, contentGroupKey string) ([]*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%d:%s", folderID, groupKeyVersion, contentGroupKey)
	files := r.groupFiles[key]
	out := make([]*models.MediaFile, 0, len(files))
	for _, file := range files {
		if file == nil {
			continue
		}
		cp := *file
		if contentID, ok := r.contentIDs[file.ID]; ok {
			cp.ContentID = contentID
		}
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeFileRepo) ListByObservedRootPath(_ context.Context, folderID int, observedRootPath string) ([]*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*models.MediaFile, 0)
	for _, files := range r.groupFiles {
		for _, file := range files {
			if file == nil || file.MediaFolderID != folderID || file.ObservedRootPath != observedRootPath {
				continue
			}
			cp := *file
			if cid, ok := r.contentIDs[file.ID]; ok {
				cp.ContentID = cid
			}
			out = append(out, &cp)
		}
	}
	slices.SortFunc(out, func(a, b *models.MediaFile) int {
		return strings.Compare(a.FilePath, b.FilePath)
	})
	return out, nil
}

func (r *fakeFileRepo) UpdateContentIDByObservedRootPath(_ context.Context, folderID int, observedRootPath, contentID string) (int, []string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	updated := 0
	var replaced []string
	for _, files := range r.groupFiles {
		for _, file := range files {
			if file == nil || file.MediaFolderID != folderID || file.ObservedRootPath != observedRootPath {
				continue
			}
			existing, ok := r.contentIDs[file.ID]
			if !ok {
				existing = file.ContentID
			}
			if existing == contentID {
				continue
			}
			if existing != "" && !slices.Contains(replaced, existing) {
				replaced = append(replaced, existing)
			}
			r.contentIDs[file.ID] = contentID
			updated++
		}
	}
	r.rootContent[fmt.Sprintf("%d:%s", folderID, observedRootPath)] = contentID
	return updated, replaced, nil
}

// setRootContent pre-seeds a root path -> content_id mapping for testing.
func (r *fakeFileRepo) setRootContent(folderID int, rootPath, contentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", folderID, rootPath)
	r.rootContent[key] = contentID
}

func (r *fakeFileRepo) setGroupFiles(folderID int, groupKeyVersion int, contentGroupKey string, files ...*models.MediaFile) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%d:%s", folderID, groupKeyVersion, contentGroupKey)
	r.groupFiles[key] = append([]*models.MediaFile(nil), files...)
}

func (r *fakeFileRepo) MarkMatchAttempted(_ context.Context, fileID int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.matchStamps[fileID] = time.Now()
	return nil
}

func (r *fakeFileRepo) ClaimUnmatched(_ context.Context, limit int) ([]*models.MediaFile, error) {
	r.mu.Lock()
	r.claimUnmatchedCalls++
	r.mu.Unlock()
	return r.claimUnmatchedLocked(limit), nil
}

func (r *fakeFileRepo) ClaimUnmatchedByFolderAndPathPrefix(_ context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimUnmatchedCalls++
	return r.claimScopedLocked(folderID, pathPrefix, limit, attemptBefore), nil
}

func (r *fakeFileRepo) ClaimUnmatchedNonSeries(_ context.Context, limit int) ([]*models.MediaFile, error) {
	r.mu.Lock()
	r.claimNonSeriesCalls++
	r.mu.Unlock()
	return r.claimUnmatchedLocked(limit), nil
}

func (r *fakeFileRepo) ClaimUnmatchedNonSeriesByFolderAndPathPrefix(_ context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimNonSeriesCalls++
	return r.claimScopedLocked(folderID, pathPrefix, limit, attemptBefore), nil
}

func (r *fakeFileRepo) ClaimUnmatchedMixed(_ context.Context, limit int) ([]*models.MediaFile, error) {
	r.mu.Lock()
	r.claimMixedCalls++
	r.mu.Unlock()
	return r.claimUnmatchedLocked(limit), nil
}

func (r *fakeFileRepo) ClaimUnmatchedMixedByFolderAndPathPrefix(_ context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimMixedCalls++
	return r.claimScopedLocked(folderID, pathPrefix, limit, attemptBefore), nil
}

// claimScopedLocked claims unlinked files of one folder under pathPrefix that
// were not attempted since attemptBefore. The caller holds r.mu.
func (r *fakeFileRepo) claimScopedLocked(folderID int, pathPrefix string, limit int, attemptBefore time.Time) []*models.MediaFile {
	files := make([]*models.MediaFile, 0)
	for _, groupFiles := range r.groupFiles {
		for _, file := range groupFiles {
			if file == nil || file.MediaFolderID != folderID {
				continue
			}
			if cid := r.contentIDs[file.ID]; cid != "" {
				continue
			}
			if pathPrefix != "" && file.FilePath != pathPrefix && !strings.HasPrefix(file.FilePath, pathPrefix+"/") {
				continue
			}
			if !attemptBefore.IsZero() {
				if stamp, ok := r.matchStamps[file.ID]; ok && !stamp.Before(attemptBefore) {
					continue
				}
			}
			cp := *file
			files = append(files, &cp)
			r.matchStamps[file.ID] = time.Now()
			if limit > 0 && len(files) >= limit {
				return files
			}
		}
	}
	return files
}

func (r *fakeFileRepo) claimUnmatchedLocked(limit int) []*models.MediaFile {
	r.mu.Lock()
	defer r.mu.Unlock()
	files := make([]*models.MediaFile, 0)
	for _, groupFiles := range r.groupFiles {
		for _, file := range groupFiles {
			if file == nil {
				continue
			}
			if cid := r.contentIDs[file.ID]; cid != "" {
				continue
			}
			cp := *file
			files = append(files, &cp)
			r.matchStamps[file.ID] = time.Now()
			if limit > 0 && len(files) >= limit {
				return files
			}
		}
	}
	return files
}

func (r *fakeFileRepo) GetUnmatched(_ context.Context, _ int) ([]*models.MediaFile, error) {
	return nil, nil
}

func (r *fakeFileRepo) GetUnmatchedByFolderAndPathPrefix(_ context.Context, _ int, _ string, _ int) ([]*models.MediaFile, error) {
	return nil, nil
}

// fakeLibraryRepo implements metadataLibraryRepo.
type fakeLibraryRepo struct {
	mu           sync.Mutex
	memberships  map[string]time.Time // "contentID:folderID" -> first_seen_at
	languages    map[string][]string  // contentID -> distinct metadata languages override
	languagesErr map[string]error     // contentID -> forced lookup error
}

func newFakeLibraryRepo() *fakeLibraryRepo {
	return &fakeLibraryRepo{
		memberships:  make(map[string]time.Time),
		languages:    make(map[string][]string),
		languagesErr: make(map[string]error),
	}
}

// setMetadataLanguages overrides GetDistinctMetadataLanguagesForItem for one
// item, optionally forcing a lookup error.
func (r *fakeLibraryRepo) setMetadataLanguages(contentID string, languages []string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.languages[contentID] = languages
	if err != nil {
		r.languagesErr[contentID] = err
	} else {
		delete(r.languagesErr, contentID)
	}
}

func (r *fakeLibraryRepo) Upsert(_ context.Context, contentID string, folderID int, firstSeenAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%s:%d", contentID, folderID)
	if _, exists := r.memberships[key]; !exists {
		r.memberships[key] = firstSeenAt
	}
	return nil
}

func (r *fakeLibraryRepo) hasMembership(contentID string, folderID int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%s:%d", contentID, folderID)
	_, ok := r.memberships[key]
	return ok
}

func (r *fakeLibraryRepo) GetFolderIDsForItem(_ context.Context, contentID string) ([]int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []int
	prefix := contentID + ":"
	for key := range r.memberships {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		var folderID int
		if _, err := fmt.Sscanf(key, contentID+":%d", &folderID); err == nil {
			ids = append(ids, folderID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (r *fakeLibraryRepo) GetDistinctMetadataLanguagesForItem(ctx context.Context, contentID string) ([]string, error) {
	r.mu.Lock()
	forcedErr := r.languagesErr[contentID]
	override, hasOverride := r.languages[contentID]
	r.mu.Unlock()
	if forcedErr != nil {
		return nil, forcedErr
	}
	if hasOverride {
		return override, nil
	}
	folderIDs, err := r.GetFolderIDsForItem(ctx, contentID)
	if err != nil {
		return nil, err
	}
	if len(folderIDs) == 0 {
		return nil, nil
	}
	return []string{"en"}, nil
}

func (r *fakeLibraryRepo) CountFoldersForItem(ctx context.Context, contentID string) (int, error) {
	folderIDs, err := r.GetFolderIDsForItem(ctx, contentID)
	if err != nil {
		return 0, err
	}
	return len(folderIDs), nil
}

type fakeMetadataFolderRepo struct {
	folders map[int]*models.MediaFolder
	// lookupErrs forces a transient failure for a folder that otherwise
	// exists. Callers distinguish "this library is gone" from "this library
	// could not be read", so the fake has to be able to produce both.
	lookupErrs map[int]error
}

func (r *fakeMetadataFolderRepo) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	if err, ok := r.lookupErrs[id]; ok {
		return nil, err
	}
	if folder, ok := r.folders[id]; ok {
		cp := *folder
		return &cp, nil
	}
	return nil, catalog.ErrFolderNotFound
}

func (r *fakeMetadataFolderRepo) ListByIDs(_ context.Context, ids []int) ([]*models.MediaFolder, error) {
	var out []*models.MediaFolder
	for _, id := range ids {
		if err, ok := r.lookupErrs[id]; ok {
			return nil, err
		}
		if folder, ok := r.folders[id]; ok && folder.Enabled {
			cp := *folder
			out = append(out, &cp)
		}
	}
	return out, nil
}

// fakeRootClaimRepo implements metadataRootClaimRepo.
type fakeRootClaimRepo struct {
	mu     sync.Mutex
	claims map[string]string // "folderID:rootPath" -> contentID
}

func newFakeRootClaimRepo() *fakeRootClaimRepo {
	return &fakeRootClaimRepo{claims: make(map[string]string)}
}

func (r *fakeRootClaimRepo) Get(_ context.Context, folderID int, rootPath string) (*models.MediaItemRoot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", folderID, rootPath)
	if cid, ok := r.claims[key]; ok {
		return &models.MediaItemRoot{
			MediaFolderID:     folderID,
			CanonicalRootPath: rootPath,
			ContentID:         cid,
		}, nil
	}
	return nil, nil
}

func (r *fakeRootClaimRepo) ClaimRoot(_ context.Context, folderID int, rootPath, contentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", folderID, rootPath)
	if _, exists := r.claims[key]; !exists {
		r.claims[key] = contentID
	}
	return nil
}

type fakeGroupClaimRepo struct {
	mu     sync.Mutex
	claims map[string]string
}

func newFakeGroupClaimRepo() *fakeGroupClaimRepo {
	return &fakeGroupClaimRepo{claims: make(map[string]string)}
}

func (r *fakeGroupClaimRepo) Get(_ context.Context, folderID int, groupKeyVersion int, contentGroupKey string) (*models.MediaItemGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%d:%s", folderID, groupKeyVersion, contentGroupKey)
	if cid, ok := r.claims[key]; ok {
		return &models.MediaItemGroup{
			MediaFolderID:   folderID,
			GroupKeyVersion: groupKeyVersion,
			ContentGroupKey: contentGroupKey,
			ContentID:       cid,
		}, nil
	}
	return nil, nil
}

func (r *fakeGroupClaimRepo) ClaimGroup(_ context.Context, folderID int, groupKeyVersion int, contentGroupKey, contentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%d:%s", folderID, groupKeyVersion, contentGroupKey)
	if _, exists := r.claims[key]; !exists {
		r.claims[key] = contentID
	}
	return nil
}

func (r *fakeGroupClaimRepo) ClaimAndRelinkFiles(_ context.Context, folderID int, groupKeyVersion int, contentGroupKey, contentID string) (int, error) {
	if err := r.ClaimGroup(context.Background(), folderID, groupKeyVersion, contentGroupKey, contentID); err != nil {
		return 0, err
	}
	return 1, nil
}

// fakeSkippedRootRepo implements metadataSkippedRootRepo.
type fakeSkippedRootRepo struct {
	mu      sync.Mutex
	skipped map[string]models.SkippedMediaRoot // "folderID:rootPath" -> root
}

func newFakeSkippedRootRepo() *fakeSkippedRootRepo {
	return &fakeSkippedRootRepo{skipped: make(map[string]models.SkippedMediaRoot)}
}

func (r *fakeSkippedRootRepo) UpsertObservedFile(_ context.Context, folderID int, rootPath, reason, sampleFilePath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", folderID, rootPath)
	r.skipped[key] = models.SkippedMediaRoot{
		MediaFolderID:  folderID,
		RootPath:       rootPath,
		Reason:         reason,
		SampleFilePath: sampleFilePath,
		FileCount:      1,
	}
	return nil
}

func (r *fakeSkippedRootRepo) Delete(_ context.Context, folderID int, rootPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", folderID, rootPath)
	delete(r.skipped, key)
	return nil
}

type fakeScannedRootRepo struct {
	mu    sync.Mutex
	roots map[string]*models.ScannedMediaRoot
}

type fakeScannedGroupRepo struct {
	mu     sync.Mutex
	groups map[string]*models.ScannedMediaGroup
}

func newFakeScannedGroupRepo() *fakeScannedGroupRepo {
	return &fakeScannedGroupRepo{groups: make(map[string]*models.ScannedMediaGroup)}
}

func (r *fakeScannedGroupRepo) Get(_ context.Context, folderID int, groupKeyVersion int, contentGroupKey string) (*models.ScannedMediaGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%d:%s", folderID, groupKeyVersion, contentGroupKey)
	group, ok := r.groups[key]
	if !ok {
		return nil, nil
	}
	cp := *group
	return &cp, nil
}

func (r *fakeScannedGroupRepo) setGroup(group *models.ScannedMediaGroup) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%d:%s", group.MediaFolderID, group.GroupKeyVersion, group.ContentGroupKey)
	cp := *group
	r.groups[key] = &cp
}

func newFakeScannedRootRepo() *fakeScannedRootRepo {
	return &fakeScannedRootRepo{roots: make(map[string]*models.ScannedMediaRoot)}
}

func (r *fakeScannedRootRepo) Get(_ context.Context, folderID int, rootPath string) (*models.ScannedMediaRoot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", folderID, rootPath)
	root, ok := r.roots[key]
	if !ok {
		return nil, nil
	}
	cp := *root
	return &cp, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

type testHarness struct {
	service          *MetadataService
	itemRepo         *fakeItemRepo
	fileRepo         *fakeFileRepo
	libraryRepo      *fakeLibraryRepo
	rootClaimRepo    *fakeRootClaimRepo
	groupClaimRepo   *fakeGroupClaimRepo
	skippedRootRepo  *fakeSkippedRootRepo
	scannedRootRepo  *fakeScannedRootRepo
	scannedGroupRepo *fakeScannedGroupRepo
}

func newTestHarness() *testHarness {
	h := &testHarness{
		itemRepo:         newFakeItemRepo(),
		fileRepo:         newFakeFileRepo(),
		libraryRepo:      newFakeLibraryRepo(),
		rootClaimRepo:    newFakeRootClaimRepo(),
		groupClaimRepo:   newFakeGroupClaimRepo(),
		skippedRootRepo:  newFakeSkippedRootRepo(),
		scannedRootRepo:  newFakeScannedRootRepo(),
		scannedGroupRepo: newFakeScannedGroupRepo(),
	}
	h.service = &MetadataService{
		itemRepo:         h.itemRepo,
		fileRepo:         h.fileRepo,
		libraryRepo:      h.libraryRepo,
		rootClaimRepo:    h.rootClaimRepo,
		groupClaimRepo:   h.groupClaimRepo,
		skippedRootRepo:  h.skippedRootRepo,
		scannedRootRepo:  h.scannedRootRepo,
		scannedGroupRepo: h.scannedGroupRepo,
	}
	return h
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestSyncRefreshDebtForItemPreservesRequestedEpisodeDebt(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	rating := 7.5
	h.itemRepo.items["series-1"] = &models.MediaItem{
		ContentID:                 "series-1",
		Type:                      "series",
		Status:                    "matched",
		Overview:                  "Complete",
		PosterPath:                testPosterPath,
		BackdropPath:              testBackdropPath,
		RatingTMDB:                &rating,
		EpisodeMetadataIncomplete: true,
	}
	debts.debts["series-1"] = &models.MetadataRefreshDebt{
		TargetType:    RefreshTargetItem,
		ContentID:     "series-1",
		ReasonMask:    RefreshDebtReasonEpisodeIncomplete,
		AttemptCount:  1,
		NextRefreshAt: time.Now().UTC(),
	}

	if err := h.service.syncRefreshDebtForItem(ctx, "series-1"); err != nil {
		t.Fatalf("syncRefreshDebtForItem: %v", err)
	}
	debt, err := debts.Get(ctx, "series-1")
	if err != nil {
		t.Fatalf("Get debt: %v", err)
	}
	if !hasRefreshDebtReason(debt.ReasonMask, RefreshDebtReasonEpisodeIncomplete) {
		t.Fatalf("reason mask = %d, want episode incomplete", debt.ReasonMask)
	}

	item := *h.itemRepo.items["series-1"]
	item.EpisodeMetadataIncomplete = false
	h.itemRepo.items["series-1"] = &item
	if err := h.service.syncRefreshDebtForItem(ctx, "series-1"); err != nil {
		t.Fatalf("syncRefreshDebtForItem complete: %v", err)
	}
	if _, err := debts.Get(ctx, "series-1"); !errors.Is(err, ErrRefreshDebtNotFound) {
		t.Fatalf("Get debt after complete = %v, want ErrRefreshDebtNotFound", err)
	}
}

func TestSyncRefreshDebtForItemClearsDebtAfterSecondaryTMDBRejected(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	debts := newFakeRefreshDebtRepo()
	staleIDs := newFakeStaleIDRepo()
	h.service.refreshDebtRepo = debts
	h.service.staleIDRepo = staleIDs
	rating := 8.0
	h.itemRepo.items["series-1"] = &models.MediaItem{
		ContentID:    "series-1",
		Type:         "series",
		Status:       "matched",
		TvdbID:       "405851",
		Overview:     "Complete",
		PosterPath:   testPosterPath,
		BackdropPath: testBackdropPath,
		RatingTMDB:   &rating,
	}
	staleIDs.set("series-1", &models.StaleMediaID{
		ContentID: "series-1", Provider: testTMDBProvider, ProviderID: "12236904",
	})
	debts.debts["series-1"] = &models.MetadataRefreshDebt{
		TargetType: RefreshTargetItem, ContentID: "series-1", ReasonMask: RefreshDebtReasonStaleProviderID,
	}

	if err := h.service.syncRefreshDebtForItem(ctx, "series-1"); err != nil {
		t.Fatalf("syncRefreshDebtForItem: %v", err)
	}
	if _, err := debts.Get(ctx, "series-1"); !errors.Is(err, ErrRefreshDebtNotFound) {
		t.Fatalf("Get debt after rejected secondary tmdb = %v, want ErrRefreshDebtNotFound", err)
	}
}

func TestSyncRefreshDebtForItemKeepsFailedCurrentProviderID(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	debts := newFakeRefreshDebtRepo()
	staleIDs := newFakeStaleIDRepo()
	h.service.refreshDebtRepo = debts
	h.service.staleIDRepo = staleIDs
	rating := 8.0
	h.itemRepo.items["series-1"] = &models.MediaItem{
		ContentID:    "series-1",
		Type:         "series",
		Status:       "matched",
		TvdbID:       "405851",
		Overview:     "Complete",
		PosterPath:   testPosterPath,
		BackdropPath: testBackdropPath,
		RatingTMDB:   &rating,
	}
	staleIDs.set("series-1", &models.StaleMediaID{
		ContentID: "series-1", Provider: testTVDBProvider, ProviderID: "405851",
	})

	if err := h.service.syncRefreshDebtForItem(ctx, "series-1"); err != nil {
		t.Fatalf("syncRefreshDebtForItem: %v", err)
	}
	debt, err := debts.Get(ctx, "series-1")
	if err != nil {
		t.Fatalf("Get debt: %v", err)
	}
	if !hasRefreshDebtReason(debt.ReasonMask, RefreshDebtReasonStaleProviderID) {
		t.Fatalf("reason mask = %d, want stale provider ID", debt.ReasonMask)
	}
}

func TestShouldReanchorProviderContentIDRequiresManualRefreshOrCorrection(t *testing.T) {
	const anchoredID = "movie-tmdb-111"
	tests := []struct {
		name      string
		contentID string
		isNew     bool
		mode      RefreshMode
		corrected bool
		want      bool
	}{
		{
			name:      "scheduled refresh",
			contentID: anchoredID, mode: ModeScheduledRefresh,
		},
		{
			name:      "identify confirming the match",
			contentID: anchoredID, mode: ModeIdentify,
		},
		{
			name:      "identify rejecting the live anchor",
			contentID: anchoredID, mode: ModeIdentify, corrected: true, want: true,
		},
		{
			name:      "scheduled refresh with a corrected identity",
			contentID: anchoredID, mode: ModeScheduledRefresh, corrected: true,
		},
		{
			name:      "initial match keeping the tag anchor",
			contentID: anchoredID, mode: ModeInitialMatch,
		},
		{
			name:      "initial match whose NFO ids replaced the tag anchor",
			contentID: anchoredID, mode: ModeInitialMatch, corrected: true, want: true,
		},
		{
			name:      "manual refresh",
			contentID: anchoredID, mode: ModeManualRefresh, want: true,
		},
		{
			name:      "new item never reanchors",
			contentID: anchoredID, isNew: true, mode: ModeManualRefresh,
		},
		{
			name:      "local item",
			contentID: "local-deadbeef", mode: ModeManualRefresh,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldReanchorProviderContentID(tt.contentID, tt.isNew, tt.mode, tt.corrected); got != tt.want {
				t.Fatalf("shouldReanchorProviderContentID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRefreshSeriesEpisodeMetadataStateUsesActionableDebt(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)

	t.Run("provider numbered episode clears stale debt", func(t *testing.T) {
		h := newTestHarness()
		ctx := context.Background()
		episodes := newFakeEpisodeRepo()
		debts := newFakeRefreshDebtRepo()
		h.service.episodeRepo = episodes
		h.service.refreshDebtRepo = debts
		h.itemRepo.items["series-provider"] = &models.MediaItem{
			ContentID:                 "series-provider",
			Type:                      "series",
			EpisodeMetadataIncomplete: true,
		}
		if err := episodes.Upsert(ctx, &models.Episode{
			ContentID:      "episode-provider",
			SeriesID:       "series-provider",
			SeasonID:       "season-provider",
			SeasonNumber:   1,
			EpisodeNumber:  1,
			Title:          "Episode 1",
			TvdbID:         "9607609",
			MetadataSource: "provider",
		}); err != nil {
			t.Fatalf("seed provider episode: %v", err)
		}
		debts.debts[fakeRefreshDebtKey(RefreshTargetEpisode, "episode-provider")] = &models.MetadataRefreshDebt{
			TargetType: RefreshTargetEpisode,
			ContentID:  "episode-provider",
			ReasonMask: RefreshDebtReasonEpisodeIncomplete,
		}

		h.service.refreshSeriesEpisodeMetadataState(ctx, "series-provider", now)

		item, err := h.itemRepo.GetByID(ctx, "series-provider")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if item.EpisodeMetadataIncomplete {
			t.Fatal("expected provider numbered episode to clear series incomplete flag")
		}
		if item.EpisodeMetadataLastCheckedAt == nil || !item.EpisodeMetadataLastCheckedAt.Equal(now) {
			t.Fatalf("last checked at = %v, want %v", item.EpisodeMetadataLastCheckedAt, now)
		}
		if _, err := debts.GetTarget(ctx, RefreshTargetEpisode, "episode-provider"); !errors.Is(err, ErrRefreshDebtNotFound) {
			t.Fatalf("provider episode debt after refresh = %v, want ErrRefreshDebtNotFound", err)
		}
	})

	t.Run("scanner fallback episode creates debt", func(t *testing.T) {
		h := newTestHarness()
		ctx := context.Background()
		episodes := newFakeEpisodeRepo()
		debts := newFakeRefreshDebtRepo()
		h.service.episodeRepo = episodes
		h.service.refreshDebtRepo = debts
		h.itemRepo.items["series-fallback"] = &models.MediaItem{
			ContentID: "series-fallback",
			Type:      "series",
		}
		if err := episodes.Upsert(ctx, &models.Episode{
			ContentID:      "episode-fallback",
			SeriesID:       "series-fallback",
			SeasonID:       "season-fallback",
			SeasonNumber:   1,
			EpisodeNumber:  1,
			Title:          "Episode 1",
			MetadataSource: "scanner_fallback",
		}); err != nil {
			t.Fatalf("seed fallback episode: %v", err)
		}

		h.service.refreshSeriesEpisodeMetadataState(ctx, "series-fallback", now)

		item, err := h.itemRepo.GetByID(ctx, "series-fallback")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if !item.EpisodeMetadataIncomplete {
			t.Fatal("expected fallback episode to keep series incomplete flag")
		}
		debt, err := debts.GetTarget(ctx, RefreshTargetEpisode, "episode-fallback")
		if err != nil {
			t.Fatalf("GetTarget fallback debt: %v", err)
		}
		if !hasRefreshDebtReason(debt.ReasonMask, RefreshDebtReasonEpisodeIncomplete) {
			t.Fatalf("fallback debt reason mask = %d, want episode incomplete", debt.ReasonMask)
		}
	})

	t.Run("old provider TBD episode clears while fallback episode keeps debt", func(t *testing.T) {
		h := newTestHarness()
		ctx := context.Background()
		episodes := newFakeEpisodeRepo()
		debts := newFakeRefreshDebtRepo()
		h.service.episodeRepo = episodes
		h.service.refreshDebtRepo = debts
		h.itemRepo.items["series-mixed"] = &models.MediaItem{
			ContentID:                 "series-mixed",
			Type:                      "series",
			EpisodeMetadataIncomplete: true,
		}
		old := now.Add(-400 * 24 * time.Hour)
		for _, ep := range []*models.Episode{
			{
				ContentID:      "episode-tbd",
				SeriesID:       "series-mixed",
				SeasonID:       "season-mixed",
				SeasonNumber:   1,
				EpisodeNumber:  1,
				Title:          "TBD",
				Overview:       "Provider overview",
				StillPath:      "s3://still.jpg",
				AirDate:        &old,
				TmdbID:         "3812334",
				MetadataSource: "provider",
			},
			{
				ContentID:      "episode-fallback",
				SeriesID:       "series-mixed",
				SeasonID:       "season-mixed",
				SeasonNumber:   1,
				EpisodeNumber:  2,
				Title:          "Episode 2",
				MetadataSource: "scanner_fallback",
			},
		} {
			if err := episodes.Upsert(ctx, ep); err != nil {
				t.Fatalf("seed %s: %v", ep.ContentID, err)
			}
		}
		debts.debts[fakeRefreshDebtKey(RefreshTargetEpisode, "episode-tbd")] = &models.MetadataRefreshDebt{
			TargetType:   RefreshTargetEpisode,
			ContentID:    "episode-tbd",
			ReasonMask:   RefreshDebtReasonEpisodeIncomplete,
			AttemptCount: refreshDebtEpisodeTerminalAttempts,
		}

		h.service.refreshSeriesEpisodeMetadataState(ctx, "series-mixed", now)

		if _, err := debts.GetTarget(ctx, RefreshTargetEpisode, "episode-tbd"); !errors.Is(err, ErrRefreshDebtNotFound) {
			t.Fatalf("real TBD episode debt after refresh = %v, want ErrRefreshDebtNotFound", err)
		}
		debt, err := debts.GetTarget(ctx, RefreshTargetEpisode, "episode-fallback")
		if err != nil || !hasRefreshDebtReason(debt.ReasonMask, RefreshDebtReasonEpisodeIncomplete) {
			t.Fatalf("fallback episode debt = %+v, %v; want episode incomplete", debt, err)
		}
		item, err := h.itemRepo.GetByID(ctx, "series-mixed")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if !item.EpisodeMetadataIncomplete {
			t.Fatal("expected the fallback episode to keep the series incomplete flag")
		}
	})

	t.Run("terminal recent TBD debt wakes when the recent window closes", func(t *testing.T) {
		h := newTestHarness()
		ctx := context.Background()
		episodes := newFakeEpisodeRepo()
		debts := newFakeRefreshDebtRepo()
		h.service.episodeRepo = episodes
		h.service.refreshDebtRepo = debts
		h.itemRepo.items["series-recent"] = &models.MediaItem{ContentID: "series-recent", Type: "series"}
		aired := now.Add(-10 * 24 * time.Hour)
		if err := episodes.Upsert(ctx, &models.Episode{
			ContentID:      "episode-recent-tbd",
			SeriesID:       "series-recent",
			SeasonID:       "season-recent",
			SeasonNumber:   1,
			EpisodeNumber:  1,
			Title:          "TBD",
			StillPath:      "s3://still.jpg",
			AirDate:        &aired,
			TmdbID:         "3812334",
			MetadataSource: "provider",
		}); err != nil {
			t.Fatalf("seed recent TBD episode: %v", err)
		}
		debts.debts[fakeRefreshDebtKey(RefreshTargetEpisode, "episode-recent-tbd")] = &models.MetadataRefreshDebt{
			TargetType:   RefreshTargetEpisode,
			ContentID:    "episode-recent-tbd",
			ReasonMask:   RefreshDebtReasonEpisodeIncomplete,
			AttemptCount: refreshDebtEpisodeTerminalAttempts,
		}

		h.service.refreshSeriesEpisodeMetadataState(ctx, "series-recent", now)

		debt, err := debts.GetTarget(ctx, RefreshTargetEpisode, "episode-recent-tbd")
		if err != nil {
			t.Fatalf("GetTarget recent TBD debt: %v", err)
		}
		want := aired.Add(EpisodeRecentStillWindow + time.Minute)
		if !debt.NextRefreshAt.Equal(want) {
			t.Fatalf("next refresh = %v, want %v (window close, not the %v terminal delay)", debt.NextRefreshAt, want, refreshDebtTerminalDelay)
		}
		if EpisodeHasActionableMetadataDebt(&models.Episode{
			Title: "TBD", StillPath: "s3://still.jpg", AirDate: &aired, TmdbID: "3812334", MetadataSource: "provider",
		}, want) {
			t.Fatal("recent TBD episode still owes debt when the debt row wakes")
		}
	})
}

func TestRequestStaleMetadataRefreshStartsOnDemandRefreshOnce(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var startedOnce sync.Once
	var doneOnce sync.Once
	var calls atomic.Int32
	h.service.hooks.process = func(ctx context.Context, req ProcessRequest) (*ProcessResult, error) {
		calls.Add(1)
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		doneOnce.Do(func() { close(done) })
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	if err := h.service.RequestStaleMetadataRefresh(ctx, RefreshTargetItem, "item-1"); err != nil {
		t.Fatalf("RequestStaleMetadataRefresh first: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for on-demand refresh to start")
	}
	if err := h.service.RequestStaleMetadataRefresh(ctx, RefreshTargetItem, "item-1"); err != nil {
		t.Fatalf("RequestStaleMetadataRefresh second: %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("on-demand refresh calls = %d, want 1 while first is running", got)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for on-demand refresh to complete")
	}
}

// TestCreateOrFindSkeleton_FailureLeavesNoItem covers skeleton creation that
// fails: no item, library membership or file link may be left behind.
func TestCreateOrFindSkeleton_FailureLeavesNoItem(t *testing.T) {
	linkErr := errors.New("media file not found")
	for _, tc := range []struct {
		name             string
		setup            func(h *testHarness)
		wantErr          func(error) bool
		checkMemberships bool
		checkUnlinked    bool
	}{
		{
			name: "disabled folder",
			setup: func(h *testHarness) {
				h.service.folderRepo = &fakeMetadataFolderRepo{
					folders: map[int]*models.MediaFolder{10: {ID: 10, Type: "movies", Enabled: false}},
				}
			},
			wantErr:       func(err error) bool { return strings.Contains(err.Error(), "disabled") },
			checkUnlinked: true,
		},
		{
			name:             "link failure deletes the skeleton",
			setup:            func(h *testHarness) { h.fileRepo.updateErrors[1] = linkErr },
			wantErr:          func(err error) bool { return errors.Is(err, linkErr) },
			checkMemberships: true,
		},
		{
			name: "folder disabled before membership deletes the skeleton",
			setup: func(h *testHarness) {
				folder := &models.MediaFolder{ID: 10, Type: "movies", Enabled: true}
				h.service.folderRepo = &fakeMetadataFolderRepo{folders: map[int]*models.MediaFolder{10: folder}}
				h.fileRepo.afterUpdate = func(_ int, contentID string) {
					if contentID != "" {
						folder.Enabled = false
					}
				}
			},
			wantErr:          func(err error) bool { return strings.Contains(err.Error(), "disabled") },
			checkMemberships: true,
			checkUnlinked:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			tc.setup(h)
			file := &models.MediaFile{ID: 1, MediaFolderID: 10, FilePath: "/media/movies/Inception (2010)/Inception.mkv"}

			result, err := h.service.createOrFindSkeleton(context.Background(), file, 10)
			if err == nil || !tc.wantErr(err) {
				t.Fatalf("err = %v, want the %s error", err, tc.name)
			}
			if result != nil {
				t.Fatalf("expected no result, got %+v", result)
			}
			if len(h.itemRepo.items) != 0 {
				t.Fatalf("expected no item to remain, got %d", len(h.itemRepo.items))
			}
			if tc.checkMemberships && len(h.libraryRepo.memberships) != 0 {
				t.Fatalf("expected no library memberships, got %d", len(h.libraryRepo.memberships))
			}
			if cid := h.fileRepo.contentIDs[file.ID]; tc.checkUnlinked && cid != "" {
				t.Fatalf("expected file to remain unlinked, got content_id %q", cid)
			}
		})
	}
}

// TestCreateOrFindSkeleton_SecondMovieFileDoesNotReuseSameContentID verifies
// that movie files sharing a scanner content group stay separate until
// metadata confirms a real shared identity.
func TestCreateOrFindSkeleton_SecondMovieFileDoesNotReuseSameContentID(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file1 := &models.MediaFile{
		ID:              1,
		MediaFolderID:   10,
		FilePath:        "/media/movies/Inception (2010)/Inception.mkv",
		ContentGroupKey: "v1|movie|inception|2010",
		GroupKeyVersion: 1,
		BaseTitle:       "Inception",
		BaseYear:        2010,
		BaseType:        "movie",
	}
	file2 := &models.MediaFile{
		ID:              2,
		MediaFolderID:   10,
		FilePath:        "/media/movies/Inception (2010)/Inception.srt.mkv",
		ContentGroupKey: "v1|movie|inception|2010",
		GroupKeyVersion: 1,
		BaseTitle:       "Inception",
		BaseYear:        2010,
		BaseType:        "movie",
	}

	result1, err := h.service.createOrFindSkeleton(ctx, file1, 10)
	if err != nil {
		t.Fatalf("first file: %v", err)
	}

	result2, err := h.service.createOrFindSkeleton(ctx, file2, 10)
	if err != nil {
		t.Fatalf("second file: %v", err)
	}

	if result2.ContentID == result1.ContentID {
		t.Errorf("expected second movie file to get a new content_id, got reused %q",
			result1.ContentID)
	}

	if !result2.IsNew {
		t.Error("expected IsNew=true for second movie file under same content group")
	}
	if len(h.groupClaimRepo.claims) != 0 {
		t.Fatalf("expected no movie group claims, got %d", len(h.groupClaimRepo.claims))
	}
}

func TestClaimConfirmedSeriesRootOwnership_ClaimsRootAndGroups(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	files := []*models.MediaFile{
		{
			ID:               1,
			MediaFolderID:    10,
			ObservedRootPath: "/media/shows/Example Show",
			GroupKeyVersion:  1,
			ContentGroupKey:  "v1|series|example_show|2024",
		},
		{
			ID:               2,
			MediaFolderID:    10,
			ObservedRootPath: "/media/shows/Example Show",
			GroupKeyVersion:  1,
			ContentGroupKey:  "v1|series|example_show|2024",
		},
		{
			ID:               3,
			MediaFolderID:    10,
			ObservedRootPath: "/media/shows/Example Show",
			GroupKeyVersion:  2,
			ContentGroupKey:  "v2|series|example_show|2024",
		},
	}

	h.service.claimConfirmedSeriesRootOwnership(ctx, 10, "/media/shows/Example Show", "matched-series", files)

	if got := h.rootClaimRepo.claims["10:/media/shows/Example Show"]; got != "matched-series" {
		t.Fatalf("root claim = %q, want matched-series", got)
	}
	if got := h.groupClaimRepo.claims["10:1:v1|series|example_show|2024"]; got != "matched-series" {
		t.Fatalf("group claim v1 = %q, want matched-series", got)
	}
	if got := h.groupClaimRepo.claims["10:2:v2|series|example_show|2024"]; got != "matched-series" {
		t.Fatalf("group claim v2 = %q, want matched-series", got)
	}
	if len(h.groupClaimRepo.claims) != 2 {
		t.Fatalf("group claim count = %d, want 2", len(h.groupClaimRepo.claims))
	}
}

func TestLocalProviderContextForContent_ScopesToRequestedLibrary(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	h.fileRepo.setGroupFiles(10, 1, "movie-a",
		&models.MediaFile{
			ID:               1,
			MediaFolderID:    10,
			FilePath:         "/library-a/Movie/Movie.mkv",
			ObservedRootPath: "/library-a/Movie",
		},
	)
	h.fileRepo.setGroupFiles(20, 1, "movie-b",
		&models.MediaFile{
			ID:               2,
			MediaFolderID:    20,
			FilePath:         "/library-b/Movie/Movie.mkv",
			ObservedRootPath: "/library-b/Movie",
		},
	)
	h.fileRepo.contentIDs[1] = "shared-content"
	h.fileRepo.contentIDs[2] = "shared-content"

	localCtx := h.service.localProviderContextForContent(ctx, "shared-content", 20)

	if got, want := localCtx.filePath, "/library-b/Movie/Movie.mkv"; got != want {
		t.Fatalf("filePath = %q, want %q", got, want)
	}
	if got, want := localCtx.representativeFilePath, "/library-b/Movie/Movie.mkv"; got != want {
		t.Fatalf("representativeFilePath = %q, want %q", got, want)
	}
	if got, want := localCtx.observedRootPath, "/library-b/Movie"; got != want {
		t.Fatalf("observedRootPath = %q, want %q", got, want)
	}
	if !slices.Equal(localCtx.allGroupFilePaths, []string{"/library-b/Movie/Movie.mkv"}) {
		t.Fatalf("allGroupFilePaths = %v", localCtx.allGroupFilePaths)
	}
	if !slices.Equal(localCtx.primarySidecarSearchPaths, []string{"/library-b/Movie"}) {
		t.Fatalf("primarySidecarSearchPaths = %v", localCtx.primarySidecarSearchPaths)
	}
}

func TestIdentityCorrectionRejectsLiveAnchor(t *testing.T) {
	rejected := make(providerIDValueSet)
	rejected.add("tvdb", "73244")
	stale := make(providerIDValueSet)
	stale.add("tvdb", "73244")
	corrected := map[string]string{"tvdb": "78107", "tmdb": "2316"}
	if !identityCorrectionRejectsLiveAnchor("series-tvdb-73244", corrected, rejected) {
		t.Error("a rejected live anchor should re-anchor")
	}
	if identityCorrectionRejectsLiveAnchor("series-tvdb-73244", corrected, rejected, stale) {
		t.Error("a rejected anchor recorded stale should keep the id")
	}
	if identityCorrectionRejectsLiveAnchor("series-tvdb-78107", corrected, rejected) {
		t.Error("an anchor the correction kept should keep the id")
	}
	if identityCorrectionRejectsLiveAnchor("146000000000000100", corrected, rejected) {
		t.Error("a legacy id has no anchor to reject")
	}
}

// A single-key choice rejects the stored IDs it doesn't restate, but when the
// chosen title's providers return the anchor again the choice only added a
// provider ID to the same title, so the item keeps its id.
func TestIdentityCorrectionKeepsAnchorTheProvidersRestate(t *testing.T) {
	rejected := make(providerIDValueSet)
	rejected.add("tmdb", "555")
	sameShow := map[string]string{"tvdb": "78107", "tmdb": "555"}
	if identityCorrectionRejectsLiveAnchor("series-tmdb-555", sameShow, rejected) {
		t.Error("an anchor the chosen title's providers restate should keep the id")
	}

	rejected = make(providerIDValueSet)
	rejected.add("imdb", "tt0000100")
	if identityCorrectionRejectsLiveAnchor("movie-imdb-tt0000100", map[string]string{"tmdb": "200", "imdb": "TT0000100"}, rejected) {
		t.Error("an IMDb anchor restated in another case should keep the id")
	}
	if !identityCorrectionRejectsLiveAnchor("movie-imdb-tt0000100", map[string]string{"tmdb": "200"}, rejected) {
		t.Error("an anchor the chosen title's providers don't return should re-anchor")
	}
}
