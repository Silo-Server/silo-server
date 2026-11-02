package backup

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Restore sets uploaded from another server. internal/uploads keeps chunks
// on one server's disk, and requests are not routed to the server that
// holds them, so an upload writes each part straight to the destination
// under backups/uploads/<id>/ and keeps its session in Postgres: any API
// server takes any part, and any server completes the upload.
//
// Each part sent is its own object, <part>.<nonce>; the part's row names the
// object that counts. Before a byte is stored the send takes an attempt row
// under the session row lock: one per part and a few per upload, so
// concurrent sends cannot each write an object. A part sent again writes a
// new object and moves the row to it in a transaction that requires the
// session to be uploading and the attempt to be its own, so once completion
// has frozen the parts a late writer cannot change them.
//
// Where the operational store is a bucket, every part is stored encrypted
// with the archive key, as backups there are (an uploaded restore set holds
// password hashes), and named <part>.<nonce>.age; the admin must have
// confirmed the key, as for backups. Each part is its own age file, so the
// parts read back in order are the bytes the client sent. The digest an
// upload freezes is the SHA-256 of those bytes (the file as uploaded),
// never of the encrypted parts: it is what a client computes, what the
// declared sha256 is checked against, and what previews and restores bind
// to.

const (
	// MaxUploadBytes is the largest restore set an upload takes.
	MaxUploadBytes int64 = 2 << 30
	// MaxActiveUploads is how many unfinished uploads a server keeps at once.
	MaxActiveUploads = 3
	// UploadExpiry is how long an unfinished or rejected upload is kept after
	// its last activity.
	UploadExpiry = 24 * time.Hour
	// CompletedUploadExpiry is how long a checked upload is kept after it was
	// last completed, previewed, or restored from. One a queued or running
	// restore reads is never deleted.
	CompletedUploadExpiry = 7 * 24 * time.Hour
	// MaxPartAttempts is how many parts of one upload may be written at once.
	MaxPartAttempts = 4
	// partWriteTimeout bounds a part's storage write, reading the request
	// body included. Its context is canceled at the deadline, which stops
	// the write: an S3 request is canceled, and a local write's temporary
	// file is closed and removed.
	partWriteTimeout = 5 * time.Minute
	// partAttemptLimit is how long a part send may take from its attempt to
	// its record. An older attempt is abandoned: another send of the part may
	// replace it, and it can no longer record its object. It exceeds the
	// write's lifetime by a margin, so an attempt is reclaimed only once its
	// writer has certainly stopped, and an upload never has more than
	// MaxPartAttempts writers.
	partAttemptLimit = partWriteTimeout + 5*time.Minute
	// uploadObjectGrace is how old an object no part row names must be before
	// the sweep deletes it. It exceeds partAttemptLimit, so the sweep never
	// deletes an object a send can still record.
	uploadObjectGrace = time.Hour
	// uploadCheckTimeout bounds a completion's read of the parts, and with
	// it how long a completion holds the subsystem lock.
	uploadCheckTimeout = 10 * time.Minute
	// uploadObjectSweepInterval spaces the sweeps that list the upload
	// folders on create and list.
	uploadObjectSweepInterval = 10 * time.Minute

	DefaultUploadPartSize int64 = 4 << 20
	MinUploadPartSize     int64 = 1 << 20
	MaxUploadPartSize     int64 = 16 << 20

	uploadsPrefix = RootPrefix + "uploads/"
	// uploadsLockKey serializes session creation so the limit holds across
	// servers. It spells "SILOBKUL".
	uploadsLockKey int64 = 0x53494C4F424B554C
	// maxRejectionMessage bounds what a rejection keeps of the reader's error.
	maxRejectionMessage = 1000
)

// Upload statuses.
const (
	UploadUploading = "uploading"
	// UploadChecking is a completion reading the parts under the lock.
	UploadChecking = "checking"
	UploadComplete = "complete"
	// UploadRejected is a completed upload that is not a restore set this
	// server reads; its parts are deleted.
	UploadRejected = "rejected"
)

// Why a completed upload was rejected. Clients switch on these.
const (
	RejectNotRestoreSet = "not_a_restore_set"
	RejectNewerSilo     = "newer_silo"
	RejectEncrypted     = "encrypted"
	RejectChecksum      = "checksum_mismatch"
)

// EncryptedUploadMessage is what an upload encrypted with another server's
// key is told. One encrypted with this server's key is decrypted and read.
const EncryptedUploadMessage = "This file is encrypted with another server's SECRET_KEY. On a computer with that server's SECRET_KEY in the environment, run `silo backup decrypt restore-set.tar.age -o restore-set.tar`, then upload restore-set.tar."

var (
	ErrUploadNotFound = errors.New("upload not found")
	ErrUploadTooLarge = fmt.Errorf("a restore set upload can be at most %d GiB", MaxUploadBytes>>30)
	ErrTooManyUploads = fmt.Errorf("%d uploads are already in progress on this server; finish or delete one first (unfinished uploads are deleted after 24 hours)", MaxActiveUploads)
	// ErrUploadNotOpen refuses a part for an upload that is being checked or
	// is finished.
	ErrUploadNotOpen = errors.New("this upload is no longer taking parts")
	// ErrUploadNotReady refuses a preview or restore of an upload that has
	// not passed its check.
	ErrUploadNotReady = errors.New("this upload has not been completed and checked")
	// ErrUploadInUse refuses deleting an upload a queued restore reads.
	ErrUploadInUse = errors.New("a restore from this upload is queued or running")
	// ErrPartBusy refuses a part while the same part, or as many parts as an
	// upload may write at once, are being written.
	ErrPartBusy = errors.New("this part, or too many parts of this upload, are being written now")
	// ErrPartAttemptExpired refuses recording a part whose send took longer
	// than partAttemptLimit.
	ErrPartAttemptExpired = errors.New("this part took too long to arrive")
)

// UploadPartError is a part the upload refuses: outside its parts, or not
// the size its position requires.
type UploadPartError struct {
	Message string
	// TooLarge is a part longer than its position allows.
	TooLarge bool
}

func (e *UploadPartError) Error() string { return e.Message }

// UploadIncompleteError is a completion before every part arrived.
type UploadIncompleteError struct{ Missing int }

func (e *UploadIncompleteError) Error() string {
	return fmt.Sprintf("%d parts have not arrived yet", e.Missing)
}

// UploadSource is what a checked restore set says about itself.
type UploadSource struct {
	ServerID      string
	ServerName    string
	SiloVersion   string
	CreatedAt     time.Time
	FormatVersion int
}

// UploadRejection says why a completed upload is not usable.
type UploadRejection struct {
	Reason  string
	Message string
}

// UploadSession is one upload's state, as every server reads it.
type UploadSession struct {
	ID            string
	Status        string
	SizeBytes     int64
	PartSize      int64
	PartCount     int
	ReceivedParts int
	ReceivedBytes int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CompletedAt   *time.Time
	// ExpiresAt is when an unfinished or rejected upload is deleted.
	ExpiresAt *time.Time
	// Digest is the SHA-256 completion froze, of the file as uploaded (not
	// of the parts as stored, which are encrypted on a bucket); a preview and
	// a restore of the upload are bound to it.
	Digest    string
	Source    *UploadSource
	Rejection *UploadRejection
	// Location is where the upload's parts are.
	Location string

	declaredSHA256 string
}

func uploadPrefix(id string) string { return uploadsPrefix + id + "/" }

func partCount(size, partSize int64) int { return int((size + partSize - 1) / partSize) }

// partLength is the size part n must have: the part size, or what remains
// for the last part.
func partLength(size, partSize int64, n int) int64 {
	if rest := size - int64(n)*partSize; rest < partSize {
		return rest
	}
	return partSize
}

// canonicalUploadID accepts the ids CreateUpload mints, in the form it
// mints them.
func canonicalUploadID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

// UploadsAvailable reports whether this server takes uploads: it has a
// destination for their parts and a PostgreSQL user store, without which
// the restore's own backup could not run.
func (s *Service) UploadsAvailable() bool { return s.uploadReady() == nil }

// uploadReady refuses what no upload on this server could be restored from.
// Uploads always go to the operational store, whatever the backup
// destination is.
func (s *Service) uploadReady() error {
	if s.sqliteUserStore() {
		return ErrSQLiteUserStore
	}
	if s.opts.Destination == nil {
		return ErrNoDestination
	}
	if s.opts.Pool == nil {
		return errors.New("uploads need the database")
	}
	if s.opts.Destination.Encrypted() && s.opts.ArchiveKey == nil {
		return ErrNoArchiveKey
	}
	return nil
}

// uploadKey returns the key new upload parts are encrypted with: nil on a
// local store, the archive key on a bucket, once the admin confirmed the
// SECRET_KEY it derives from (ErrKeyNotConfirmed otherwise, as for backups).
func (s *Service) uploadKey(ctx context.Context) (*ArchiveKey, error) {
	if !s.opts.Destination.Encrypted() {
		return nil, nil
	}
	confirmed, err := s.keyConfirmed(ctx)
	if err != nil {
		return nil, err
	}
	if !confirmed {
		return nil, ErrKeyNotConfirmed
	}
	return s.opts.ArchiveKey, nil
}

// CreateUpload opens an upload of size bytes. sha256, when set, is the
// digest the client expects; completion rejects other bytes. partSize 0
// picks the default.
func (s *Service) CreateUpload(ctx context.Context, userID int, size int64, sha256 string, partSize int64) (UploadSession, error) {
	if err := s.uploadReady(); err != nil {
		return UploadSession{}, err
	}
	if size > MaxUploadBytes {
		return UploadSession{}, ErrUploadTooLarge
	}
	if size <= 0 {
		return UploadSession{}, &UploadPartError{Message: "an upload must have at least one byte"}
	}
	if partSize == 0 {
		partSize = DefaultUploadPartSize
	}
	if partSize < MinUploadPartSize || partSize > MaxUploadPartSize {
		return UploadSession{}, &UploadPartError{Message: fmt.Sprintf("the part size must be between %d and %d bytes", MinUploadPartSize, MaxUploadPartSize)}
	}
	sha256 = strings.ToLower(strings.TrimSpace(sha256))
	if sha256 != "" && !sha256Hex.MatchString(sha256) {
		return UploadSession{}, &UploadPartError{Message: "sha256 must be 64 hexadecimal digits"}
	}
	if _, err := s.uploadKey(ctx); err != nil {
		return UploadSession{}, err
	}
	s.sweepUploadsOften(ctx)

	id := uuid.NewString()
	tx, err := s.opts.Pool.Begin(ctx)
	if err != nil {
		return UploadSession{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, uploadsLockKey); err != nil {
		return UploadSession{}, err
	}
	// Each open upload reserves what it has not written yet, plus room for
	// the parts it may be writing at once; written parts already count
	// against the free space.
	var (
		active   int
		reserved int64
	)
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(sum(u.size_bytes + $2 * u.part_size - COALESCE(p.bytes, 0)), 0)::bigint
		FROM backup_uploads u
		LEFT JOIN LATERAL (SELECT sum(size_bytes)::bigint AS bytes FROM backup_upload_parts WHERE upload_id = u.id) p ON TRUE
		WHERE u.status IN ('uploading', 'checking') AND u.updated_at > now() - make_interval(secs => $1)`,
		UploadExpiry.Seconds(), MaxPartAttempts).Scan(&active, &reserved); err != nil {
		return UploadSession{}, err
	}
	if active >= MaxActiveUploads {
		return UploadSession{}, ErrTooManyUploads
	}
	need := reserved + size + MaxPartAttempts*partSize
	if err := s.checkUploadSpace(ctx, uint64(max(need, 0)), "the uploads in progress and this one"); err != nil {
		return UploadSession{}, err
	}
	var creator *int
	if userID > 0 {
		creator = &userID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO backup_uploads (id, status, size_bytes, part_size, declared_sha256, created_by_user_id)
		VALUES ($1, 'uploading', $2, $3, $4, $5)`, id, size, partSize, sha256, creator); err != nil {
		return UploadSession{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UploadSession{}, err
	}
	return s.Upload(ctx, id)
}

const uploadColumns = `u.id::text, u.status, u.size_bytes, u.part_size, u.declared_sha256, u.digest, u.created_at, u.updated_at, u.completed_at,
	u.source_server_id, u.source_server_name, u.source_silo_version, u.source_created_at, u.format_version, u.rejected_reason, u.rejected_message,
	COALESCE(p.n, 0), COALESCE(p.bytes, 0)
	FROM backup_uploads u
	LEFT JOIN LATERAL (SELECT count(*) AS n, sum(size_bytes)::bigint AS bytes FROM backup_upload_parts WHERE upload_id = u.id) p ON TRUE`

func (s *Service) scanUpload(row pgx.Row) (UploadSession, error) {
	var (
		u              UploadSession
		sourceID, name string
		version        string
		sourceAt       *time.Time
		format         int
		reason, msg    string
	)
	err := row.Scan(&u.ID, &u.Status, &u.SizeBytes, &u.PartSize, &u.declaredSHA256, &u.Digest, &u.CreatedAt, &u.UpdatedAt, &u.CompletedAt,
		&sourceID, &name, &version, &sourceAt, &format, &reason, &msg, &u.ReceivedParts, &u.ReceivedBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadSession{}, ErrUploadNotFound
	}
	if err != nil {
		return UploadSession{}, err
	}
	u.PartCount = partCount(u.SizeBytes, u.PartSize)
	u.Location = s.opts.Destination.Location(uploadPrefix(u.ID))
	expires := u.UpdatedAt.Add(UploadExpiry)
	if u.Status == UploadComplete {
		u.Source = &UploadSource{ServerID: sourceID, ServerName: name, SiloVersion: version, FormatVersion: format}
		if sourceAt != nil {
			u.Source.CreatedAt = *sourceAt
		}
		expires = u.UpdatedAt.Add(CompletedUploadExpiry)
	}
	u.ExpiresAt = &expires
	if u.Status == UploadRejected {
		u.Rejection = &UploadRejection{Reason: reason, Message: msg}
	}
	return u, nil
}

// checkUploadSpace requires need plus the 1 GB margin free on a local
// destination, which shares a disk with the database.
func (s *Service) checkUploadSpace(ctx context.Context, need uint64, what string) error {
	free, ok, err := s.opts.Destination.FreeBytes(ctx)
	if err != nil || !ok {
		return err
	}
	if total := need + freeSpaceMargin; free < total {
		return fmt.Errorf("%w: %s free, %s needed (%s, plus 1 GB)", ErrNotEnoughSpace, formatBytes(free), formatBytes(total), what)
	}
	return nil
}

// Upload reads one upload.
func (s *Service) Upload(ctx context.Context, id string) (UploadSession, error) {
	if !canonicalUploadID(id) {
		return UploadSession{}, ErrUploadNotFound
	}
	if err := s.uploadReady(); err != nil {
		return UploadSession{}, err
	}
	return s.scanUpload(s.opts.Pool.QueryRow(ctx, `SELECT `+uploadColumns+` WHERE u.id = $1`, id))
}

// Uploads lists this server's uploads, newest first, after deleting the
// expired ones.
func (s *Service) Uploads(ctx context.Context) ([]UploadSession, error) {
	if err := s.uploadReady(); err != nil {
		return nil, err
	}
	s.sweepUploadsOften(ctx)
	rows, err := s.opts.Pool.Query(ctx, `SELECT `+uploadColumns+` ORDER BY u.created_at DESC, u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UploadSession{}
	for rows.Next() {
		u, err := s.scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// PutUploadPart stores part n (counting from 0). Every part but the last is
// exactly the part size. Sending a part again replaces it until the upload
// is completed. length is the request's declared length, or -1.
func (s *Service) PutUploadPart(ctx context.Context, id string, n int, body io.Reader, length int64) (UploadSession, error) {
	u, err := s.Upload(ctx, id)
	if err != nil {
		return UploadSession{}, err
	}
	if u.Status != UploadUploading {
		return UploadSession{}, ErrUploadNotOpen
	}
	if n < 0 || n >= u.PartCount {
		return UploadSession{}, &UploadPartError{Message: fmt.Sprintf("part %d is outside this upload's parts 0 to %d", n, u.PartCount-1)}
	}
	want := partLength(u.SizeBytes, u.PartSize, n)
	if length >= 0 && length != want {
		return UploadSession{}, &UploadPartError{Message: fmt.Sprintf("part %d must be %d bytes, not %d", n, want, length), TooLarge: length > want}
	}
	encryptWith, err := s.uploadKey(ctx)
	if err != nil {
		return UploadSession{}, err
	}
	name := storedName(fmt.Sprintf("%05d.%s", n, strings.ToLower(rand.Text()[:16])), encryptWith != nil)
	key := uploadPrefix(id) + name
	if err := s.beginAttempt(ctx, id, n, name); err != nil {
		return UploadSession{}, err
	}
	discard := func() {
		ctx := context.WithoutCancel(ctx)
		if _, err := s.opts.Pool.Exec(ctx, `DELETE FROM backup_upload_attempts WHERE upload_id = $1 AND part_number = $2 AND object_name = $3`, id, n, name); err != nil {
			slog.WarnContext(ctx, "ending an upload part attempt failed", "component", "backup", "upload_id", id, "error", err)
		}
		if err := s.opts.Destination.Delete(ctx, key); err != nil {
			slog.WarnContext(ctx, "deleting a refused upload part failed", "component", "backup", "upload_id", id, "error", err)
		}
	}
	// Free space is checked again for every part: other writers share the
	// disk, and a full one would take the database down.
	if err := s.checkUploadSpace(ctx, uint64(want), "this part"); err != nil { //nolint:gosec // G115: a part length is positive and at most MaxUploadPartSize
		discard()
		return UploadSession{}, err
	}
	counted := &countingReader{r: io.LimitReader(body, want+1)}
	stored := io.Reader(counted)
	if encryptWith != nil {
		enc := encryptWith.encryptingReader(counted)
		defer func() { _ = enc.Close() }()
		stored = enc
	}
	write, cancelWrite := context.WithTimeout(ctx, s.partWriteTimeout())
	err = s.opts.Destination.Put(write, key, stored)
	cancelWrite()
	if err != nil {
		discard()
		return UploadSession{}, fmt.Errorf("storing part %d: %w", n, err)
	}
	if counted.n != want {
		discard()
		return UploadSession{}, &UploadPartError{Message: fmt.Sprintf("part %d must be %d bytes", n, want), TooLarge: counted.n > want}
	}
	previous, err := s.recordPart(ctx, id, n, want, name)
	if err != nil {
		discard()
		return UploadSession{}, err
	}
	// A crash before this delete leaves the replaced object; the sweep
	// deletes objects no part row names.
	if previous != "" {
		if err := s.opts.Destination.Delete(ctx, uploadPrefix(id)+previous); err != nil {
			slog.WarnContext(ctx, "deleting a replaced upload part failed", "component", "backup", "upload_id", id, "error", err)
		}
	}
	return s.Upload(ctx, id)
}

// beginAttempt reserves the write of part n as object name. Under the
// session row lock it drops abandoned attempts, refuses a part already being
// written or an upload writing MaxPartAttempts parts, and records the
// attempt, all before the part's bytes are read.
func (s *Service) beginAttempt(ctx context.Context, id string, n int, name string) error {
	tx, err := s.lockOpenUpload(ctx, id)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `DELETE FROM backup_upload_attempts WHERE upload_id = $1 AND started_at <= now() - make_interval(secs => $2)`, id, partAttemptLimit.Seconds()); err != nil {
		return err
	}
	var (
		writing int
		same    bool
	)
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(bool_or(part_number = $2), false) FROM backup_upload_attempts WHERE upload_id = $1`, id, n).Scan(&writing, &same); err != nil {
		return err
	}
	if same || writing >= MaxPartAttempts {
		return ErrPartBusy
	}
	if _, err := tx.Exec(ctx, `INSERT INTO backup_upload_attempts (upload_id, part_number, object_name) VALUES ($1, $2, $3)`, id, n, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lockOpenUpload begins a transaction holding the session row of an upload
// that is taking parts.
func (s *Service) lockOpenUpload(ctx context.Context, id string) (pgx.Tx, error) {
	tx, err := s.opts.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM backup_uploads WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if err == nil && status != UploadUploading {
		err = ErrUploadNotOpen
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrUploadNotFound
	}
	if err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return nil, err
	}
	return tx, nil
}

// recordPart points part n at object name while the upload is taking parts
// and the send's attempt is still its own and recent, and returns the object
// it replaced.
func (s *Service) recordPart(ctx context.Context, id string, n int, size int64, name string) (string, error) {
	tx, err := s.lockOpenUpload(ctx, id)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tag, err := tx.Exec(ctx, `DELETE FROM backup_upload_attempts WHERE upload_id = $1 AND part_number = $2 AND object_name = $3
		AND started_at > now() - make_interval(secs => $4)`, id, n, name, partAttemptLimit.Seconds())
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrPartAttemptExpired
	}
	var previous string
	if err := tx.QueryRow(ctx, `SELECT object_name FROM backup_upload_parts WHERE upload_id = $1 AND part_number = $2`, id, n).Scan(&previous); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO backup_upload_parts (upload_id, part_number, size_bytes, object_name) VALUES ($1, $2, $3, $4)
		ON CONFLICT (upload_id, part_number) DO UPDATE SET size_bytes = EXCLUDED.size_bytes, object_name = EXCLUDED.object_name`, id, n, size, name); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE backup_uploads SET updated_at = now() WHERE id = $1`, id); err != nil {
		return "", err
	}
	return previous, tx.Commit(ctx)
}

// CompleteUpload checks a fully received upload under the subsystem lock:
// it reads every part in order through the restore-set reader, freezes the
// SHA-256 of the bytes it read, and records what the set says about itself.
// An upload that is not a restore set this server reads is rejected and its
// parts deleted. A completed or rejected upload answers as it is.
func (s *Service) CompleteUpload(ctx context.Context, id string) (UploadSession, error) {
	u, err := s.Upload(ctx, id)
	if err != nil {
		return UploadSession{}, err
	}
	if u.Status == UploadComplete || u.Status == UploadRejected {
		return u, nil
	}
	held, err := s.opts.Locker.TryLock(ctx)
	if err != nil {
		return UploadSession{}, err
	}
	defer held.Release(context.WithoutCancel(ctx))
	ctx, cancel := withLost(ctx, held)
	defer cancel()
	keys, err := s.beginCheck(ctx, id)
	if err != nil {
		return UploadSession{}, err
	}
	op := s.beginOperation(ctx, held, ActiveOperation{Operation: OperationUpload, UploadID: id, StartedAt: s.opts.Now()})
	defer op.end(ctx, nil)

	// The read has a deadline, and the parts close when it passes, so a
	// stalled destination read cannot keep the lock.
	work, cancelWork := context.WithTimeout(ctx, s.uploadCheckTimeout())
	defer cancelWork()
	parts := s.openParts(work, keys)
	defer func() { _ = parts.Close() }()
	rs, rejection, err := s.checkUpload(work, parts, u.declaredSHA256)
	// Every outcome is written on the lock's own session (held.Exec), so it
	// fails once the lock is lost: a server that took the lock since may be
	// checking this upload itself. An upload left checking is recovered by
	// the next completion (beginCheck).
	if err != nil {
		// The bytes could not be read: the upload stays, and completing it
		// again checks it again.
		resetCtx, cancelReset := context.WithTimeout(context.WithoutCancel(ctx), uploadOutcomeTimeout)
		defer cancelReset()
		if _, resetErr := held.Exec(resetCtx, `UPDATE backup_uploads SET status = 'uploading', updated_at = now() WHERE id = $1 AND status = 'checking'`, id); resetErr != nil {
			slog.WarnContext(ctx, "reopening an upload after a failed check failed", "component", "backup", "upload_id", id, "error", resetErr)
		}
		return UploadSession{}, fmt.Errorf("reading the upload: %w", err)
	}
	if rejection != nil {
		if err := s.rejectUpload(ctx, held, id, *rejection); err != nil {
			return UploadSession{}, err
		}
		return s.Upload(ctx, id)
	}
	tag, err := held.Exec(ctx, `UPDATE backup_uploads SET status = 'complete', digest = $2, completed_at = now(), updated_at = now(),
			source_server_id = $3, source_server_name = $4, source_silo_version = $5, source_created_at = $6, format_version = $7
		WHERE id = $1 AND status = 'checking'`,
		id, rs.Digest, rs.Index.ServerID, rs.Index.ServerName, rs.Index.SiloVersion, rs.Index.CreatedAt, rs.Index.Version)
	if err != nil {
		return UploadSession{}, err
	}
	if tag.RowsAffected() == 0 {
		return UploadSession{}, ErrUploadCheckTakenOver
	}
	// The first preview needs exactly this read.
	s.restoreSets.store(rs.Digest, rs)
	return s.Upload(ctx, id)
}

// ErrUploadCheckTakenOver reports a check whose upload was no longer
// checking when its outcome was written: the lock was lost and another
// completion took the upload over.
var ErrUploadCheckTakenOver = errors.New("the upload is no longer being checked here; complete it again")

// uploadOutcomeTimeout bounds the write that reopens an upload after a check
// that could not read it, which runs after the check's own deadline.
const uploadOutcomeTimeout = 10 * time.Second

// partWriteTimeout is partWriteTimeout, or a shorter test override; never
// longer, so partAttemptLimit always outlasts a write.
func (s *Service) partWriteTimeout() time.Duration {
	if s.opts.UploadPartWriteTimeout > 0 && s.opts.UploadPartWriteTimeout < partWriteTimeout {
		return s.opts.UploadPartWriteTimeout
	}
	return partWriteTimeout
}

func (s *Service) uploadCheckTimeout() time.Duration {
	if s.opts.UploadCheckTimeout > 0 {
		return s.opts.UploadCheckTimeout
	}
	return uploadCheckTimeout
}

// beginCheck marks the upload checking, which stops further parts, and
// returns its part objects in order. Under the lock no other completion
// runs, so an upload found checking was left by a server that stopped.
func (s *Service) beginCheck(ctx context.Context, id string) ([]string, error) {
	tx, err := s.opts.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var (
		status         string
		size, partSize int64
	)
	err = tx.QueryRow(ctx, `SELECT status, size_bytes, part_size FROM backup_uploads WHERE id = $1 FOR UPDATE`, id).Scan(&status, &size, &partSize)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUploadNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != UploadUploading && status != UploadChecking {
		return nil, ErrUploadNotOpen
	}
	rows, err := tx.Query(ctx, `SELECT part_number, size_bytes, object_name FROM backup_upload_parts WHERE upload_id = $1 ORDER BY part_number`, id)
	if err != nil {
		return nil, err
	}
	type part struct {
		n    int
		size int64
		name string
	}
	var parts []part
	for rows.Next() {
		var p part
		if err := rows.Scan(&p.n, &p.size, &p.name); err != nil {
			rows.Close()
			return nil, err
		}
		parts = append(parts, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if missing := partCount(size, partSize) - len(parts); missing > 0 {
		return nil, &UploadIncompleteError{Missing: missing}
	}
	keys := make([]string, 0, len(parts))
	for i, p := range parts {
		if p.n != i || p.size != partLength(size, partSize, i) {
			return nil, fmt.Errorf("upload part %d is not what the upload needs", p.n)
		}
		keys = append(keys, uploadPrefix(id)+p.name)
	}
	if _, err := tx.Exec(ctx, `UPDATE backup_uploads SET status = 'checking', updated_at = now() WHERE id = $1`, id); err != nil {
		return nil, err
	}
	return keys, tx.Commit(ctx)
}

// openUploaded reads an uploaded restore set: decrypted with this server's
// key when it is an age file, as stored otherwise. A file encrypted with
// another key returns ErrWrongKey.
func (s *Service) openUploaded(parts *partsReader) (*storedReader, error) {
	br := bufio.NewReaderSize(parts, 64<<10)
	head, _ := br.Peek(64)
	if parts.err != nil {
		_ = parts.Close()
		return nil, parts.err
	}
	var key *ArchiveKey
	if isAgeHeader(head) {
		if s.opts.ArchiveKey == nil {
			_ = parts.Close()
			return nil, ErrWrongKey
		}
		key = s.opts.ArchiveKey
	}
	return openStored(readCloser{Reader: br, Closer: parts}, key)
}

type readCloser struct {
	io.Reader
	io.Closer
}

// checkUpload reads an upload through the restore-set reader, decrypting
// one encrypted with this server's key. It returns the set, or why the
// upload is rejected, or an error when the bytes could not be read, which
// rejects nothing.
func (s *Service) checkUpload(ctx context.Context, parts *partsReader, declared string) (*RestoreSet, *UploadRejection, error) {
	body, err := s.openUploaded(parts)
	switch {
	case parts.err != nil:
		return nil, nil, parts.err
	case errors.Is(err, ErrWrongKey):
		return nil, &UploadRejection{Reason: RejectEncrypted, Message: EncryptedUploadMessage}, nil
	case err != nil:
		return nil, &UploadRejection{Reason: RejectNotRestoreSet, Message: truncateRejection(err.Error())}, nil
	}
	rs, err := readStoredRestoreSet(ctx, body, ReadOptions{})
	switch {
	case parts.err != nil:
		return nil, nil, parts.err
	case ctx.Err() != nil:
		return nil, nil, context.Cause(ctx)
	case errors.Is(err, ErrRestoreSetTooNew):
		return nil, &UploadRejection{Reason: RejectNewerSilo, Message: ErrRestoreSetTooNew.Error()}, nil
	case err != nil:
		return nil, &UploadRejection{Reason: RejectNotRestoreSet, Message: truncateRejection(err.Error())}, nil
	case declared != "" && declared != rs.Digest:
		return nil, &UploadRejection{Reason: RejectChecksum, Message: "The file that arrived does not match the SHA-256 sent when the upload started."}, nil
	}
	return rs, nil, nil
}

func truncateRejection(message string) string {
	if len(message) > maxRejectionMessage {
		return message[:maxRejectionMessage]
	}
	return message
}

// rejectUpload records why an upload is unusable and deletes its parts.
// The row stays until the sweep, so completing it again answers the same.
func (s *Service) rejectUpload(ctx context.Context, held Held, id string, rejection UploadRejection) error {
	// One statement on the lock's session, so it cannot land after the lock
	// was lost. A checked upload has parts, so deleting none means the
	// upload was no longer this check's to reject.
	tag, err := held.Exec(ctx, `WITH rejected AS (
			UPDATE backup_uploads SET status = 'rejected', rejected_reason = $2, rejected_message = $3, updated_at = now()
			WHERE id = $1 AND status = 'checking' RETURNING id)
		DELETE FROM backup_upload_parts WHERE upload_id IN (SELECT id FROM rejected)`, id, rejection.Reason, rejection.Message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUploadCheckTakenOver
	}
	if err := s.opts.Destination.DeletePrefix(ctx, uploadPrefix(id)); err != nil {
		slog.WarnContext(ctx, "deleting a rejected upload's parts failed", "component", "backup", "upload_id", id, "error", err)
	}
	return nil
}

// partsReader reads an upload's parts in order as one stream. err records
// a failure to read the destination, which says nothing about the upload.
// Close may run while a Read waits, from the context's AfterFunc; it closes
// the part being read, which ends that Read.
type partsReader struct {
	open func(key string) (io.ReadCloser, error)
	stop func() bool
	keys []string
	err  error

	mu     sync.Mutex
	cur    io.ReadCloser
	closed bool
}

// errPartsClosed ends a read of parts that were closed.
var errPartsClosed = errors.New("the upload's parts were closed")

// openParts reads keys from the destination in order, and closes them when
// ctx ends. A part stored encrypted (named .age) is decrypted, so the
// stream is the bytes the client sent.
func (s *Service) openParts(ctx context.Context, keys []string) *partsReader {
	r := &partsReader{keys: keys, open: func(key string) (io.ReadCloser, error) {
		body, _, err := s.opts.Destination.Open(ctx, key)
		if err != nil || !strings.HasSuffix(key, EncryptedSuffix) {
			return body, err
		}
		if s.opts.ArchiveKey == nil {
			_ = body.Close()
			return nil, ErrWrongKey
		}
		plain, err := s.opts.ArchiveKey.Decrypt(body)
		if err != nil {
			_ = body.Close()
			return nil, err
		}
		return readCloser{Reader: plain, Closer: body}, nil
	}}
	r.stop = context.AfterFunc(ctx, func() { _ = r.Close() })
	return r
}

// current returns the part being read, opening the next one when none is.
// It returns nil at the end of the parts.
func (r *partsReader) current() (io.ReadCloser, error) {
	r.mu.Lock()
	closed, cur := r.closed, r.cur
	r.mu.Unlock()
	if closed {
		return nil, errPartsClosed
	}
	if cur != nil || len(r.keys) == 0 {
		return cur, nil
	}
	body, err := r.open(r.keys[0])
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", r.keys[0], err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		_ = body.Close()
		return nil, errPartsClosed
	}
	r.cur, r.keys = body, r.keys[1:]
	return body, nil
}

func (r *partsReader) Read(p []byte) (int, error) {
	for {
		if r.err != nil {
			return 0, r.err
		}
		cur, err := r.current()
		if err != nil {
			r.err = err
			return 0, err
		}
		if cur == nil {
			return 0, io.EOF
		}
		n, err := cur.Read(p)
		if errors.Is(err, io.EOF) {
			r.mu.Lock()
			if r.cur == cur {
				r.cur = nil
			}
			r.mu.Unlock()
			_ = cur.Close()
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			r.err = err
		}
		return n, err
	}
}

func (r *partsReader) Close() error {
	r.mu.Lock()
	cur := r.cur
	r.cur, r.closed = nil, true
	r.mu.Unlock()
	if r.stop != nil {
		r.stop()
	}
	if cur != nil {
		return cur.Close()
	}
	return nil
}

// completedUpload returns a checked upload's digest and its part objects
// in order.
func (s *Service) completedUpload(ctx context.Context, id string) (string, []string, error) {
	u, err := s.Upload(ctx, id)
	if err != nil {
		return "", nil, err
	}
	if u.Status != UploadComplete {
		return "", nil, ErrUploadNotReady
	}
	// A preview or restore is a use: the upload's expiry starts again.
	if _, err := s.opts.Pool.Exec(ctx, `UPDATE backup_uploads SET updated_at = now() WHERE id = $1 AND status = 'complete'`, id); err != nil {
		return "", nil, err
	}
	rows, err := s.opts.Pool.Query(ctx, `SELECT object_name FROM backup_upload_parts WHERE upload_id = $1 ORDER BY part_number`, id)
	if err != nil {
		return "", nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", nil, err
	}
	keys := make([]string, 0, len(names))
	for _, name := range names {
		keys = append(keys, uploadPrefix(id)+name)
	}
	return u.Digest, keys, nil
}
