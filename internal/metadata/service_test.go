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

// ---------------------------------------------------------------------------
// Fake repositories
// ---------------------------------------------------------------------------

// fakeItemRepo implements metadataItemRepo with an in-memory store.
type fakeItemRepo struct {
	mu    sync.Mutex
	items map[string]*models.MediaItem
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
	return nil, fmt.Errorf("item not found: %s", contentID)
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

func (r *fakeItemRepo) ReplacePeople(_ context.Context, _ string, _ []models.ItemPerson) error {
	return nil
}

func (r *fakeItemRepo) ListUnmatchedByFolderAndPathPrefix(_ context.Context, _ int, _ string, _ int) ([]string, error) {
	return nil, nil
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

func (r *fakeRefreshDebtRepo) RequestDue(
	ctx context.Context,
	targetType string,
	contentID string,
	priority int,
	reasonMask int64,
	nextRefreshAt time.Time,
	_ time.Duration,
) error {
	return r.UpsertTargetDebt(ctx, targetType, contentID, priority, reasonMask, nextRefreshAt)
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
	r.debts[key] = &models.MetadataRefreshDebt{
		TargetType:    targetType,
		ContentID:     contentID,
		Priority:      priority,
		ReasonMask:    reasonMask,
		NextRefreshAt: nextRefreshAt,
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
	r.debts[key] = &models.MetadataRefreshDebt{
		TargetType:    targetType,
		ContentID:     contentID,
		Priority:      priority,
		ReasonMask:    reasonMask,
		NextRefreshAt: nextRefreshAt,
	}
	return nil
}

func (r *fakeRefreshDebtRepo) DeleteDebt(ctx context.Context, contentID string) error {
	return r.DeleteTargetDebt(ctx, RefreshTargetItem, contentID)
}

func (r *fakeRefreshDebtRepo) DeleteTargetDebt(_ context.Context, targetType, contentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.debts, fakeRefreshDebtKey(targetType, contentID))
	return nil
}

// fakeFileRepo implements FileContentUpdater with an in-memory store.
type fakeFileRepo struct {
	mu                  sync.Mutex
	contentIDs          map[int]string    // fileID -> contentID
	rootContent         map[string]string // "folderID:rootPath" -> contentID
	rootCandidates      map[string][]string
	rootCandidateStatus map[string]string
	groupContent        map[string]string // "folderID:version:key" -> contentID
	groupFiles          map[string][]*models.MediaFile
	matchStamps         map[int]time.Time // fileID -> match_attempted_at
	updateErrors        map[int]error
	afterUpdate         func(fileID int, contentID string)
	claimUnmatchedCalls int
	claimNonSeriesCalls int
	claimMixedCalls     int
}

func newFakeFileRepo() *fakeFileRepo {
	return &fakeFileRepo{
		contentIDs:          make(map[int]string),
		rootContent:         make(map[string]string),
		rootCandidates:      make(map[string][]string),
		rootCandidateStatus: make(map[string]string),
		groupContent:        make(map[string]string),
		groupFiles:          make(map[string][]*models.MediaFile),
		matchStamps:         make(map[int]time.Time),
		updateErrors:        make(map[int]error),
	}
}

func (r *fakeFileRepo) UpdateContentID(_ context.Context, fileID int, contentID string) error {
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
	if candidates := r.rootCandidates[key]; len(candidates) > 0 {
		for _, candidate := range candidates {
			if strings.EqualFold(strings.TrimSpace(r.rootCandidateStatus[candidate]), "matched") {
				return candidate, nil
			}
		}
		return candidates[0], nil
	}
	if cid, ok := r.rootContent[key]; ok {
		return cid, nil
	}
	return "", nil
}

func (r *fakeFileRepo) GetByContentID(_ context.Context, contentID string) ([]*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *fakeFileRepo) UpdateContentIDByObservedRootPath(_ context.Context, folderID int, observedRootPath, contentID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	updated := 0
	for _, files := range r.groupFiles {
		for _, file := range files {
			if file == nil || file.MediaFolderID != folderID || file.ObservedRootPath != observedRootPath {
				continue
			}
			if existing := r.contentIDs[file.ID]; existing == contentID {
				continue
			}
			r.contentIDs[file.ID] = contentID
			updated++
		}
	}
	r.rootContent[fmt.Sprintf("%d:%s", folderID, observedRootPath)] = contentID
	return updated, nil
}

// setRootContent pre-seeds a root path -> content_id mapping for testing.
func (r *fakeFileRepo) setRootContent(folderID int, rootPath, contentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", folderID, rootPath)
	r.rootContent[key] = contentID
}

func (r *fakeFileRepo) setRootCandidates(folderID int, rootPath string, candidates map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", folderID, rootPath)
	r.rootCandidates[key] = r.rootCandidates[key][:0]
	for contentID, status := range candidates {
		r.rootCandidates[key] = append(r.rootCandidates[key], contentID)
		r.rootCandidateStatus[contentID] = status
	}
	slices.Sort(r.rootCandidates[key])
}

func (r *fakeFileRepo) setGroupContent(folderID int, groupKeyVersion int, contentGroupKey, contentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%d:%s", folderID, groupKeyVersion, contentGroupKey)
	r.groupContent[key] = contentID
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
				return files, nil
			}
		}
	}
	return files, nil
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
				return files, nil
			}
		}
	}
	return files, nil
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
				return files, nil
			}
		}
	}
	return files, nil
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
	mu          sync.Mutex
	memberships map[string]time.Time // "contentID:folderID" -> first_seen_at
}

func newFakeLibraryRepo() *fakeLibraryRepo {
	return &fakeLibraryRepo{memberships: make(map[string]time.Time)}
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
}

func (r *fakeMetadataFolderRepo) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	if folder, ok := r.folders[id]; ok {
		cp := *folder
		return &cp, nil
	}
	return nil, fmt.Errorf("folder not found: %d", id)
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

func (r *fakeSkippedRootRepo) Upsert(_ context.Context, root models.SkippedMediaRoot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", root.MediaFolderID, root.RootPath)
	r.skipped[key] = root
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

func (r *fakeScannedRootRepo) setRoot(root *models.ScannedMediaRoot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d:%s", root.MediaFolderID, root.RootPath)
	cp := *root
	r.roots[key] = &cp
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

func TestRecordRefreshFailure_IncrementsDebtAttemptForManualRefresh(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	h.itemRepo.items["item-1"] = &models.MediaItem{
		ContentID: "item-1",
		Status:    "matched",
	}
	debts.debts["item-1"] = &models.MetadataRefreshDebt{
		ContentID:     "item-1",
		AttemptCount:  1,
		NextRefreshAt: time.Now().UTC(),
	}

	h.service.recordRefreshFailure(ctx, "item-1", fmt.Errorf("provider failed"), true)

	debt, err := debts.Get(ctx, "item-1")
	if err != nil {
		t.Fatalf("Get debt: %v", err)
	}
	if debt.AttemptCount != 2 {
		t.Fatalf("manual failure attempt_count = %d, want 2", debt.AttemptCount)
	}
	if debt.LastError != "provider failed" {
		t.Fatalf("last_error = %q, want provider failed", debt.LastError)
	}
}

func TestRecordRefreshFailure_KeepsClaimedAttemptForScheduledRefresh(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	h.itemRepo.items["item-1"] = &models.MediaItem{
		ContentID: "item-1",
		Status:    "matched",
	}
	debts.debts["item-1"] = &models.MetadataRefreshDebt{
		ContentID:     "item-1",
		AttemptCount:  1,
		NextRefreshAt: time.Now().UTC(),
	}

	h.service.recordRefreshFailure(ctx, "item-1", fmt.Errorf("provider failed"), false)

	debt, err := debts.Get(ctx, "item-1")
	if err != nil {
		t.Fatalf("Get debt: %v", err)
	}
	if debt.AttemptCount != 1 {
		t.Fatalf("scheduled failure attempt_count = %d, want 1", debt.AttemptCount)
	}
	if debt.LastError != "provider failed" {
		t.Fatalf("last_error = %q, want provider failed", debt.LastError)
	}
}

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
		PosterPath:                "/poster.jpg",
		BackdropPath:              "/backdrop.jpg",
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

// TestCreateOrFindSkeleton_NoFolderIDs verifies that createOrFindSkeleton
// creates a real item even when the parent directory has no embedded folder IDs
// (e.g. {tmdb-12345}). Before this change, such roots were skipped entirely.
func TestCreateOrFindSkeleton_NoFolderIDs(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// File under a folder without embedded IDs — title/year only.
	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must have created a new item.
	if !result.IsNew {
		t.Fatal("expected IsNew=true for first file under a new root")
	}

	if result.ContentID == "" {
		t.Fatal("expected a non-empty content_id")
	}

	// The item should exist in the repo with status "pending".
	item, err := h.itemRepo.GetByID(ctx, result.ContentID)
	if err != nil {
		t.Fatalf("item not found in repo: %v", err)
	}
	if item.Status != "pending" {
		t.Errorf("expected status=pending, got %q", item.Status)
	}

	// The file should be linked.
	if cid, ok := h.fileRepo.contentIDs[file.ID]; !ok || cid != result.ContentID {
		t.Errorf("file not linked to skeleton item: got %q", cid)
	}
}

func TestCreateOrFindSkeleton_DisabledFolderDoesNotCreateItem(t *testing.T) {
	h := newTestHarness()
	h.service.folderRepo = &fakeMetadataFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: false},
		},
	}
	ctx := context.Background()

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err == nil {
		t.Fatal("expected disabled folder error")
	}
	if result != nil {
		t.Fatalf("expected no result, got %+v", result)
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
	if len(h.itemRepo.items) != 0 {
		t.Fatalf("expected no items to be created, got %d", len(h.itemRepo.items))
	}
	if cid := h.fileRepo.contentIDs[file.ID]; cid != "" {
		t.Fatalf("expected file to remain unlinked, got content_id %q", cid)
	}
}

func TestCreateOrFindSkeleton_LinkFailureDeletesSkeleton(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	linkErr := errors.New("media file not found")
	h.fileRepo.updateErrors[1] = linkErr

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err == nil {
		t.Fatal("expected link error")
	}
	if result != nil {
		t.Fatalf("expected no result, got %+v", result)
	}
	if !errors.Is(err, linkErr) {
		t.Fatalf("expected wrapped link error, got %v", err)
	}
	if len(h.itemRepo.items) != 0 {
		t.Fatalf("expected skeleton item to be deleted, got %d items", len(h.itemRepo.items))
	}
	if len(h.libraryRepo.memberships) != 0 {
		t.Fatalf("expected no library memberships, got %d", len(h.libraryRepo.memberships))
	}
}

func TestCreateOrFindSkeleton_DisabledBeforeMembershipDeletesSkeleton(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	folder := &models.MediaFolder{ID: 10, Type: "movies", Enabled: true}
	h.service.folderRepo = &fakeMetadataFolderRepo{
		folders: map[int]*models.MediaFolder{10: folder},
	}
	h.fileRepo.afterUpdate = func(_ int, contentID string) {
		if contentID != "" {
			folder.Enabled = false
		}
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err == nil {
		t.Fatal("expected disabled folder error")
	}
	if result != nil {
		t.Fatalf("expected no result, got %+v", result)
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
	if len(h.itemRepo.items) != 0 {
		t.Fatalf("expected skeleton item to be deleted, got %d items", len(h.itemRepo.items))
	}
	if len(h.libraryRepo.memberships) != 0 {
		t.Fatalf("expected no library memberships, got %d", len(h.libraryRepo.memberships))
	}
	if cid := h.fileRepo.contentIDs[file.ID]; cid != "" {
		t.Fatalf("expected file link to be cleared, got content_id %q", cid)
	}
}

// TestCreateOrFindSkeleton_PendingItemGetsLibraryMembership verifies that
// library membership is created immediately for pending items, not just for
// matched items.
func TestCreateOrFindSkeleton_PendingItemGetsLibraryMembership(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Library membership must exist immediately after skeleton creation.
	if !h.libraryRepo.hasMembership(result.ContentID, 10) {
		t.Error("expected library membership for newly created pending item, but none found")
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

// TestCreateOrFindSkeleton_WithFolderIDs verifies that the happy-path with
// folder IDs still works correctly after the refactor.
func TestCreateOrFindSkeleton_WithFolderIDs(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010) {tmdb-27205}/Inception.mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsNew {
		t.Fatal("expected IsNew=true")
	}
	if result.TmdbID != "27205" {
		t.Errorf("expected tmdb_id=27205, got %q", result.TmdbID)
	}

	item, err := h.itemRepo.GetByID(ctx, result.ContentID)
	if err != nil {
		t.Fatalf("item not found: %v", err)
	}
	if item.TmdbID != "27205" {
		t.Errorf("expected item tmdb_id=27205, got %q", item.TmdbID)
	}
}

func TestCreateOrFindSkeleton_IDTaggedMovieFolderBeatsDivergentReleaseFilename(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeMetadataFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true},
		},
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/The Expendables 4 {imdb-tt3291150} {tmdb-299054}/Expend4bles (2023) [Remux-1080p 8-bit AVC TrueHD Atmos 7.1]-CiNEPHiLES.mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantRoot := "/media/movies/The Expendables 4 {imdb-tt3291150} {tmdb-299054}"
	if result.RootPath != wantRoot {
		t.Fatalf("RootPath = %q, want %q", result.RootPath, wantRoot)
	}
	if result.TmdbID != "299054" || result.ImdbID != "tt3291150" {
		t.Fatalf("provider IDs = (%q, %q), want tmdb 299054 and imdb tt3291150", result.TmdbID, result.ImdbID)
	}

	if _, exists := h.skippedRootRepo.skipped["10:/media/movies/The Expendables 4 {imdb-tt3291150} {tmdb-299054}/Expend4bles (2023) [Remux-1080p 8-bit AVC TrueHD Atmos 7.1]-CiNEPHiLES"]; exists {
		t.Fatal("unexpected skipped-root record for synthetic filename stem path")
	}
	if _, exists := h.skippedRootRepo.skipped["10:"+wantRoot]; exists {
		t.Fatal("unexpected skipped-root record for ID-tagged parent folder")
	}

	item, err := h.itemRepo.GetByID(ctx, result.ContentID)
	if err != nil {
		t.Fatalf("item not found: %v", err)
	}
	if item.TmdbID != "299054" || item.ImdbID != "tt3291150" {
		t.Fatalf("item provider IDs = (%q, %q), want tmdb 299054 and imdb tt3291150", item.TmdbID, item.ImdbID)
	}
}

func TestCreateOrFindSkeleton_TrustedFilenameIDBypassesAmbiguousScanner(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file := &models.MediaFile{
		ID:              1,
		MediaFolderID:   10,
		FilePath:        "/media/movies/Predator (1987)/Predator Ultimate Hunter Edition (1987) {tmdb-106}.mkv",
		GroupKeyVersion: 1,
		ContentGroupKey: "v1|movie|predator|1987",
	}
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID:   10,
		GroupKeyVersion: 1,
		ContentGroupKey: "v1|movie|predator|1987",
		BaseTitle:       "Predator",
		BaseYear:        1987,
		InferredType:    "movie",
		State:           "ambiguous",
	})

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := result.ItemStatus, "pending"; got != want {
		t.Fatalf("ItemStatus = %q, want %q", got, want)
	}
	if got, want := result.TmdbID, "106"; got != want {
		t.Fatalf("TmdbID = %q, want %q", got, want)
	}

	item, err := h.itemRepo.GetByID(ctx, result.ContentID)
	if err != nil {
		t.Fatalf("item not found: %v", err)
	}
	if got, want := item.Status, "pending"; got != want {
		t.Fatalf("item.Status = %q, want %q", got, want)
	}
	if got, want := item.TmdbID, "106"; got != want {
		t.Fatalf("item.TmdbID = %q, want %q", got, want)
	}
}

func TestCreateOrFindSkeleton_TrustedFilenameIDBeatsStaleFolderID(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "predator-106",
		Status:    "matched",
		Title:     "Predator",
		Year:      1987,
		Type:      "movie",
		TmdbID:    "106",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Predator (1987) {tmdb-999}/Predator Ultimate Hunter Edition (1987) {tmdb-106}.mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := result.ContentID, "predator-106"; got != want {
		t.Fatalf("ContentID = %q, want %q", got, want)
	}
	if result.IsNew {
		t.Fatal("expected matched external-id reuse to keep IsNew=false")
	}
	if got := h.fileRepo.contentIDs[file.ID]; got != "predator-106" {
		t.Fatalf("linked content_id = %q, want predator-106", got)
	}
}

func TestCreateOrFindSkeleton_MoviesLibraryEpisodeShapedMovieStaysMovie(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeMetadataFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true},
		},
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Type != "movie" {
		t.Fatalf("Type = %q, want movie", result.Type)
	}
	if result.RootPath != "/media/movies/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}" {
		t.Fatalf("RootPath = %q", result.RootPath)
	}
	if result.TmdbID != "588077" || result.ImdbID != "tt12261772" {
		t.Fatalf("provider IDs = (%q, %q), want tmdb 588077 and imdb tt12261772", result.TmdbID, result.ImdbID)
	}

	item, err := h.itemRepo.GetByID(ctx, result.ContentID)
	if err != nil {
		t.Fatalf("item not found: %v", err)
	}
	if item.Type != "movie" {
		t.Fatalf("item.Type = %q, want movie", item.Type)
	}
	if item.TmdbID != "588077" || item.ImdbID != "tt12261772" {
		t.Fatalf("item provider IDs = (%q, %q), want tmdb 588077 and imdb tt12261772", item.TmdbID, item.ImdbID)
	}
}

func TestCreateOrFindSkeleton_MixedLibraryEpisodeShapedMovieStaysMovie(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeMetadataFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "mixed", Enabled: true},
		},
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Type != "movie" {
		t.Fatalf("Type = %q, want movie", result.Type)
	}
	if result.RootPath != "/media/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}" {
		t.Fatalf("RootPath = %q", result.RootPath)
	}
}

// TestPendingItemLifecycle_UnmatchedTransition verifies that the worker
// correctly transitions a pending item to "unmatched" when enrichment fails.
func TestPendingItemLifecycle_UnmatchedTransition(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Pre-create an item with status "pending".
	contentID := "test-content-123"
	h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: contentID,
		Status:    "pending",
		Title:     "Test Movie",
		Year:      2020,
		Type:      "movie",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	})

	// Call updateItemStatus to simulate what the worker does on failure.
	h.service.updateItemStatus(ctx, contentID, "unmatched")

	item, err := h.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatalf("item not found: %v", err)
	}
	if item.Status != "unmatched" {
		t.Errorf("expected status=unmatched, got %q", item.Status)
	}
}

// TestCreateOrFindSkeleton_MovieIgnoresGroupClaimDedup verifies that scanner
// group claims do not merge movie files before metadata confirmation.
func TestCreateOrFindSkeleton_MovieIgnoresGroupClaimDedup(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	existingContentID := "existing-content-456"
	_, err := h.groupClaimRepo.ClaimAndRelinkFiles(ctx, 10, 1, "v1|movie|inception|2010", existingContentID)
	if err != nil {
		t.Fatalf("claiming test content group: %v", err)
	}

	// Also pre-seed the item so upsertLibraryMembership can find it.
	h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: existingContentID,
		Status:    "pending",
		Title:     "Inception",
		Year:      2010,
		Type:      "movie",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	})

	file := &models.MediaFile{
		ID:              5,
		MediaFolderID:   10,
		FilePath:        "/media/movies/Inception (2010)/Inception.720p.mkv",
		ContentGroupKey: "v1|movie|inception|2010",
		GroupKeyVersion: 1,
		BaseTitle:       "Inception",
		BaseYear:        2010,
		BaseType:        "movie",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContentID == existingContentID {
		t.Errorf("expected movie to ignore pre-confirmation group claim %q", existingContentID)
	}
	if !result.IsNew {
		t.Error("expected IsNew=true when only a movie group claim exists")
	}
}

func TestCreateOrFindSkeleton_MovieReusesMatchedGroupClaim(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	existingContentID := "existing-content-456"
	_, err := h.groupClaimRepo.ClaimAndRelinkFiles(ctx, 10, 1, "v1|movie|inception|2010", existingContentID)
	if err != nil {
		t.Fatalf("claiming test content group: %v", err)
	}

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: existingContentID,
		Status:    "matched",
		Title:     "Inception",
		Year:      2010,
		Type:      "movie",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	file := &models.MediaFile{
		ID:                5,
		MediaFolderID:     10,
		FilePath:          "/media/movies/Inception (2010)/Inception.720p.mkv",
		CanonicalRootPath: "/media/movies/Inception (2010)",
		ContentGroupKey:   "v1|movie|inception|2010",
		GroupKeyVersion:   1,
		BaseTitle:         "Inception",
		BaseYear:          2010,
		BaseType:          "movie",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContentID != existingContentID {
		t.Fatalf("result.ContentID = %q, want %q", result.ContentID, existingContentID)
	}
	if result.IsNew {
		t.Fatal("expected IsNew=false when reusing matched movie group claim")
	}
}

func TestCreateOrFindSkeleton_MovieIgnoresRootClaimDedup(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	existingContentID := "existing-content-root"
	if err := h.rootClaimRepo.ClaimRoot(ctx, 10, "/media/movies/Inception (2010)", existingContentID); err != nil {
		t.Fatalf("claiming test root: %v", err)
	}
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: existingContentID,
		Status:    "pending",
		Title:     "Inception",
		Year:      2010,
		Type:      "movie",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	file := &models.MediaFile{
		ID:            5,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.720p.mkv",
		BaseTitle:     "Inception",
		BaseYear:      2010,
		BaseType:      "movie",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContentID == existingContentID {
		t.Errorf("expected movie to ignore pre-confirmation root claim %q", existingContentID)
	}
	if !result.IsNew {
		t.Error("expected IsNew=true when only a movie root claim exists")
	}
}

func TestCreateOrFindSkeleton_MovieSkipsPendingExternalIDDedup(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "pending-predator",
		Status:    "pending",
		Title:     "Predator",
		Year:      1987,
		Type:      "movie",
		TmdbID:    "106",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Predator (1987) {tmdb-106}/Predator (1987).mkv",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContentID == "pending-predator" {
		t.Fatal("expected pending external-id movie item to be ignored")
	}
	if !result.IsNew {
		t.Fatal("expected IsNew=true when only a pending external-id movie item exists")
	}
}

func TestCreateOrFindSkeleton_MovieSkipsTitleYearDedup(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "pending-inception",
		Status:    "pending",
		Title:     "Inception",
		Year:      2010,
		Type:      "movie",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
		BaseTitle:     "Inception",
		BaseYear:      2010,
		BaseType:      "movie",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContentID == "pending-inception" {
		t.Fatal("expected movie title/year fallback dedup to be disabled")
	}
	if !result.IsNew {
		t.Fatal("expected IsNew=true when only title/year movie match exists")
	}
}

func TestCreateOrFindSkeleton_SeriesReusesObservedRootScopedItem(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	existingContentID := "series-root-shell"
	h.fileRepo.setRootContent(10, "/media/shows/Example Show", existingContentID)
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: existingContentID,
		Status:    "pending",
		Title:     "Example Show",
		Year:      2024,
		Type:      "series",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	file := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/shows/Example Show/Season 01/Example.Show.S01E02.mkv",
		ObservedRootPath: "/media/shows/Example Show",
		GroupKeyVersion:  1,
		ContentGroupKey:  "v1|series|example_show|2024",
		BaseTitle:        "Example Show",
		BaseYear:         2024,
		BaseType:         "series",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContentID != existingContentID {
		t.Fatalf("ContentID = %q, want %q", result.ContentID, existingContentID)
	}
	if result.IsNew {
		t.Fatal("expected IsNew=false for same observed-root series shell reuse")
	}
}

func TestCreateOrFindSkeleton_SeriesSkipsPendingExternalIDDedupAcrossRoots(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "pending-series",
		Status:    "pending",
		Title:     "Example Show",
		Year:      2024,
		Type:      "series",
		TmdbID:    "12345",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	file := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/shows/Example Show Dolby Vision {tmdb-12345}/Season 01/Example.Show.S01E01.mkv",
		ObservedRootPath: "/media/shows/Example Show Dolby Vision {tmdb-12345}",
		BaseTitle:        "Example Show",
		BaseYear:         2024,
		BaseType:         "series",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContentID == "pending-series" {
		t.Fatal("expected pending external-id series item to be ignored across roots")
	}
	if !result.IsNew {
		t.Fatal("expected IsNew=true when only a pending external-id series item exists")
	}
}

func TestCreateOrFindSkeleton_SeriesPrefersMatchedRootItemOverProvisionalShell(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "pending-root-shell",
		Status:    "pending",
		Title:     "Example Show",
		Year:      2024,
		Type:      "series",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert pending shell: %v", err)
	}
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "matched-series",
		Status:    "matched",
		Title:     "Example Show",
		Year:      2024,
		Type:      "series",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert matched series: %v", err)
	}
	h.fileRepo.setRootCandidates(10, "/media/shows/Example Show", map[string]string{
		"pending-root-shell": "pending",
		"matched-series":     "matched",
	})

	file := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/shows/Example Show/Season 01/Example.Show.S01E03.mkv",
		ObservedRootPath: "/media/shows/Example Show",
		BaseTitle:        "Example Show",
		BaseYear:         2024,
		BaseType:         "series",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := result.ContentID, "matched-series"; got != want {
		t.Fatalf("ContentID = %q, want %q", got, want)
	}
	if result.IsNew {
		t.Fatal("expected IsNew=false when a matched same-root series already exists")
	}
}

func TestCreateOrFindSkeleton_SeriesSkipsTitleYearDedupAcrossRoots(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "pending-title-year-series",
		Status:    "pending",
		Title:     "Example Show",
		Year:      2024,
		Type:      "series",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	file := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/shows/Example Show HDR/Season 01/Example.Show.S01E01.mkv",
		ObservedRootPath: "/media/shows/Example Show HDR",
		BaseTitle:        "Example Show",
		BaseYear:         2024,
		BaseType:         "series",
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContentID == "pending-title-year-series" {
		t.Fatal("expected series title/year fallback dedup to be disabled across roots")
	}
	if !result.IsNew {
		t.Fatal("expected IsNew=true when only title/year series match exists")
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

func TestCreateOrFindSkeleton_PrefersScannedGroupHints(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID:          10,
		GroupKeyVersion:        1,
		ContentGroupKey:        "v1|movie|bagman|2024",
		State:                  "resolved",
		InferredType:           "movie",
		BaseTitle:              "Bagman",
		BaseYear:               2024,
		TypeConfidence:         "high",
		SampleObservedRootPath: "/media/movies/Wrapper Root",
	})

	file := &models.MediaFile{
		ID:               9,
		MediaFolderID:    10,
		FilePath:         "/media/movies/Wrapper Root/Bagman.2024.1080p.mkv",
		ObservedRootPath: "/media/movies/Wrapper Root",
		ContentGroupKey:  "v1|movie|bagman|2024",
		GroupKeyVersion:  1,
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ObservedRootPath != "/media/movies/Wrapper Root" {
		t.Fatalf("ObservedRootPath = %q", result.ObservedRootPath)
	}
	if result.Title != "Bagman" || result.Year != 2024 {
		t.Fatalf("scanned group hints not applied, got title=%q year=%d", result.Title, result.Year)
	}
}

func TestCreateOrFindSkeleton_AmbiguousScannedGroupCreatesAmbiguousItem(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID:          10,
		GroupKeyVersion:        1,
		ContentGroupKey:        "v1|movie|unknown root|0000",
		State:                  "ambiguous",
		InferredType:           "movie",
		TypeConfidence:         "low",
		SampleObservedRootPath: "/media/mixed/Unknown Root",
	})

	file := &models.MediaFile{
		ID:               10,
		MediaFolderID:    10,
		FilePath:         "/media/mixed/Unknown Root/mystery.mkv",
		ObservedRootPath: "/media/mixed/Unknown Root",
		ContentGroupKey:  "v1|movie|unknown root|0000",
		GroupKeyVersion:  1,
	}

	result, err := h.service.createOrFindSkeleton(ctx, file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ItemStatus != "ambiguous" {
		t.Fatalf("ItemStatus = %q, want ambiguous", result.ItemStatus)
	}

	item, err := h.itemRepo.GetByID(ctx, result.ContentID)
	if err != nil {
		t.Fatalf("item not found: %v", err)
	}
	if item.Status != "ambiguous" {
		t.Fatalf("item.Status = %q, want ambiguous", item.Status)
	}
}
