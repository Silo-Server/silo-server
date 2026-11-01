package apiv2

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/models"
)

// AdminBackupService is the backup subsystem the Backups page drives.
type AdminBackupService interface {
	// Configured reports whether the current backup destination exists,
	// without probing it: capabilities describe configuration, not health.
	Configured() bool
	// Destinations lists the destinations this server can write to.
	Destinations() []string
	List(context.Context) (backup.Listing, error)
	// TestS3 checks a bucket the way backups use it.
	TestS3(ctx context.Context, cfg backup.S3Config) (backup.S3TestResult, error)
	// AcknowledgeKey records that the admin keeps a copy of the SECRET_KEY
	// whose check value is check, which must be this server's.
	AcknowledgeKey(ctx context.Context, check string) error
	StartManual(context.Context) (string, error)
	Delete(context.Context, string) error
	DownloadLinks(ctx context.Context, id, scope string, ttl time.Duration) ([]backup.ObjectLink, error)
	OpenObject(context.Context, string, string) (io.ReadCloser, int64, error)
	// Preview compares a backup's restore set with this server.
	Preview(ctx context.Context, id string) (*backup.RestorePreview, error)
	// StartRestore queues a backup_restore job for the chosen items.
	StartRestore(ctx context.Context, userID int, req backup.RestoreRequest) (*models.AdminJob, error)

	// UploadsAvailable reports whether restore sets can be uploaded here.
	UploadsAvailable() bool
	Uploads(context.Context) ([]backup.UploadSession, error)
	CreateUpload(ctx context.Context, userID int, size int64, sha256 string, partSize int64) (backup.UploadSession, error)
	PutUploadPart(ctx context.Context, id string, part int, body io.Reader, length int64) (backup.UploadSession, error)
	CompleteUpload(ctx context.Context, id string) (backup.UploadSession, error)
	DeleteUpload(ctx context.Context, id string) error
	// PreviewUpload compares a complete upload's restore set with this
	// server.
	PreviewUpload(ctx context.Context, id string) (*backup.RestorePreview, error)
}

// adminBackupLinkTTL matches the job artifact links.
const adminBackupLinkTTL = 15 * time.Minute

const adminBackupsTag = "admin-backups"

// Parameters of the signed object route.
const (
	backupObjectNameParam = "name"
	signedLinkExpiryParam = "exp"
	signedLinkSigParam    = "sig"
)

// AdminBackupCapabilities advertises what this build's backups can do. Each
// flag is false until the feature ships, so a client never offers an action
// the server would refuse.
type AdminBackupCapabilities struct {
	Capability
	Destinations     []string `json:"destinations" doc:"Backup destinations this server can write to: default, the server's own storage, when it has one, and s3, a bucket used only for backups"`
	Encryption       bool     `json:"encryption" doc:"Whether backups can be encrypted. Backups in any S3 bucket always are, with a key derived from SECRET_KEY: the s3 destination, and the default one when this server's private storage is a bucket"`
	Upload           bool     `json:"upload" doc:"Whether a restore set can be uploaded from another server"`
	MaxUploadBytes   int64    `json:"max_upload_bytes" doc:"The largest restore set an upload takes"`
	SelectiveRestore bool     `json:"selective_restore" doc:"Whether parts of a backup can be previewed and restored from the API: settings, plugins, libraries, accounts and their profiles' data, collections and uploaded artwork"`
	Download         bool     `json:"download" doc:"Whether a full backup can be downloaded"`
	available        bool
}

func (c AdminBackupCapabilities) capabilityState() string {
	return configuredCapabilityState(c.available)
}

type AdminBackupCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminBackupCapabilities
}

type AdminBackupObject struct {
	Name      string `json:"name" doc:"Object name in the backup folder as stored, such as db.dump, or db.dump.age when the backup is encrypted"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256" doc:"SHA-256 of the stored bytes"`
}

type AdminBackupMissingBlob struct {
	Store  string `json:"store" enum:"assets,operational"`
	Key    string `json:"key"`
	Source string `json:"source" doc:"What referenced the file, such as library poster"`
}

// AdminBackupOmitted is an item a backup's restore set could not carry. The
// database dump still holds it; only selective restore cannot offer it.
type AdminBackupOmitted struct {
	Section string `json:"section" enum:"users"`
	ID      string `json:"id" doc:"The item's id in its section, such as the account id"`
	Label   string `json:"label"`
	Reason  string `json:"reason" enum:"too_large,export_failed" doc:"too_large: its document exceeds what a restore set may hold; export_failed: reading it failed"`
	Message string `json:"message"`
	Cause   string `json:"cause,omitempty" doc:"What failed, stable across backups so items can be grouped: the part being read (a profile data type such as notifications, or profiles or items) and, when its error names one, the step that failed in it, such as notifications: reading notification preferences. Absent for too_large and for backups taken before restore set format 3"`
	Error   string `json:"error,omitempty" doc:"The underlying error, without ids"`
}

type AdminBackup struct {
	ID           string                   `json:"id" doc:"The backup folder name: UTC start time and kind"`
	Kind         string                   `json:"kind" enum:"nightly,manual,before-restore"`
	Status       string                   `json:"status" enum:"complete,complete_with_warnings,in_progress,incomplete" doc:"complete: manifest written, checksums recorded, table of contents readable; it does not claim a restore was tested. complete_with_warnings: some referenced files were missing when copied, or the restore set left items out (restore_set_omitted). in_progress: being written now. incomplete: no manifest and nothing running; deleted after 24 hours"`
	CreatedAt    Instant                  `json:"created_at"`
	CompletedAt  *Instant                 `json:"completed_at,omitempty"`
	SizeBytes    int64                    `json:"size_bytes"`
	SiloVersion  string                   `json:"silo_version,omitempty"`
	Location     string                   `json:"location" doc:"Where the backup folder is: a filesystem path on this server's storage, or s3://bucket/key in a bucket"`
	BlobCount    int                      `json:"blob_count" doc:"Files copied into blobs.tar"`
	MissingBlobs []AdminBackupMissingBlob `json:"missing_blobs"`
	// RestoreSetOmitted surfaces what selective restore cannot offer.
	RestoreSetOmitted []AdminBackupOmitted `json:"restore_set_omitted" doc:"Items the restore set left out; the dump still holds them"`
	Objects           []AdminBackupObject  `json:"objects"`
	Encrypted         bool                 `json:"encrypted" doc:"Whether the objects are stored encrypted with a key derived from SECRET_KEY. The manifest is never encrypted"`
	KeyCheck          string               `json:"key_check,omitempty" doc:"Check value of the SECRET_KEY the backup was taken with: 16 hex digits in groups of 4"`
	KeyMatches        bool                 `json:"key_matches" doc:"Whether key_check is this server's. An encrypted backup that does not match cannot be previewed, restored, or downloaded decrypted here"`
}

// AdminBackupActive is the operation holding the cluster-wide backup lock,
// on whichever server runs it.
type AdminBackupActive struct {
	Operation string  `json:"operation" enum:"backup,delete,restore,upload" doc:"restore: a selective restore from backup_id or upload_id is running; upload: the upload upload_id is being checked"`
	BackupID  string  `json:"backup_id,omitempty"`
	UploadID  string  `json:"upload_id,omitempty" doc:"The upload a restore reads, or the upload being checked or deleted"`
	StartedAt Instant `json:"started_at"`
	Progress  float64 `json:"progress" minimum:"0" maximum:"100" doc:"Percent complete, for a backup"`
	Message   string  `json:"message,omitempty"`
}

// AdminBackupLastRun is the latest backup run's outcome.
type AdminBackupLastRun struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind" enum:"nightly,manual,before-restore"`
	Status     string   `json:"status" enum:"complete,complete_with_warnings,failed,interrupted" doc:"interrupted: the server running it stopped before it finished"`
	StartedAt  Instant  `json:"started_at"`
	FinishedAt *Instant `json:"finished_at,omitempty"`
	Error      string   `json:"error,omitempty" doc:"Why a failed or interrupted run stopped"`
}

type AdminBackupWarning struct {
	Code    string `json:"code" enum:"disabled,no_recent_backup,destination_unavailable,sqlite_user_store,pg_dump_missing,pg_dump_too_old,key_not_confirmed,no_backups_at_destination" doc:"no_recent_backup: no backup completed in 48 hours, at any destination. no_backups_at_destination: backups completed before, but none is at the current destination (it was switched); earlier copies stay where they were written. key_not_confirmed: backups are encrypted (an S3 bucket) and this server's SECRET_KEY was not confirmed, or changed since; encrypted backups and uploads are paused until POST /admin/backups/s3/acknowledge-key"`
	Message string `json:"message"`
}

type AdminBackupList struct {
	ServerID             string               `json:"server_id" doc:"This server's identity; its backups live under backups/<server_id>/"`
	Destination          string               `json:"destination" enum:"default,s3" doc:"Where new backups go (backup.destination). Items lists only this destination's backups; copies at a previous destination stay there"`
	DestinationAvailable bool                 `json:"destination_available"`
	Encrypted            bool                 `json:"encrypted" doc:"Whether new backups are encrypted: true for the s3 destination, and for default when this server's private storage is an S3 bucket. Encrypted backups run only while key_confirmed is true"`
	KeyCheck             string               `json:"key_check" doc:"This server's SECRET_KEY check value, 16 hex digits in groups of 4. It names the key without revealing it; silo secret check prints it for a key"`
	KeyConfirmed         bool                 `json:"key_confirmed" doc:"Whether an admin confirmed keeping a copy of the SECRET_KEY with this check value. Encrypted backups (any S3 bucket) run only while it is true"`
	Enabled              bool                 `json:"enabled" doc:"Whether nightly backups run (backup.enabled)"`
	Busy                 bool                 `json:"busy" doc:"Whether a backup or restore holds the cluster-wide backup lock now"`
	Active               *AdminBackupActive   `json:"active,omitempty" doc:"The operation holding the lock, started on any server"`
	LastRun              *AdminBackupLastRun  `json:"last_run,omitempty" doc:"The latest backup run, including one that failed or was interrupted"`
	Keep                 int                  `json:"keep" doc:"How many nightly and manual backups rotation keeps (backup.keep)"`
	LastSuccessAt        *Instant             `json:"last_success_at,omitempty" doc:"When the latest successful backup finished, at any destination"`
	Items                []AdminBackup        `json:"items" doc:"This server's backups, newest first"`
	OtherServerIDs       []string             `json:"other_server_ids" doc:"Other server identities with backups at this destination"`
	Warnings             []AdminBackupWarning `json:"warnings" doc:"Conditions the dashboard should show"`
}

type AdminBackupListOutput struct{ Body AdminBackupList }

type AdminBackupStarted struct {
	ID     string `json:"id"`
	Kind   string `json:"kind" enum:"manual"`
	Status string `json:"status" enum:"in_progress"`
}

type AdminBackupStartedOutput struct{ Body AdminBackupStarted }

type AdminBackupIDInput struct {
	ID string `path:"id" minLength:"1" maxLength:"64" pattern:"^[0-9]{8}T[0-9]{6}(\\.[0-9]{3})?Z-[a-z-]+$"`
}

type AdminBackupDownloadLink struct {
	Name      string  `json:"name"`
	SizeBytes int64   `json:"size_bytes"`
	URL       string  `json:"url" format:"uri-reference" doc:"Short-lived URL that downloads the object without a session: a presigned storage URL, or a signed path on this server"`
	ExpiresAt Instant `json:"expires_at"`
}

type AdminBackupDownloadLinksBody struct {
	Scope string `json:"scope" enum:"full,restore-set" doc:"What to download: full is every stored object as it is, encrypted for an encrypted backup (decrypt with silo backup decrypt); restore-set is restore-set.tar alone, decrypted by this server for an encrypted backup"`
}

type AdminBackupDownloadLinksInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"64" pattern:"^[0-9]{8}T[0-9]{6}(\\.[0-9]{3})?Z-[a-z-]+$"`
	Body AdminBackupDownloadLinksBody
}

type AdminBackupDownloadLinksOutput struct {
	Body struct {
		Items []AdminBackupDownloadLink `json:"items"`
	}
}

func backupProblem(err error) error {
	switch {
	case errors.Is(err, backup.ErrBusy):
		return NewProblem(TypeConflict, "Another backup or restore is running.")
	case errors.Is(err, backup.ErrBackupInUse):
		return NewProblem(TypeConflict, "A restore from this backup is queued or running. Delete it once the restore finishes.")
	case errors.Is(err, backup.ErrNotFound):
		return NewProblem(TypeNotFound, "Backup not found.")
	case errors.Is(err, backup.ErrSQLiteUserStore):
		return NewProblem(TypeConflict, backup.ErrSQLiteUserStore.Error())
	case errors.Is(err, backup.ErrNoDestination), errors.Is(err, backup.ErrNoArchiveKey):
		return NewProblem(TypeCapabilityNotConfigured, "This server has no storage for backups.")
	case errors.Is(err, backup.ErrNoRestoreSet):
		return NewProblem(TypeConflict, "This backup has no restore set: it was taken before selective restore existed.")
	case errors.Is(err, backup.ErrRestoreSetTooNew):
		return NewProblem(TypeConflict, backup.ErrRestoreSetTooNew.Error())
	case errors.Is(err, backup.ErrDigestMismatch):
		return NewProblem(TypeRestoreSetChanged, "The backup changed since it was previewed. Open it again.")
	case errors.As(err, new(*backup.InvalidRestoreSetError)):
		return NewProblem(TypeConflict, "The backup's restore set cannot be read: "+err.Error())
	case errors.As(err, new(*backup.WrongKeyError)):
		return NewProblem(TypeConflict, err.Error()+".")
	case errors.Is(err, backup.ErrKeyNotConfirmed):
		return NewProblem(TypeConflict, backup.ErrKeyNotConfirmed.Error()+".")
	case errors.Is(err, backup.ErrKeyCheckChanged):
		return NewProblem(TypeConflict, "This server's SECRET_KEY check value is not the one in the request. Read it again.")
	case errors.Is(err, backup.ErrS3NotConfigured):
		return NewProblem(TypeCapabilityNotConfigured, "Backups go to an S3 bucket, and no bucket is set up.")
	default:
		return serviceProblem(err)
	}
}

func (reg *Registry) backups() (AdminBackupService, *Problem) {
	if reg.deps.AdminBackups == nil {
		return nil, unavailable("backups")
	}
	return reg.deps.AdminBackups, nil
}

func registerAdminBackups(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/backups"+path, id, adminBackupsTag, summary), Class: ClassActingAdmin, ServiceBacked: true}
		if method != http.MethodGet {
			o.DemoRestricted = true
		}
		return o
	}
	capabilities := op(http.MethodGet, "/capabilities", "getAdminBackupCapabilities", "What backups on this server support. not_configured when the server has no storage to write backups to.")
	capabilities.ServiceBacked = false
	Register(reg, capabilities, func(context.Context, *CapabilityInput) (*AdminBackupCapabilitiesOutput, error) {
		configured := reg.deps.AdminBackups != nil && reg.deps.AdminBackups.Configured()
		destinations := []string{}
		if reg.deps.AdminBackups != nil {
			destinations = append(destinations, reg.deps.AdminBackups.Destinations()...)
		}
		upload := configured && reg.deps.AdminBackups.UploadsAvailable()
		return &AdminBackupCapabilitiesOutput{Body: AdminBackupCapabilities{
			Destinations: destinations, Encryption: slices.Contains(destinations, backup.DestinationS3),
			Download: configured, SelectiveRestore: configured, Upload: upload,
			MaxUploadBytes: backup.MaxUploadBytes, available: configured,
		}}, nil
	})
	Register(reg, op(http.MethodGet, "", "listAdminBackups", "List this server's backups with their manifest summaries, the other server identities at the destination, and the warnings the dashboard shows."),
		func(ctx context.Context, _ *struct{}) (*AdminBackupListOutput, error) {
			svc, p := reg.backups()
			if p != nil {
				return nil, p
			}
			listing, err := svc.List(ctx)
			if err != nil {
				return nil, backupProblem(err)
			}
			return &AdminBackupListOutput{Body: adminBackupListOf(listing)}, nil
		})
	start := op(http.MethodPost, "", "startAdminBackup", "Back up now. The backup runs on this server; poll the list or the backup task for its result. 409 when another backup or restore is running.")
	start.DefaultStatus = http.StatusAccepted
	start.RetrySafety = RetrySafetyNonRetryable
	start.Errors = []int{http.StatusConflict}
	Register(reg, start, func(ctx context.Context, _ *struct{}) (*AdminBackupStartedOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		id, err := svc.StartManual(ctx)
		if err != nil {
			return nil, backupProblem(err)
		}
		return &AdminBackupStartedOutput{Body: AdminBackupStarted{ID: id, Kind: string(backup.KindManual), Status: string(backup.StatusInProgress)}}, nil
	})
	del := op(http.MethodDelete, "/{id}", "deleteAdminBackup", "Delete one backup of this server. 409 when another backup or restore is running, or a queued or running restore reads this backup.")
	del.RetrySafety = RetrySafetyNaturalIdempotent
	del.Errors = []int{http.StatusConflict}
	Register(reg, del, func(ctx context.Context, in *AdminBackupIDInput) (*struct{}, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		if err := svc.Delete(ctx, in.ID); err != nil {
			return nil, backupProblem(err)
		}
		return nil, nil
	})
	links := op(http.MethodPost, "/{id}/download-links", "createAdminBackupDownloadLinks", "Mint short-lived download URLs for a complete backup's objects, or for its restore set alone: presigned storage URLs, or signed links to this server where the storage cannot presign.")
	links.RetrySafety = RetrySafetyNaturalIdempotent
	links.Errors = []int{http.StatusConflict}
	Register(reg, links, func(ctx context.Context, in *AdminBackupDownloadLinksInput) (*AdminBackupDownloadLinksOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		objects, err := svc.DownloadLinks(ctx, in.ID, in.Body.Scope, adminBackupLinkTTL)
		if err != nil {
			return nil, backupProblem(err)
		}
		out := &AdminBackupDownloadLinksOutput{}
		out.Body.Items = make([]AdminBackupDownloadLink, 0, len(objects))
		now := time.Now()
		for _, obj := range objects {
			link := AdminBackupDownloadLink{Name: obj.Name, SizeBytes: obj.Size, URL: obj.URL, ExpiresAt: NewInstant(obj.ExpiresAt)}
			if link.URL == "" {
				if reg.deps.AdminBackupSigner == nil {
					return nil, unavailable("backup downloads")
				}
				signed, expires := reg.deps.AdminBackupSigner.SignFor(artworkurl.BackupObjectKey(in.ID, obj.Name), now, adminBackupLinkTTL)
				link.URL, link.ExpiresAt = signed, NewInstant(expires)
			}
			out.Body.Items = append(out.Body.Items, link)
		}
		return out, nil
	})
	registerAdminBackupObjectDownload(reg)
	registerAdminBackupS3(reg, op)
	registerAdminBackupRestore(reg, op)
	registerAdminBackupUploads(reg, op)
}

func adminBackupListOf(l backup.Listing) AdminBackupList {
	out := AdminBackupList{
		ServerID: l.ServerID, Destination: l.Destination, DestinationAvailable: l.DestinationAvailable,
		Encrypted: l.Encrypted, KeyCheck: l.KeyCheck, KeyConfirmed: l.KeyConfirmed,
		Enabled: l.Enabled, Busy: l.Busy, Keep: l.Keep, LastSuccessAt: instantPtr(l.LastSuccessAt),
		Items: make([]AdminBackup, 0, len(l.Backups)), OtherServerIDs: append([]string{}, l.OtherServerIDs...),
		Warnings: make([]AdminBackupWarning, 0, len(l.Warnings)),
	}
	if a := l.Active; a != nil {
		out.Active = &AdminBackupActive{Operation: a.Operation, BackupID: a.BackupID, UploadID: a.UploadID, StartedAt: NewInstant(a.StartedAt), Progress: a.Progress, Message: a.Message}
	}
	if r := l.LastRun; r != nil {
		out.LastRun = &AdminBackupLastRun{ID: r.ID, Kind: string(r.Kind), Status: r.Status, StartedAt: NewInstant(r.StartedAt), Error: r.Error}
		if !r.FinishedAt.IsZero() {
			out.LastRun.FinishedAt = new(NewInstant(r.FinishedAt))
		}
	}
	for _, w := range l.Warnings {
		out.Warnings = append(out.Warnings, AdminBackupWarning{Code: w.Code, Message: w.Message})
	}
	for _, b := range l.Backups {
		item := AdminBackup{
			ID: b.ID, Kind: string(b.Kind), Status: string(b.Status), CreatedAt: NewInstant(b.CreatedAt),
			CompletedAt: instantPtr(b.CompletedAt), SizeBytes: b.SizeBytes, SiloVersion: b.SiloVersion, Location: b.Location, BlobCount: b.BlobCount,
			MissingBlobs: make([]AdminBackupMissingBlob, 0, len(b.MissingBlobs)), Objects: make([]AdminBackupObject, 0, len(b.Objects)),
			RestoreSetOmitted: adminBackupOmittedOf(b.RestoreSetOmitted),
			Encrypted:         b.Encrypted, KeyCheck: b.KeyCheck, KeyMatches: b.KeyMatches,
		}
		for _, m := range b.MissingBlobs {
			item.MissingBlobs = append(item.MissingBlobs, AdminBackupMissingBlob{Store: m.Store, Key: m.Key, Source: m.Source})
		}
		for _, o := range b.Objects {
			item.Objects = append(item.Objects, AdminBackupObject{Name: o.Name, SizeBytes: o.Size, SHA256: o.SHA256})
		}
		out.Items = append(out.Items, item)
	}
	return out
}

// registerAdminBackupObjectDownload streams one backup object to a signed
// capability, as the job artifact route does: the browser opens the link
// with no Authorization header, and the signature names one object of one
// backup under its own capability domain.
func registerAdminBackupObjectDownload(reg *Registry) {
	operation := humaOp(http.MethodGet, Prefix+"/admin/backups/{id}/objects/{name}", "downloadAdminBackupObject", adminBackupsTag,
		"Stream one object of a backup. Authorized by the signed capability in the download link, not by a session.")
	operation.Parameters = []*huma.Param{
		{Name: "id", In: paramInPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1), MaxLength: new(64)}},
		{Name: backupObjectNameParam, In: paramInPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString, Enum: []any{
			backup.ManifestName, backup.DumpName, backup.BlobsName, backup.RestoreSetName,
			backup.DumpName + backup.EncryptedSuffix, backup.BlobsName + backup.EncryptedSuffix, backup.RestoreSetName + backup.EncryptedSuffix,
		}}},
		{Name: signedLinkExpiryParam, In: artworkParamQuery, Required: true, Schema: &huma.Schema{Type: artworkIntegerType, Format: playbackIntegerFormat}},
		{Name: signedLinkSigParam, In: artworkParamQuery, Required: true, Schema: &huma.Schema{Type: huma.TypeString}},
	}
	operation.Responses = map[string]*huma.Response{
		"200": {
			Description: "The stored object",
			Content:     map[string]*huma.MediaType{directMediaBinary: {Schema: &huma.Schema{Type: huma.TypeString, Format: artworkBinaryFormat}}},
			Headers: map[string]*huma.Param{
				directDisposition:         {Schema: &huma.Schema{Type: huma.TypeString}},
				adminSubtitleLengthHeader: {Schema: &huma.Schema{Type: artworkIntegerType}},
			},
		},
		"404": {Description: "Object not found, or the capability is invalid or expired"},
		"409": {Description: "The backup is encrypted with another server's SECRET_KEY, so this server cannot decrypt its restore set, or the stored restore set no longer matches its checksum in the manifest"},
		"503": {Description: "Backup storage unavailable"},
	}
	RegisterRaw(reg, RawOperation{
		Operation: Operation{Operation: operation, Class: ClassPublic, ServiceBacked: true},
		Protocol:  "backup-object",
		Reason:    "Backup bytes must not pass through JSON encoding, and a signed capability stands in for the presigned URL a local store cannot provide.",
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, name := chi.URLParam(r, "id"), chi.URLParam(r, backupObjectNameParam)
		notFound := func() { writeProblem(w, r, NewProblem(TypeNotFound, "Backup object not found.")) }
		if reg.deps.AdminBackups == nil || reg.deps.AdminBackupSigner == nil || len(id) > 64 {
			notFound()
			return
		}
		exp, err := strconv.ParseInt(r.URL.Query().Get(signedLinkExpiryParam), 10, 64)
		if err != nil || reg.deps.AdminBackupSigner.Verify(artworkurl.BackupObjectKey(id, name), exp, r.URL.Query().Get(signedLinkSigParam), time.Now()) != nil {
			notFound()
			return
		}
		body, size, err := reg.deps.AdminBackups.OpenObject(r.Context(), id, name)
		switch {
		case errors.Is(err, backup.ErrNotFound):
			notFound()
			return
		case errors.Is(err, backup.ErrWrongKey), errors.Is(err, backup.ErrStoredMismatch):
			writeProblem(w, r, NewProblem(TypeConflict, err.Error()+"."))
			return
		case err != nil:
			writeProblem(w, r, unavailable("backups"))
			return
		}
		defer func() { _ = body.Close() }()
		sw := httpstream.NewRollingDeadlineWriter(w)
		sw.Header().Set("Content-Type", directMediaBinary)
		// Each object saves under its stored name, so a folder of downloads
		// is a backup folder silo backup restore --from reads, and
		// restore-set.tar is the file an upload on another server takes.
		sw.Header().Set(directDisposition, mime.FormatMediaType("attachment", map[string]string{dispositionFilenameParam: name}))
		sw.Header().Set("Cache-Control", "no-store")
		sw.Header().Set("Accept-Ranges", "none")
		// A restore set decrypted on the way has no length until it ends.
		if size >= 0 {
			sw.Header().Set(adminSubtitleLengthHeader, strconv.FormatInt(size, 10))
		}
		sw.WriteHeader(http.StatusOK)
		if _, err := io.Copy(sw, body); err != nil {
			slog.WarnContext(r.Context(), "backup download interrupted", "component", "backup", "backup_id", id, "object", name)
		}
	}))
}

func adminBackupOmittedOf(list []backup.OmittedItem) []AdminBackupOmitted {
	out := make([]AdminBackupOmitted, 0, len(list))
	for _, o := range list {
		out = append(out, AdminBackupOmitted{Section: o.Section, ID: o.ID, Label: o.Label, Reason: o.Reason, Message: o.Message, Cause: o.Cause, Error: o.Error})
	}
	return out
}
