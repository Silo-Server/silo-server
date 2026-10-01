package storagetransition

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

const StagedTargetSettingKey = config.StorageTransitionTargetKey

const storageTransitionAdmissionLockKey int64 = 0x53494c4f535453

const (
	transitionPhaseStaged         = "staged"
	transitionPhaseCopying        = "copying"
	transitionPhaseFailed         = "failed"
	transitionPhaseRestartPending = "restart_pending"
)

const (
	recoveryStatePending      = "pending"
	recoveryStateRunning      = "running"
	recoveryStateWaitingRetry = "waiting_retry"
	recoveryStateBlocked      = "blocked"
)

var (
	errPrivateBucketRequired     = errors.New("a private S3 bucket is required to preserve profile avatars; configure private storage or choose Start fresh")
	errUnverifiedTargetNamespace = errors.New("target public and private storage locations may overlap; use distinct buckets or key prefixes for Start fresh")
	errCommittedTargetMismatch   = errors.New("committed storage target does not match the active artwork store")
	errCommittedStageUnreadable  = errors.New("committed storage transition state cannot be read")
	errCommittedStageInvalid     = errors.New("committed storage transition state is invalid")
)

const (
	PolicyFresh           = "start_fresh"
	PolicyPreserveUploads = "preserve_uploads"
	PolicyMigrateAll      = "migrate_all"

	settingArtworkBackend   = "artwork.storage_backend"
	settingArtworkLocalPath = "artwork.local_path"
	settingPublicEndpoint   = "s3.public_endpoint"
	settingPublicBucket     = "s3.public_bucket"
	settingPrivateEndpoint  = "s3.private_endpoint"
	settingPrivateBucket    = "s3.private_bucket"
	settingPublicReadURL    = "s3.public_read_endpoint"
	settingPublicAccessKey  = "s3.public_access_key"
	settingPrivateAccessKey = "s3.private_access_key"
	settingLegacyEndpoint   = "s3.operational_endpoint"
	settingLegacyBucket     = "s3.operational_bucket"
	settingPublicSecretKey  = "s3.public_secret_key"
	settingPublicKeyPrefix  = "s3.public_key_prefix"
	settingPrivateKeyPrefix = "s3.private_key_prefix"
	settingPrivateSecretKey = "s3.private_secret_key"
	storageRolePublic       = "public"
	storageRolePrivate      = "private"
)

var publicStorageKeys = []string{
	settingArtworkBackend, settingArtworkLocalPath,
	settingPublicEndpoint, settingPublicReadURL, "s3.public_region", "s3.public_path_style",
	settingPublicBucket, settingPublicKeyPrefix, settingPublicAccessKey, settingPublicSecretKey,
	"s3.public_url_auth", "s3.public_token_secret", "s3.public_token_param", "s3.public_token_ttl",
}

var privateStorageKeys = []string{
	settingPrivateEndpoint, "s3.private_region", "s3.private_path_style", settingPrivateBucket,
	settingPrivateKeyPrefix, settingPrivateAccessKey, settingPrivateSecretKey,
}

var legacyOperationalKeys = []string{
	settingLegacyEndpoint, "s3.operational_public_endpoint", "s3.operational_region",
	"s3.operational_path_style", settingLegacyBucket, "s3.operational_key_prefix",
	"s3.operational_access_key", "s3.operational_secret_key", "s3.operational_url_auth",
	"s3.operational_token_secret", "s3.operational_token_param", "s3.operational_token_ttl",
}

type Settings interface {
	Get(context.Context, string) (string, error)
	GetAll(context.Context) (map[string]string, error)
	UpdateAtomic(context.Context, func(map[string]string) (map[string]string, error)) error
}

type JobRepository interface {
	Create(context.Context, adminjob.CreateJobInput) (*models.AdminJob, error)
	GetActiveByType(context.Context, string) (*models.AdminJob, error)
}

type StartRequest struct {
	Policy string            `json:"policy"`
	Values map[string]string `json:"values"`
}

type Preflight struct {
	CurrentBackend string   `json:"current_backend"`
	TargetBackend  string   `json:"target_backend"`
	Policy         string   `json:"policy"`
	Warnings       []string `json:"warnings"`
	ProviderImages string   `json:"provider_images"`
	Uploads        string   `json:"uploads"`
	Diagnostics    string   `json:"diagnostics"`
	Subtitles      string   `json:"subtitles"`
	CatalogSeeds   string   `json:"catalog_seeds"`
}

type SourceHealth struct {
	CurrentBackend     string `json:"current_backend"`
	Reachable          bool   `json:"reachable"`
	PublicConfigured   bool   `json:"public_configured"`
	PublicReachable    bool   `json:"public_reachable"`
	PrivateConfigured  bool   `json:"private_configured"`
	PrivateReachable   bool   `json:"private_reachable"`
	ReachabilityProbed bool   `json:"reachability_probed"`
	Message            string `json:"message"`
	RecoveryPending    bool   `json:"recovery_pending"`
	RecoveryState      string `json:"recovery_state,omitempty"`
	RecoveryError      string `json:"recovery_error,omitempty"`
	RecoveryProgress   int    `json:"recovery_progress_percent,omitempty"`
	RecoveryMessage    string `json:"recovery_progress_message,omitempty"`
}

type SourceUnavailableError struct {
	Scope string
	Err   error
}

func (e *SourceUnavailableError) Error() string {
	return fmt.Sprintf("current %s S3 storage is unreachable; reconnect the existing bucket or choose Start fresh", e.Scope)
}

func (e *SourceUnavailableError) Unwrap() error { return e.Err }

// ValidationError identifies a request or transition-state problem that the
// administrator can correct. Infrastructure failures remain unwrapped so API
// callers receive a retryable service error instead of a misleading 422.
type ValidationError struct {
	Err error
}

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func NewValidationError(err error) *ValidationError {
	return &ValidationError{Err: err}
}

func validationErrorf(format string, args ...any) error {
	return NewValidationError(fmt.Errorf(format, args...))
}

type stagedTarget struct {
	ID                  string            `json:"id"`
	Policy              string            `json:"policy"`
	SourceIdentity      string            `json:"source_identity"`
	SourcePrivateBucket string            `json:"source_private_bucket,omitempty"`
	TargetIdentity      string            `json:"target_identity,omitempty"`
	TargetPrivateBucket string            `json:"target_private_bucket,omitempty"`
	PublicReconcile     bool              `json:"public_reconcile,omitempty"`
	BrandingReconcile   bool              `json:"branding_reconcile,omitempty"`
	SkippedObjects      int               `json:"skipped_objects,omitempty"`
	Phase               string            `json:"phase"`
	LastError           string            `json:"last_error,omitempty"`
	RecoveryState       string            `json:"recovery_state,omitempty"`
	RecoveryProgress    int               `json:"recovery_progress_percent,omitempty"`
	RecoveryMessage     string            `json:"recovery_progress_message,omitempty"`
	Values              map[string]string `json:"values"`
	// Baseline is the storage settings as they were at Start. Commit compares
	// against it to tell the transition's own changes from edits saved while
	// it ran.
	Baseline map[string]string `json:"baseline,omitempty"`
}

type objectCheckpoint struct {
	Size         int64
	SHA256       string
	Listing      objectListing
	ListingRunID string
}

type objectListing struct {
	Size    int64
	ETag    string
	ModTime time.Time
}

func listingFromObject(info blobstore.ObjectInfo) objectListing {
	return objectListing{Size: info.Size, ETag: info.ETag, ModTime: info.ModTime}
}

func (l objectListing) reliable() bool {
	return l.ETag != "" && !l.ModTime.IsZero()
}

type artworkReconcileCheckpointEnvelope struct {
	BaselineIdentity string                              `json:"baseline_identity"`
	TargetIdentity   string                              `json:"target_identity"`
	Checkpoint       metadata.ArtworkReconcileCheckpoint `json:"checkpoint"`
}

type prefixCursor struct {
	Cursor    string
	Objects   int
	Bytes     int64
	Completed bool
}

type Result struct {
	Phase                 string                         `json:"phase"`
	VerifiedObjects       int                            `json:"verified_objects"`
	Policy                string                         `json:"policy"`
	SourceIdentity        string                         `json:"source_identity"`
	TargetIdentity        string                         `json:"target_identity"`
	CopiedObjects         int                            `json:"copied_objects"`
	CopiedBytes           int64                          `json:"copied_bytes"`
	SkippedObjects        int                            `json:"skipped_objects"`
	SkippedKeys           []string                       `json:"skipped_keys,omitempty"`
	ArtworkReconcile      metadata.ArtworkReconcileStats `json:"artwork_reconcile"`
	CommitUnknown         bool                           `json:"commit_outcome_unknown,omitempty"`
	ClaimGeneration       int64                          `json:"claim_generation,omitzero"`
	ManualRestartRequired bool                           `json:"manual_restart_required,omitzero"`
	RestartRequired       bool                           `json:"restart_required"`
	OldStorageRetained    bool                           `json:"old_storage_retained"`
}

// StorageTransitionCommitUnknown lets the generic admin-job runner preserve a
// running receipt for restart recovery without importing this package.
func (r Result) StorageTransitionCommitUnknown() bool { return r.CommitUnknown }

// WithStorageTransitionRestartReceipt ties the committed result to its worker
// claim so a resumed job cannot display an older restart receipt as current.
func (r Result) WithStorageTransitionRestartReceipt(manual bool, claimGeneration int64) any {
	r.ManualRestartRequired = manual
	r.ClaimGeneration = claimGeneration
	return r
}

type Service struct {
	admissionMu          sync.Mutex
	nodeAdmission        *pglock.NodeAdmission
	pool                 *pgxpool.Pool
	settings             Settings
	jobs                 JobRepository
	source               blobstore.Store
	private              blobstore.Store
	reconcile            func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error)
	reconcileResumable   func(context.Context, blobstore.Store, *metadata.ArtworkReconcileCheckpoint, func(metadata.ArtworkReconcileCheckpoint) error, func(float64, string)) (metadata.ArtworkReconcileStats, error)
	brandingReconcile    func(context.Context) (int, int, error)
	openPublic           func(map[string]string) (blobstore.Store, error)
	openPrivate          func(map[string]string) blobstore.Store
	commitVerifyAttempts int
	commitVerifyBackoff  func(context.Context, int) error
	postRestartBackoff   func(context.Context, int) error
	progressInterval     time.Duration
	probeTimeout         time.Duration
	receiptFlushBytes    int64
	receiptFlushInterval time.Duration
	copyWorkers          int
	smallObjectBytes     int64
	// Execute retries this many times for a node rejoining after a database blip.
	admissionRejoinAttempts int
	admissionRejoinBackoff  func(context.Context, int) error
	memoryMu                sync.Mutex
	memoryObjects           map[string]objectCheckpoint
	memoryCursors           map[string]prefixCursor
}

// SetNodeAdmission connects the transition to the API process's storage
// admission lock. Production API processes install it before opening storage.
func (s *Service) SetNodeAdmission(admission *pglock.NodeAdmission) {
	s.nodeAdmission = admission
}

func New(pool *pgxpool.Pool, settings Settings, jobs JobRepository, source, private blobstore.Store) *Service {
	service := &Service{pool: pool, settings: settings, jobs: jobs, source: source, private: private, commitVerifyAttempts: 5, progressInterval: 2 * time.Second, probeTimeout: 5 * time.Second, receiptFlushBytes: 256 << 20, receiptFlushInterval: 10 * time.Second, copyWorkers: 8, smallObjectBytes: 8 << 20, admissionRejoinAttempts: 10, memoryObjects: map[string]objectCheckpoint{}, memoryCursors: map[string]prefixCursor{}}
	service.openPublic = openTarget
	service.openPrivate = openPrivateTarget
	service.commitVerifyBackoff = func(ctx context.Context, attempt int) error {
		timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	service.admissionRejoinBackoff = func(ctx context.Context, _ int) error {
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	service.postRestartBackoff = func(ctx context.Context, attempt int) error {
		delay := time.Second << min(attempt, 5)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	service.reconcileResumable = func(ctx context.Context, target blobstore.Store, checkpoint *metadata.ArtworkReconcileCheckpoint, save func(metadata.ArtworkReconcileCheckpoint) error, progress func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.NewArtworkCacheReconciler(pool, target).RunResumable(ctx, checkpoint, save, progress)
	}
	return service
}

// SetBrandingReconciler wires the cheap post-transition branding reference
// check without coupling this package to the branding service implementation.
func (s *Service) SetBrandingReconciler(reconcile func(context.Context) (int, int, error)) {
	s.brandingReconcile = reconcile
}

func (s *Service) SourceHealth(ctx context.Context, probe bool) (SourceHealth, error) {
	if s == nil || s.settings == nil || s.source == nil {
		return SourceHealth{}, errors.New("storage transition source health is unavailable")
	}
	current, err := s.settings.GetAll(ctx)
	if err != nil {
		if _, _, stageErr := s.committedStage(ctx); isUnreadableCommittedStage(stageErr) {
			return blockedRecoveryHealth(SourceHealth{}, stageErr), nil
		}
		return SourceHealth{}, err
	}
	effective := config.EffectiveAdminSettings(current)
	health := sourceHealthConfigured(effective)
	if probe {
		health = s.probedSourceHealth(ctx, effective)
	}
	staged, pending, stageErr := s.committedStage(ctx)
	if isUnreadableCommittedStage(stageErr) {
		return blockedRecoveryHealth(health, stageErr), nil
	}
	if stageErr != nil {
		return SourceHealth{}, stageErr
	}
	if pending && (staged.PublicReconcile || staged.BrandingReconcile) {
		health.RecoveryPending = true
		health.RecoveryState = staged.RecoveryState
		if health.RecoveryState == "" {
			health.RecoveryState = recoveryStatePending
		}
		health.RecoveryError = staged.LastError
		health.RecoveryProgress = staged.RecoveryProgress
		health.RecoveryMessage = staged.RecoveryMessage
	}
	return health, nil
}

func isUnreadableCommittedStage(err error) bool {
	return errors.Is(err, errCommittedStageUnreadable) || errors.Is(err, errCommittedStageInvalid)
}

func blockedRecoveryHealth(health SourceHealth, err error) SourceHealth {
	health.RecoveryPending = true
	health.RecoveryState = recoveryStateBlocked
	health.RecoveryMessage = "Recovery is blocked"
	if errors.Is(err, errCommittedStageInvalid) {
		health.RecoveryError = "Committed storage transition state is invalid; restore the staged setting from backup or contact support."
	} else {
		health.RecoveryError = "Committed storage transition state could not be read or decrypted; restore access to the setting or contact support."
	}
	return health
}

func (s *Service) probedSourceHealth(ctx context.Context, current map[string]string) SourceHealth {
	probeCtx := ctx
	cancel := func() {}
	if s.probeTimeout > 0 {
		probeCtx, cancel = context.WithTimeout(ctx, s.probeTimeout)
	}
	defer cancel()
	return s.sourceHealth(probeCtx, current)
}

func (s *Service) sourceHealth(ctx context.Context, current map[string]string) SourceHealth {
	health := sourceHealthConfigured(current)
	health.ReachabilityProbed = true

	var publicErr, privateErr error
	var wg sync.WaitGroup
	if health.PublicConfigured {
		wg.Add(1)
		go func() {
			defer wg.Done()
			publicErr = s.source.Probe(ctx)
		}()
	}
	if health.PrivateConfigured {
		if s.private == nil {
			privateErr = errors.New("private S3 store is not configured")
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				privateErr = s.private.Probe(ctx)
			}()
		}
	}
	wg.Wait()

	health.PublicReachable = publicErr == nil
	health.PrivateReachable = privateErr == nil
	health.Reachable = health.PublicReachable && health.PrivateReachable
	switch {
	case !health.PublicReachable && !health.PrivateReachable:
		health.Message = "Current public and private S3 storage are unreachable."
	case !health.PublicReachable:
		health.Message = "Current public S3 storage is unreachable."
	case !health.PrivateReachable:
		health.Message = "Current private S3 storage is unreachable."
	case health.PublicConfigured || health.PrivateConfigured:
		health.Message = "Current S3 storage is reachable."
	default:
		health.Message = "The current storage does not use S3."
	}
	return health
}

func sourceHealthConfigured(current map[string]string) SourceHealth {
	health := SourceHealth{CurrentBackend: resolvedBackend(current)}
	health.PublicConfigured = health.CurrentBackend == blobstore.BackendS3
	health.PrivateConfigured = strings.TrimSpace(current[settingPrivateBucket]) != ""
	health.Message = "Source reachability was not probed."
	return health
}

func (s *Service) Start(ctx context.Context, userID int, req StartRequest) (*models.AdminJob, Preflight, error) {
	if s == nil || s.settings == nil || s.jobs == nil || s.source == nil {
		return nil, Preflight{}, errors.New("storage transition is unavailable")
	}
	if !validPolicy(req.Policy) {
		return nil, Preflight{}, validationErrorf("unknown migration policy %q", req.Policy)
	}
	// Stage selection and job admission must have one owner. The job index
	// alone cannot prevent a losing request from changing the winner's stage.
	if !s.admissionMu.TryLock() {
		return nil, Preflight{}, &adminjob.ActiveJobConflictError{}
	}
	defer s.admissionMu.Unlock()
	if s.pool != nil {
		lock, acquired, err := pglock.TryAcquire(ctx, s.pool, storageTransitionAdmissionLockKey)
		if err != nil {
			return nil, Preflight{}, fmt.Errorf("acquire storage transition admission lock: %w", err)
		}
		if !acquired {
			return nil, Preflight{}, &adminjob.ActiveJobConflictError{}
		}
		defer func() {
			if err := lock.Release(context.Background()); err != nil {
				slog.WarnContext(ctx, "storage transition admission lock release failed", "error", err)
			}
		}()
	}
	nodeExclusiveHeld := false
	if s.nodeAdmission != nil {
		acquired, err := s.nodeAdmission.TryExclusive(ctx)
		if err != nil {
			return nil, Preflight{}, fmt.Errorf("check storage node admission: %w", err)
		}
		if !acquired {
			return nil, Preflight{}, NewValidationError(errors.New("storage transitions require a maintenance window with only one write-capable API node"))
		}
		nodeExclusiveHeld = true
		defer func() {
			if !nodeExclusiveHeld {
				return
			}
			if err := s.nodeAdmission.ReleaseExclusive(context.Background()); err != nil {
				slog.ErrorContext(ctx, "release storage node admission after Start", "error", err)
			}
		}()
	}
	if _, _, err := s.committedStage(ctx); err != nil {
		return nil, Preflight{}, fmt.Errorf("inspect existing storage transition: %w", err)
	}
	activeJob, err := s.jobs.GetActiveByType(ctx, adminjob.JobTypeStorageTransition)
	if err == nil {
		return nil, Preflight{}, &adminjob.ActiveJobConflictError{Job: activeJob}
	}
	if !errors.Is(err, adminjob.ErrJobNotFound) {
		return nil, Preflight{}, fmt.Errorf("check active storage transition: %w", err)
	}
	if s.pool != nil {
		var activeNodes int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM node_heartbeats WHERE updated_at > now() - interval '2 minutes'`).Scan(&activeNodes); err != nil {
			return nil, Preflight{}, fmt.Errorf("check active Silo nodes: %w", err)
		}
		if activeNodes > 1 {
			return nil, Preflight{}, NewValidationError(errors.New("storage transitions require a maintenance window with only one active Silo node"))
		}
		var activeJobs int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM admin_jobs WHERE status IN ('queued','running') AND job_type <> $1`, adminjob.JobTypeStorageTransition).Scan(&activeJobs); err != nil {
			return nil, Preflight{}, fmt.Errorf("check active background jobs: %w", err)
		}
		if activeJobs > 0 {
			return nil, Preflight{}, NewValidationError(errors.New("storage transitions require all other background jobs to finish or be canceled"))
		}
	}
	current, err := s.settings.GetAll(ctx)
	if err != nil {
		return nil, Preflight{}, err
	}
	effectiveCurrent := config.EffectiveAdminSettings(current)
	// Start from canonical keys: fold the legacy s3.operational_* aliases into
	// the keys they fill and drop them. Otherwise a key the request clears
	// would be refilled from its alias, and an auto backend whose public
	// bucket exists only as an alias would resolve to local.
	target := clone(current)
	for _, key := range storageKeys() {
		target[key] = effectiveCurrent[key]
	}
	for _, key := range legacyOperationalKeys {
		target[key] = ""
	}
	for key, value := range req.Values {
		if !isStorageKey(key) {
			return nil, Preflight{}, validationErrorf("setting %q is not part of a storage transition", key)
		}
		// Apply the same per-key rules as the settings API, so a transition can
		// never commit a value the server cannot boot with, such as an unknown
		// backend or a relative local path.
		normalized, err := config.NormalizeAdminSetting(key, value)
		if err != nil {
			return nil, Preflight{}, NewValidationError(err)
		}
		target[key] = normalized
	}
	backend := resolvedBackend(target)
	if backend == blobstore.BackendLocal {
		target[settingArtworkBackend] = blobstore.BackendLocal
		// A local install keeps its private bucket and any saved public S3
		// settings unless the request changes them. Disabling S3 selects local
		// disk for assets and operational data; the policy determines which
		// source blobs are copied there.
		if resolvedBackend(effectiveCurrent) != blobstore.BackendLocal {
			for _, key := range storageKeys()[2:] {
				target[key] = ""
			}
		}
	}
	if requested, ok := req.Values[settingPrivateBucket]; ok && strings.TrimSpace(requested) == "" {
		// No bucket means no private storage. Clear the rest of the private
		// location with it, so a kept endpoint or prefix cannot leave it half
		// configured.
		for _, key := range privateStorageKeys {
			target[key] = ""
		}
	}
	// Validate the complete storage target without letting unrelated saved
	// settings or bootstrap capabilities block a storage transition.
	if err := config.ValidateAdminSettings(selectStorageValues(target)); err != nil {
		return nil, Preflight{}, NewValidationError(err)
	}
	effectiveTarget := config.EffectiveAdminSettings(target)
	targetAssets, targetPrivate, err := s.targetIdentities(selectStorageValues(effectiveTarget))
	if err != nil {
		return nil, Preflight{}, NewValidationError(err)
	}
	if targetAssets == s.source.Identity() && targetPrivate == storeIdentity(s.private) {
		return nil, Preflight{}, NewValidationError(errors.New("target storage is the same as active storage"))
	}
	currentLocation := locationOf(s.source.Identity(), storeIdentity(s.private))
	targetLocation := locationOf(targetAssets, targetPrivate)
	if req.Policy != PolicyFresh && currentLocation.operational != "" && targetLocation.operational == "" {
		return nil, Preflight{}, NewValidationError(errPrivateBucketRequired)
	}
	if storageNamespacesOverlap(targetAssets, targetPrivate) {
		return nil, Preflight{}, NewValidationError(errors.New("target public and private storage locations overlap"))
	}
	if req.Policy == PolicyFresh && (targetAssets == s.source.Identity() || targetPrivate == storeIdentity(s.private)) && len(namespaceProbes(targetAssets, targetPrivate)) != 0 {
		return nil, Preflight{}, NewValidationError(errUnverifiedTargetNamespace)
	}
	if req.Policy != PolicyFresh {
		health := s.probedSourceHealth(ctx, effectiveCurrent)
		if !health.PublicReachable {
			return nil, Preflight{}, &SourceUnavailableError{Scope: storageRolePublic, Err: errors.New(health.Message)}
		}
		if !health.PrivateReachable {
			return nil, Preflight{}, &SourceUnavailableError{Scope: storageRolePrivate, Err: errors.New(health.Message)}
		}
	}
	selectedTarget := selectStorageValues(effectiveTarget)
	transition := stagedTarget{
		ID:                  uuid.NewString(),
		Policy:              req.Policy,
		SourceIdentity:      s.source.Identity(),
		SourcePrivateBucket: strings.TrimSpace(effectiveCurrent[settingPrivateBucket]),
		TargetPrivateBucket: strings.TrimSpace(effectiveTarget[settingPrivateBucket]),
		Phase:               transitionPhaseStaged,
		Values:              selectedTarget,
		Baseline:            selectStorageValues(effectiveCurrent),
	}
	encoded, err := json.Marshal(transition)
	if err != nil {
		return nil, Preflight{}, err
	}
	replacedStageID := ""
	if err := s.settings.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		if raw := strings.TrimSpace(current[StagedTargetSettingKey]); raw != "" {
			var existing stagedTarget
			if err := json.Unmarshal([]byte(raw), &existing); err != nil {
				return nil, fmt.Errorf("decode existing storage transition: %w", err)
			}
			if existing.Phase == transitionPhaseRestartPending {
				if existing.PublicReconcile || existing.BrandingReconcile {
					switch existing.RecoveryState {
					case recoveryStateRunning:
						return nil, NewValidationError(errors.New("a committed storage transition is still reconciling after restart"))
					case recoveryStateWaitingRetry:
						return nil, NewValidationError(errors.New("a committed storage transition is waiting to retry post-restart reconciliation"))
					case recoveryStateBlocked:
						return nil, NewValidationError(errors.New("a committed storage transition has blocked post-restart reconciliation; inspect storage recovery status"))
					default:
						return nil, NewValidationError(errors.New("a committed storage transition has post-restart reconciliation pending"))
					}
				}
				return nil, NewValidationError(errors.New("a committed storage transition is awaiting restart"))
			}
			sameTransition := existing.Policy == req.Policy && existing.SourceIdentity == s.source.Identity() && equalValues(existing.Values, selectedTarget)
			if !sameTransition && existing.Phase != transitionPhaseFailed {
				return nil, NewValidationError(errors.New("another storage transition is staged; retry it with the same target and policy or restart after a committed transition"))
			}
			if !sameTransition {
				// A failed preflight must not permanently pin an invalid endpoint or
				// credential draft. Replace it with a fresh transition while retaining
				// the old storage itself; stale copy checkpoints are removed below.
				replacedStageID = existing.ID
				return map[string]string{StagedTargetSettingKey: string(encoded)}, nil
			}
			transition = existing
			transition.Phase = transitionPhaseStaged
			transition.LastError = ""
			encoded, err = json.Marshal(transition)
			if err != nil {
				return nil, err
			}
		}
		return map[string]string{StagedTargetSettingKey: string(encoded)}, nil
	}); err != nil {
		return nil, Preflight{}, err
	}
	if replacedStageID != "" && s.pool != nil {
		if _, err := s.pool.Exec(ctx, `DELETE FROM storage_transition_checkpoints WHERE transition_id=$1`, replacedStageID); err != nil {
			return nil, Preflight{}, fmt.Errorf("clear replaced storage transition checkpoints: %w", err)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM storage_transition_cursors WHERE transition_id=$1`, replacedStageID); err != nil {
			return nil, Preflight{}, fmt.Errorf("clear replaced storage transition cursors: %w", err)
		}
	}
	// The queued job has not copied anything yet. Release exclusivity before
	// publishing the job, so a worker cannot observe our own temporary lock as
	// a competing node. Execute reacquires it before touching either store. A
	// node that joins in between makes Execute fail safely.
	if nodeExclusiveHeld {
		if err := s.nodeAdmission.ReleaseExclusive(context.Background()); err != nil {
			return nil, Preflight{}, fmt.Errorf("release storage node admission before queuing: %w", err)
		}
		nodeExclusiveHeld = false
	}
	job, err := s.jobs.Create(ctx, adminjob.CreateJobInput{JobType: adminjob.JobTypeStorageTransition, CreatedByUserID: userID, RequestPayload: adminjob.StorageTransitionRequest{TransitionID: transition.ID, Policy: req.Policy}, Message: "Queued storage transition"})
	if err != nil {
		// An INSERT may have committed even when its response was lost. Retain
		// the stage unless a separate read confirms that no job was admitted.
		// Admission remains locked while making an unclaimed stage replaceable.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, lookupErr := s.jobs.GetActiveByType(cleanupCtx, adminjob.JobTypeStorageTransition); errors.Is(lookupErr, adminjob.ErrJobNotFound) {
			if stageErr := s.recordStageFailure(cleanupCtx, transition.ID, err); stageErr != nil {
				slog.WarnContext(ctx, "storage transition admission failure could not be recorded", "error", stageErr)
			}
		}
		return nil, Preflight{}, err
	}
	return job, describe(currentLocation, targetLocation, req.Policy), nil
}

// tryExclusiveAdmission waits a few seconds for a node that is rejoining after
// a database blip. The queued job can be claimed as soon as the database
// answers, up to one admission probe before the node has rejoined.
func (s *Service) tryExclusiveAdmission(ctx context.Context) (bool, error) {
	for attempt := 0; ; attempt++ {
		acquired, err := s.nodeAdmission.TryExclusive(ctx)
		if !errors.Is(err, pglock.ErrAdmissionRejoining) || attempt >= s.admissionRejoinAttempts || s.admissionRejoinBackoff == nil {
			return acquired, err
		}
		if err := s.admissionRejoinBackoff(ctx, attempt); err != nil {
			return false, err
		}
	}
}

func (s *Service) ExecuteStorageTransition(ctx context.Context, req adminjob.StorageTransitionRequest, progress func(adminjob.StorageTransitionProgress)) (resultValue any, resultErr error) {
	phase := "preparing"
	report := func(current, total int, message string) {
		progress(adminjob.StorageTransitionProgress{Current: current, Total: total, Phase: phase, Message: message})
	}
	defer func() {
		if resultErr != nil {
			_ = s.recordStageFailure(context.Background(), req.TransitionID, resultErr)
		}
	}()
	if s.nodeAdmission != nil {
		acquired, err := s.tryExclusiveAdmission(ctx)
		if err != nil {
			return nil, fmt.Errorf("acquire exclusive storage node admission: %w", err)
		}
		if !acquired {
			return nil, NewValidationError(errors.New("another write-capable API node is active; storage transition cannot copy safely"))
		}
		copyCtx, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-s.nodeAdmission.Lost():
				cancel()
			case <-copyCtx.Done():
			}
		}()
		defer cancel()
		ctx = copyCtx
		committed := false
		defer func() {
			if committed {
				return
			}
			if err := s.nodeAdmission.ReleaseExclusive(context.Background()); err != nil {
				slog.ErrorContext(ctx, "release storage node admission after failed transition", "error", err)
			}
		}()
		// Retain exclusive ownership after a successful or uncertain commit;
		// the old process and its source fences remain alive until restart.
		defer func() {
			if resultErr == nil {
				committed = true
			}
		}()
	}
	if !validPolicy(req.Policy) {
		return nil, fmt.Errorf("unknown migration policy %q", req.Policy)
	}
	raw, err := s.settings.Get(ctx, StagedTargetSettingKey)
	if err != nil {
		return nil, fmt.Errorf("read staged storage target: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("no storage target is staged")
	}
	var staged stagedTarget
	if err := json.Unmarshal([]byte(raw), &staged); err != nil {
		return nil, fmt.Errorf("decode staged storage target: %w", err)
	}
	if staged.ID == "" {
		// Compatibility for a transition staged by an earlier build.
		staged.ID = req.TransitionID
		if staged.ID == "" {
			staged.ID = uuid.NewString()
		}
		if err := s.updateStage(ctx, "", func(state *stagedTarget) {
			state.ID = staged.ID
			state.Policy = req.Policy
			state.SourceIdentity = s.source.Identity()
			state.Phase = transitionPhaseStaged
		}); err != nil {
			return nil, err
		}
	}
	if req.TransitionID == "" {
		req.TransitionID = staged.ID
	}
	if req.TransitionID == "" || staged.ID != req.TransitionID {
		return nil, errors.New("storage transition request does not match the staged target")
	}
	if staged.Policy != "" && staged.Policy != req.Policy {
		return nil, errors.New("storage transition policy does not match the staged target")
	}
	if staged.SourceIdentity != "" && staged.SourceIdentity != s.source.Identity() {
		return nil, errors.New("active source storage changed after the transition was staged")
	}
	if staged.Phase == transitionPhaseRestartPending {
		// The runner settles the receipt instead of recording a failure.
		return nil, fmt.Errorf("%w; awaiting restart or recovery", adminjob.ErrStorageTransitionAlreadyCommitted)
	}
	if err := s.updateStage(ctx, staged.ID, func(state *stagedTarget) {
		state.Phase = transitionPhaseCopying
		state.LastError = ""
	}); err != nil {
		return nil, err
	}
	target, err := s.openPublic(staged.Values)
	if err != nil {
		return nil, err
	}
	targetPrivate := s.openPrivate(staged.Values)
	publicChanged := target.Identity() != s.source.Identity()
	privateChanged := storeIdentity(s.private) != storeIdentity(targetPrivate)
	if !publicChanged && !privateChanged {
		return nil, errors.New("target storage is the same as active storage")
	}
	var sourcePrivateIsPublic bool
	if req.Policy != PolicyFresh && operationalStore(s.source, s.private) != nil && operationalStore(target, targetPrivate) == nil {
		return nil, errPrivateBucketRequired
	}
	phase = "checking_target"
	report(0, 0, "Checking target storage")
	if publicChanged {
		if err := target.Probe(ctx); err != nil {
			return nil, fmt.Errorf("target storage is unavailable: %w", err)
		}
	}
	result := Result{Policy: req.Policy, SourceIdentity: s.source.Identity(), TargetIdentity: target.Identity(), RestartRequired: true, OldStorageRetained: true}
	if targetPrivate != nil && privateChanged {
		if err := targetPrivate.Probe(ctx); err != nil {
			return nil, fmt.Errorf("target private storage is unavailable: %w", err)
		}
	}
	if req.Policy != PolicyFresh {
		if publicChanged {
			if err := ensureNamespacesDistinct(ctx, s.source, target, "source and target public storage locations overlap"); err != nil {
				return nil, err
			}
			if err := ensureNamespacesDistinct(ctx, s.private, target, "source private and target public storage locations overlap"); err != nil {
				return nil, err
			}
		}
		if privateChanged && targetPrivate != nil {
			if err := ensureNamespacesDistinct(ctx, s.private, targetPrivate, "source and target private storage locations overlap"); err != nil {
				return nil, err
			}
			if err := ensureNamespacesDistinct(ctx, s.source, targetPrivate, "source public and target private storage locations overlap"); err != nil {
				return nil, err
			}
		}
		// Resolve source aliases before copying or fencing writes. Both copy
		// passes use this result to keep nested private data out of public storage.
		sourcePrivateIsPublic, err = namespacesOverlapObserved(ctx, s.source, s.private)
		if err != nil {
			return nil, fmt.Errorf("verify source storage namespaces: %w", err)
		}
	}
	if targetPrivate != nil {
		if req.Policy == PolicyFresh && (!publicChanged || !privateChanged) && !storageNamespacesOverlap(target.Identity(), targetPrivate.Identity()) && len(namespaceProbes(target.Identity(), targetPrivate.Identity())) != 0 {
			return nil, errUnverifiedTargetNamespace
		}
		if err := ensureNamespacesDistinct(ctx, target, targetPrivate, "target public and private storage locations overlap"); err != nil {
			return nil, err
		}
	}
	if publicChanged {
		if err := probeTargetStorage(ctx, target); err != nil {
			return nil, fmt.Errorf("target storage is unavailable: %w", err)
		}
	}
	if targetPrivate != nil && privateChanged {
		if err := probeTargetStorage(ctx, targetPrivate); err != nil {
			return nil, fmt.Errorf("target private storage is unavailable: %w", err)
		}
	}
	var sameRunListings map[string]objectListing
	if s.pool == nil {
		sameRunListings = make(map[string]objectListing)
	}
	copyRunID := uuid.NewString()
	if req.Policy != PolicyFresh {
		phase = "copying"
		report(0, 0, "Copying storage data")
		bulk, err := s.copyTransitionData(ctx, staged, req.Policy, target, targetPrivate, publicChanged, privateChanged, sourcePrivateIsPublic, copyRunID, sameRunListings, false, report)
		if err != nil {
			return nil, err
		}
		applyCopyPass(&result, bulk)
	}

	var releaseFences []func()
	fenceCommitted := false
	phase = "verifying"
	report(0, 0, "Verifying final storage changes")
	defer func() {
		if !fenceCommitted {
			for i := len(releaseFences) - 1; i >= 0; i-- {
				releaseFences[i]()
			}
		}
	}()
	// Fence only the sources this transition copies from: pausing a store
	// whose data stays put would block its writers until restart for nothing.
	// A local install shares one fenced root between the assets and
	// operational stores; fence it once, because the fence takes the whole
	// semaphore and a second acquire would never return.
	var fenced []blobstore.Store
	if publicChanged {
		fenced = append(fenced, s.source)
	}
	sourceOperational := operationalStore(s.source, s.private)
	if targetOperational := operationalStore(target, targetPrivate); sourceOperational != nil && targetOperational != nil && sourceOperational.Identity() != targetOperational.Identity() {
		fenced = append(fenced, sourceOperational)
	}
	for _, store := range distinctStores(fenced...) {
		if fencer, ok := store.(blobstore.MutationFencer); ok {
			report(0, 0, "Pausing storage writes for final verification")
			release, err := fencer.BeginMutationFence(ctx)
			if err != nil {
				return nil, fmt.Errorf("pause storage writes: %w", err)
			}
			releaseFences = append(releaseFences, release)
		}
	}
	if req.Policy != PolicyFresh {
		finalPass, err := s.copyTransitionData(ctx, staged, req.Policy, target, targetPrivate, publicChanged, privateChanged, sourcePrivateIsPublic, copyRunID, sameRunListings, true, report)
		if err != nil {
			return nil, err
		}
		applyCopyPass(&result, finalPass)
	}
	staged.SkippedObjects = result.SkippedObjects
	staged.PublicReconcile = publicChanged && (req.Policy != PolicyMigrateAll || result.SkippedObjects != 0)
	staged.BrandingReconcile = publicChanged
	commitUnknown := false
	privateIdentity := ""
	if privateChanged {
		privateIdentity = storeIdentity(targetPrivate)
	}
	// Removing a private bucket can populate the unchanged local root with
	// operational objects. Once the final pass verifies them, that root needs
	// the same identity guard as a root populated by an asset write.
	recordLocalRoot := !publicChanged && privateChanged && targetPrivate == nil && req.Policy != PolicyFresh && result.CopiedObjects > 0
	phase = "committing"
	report(result.CopiedObjects, 0, "Committing verified storage transition")
	if err := s.commit(ctx, staged, target.Identity(), privateChanged, privateIdentity, recordLocalRoot); err != nil {
		committed, known := s.verifyCommitOutcome(staged.ID)
		if known && !committed {
			return nil, err
		}
		if !known {
			commitUnknown = true
			result.CommitUnknown = true
			phase = transitionPhaseRestartPending
			report(result.CopiedObjects, result.CopiedObjects, "Storage commit outcome is unknown; restart required to recover safely")
		}
	}
	// Keep both source fences held after commit. The process restarts immediately,
	// and releasing them here would reopen a window for writes to land in the old
	// stores after their final copy but before shutdown.
	fenceCommitted = true
	if !commitUnknown {
		phase = transitionPhaseRestartPending
		report(result.CopiedObjects, result.CopiedObjects, "Storage transition committed; restart required")
	}
	result.Phase = transitionPhaseRestartPending
	result.VerifiedObjects = result.CopiedObjects
	return result, nil
}

type copyPass struct {
	objects int
	bytes   int64
	skipped []string
}

func applyCopyPass(result *Result, pass copyPass) {
	result.CopiedObjects = pass.objects
	result.CopiedBytes = pass.bytes
	result.SkippedObjects = len(pass.skipped)
	result.SkippedKeys = append(result.SkippedKeys[:0], pass.skipped...)
}

func (s *Service) copyTransitionData(ctx context.Context, staged stagedTarget, policy string, target, targetPrivate blobstore.Store, publicChanged, privateChanged, sourcePrivateIsPublic bool, runID string, sameRunListings map[string]objectListing, finalPass bool, progress func(int, int, string)) (copyPass, error) {
	var pass copyPass
	if sourcePrivateIsPublic {
		// A public prefix nested inside a private operational namespace gives
		// private objects public logical keys. For example, a public prefix of
		// diagnostics makes diagnostics/1/report.zip appear as 1/report.zip;
		// neither role can classify that object safely for a copy.
		if publicPrefix, nested := nestedS3Prefix(storeIdentity(s.private), s.source.Identity()); nested && publicPrefix != "" {
			for _, operationalPrefix := range operationalPrefixes {
				if keyPrefixContains(operationalPrefix, publicPrefix) || keyPrefixContains(publicPrefix, operationalPrefix) {
					return pass, validationErrorf("public source prefix %q overlaps private operational namespace %q", publicPrefix, operationalPrefix)
				}
			}
		}
	}
	copyScope := func(scope string, source, destination blobstore.Store, prefix string, excluded ...string) error {
		copied, bytes, skipped, err := s.copyPrefixPass(ctx, staged.ID, scope, source, destination, prefix, progress, pass.objects, runID, sameRunListings, finalPass, excluded...)
		if err != nil {
			return err
		}
		pass.objects += copied
		pass.bytes += bytes
		pass.skipped = append(pass.skipped, skipped...)
		return nil
	}
	if publicChanged {
		prefixes := []string{""}
		if policy == PolicyPreserveUploads {
			prefixes = append([]string(nil), preservedAssetPrefixes...)
		}
		for _, prefix := range prefixes {
			scope := "public:"
			if prefix != "" {
				scope += prefix
			}
			// Operational blobs follow the operational store, never the public
			// tree. A local root and a legacy shared bucket hold both under one
			// namespace, including when private storage is nested under a prefix.
			excluded := append([]string(nil), operationalPrefixes...)
			if sourcePrivateIsPublic {
				if privatePrefix, nested := nestedS3Prefix(s.source.Identity(), storeIdentity(s.private)); nested && privatePrefix != "" {
					excluded = append(excluded, privatePrefix)
				}
			}
			if err := copyScope(scope, s.source, target, prefix, excluded...); err != nil {
				return pass, err
			}
		}
	}

	sourceOperational := operationalStore(s.source, s.private)
	targetOperational := operationalStore(target, targetPrivate)
	if sourceOperational == nil || targetOperational == nil || sourceOperational.Identity() == targetOperational.Identity() {
		return pass, nil
	}
	if err := copyScope("avatars:"+avatarPrefix, sourceOperational, targetOperational, avatarPrefix); err != nil {
		return pass, err
	}
	if policy != PolicyMigrateAll {
		return pass, nil
	}
	if sourceOperational != s.private || targetOperational != targetPrivate {
		// A local root holds artwork beside these blobs, so copy only the
		// operational namespaces into or out of it.
		for _, prefix := range operationalArtifactPrefixes {
			if err := copyScope("private:"+prefix, sourceOperational, targetOperational, prefix); err != nil {
				return pass, err
			}
		}
		return pass, nil
	}
	prefixes := []string{""}
	excluded := []string{avatarPrefix}
	if sourcePrivateIsPublic {
		// Split a legacy shared namespace by ownership instead of copying
		// its public artwork into the private destination as well.
		if publicPrefix, nested := nestedS3Prefix(s.private.Identity(), s.source.Identity()); nested {
			if publicPrefix == "" {
				prefixes = append([]string(nil), operationalArtifactPrefixes...)
			} else {
				excluded = append(excluded, publicPrefix)
			}
		}
	}
	for _, prefix := range prefixes {
		if err := copyScope("private:"+prefix, s.private, targetPrivate, prefix, excluded...); err != nil {
			return pass, err
		}
	}
	return pass, nil
}

const avatarPrefix = "profile-avatars"

// operationalArtifactPrefixes are the operational blobs that carry a stored
// bucket reference: diagnostic bundles and admin job artifacts.
var operationalArtifactPrefixes = []string{"diagnostics", "catalog-seeds"}

// operationalPrefixes are every namespace the operational store owns.
var operationalPrefixes = append(append([]string(nil), operationalArtifactPrefixes...), avatarPrefix)

// preservedAssetPrefixes are the assets preserve_uploads copies: uploads that
// cannot be downloaded again from a provider.
var preservedAssetPrefixes = []string{"branding", "collection-images", "user-collection-images", "library-posters", "subtitles"}

// operationalStore mirrors blobstore.Open: a private bucket owns diagnostics,
// job artifacts, and avatars whatever the backend is, a local backend without
// one keeps them in its root, and an S3 backend without one has none.
func operationalStore(assets, private blobstore.Store) blobstore.Store {
	if private != nil {
		return private
	}
	if assets != nil && strings.HasPrefix(assets.Identity(), blobstore.BackendLocal+"|") {
		return assets
	}
	return nil
}

// operationalBucket is the bucket name diagnostics and job artifact rows record
// for an operational location, matching what their writers store.
func operationalBucket(assetsIdentity, privateBucket string) string {
	if privateBucket != "" {
		return privateBucket
	}
	if strings.HasPrefix(assetsIdentity, blobstore.BackendLocal+"|") {
		return blobstore.LocalBucket
	}
	return ""
}

func distinctStores(stores ...blobstore.Store) []blobstore.Store {
	out := make([]blobstore.Store, 0, len(stores))
	for _, store := range stores {
		if store == nil || slices.Contains(out, store) {
			continue
		}
		out = append(out, store)
	}
	return out
}

func openPrivateTarget(values map[string]string) blobstore.Store {
	if strings.TrimSpace(values[settingPrivateBucket]) == "" {
		return nil
	}
	pathStyle, _ := strconv.ParseBool(values["s3.private_path_style"])
	return blobstore.NewS3(s3client.NewClient(s3client.BucketConfig{Role: storageRolePrivate, Endpoint: values[settingPrivateEndpoint], Region: values["s3.private_region"], PathStyle: pathStyle, Bucket: values[settingPrivateBucket], KeyPrefix: values[settingPrivateKeyPrefix], AccessKey: values[settingPrivateAccessKey], SecretKey: values[settingPrivateSecretKey]}))
}

func storeIdentity(store blobstore.Store) string {
	if store == nil {
		return ""
	}
	return store.Identity()
}

func storageNamespacesOverlap(sourceIdentity, targetIdentity string) bool {
	if sourceIdentity == "" || targetIdentity == "" {
		return false
	}
	if strings.HasPrefix(sourceIdentity, blobstore.BackendLocal+"|") && strings.HasPrefix(targetIdentity, blobstore.BackendLocal+"|") {
		sourceRoot := filepath.Clean(strings.TrimPrefix(sourceIdentity, blobstore.BackendLocal+"|"))
		targetRoot := filepath.Clean(strings.TrimPrefix(targetIdentity, blobstore.BackendLocal+"|"))
		return pathContains(sourceRoot, targetRoot) || pathContains(targetRoot, sourceRoot)
	}
	if strings.HasPrefix(sourceIdentity, blobstore.BackendS3+"|") && strings.HasPrefix(targetIdentity, blobstore.BackendS3+"|") {
		source := strings.SplitN(sourceIdentity, "|", 4)
		target := strings.SplitN(targetIdentity, "|", 4)
		if len(source) != 4 || len(target) != 4 || source[1] != target[1] || source[2] != target[2] {
			return false
		}
		sourcePrefix := strings.Trim(source[3], "/")
		targetPrefix := strings.Trim(target[3], "/")
		return keyPrefixContains(sourcePrefix, targetPrefix) || keyPrefixContains(targetPrefix, sourcePrefix)
	}
	return false
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func keyPrefixContains(parent, child string) bool {
	return parent == "" || child == parent || strings.HasPrefix(child, parent+"/")
}

// nestedS3Prefix derives the relative prefix after namespace overlap has been
// verified. Endpoint strings may differ when the stores use aliases.
func nestedS3Prefix(parentIdentity, childIdentity string) (string, bool) {
	parent := strings.SplitN(parentIdentity, "|", 4)
	child := strings.SplitN(childIdentity, "|", 4)
	if len(parent) != 4 || len(child) != 4 || parent[0] != blobstore.BackendS3 || child[0] != blobstore.BackendS3 || parent[2] != child[2] {
		return "", false
	}
	parentPrefix, childPrefix := strings.Trim(parent[3], "/"), strings.Trim(child[3], "/")
	if !keyPrefixContains(parentPrefix, childPrefix) {
		return "", false
	}
	return strings.TrimPrefix(strings.TrimPrefix(childPrefix, parentPrefix), "/"), true
}

type namespaceProbe struct {
	targetKey string
	sourceKey string
}

func ensureNamespacesDistinct(ctx context.Context, source, target blobstore.Store, message string) error {
	overlap, err := namespacesOverlapObserved(ctx, source, target)
	if err != nil {
		return fmt.Errorf("verify distinct storage namespaces: %w", err)
	}
	if overlap {
		return errors.New(message)
	}
	return nil
}

func namespacesOverlapObserved(ctx context.Context, source, target blobstore.Store) (bool, error) {
	if source == nil || target == nil {
		return false, nil
	}
	if storageNamespacesOverlap(source.Identity(), target.Identity()) {
		return true, nil
	}
	for _, probe := range namespaceProbes(source.Identity(), target.Identity()) {
		visible, err := runNamespaceProbe(ctx, source, target, probe)
		if err != nil {
			return false, err
		}
		if visible {
			return true, nil
		}
	}
	return false, nil
}

func namespaceProbes(sourceIdentity, targetIdentity string) []namespaceProbe {
	source := strings.SplitN(sourceIdentity, "|", 4)
	target := strings.SplitN(targetIdentity, "|", 4)
	if len(source) != 4 || len(target) != 4 || source[0] != blobstore.BackendS3 || target[0] != blobstore.BackendS3 || source[2] != target[2] {
		return nil
	}
	sourcePrefix := strings.Trim(source[3], "/")
	targetPrefix := strings.Trim(target[3], "/")
	sentinel := path.Join("storage-transition-probe", uuid.NewString())
	switch {
	case sourcePrefix == targetPrefix:
		return []namespaceProbe{{targetKey: sentinel, sourceKey: sentinel}}
	case keyPrefixContains(sourcePrefix, targetPrefix):
		relative := strings.TrimPrefix(strings.TrimPrefix(targetPrefix, sourcePrefix), "/")
		return []namespaceProbe{{targetKey: sentinel, sourceKey: path.Join(relative, sentinel)}}
	case keyPrefixContains(targetPrefix, sourcePrefix):
		relative := strings.TrimPrefix(strings.TrimPrefix(sourcePrefix, targetPrefix), "/")
		return []namespaceProbe{{targetKey: path.Join(relative, sentinel), sourceKey: sentinel}}
	default:
		return nil
	}
}

func runNamespaceProbe(ctx context.Context, source, target blobstore.Store, probe namespaceProbe) (visible bool, resultErr error) {
	if err := target.Put(ctx, probe.targetKey, []byte("silo storage namespace probe")); err != nil {
		return false, fmt.Errorf("write target sentinel: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		deleted, cleanupErr := target.Delete(cleanupCtx, []string{probe.targetKey})
		if cleanupErr == nil && deleted != 1 {
			cleanupErr = fmt.Errorf("deleted %d of 1 target sentinels", deleted)
		}
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("target sentinel cleanup failed; delete permission is required: %w", cleanupErr))
		}
	}()
	if _, err := source.Stat(ctx, probe.sourceKey); err == nil {
		return true, nil
	} else if !errors.Is(err, blobstore.ErrNotFound) {
		return false, fmt.Errorf("read source sentinel: %w", err)
	}
	return false, nil
}

// probeTargetStorage verifies that a destination can round-trip and remove a
// small object before settings point at it. The cleanup uses a detached bounded
// context because a timed-out write may still have reached the destination.
func probeTargetStorage(ctx context.Context, target blobstore.Store) (resultErr error) {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	key := path.Join("storage-transition-probe", uuid.NewString())
	content := []byte("silo storage destination probe")
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cleanupCancel()
		deleted, err := target.Delete(cleanupCtx, []string{key})
		if err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("delete target probe: %w", err))
		} else if deleted != 1 {
			resultErr = errors.Join(resultErr, fmt.Errorf("delete target probe: removed %d of 1 objects", deleted))
		}
	}()
	if err := target.Put(probeCtx, key, content); err != nil {
		return fmt.Errorf("write target probe: %w", err)
	}
	reader, info, err := target.Get(probeCtx, key)
	if err != nil {
		return fmt.Errorf("read target probe: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, int64(len(content)+1)))
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return fmt.Errorf("read target probe: %w", err)
	}
	if info.Size != int64(len(content)) || string(data) != string(content) {
		return errors.New("target probe returned different content")
	}
	return nil
}

func (s *Service) copyPrefix(ctx context.Context, transitionID, scope string, source, target blobstore.Store, prefix string, progress func(int, int, string), offset int, excludedPrefixes ...string) (int, int64, []string, error) {
	return s.copyPrefixPass(ctx, transitionID, scope, source, target, prefix, progress, offset, uuid.NewString(), nil, false, excludedPrefixes...)
}

func (s *Service) copyPrefixPass(ctx context.Context, transitionID, scope string, source, target blobstore.Store, prefix string, progress func(int, int, string), offset int, runID string, sameRunListings map[string]objectListing, finalPass bool, excludedPrefixes ...string) (int, int64, []string, error) {
	state, err := s.loadCursor(ctx, transitionID, scope)
	if err != nil {
		return 0, 0, nil, err
	}
	if state.Cursor != "" || state.Objects != 0 || state.Bytes != 0 || state.Completed {
		// A previous execution released its mutation fence before this retry.
		// Re-enumerate from the beginning so objects added or replaced before the
		// saved cursor cannot be omitted. Per-object checkpoints still avoid
		// rewriting data whose source and target digests both remain unchanged.
		state = prefixCursor{}
		if err := s.saveCursor(ctx, transitionID, scope, state); err != nil {
			return 0, 0, nil, err
		}
	}
	cursor, copied, bytes := state.Cursor, state.Objects, state.Bytes
	var skipped []string
	var memorySeen map[string]struct{}
	if s.pool == nil && finalPass {
		memorySeen = make(map[string]struct{})
	}
	for {
		objects, next, err := source.List(ctx, prefix, cursor, 250)
		if err != nil {
			return copied, bytes, skipped, fmt.Errorf("list source storage: %w", err)
		}
		validKeys := make([]string, 0, len(objects))
		for _, object := range objects {
			if hasStoragePrefix(object.Key, excludedPrefixes) {
				continue
			}
			if validateErr := blobstore.ValidateKey(object.Key); validateErr == nil {
				validKeys = append(validKeys, object.Key)
			}
		}
		checkpoints, err := s.loadCheckpointPage(ctx, transitionID, scope, validKeys)
		if err != nil {
			return copied, bytes, skipped, err
		}
		pageReceipts := make(map[string]objectCheckpoint, len(validKeys))
		var pendingReceiptBytes int64
		lastReceiptFlush := time.Now()
		flushReceipts := func(flushCtx context.Context) error {
			if len(pageReceipts) == 0 {
				return nil
			}
			if err := s.saveCheckpointPage(flushCtx, transitionID, scope, runID, pageReceipts, finalPass); err != nil {
				return err
			}
			clear(pageReceipts)
			pendingReceiptBytes = 0
			lastReceiptFlush = time.Now()
			return nil
		}
		failPage := func(cause error) (int, int64, []string, error) {
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			return copied, bytes, skipped, errors.Join(cause, flushReceipts(flushCtx))
		}
		recordReceipt := func(key string, checkpoint objectCheckpoint, size int64) error {
			pageReceipts[key] = checkpoint
			pendingReceiptBytes += max(size, 0)
			bytesDue := s.receiptFlushBytes > 0 && pendingReceiptBytes >= s.receiptFlushBytes
			timeDue := s.receiptFlushInterval > 0 && time.Since(lastReceiptFlush) >= s.receiptFlushInterval
			if bytesDue || timeDue {
				return flushReceipts(ctx)
			}
			return nil
		}
		// Filter the page in listing order, then copy it with a bounded pool.
		// Receipts, counters, and progress are updated under one lock, and the
		// page's receipts flush before the cursor advances, as before.
		work := make([]blobstore.ObjectInfo, 0, len(objects))
		for _, object := range objects {
			if hasStoragePrefix(object.Key, excludedPrefixes) {
				continue
			}
			if err := blobstore.ValidateKey(object.Key); errors.Is(err, blobstore.ErrInvalidKey) {
				skipped = append(skipped, object.Key)
				progress(offset+copied, 0, "Skipped invalid storage key "+object.Key)
				continue
			} else if err != nil {
				return failPage(fmt.Errorf("validate source key %q: %w", object.Key, err))
			}
			if memorySeen != nil {
				memorySeen[object.Key] = struct{}{}
			}
			work = append(work, object)
		}
		var mu sync.Mutex
		var lastProgress time.Time
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(max(s.copyWorkers, 1))
		for _, object := range work {
			checkpoint, found := checkpoints[object.Key]
			group.Go(func() error {
				if err := groupCtx.Err(); err != nil {
					return err
				}
				outcome, err := s.copyObject(groupCtx, transitionID, scope, source, target, object, checkpoint, found, runID, sameRunListings, finalPass)
				if err != nil {
					return err
				}
				if outcome.vanished {
					// The source can change before the bulk pass obtains the object.
					// The fenced pass will decide whether it was deleted or replaced.
					return nil
				}
				mu.Lock()
				defer mu.Unlock()
				copied++
				bytes += outcome.checkpoint.Size
				if outcome.rememberListing && sameRunListings != nil {
					sameRunListings[checkpointKey(transitionID, scope, object.Key)] = outcome.checkpoint.Listing
				}
				if err := recordReceipt(object.Key, outcome.checkpoint, outcome.checkpoint.Size); err != nil {
					return err
				}
				// Each progress call is a job-row write and a realtime event, so
				// report at most once per interval rather than once per object.
				if now := time.Now(); s.progressInterval <= 0 || now.Sub(lastProgress) >= s.progressInterval {
					lastProgress = now
					progress(offset+copied, 0, outcome.message+" "+object.Key)
				}
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return failPage(err)
		}
		progress(offset+copied, 0, fmt.Sprintf("Copied %d objects", copied))
		if err := flushReceipts(ctx); err != nil {
			return failPage(err)
		}
		if err := s.saveCursor(ctx, transitionID, scope, prefixCursor{Cursor: next, Objects: copied, Bytes: bytes, Completed: next == ""}); err != nil {
			return copied, bytes, skipped, err
		}
		if next == "" {
			if finalPass {
				if err := s.deleteCheckpointOrphans(ctx, transitionID, scope, runID, target, memorySeen); err != nil {
					return copied, bytes, skipped, err
				}
			}
			return copied, bytes, skipped, nil
		}
		cursor = next
	}
}

type copyOutcome struct {
	checkpoint      objectCheckpoint
	message         string
	rememberListing bool
	vanished        bool
}

// copyObject copies or verifies one object and returns its receipt. It holds
// no shared state, so a page's objects can run concurrently.
func (s *Service) copyObject(ctx context.Context, transitionID, scope string, source, target blobstore.Store, object blobstore.ObjectInfo, checkpoint objectCheckpoint, found bool, runID string, sameRunListings map[string]objectListing, finalPass bool) (copyOutcome, error) {
	listing := listingFromObject(object)
	if finalPass && found && listingShortcutReliable(source, listing) {
		var sameRunUnchanged bool
		if s.pool == nil {
			// Written only by the bulk pass; the fenced pass reads it.
			sameRunUnchanged = sameRunListingEqual(sameRunListings[checkpointKey(transitionID, scope, object.Key)], listing)
		} else {
			sameRunUnchanged = checkpoint.ListingRunID == runID && sameRunListingEqual(checkpoint.Listing, listing)
		}
		if sameRunUnchanged {
			return copyOutcome{checkpoint: checkpoint, message: "Verified unchanged"}, nil
		}
	}
	rememberListing := !finalPass && listing.reliable()
	if found && checkpoint.Size == object.Size {
		sourceDigest, sourceSize, err := objectDigest(ctx, source, object.Key)
		if err != nil {
			if !finalPass && errors.Is(err, blobstore.ErrNotFound) {
				return copyOutcome{vanished: true}, nil
			}
			return copyOutcome{}, fmt.Errorf("revalidate source checkpoint %q: %w", object.Key, err)
		}
		if sourceSize == checkpoint.Size && sourceDigest == checkpoint.SHA256 {
			if finalPass && checkpoint.ListingRunID == runID {
				// This run verified the target copy against this digest, and
				// only the transition writes there, so the unchanged source
				// needs no second read of the target.
				return copyOutcome{checkpoint: checkpoint, message: "Verified unchanged"}, nil
			}
			digest, size, verifyErr := objectDigest(ctx, target, object.Key)
			if verifyErr == nil && size == checkpoint.Size && digest == checkpoint.SHA256 {
				checkpoint.Listing = listing
				checkpoint.ListingRunID = runID
				return copyOutcome{checkpoint: checkpoint, message: "Verified existing", rememberListing: rememberListing}, nil
			}
			if verifyErr != nil && !errors.Is(verifyErr, blobstore.ErrNotFound) {
				return copyOutcome{}, fmt.Errorf("verify checkpoint %q: %w", object.Key, verifyErr)
			}
		}
	}
	reader, info, err := source.Get(ctx, object.Key)
	if err != nil {
		if !finalPass && errors.Is(err, blobstore.ErrNotFound) {
			return copyOutcome{vanished: true}, nil
		}
		return copyOutcome{}, fmt.Errorf("read %q: %w", object.Key, err)
	}
	hasher := sha256.New()
	copyReader := io.TeeReader(reader, hasher)
	if info.Size >= 0 && info.Size <= s.smallObjectBytes {
		// Put records the content checksum S3 compares in Matches, so the
		// image cache can reuse copied artwork instead of uploading it again.
		// The S3 uploader buffers a part this size anyway.
		var data []byte
		data, err = io.ReadAll(io.LimitReader(copyReader, s.smallObjectBytes+1))
		if err == nil {
			err = target.Put(ctx, object.Key, data)
		}
	} else {
		err = target.PutStream(ctx, object.Key, copyReader, "")
	}
	closeErr := reader.Close()
	if err != nil || closeErr != nil {
		return copyOutcome{}, fmt.Errorf("write %q: %w", object.Key, errors.Join(err, closeErr))
	}
	sourceDigest := fmt.Sprintf("%x", hasher.Sum(nil))
	targetDigest, targetSize, err := objectDigest(ctx, target, object.Key)
	if err != nil {
		return copyOutcome{}, fmt.Errorf("verify %q: %w", object.Key, err)
	}
	if targetSize != info.Size || targetDigest != sourceDigest {
		return copyOutcome{}, fmt.Errorf("verify %q: target checksum or size differs from source", object.Key)
	}
	return copyOutcome{
		checkpoint:      objectCheckpoint{Size: info.Size, SHA256: sourceDigest, Listing: listing, ListingRunID: runID},
		message:         "Copying",
		rememberListing: rememberListing,
	}, nil
}

func listingShortcutReliable(source blobstore.Store, listing objectListing) bool {
	return !strings.HasPrefix(source.Identity(), blobstore.BackendLocal+"|") && listing.reliable()
}

func sameRunListingEqual(previous, current objectListing) bool {
	return previous.reliable() && current.reliable() && previous.Size == current.Size && previous.ETag == current.ETag && previous.ModTime.Equal(current.ModTime)
}

func hasStoragePrefix(key string, prefixes []string) bool {
	for _, prefix := range prefixes {
		prefix = strings.Trim(strings.TrimSpace(prefix), "/")
		if prefix != "" && (key == prefix || strings.HasPrefix(key, prefix+"/")) {
			return true
		}
	}
	return false
}

func objectDigest(ctx context.Context, store blobstore.Store, key string) (string, int64, error) {
	reader, info, err := store.Get(ctx, key)
	if err != nil {
		return "", 0, err
	}
	hasher := sha256.New()
	written, readErr := io.Copy(hasher, reader)
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return "", 0, err
	}
	if info.Size != written {
		return "", written, fmt.Errorf("reported size %d differs from bytes read %d", info.Size, written)
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), written, nil
}

func checkpointKey(transitionID, scope, key string) string {
	return transitionID + "\x00" + scope + "\x00" + key
}

func (s *Service) loadCheckpointPage(ctx context.Context, transitionID, scope string, keys []string) (map[string]objectCheckpoint, error) {
	checkpoints := make(map[string]objectCheckpoint, len(keys))
	if s.pool == nil {
		s.memoryMu.Lock()
		defer s.memoryMu.Unlock()
		for _, key := range keys {
			if checkpoint, ok := s.memoryObjects[checkpointKey(transitionID, scope, key)]; ok {
				checkpoints[key] = checkpoint
			}
		}
		return checkpoints, nil
	}
	if len(keys) == 0 {
		return checkpoints, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT object_key, source_size, sha256, listed_size, listed_etag, listed_mod_time, listing_run_id
		FROM storage_transition_checkpoints
		WHERE transition_id=$1 AND scope=$2 AND object_key=ANY($3::text[])`, transitionID, scope, keys)
	if err != nil {
		return nil, fmt.Errorf("load storage transition checkpoint page: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var checkpoint objectCheckpoint
		var listedSize *int64
		var listedETag, listingRunID *string
		var listedModTime *time.Time
		if err := rows.Scan(&key, &checkpoint.Size, &checkpoint.SHA256, &listedSize, &listedETag, &listedModTime, &listingRunID); err != nil {
			return nil, fmt.Errorf("scan storage transition checkpoint page: %w", err)
		}
		if listedSize != nil && listedETag != nil && listedModTime != nil && listingRunID != nil {
			checkpoint.Listing = objectListing{Size: *listedSize, ETag: *listedETag, ModTime: *listedModTime}
			checkpoint.ListingRunID = *listingRunID
		}
		checkpoints[key] = checkpoint
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate storage transition checkpoint page: %w", err)
	}
	return checkpoints, nil
}

func (s *Service) saveCheckpointPage(ctx context.Context, transitionID, scope, runID string, checkpoints map[string]objectCheckpoint, finalPass bool) error {
	if len(checkpoints) == 0 {
		return nil
	}
	if s.pool == nil {
		s.memoryMu.Lock()
		defer s.memoryMu.Unlock()
		for key, checkpoint := range checkpoints {
			s.memoryObjects[checkpointKey(transitionID, scope, key)] = checkpoint
		}
		return nil
	}
	batch := &pgx.Batch{}
	for key, checkpoint := range checkpoints {
		if finalPass {
			batch.Queue(`INSERT INTO storage_transition_checkpoints
				(transition_id, scope, object_key, source_size, sha256, seen_run_id)
				VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (transition_id, scope, object_key) DO UPDATE SET
					source_size=EXCLUDED.source_size, sha256=EXCLUDED.sha256,
					seen_run_id=EXCLUDED.seen_run_id, completed_at=now()`,
				transitionID, scope, key, checkpoint.Size, checkpoint.SHA256, runID)
		} else {
			batch.Queue(`INSERT INTO storage_transition_checkpoints
				(transition_id, scope, object_key, source_size, sha256, listed_size, listed_etag, listed_mod_time, listing_run_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
				ON CONFLICT (transition_id, scope, object_key) DO UPDATE SET
					source_size=EXCLUDED.source_size, sha256=EXCLUDED.sha256,
					listed_size=EXCLUDED.listed_size, listed_etag=EXCLUDED.listed_etag,
					listed_mod_time=EXCLUDED.listed_mod_time, listing_run_id=EXCLUDED.listing_run_id,
					seen_run_id=NULL, completed_at=now()`,
				transitionID, scope, key, checkpoint.Size, checkpoint.SHA256,
				checkpoint.Listing.Size, checkpoint.Listing.ETag, checkpoint.Listing.ModTime, runID)
		}
	}
	results := s.pool.SendBatch(ctx, batch)
	for range checkpoints {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("save storage transition checkpoint page: %w", err)
		}
	}
	return results.Close()
}

func (s *Service) deleteCheckpointOrphans(ctx context.Context, transitionID, scope, runID string, target blobstore.Store, seen map[string]struct{}) error {
	deleteBatch := func(keys []string) error {
		deleted, err := target.Delete(ctx, keys)
		if err != nil {
			return fmt.Errorf("delete target objects removed from source: %w", err)
		}
		if deleted != len(keys) {
			return fmt.Errorf("delete target objects removed from source: deleted %d of %d", deleted, len(keys))
		}
		return s.deleteCheckpoints(ctx, transitionID, scope, keys)
	}
	if s.pool == nil {
		keys, err := s.checkpointKeys(ctx, transitionID, scope)
		if err != nil {
			return err
		}
		missing := make([]string, 0)
		for _, key := range keys {
			if _, ok := seen[key]; !ok {
				missing = append(missing, key)
			}
		}
		for start := 0; start < len(missing); start += 500 {
			end := min(start+500, len(missing))
			batch := missing[start:end]
			if err := deleteBatch(batch); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		rows, err := s.pool.Query(ctx, `SELECT object_key FROM storage_transition_checkpoints
			WHERE transition_id=$1 AND scope=$2 AND seen_run_id IS DISTINCT FROM $3
			ORDER BY object_key LIMIT 500`, transitionID, scope, runID)
		if err != nil {
			return fmt.Errorf("list unseen storage transition checkpoints: %w", err)
		}
		missing := make([]string, 0, 500)
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return fmt.Errorf("scan unseen storage transition checkpoint: %w", err)
			}
			missing = append(missing, key)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return fmt.Errorf("iterate unseen storage transition checkpoints: %w", rowsErr)
		}
		if len(missing) == 0 {
			return nil
		}
		if err := deleteBatch(missing); err != nil {
			return err
		}
	}
}

func (s *Service) checkpointKeys(ctx context.Context, transitionID, scope string) ([]string, error) {
	if s.pool != nil {
		return nil, errors.New("checkpointKeys is only available for in-memory transitions")
	}
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	prefix := checkpointKey(transitionID, scope, "")
	keys := make([]string, 0)
	for composite := range s.memoryObjects {
		if strings.HasPrefix(composite, prefix) {
			keys = append(keys, strings.TrimPrefix(composite, prefix))
		}
	}
	return keys, nil
}

func (s *Service) deleteCheckpoints(ctx context.Context, transitionID, scope string, keys []string) error {
	if s.pool == nil {
		s.memoryMu.Lock()
		defer s.memoryMu.Unlock()
		for _, key := range keys {
			delete(s.memoryObjects, checkpointKey(transitionID, scope, key))
		}
		return nil
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM storage_transition_checkpoints WHERE transition_id=$1 AND scope=$2 AND object_key=ANY($3::text[])`, transitionID, scope, keys); err != nil {
		return fmt.Errorf("delete storage transition checkpoints: %w", err)
	}
	return nil
}

func (s *Service) loadCursor(ctx context.Context, transitionID, scope string) (prefixCursor, error) {
	if s.pool == nil {
		s.memoryMu.Lock()
		defer s.memoryMu.Unlock()
		return s.memoryCursors[checkpointKey(transitionID, scope, "")], nil
	}
	var cursor prefixCursor
	err := s.pool.QueryRow(ctx, `SELECT cursor, copied_objects, copied_bytes, completed FROM storage_transition_cursors WHERE transition_id=$1 AND scope=$2`, transitionID, scope).Scan(&cursor.Cursor, &cursor.Objects, &cursor.Bytes, &cursor.Completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return prefixCursor{}, nil
	}
	if err != nil {
		return prefixCursor{}, fmt.Errorf("load storage transition cursor: %w", err)
	}
	return cursor, nil
}

func (s *Service) saveCursor(ctx context.Context, transitionID, scope string, cursor prefixCursor) error {
	if s.pool == nil {
		s.memoryMu.Lock()
		defer s.memoryMu.Unlock()
		s.memoryCursors[checkpointKey(transitionID, scope, "")] = cursor
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO storage_transition_cursors (transition_id, scope, cursor, copied_objects, copied_bytes, completed) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (transition_id, scope) DO UPDATE SET cursor=EXCLUDED.cursor, copied_objects=EXCLUDED.copied_objects, copied_bytes=EXCLUDED.copied_bytes, completed=EXCLUDED.completed, updated_at=now()`, transitionID, scope, cursor.Cursor, cursor.Objects, cursor.Bytes, cursor.Completed)
	if err != nil {
		return fmt.Errorf("save storage transition cursor: %w", err)
	}
	return nil
}

func openTarget(values map[string]string) (blobstore.Store, error) {
	if resolvedBackend(values) == blobstore.BackendLocal {
		return blobstore.NewFilesystem(values[settingArtworkLocalPath])
	}
	pathStyle, _ := strconv.ParseBool(values["s3.public_path_style"])
	client := s3client.NewClient(s3client.BucketConfig{Role: storageRolePublic, Endpoint: values[settingPublicEndpoint], PublicEndpoint: values[settingPublicReadURL], Region: values["s3.public_region"], PathStyle: pathStyle, Bucket: values[settingPublicBucket], KeyPrefix: values[settingPublicKeyPrefix], AccessKey: values[settingPublicAccessKey], SecretKey: values[settingPublicSecretKey], URLAuth: values["s3.public_url_auth"], TokenSecret: values["s3.public_token_secret"], TokenParam: values["s3.public_token_param"]})
	return blobstore.NewS3(client), nil
}

// commit applies the staged settings and records the new locations. When the
// private bucket changes, its recorded identity follows: the new bucket's, or
// none when private storage is removed.
func (s *Service) commit(ctx context.Context, staged stagedTarget, identity string, privateChanged bool, privateIdentity string, recordLocalRoot bool) error {
	return s.settings.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		raw := strings.TrimSpace(current[StagedTargetSettingKey])
		if raw == "" {
			return nil, errors.New("staged storage target disappeared before commit")
		}
		var currentStage stagedTarget
		if err := json.Unmarshal([]byte(raw), &currentStage); err != nil || currentStage.ID != staged.ID || currentStage.Phase != transitionPhaseCopying {
			return nil, errors.New("staged storage target changed before commit")
		}
		// A node that lost its admission session may rejoin once the exclusive
		// lock is gone. Confirming ownership inside this transaction means any
		// rejoin check that reads the location afterwards sees this commit.
		if s.nodeAdmission != nil {
			if err := s.nodeAdmission.VerifyExclusive(ctx); err != nil {
				return nil, fmt.Errorf("storage node admission lost before commit: %w", err)
			}
		}
		// Write what the transition changed. A setting it left alone keeps
		// today's value, so a credential rotated or a read endpoint changed
		// while the copy ran is not reverted to the snapshot taken at Start.
		// Location keys always take the staged value: the copy was verified
		// against exactly that location.
		now := config.EffectiveAdminSettings(current)
		writes := selectStorageValues(staged.Values)
		for key, value := range writes {
			if staged.Baseline != nil && !isLocationKey(key) && staged.Baseline[key] == value {
				writes[key] = now[key]
			}
		}
		for _, key := range legacyOperationalKeys {
			writes[key] = ""
		}
		// A private-only move does not claim an unwritten artwork location,
		// unless the copy verified operational objects in the local root.
		// Read the current row here so a first asset write during the copy
		// remains recorded when this transaction commits.
		if s.source.Identity() != identity || recordLocalRoot || current[blobstore.IdentitySettingKey] != "" {
			writes[blobstore.IdentitySettingKey] = identity
		}
		if privateChanged {
			writes[blobstore.OperationalIdentitySettingKey] = privateIdentity
		}
		staged.TargetIdentity = identity
		staged.Phase = transitionPhaseRestartPending
		staged.LastError = ""
		encoded, err := json.Marshal(staged)
		if err != nil {
			return nil, err
		}
		writes[StagedTargetSettingKey] = string(encoded)
		return writes, nil
	})
}

func (s *Service) stageCommitted(transitionID string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := s.settings.Get(ctx, StagedTargetSettingKey)
	if err != nil {
		return false, err
	}
	var staged stagedTarget
	if err := json.Unmarshal([]byte(raw), &staged); err != nil {
		return false, err
	}
	return staged.ID == transitionID && staged.Phase == transitionPhaseRestartPending, nil
}

// verifyCommitOutcome distinguishes a rejected commit from a response lost
// after PostgreSQL durably applied it. An unresolved outcome is deliberately
// treated as possibly committed by the caller: source fences remain held and
// restart recovery decides from durable state.
func (s *Service) verifyCommitOutcome(transitionID string) (committed, known bool) {
	attempts := s.commitVerifyAttempts
	if attempts <= 0 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		committed, err := s.stageCommitted(transitionID)
		if err == nil {
			return committed, true
		}
		if attempt+1 < attempts && s.commitVerifyBackoff != nil {
			if err := s.commitVerifyBackoff(context.Background(), attempt); err != nil {
				break
			}
		}
	}
	return false, false
}

// FinalizeCommitted performs only bounded restart recovery. Potentially large
// catalog and branding reconciliation is left staged for RunPostRestartWork,
// which the server starts after its HTTP listener is accepting connections.
func (s *Service) FinalizeCommitted(ctx context.Context) error {
	staged, ok, err := s.committedStage(ctx)
	if isUnreadableCommittedStage(err) {
		slog.ErrorContext(ctx, "storage transition recovery is blocked by an unreadable staged setting", "error", err)
		return nil
	}
	if err != nil || !ok {
		return err
	}
	return s.finalizeCommittedStage(ctx, staged)
}

// finalizeCommittedStage is also used after the listener starts. A temporary
// settings read failure at boot must not let later reconciliation clear the
// stage before artifact locations and the job receipt are repaired.
func (s *Service) finalizeCommittedStage(ctx context.Context, staged stagedTarget) error {
	if staged.TargetIdentity == "" || staged.TargetIdentity != s.source.Identity() {
		return errCommittedTargetMismatch
	}
	// migrate_all copied the objects, so their rows follow them. Rows naming
	// the "local" sentinel follow under every policy: an S3 reader would take
	// it as a real bucket name and presign or delete against someone else's
	// bucket. Their objects were not copied, so they read as missing instead.
	oldBucket := operationalBucket(staged.SourceIdentity, staged.SourcePrivateBucket)
	newBucket := operationalBucket(staged.TargetIdentity, staged.TargetPrivateBucket)
	if staged.Policy == PolicyMigrateAll || oldBucket == blobstore.LocalBucket {
		if err := s.repointPrivateArtifacts(ctx, oldBucket, newBucket); err != nil {
			return err
		}
	}
	if err := s.completeFinalizedJob(ctx, staged); err != nil {
		return err
	}
	if staged.PublicReconcile || staged.BrandingReconcile {
		return nil
	}
	return s.clearRecovery(ctx, staged.ID)
}

// RunPostRestartWork keeps managed recovery alive after the listener starts.
// Each attempt reacquires the cluster-wide lock and resumes its durable
// checkpoint, allowing another API node to take over after an owner dies.
func (s *Service) RunPostRestartWork(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		done, owned, err := s.runPostRestartAttempt(ctx)
		if errors.Is(err, errCommittedTargetMismatch) || errors.Is(err, errCommittedStageInvalid) {
			_ = s.persistRecoveryStatusDetached(ctx, recoveryStateBlocked, err.Error(), 0, "Recovery is blocked")
			return err
		}
		if done {
			return err
		}
		if err != nil && !owned && ctx.Err() == nil {
			slog.WarnContext(ctx, "storage transition: non-owner recovery attempt failed", "error", err)
		}
		if s.postRestartBackoff == nil {
			return err
		}
		if waitErr := s.postRestartBackoff(ctx, attempt); waitErr != nil {
			return waitErr
		}
	}
}

// runPostRestartAttempt performs one ownership attempt. done is true when no
// work remains or when the attempt reached a non-retryable terminal result.
func (s *Service) runPostRestartAttempt(ctx context.Context) (done, owned bool, resultErr error) {
	staged, ok, err := s.committedStage(ctx)
	if err != nil {
		return false, false, fmt.Errorf("precheck committed storage transition: %w", err)
	}
	if !ok {
		return true, false, nil
	}
	if err := s.finalizeCommittedStage(ctx, staged); err != nil {
		return false, false, err
	}
	if !staged.PublicReconcile && !staged.BrandingReconcile {
		return true, false, nil
	}

	var lock *pglock.Lock
	if s.pool != nil {
		var acquired bool
		lock, acquired, err = pglock.TryAcquire(ctx, s.pool, pglock.ArtworkReconcileLockKey)
		if err != nil {
			return false, false, fmt.Errorf("acquire storage transition reconcile lock: %w", err)
		}
		if !acquired {
			return false, false, nil
		}
		defer func() {
			if releaseErr := lock.Release(context.Background()); releaseErr != nil {
				// The work result is already durable; a failed unlock destroys the
				// connection, so logging is safer than replaying completed work.
				slog.WarnContext(ctx, "storage transition reconcile lock release failed", "error", releaseErr)
			}
		}()
	}
	// Persist a failed owner's retry state before releasing the advisory lock.
	// Otherwise a new owner can publish running and then be overwritten by the
	// old owner's delayed failure write.
	defer func() {
		if resultErr == nil || errors.Is(resultErr, errCommittedTargetMismatch) || errors.Is(resultErr, errCommittedStageInvalid) {
			return
		}
		if statusErr := s.persistRecoveryStatusDetached(ctx, recoveryStateWaitingRetry, resultErr.Error(), 0, "Reconciliation paused; waiting to retry"); statusErr != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "storage transition: persist retry status failed", "error", statusErr)
		}
	}()
	staged, ok, err = s.committedStage(ctx)
	if err != nil {
		return false, true, err
	}
	if !ok || (!staged.PublicReconcile && !staged.BrandingReconcile) {
		return true, true, nil
	}
	if staged.TargetIdentity == "" || staged.TargetIdentity != s.source.Identity() {
		return true, true, errCommittedTargetMismatch
	}
	if err := s.persistRecoveryStatus(ctx, recoveryStateRunning, "", staged.RecoveryProgress, "Reconciling committed artwork storage"); err != nil {
		return false, true, err
	}

	if staged.PublicReconcile {
		checkpoint, err := s.loadArtworkReconcileCheckpoint(ctx, staged)
		if err != nil {
			return false, true, err
		}
		save := func(next metadata.ArtworkReconcileCheckpoint) error {
			return s.saveArtworkReconcileCheckpoint(ctx, staged, next)
		}
		var progressMu sync.Mutex
		lastProgressWrite := time.Time{}
		reportProgress := func(percent float64, message string) {
			progressMu.Lock()
			defer progressMu.Unlock()
			now := time.Now()
			if !lastProgressWrite.IsZero() && s.progressInterval > 0 && now.Sub(lastProgressWrite) < s.progressInterval {
				return
			}
			lastProgressWrite = now
			if persistErr := s.persistRecoveryStatus(ctx, recoveryStateRunning, "", int(percent), message); persistErr != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "storage transition: persist reconcile progress failed", "error", persistErr)
			}
		}
		var stats metadata.ArtworkReconcileStats
		if s.reconcile != nil {
			stats, err = s.reconcile(ctx, s.source, reportProgress)
		} else if s.reconcileResumable != nil {
			stats, err = s.reconcileResumable(ctx, s.source, checkpoint, save, reportProgress)
		} else {
			err = errors.New("artwork reconcile is not configured")
		}
		if err != nil {
			return false, true, fmt.Errorf("reconcile committed artwork storage: %w", err)
		}
		if stats.SweepErrors > 0 {
			return false, true, fmt.Errorf("reconcile committed artwork storage: %d rows could not be verified", stats.SweepErrors)
		}
		if err := s.updateStage(ctx, staged.ID, func(state *stagedTarget) {
			state.PublicReconcile = false
		}); err != nil {
			return false, true, fmt.Errorf("record completed artwork reconciliation: %w", err)
		}
		staged.PublicReconcile = false
	}
	if staged.BrandingReconcile {
		if s.brandingReconcile != nil {
			if _, _, err := s.brandingReconcile(ctx); err != nil {
				return false, true, fmt.Errorf("reconcile committed branding assets: %w", err)
			}
		}
		if err := s.updateStage(ctx, staged.ID, func(state *stagedTarget) {
			state.BrandingReconcile = false
		}); err != nil {
			return false, true, fmt.Errorf("record completed branding reconciliation: %w", err)
		}
	}
	if err := s.setSetting(ctx, config.ArtworkStorageReconcileCheckpointKey, ""); err != nil {
		return false, true, fmt.Errorf("clear artwork reconcile checkpoint: %w", err)
	}
	if err := s.clearRecovery(ctx, staged.ID); err != nil {
		return false, true, err
	}
	return true, true, nil
}

func (s *Service) persistRecoveryStatusDetached(parent context.Context, state, lastError string, progress int, message string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	return s.persistRecoveryStatus(ctx, state, lastError, progress, message)
}

func (s *Service) persistRecoveryStatus(ctx context.Context, state, lastError string, progress int, message string) error {
	return s.updateStage(ctx, "", func(staged *stagedTarget) {
		if staged.Phase != transitionPhaseRestartPending || (!staged.PublicReconcile && !staged.BrandingReconcile) {
			return
		}
		staged.RecoveryState = state
		staged.LastError = lastError
		if progress > 0 || staged.RecoveryProgress == 0 {
			staged.RecoveryProgress = min(max(progress, 0), 100)
		}
		if message != "" {
			staged.RecoveryMessage = message
		}
	})
}

func (s *Service) committedStage(ctx context.Context) (stagedTarget, bool, error) {
	if s == nil || s.settings == nil || s.source == nil {
		return stagedTarget{}, false, nil
	}
	raw, err := s.settings.Get(ctx, StagedTargetSettingKey)
	if err != nil {
		return stagedTarget{}, false, fmt.Errorf("%w: read staged setting: %w", errCommittedStageUnreadable, err)
	}
	if strings.TrimSpace(raw) == "" {
		return stagedTarget{}, false, nil
	}
	var staged stagedTarget
	if err := json.Unmarshal([]byte(raw), &staged); err != nil {
		return stagedTarget{}, false, fmt.Errorf("%w: decode staged JSON: %w", errCommittedStageInvalid, err)
	}
	return staged, staged.Phase == transitionPhaseRestartPending, nil
}

func (s *Service) clearRecovery(ctx context.Context, transitionID string) error {
	if s.pool != nil && transitionID != "" {
		if _, err := s.pool.Exec(ctx, `DELETE FROM storage_transition_checkpoints WHERE transition_id=$1`, transitionID); err != nil {
			return fmt.Errorf("clear storage transition checkpoints: %w", err)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM storage_transition_cursors WHERE transition_id=$1`, transitionID); err != nil {
			return fmt.Errorf("clear storage transition cursors: %w", err)
		}
	} else if transitionID != "" {
		s.memoryMu.Lock()
		prefix := transitionID + "\x00"
		for key := range s.memoryObjects {
			if strings.HasPrefix(key, prefix) {
				delete(s.memoryObjects, key)
			}
		}
		for key := range s.memoryCursors {
			if strings.HasPrefix(key, prefix) {
				delete(s.memoryCursors, key)
			}
		}
		s.memoryMu.Unlock()
	}
	return s.clearStaged(ctx, transitionID)
}

func (s *Service) loadArtworkReconcileCheckpoint(ctx context.Context, staged stagedTarget) (*metadata.ArtworkReconcileCheckpoint, error) {
	raw, err := s.settings.Get(ctx, config.ArtworkStorageReconcileCheckpointKey)
	if err != nil {
		return nil, fmt.Errorf("read artwork reconcile checkpoint: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var envelope artworkReconcileCheckpointEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		slog.WarnContext(ctx, "storage transition: ignoring invalid artwork reconcile checkpoint", "error", err)
		return nil, nil
	}
	if envelope.BaselineIdentity != staged.SourceIdentity || envelope.TargetIdentity != staged.TargetIdentity {
		return nil, nil
	}
	return &envelope.Checkpoint, nil
}

func (s *Service) saveArtworkReconcileCheckpoint(ctx context.Context, staged stagedTarget, checkpoint metadata.ArtworkReconcileCheckpoint) error {
	encoded, err := json.Marshal(artworkReconcileCheckpointEnvelope{BaselineIdentity: staged.SourceIdentity, TargetIdentity: staged.TargetIdentity, Checkpoint: checkpoint})
	if err != nil {
		return err
	}
	return s.setSetting(ctx, config.ArtworkStorageReconcileCheckpointKey, string(encoded))
}

func (s *Service) setSetting(ctx context.Context, key, value string) error {
	return s.settings.UpdateAtomic(ctx, func(map[string]string) (map[string]string, error) {
		return map[string]string{key: value}, nil
	})
}

func (s *Service) completeFinalizedJob(ctx context.Context, staged stagedTarget) error {
	if s.pool == nil || staged.ID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE admin_jobs
		SET status=CASE WHEN status IN ('queued','running') THEN 'completed' ELSE status END,
			result_payload=jsonb_set(
				jsonb_set(CASE WHEN jsonb_typeof(result_payload) = 'object' THEN result_payload ELSE '{}'::jsonb END, '{manual_restart_required}', 'false'::jsonb, true),
				'{phase}', '"completed"'::jsonb, true),
			message=CASE WHEN status IN ('queued','running') THEN 'Storage transition completed after restart' ELSE message END,
			error_message=CASE WHEN status IN ('queued','running') THEN '' ELSE error_message END,
			cancel_requested=CASE WHEN status IN ('queued','running') THEN false ELSE cancel_requested END,
			completed_at=CASE WHEN status IN ('queued','running') THEN COALESCE(completed_at, now()) ELSE completed_at END,
			expires_at=CASE WHEN status IN ('queued','running') THEN GREATEST(COALESCE(expires_at, now() + interval '7 days'), now() + interval '24 hours') ELSE expires_at END,
			heartbeat_at=CASE WHEN status IN ('queued','running') THEN now() ELSE heartbeat_at END,
			updated_at=CASE WHEN status IN ('queued','running') THEN now() ELSE updated_at END
		WHERE job_type=$2 AND request_payload->>'transition_id'=$1
		  AND status IN ('queued','running','completed')
		  AND (status <> 'completed' OR result_payload->>'manual_restart_required' = 'true' OR result_payload->>'phase' = 'restart_pending')`, staged.ID, adminjob.JobTypeStorageTransition)
	if err != nil {
		return fmt.Errorf("complete finalized storage transition job: %w", err)
	}
	return nil
}

// repointPrivateArtifacts runs before the restarted server begins serving. The
// migrate-all copy preserves object keys, so only bucket-bearing database
// references need to move. A local root records the "local" bucket, so rows
// move in both directions between local disk and private S3. Keeping this step
// behind the restart means the old process continues resolving downloads
// through its old client until shutdown.
func (s *Service) repointPrivateArtifacts(ctx context.Context, oldBucket, newBucket string) error {
	if s.pool == nil || oldBucket == "" || newBucket == "" || oldBucket == newBucket {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin private artifact relocation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // Commit below decides the outcome.
	if _, err := tx.Exec(ctx, `UPDATE client_diagnostic_reports SET blob_bucket=$2 WHERE blob_bucket=$1`, oldBucket, newBucket); err != nil {
		return fmt.Errorf("repoint diagnostic bundles: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE admin_jobs SET artifact_bucket=$2 WHERE artifact_bucket=$1`, oldBucket, newBucket); err != nil {
		return fmt.Errorf("repoint catalog job artifacts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit private artifact relocation: %w", err)
	}
	return nil
}

func (s *Service) recordStageFailure(ctx context.Context, transitionID string, cause error) error {
	return s.updateStage(ctx, transitionID, func(state *stagedTarget) {
		if state.Phase == transitionPhaseRestartPending {
			return
		}
		state.Phase = transitionPhaseFailed
		state.LastError = cause.Error()
	})
}

// CancelStorageTransition releases a queued transition target so a later
// request can select a different destination, including after a stale running
// job is requeued. A committed stage cannot be canceled.
func (s *Service) CancelStorageTransition(ctx context.Context, req adminjob.StorageTransitionRequest) error {
	committed := false
	err := s.updateStage(ctx, req.TransitionID, func(state *stagedTarget) {
		if state.Phase == transitionPhaseRestartPending {
			committed = true
			return
		}
		if state.Phase == transitionPhaseStaged || state.Phase == transitionPhaseCopying {
			state.Phase = transitionPhaseFailed
			state.LastError = "Storage transition canceled before commit"
		}
	})
	if err != nil {
		return err
	}
	if committed {
		return adminjob.ErrStorageTransitionAlreadyCommitted
	}
	return nil
}

func (s *Service) updateStage(ctx context.Context, transitionID string, update func(*stagedTarget)) error {
	return s.settings.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		raw := strings.TrimSpace(current[StagedTargetSettingKey])
		if raw == "" {
			return nil, errors.New("storage transition is no longer staged")
		}
		var staged stagedTarget
		if err := json.Unmarshal([]byte(raw), &staged); err != nil {
			return nil, err
		}
		if transitionID != "" && staged.ID != "" && staged.ID != transitionID {
			return nil, errors.New("storage transition state belongs to another request")
		}
		update(&staged)
		encoded, err := json.Marshal(staged)
		if err != nil {
			return nil, err
		}
		return map[string]string{StagedTargetSettingKey: string(encoded)}, nil
	})
}

func (s *Service) clearStaged(ctx context.Context, transitionID string) error {
	return s.settings.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		raw := strings.TrimSpace(current[StagedTargetSettingKey])
		if raw == "" {
			return nil, nil
		}
		var staged stagedTarget
		if err := json.Unmarshal([]byte(raw), &staged); err != nil {
			return nil, err
		}
		if staged.ID != transitionID {
			return nil, nil
		}
		return map[string]string{StagedTargetSettingKey: ""}, nil
	})
}

// targetIdentities returns the identities the target assets and private stores
// would report, without creating a local root or contacting S3.
func (s *Service) targetIdentities(values map[string]string) (assets, private string, err error) {
	if resolvedBackend(values) == blobstore.BackendLocal {
		assets, err = blobstore.LocalIdentity(values[settingArtworkLocalPath])
		if err != nil {
			return "", "", err
		}
	} else {
		store, openErr := s.openPublic(values)
		if openErr != nil {
			return "", "", openErr
		}
		assets = store.Identity()
	}
	return assets, storeIdentity(s.openPrivate(values)), nil
}

// storageLocation names where the assets and operational stores live, as the
// normalized store identities the copy itself compares.
type storageLocation struct {
	backend     string
	assets      string
	operational string
}

// locationOf mirrors operationalStore for identities: the private bucket when
// configured, the local root without one, and nothing for S3 without one.
func locationOf(assetsIdentity, privateIdentity string) storageLocation {
	loc := storageLocation{backend: blobstore.BackendS3, assets: assetsIdentity, operational: privateIdentity}
	if strings.HasPrefix(assetsIdentity, blobstore.BackendLocal+"|") {
		loc.backend = blobstore.BackendLocal
		if loc.operational == "" {
			loc.operational = assetsIdentity
		}
	}
	return loc
}

func (l storageLocation) operationalName() string {
	if strings.HasPrefix(l.operational, blobstore.BackendLocal+"|") {
		return "local disk"
	}
	return "the private bucket"
}

func describe(current, target storageLocation, policy string) Preflight {
	warnings := []string{"The old storage location will not be deleted automatically.", "A Silo restart is required after the transition commits."}
	assetsMove := current.assets != target.assets
	operationalMoves := current.operational != "" && target.operational != "" && current.operational != target.operational
	provider := "Copied to the new artwork store."
	uploads := provider
	subtitles := "Downloaded subtitles are copied to the new artwork store."
	switch policy {
	case PolicyFresh:
		provider = "Cached copies are not read from the old store; paths return to saved provider URLs and can be rebuilt with Backfill Metadata Images."
		uploads = "Custom artwork and generated-only images are cleared and must be uploaded or regenerated again."
		subtitles = "Subtitle rows are retained, but their files are not copied; those subtitles are unavailable until downloaded again."
	case PolicyPreserveUploads:
		provider = "Provider cache is not copied; paths return to saved provider URLs and can be rebuilt with Backfill Metadata Images."
		switch {
		case operationalMoves:
			uploads = "Branding, collection and library posters, profile avatars, and downloaded subtitles are copied."
		case current.operational == "":
			uploads = "Branding, collection and library posters, and downloaded subtitles are copied."
		default:
			uploads = "Branding, collection and library posters, and downloaded subtitles are copied; profile avatars stay in their current storage."
		}
	}
	if !assetsMove {
		provider = "Artwork stays in its current storage."
		uploads = "Uploaded artwork stays in its current storage."
		subtitles = "Downloaded subtitles stay in their current storage."
		if operationalMoves && policy == PolicyFresh {
			uploads += " Profile avatars are not copied."
		} else if operationalMoves {
			uploads += " Profile avatars are copied."
		}
	} else if policy == PolicyFresh {
		warnings = append(warnings, "Start fresh leaves existing subtitle rows pointing at files in the old storage.")
	}
	if assetsMove && (policy == PolicyFresh || policy == PolicyPreserveUploads) {
		warnings = append(warnings, "NFO/sidecar artwork is not copied. After the restart and artwork reconciliation, refresh metadata for affected libraries to restore it; Backfill Metadata Images does not restore local sidecar artwork.")
	}
	fate := "stay in their current storage."
	switch {
	case current.operational == target.operational || current.operational == "":
		// Nothing moves, or there was no operational storage to read.
	case target.operational == "":
		fate = "remain in the old storage; the new location has no private storage, so they are unavailable."
	case policy == PolicyMigrateAll:
		fate = "are copied to " + target.operationalName() + " and their references are updated after restart."
	default:
		fate = "remain on " + current.operationalName() + " and are unavailable after the switch."
	}
	return Preflight{
		CurrentBackend: current.backend, TargetBackend: target.backend, Policy: policy, Warnings: warnings,
		ProviderImages: provider, Uploads: uploads, Subtitles: subtitles,
		Diagnostics: "Diagnostic bundles " + fate, CatalogSeeds: "Catalog job artifacts " + fate,
	}
}

func validPolicy(policy string) bool {
	return policy == PolicyFresh || policy == PolicyPreserveUploads || policy == PolicyMigrateAll
}
func resolvedBackend(values map[string]string) string {
	backend := strings.ToLower(strings.TrimSpace(values[settingArtworkBackend]))
	if backend == "" || backend == config.ArtworkBackendAuto {
		if strings.TrimSpace(values[settingPublicBucket]) != "" {
			return blobstore.BackendS3
		}
		return blobstore.BackendLocal
	}
	return backend
}

// isLocationKey reports whether key selects where blobs live rather than how
// Silo reaches them.
func isLocationKey(key string) bool {
	switch key {
	case settingArtworkBackend, settingArtworkLocalPath,
		settingPublicEndpoint, settingPublicBucket, settingPublicKeyPrefix,
		settingPrivateEndpoint, settingPrivateBucket, settingPrivateKeyPrefix:
		return true
	}
	return false
}

// storageKeys lists every key a transition owns: the backend and local path
// first, then the public and private S3 settings.
func storageKeys() []string {
	return append(append([]string{}, publicStorageKeys...), privateStorageKeys...)
}

func isStorageKey(key string) bool {
	for _, candidate := range storageKeys() {
		if key == candidate {
			return true
		}
	}
	return false
}
func selectStorageValues(values map[string]string) map[string]string {
	out := map[string]string{}
	for _, key := range append(append([]string{}, publicStorageKeys...), privateStorageKeys...) {
		out[key] = values[key]
	}
	return out
}
func clone(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

func equalValues(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
