package storagetransition

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type memorySettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *memorySettings) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}
func (s *memorySettings) GetAll(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.values), nil
}
func (s *memorySettings) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}
func (s *memorySettings) SetIfAbsent(_ context.Context, key, value string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values[key] != "" {
		return false, nil
	}
	s.values[key] = value
	return true, nil
}
func (s *memorySettings) UpdateAtomic(_ context.Context, update func(map[string]string) (map[string]string, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	writes, err := update(clone(s.values))
	if err != nil {
		return err
	}
	for key, value := range writes {
		s.values[key] = value
	}
	return nil
}

type memoryStore struct {
	// mu guards objects and the counters: a page's objects copy concurrently.
	mu         sync.Mutex
	identity   string
	objects    map[string][]byte
	lists      int
	gets       int
	stats      int
	puts       int
	streams    int
	probes     int
	failGet    bool
	failGetKey string
	probeErr   error
	probe      func(context.Context) error
}

// partialDeleteStore models S3 DeleteObjects returning a short success count
// after one or more objects fail deletion without a request-level error.
type partialDeleteStore struct {
	*memoryStore
	failDeleteKey  string
	failAllDeletes bool
}

func (s *partialDeleteStore) Delete(_ context.Context, keys []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for _, key := range keys {
		if s.failAllDeletes || key == s.failDeleteKey {
			continue
		}
		delete(s.objects, key)
		deleted++
	}
	return deleted, nil
}

type memoryJobs struct{}

func (memoryJobs) GetActiveByType(context.Context, string) (*models.AdminJob, error) {
	return nil, adminjob.ErrJobNotFound
}

type activeMemoryJobs struct {
	job         *models.AdminJob
	createCalls int
}

func (j *activeMemoryJobs) GetActiveByType(context.Context, string) (*models.AdminJob, error) {
	return j.job, nil
}

func (j *activeMemoryJobs) Create(context.Context, adminjob.CreateJobInput) (*models.AdminJob, error) {
	j.createCalls++
	return nil, errors.New("unexpected create")
}

type applyingErrorSettings struct {
	*memorySettings
	failCommit bool
}

type applyingFlakyReadSettings struct {
	*memorySettings
	remainingFailures int
	committed         bool
}

type transientGetSettings struct {
	*memorySettings
	remainingFailures int
}

type permanentGetErrorSettings struct {
	*memorySettings
	err error
}

type nthGetErrorSettings struct {
	*memorySettings
	failAt int
	gets   int
}

func (s *transientGetSettings) Get(ctx context.Context, key string) (string, error) {
	if key == StagedTargetSettingKey && s.remainingFailures > 0 {
		s.remainingFailures--
		return "", errors.New("injected staged-state read outage")
	}
	return s.memorySettings.Get(ctx, key)
}

func (s *permanentGetErrorSettings) Get(context.Context, string) (string, error) {
	return "", s.err
}

func (s *nthGetErrorSettings) Get(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	s.gets++
	fail := key == StagedTargetSettingKey && s.gets == s.failAt
	s.mu.Unlock()
	if fail {
		return "", errors.New("injected staged-state read outage")
	}
	return s.memorySettings.Get(ctx, key)
}

func (s *applyingFlakyReadSettings) Get(ctx context.Context, key string) (string, error) {
	if s.committed && s.remainingFailures > 0 {
		s.remainingFailures--
		return "", errors.New("injected verification outage")
	}
	return s.memorySettings.Get(ctx, key)
}

func (s *applyingFlakyReadSettings) UpdateAtomic(ctx context.Context, update func(map[string]string) (map[string]string, error)) error {
	err := s.memorySettings.UpdateAtomic(ctx, update)
	if err != nil {
		return err
	}
	s.mu.Lock()
	committed := s.values[blobstore.IdentitySettingKey] != ""
	s.mu.Unlock()
	if committed && !s.committed {
		s.committed = true
		return errors.New("injected ambiguous commit error")
	}
	return nil
}

func (s *applyingErrorSettings) UpdateAtomic(ctx context.Context, update func(map[string]string) (map[string]string, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	writes, err := update(clone(s.values))
	if err != nil {
		return err
	}
	for key, value := range writes {
		s.values[key] = value
	}
	if s.failCommit && writes[blobstore.IdentitySettingKey] != "" {
		s.failCommit = false
		return errors.New("injected ambiguous commit error")
	}
	return nil
}

type rejectingCommitSettings struct{ *memorySettings }

func (s *rejectingCommitSettings) UpdateAtomic(_ context.Context, update func(map[string]string) (map[string]string, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	writes, err := update(clone(s.values))
	if err != nil {
		return err
	}
	if writes[blobstore.IdentitySettingKey] != "" {
		return errors.New("injected commit failure")
	}
	for key, value := range writes {
		s.values[key] = value
	}
	return nil
}

type fencedMemoryStore struct {
	*memoryStore
	onFence  func()
	fenced   bool
	released bool
}

func (s *fencedMemoryStore) BeginMutationFence(context.Context) (func(), error) {
	if s.onFence != nil {
		s.onFence()
	}
	s.fenced = true
	return func() {
		s.fenced = false
		s.released = true
	}, nil
}

type aliasStore struct {
	*memoryStore
	backing   map[string][]byte
	prefix    string
	statErr   error
	deleteErr error
}

type unreliableListingStore struct{ *memoryStore }

// vanishAfterListStore models a source object deleted after its list entry was
// returned but before the transition reads it.
type vanishAfterListStore struct {
	*memoryStore
	vanishKey string
}

func (s *vanishAfterListStore) List(ctx context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error) {
	objects, next, err := s.memoryStore.List(ctx, prefix, cursor, limit)
	if err == nil {
		s.mu.Lock()
		delete(s.objects, s.vanishKey)
		s.mu.Unlock()
	}
	return objects, next, err
}

type failingTargetStore struct {
	*memoryStore
	putErr      error
	getErr      error
	shortDelete bool
}

func (s *failingTargetStore) Put(ctx context.Context, key string, data []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	return s.memoryStore.Put(ctx, key, data)
}

func (s *failingTargetStore) Get(ctx context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	if s.getErr != nil {
		return nil, blobstore.ObjectInfo{}, s.getErr
	}
	return s.memoryStore.Get(ctx, key)
}

func (s *failingTargetStore) Delete(ctx context.Context, keys []string) (int, error) {
	deleted, err := s.memoryStore.Delete(ctx, keys)
	if s.shortDelete {
		return 0, err
	}
	return deleted, err
}

type localInPlaceRewriteStore struct {
	*memoryStore
	onFence func()
}

func (s *localInPlaceRewriteStore) BeginMutationFence(context.Context) (func(), error) {
	if s.onFence != nil {
		s.onFence()
	}
	return func() {}, nil
}

func (s *localInPlaceRewriteStore) List(ctx context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error) {
	objects, next, err := s.memoryStore.List(ctx, prefix, cursor, limit)
	for i := range objects {
		objects[i].ETag = `"1-constant"`
		objects[i].ModTime = time.Unix(1, 0)
	}
	return objects, next, err
}

func (s *unreliableListingStore) List(ctx context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error) {
	objects, next, err := s.memoryStore.List(ctx, prefix, cursor, limit)
	for i := range objects {
		objects[i].ETag = ""
		objects[i].ModTime = time.Time{}
	}
	return objects, next, err
}

func (s *aliasStore) physical(key string) string {
	if s.prefix == "" {
		return key
	}
	return strings.Trim(s.prefix, "/") + "/" + key
}

func (s *aliasStore) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backing[s.physical(key)] = append([]byte(nil), data...)
	return nil
}

func (s *aliasStore) Stat(_ context.Context, key string) (blobstore.ObjectInfo, error) {
	if s.statErr != nil {
		return blobstore.ObjectInfo{}, s.statErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.backing[s.physical(key)]
	if !ok {
		return blobstore.ObjectInfo{}, blobstore.ErrNotFound
	}
	return blobstore.ObjectInfo{Key: key, Size: int64(len(data))}, nil
}

func (s *aliasStore) Delete(_ context.Context, keys []string) (int, error) {
	if s.deleteErr != nil {
		return 0, s.deleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for _, key := range keys {
		physical := s.physical(key)
		if _, ok := s.backing[physical]; ok {
			delete(s.backing, physical)
			deleted++
		}
	}
	return deleted, nil
}

func (memoryJobs) Create(_ context.Context, input adminjob.CreateJobInput) (*models.AdminJob, error) {
	return &models.AdminJob{ID: "job", JobType: input.JobType}, nil
}

func (s *memoryStore) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	s.objects[key] = append([]byte(nil), data...)
	return nil
}
func (s *memoryStore) PutStream(ctx context.Context, key string, r io.Reader, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.streams++
	s.mu.Unlock()
	return s.Put(ctx, key, data)
}
func (s *memoryStore) Get(_ context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if s.failGet || s.failGetKey == key {
		return nil, blobstore.ObjectInfo{}, errors.New("injected read failure")
	}
	data, ok := s.objects[key]
	if !ok {
		return nil, blobstore.ObjectInfo{}, blobstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), memoryObjectInfo(key, data), nil
}
func (s *memoryStore) Stat(_ context.Context, key string) (blobstore.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats++
	data, ok := s.objects[key]
	if !ok {
		return blobstore.ObjectInfo{}, blobstore.ErrNotFound
	}
	return memoryObjectInfo(key, data), nil
}
func (s *memoryStore) Delete(_ context.Context, keys []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.objects, key)
	}
	return len(keys), nil
}
func (s *memoryStore) DeletePrefix(context.Context, string) (int, error) { return 0, nil }
func (s *memoryStore) List(_ context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists++
	var keys []string
	for key := range s.objects {
		if (prefix == "" || strings.HasPrefix(key, prefix+"/")) && key > cursor {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]blobstore.ObjectInfo, len(keys))
	for i, key := range keys {
		out[i] = memoryObjectInfo(key, s.objects[key])
	}
	next := ""
	if len(keys) > 0 {
		last := keys[len(keys)-1]
		for key := range s.objects {
			if key > last && (prefix == "" || strings.HasPrefix(key, prefix+"/")) {
				next = last
				break
			}
		}
	}
	return out, next, nil
}
func (s *memoryStore) Probe(ctx context.Context) error {
	s.probes++
	if s.probe != nil {
		return s.probe(ctx)
	}
	return s.probeErr
}
func (s *memoryStore) Identity() string { return s.identity }

func memoryObjectInfo(key string, data []byte) blobstore.ObjectInfo {
	digest := sha256.Sum256(data)
	return blobstore.ObjectInfo{Key: key, Size: int64(len(data)), ModTime: time.Unix(1, 0), ETag: fmt.Sprintf("%x", digest)}
}

func stagedLocal(t *testing.T, dir string) *memorySettings {
	t.Helper()
	raw, err := json.Marshal(stagedTarget{Values: map[string]string{"artwork.storage_backend": "local", "artwork.local_path": dir}})
	if err != nil {
		t.Fatal(err)
	}
	return &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
}

// sequential copies one object at a time and reports every object, for tests
// that act on a specific progress count or depend on copy order.
func sequential(service *Service) *Service {
	service.copyWorkers = 1
	service.progressInterval = 0
	return service
}

func testService(settings *memorySettings, source *memoryStore) *Service {
	service := New(nil, settings, nil, source, nil)
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.ArtworkReconcileStats{Mode: metadata.ArtworkReconcileModeBulkReset, Requeued: 3}, nil
	}
	return service
}

func TestStartFreshNeverReadsOldStorage(t *testing.T) {
	source := &memoryStore{identity: "s3|old|bucket|", objects: map[string][]byte{"tmdb/poster.jpg": []byte("large cache")}}
	settings := stagedLocal(t, t.TempDir())
	result, err := testService(settings, source).ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyFresh}, func(adminjob.StorageTransitionProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	if source.lists != 0 || source.gets != 0 {
		t.Fatalf("fresh transition touched old storage: lists=%d gets=%d", source.lists, source.gets)
	}
	got := result.(Result)
	if got.CopiedObjects != 0 || got.ArtworkReconcile.Requeued != 0 || !got.OldStorageRetained {
		t.Fatalf("result = %#v", got)
	}
	var staged stagedTarget
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &staged); err != nil {
		t.Fatal(err)
	}
	if staged.Phase != transitionPhaseRestartPending || !strings.HasPrefix(settings.values[blobstore.IdentitySettingKey], "local|") {
		t.Fatalf("settings not committed: %#v", settings.values)
	}
	target, err := blobstore.NewFilesystem(targetDirFromIdentity(t, staged.TargetIdentity))
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(nil, settings, nil, target, nil)
	reconciled := false
	restarted.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		reconciled = true
		return metadata.ArtworkReconcileStats{Requeued: 3}, nil
	}
	if err := restarted.FinalizeCommitted(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reconciled {
		t.Fatal("FinalizeCommitted ran the artwork reconcile before the server listener started")
	}
	if err := restarted.RunPostRestartWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reconciled {
		t.Fatal("public artwork was not reconciled by post-restart work")
	}
}

func TestPostRestartReconcileResumesAndClearsRecoveryOnlyAfterCompletion(t *testing.T) {
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	stage := stagedTarget{ID: "resume-reconcile", Policy: PolicyFresh, SourceIdentity: "s3|old|public|", TargetIdentity: target.Identity(), PublicReconcile: true, Phase: transitionPhaseRestartPending, Values: map[string]string{settingArtworkBackend: blobstore.BackendLocal}}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, nil, target, nil)
	service.progressInterval = 0
	retryObserved := false
	service.postRestartBackoff = func(context.Context, int) error {
		retryObserved = true
		health, healthErr := service.SourceHealth(t.Context(), false)
		if healthErr != nil {
			t.Fatal(healthErr)
		}
		if !health.RecoveryPending || health.RecoveryState != recoveryStateWaitingRetry || !strings.Contains(health.RecoveryError, "injected transient") || health.RecoveryProgress != 25 {
			t.Fatalf("retry health = %#v", health)
		}
		return nil
	}
	calls := 0
	service.reconcileResumable = func(_ context.Context, _ blobstore.Store, checkpoint *metadata.ArtworkReconcileCheckpoint, save func(metadata.ArtworkReconcileCheckpoint) error, report func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		calls++
		if calls == 1 {
			if checkpoint != nil {
				t.Fatal("first reconcile unexpectedly resumed a checkpoint")
			}
			if err := save(metadata.ArtworkReconcileCheckpoint{Done: 25}); err != nil {
				return metadata.ArtworkReconcileStats{}, err
			}
			report(25, "Verified first page")
			return metadata.ArtworkReconcileStats{}, errors.New("injected transient reconcile error")
		}
		if checkpoint == nil || checkpoint.Done != 25 {
			t.Fatalf("resume checkpoint = %#v, want Done=25", checkpoint)
		}
		return metadata.ArtworkReconcileStats{Verified: 10}, nil
	}
	if err := service.FinalizeCommitted(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("FinalizeCommitted invoked the reconciler")
	}
	if err := service.RunPostRestartWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !retryObserved || calls != 2 {
		t.Fatalf("retry observed=%t calls=%d, want true and 2", retryObserved, calls)
	}
	if settings.values[StagedTargetSettingKey] != "" || settings.values[config.ArtworkStorageReconcileCheckpointKey] != "" {
		t.Fatalf("completed reconcile retained recovery state: %#v", settings.values)
	}
}

func TestStartReportsPendingPostRestartReconciliation(t *testing.T) {
	sourceDir := t.TempDir()
	stage := stagedTarget{ID: "pending", Policy: PolicyFresh, SourceIdentity: "local|" + sourceDir, TargetIdentity: "local|" + sourceDir, PublicReconcile: true, Phase: transitionPhaseRestartPending, Values: map[string]string{settingArtworkBackend: blobstore.BackendLocal, settingArtworkLocalPath: sourceDir}}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw), settingArtworkBackend: blobstore.BackendLocal, settingArtworkLocalPath: sourceDir}}
	service := New(nil, settings, memoryJobs{}, &memoryStore{identity: "local|" + sourceDir, objects: map[string][]byte{}}, nil)
	_, _, err = service.Start(t.Context(), 1, StartRequest{Policy: PolicyFresh, Values: map[string]string{settingArtworkBackend: blobstore.BackendLocal, settingArtworkLocalPath: t.TempDir()}})
	if err == nil || !strings.Contains(err.Error(), "reconciliation pending") {
		t.Fatalf("Start error = %v, want pending reconciliation guidance", err)
	}
}

func TestUnreadableCommittedStageDoesNotPreventBootAndBlocksNewTransitions(t *testing.T) {
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	stage := stagedTarget{ID: "unreadable", TargetIdentity: target.Identity(), PublicReconcile: true, Phase: transitionPhaseRestartPending}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("injected decrypt failure")
	settings := &permanentGetErrorSettings{
		memorySettings: &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}},
		err:            readErr,
	}
	service := New(nil, settings, memoryJobs{}, target, nil)

	if err := service.FinalizeCommitted(t.Context()); err != nil {
		t.Fatalf("FinalizeCommitted returned a fatal error: %v", err)
	}
	health, err := service.SourceHealth(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !health.RecoveryPending || health.RecoveryState != recoveryStateBlocked || !strings.Contains(health.RecoveryError, "read or decrypted") {
		t.Fatalf("blocked health = %#v", health)
	}
	_, _, err = service.Start(t.Context(), 1, StartRequest{Policy: PolicyFresh})
	if !errors.Is(err, errCommittedStageUnreadable) {
		t.Fatalf("Start error = %v, want unreadable committed stage", err)
	}
	if settings.values[StagedTargetSettingKey] != string(raw) {
		t.Fatal("unreadable staged setting was rewritten")
	}
}

func TestInvalidCommittedStageDoesNotPreventBootAndBlocksNewTransitions(t *testing.T) {
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: "{not-json"}}
	service := New(nil, settings, memoryJobs{}, target, nil)

	if err := service.FinalizeCommitted(t.Context()); err != nil {
		t.Fatalf("FinalizeCommitted returned a fatal error: %v", err)
	}
	health, err := service.SourceHealth(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !health.RecoveryPending || health.RecoveryState != recoveryStateBlocked || !strings.Contains(health.RecoveryError, "invalid") {
		t.Fatalf("blocked health = %#v", health)
	}
	_, _, err = service.Start(t.Context(), 1, StartRequest{Policy: PolicyFresh})
	if !errors.Is(err, errCommittedStageInvalid) {
		t.Fatalf("Start error = %v, want invalid committed stage", err)
	}
	if settings.values[StagedTargetSettingKey] != "{not-json" {
		t.Fatal("invalid staged setting was rewritten")
	}
}

func TestFinalizeCommittedKeepsTargetIdentityMismatchFatal(t *testing.T) {
	stage := stagedTarget{ID: "mismatch", TargetIdentity: "local|expected", Phase: transitionPhaseRestartPending}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, nil, &memoryStore{identity: "local|actual", objects: map[string][]byte{}}, nil)
	if err := service.FinalizeCommitted(t.Context()); !errors.Is(err, errCommittedTargetMismatch) {
		t.Fatalf("FinalizeCommitted error = %v, want target mismatch", err)
	}
}

func TestPostRestartBrandingReconcileClearsDanglingReference(t *testing.T) {
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	stage := stagedTarget{ID: "branding", Policy: PolicyFresh, SourceIdentity: "s3|old|public|", TargetIdentity: target.Identity(), BrandingReconcile: true, Phase: transitionPhaseRestartPending, Values: map[string]string{settingArtworkBackend: blobstore.BackendLocal}}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw), "branding.logo_asset": "missing.webp"}}
	service := New(nil, settings, nil, target, nil)
	service.postRestartBackoff = func(context.Context, int) error { return nil }
	service.SetBrandingReconciler(func(context.Context) (int, int, error) {
		if _, err := target.Stat(t.Context(), "branding/missing.webp"); !errors.Is(err, blobstore.ErrNotFound) {
			return 1, 0, err
		}
		_ = settings.Set(t.Context(), "branding.logo_asset", "")
		return 1, 1, nil
	})
	if err := service.RunPostRestartWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if settings.values["branding.logo_asset"] != "" {
		t.Fatal("dangling branding reference survived post-restart reconciliation")
	}
}

func TestBrandingFailureDoesNotRepeatCompletedCatalogReconcile(t *testing.T) {
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	stage := stagedTarget{ID: "branding-retry", Policy: PolicyFresh, SourceIdentity: "s3|old|public|", TargetIdentity: target.Identity(), PublicReconcile: true, BrandingReconcile: true, Phase: transitionPhaseRestartPending, Values: map[string]string{settingArtworkBackend: blobstore.BackendLocal}}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, nil, target, nil)
	catalogRuns := 0
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		catalogRuns++
		return metadata.ArtworkReconcileStats{Verified: 1}, nil
	}
	brandingRuns := 0
	service.SetBrandingReconciler(func(context.Context) (int, int, error) {
		brandingRuns++
		if brandingRuns == 1 {
			return 0, 0, errors.New("injected branding outage")
		}
		return 0, 0, nil
	})
	if err := service.RunPostRestartWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if catalogRuns != 1 || brandingRuns != 2 {
		t.Fatalf("catalog runs=%d branding runs=%d, want 1 and 2", catalogRuns, brandingRuns)
	}
}

func TestPostRestartIdentityMismatchDoesNotRetry(t *testing.T) {
	stage := stagedTarget{ID: "mismatch", TargetIdentity: "local|expected", PublicReconcile: true, Phase: transitionPhaseRestartPending}
	raw, _ := json.Marshal(stage)
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, nil, &memoryStore{identity: "local|other", objects: map[string][]byte{}}, nil)
	backoffs := 0
	service.postRestartBackoff = func(context.Context, int) error { backoffs++; return nil }
	err := service.RunPostRestartWork(t.Context())
	if !errors.Is(err, errCommittedTargetMismatch) || backoffs != 0 {
		t.Fatalf("RunPostRestartWork error=%v backoffs=%d", err, backoffs)
	}
	health, err := service.SourceHealth(t.Context(), false)
	if err != nil || health.RecoveryState != recoveryStateBlocked {
		t.Fatalf("blocked health=%#v err=%v", health, err)
	}
}

func TestPostRestartRetryStopsOnContextCancellation(t *testing.T) {
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	stage := stagedTarget{ID: "cancel-retry", TargetIdentity: target.Identity(), PublicReconcile: true, Phase: transitionPhaseRestartPending}
	raw, _ := json.Marshal(stage)
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, nil, target, nil)
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.ArtworkReconcileStats{}, errors.New("temporary outage")
	}
	backoffStarted := make(chan struct{})
	service.postRestartBackoff = func(ctx context.Context, _ int) error {
		close(backoffStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.RunPostRestartWork(ctx) }()
	<-backoffStarted
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunPostRestartWork cancellation = %v", err)
	}
}

func TestPostRestartRetriesTransientPrecheckReadAndClearsRecovery(t *testing.T) {
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	stage := stagedTarget{ID: "stage-read-retry", TargetIdentity: target.Identity(), PublicReconcile: true, Phase: transitionPhaseRestartPending}
	raw, _ := json.Marshal(stage)
	settings := &transientGetSettings{memorySettings: &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}, remainingFailures: 1}
	service := New(nil, settings, nil, target, nil)
	backoffs := 0
	service.postRestartBackoff = func(context.Context, int) error {
		backoffs++
		health, err := service.SourceHealth(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		if health.RecoveryState != recoveryStatePending || health.RecoveryError != "" {
			t.Fatalf("retry health = %#v", health)
		}
		return nil
	}
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.ArtworkReconcileStats{Verified: 1}, nil
	}
	if err := service.RunPostRestartWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if backoffs != 1 || settings.values[StagedTargetSettingKey] != "" {
		t.Fatalf("backoffs=%d staged=%q", backoffs, settings.values[StagedTargetSettingKey])
	}
}

func TestPostRestartFinalizesStageAfterBootReadFailure(t *testing.T) {
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	stage := stagedTarget{ID: "boot-read-retry", TargetIdentity: target.Identity(), Phase: transitionPhaseRestartPending}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &transientGetSettings{memorySettings: &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}, remainingFailures: 1}
	service := New(nil, settings, nil, target, nil)
	if err := service.FinalizeCommitted(t.Context()); err != nil {
		t.Fatal(err)
	}
	if settings.values[StagedTargetSettingKey] == "" {
		t.Fatal("boot read failure unexpectedly cleared the committed stage")
	}
	if err := service.RunPostRestartWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if settings.values[StagedTargetSettingKey] != "" {
		t.Fatal("post-restart recovery left the committed stage behind")
	}
	if err := service.RunPostRestartWork(t.Context()); err != nil {
		t.Fatalf("repeated recovery failed: %v", err)
	}
}

func TestPostRestartBlocksUndecodableStageWithoutRetry(t *testing.T) {
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: "{not-json"}}
	service := New(nil, settings, nil, &memoryStore{identity: "local|target", objects: map[string][]byte{}}, nil)
	backoffs := 0
	service.postRestartBackoff = func(context.Context, int) error { backoffs++; return nil }
	err := service.RunPostRestartWork(t.Context())
	if !errors.Is(err, errCommittedStageInvalid) || backoffs != 0 {
		t.Fatalf("error=%v backoffs=%d", err, backoffs)
	}
	health, healthErr := service.SourceHealth(t.Context(), false)
	if healthErr != nil || !health.RecoveryPending || health.RecoveryState != recoveryStateBlocked || health.RecoveryError == "" {
		t.Fatalf("blocked health=%#v err=%v", health, healthErr)
	}
}

func TestSourceHealthWithoutProbeDoesNotTouchStores(t *testing.T) {
	settings := &memorySettings{values: map[string]string{settingArtworkBackend: blobstore.BackendS3, settingPublicBucket: "public", settingPrivateBucket: "private"}}
	public := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{}}
	private := &memoryStore{identity: "s3|source|private|", objects: map[string][]byte{}}
	service := New(nil, settings, nil, public, private)
	health, err := service.SourceHealth(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if health.ReachabilityProbed || public.probes != 0 || private.probes != 0 || !health.PublicConfigured || !health.PrivateConfigured {
		t.Fatalf("non-probing health=%#v public probes=%d private probes=%d", health, public.probes, private.probes)
	}
}

func TestSourceHealthProbeIsBounded(t *testing.T) {
	settings := &memorySettings{values: map[string]string{settingArtworkBackend: blobstore.BackendS3, settingPublicBucket: "public"}}
	probeCanceled := make(chan struct{})
	public := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{}, probe: func(ctx context.Context) error {
		<-ctx.Done()
		close(probeCanceled)
		return ctx.Err()
	}}
	service := New(nil, settings, nil, public, nil)
	service.probeTimeout = time.Millisecond
	health, err := service.SourceHealth(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	<-probeCanceled
	if !health.ReachabilityProbed || health.PublicReachable || health.Reachable {
		t.Fatalf("timed-out health=%#v", health)
	}
}

func targetDirFromIdentity(t *testing.T, identity string) string {
	t.Helper()
	if !strings.HasPrefix(identity, blobstore.BackendLocal+"|") {
		t.Fatalf("identity %q is not local", identity)
	}
	return strings.TrimPrefix(identity, blobstore.BackendLocal+"|")
}

func TestPreserveUploadsSkipsProviderCache(t *testing.T) {
	source := &memoryStore{identity: "s3|old|bucket|", objects: map[string][]byte{
		"branding/mark/a.webp":                 []byte("brand"),
		"user-collection-images/c/poster.webp": []byte("custom"),
		"tmdb/posters/large.webp":              []byte("provider"),
	}}
	targetDir := t.TempDir()
	settings := stagedLocal(t, targetDir)
	result, err := testService(settings, source).ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyPreserveUploads}, func(adminjob.StorageTransitionProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	got := result.(Result)
	if got.CopiedObjects != 2 {
		t.Fatalf("copied %d objects, want 2", got.CopiedObjects)
	}
	target, err := blobstore.NewFilesystem(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Stat(t.Context(), "branding/mark/a.webp"); err != nil {
		t.Fatalf("branding not copied: %v", err)
	}
	if _, err := target.Stat(t.Context(), "tmdb/posters/large.webp"); err == nil {
		t.Fatal("provider cache was copied")
	}
}

func TestMigrateAllCopiesProviderCache(t *testing.T) {
	source := &memoryStore{identity: "s3|old|bucket|", objects: map[string][]byte{
		"branding/mark/a.webp":                 []byte("brand"),
		"user-collection-images/c/poster.webp": []byte("custom"),
		"tmdb/posters/large.webp":              []byte("provider"),
	}}
	targetDir := t.TempDir()
	settings := stagedLocal(t, targetDir)
	result, err := testService(settings, source).ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	got := result.(Result)
	if got.CopiedObjects != 3 {
		t.Fatalf("copied %d objects, want 3", got.CopiedObjects)
	}
	var committed stagedTarget
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &committed); err != nil {
		t.Fatal(err)
	}
	if committed.PublicReconcile || !committed.BrandingReconcile {
		t.Fatalf("migrate-all post-restart work = %#v, want branding-only", committed)
	}
	target, err := blobstore.NewFilesystem(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"branding/mark/a.webp", "user-collection-images/c/poster.webp", "tmdb/posters/large.webp"} {
		if _, err := target.Stat(t.Context(), key); err != nil {
			t.Fatalf("%s not copied: %v", key, err)
		}
	}
}

func TestMigrateAllKeepsAvatarsOutOfPublicS3Target(t *testing.T) {
	source := &memoryStore{identity: "local|source", objects: map[string][]byte{
		"branding/mark/a.webp":          []byte("brand"),
		"profile-avatars/u/avatar.webp": []byte("private-avatar"),
	}}
	public := &memoryStore{identity: "s3|public", objects: map[string][]byte{}}
	private := &memoryStore{identity: "s3|private", objects: map[string][]byte{}}
	service := testService(&memorySettings{values: map[string]string{}}, source)
	if _, _, _, err := service.copyPrefix(t.Context(), "transition-1", "public:", source, public, "", func(int, int, string) {}, 0, "profile-avatars"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.copyPrefix(t.Context(), "transition-1", "avatars:profile-avatars", source, private, "profile-avatars", func(int, int, string) {}, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := public.objects["profile-avatars/u/avatar.webp"]; ok {
		t.Fatal("private avatar was copied to public target")
	}
	if string(private.objects["profile-avatars/u/avatar.webp"]) != "private-avatar" {
		t.Fatal("avatar was not copied to private target")
	}
}

func TestMigrateAllNeverCopiesLegacyAvatarsToPublicOnlyS3(t *testing.T) {
	source := &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{
		"tmdb/poster.webp":              []byte("poster"),
		"profile-avatars/u/avatar.webp": []byte("legacy-private-avatar"),
	}}
	target := &memoryStore{identity: "s3|new|public|", objects: map[string][]byte{}}
	stage := stagedTarget{
		ID:             "public-only",
		Policy:         PolicyMigrateAll,
		SourceIdentity: source.Identity(),
		Phase:          transitionPhaseStaged,
		Values:         map[string]string{"artwork.storage_backend": blobstore.BackendS3, "s3.public_bucket": "public"},
	}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return nil }
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.ArtworkReconcileStats{}, nil
	}

	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if _, ok := target.objects["profile-avatars/u/avatar.webp"]; ok {
		t.Fatal("legacy private avatar was copied to public-only S3")
	}
	if string(target.objects["tmdb/poster.webp"]) != "poster" {
		t.Fatal("public artwork was not migrated")
	}
}

func TestCopyMigrationRejectsOverlappingS3Namespaces(t *testing.T) {
	tests := []struct {
		name   string
		source string
		target string
	}{
		{name: "target nested under source", source: "s3|https://s3.example.test|bucket|", target: "s3|https://s3.example.test|bucket|nested"},
		{name: "source nested under target", source: "s3|https://s3.example.test|bucket|nested", target: "s3|https://s3.example.test|bucket|"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &memoryStore{identity: tt.source, objects: map[string][]byte{"tmdb/a.webp": []byte("poster")}}
			target := &memoryStore{identity: tt.target, objects: map[string][]byte{}}
			stage := stagedTarget{ID: "overlap", Policy: PolicyMigrateAll, SourceIdentity: source.Identity(), Phase: transitionPhaseStaged, Values: map[string]string{"artwork.storage_backend": blobstore.BackendS3, "s3.public_bucket": "bucket"}}
			raw, err := json.Marshal(stage)
			if err != nil {
				t.Fatal(err)
			}
			settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
			service := New(nil, settings, nil, source, nil)
			service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
			service.openPrivate = func(map[string]string) blobstore.Store { return nil }

			_, err = service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {})
			if err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("ExecuteStorageTransition() error = %v, want overlap rejection", err)
			}
			if source.lists != 0 || len(target.objects) != 0 {
				t.Fatalf("overlapping migration touched storage: lists=%d target=%#v", source.lists, target.objects)
			}
		})
	}
}

func TestExecuteProbesTargetBeforeNamespaceSentinel(t *testing.T) {
	source := &memoryStore{identity: "s3|https://old.example|bucket|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}
	target := &memoryStore{identity: "s3|https://new.example|bucket|nested", objects: map[string][]byte{}, probeErr: errors.New("offline")}
	stage := stagedTarget{ID: "probe-order", Policy: PolicyMigrateAll, SourceIdentity: source.Identity(), Phase: transitionPhaseStaged, Values: map[string]string{settingArtworkBackend: blobstore.BackendS3, settingPublicBucket: "bucket"}}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return nil }
	_, err = service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {})
	if err == nil || !strings.Contains(err.Error(), "target storage is unavailable") {
		t.Fatalf("target probe error = %v", err)
	}
	if target.puts != 0 {
		t.Fatalf("namespace sentinel was written before target reachability probe: puts=%d", target.puts)
	}
}

func TestFreshTransitionValidatesChangedTargetOperations(t *testing.T) {
	for _, scope := range []string{"assets", "private"} {
		for _, failure := range []string{"write", "read", "delete"} {
			t.Run(scope+"/"+failure, func(t *testing.T) {
				source := &memoryStore{identity: "local|source", objects: map[string][]byte{}}
				target := &failingTargetStore{memoryStore: &memoryStore{identity: "s3|target|bucket|", objects: map[string][]byte{}}}
				switch failure {
				case "write":
					target.putErr = errors.New("put denied")
				case "read":
					target.getErr = errors.New("get denied")
				case "delete":
					target.shortDelete = true
				}
				stage := stagedTarget{ID: "target-operations", Policy: PolicyFresh, SourceIdentity: source.Identity(), Phase: transitionPhaseStaged, Values: map[string]string{settingArtworkBackend: blobstore.BackendLocal}}
				raw, err := json.Marshal(stage)
				if err != nil {
					t.Fatal(err)
				}
				settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
				service := New(nil, settings, nil, source, nil)
				service.openPublic = func(map[string]string) (blobstore.Store, error) {
					if scope == "assets" {
						return target, nil
					}
					return source, nil
				}
				service.openPrivate = func(map[string]string) blobstore.Store {
					if scope == "private" {
						return target
					}
					return nil
				}
				_, err = service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: PolicyFresh}, func(adminjob.StorageTransitionProgress) {})
				if err == nil || !strings.Contains(err.Error(), failure+" target probe") {
					t.Fatalf("fresh transition with %s denied = %v", failure, err)
				}
				if settings.values[blobstore.IdentitySettingKey] != "" || source.gets != 0 || source.lists != 0 || len(target.objects) != 0 {
					t.Fatalf("failed probe committed or read source: identity=%q source reads=%d lists=%d target=%#v", settings.values[blobstore.IdentitySettingKey], source.gets, source.lists, target.objects)
				}
			})
		}
	}
}

func TestTargetProbeReportsCleanupFailureAfterWriteError(t *testing.T) {
	target := &failingTargetStore{
		memoryStore: &memoryStore{identity: "s3|target|bucket|", objects: map[string][]byte{}},
		putErr:      errors.New("write response lost"),
		shortDelete: true,
	}
	err := probeTargetStorage(t.Context(), target)
	if err == nil || !strings.Contains(err.Error(), "write target probe") || !strings.Contains(err.Error(), "delete target probe") {
		t.Fatalf("ambiguous write and failed cleanup = %v", err)
	}
}

func TestFreshTransitionAllowsOverlappingNamespace(t *testing.T) {
	source := &memoryStore{identity: "s3|https://s3.example.test|bucket|", objects: map[string][]byte{"tmdb/a.webp": []byte("poster")}}
	target := &memoryStore{identity: "s3|https://s3.example.test|bucket|fresh", objects: map[string][]byte{}}
	stage := stagedTarget{ID: "fresh-overlap", Policy: PolicyFresh, SourceIdentity: source.Identity(), Phase: transitionPhaseStaged, Values: map[string]string{"artwork.storage_backend": blobstore.BackendS3, "s3.public_bucket": "bucket"}}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return nil }
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.ArtworkReconcileStats{}, nil
	}

	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: PolicyFresh}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatalf("fresh transition rejected overlapping namespace: %v", err)
	}
	if source.lists != 0 {
		t.Fatalf("fresh transition read source storage %d times", source.lists)
	}
}

func TestNamespaceSentinelDetectsEndpointAliasAndCleansUp(t *testing.T) {
	backing := map[string][]byte{}
	source := &aliasStore{
		memoryStore: &memoryStore{identity: "s3|https://storage.internal|bucket|", objects: map[string][]byte{}},
		backing:     backing,
	}
	target := &aliasStore{
		memoryStore: &memoryStore{identity: "s3|https://127.0.0.1:443/|bucket|new", objects: map[string][]byte{}},
		backing:     backing,
		prefix:      "new",
	}
	if err := ensureNamespacesDistinct(t.Context(), source, target, "overlap"); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("alias overlap error = %v", err)
	}
	if len(backing) != 0 {
		t.Fatalf("sentinel leaked after overlap detection: %#v", backing)
	}
}

func TestNamespaceSentinelAllowsDistinctStoresAndCleansUpErrors(t *testing.T) {
	source := &aliasStore{
		memoryStore: &memoryStore{identity: "s3|https://source.example|bucket|", objects: map[string][]byte{}},
		backing:     map[string][]byte{},
	}
	targetBacking := map[string][]byte{}
	target := &aliasStore{
		memoryStore: &memoryStore{identity: "s3|https://target.example|bucket|new", objects: map[string][]byte{}},
		backing:     targetBacking,
		prefix:      "new",
	}
	if err := ensureNamespacesDistinct(t.Context(), source, target, "overlap"); err != nil {
		t.Fatalf("distinct stores rejected: %v", err)
	}
	if len(targetBacking) != 0 {
		t.Fatalf("sentinel leaked after success: %#v", targetBacking)
	}
	source.statErr = errors.New("injected stat failure")
	if err := ensureNamespacesDistinct(t.Context(), source, target, "overlap"); err == nil || !strings.Contains(err.Error(), "injected stat failure") {
		t.Fatalf("probe failure = %v", err)
	}
	if len(targetBacking) != 0 {
		t.Fatalf("sentinel leaked after failure: %#v", targetBacking)
	}
}

func TestNamespaceSentinelReportsMissingDeletePermission(t *testing.T) {
	source := &aliasStore{memoryStore: &memoryStore{identity: "s3|https://source.example|bucket|", objects: map[string][]byte{}}, backing: map[string][]byte{}}
	target := &aliasStore{memoryStore: &memoryStore{identity: "s3|https://target.example|bucket|nested", objects: map[string][]byte{}}, backing: map[string][]byte{}, prefix: "nested", deleteErr: errors.New("access denied")}
	err := ensureNamespacesDistinct(t.Context(), source, target, "overlap")
	if err == nil || !strings.Contains(err.Error(), "delete permission is required") {
		t.Fatalf("sentinel cleanup error = %v", err)
	}
}

func TestNamespaceSentinelRejectsShortDelete(t *testing.T) {
	source := &memoryStore{identity: "s3|https://source.example|bucket|", objects: map[string][]byte{}}
	target := &partialDeleteStore{
		memoryStore:    &memoryStore{identity: "s3|https://target.example|bucket|nested", objects: map[string][]byte{}},
		failAllDeletes: true,
	}
	err := ensureNamespacesDistinct(t.Context(), source, target, "overlap")
	if err == nil || !strings.Contains(err.Error(), "delete permission is required") {
		t.Fatalf("sentinel cleanup error = %v", err)
	}
	if len(target.objects) != 1 {
		t.Fatalf("target sentinel count=%d, want one undeleted sentinel", len(target.objects))
	}
}

func TestTargetPublicPrivateOverlapPolicy(t *testing.T) {
	for _, policy := range []string{PolicyPreserveUploads, PolicyMigrateAll, PolicyFresh} {
		t.Run(policy, func(t *testing.T) {
			settings := &memorySettings{values: map[string]string{
				"artwork.storage_backend": blobstore.BackendLocal,
				"artwork.local_path":      t.TempDir(),
			}}
			service := New(nil, settings, memoryJobs{}, &memoryStore{identity: "local|source", objects: map[string][]byte{}}, nil)
			_, _, err := service.Start(t.Context(), 1, StartRequest{Policy: policy, Values: map[string]string{
				"artwork.storage_backend": blobstore.BackendS3,
				"s3.public_endpoint":      "https://s3.example",
				"s3.public_bucket":        "shared",
				"s3.private_endpoint":     "https://s3.example",
				"s3.private_bucket":       "shared",
			}})
			if err == nil || !strings.Contains(err.Error(), "target public and private") {
				t.Fatalf("policy %s error = %v, want target overlap rejection", policy, err)
			}
		})
	}
}

func TestStartFreshRejectsAmbiguousTargetWithoutProbingSource(t *testing.T) {
	source := &memoryStore{identity: "s3|https://old.example|shared|", objects: map[string][]byte{}, probeErr: errors.New("old storage unavailable")}
	settings := &memorySettings{values: map[string]string{
		settingArtworkBackend: blobstore.BackendS3,
		settingPublicEndpoint: "https://old.example",
		settingPublicBucket:   "shared",
	}}
	service := New(nil, settings, memoryJobs{}, source, nil)
	_, _, err := service.Start(t.Context(), 1, StartRequest{Policy: PolicyFresh, Values: map[string]string{
		settingPrivateEndpoint: "https://new.example",
		settingPrivateBucket:   "shared",
	}})
	if !errors.Is(err, errUnverifiedTargetNamespace) {
		t.Fatalf("ambiguous target error = %v", err)
	}
	if source.probes != 0 || source.stats != 0 || source.gets != 0 || source.lists != 0 || source.puts != 0 {
		t.Fatalf("Start fresh touched source: probes=%d stats=%d gets=%d lists=%d puts=%d", source.probes, source.stats, source.gets, source.lists, source.puts)
	}
}

func TestExecuteFreshRejectsAmbiguousUnchangedTargetWithoutSourceAccess(t *testing.T) {
	for _, unchanged := range []string{"public", "private"} {
		t.Run(unchanged, func(t *testing.T) {
			publicBucket, privateBucket := "old-public", "old-private"
			if unchanged == "public" {
				publicBucket = "shared"
			} else {
				privateBucket = "shared"
			}
			publicSource := &memoryStore{identity: "s3|https://old-public.example|" + publicBucket + "|", objects: map[string][]byte{}}
			privateSource := &memoryStore{identity: "s3|https://old-private.example|" + privateBucket + "|", objects: map[string][]byte{}}
			publicTarget := &memoryStore{identity: "s3|https://new-public.example|shared|", objects: map[string][]byte{}}
			privateTarget := &memoryStore{identity: "s3|https://new-private.example|shared|", objects: map[string][]byte{}}
			if unchanged == "public" {
				publicTarget = publicSource
			} else {
				privateTarget = privateSource
			}
			stage := stagedTarget{ID: "ambiguous-target", Policy: PolicyFresh, SourceIdentity: publicSource.Identity(), Phase: transitionPhaseStaged, Values: map[string]string{settingArtworkBackend: blobstore.BackendS3}}
			raw, err := json.Marshal(stage)
			if err != nil {
				t.Fatal(err)
			}
			settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
			service := New(nil, settings, nil, publicSource, privateSource)
			service.openPublic = func(map[string]string) (blobstore.Store, error) { return publicTarget, nil }
			service.openPrivate = func(map[string]string) blobstore.Store { return privateTarget }
			_, err = service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: PolicyFresh}, func(adminjob.StorageTransitionProgress) {})
			if !errors.Is(err, errUnverifiedTargetNamespace) {
				t.Fatalf("ambiguous target error = %v", err)
			}
			if publicSource.stats != 0 || publicSource.gets != 0 || publicSource.lists != 0 || publicSource.puts != 0 || privateSource.stats != 0 || privateSource.gets != 0 || privateSource.lists != 0 || privateSource.puts != 0 {
				t.Fatal("Start fresh read or wrote an active source before rejecting ambiguous targets")
			}
			if settings.values[blobstore.IdentitySettingKey] != "" {
				t.Fatal("ambiguous target was committed")
			}
		})
	}
}

func TestLegacySharedSourceSeparatesPublicAndPrivateData(t *testing.T) {
	shared := &memoryStore{identity: "s3|endpoint|legacy|", objects: map[string][]byte{
		"tmdb/poster.webp":              []byte("public"),
		"profile-avatars/u/avatar.webp": []byte("avatar"),
		"diagnostics/report.zip":        []byte("diagnostic"),
	}}
	publicTarget := &memoryStore{identity: "s3|endpoint|public-new|", objects: map[string][]byte{}}
	privateTarget := &memoryStore{identity: "s3|endpoint|private-new|", objects: map[string][]byte{}}
	stage := stagedTarget{ID: "legacy", Policy: PolicyMigrateAll, SourceIdentity: shared.Identity(), SourcePrivateBucket: "legacy", TargetPrivateBucket: "private-new", Values: map[string]string{"artwork.storage_backend": blobstore.BackendS3}}
	service := New(nil, &memorySettings{values: map[string]string{}}, nil, shared, shared)
	pass, err := service.copyTransitionData(t.Context(), stage, PolicyMigrateAll, publicTarget, privateTarget, true, true, true, "run", map[string]objectListing{}, false, func(int, int, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if pass.objects != 3 {
		t.Fatalf("copied objects = %d, want public artwork, avatar, and diagnostic", pass.objects)
	}
	if string(privateTarget.objects["profile-avatars/u/avatar.webp"]) != "avatar" {
		t.Fatal("avatar missing from private target")
	}
	if _, ok := privateTarget.objects["tmdb/poster.webp"]; ok {
		t.Fatal("legacy public tree was duplicated into private storage")
	}
	if string(privateTarget.objects["diagnostics/report.zip"]) != "diagnostic" {
		t.Fatal("legacy diagnostic missing from private target")
	}
	if _, ok := publicTarget.objects["diagnostics/report.zip"]; ok {
		t.Fatal("legacy diagnostic was copied to public storage")
	}
}

func TestExecuteRejectsOverlappingTargetPublicPrivate(t *testing.T) {
	for _, policy := range []string{PolicyPreserveUploads, PolicyMigrateAll, PolicyFresh} {
		t.Run(policy, func(t *testing.T) {
			source := &memoryStore{identity: "s3|endpoint|old|", objects: map[string][]byte{}}
			publicTarget := &memoryStore{identity: "s3|endpoint|shared|nested", objects: map[string][]byte{}}
			privateTarget := &memoryStore{identity: "s3|endpoint|shared|", objects: map[string][]byte{}}
			stage := stagedTarget{ID: "target-overlap", Policy: policy, SourceIdentity: source.Identity(), Phase: transitionPhaseStaged, Values: map[string]string{"artwork.storage_backend": blobstore.BackendS3, "s3.public_bucket": "shared", "s3.private_bucket": "shared"}}
			raw, err := json.Marshal(stage)
			if err != nil {
				t.Fatal(err)
			}
			settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
			service := New(nil, settings, nil, source, nil)
			service.openPublic = func(map[string]string) (blobstore.Store, error) { return publicTarget, nil }
			service.openPrivate = func(map[string]string) blobstore.Store { return privateTarget }
			service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
				return metadata.ArtworkReconcileStats{}, nil
			}
			_, err = service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: policy}, func(adminjob.StorageTransitionProgress) {})
			if err == nil || !strings.Contains(err.Error(), "target public and private") {
				t.Fatalf("ExecuteStorageTransition() error = %v, want target overlap", err)
			}
		})
	}
}

func TestFinalFencedPassIncludesObjectWrittenAfterBulkCopy(t *testing.T) {
	base := &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}
	source := &fencedMemoryStore{memoryStore: base}
	source.onFence = func() { base.objects["tmdb/b.webp"] = []byte("between-passes") }
	targetDir := t.TempDir()
	settings := stagedLocal(t, targetDir)
	service := New(nil, settings, nil, source, nil)
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.ArtworkReconcileStats{}, nil
	}
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	target, err := blobstore.NewFilesystem(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	reader, _, err := target.Get(t.Context(), "tmdb/b.webp")
	if err != nil {
		t.Fatal("object written between passes was omitted:", err)
	}
	data, _ := io.ReadAll(reader)
	_ = reader.Close()
	if string(data) != "between-passes" {
		t.Fatalf("target data = %q", data)
	}
}

func TestFinalFencedPassSkipsReadsForUnchangedSameRunObjects(t *testing.T) {
	base := &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}
	source := &fencedMemoryStore{memoryStore: base}
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	settings := stagedLocal(t, t.TempDir())
	service := New(nil, settings, nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if base.gets != 1 {
		t.Fatalf("source Get calls = %d, want one bulk-pass read and no fenced-pass read", base.gets)
	}
}

func TestFinalFencedPassRevalidatesWhenListingMetadataIsIncomplete(t *testing.T) {
	base := &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}
	source := &unreliableListingStore{memoryStore: base}
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	settings := stagedLocal(t, t.TempDir())
	service := New(nil, settings, nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if base.gets != 2 {
		t.Fatalf("source Get calls = %d, want bulk read plus fenced digest revalidation", base.gets)
	}
}

func TestFinalFencedPassRecopiesSameSizeReplacement(t *testing.T) {
	base := &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}
	source := &fencedMemoryStore{memoryStore: base}
	source.onFence = func() { base.objects["tmdb/a.webp"] = []byte("b") }
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	settings := stagedLocal(t, t.TempDir())
	service := New(nil, settings, nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if string(target.objects["tmdb/a.webp"]) != "b" || base.gets != 3 {
		t.Fatalf("same-size replacement was not recopied: target=%q source_gets=%d", target.objects["tmdb/a.webp"], base.gets)
	}
}

func TestLocalSourceDoesNotTrustSameSizeAndModificationTime(t *testing.T) {
	base := &memoryStore{identity: "local|source", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}
	source := &localInPlaceRewriteStore{memoryStore: base}
	source.onFence = func() { base.objects["tmdb/a.webp"] = []byte("b") }
	target := &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}
	targetPrivate := &memoryStore{identity: "s3|target|private|", objects: map[string][]byte{}}
	settings := stagedLocal(t, t.TempDir())
	service := New(nil, settings, nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return targetPrivate }
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if string(target.objects["tmdb/a.webp"]) != "b" || base.gets < 2 {
		t.Fatalf("local in-place rewrite was not digest-revalidated: target=%q gets=%d", target.objects["tmdb/a.webp"], base.gets)
	}
}

func TestCopyPrefixCancellationFlushesReceiptsForSameRunFencedPass(t *testing.T) {
	source := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{}}
	for i := range 6 {
		source.objects[fmt.Sprintf("tmdb/%02d.webp", i)] = []byte(fmt.Sprintf("image-%d", i))
	}
	target := &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}
	service := sequential(New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil))
	runID := "cancel-flush-run"
	listings := map[string]objectListing{}
	ctx, cancel := context.WithCancel(t.Context())
	_, _, _, err := service.copyPrefixPass(ctx, "cancel-flush", "public:", source, target, "", func(current, _ int, _ string) {
		if current == 3 {
			cancel()
		}
	}, 0, runID, listings, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first pass error=%v", err)
	}
	getsAfterCancel := source.gets
	if getsAfterCancel != 3 {
		t.Fatalf("source gets after cancellation=%d, want 3", getsAfterCancel)
	}
	if _, _, _, err := service.copyPrefixPass(t.Context(), "cancel-flush", "public:", source, target, "", func(int, int, string) {}, 0, runID, listings, true); err != nil {
		t.Fatal(err)
	}
	if source.gets-getsAfterCancel != 3 {
		t.Fatalf("resume source gets=%d, want only 3 uncopied objects", source.gets-getsAfterCancel)
	}
}

func TestBulkPassDoesNotUseSameRunListingShortcut(t *testing.T) {
	source := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("image")}}
	target := &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}
	service := New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil)
	runID := "bulk-no-shortcut"
	listings := map[string]objectListing{}
	if _, _, _, err := service.copyPrefixPass(t.Context(), "bulk-no-shortcut", "public:", source, target, "", func(int, int, string) {}, 0, runID, listings, false); err != nil {
		t.Fatal(err)
	}
	firstGets := source.gets
	if _, _, _, err := service.copyPrefixPass(t.Context(), "bulk-no-shortcut", "public:", source, target, "", func(int, int, string) {}, 0, runID, listings, false); err != nil {
		t.Fatal(err)
	}
	if source.gets != firstGets+1 {
		t.Fatalf("second bulk pass source gets=%d, want one digest revalidation", source.gets-firstGets)
	}
}

func TestBulkPassSkipsObjectVanishedAfterListing(t *testing.T) {
	base := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{
		"tmdb/gone.webp": []byte("gone"), "tmdb/keep.webp": []byte("keep"),
	}}
	source := &vanishAfterListStore{memoryStore: base, vanishKey: "tmdb/gone.webp"}
	target := &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}
	service := sequential(New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil))
	listings := map[string]objectListing{}
	copied, bytes, _, err := service.copyPrefixPass(t.Context(), "vanished", "public:", source, target, "", func(int, int, string) {}, 0, "bulk", listings, false)
	if err != nil {
		t.Fatal(err)
	}
	if copied != 1 || bytes != 4 || string(target.objects["tmdb/keep.webp"]) != "keep" {
		t.Fatalf("copied=%d bytes=%d target=%v, want only the remaining object", copied, bytes, target.objects)
	}
	if _, ok := service.memoryObjects[checkpointKey("vanished", "public:", "tmdb/gone.webp")]; ok {
		t.Fatal("vanished source object received a checkpoint")
	}
	if _, ok := listings[checkpointKey("vanished", "public:", "tmdb/gone.webp")]; ok {
		t.Fatal("vanished source object received a same-run listing")
	}
	if _, _, _, err := service.copyPrefixPass(t.Context(), "vanished", "public:", source, target, "", func(int, int, string) {}, 0, "bulk", listings, true); err != nil {
		t.Fatal(err)
	}
}

func TestBulkPassSkipsCheckpointSourceVanishedAfterListing(t *testing.T) {
	base := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{"tmdb/gone.webp": []byte("gone")}}
	target := &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}
	service := sequential(New(nil, &memorySettings{values: map[string]string{}}, nil, base, nil))
	if _, _, _, err := service.copyPrefixPass(t.Context(), "vanished-checkpoint", "public:", base, target, "", func(int, int, string) {}, 0, "first", nil, false); err != nil {
		t.Fatal(err)
	}
	source := &vanishAfterListStore{memoryStore: base, vanishKey: "tmdb/gone.webp"}
	copied, bytes, _, err := service.copyPrefixPass(t.Context(), "vanished-checkpoint", "public:", source, target, "", func(int, int, string) {}, 0, "second", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := service.memoryObjects[checkpointKey("vanished-checkpoint", "public:", "tmdb/gone.webp")]
	if copied != 0 || bytes != 0 || checkpoint.ListingRunID != "first" {
		t.Fatalf("vanished source copied=%d bytes=%d checkpoint run=%q, want unchanged first receipt", copied, bytes, checkpoint.ListingRunID)
	}
	if _, _, _, err := service.copyPrefixPass(t.Context(), "vanished-checkpoint", "public:", source, target, "", func(int, int, string) {}, 0, "second", nil, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := target.objects["tmdb/gone.webp"]; ok {
		t.Fatal("fenced orphan cleanup retained the deleted source object")
	}
	if _, ok := service.memoryObjects[checkpointKey("vanished-checkpoint", "public:", "tmdb/gone.webp")]; ok {
		t.Fatal("fenced orphan cleanup retained the deleted object checkpoint")
	}
}

func TestFencedPassFailsIfListedSourceObjectVanished(t *testing.T) {
	base := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{"tmdb/gone.webp": []byte("gone")}}
	source := &vanishAfterListStore{memoryStore: base, vanishKey: "tmdb/gone.webp"}
	target := &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}
	service := sequential(New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil))
	_, _, _, err := service.copyPrefixPass(t.Context(), "fenced-vanished", "public:", source, target, "", func(int, int, string) {}, 0, "run", nil, true)
	if !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("fenced pass error = %v, want ErrNotFound", err)
	}
}

func TestCopyPrefixErrorFlushesEarlierReceipts(t *testing.T) {
	source := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{
		"tmdb/01.webp": []byte("one"), "tmdb/02.webp": []byte("two"), "tmdb/03.webp": []byte("three"),
	}, failGetKey: "tmdb/02.webp"}
	service := sequential(New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil))
	_, _, _, err := service.copyPrefixPass(t.Context(), "error-flush", "public:", source, &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}, "", func(int, int, string) {}, 0, "run", map[string]objectListing{}, false)
	if err == nil {
		t.Fatal("expected injected object read failure")
	}
	if _, ok := service.memoryObjects[checkpointKey("error-flush", "public:", "tmdb/01.webp")]; !ok {
		t.Fatal("verified object before the failure has no durable receipt")
	}
	if _, ok := service.memoryObjects[checkpointKey("error-flush", "public:", "tmdb/02.webp")]; ok {
		t.Fatal("failed object received a checkpoint")
	}
}

func TestCopyPrefixThresholdFlushesBeforePageEnds(t *testing.T) {
	source := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{
		"tmdb/01.webp": []byte("one"), "tmdb/02.webp": []byte("two"),
	}}
	service := sequential(New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil))
	service.receiptFlushBytes = 1
	observed := false
	_, _, _, err := service.copyPrefixPass(t.Context(), "threshold-flush", "public:", source, &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}, "", func(current, _ int, _ string) {
		if current == 1 {
			_, observed = service.memoryObjects[checkpointKey("threshold-flush", "public:", "tmdb/01.webp")]
		}
	}, 0, "run", map[string]objectListing{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("byte threshold did not flush the first receipt before page completion")
	}
}

func TestFencedPassEarlyExitDoesNotDeleteOrphans(t *testing.T) {
	source := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{
		"tmdb/01.webp": []byte("one"), "tmdb/02.webp": []byte("two"), "tmdb/03.webp": []byte("three"),
	}}
	target := &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}
	service := sequential(New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil))
	runID := "orphan-run"
	listings := map[string]objectListing{}
	if _, _, _, err := service.copyPrefixPass(t.Context(), "orphan-early", "public:", source, target, "", func(int, int, string) {}, 0, runID, listings, false); err != nil {
		t.Fatal(err)
	}
	delete(source.objects, "tmdb/03.webp")
	ctx, cancel := context.WithCancel(t.Context())
	_, _, _, err := service.copyPrefixPass(ctx, "orphan-early", "public:", source, target, "", func(current, _ int, _ string) {
		if current == 1 {
			cancel()
		}
	}, 0, runID, listings, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fenced pass error=%v", err)
	}
	if _, ok := target.objects["tmdb/03.webp"]; !ok {
		t.Fatal("early fenced pass ran orphan cleanup")
	}
}

func TestFinalFencedPassDeletesOnlyCheckpointedTargetOrphans(t *testing.T) {
	base := &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{
		"profile-avatars/u/avatar.webp": []byte("avatar"),
		"tmdb/keep.webp":                []byte("keep"),
	}}
	source := &fencedMemoryStore{memoryStore: base}
	source.onFence = func() { delete(base.objects, "profile-avatars/u/avatar.webp") }
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{"unrelated/existing.webp": []byte("untouched")}}
	settings := stagedLocal(t, t.TempDir())
	service := New(nil, settings, nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if _, ok := target.objects["profile-avatars/u/avatar.webp"]; ok {
		t.Fatal("avatar deleted from the source between passes remained in the target")
	}
	if string(target.objects["unrelated/existing.webp"]) != "untouched" {
		t.Fatal("pre-existing unrelated target object was modified")
	}
	if string(base.objects["tmdb/keep.webp"]) != "keep" {
		t.Fatal("transition modified source storage")
	}
}

func TestPartialOrphanDeletionBlocksCommitAndPreservesCheckpoints(t *testing.T) {
	base := &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{
		"tmdb/failed.webp":    []byte("failed"),
		"tmdb/succeeded.webp": []byte("succeeded"),
		"tmdb/keep.webp":      []byte("keep"),
	}}
	source := &fencedMemoryStore{memoryStore: base}
	source.onFence = func() {
		delete(base.objects, "tmdb/failed.webp")
		delete(base.objects, "tmdb/succeeded.webp")
	}
	target := &partialDeleteStore{
		memoryStore:   &memoryStore{identity: "local|target", objects: map[string][]byte{}},
		failDeleteKey: "tmdb/failed.webp",
	}
	settings := stagedLocal(t, t.TempDir())
	service := New(nil, settings, nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	request := adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}
	if _, err := service.ExecuteStorageTransition(t.Context(), request, func(adminjob.StorageTransitionProgress) {}); err == nil || !strings.Contains(err.Error(), "deleted 1 of 2") {
		t.Fatalf("partial orphan deletion error=%v", err)
	}
	if settings.values[blobstore.IdentitySettingKey] != "" {
		t.Fatal("transition committed after an orphan failed deletion")
	}
	var staged stagedTarget
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &staged); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tmdb/failed.webp", "tmdb/succeeded.webp"} {
		if _, ok := service.memoryObjects[checkpointKey(staged.ID, "public:", key)]; !ok {
			t.Fatalf("orphan checkpoint %q was removed after a partial deletion", key)
		}
	}
	if _, ok := target.objects["tmdb/failed.webp"]; !ok {
		t.Fatal("failed target object was unexpectedly deleted")
	}
	if _, ok := target.objects["tmdb/succeeded.webp"]; ok {
		t.Fatal("successfully deleted target object remains")
	}

	target.failDeleteKey = ""
	request.TransitionID = staged.ID
	if _, err := service.ExecuteStorageTransition(t.Context(), request, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatalf("retry after deletion permission restored: %v", err)
	}
	if settings.values[blobstore.IdentitySettingKey] != target.Identity() {
		t.Fatal("transition did not commit after orphan cleanup succeeded")
	}
	for _, key := range []string{"tmdb/failed.webp", "tmdb/succeeded.webp"} {
		if _, ok := service.memoryObjects[checkpointKey(staged.ID, "public:", key)]; ok {
			t.Fatalf("orphan checkpoint %q remains after successful retry", key)
		}
	}
}

func TestSameRunListingShortcutCoversMultiplePages(t *testing.T) {
	objects := make(map[string][]byte, 301)
	for i := range 301 {
		objects[fmt.Sprintf("tmdb/%03d.webp", i)] = []byte(fmt.Sprintf("%03d", i))
	}
	base := &memoryStore{identity: "s3|old|public|", objects: objects}
	source := &fencedMemoryStore{memoryStore: base}
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	settings := stagedLocal(t, t.TempDir())
	service := New(nil, settings, nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if base.gets != len(objects) || len(target.objects) != len(objects) {
		t.Fatalf("multi-page shortcut source_gets=%d target_objects=%d want=%d", base.gets, len(target.objects), len(objects))
	}
}

func TestAmbiguousCommitErrorRemainsCommitted(t *testing.T) {
	source := &fencedMemoryStore{memoryStore: &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}}
	baseSettings := stagedLocal(t, t.TempDir())
	settings := &applyingErrorSettings{memorySettings: baseSettings, failCommit: true}
	service := New(nil, settings, nil, source, nil)
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.ArtworkReconcileStats{}, nil
	}
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatalf("ambiguous committed update returned failure: %v", err)
	}
	var staged stagedTarget
	if err := json.Unmarshal([]byte(baseSettings.values[StagedTargetSettingKey]), &staged); err != nil {
		t.Fatal(err)
	}
	if staged.Phase != transitionPhaseRestartPending || source.released || !source.fenced {
		t.Fatalf("committed stage/fence = %#v fenced=%t released=%t", staged, source.fenced, source.released)
	}
}

func TestAmbiguousCommitVerificationRetriesThroughTransientReadFailures(t *testing.T) {
	source := &fencedMemoryStore{memoryStore: &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}}
	settings := &applyingFlakyReadSettings{memorySettings: stagedLocal(t, t.TempDir()), remainingFailures: 2}
	service := New(nil, settings, nil, source, nil)
	service.commitVerifyBackoff = func(context.Context, int) error { return nil }
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatalf("ambiguous committed update returned failure after verification recovered: %v", err)
	}
	if !source.fenced || source.released {
		t.Fatalf("committed source fence was released: fenced=%t released=%t", source.fenced, source.released)
	}
}

func TestUndeterminedCommitOutcomeKeepsFenceAndRequestsRecoveryRestart(t *testing.T) {
	source := &fencedMemoryStore{memoryStore: &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}}
	settings := &applyingFlakyReadSettings{memorySettings: stagedLocal(t, t.TempDir()), remainingFailures: 99}
	service := New(nil, settings, nil, source, nil)
	service.commitVerifyAttempts = 2
	service.commitVerifyBackoff = func(context.Context, int) error { return nil }
	lastMessage := ""
	result, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(progress adminjob.StorageTransitionProgress) { lastMessage = progress.Message })
	if err != nil {
		t.Fatalf("undetermined commit outcome returned failure: %v", err)
	}
	if !result.(Result).CommitUnknown {
		t.Fatal("undetermined commit outcome was not surfaced to restart recovery")
	}
	if !source.fenced || source.released {
		t.Fatalf("possibly committed source fence was released: fenced=%t released=%t", source.fenced, source.released)
	}
	if !strings.Contains(lastMessage, "outcome is unknown") {
		t.Fatalf("progress message = %q, want unknown commit recovery guidance", lastMessage)
	}
}

func TestCommitFailureDoesNotReconcileAndReleasesFence(t *testing.T) {
	source := &fencedMemoryStore{memoryStore: &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}}
	baseSettings := stagedLocal(t, t.TempDir())
	settings := &rejectingCommitSettings{memorySettings: baseSettings}
	service := New(nil, settings, nil, source, nil)
	reconciled := false
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		reconciled = true
		return metadata.ArtworkReconcileStats{}, nil
	}
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err == nil {
		t.Fatal("expected commit failure")
	}
	if reconciled {
		t.Fatal("catalog reconciled before a successful commit")
	}
	if !source.released || source.fenced {
		t.Fatalf("source fence not released after commit failure: fenced=%t released=%t", source.fenced, source.released)
	}
}

func TestCancellationAfterBulkCopyDoesNotReconcileAndReleasesFence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	source := &fencedMemoryStore{memoryStore: &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}}
	source.onFence = cancel
	settings := stagedLocal(t, t.TempDir())
	service := New(nil, settings, nil, source, nil)
	reconciled := false
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		reconciled = true
		return metadata.ArtworkReconcileStats{}, nil
	}
	if _, err := service.ExecuteStorageTransition(ctx, adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteStorageTransition() error = %v, want context cancellation", err)
	}
	if reconciled {
		t.Fatal("catalog reconciled before commit after cancellation")
	}
	if !source.released || source.fenced {
		t.Fatalf("source fence not released after cancellation: fenced=%t released=%t", source.fenced, source.released)
	}
}

func TestCancelQueuedTransitionMakesStageReplaceable(t *testing.T) {
	stage := stagedTarget{ID: "queued", Policy: PolicyMigrateAll, SourceIdentity: "s3|old|public|", Phase: transitionPhaseStaged, Values: map[string]string{"artwork.storage_backend": blobstore.BackendLocal, "artwork.local_path": t.TempDir()}}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, nil, &memoryStore{identity: stage.SourceIdentity, objects: map[string][]byte{}}, nil)
	if err := service.CancelStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: stage.Policy}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &stage); err != nil {
		t.Fatal(err)
	}
	if stage.Phase != transitionPhaseFailed || !strings.Contains(stage.LastError, "canceled") {
		t.Fatalf("canceled stage = %#v", stage)
	}
}

func TestCopyPrefixSkipsInvalidListedKeys(t *testing.T) {
	source := &memoryStore{identity: "s3|old|bucket|", objects: map[string][]byte{
		"branding/":          {},
		"branding/logo.webp": []byte("logo"),
	}}
	target := &memoryStore{identity: "s3|new|bucket|", objects: map[string][]byte{}}
	service := testService(&memorySettings{values: map[string]string{}}, source)
	copied, _, skipped, err := service.copyPrefix(t.Context(), "invalid-keys", "public:", source, target, "", func(int, int, string) {}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if copied != 1 || len(skipped) != 1 || skipped[0] != "branding/" {
		t.Fatalf("copied=%d skipped=%#v", copied, skipped)
	}
	if _, ok := target.objects["branding/"]; ok {
		t.Fatal("invalid folder marker was copied")
	}
}

func TestTransitionResultSurfacesSkippedInvalidKeys(t *testing.T) {
	source := &memoryStore{identity: "s3|old|bucket|", objects: map[string][]byte{
		"branding/":          {},
		"branding/logo.webp": []byte("logo"),
	}}
	settings := stagedLocal(t, t.TempDir())
	service := testService(settings, source)
	var messages []string
	value, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(progress adminjob.StorageTransitionProgress) {
		messages = append(messages, progress.Message)
	})
	if err != nil {
		t.Fatal(err)
	}
	result := value.(Result)
	if result.SkippedObjects != 1 || len(result.SkippedKeys) != 1 || result.SkippedKeys[0] != "branding/" {
		t.Fatalf("result skipped fields = %#v", result)
	}
	if !slices.Contains(messages, "Skipped invalid storage key branding/") {
		t.Fatalf("progress did not surface skipped key: %#v", messages)
	}
	var committed stagedTarget
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &committed); err != nil {
		t.Fatal(err)
	}
	if !committed.PublicReconcile {
		t.Fatal("migrate-all with skipped keys did not schedule catalog reconciliation")
	}
}

func TestSubtitleCopyPolicy(t *testing.T) {
	for _, tt := range []struct {
		name          string
		policy        string
		targetBackend string
		wantCopied    bool
	}{
		{name: "s3 preserve", policy: PolicyPreserveUploads, targetBackend: blobstore.BackendS3, wantCopied: true},
		{name: "s3 migrate all", policy: PolicyMigrateAll, targetBackend: blobstore.BackendS3, wantCopied: true},
		{name: "local preserve", policy: PolicyPreserveUploads, targetBackend: blobstore.BackendLocal, wantCopied: true},
		{name: "local migrate all", policy: PolicyMigrateAll, targetBackend: blobstore.BackendLocal, wantCopied: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := &memoryStore{identity: "s3|old|public|", objects: map[string][]byte{"subtitles/movie/en.srt": []byte("subtitle")}}
			target := &memoryStore{identity: tt.targetBackend + "|target", objects: map[string][]byte{}}
			stage := stagedTarget{ID: "subtitles", SourceIdentity: source.Identity(), Values: map[string]string{"artwork.storage_backend": tt.targetBackend}}
			service := New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil)
			if _, err := service.copyTransitionData(t.Context(), stage, tt.policy, target, nil, true, false, false, "run", map[string]objectListing{}, false, func(int, int, string) {}); err != nil {
				t.Fatal(err)
			}
			_, copied := target.objects["subtitles/movie/en.srt"]
			if copied != tt.wantCopied {
				t.Fatalf("subtitle copied = %t, want %t", copied, tt.wantCopied)
			}
		})
	}
}

func TestPrivateOnlyMigrationSkipsPublicReconcileAndCopiesPrivateData(t *testing.T) {
	public := &memoryStore{identity: "s3|endpoint|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("public")}}
	oldPrivate := &memoryStore{identity: "s3|endpoint|private-old|", objects: map[string][]byte{
		"diagnostics/report.zip":        []byte("report"),
		"profile-avatars/u/avatar.webp": []byte("avatar"),
	}}
	newPrivate := &memoryStore{identity: "s3|endpoint|private-new|", objects: map[string][]byte{}}
	stage := stagedTarget{
		ID:                  "private-only",
		Policy:              PolicyMigrateAll,
		SourceIdentity:      public.Identity(),
		SourcePrivateBucket: "private-old",
		TargetPrivateBucket: "private-new",
		Phase:               transitionPhaseStaged,
		Values:              map[string]string{"artwork.storage_backend": blobstore.BackendS3, "s3.public_bucket": "public", "s3.private_bucket": "private-new"},
	}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, nil, public, oldPrivate)
	service.openPublic = func(map[string]string) (blobstore.Store, error) {
		return &memoryStore{identity: public.Identity(), objects: map[string][]byte{}}, nil
	}
	service.openPrivate = func(map[string]string) blobstore.Store { return newPrivate }
	reconciled := false
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		reconciled = true
		return metadata.ArtworkReconcileStats{}, nil
	}

	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if reconciled {
		t.Fatal("private-only transition reconciled unchanged public artwork")
	}
	if string(newPrivate.objects["diagnostics/report.zip"]) != "report" || string(newPrivate.objects["profile-avatars/u/avatar.webp"]) != "avatar" {
		t.Fatalf("private target is incomplete: %#v", newPrivate.objects)
	}
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		t.Fatal("private-only transition reconciled public artwork after restart")
		return metadata.ArtworkReconcileStats{}, nil
	}
	if err := service.FinalizeCommitted(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCopyPrefixResumeRevalidatesCompletedSourceObjects(t *testing.T) {
	source := &memoryStore{identity: "s3|old|bucket|", objects: map[string][]byte{
		"tmdb/a.webp": []byte("a"),
		"tmdb/b.webp": []byte("bb"),
	}}
	target, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := testService(&memorySettings{values: map[string]string{}}, source)
	if _, _, _, err := service.copyPrefix(t.Context(), "transition-1", "public:tmdb", source, target, "tmdb", func(int, int, string) {}, 0); err != nil {
		t.Fatal(err)
	}
	firstGets := source.gets
	if _, _, _, err := service.copyPrefix(t.Context(), "transition-1", "public:tmdb", source, target, "tmdb", func(int, int, string) {}, 0); err != nil {
		t.Fatal(err)
	}
	if source.gets != firstGets+2 {
		t.Fatalf("resume source revalidation gets %d -> %d, want two checks", firstGets, source.gets)
	}
}

func TestCopyPrefixResumeFindsObjectsAddedBeforeSavedCursor(t *testing.T) {
	source := &memoryStore{identity: "s3|old|bucket|", objects: map[string][]byte{
		"tmdb/b.webp": []byte("b"),
	}}
	target := &memoryStore{identity: "s3|new|bucket|", objects: map[string][]byte{}}
	service := testService(&memorySettings{values: map[string]string{}}, source)
	if _, _, _, err := service.copyPrefix(t.Context(), "transition-1", "public:tmdb", source, target, "tmdb", func(int, int, string) {}, 0); err != nil {
		t.Fatal(err)
	}
	source.objects["tmdb/a.webp"] = []byte("new-before-cursor")
	if _, _, _, err := service.copyPrefix(t.Context(), "transition-1", "public:tmdb", source, target, "tmdb", func(int, int, string) {}, 0); err != nil {
		t.Fatal(err)
	}
	if string(target.objects["tmdb/a.webp"]) != "new-before-cursor" {
		t.Fatal("object added before the old cursor was omitted on resume")
	}
}

func TestCopyPrefixResumeRepairsChangedSourceObject(t *testing.T) {
	source := &memoryStore{identity: "s3|old|bucket|", objects: map[string][]byte{
		"tmdb/a.webp": []byte("first"),
	}}
	target := &memoryStore{identity: "s3|new|bucket|", objects: map[string][]byte{}}
	service := testService(&memorySettings{values: map[string]string{}}, source)
	if _, _, _, err := service.copyPrefix(t.Context(), "transition-1", "public:tmdb", source, target, "tmdb", func(int, int, string) {}, 0); err != nil {
		t.Fatal(err)
	}
	source.objects["tmdb/a.webp"] = []byte("other") // same size, different content
	if _, _, _, err := service.copyPrefix(t.Context(), "transition-1", "public:tmdb", source, target, "tmdb", func(int, int, string) {}, 0); err != nil {
		t.Fatal(err)
	}
	if string(target.objects["tmdb/a.webp"]) != "other" {
		t.Fatalf("target retained stale source content: %q", target.objects["tmdb/a.webp"])
	}
}

func TestCopyPrefixRepairsSameSizeCorruptionOnResume(t *testing.T) {
	source := &memoryStore{identity: "source", objects: map[string][]byte{
		"tmdb/a.webp": []byte("right"),
	}}
	target := &memoryStore{identity: "target", objects: map[string][]byte{}}
	service := testService(&memorySettings{values: map[string]string{}}, source)
	if _, _, _, err := service.copyPrefix(t.Context(), "transition-1", "public:tmdb", source, target, "tmdb", func(int, int, string) {}, 0); err != nil {
		t.Fatal(err)
	}
	firstGets := source.gets
	target.objects["tmdb/a.webp"] = []byte("wrong") // same length as the verified source object
	if _, _, _, err := service.copyPrefix(t.Context(), "transition-1", "public:tmdb", source, target, "tmdb", func(int, int, string) {}, 0); err != nil {
		t.Fatal(err)
	}
	if source.gets != firstGets+2 {
		t.Fatalf("corrupt object was not recopied: gets %d -> %d", firstGets, source.gets)
	}
	if string(target.objects["tmdb/a.webp"]) != "right" {
		t.Fatalf("target remains corrupt: %q", target.objects["tmdb/a.webp"])
	}
}

func TestFailedTransitionRetainsRecoverableStage(t *testing.T) {
	source := &memoryStore{identity: "s3|old|bucket|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")}, failGet: true}
	settings := stagedLocal(t, t.TempDir())
	_, err := testService(settings, source).ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {})
	if err == nil {
		t.Fatal("expected injected copy failure")
	}
	var staged stagedTarget
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &staged); err != nil {
		t.Fatal(err)
	}
	if staged.Phase != transitionPhaseFailed || !strings.Contains(staged.LastError, "injected read failure") {
		t.Fatalf("stage was not retained for retry: %#v", staged)
	}
}

func TestStartReplacesFailedStageWhenTargetChanges(t *testing.T) {
	old, err := json.Marshal(stagedTarget{
		ID:             "failed-transition",
		Policy:         PolicyMigrateAll,
		SourceIdentity: "source",
		Phase:          transitionPhaseFailed,
		Values: map[string]string{
			"artwork.storage_backend": "local",
			"artwork.local_path":      "/bad-target",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": blobstore.BackendLocal,
		"artwork.local_path":      t.TempDir(),
		StagedTargetSettingKey:    string(old),
	}}
	service := New(nil, settings, memoryJobs{}, &memoryStore{identity: "source", objects: map[string][]byte{}}, nil)
	if _, _, err := service.Start(t.Context(), 1, StartRequest{Policy: PolicyMigrateAll, Values: map[string]string{
		"artwork.storage_backend": blobstore.BackendLocal,
		"artwork.local_path":      t.TempDir(),
	}}); err != nil {
		t.Fatal(err)
	}
	var replacement stagedTarget
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.ID == "failed-transition" || replacement.Phase != transitionPhaseStaged {
		t.Fatalf("failed stage was not replaced: %#v", replacement)
	}
}

func TestStartRejectsExistingActiveStorageTransitionWithoutChangingStage(t *testing.T) {
	for _, status := range []string{adminjob.StatusQueued, adminjob.StatusRunning} {
		t.Run(status, func(t *testing.T) {
			stage := stagedTarget{
				ID:             "existing-transition",
				Policy:         PolicyMigrateAll,
				SourceIdentity: "source",
				Phase:          transitionPhaseCopying,
				LastError:      "keep this state",
				Values: map[string]string{
					settingArtworkBackend:   blobstore.BackendLocal,
					settingArtworkLocalPath: "/existing-target",
				},
			}
			raw, err := json.Marshal(stage)
			if err != nil {
				t.Fatal(err)
			}
			settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
			jobs := &activeMemoryJobs{job: &models.AdminJob{ID: "active-job", JobType: adminjob.JobTypeStorageTransition, Status: status}}
			service := New(nil, settings, jobs, &memoryStore{identity: "source", objects: map[string][]byte{}}, nil)

			_, _, err = service.Start(t.Context(), 1, StartRequest{Policy: PolicyMigrateAll, Values: stage.Values})
			var conflict *adminjob.ActiveJobConflictError
			if !errors.As(err, &conflict) || conflict.Job == nil || conflict.Job.ID != "active-job" {
				t.Fatalf("Start error = %v, want active job conflict", err)
			}
			if jobs.createCalls != 0 {
				t.Fatalf("Create calls = %d, want 0", jobs.createCalls)
			}
			if settings.values[StagedTargetSettingKey] != string(raw) {
				t.Fatalf("active transition stage changed:\n got %s\nwant %s", settings.values[StagedTargetSettingKey], raw)
			}
		})
	}
}

func TestExecuteRefusesCommittedTransitionAwaitingRestart(t *testing.T) {
	stage := stagedTarget{
		ID:             "committed-transition",
		Policy:         PolicyMigrateAll,
		SourceIdentity: "source",
		Phase:          transitionPhaseRestartPending,
		Values: map[string]string{
			settingArtworkBackend:   blobstore.BackendLocal,
			settingArtworkLocalPath: t.TempDir(),
		},
	}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(nil, settings, memoryJobs{}, &memoryStore{identity: "source", objects: map[string][]byte{}}, nil)

	_, err = service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: stage.Policy}, func(adminjob.StorageTransitionProgress) {})
	if err == nil || !strings.Contains(err.Error(), "already committed") {
		t.Fatalf("ExecuteStorageTransition error = %v, want committed-stage rejection", err)
	}
	var after stagedTarget
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &after); err != nil {
		t.Fatal(err)
	}
	if after.Phase != transitionPhaseRestartPending {
		t.Fatalf("phase = %q, want %q", after.Phase, transitionPhaseRestartPending)
	}
}

func TestStartRejectsCopyWhenCurrentPublicS3IsUnreachable(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": blobstore.BackendS3,
		"s3.public_bucket":        "old-public",
	}}
	service := New(nil, settings, memoryJobs{}, &memoryStore{
		identity: "s3|old|old-public|",
		objects:  map[string][]byte{},
		probeErr: errors.New("injected source outage"),
	}, nil)

	_, _, err := service.Start(t.Context(), 1, StartRequest{Policy: PolicyMigrateAll, Values: map[string]string{
		"artwork.storage_backend": blobstore.BackendLocal,
		"artwork.local_path":      t.TempDir(),
	}})
	if err == nil || !strings.Contains(err.Error(), "current public S3 storage is unreachable") {
		t.Fatalf("error = %v, want unreachable public S3 preflight failure", err)
	}
	if strings.Contains(err.Error(), "injected source outage") {
		t.Fatalf("error exposed provider diagnostics: %v", err)
	}
	if settings.values[StagedTargetSettingKey] != "" {
		t.Fatal("unreachable source was staged")
	}
}

func TestStartRejectsCopyWhenCurrentPrivateS3IsUnreachable(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": blobstore.BackendS3,
		"s3.public_bucket":        "old-public",
		"s3.private_bucket":       "old-private",
	}}
	public := &memoryStore{identity: "s3|old|old-public|", objects: map[string][]byte{}}
	private := &memoryStore{identity: "s3|old|old-private|", objects: map[string][]byte{}, probeErr: errors.New("injected private outage")}
	service := New(nil, settings, memoryJobs{}, public, private)

	_, _, err := service.Start(t.Context(), 1, StartRequest{Policy: PolicyPreserveUploads, Values: map[string]string{
		"artwork.storage_backend": blobstore.BackendLocal,
		"artwork.local_path":      t.TempDir(),
	}})
	if err == nil || !strings.Contains(err.Error(), "current private S3 storage is unreachable") {
		t.Fatalf("error = %v, want unreachable private S3 preflight failure", err)
	}
}

func TestStartFreshAllowsUnreachableCurrentS3(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": blobstore.BackendS3,
		"s3.public_bucket":        "old-public",
		"s3.private_bucket":       "old-private",
	}}
	public := &memoryStore{identity: "s3|old|old-public|", objects: map[string][]byte{}, probeErr: errors.New("injected source outage")}
	service := New(nil, settings, memoryJobs{}, public, nil)

	job, _, err := service.Start(t.Context(), 1, StartRequest{Policy: PolicyFresh, Values: map[string]string{
		"artwork.storage_backend": blobstore.BackendLocal,
		"artwork.local_path":      t.TempDir(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.ID == "" {
		t.Fatal("fresh transition did not create a job")
	}
}

func TestStartRejectsAvatarPreservationToPublicOnlyS3(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": blobstore.BackendLocal,
		"artwork.local_path":      t.TempDir(),
	}}
	service := New(nil, settings, memoryJobs{}, &memoryStore{identity: "local|source", objects: map[string][]byte{}}, nil)

	_, _, err := service.Start(t.Context(), 1, StartRequest{Policy: PolicyPreserveUploads, Values: map[string]string{
		"artwork.storage_backend": blobstore.BackendS3,
		"s3.public_endpoint":      "https://s3.example",
		"s3.public_bucket":        "public-only",
	}})
	if err == nil || !strings.Contains(err.Error(), "private S3 bucket is required") {
		t.Fatalf("error = %v, want private-bucket requirement", err)
	}
	if settings.values[StagedTargetSettingKey] != "" {
		t.Fatal("unsafe public-only avatar transition was staged")
	}
}

func TestSourceHealthReportsConfiguredS3Stores(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": blobstore.BackendS3,
		"s3.public_bucket":        "old-public",
		"s3.private_bucket":       "old-private",
	}}
	public := &memoryStore{identity: "s3|old|old-public|", objects: map[string][]byte{}, probeErr: errors.New("public unavailable")}
	private := &memoryStore{identity: "s3|old|old-private|", objects: map[string][]byte{}}
	service := New(nil, settings, memoryJobs{}, public, private)

	health, err := service.SourceHealth(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if health.Reachable || !health.PublicConfigured || health.PublicReachable || !health.PrivateConfigured || !health.PrivateReachable {
		t.Fatalf("unexpected source health: %#v", health)
	}
	if health.Message != "Current public S3 storage is unreachable." {
		t.Fatalf("message = %q", health.Message)
	}
}
