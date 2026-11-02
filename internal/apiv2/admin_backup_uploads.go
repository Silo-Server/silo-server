package apiv2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/backup"
)

// TypeUploadLimitReached: the server already has as many unfinished
// uploads as it keeps at once.
var TypeUploadLimitReached = ProblemType{"upload_limit_reached", http.StatusConflict, "Upload limit reached"}

const (
	// uploadPartReadTimeout lets a slow uplink send the largest part. The
	// part operation reads its own body, so the deadline is set on the
	// connection directly: the framework's body timeout applies only to
	// bodies it reads, and the listener's read timeout would otherwise cut
	// the part off.
	uploadPartReadTimeout = 2 * time.Minute
	// uploadPartWriteSlack is the time a part's answer has after its read
	// deadline: storing the last bytes and recording the part.
	uploadPartWriteSlack = time.Minute
	// uploadCheckWriteDeadline covers the completion's bounded read of a
	// 2 GiB set (backup.uploadCheckTimeout) before it answers.
	uploadCheckWriteDeadline = 15 * time.Minute
)

type AdminBackupUploadSource struct {
	ServerID      string  `json:"server_id" doc:"The server the restore set was taken on"`
	ServerName    string  `json:"server_name"`
	SiloVersion   string  `json:"silo_version"`
	CreatedAt     Instant `json:"created_at" doc:"When the backup holding the restore set was taken"`
	FormatVersion int     `json:"format_version"`
}

type AdminBackupUploadRejection struct {
	Reason  string `json:"reason" enum:"not_a_restore_set,newer_silo,checksum_mismatch" doc:"not_a_restore_set: the file is not a restore set Silo reads (message says what failed); newer_silo: written by a newer Silo, upgrade this server first; checksum_mismatch: the bytes do not match the sha256 sent when the upload started"`
	Message string `json:"message"`
}

// AdminBackupUpload is one uploaded restore set, finished or not.
type AdminBackupUpload struct {
	ID            string                      `json:"id"`
	Status        string                      `json:"status" enum:"uploading,checking,complete,rejected" doc:"uploading: taking parts; checking: being read and checked; complete: checked, and can be previewed and restored; rejected: not a restore set this server reads (rejection says why); its parts are deleted"`
	SizeBytes     int64                       `json:"size_bytes"`
	PartSize      int64                       `json:"part_size" doc:"Every part but the last is exactly this many bytes"`
	PartCount     int                         `json:"part_count"`
	ReceivedParts int                         `json:"received_parts"`
	ReceivedBytes int64                       `json:"received_bytes"`
	CreatedAt     Instant                     `json:"created_at"`
	UpdatedAt     Instant                     `json:"updated_at" doc:"The last part or status change"`
	CompletedAt   *Instant                    `json:"completed_at,omitempty"`
	ExpiresAt     *Instant                    `json:"expires_at,omitempty" doc:"When the upload is deleted: an unfinished or rejected one 24 hours after its last activity, a complete one 7 days after it was last completed, previewed, or restored from. One a queued or running restore reads is kept."`
	Digest        string                      `json:"digest,omitempty" doc:"SHA-256 of the file as uploaded, frozen when the upload completed; a restore request must carry it"`
	Source        *AdminBackupUploadSource    `json:"source,omitempty" doc:"What a complete restore set says about itself"`
	Rejection     *AdminBackupUploadRejection `json:"rejection,omitempty"`
	Location      string                      `json:"location" doc:"Where the upload's parts are: a folder in this server's storage, or s3://bucket/key in a bucket"`
}

type AdminBackupUploadOutput struct{ Body AdminBackupUpload }

type AdminBackupUploadListOutput struct {
	Body struct {
		Items []AdminBackupUpload `json:"items"`
	}
}

type AdminBackupUploadCreateBody struct {
	SizeBytes int64  `json:"size_bytes" minimum:"1" doc:"The file's size; at most max_upload_bytes from the capabilities (2 GiB)"`
	SHA256    string `json:"sha256,omitempty" pattern:"^[0-9a-f]{64}$" doc:"The file's SHA-256, when the client knows it; completion rejects other bytes"`
	PartSize  int64  `json:"part_size,omitempty" minimum:"1048576" maximum:"16777216" doc:"Omitted picks 4 MiB"`
}

type AdminBackupUploadCreateInput struct {
	Body AdminBackupUploadCreateBody
}

type AdminBackupUploadPath struct {
	UploadID string `path:"upload_id" pattern:"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"`
}

type AdminBackupUploadPartInput struct {
	AdminBackupUploadPath
	Part int `path:"part" minimum:"0" maximum:"4095" doc:"The part's position, counting from 0"`
	requestCapture
}

type AdminBackupUploadCompleteInput struct {
	AdminBackupUploadPath
	requestCapture
}

type AdminBackupUploadRestoreInput struct {
	AdminBackupUploadPath
	Body AdminBackupRestoreBody
}

func adminBackupUploadOf(u backup.UploadSession) AdminBackupUpload {
	out := AdminBackupUpload{ID: u.ID, Status: u.Status, SizeBytes: u.SizeBytes, PartSize: u.PartSize, PartCount: u.PartCount,
		ReceivedParts: u.ReceivedParts, ReceivedBytes: u.ReceivedBytes, CreatedAt: NewInstant(u.CreatedAt), UpdatedAt: NewInstant(u.UpdatedAt),
		CompletedAt: instantPtr(u.CompletedAt), ExpiresAt: instantPtr(u.ExpiresAt), Digest: u.Digest, Location: u.Location}
	if src := u.Source; src != nil {
		out.Source = &AdminBackupUploadSource{ServerID: src.ServerID, ServerName: src.ServerName, SiloVersion: src.SiloVersion,
			CreatedAt: NewInstant(src.CreatedAt), FormatVersion: src.FormatVersion}
	}
	if r := u.Rejection; r != nil {
		out.Rejection = &AdminBackupUploadRejection{Reason: r.Reason, Message: r.Message}
	}
	return out
}

// uploadProblem maps the upload errors, then the backup ones.
func uploadProblem(err error) error {
	if part, ok := errors.AsType[*backup.UploadPartError](err); ok {
		if part.TooLarge {
			return NewProblem(TypePayloadTooLarge, part.Message+".")
		}
		return validationProblem(locationBody, codeInvalid, part.Message+".")
	}
	if incomplete, ok := errors.AsType[*backup.UploadIncompleteError](err); ok {
		return NewProblem(TypeConflict, "Every part must arrive before the upload is completed: "+incomplete.Error()+".")
	}
	var maxBytes *http.MaxBytesError
	switch {
	case errors.Is(err, backup.ErrUploadNotFound):
		return NewProblem(TypeNotFound, "Upload not found.")
	case errors.Is(err, backup.ErrUploadTooLarge), errors.As(err, &maxBytes):
		return NewProblem(TypePayloadTooLarge, fmt.Sprintf("A restore set upload can be at most %d GiB.", backup.MaxUploadBytes>>30))
	case errors.Is(err, backup.ErrTooManyUploads):
		return NewProblem(TypeUploadLimitReached, fmt.Sprintf("%d uploads are already in progress on this server. Finish or delete one first; unfinished uploads are deleted after 24 hours.", backup.MaxActiveUploads))
	case errors.Is(err, backup.ErrUploadCheckTakenOver):
		return NewProblem(TypeConflict, "This upload's check stopped before it finished. Complete it again.")
	case errors.Is(err, backup.ErrUploadNotOpen):
		return NewProblem(TypeConflict, "This upload is being checked or is finished, so it takes no more parts.")
	case errors.Is(err, backup.ErrUploadNotReady):
		return NewProblem(TypeConflict, "This upload hasn't been completed and checked, so it can't be opened.")
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, backup.ErrPartAttemptExpired):
		return NewProblem(TypeRequestTimeout, "The part didn't arrive in time. Send it again.")
	case errors.Is(err, backup.ErrPartBusy):
		return NewProblem(TypeConflict, "This part, or as many parts as an upload may send at once, is being received now. Send it again when that finishes.")
	case errors.Is(err, backup.ErrUploadInUse):
		return NewProblem(TypeConflict, "A restore from this upload is queued or running. Delete it after the restore ends.")
	case errors.Is(err, backup.ErrNotEnoughSpace):
		return NewProblem(TypeConflict, "There isn't enough free space for this upload: "+err.Error()+".")
	}
	return backupProblem(err)
}

func registerAdminBackupUploads(reg *Registry, op func(method, path, id, summary string) Operation) {
	list := op(http.MethodGet, "/uploads", "listAdminBackupUploads",
		"List restore sets uploaded to this server, newest first, finished or not. Unfinished and rejected uploads are deleted 24 hours after their last activity, complete ones 7 days after their last use.")
	Register(reg, list, func(ctx context.Context, _ *struct{}) (*AdminBackupUploadListOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		uploads, err := svc.Uploads(ctx)
		if err != nil {
			return nil, uploadProblem(err)
		}
		out := &AdminBackupUploadListOutput{}
		out.Body.Items = make([]AdminBackupUpload, 0, len(uploads))
		for _, u := range uploads {
			out.Body.Items = append(out.Body.Items, adminBackupUploadOf(u))
		}
		return out, nil
	})

	create := op(http.MethodPost, "/uploads", "createAdminBackupUpload",
		"Start uploading a restore set (restore-set.tar, from another server's Download restore set). The answer gives the part size; send the parts, then complete the upload. Parts go to the shared storage, so any API server takes any part. 413 above 2 GiB; 409 upload_limit_reached when three uploads are already in progress.")
	create.DefaultStatus = http.StatusCreated
	create.RetrySafety = RetrySafetyNonRetryable
	create.Errors = []int{http.StatusConflict, http.StatusRequestEntityTooLarge}
	Register(reg, create, func(ctx context.Context, in *AdminBackupUploadCreateInput) (*AdminBackupUploadOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		u, err := svc.CreateUpload(ctx, claimsFrom(ctx).UserID, in.Body.SizeBytes, in.Body.SHA256, in.Body.PartSize)
		if err != nil {
			return nil, uploadProblem(err)
		}
		return &AdminBackupUploadOutput{Body: adminBackupUploadOf(u)}, nil
	})

	put := op(http.MethodPut, "/uploads/{upload_id}/parts/{part}", "putAdminBackupUploadPart",
		"Store one part of an upload. Every part but the last is exactly part_size bytes. Parts may arrive in any order and on any API server; sending a part again replaces it until the upload is completed. 409 while the same part, or four parts of the upload, are being received; 408 when the part does not arrive within 2 minutes.")
	put.RetrySafety = RetrySafetyNaturalIdempotent
	put.MaxBodyBytes = backup.MaxUploadPartSize
	put.Errors = []int{http.StatusNotFound, http.StatusRequestTimeout, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType}
	Register(reg, put, func(ctx context.Context, in *AdminBackupUploadPartInput) (*AdminBackupUploadOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		timeout := uploadPartReadTimeout
		if reg.deps.uploadPartReadTimeout > 0 {
			timeout = reg.deps.uploadPartReadTimeout
		}
		now := time.Now()
		rc := http.NewResponseController(in.writer)
		// A writer without deadlines (a test recorder) leaves the listener's.
		_ = rc.SetReadDeadline(now.Add(timeout))
		_ = rc.SetWriteDeadline(now.Add(timeout + uploadPartWriteSlack))
		u, err := svc.PutUploadPart(ctx, in.UploadID, in.Part, in.request.Body, in.request.ContentLength)
		if err != nil {
			return nil, uploadProblem(err)
		}
		return &AdminBackupUploadOutput{Body: adminBackupUploadOf(u)}, nil
	})
	registeredOperation(reg.api.OpenAPI(), put).RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
		mediaTypeBinary: {Schema: &huma.Schema{Type: huma.TypeString, Format: diagnosticsChunkBinaryFormat}},
	}}

	complete := op(http.MethodPost, "/uploads/{upload_id}/complete", "completeAdminBackupUpload",
		"Check a fully received upload: its bytes are read in order through the restore-set checks, and their SHA-256 is frozen. The answer is complete, or rejected with the reason (not a restore set, or from a newer Silo). A finished upload answers as it is. 409 while another backup or restore holds the lock; complete it again then.")
	complete.RetrySafety = RetrySafetyNaturalIdempotent
	complete.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, complete, func(ctx context.Context, in *AdminBackupUploadCompleteInput) (*AdminBackupUploadOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		// Reading a large set back can outlast the listener's write timeout.
		_ = http.NewResponseController(in.writer).SetWriteDeadline(time.Now().Add(uploadCheckWriteDeadline))
		// The check does not stop when the client goes away: a closed dialog
		// would otherwise reopen a fully sent upload. Its read has its own
		// deadline (uploadCheckTimeout), and the uploads list shows the result.
		u, err := svc.CompleteUpload(context.WithoutCancel(ctx), in.UploadID)
		if err != nil {
			return nil, uploadProblem(err)
		}
		return &AdminBackupUploadOutput{Body: adminBackupUploadOf(u)}, nil
	})

	del := op(http.MethodDelete, "/uploads/{upload_id}", "deleteAdminBackupUpload",
		"Delete an upload and its parts. 409 while a restore from it is queued or running, or while another backup or restore holds the lock for a complete upload.")
	del.RetrySafety = RetrySafetyNaturalIdempotent
	del.Errors = []int{http.StatusConflict}
	Register(reg, del, func(ctx context.Context, in *AdminBackupUploadPath) (*struct{}, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		if err := svc.DeleteUpload(ctx, in.UploadID); err != nil {
			return nil, uploadProblem(err)
		}
		return nil, nil
	})

	registerAdminBackupUploadRestore(reg, op)
}

// registerAdminBackupUploadRestore previews and restores a complete upload
// as the backup operations do a backup.
func registerAdminBackupUploadRestore(reg *Registry, op func(method, path, id, summary string) Operation) {
	preview := op(http.MethodGet, "/uploads/{upload_id}/restore-set", "getAdminBackupUploadRestoreSet",
		"Preview a complete upload's restore set against this server, as for a backup. The digest is the one frozen when the upload completed. 409 for an upload not yet complete.")
	preview.Errors = []int{http.StatusConflict}
	Register(reg, preview, func(ctx context.Context, in *AdminBackupUploadPath) (*AdminBackupRestoreSetOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		out, err := svc.PreviewUpload(ctx, in.UploadID)
		if err != nil {
			return nil, uploadProblem(err)
		}
		return &AdminBackupRestoreSetOutput{Body: adminBackupRestoreSetOf(out)}, nil
	})

	restore := op(http.MethodPost, "/uploads/{upload_id}/restore", "startAdminBackupUploadRestore",
		"Restore the chosen items of a complete upload as a backup_restore job, as for a backup: a backup of this server is taken first. 409 when another backup or restore is running, or the digest is not the upload's.")
	restore.DefaultStatus = http.StatusAccepted
	restore.RetrySafety = RetrySafetyNonRetryable
	restore.Errors = []int{http.StatusConflict}
	Register(reg, restore, func(ctx context.Context, in *AdminBackupUploadRestoreInput) (*AdminBackupRestoreOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		req := restoreRequestOf(in.Body)
		req.UploadID = in.UploadID
		job, err := svc.StartRestore(ctx, claimsFrom(ctx).UserID, req)
		if err != nil {
			if errors.Is(err, backup.ErrUploadNotFound) || errors.Is(err, backup.ErrUploadNotReady) {
				return nil, uploadProblem(err)
			}
			return nil, restoreProblem(err)
		}
		return &AdminBackupRestoreOutput{Location: Prefix + "/admin/jobs/" + job.ID, RetryAfter: "5", Body: reg.adminTaskJobOf(ctx, job, true)}, nil
	})
}
