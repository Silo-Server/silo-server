package backup

// Deleting uploads, and sweeping the expired and orphaned ones.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DeleteUpload deletes an upload and its parts. An unfinished or rejected
// upload, which no restore can name, is deleted at once; a completed one
// only under the subsystem lock and when no queued or running restore reads
// it. Restores are queued under the same lock (StartRestore), so none is
// queued between the check and the delete.
func (s *Service) DeleteUpload(ctx context.Context, id string) error {
	u, err := s.Upload(ctx, id)
	if err != nil {
		return err
	}
	if u.Status == UploadUploading || u.Status == UploadRejected {
		tag, err := s.opts.Pool.Exec(ctx, `DELETE FROM backup_uploads WHERE id = $1 AND status IN ('uploading', 'rejected')`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			s.deleteUploadParts(ctx, id)
			return nil
		}
		// It was completed meanwhile.
	}
	held, err := s.opts.Locker.TryLock(ctx)
	if err != nil {
		return err
	}
	defer held.Release(context.WithoutCancel(ctx))
	ctx, cancel := withLost(ctx, held)
	defer cancel()
	if active, err := s.activeRestoreSource(ctx); err != nil {
		return err
	} else if active.uploadID == id {
		return ErrUploadInUse
	}
	op := s.beginOperation(ctx, held, ActiveOperation{Operation: OperationDelete, UploadID: id, StartedAt: s.opts.Now()})
	defer op.end(ctx, nil)
	// On the lock's own session: after the lock is lost another server may
	// be queueing a restore from this upload, and the delete must fail.
	tag, err := held.Exec(ctx, `DELETE FROM backup_uploads WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUploadNotFound
	}
	s.deleteUploadParts(ctx, id)
	return nil
}

// deleteUploadParts deletes the objects of an upload whose row is gone. A
// failure leaves them for the nightly sweep.
func (s *Service) deleteUploadParts(ctx context.Context, id string) {
	if err := s.opts.Destination.DeletePrefix(ctx, uploadPrefix(id)); err != nil {
		slog.WarnContext(ctx, "deleting an upload's parts failed", "component", "backup", "upload_id", id, "error", err)
	}
}

// sweepUploadsOften runs the quick sweep, and the full one when the last
// full sweep on this server is older than uploadObjectSweepInterval, so
// orphaned objects are reclaimed even when nightly backups are off.
func (s *Service) sweepUploadsOften(ctx context.Context) {
	s.uploadSweepMu.Lock()
	full := s.opts.Now().Sub(s.lastUploadSweep) >= uploadObjectSweepInterval
	if full {
		s.lastUploadSweep = s.opts.Now()
	}
	s.uploadSweepMu.Unlock()
	if _, warnings := s.sweepUploads(ctx, full, nil); len(warnings) > 0 {
		slog.WarnContext(ctx, "sweeping uploads", "component", "backup", "warnings", warnings)
	}
}

// sweepUploads deletes uploads unfinished or rejected for UploadExpiry and
// checked uploads unused for CompletedUploadExpiry, with their parts. A
// checking or complete session is deleted only under the subsystem lock:
// locked says the caller holds it; otherwise the sweep takes it, without
// waiting, when such a session is due, and leaves them all when it is busy.
// Under the lock no completion is reading a session, no restore is running
// from it, and none is being queued (StartRestore); the one a queued
// restore names is kept. Deleting a session row is atomic against a
// completion (which moves it to checking first) and a part write (which
// requires the row).
//
// With full it also lists the upload folders and deletes objects older than
// uploadObjectGrace that no part row or attempt names: a part a crash kept
// from being deleted when it was replaced, or the folder of a session that
// is gone (a failed delete, or a full restore's database that no longer
// knows it).
func (s *Service) sweepUploads(ctx context.Context, full bool, held Held) (swept, warnings []string) {
	if s.opts.Pool == nil || s.opts.Destination == nil {
		return nil, nil
	}
	ids, warnings := s.deleteDueUploads(ctx, held)
	for _, id := range ids {
		if err := s.opts.Destination.DeletePrefix(ctx, uploadPrefix(id)); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not delete expired upload %s: %v", id, err))
			continue
		}
		swept = append(swept, "uploads/"+id)
	}
	if !full {
		return swept, warnings
	}
	reclaimed, more := s.sweepUploadObjects(ctx)
	return append(swept, reclaimed...), append(warnings, more...)
}

// deleteDueUploads deletes the due session rows and returns their ids.
// held is the subsystem lock when the caller holds it; otherwise a lock it
// takes is released before the parts are deleted: with the rows gone,
// nothing can name them.
func (s *Service) deleteDueUploads(ctx context.Context, held Held) (ids, warnings []string) {
	// Sessions unfinished or rejected too long need no lock.
	rows, err := s.opts.Pool.Query(ctx, `DELETE FROM backup_uploads
		WHERE status IN ('uploading', 'rejected') AND updated_at < now() - make_interval(secs => $1)
		RETURNING id::text`, UploadExpiry.Seconds())
	if err == nil {
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil {
		return nil, []string{"sweeping uploads: " + err.Error()}
	}
	if held == nil && s.opts.Locker != nil {
		taken, err := s.lockForUploadSweep(ctx)
		if err != nil {
			warnings = append(warnings, "sweeping checked uploads: "+err.Error())
		}
		if taken != nil {
			defer taken.Release(context.WithoutCancel(ctx))
			var cancel context.CancelFunc
			ctx, cancel = withLost(ctx, taken)
			defer cancel()
			held = taken
		}
	}
	if held == nil {
		return ids, warnings
	}
	checked, more := s.deleteDueCheckedUploads(ctx, held)
	return append(ids, checked...), append(warnings, more...)
}

// deleteDueCheckedUploads deletes due checking and complete sessions, which
// only the lock's holder may, on the lock's own session (held.Exec): once
// the lock is lost another server may hold it and be queueing a restore
// from one, so the delete then fails instead. The one an active restore
// names is kept.
func (s *Service) deleteDueCheckedUploads(ctx context.Context, held Held) (ids, warnings []string) {
	active, err := s.activeRestoreSource(ctx)
	if err != nil {
		return nil, []string{err.Error()}
	}
	const due = `((status = 'checking' AND updated_at < now() - make_interval(secs => $1))
		OR (status = 'complete' AND updated_at < now() - make_interval(secs => $2) AND id::text <> $3))`
	args := []any{UploadExpiry.Seconds(), CompletedUploadExpiry.Seconds(), active.uploadID}
	candidates, err := s.uploadIDs(ctx, `SELECT id::text FROM backup_uploads WHERE `+due, args...)
	if err != nil || len(candidates) == 0 {
		return nil, errorWarnings("sweeping checked uploads", err)
	}
	if _, err := held.Exec(ctx, `DELETE FROM backup_uploads WHERE id::text = ANY($4) AND `+due, append(args, candidates)...); err != nil {
		return nil, errorWarnings("sweeping checked uploads", err)
	}
	// Under the lock a deleted session cannot come back, so the candidates
	// still there are the ones the delete kept.
	kept, err := s.uploadIDs(ctx, `SELECT id::text FROM backup_uploads WHERE id::text = ANY($1)`, candidates)
	if err != nil {
		return nil, errorWarnings("sweeping checked uploads", err)
	}
	for _, id := range candidates {
		if !slices.Contains(kept, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s *Service) uploadIDs(ctx context.Context, sql string, args ...any) ([]string, error) {
	rows, err := s.opts.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func errorWarnings(what string, err error) []string {
	if err == nil {
		return nil
	}
	return []string{what + ": " + err.Error()}
}

// lockForUploadSweep takes the subsystem lock for a sweep that does not
// hold it, only when a checking or complete session is due, so listing
// uploads does not take the lock (and make a nightly backup skip) for
// nothing. It returns nil, and no error, when nothing is due or the lock is
// busy.
func (s *Service) lockForUploadSweep(ctx context.Context) (Held, error) {
	var due bool
	if err := s.opts.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backup_uploads WHERE
			(status = 'checking' AND updated_at < now() - make_interval(secs => $1))
			OR (status = 'complete' AND updated_at < now() - make_interval(secs => $2)))`,
		UploadExpiry.Seconds(), CompletedUploadExpiry.Seconds()).Scan(&due); err != nil || !due {
		return nil, err
	}
	held, err := s.opts.Locker.TryLock(ctx)
	if errors.Is(err, ErrBusy) {
		return nil, nil
	}
	return held, err
}

// sweepUploadObjects deletes upload objects older than uploadObjectGrace
// that nothing names. The objects are listed before the rows are read, and
// a send can record its object only within partAttemptLimit of its
// attempt, so an object this finds unnamed and old can never be named
// later.
func (s *Service) sweepUploadObjects(ctx context.Context) (swept, warnings []string) {
	objects, err := s.opts.Destination.List(ctx, uploadsPrefix)
	if err != nil {
		return nil, []string{"listing uploads: " + err.Error()}
	}
	type object struct {
		name string
		at   time.Time
	}
	byUpload := map[string][]object{}
	for _, obj := range objects {
		id, name, ok := strings.Cut(strings.TrimPrefix(obj.Key, uploadsPrefix), "/")
		if ok && canonicalUploadID(id) {
			byUpload[id] = append(byUpload[id], object{name: name, at: obj.ModTime})
		}
	}
	if len(byUpload) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(byUpload))
	for id := range byUpload {
		ids = append(ids, id)
	}
	rows, err := s.opts.Pool.Query(ctx, `SELECT u.id::text,
			ARRAY(SELECT object_name FROM backup_upload_parts WHERE upload_id = u.id),
			ARRAY(SELECT object_name FROM backup_upload_attempts WHERE upload_id = u.id)
		FROM backup_uploads u WHERE u.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, []string{"sweeping upload parts: " + err.Error()}
	}
	named := map[string]map[string]bool{}
	for rows.Next() {
		var (
			id              string
			parts, attempts []string
		)
		if err := rows.Scan(&id, &parts, &attempts); err != nil {
			rows.Close()
			return nil, []string{"sweeping upload parts: " + err.Error()}
		}
		named[id] = map[string]bool{}
		for _, name := range append(parts, attempts...) {
			named[id][name] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, []string{"sweeping upload parts: " + err.Error()}
	}
	cutoff := s.opts.Now().Add(-uploadObjectGrace)
	for id, list := range byUpload {
		names, known := named[id]
		var stale []string
		recent := false
		for _, obj := range list {
			switch {
			case obj.at.After(cutoff):
				recent = true
			case !names[obj.name]:
				stale = append(stale, uploadPrefix(id)+obj.name)
			}
		}
		var err error
		switch {
		case !known && !recent:
			err = s.opts.Destination.DeletePrefix(ctx, uploadPrefix(id))
		case len(stale) > 0:
			err = s.opts.Destination.Delete(ctx, stale...)
		default:
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not delete unused objects of upload %s: %v", id, err))
			continue
		}
		swept = append(swept, "uploads/"+id)
	}
	return swept, warnings
}
