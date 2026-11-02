package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// Selective restore reads a backup's restore set, previews it against this
// server, and applies the chosen items as a backup_restore admin job.
// docs/architecture/backups.md describes the rules; this file holds the
// shared types.

// Item statuses a preview reports against this server.
const (
	ItemOnThisServer    = "on_this_server"
	ItemNotOnThisServer = "not_on_this_server"
	ItemDiffers         = "differs"
	ItemFolderMissing   = "folder_missing"
	ItemAmbiguous       = "ambiguous"
	ItemNeedsAnotherKey = "needs_another_key"
)

// Reasons an item cannot be chosen, is restored only in part, or was left
// out. Clients switch on these; messages are for display.
const (
	ReasonSame                 = "same"
	ReasonPathInUse            = "path_in_use"
	ReasonFolderMissing        = "folder_missing"
	ReasonLibraryNotRestorable = "library_not_restorable"
	ReasonLibraryNotMapped     = "library_not_mapped"
	ReasonAmbiguousName        = "ambiguous_name"
	ReasonNeedsAnotherKey      = "needs_another_key"
	ReasonConfigNeedsKey       = "config_needs_another_key"
	ReasonOtherVersion         = "other_version"
	ReasonItemNotOnThisServer  = "item_not_on_this_server"
	ReasonFileMissing          = "file_missing"
	ReasonNotInBackup          = "not_in_backup"
	ReasonChanged              = "changed_since_preview"
	// ReasonTemplateNotAvailable: a template collection whose template this
	// server's catalog does not have, or one that does not suit the library.
	ReasonTemplateNotAvailable = "template_not_available"
	// ReasonCollectionNotMapped: a page section naming a collection that is
	// neither here nor restorable.
	ReasonCollectionNotMapped = "collection_not_mapped"
)

// Report outcomes.
const (
	OutcomeRestored  = "restored"
	OutcomeUnchanged = "unchanged"
	OutcomeLeftOut   = "left_out"
	OutcomeFailed    = "failed"
)

// Item kinds.
const (
	KindSettingsPage = "settings_page"
	KindPlugin       = "plugin"
	KindLibrary      = "library"
	KindCollection   = "collection"
	KindItemArtwork  = "item_artwork"
	KindBranding     = "branding"
	// KindTemplateCollection is a collection a template bundle created.
	KindTemplateCollection = "template_collection"
	// KindHomeSection is an admin home or library page section.
	KindHomeSection = "home_section"
)

// OperationRestore is the status row's operation while a restore holds the
// lock.
const OperationRestore = "restore"

// ErrDigestMismatch refuses a restore whose source changed since its
// preview.
var ErrDigestMismatch = errors.New("the backup changed since it was previewed; open it again")

// RestoreTargets are the write paths a restore applies through. Each takes
// the caller's transaction, so an item's writes commit with its fenced
// outcome, and returns work to run after that commit (events, restart flags,
// follow-up scans). They are built with the API handlers, after the service.
type RestoreTargets struct {
	// ApplySettings writes values through the admin settings write path in
	// tx and reports the keys that apply after a restart.
	ApplySettings func(ctx context.Context, tx pgx.Tx, values map[string]string) (restartKeys []string, after func(context.Context), err error)
	Plugins       PluginRestorer
	// LibraryCreated runs the library editor's follow-ups for a committed
	// new library: sections, provider chain, monitoring, and the first scan.
	// It may run again after a crash, so each step must be idempotent and
	// must not overwrite setup an admin made since.
	LibraryCreated func(ctx context.Context, id int) error
	// PluginInstalled runs a restored installation's follow-ups: the
	// lifecycle change (EventPluginsChanged) and the provider-chain sync an
	// upload performs. Like LibraryCreated it may run again.
	PluginInstalled func(ctx context.Context, installationID int) error
	// ArtworkChanged announces artwork published for an item.
	ArtworkChanged func(ctx context.Context, contentID, parentContentID string)
	// TrackArtwork registers an artwork revision's objects with the
	// revision collector (catalog.ArtworkRevisionTracker), as the image
	// cache does around its uploads. Nil tracks nothing.
	TrackArtwork func(ctx context.Context, originalPath, imageType string, objectKeys []string) error
	// CheckFolder validates a library folder as the library editor does.
	CheckFolder func(ctx context.Context, path string) error
	// CreateJob queues the backup_restore admin job.
	CreateJob func(ctx context.Context, userID int, req RestoreRequest) (*models.AdminJob, error)
	// ActiveRestore returns the request of the restore job queued or
	// running now, or nil when there is none.
	ActiveRestore func(ctx context.Context) (*RestoreRequest, error)
	// TemplateCollection returns the collection a bundle apply creates from
	// a template for a library of libraryType, with every default, and the
	// template's title; ErrTemplateNotAvailable when this server's catalog
	// has no such template or it does not suit the library.
	TemplateCollection func(bundleID, templateID string, libraryID int, libraryType string) (TemplateDefaults, error)
	// TemplateTitle names a template in this server's catalog.
	TemplateTitle func(templateID string) (string, bool)
	// TemplateCollectionCreated finishes a restored template collection
	// after its commit: its template poster, its first sync, and its next
	// scheduled sync. It may run again, and a failure is retried.
	TemplateCollectionCreated func(ctx context.Context, collectionID string) error
	// CheckSection validates a page section as the admin section editor
	// does; create marks a new row.
	CheckSection func(sectionType, scope string, libraryID *int, config json.RawMessage, create bool) error
}

// TemplateDefaults is a template collection as a bundle apply creates it.
type TemplateDefaults struct {
	Title      string
	Input      catalog.CreateLibraryCollectionInput
	NeedsSetup bool
}

// ErrTemplateNotAvailable reports a template this server's catalog does not
// have, or one that does not suit the library.
var ErrTemplateNotAvailable = errors.New("this server's collection templates do not include this template")

// PluginRestorer is the plugin write path a restore uses.
type PluginRestorer interface {
	RestorePlugin(ctx context.Context, tx pgx.Tx, in plugins.RestoreInstallationInput) (plugins.RestoreInstallationResult, func(context.Context), error)
	DecodeStoredConfig(installationID int, key string, stored []byte) (map[string]any, error)
}

// SetRestoreTargets wires selective restore.
func (s *Service) SetRestoreTargets(t RestoreTargets) { s.targets = t }

// RestoreRequest is a restore job's payload: the source backup of this
// server, the digest its preview returned, and the chosen item ids by
// section.
type RestoreRequest struct {
	BackupID  string              `json:"backup_id"`
	Digest    string              `json:"digest"`
	Selection map[string][]string `json:"selection"`
}

// maxSelectedItems bounds one restore request.
const maxSelectedItems = 100_000

// FencedUpdate is the job row update a fenced transaction records.
type FencedUpdate struct {
	Result  any
	Current int
	Total   int
	Message string
}

// RestoreRun is one claimed restore job, as the admin job runner hands it
// over.
type RestoreRun struct {
	Payload []byte
	// Fenced runs fn in a transaction that locks the job row first,
	// requiring it running under this worker's claim, and records fn's update
	// in the same transaction.
	Fenced    func(ctx context.Context, fn func(pgx.Tx) (FencedUpdate, error)) error
	Published func(ctx context.Context)
}

// restoreSource names a restore set: a backup of this server.
type restoreSource struct{ backupID string }

func (r RestoreRequest) source() restoreSource {
	return restoreSource{backupID: r.BackupID}
}

// locate returns a source's digest, frozen in its manifest, and a function
// that opens it. The digest is of the stored bytes; the opened reader hashes
// them as it reads.
func (s *Service) locate(ctx context.Context, src restoreSource) (string, func(context.Context) (*storedReader, error), error) {
	b, err := s.completeBackup(ctx, src.backupID)
	if err != nil {
		return "", nil, err
	}
	entry, ok := b.manifest.object(RestoreSetName)
	if b.manifest.RestoreSet == nil || !ok {
		return "", nil, ErrNoRestoreSet
	}
	return entry.SHA256, func(ctx context.Context) (*storedReader, error) {
		return s.openPlain(ctx, b, RestoreSetName)
	}, nil
}

// readStoredRestoreSet reads a restore set from body and records the digest
// of the stored bytes it read, which is what manifests and uploads freeze.
func readStoredRestoreSet(ctx context.Context, body *storedReader, opts ReadOptions) (*RestoreSet, error) {
	rs, err := ReadRestoreSet(ctx, body, opts)
	if err != nil {
		return nil, err
	}
	stored, err := body.storedEntry(RestoreSetName)
	if err != nil {
		rs.Cleanup()
		return nil, fmt.Errorf("restore set: %w", err)
	}
	rs.Digest = stored.SHA256
	return rs, nil
}

// openRestoreSet reads a source's restore set and checks it against the
// digest recorded for it. extract writes plugin archives and blobs to a
// temporary directory the caller removes with Cleanup.
func (s *Service) openRestoreSet(ctx context.Context, src restoreSource, extract bool) (*RestoreSet, error) {
	digest, open, err := s.locate(ctx, src)
	if err != nil {
		return nil, err
	}
	body, err := open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	opts := ReadOptions{}
	if extract {
		opts.ExtractDir = os.TempDir()
	}
	rs, err := readStoredRestoreSet(ctx, body, opts)
	if err != nil {
		if errors.Is(err, ErrRestoreSetTooNew) || ctx.Err() != nil {
			return nil, err
		}
		return nil, &InvalidRestoreSetError{Err: err}
	}
	if rs.Digest != digest {
		rs.Cleanup()
		return nil, fmt.Errorf("%s does not match the checksum in its manifest", RestoreSetName)
	}
	return rs, nil
}

// previewSet returns a source's validated restore set for a preview,
// sharing one read among previews of the same archive (restoreSetCache).
func (s *Service) previewSet(ctx context.Context, src restoreSource) (*RestoreSet, error) {
	digest, _, err := s.locate(ctx, src)
	if err != nil {
		return nil, err
	}
	return s.restoreSets.get(ctx, digest, func(ctx context.Context) (*RestoreSet, error) {
		rs, err := s.openRestoreSet(ctx, src, false)
		if err != nil {
			return nil, err
		}
		if rs.Digest != digest {
			return nil, errors.New("the restore set changed while it was read")
		}
		return rs, nil
	})
}

// StartRestore checks a request and queues the restore job. Nothing is read
// beyond the manifest: the job reads the restore set, under the lock.
//
// The source is checked again and the job created under the subsystem lock,
// which backup deletion and rotation hold while they check the queued
// restore's source: either they see the job, or the job's check sees the
// source gone. The lock is released once the job is queued; its worker
// takes it again.
func (s *Service) StartRestore(ctx context.Context, userID int, req RestoreRequest) (*models.AdminJob, error) {
	if s.targets.CreateJob == nil || s.opts.Locker == nil {
		return nil, errors.New("selective restore is not available on this server")
	}
	if req.BackupID == "" {
		return nil, &SelectionError{Message: "a restore names a backup"}
	}
	total := 0
	for section, ids := range req.Selection {
		if _, known := sectionByID(section); !known {
			return nil, &SelectionError{Message: fmt.Sprintf("section %q is not one a restore applies", section)}
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if id == "" || len(id) > 512 || seen[id] {
				return nil, &SelectionError{Message: fmt.Sprintf("section %q lists an empty, overlong, or repeated item", section)}
			}
			seen[id] = true
		}
		total += len(ids)
	}
	if total == 0 {
		return nil, &SelectionError{Message: "choose at least one item to restore"}
	}
	if total > maxSelectedItems {
		return nil, &SelectionError{Message: fmt.Sprintf("a restore can apply at most %d items", maxSelectedItems)}
	}
	held, err := s.opts.Locker.TryLock(ctx)
	if err != nil {
		return nil, err
	}
	defer held.Release(context.WithoutCancel(ctx))
	ctx, cancel := withLost(ctx, held)
	defer cancel()
	digest, _, err := s.locate(ctx, req.source())
	if err != nil {
		return nil, err
	}
	if req.Digest != digest {
		return nil, ErrDigestMismatch
	}
	return s.targets.CreateJob(ctx, userID, req)
}

// ErrBackupInUse refuses deleting a backup a queued or running restore
// reads.
var ErrBackupInUse = errors.New("a restore from this backup is queued or running")

// activeRestoreSource returns the source of the restore queued or running
// now; zero when there is none, or when this server cannot queue restores.
// The caller holds the subsystem lock, under which restores are queued
// (StartRestore), so no restore is queued between this read and what the
// caller then deletes.
func (s *Service) activeRestoreSource(ctx context.Context) (restoreSource, error) {
	if s.targets.ActiveRestore == nil {
		return restoreSource{}, nil
	}
	active, err := s.targets.ActiveRestore(ctx)
	if err != nil {
		return restoreSource{}, fmt.Errorf("reading the queued restore: %w", err)
	}
	if active == nil {
		return restoreSource{}, nil
	}
	return active.source(), nil
}

// InvalidRestoreSetError is a restore set that failed the reader's checks:
// damaged, truncated, or not written by Silo.
type InvalidRestoreSetError struct{ Err error }

func (e *InvalidRestoreSetError) Error() string { return e.Err.Error() }
func (e *InvalidRestoreSetError) Unwrap() error { return e.Err }

// SelectionError is a restore request the server will not queue.
type SelectionError struct{ Message string }

func (e *SelectionError) Error() string { return e.Message }

// SelectiveReport is a restore's outcome per item, kept as the job's result.
// It is written in the same transaction as each item, so it always shows
// what committed, even after an interruption.
type SelectiveReport struct {
	BackupID              string `json:"backup_id"`
	Digest                string `json:"digest"`
	BeforeRestoreBackupID string `json:"before_restore_backup_id,omitempty"`
	// LibraryMap maps each backup library id to the library it is on this
	// server, restored or matched. Later sections remap through it.
	LibraryMap map[string]int `json:"library_map"`
	Items      []ReportItem   `json:"items"`
	Counts     map[string]int `json:"counts"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Error      string         `json:"error,omitempty"`
	// ErrorCode classifies Error for clients: one of the RestoreError*
	// codes.
	ErrorCode string `json:"error_code,omitempty"`
}

// Codes a restore report gives the error that stopped it.
const (
	RestoreErrorSetChanged          = "restore_set_changed"
	RestoreErrorBusy                = "busy"
	RestoreErrorBeforeRestoreBackup = "before_restore_backup_failed"
	RestoreErrorFailed              = "failed"
)

// restoreErrorCode classifies the error that stopped a restore.
func restoreErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrDigestMismatch):
		return RestoreErrorSetChanged
	case errors.Is(err, ErrBusy):
		return RestoreErrorBusy
	case errors.Is(err, errBeforeRestoreBackup):
		return RestoreErrorBeforeRestoreBackup
	}
	return RestoreErrorFailed
}

// errBeforeRestoreBackup marks a restore stopped because its before-restore
// backup failed.
var errBeforeRestoreBackup = errors.New("the backup taken before restoring failed, so nothing was restored")

// ReportItem is one item's outcome.
type ReportItem struct {
	Section string `json:"section"`
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Outcome string `json:"outcome"`
	// Reason is a code for a left-out item, Message the sentence to show,
	// and Error a failed item's error.
	Reason          string   `json:"reason,omitempty"`
	Message         string   `json:"message,omitempty"`
	Error           string   `json:"error,omitempty"`
	RestartRequired bool     `json:"restart_required,omitempty"`
	RestartKeys     []string `json:"restart_keys,omitempty"`
	Notes           []string `json:"notes,omitempty"`
	MissingBlobs    []string `json:"missing_blobs,omitempty"`
}
