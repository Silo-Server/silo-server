package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Follow-ups: work a restore owes after an item commits, recorded in the
// item's transaction and finished after it.

// Follow-up kinds, as backup_restore_followups records them.
const (
	followUpLibraryCreated  = "library_created"
	followUpPluginInstalled = "plugin_installed"
	// followUpLibrarySections seeds a restored library's default and home
	// sections where the restore wrote none. It runs once the restore's
	// items are done (finishFollowUps), never after the library's own
	// item: a saved section the restore writes later, renamed from its
	// default, would otherwise sit beside a fresh default.
	followUpLibrarySections = "library_sections"
	// followUpCollectionCreated stores a restored template collection's
	// template poster and syncs it; target_id is the collection id.
	followUpCollectionCreated = "collection_created"
)

// recordFollowUp notes, in the item's transaction, work owed after commit.
func recordFollowUp(ctx context.Context, tx pgx.Tx, kind string, id int) error {
	if _, err := tx.Exec(ctx, `INSERT INTO backup_restore_followups (kind, target_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, kind, id); err != nil {
		return fmt.Errorf("recording follow-up work: %w", err)
	}
	return nil
}

// CompleteFollowUps runs every follow-up a restore recorded and has not
// finished, this one's or an earlier one that stopped between an item's
// commit and its follow-up, deleting each row only once its work succeeded.
// Only work a restore recorded runs: a library or plugin that exists for any
// other reason is never set up again. A follow-up whose target is gone is
// dropped; one that fails stays for the next run. A restore job calls it
// before its first item and after each item that recorded one. The ones
// that wait for the end of a restore (followUpLibrarySections) wait for
// finishFollowUps.
func (s *Service) CompleteFollowUps(ctx context.Context) { s.completeFollowUps(ctx, false) }

// finishFollowUps is CompleteFollowUps at the end of a restore, the
// follow-ups that wait for it included. ExecuteRestore calls it once, after
// its last item.
func (s *Service) finishFollowUps(ctx context.Context) { s.completeFollowUps(ctx, true) }

func (s *Service) completeFollowUps(ctx context.Context, final bool) {
	rows, err := s.opts.Pool.Query(ctx, `
		SELECT f.kind, f.target_id,
			CASE f.kind
				WHEN 'library_created' THEN EXISTS (SELECT 1 FROM media_folders m WHERE m.id = f.target_id)
				WHEN 'library_sections' THEN EXISTS (SELECT 1 FROM media_folders m WHERE m.id = f.target_id)
				WHEN 'collection_created' THEN EXISTS (SELECT 1 FROM library_collections c WHERE c.id = f.target_id::text)
				ELSE EXISTS (SELECT 1 FROM plugin_installations p WHERE p.id = f.target_id)
			END
		FROM backup_restore_followups f ORDER BY f.created_at, f.kind, f.target_id`)
	if err != nil {
		slog.WarnContext(ctx, "reading restore follow-ups failed", "component", "backup", "error", err)
		return
	}
	type followUp struct {
		kind   string
		id     int
		exists bool
	}
	var pending []followUp
	for rows.Next() {
		var f followUp
		if err := rows.Scan(&f.kind, &f.id, &f.exists); err != nil {
			rows.Close()
			slog.WarnContext(ctx, "reading restore follow-ups failed", "component", "backup", "error", err)
			return
		}
		pending = append(pending, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.WarnContext(ctx, "reading restore follow-ups failed", "component", "backup", "error", err)
		return
	}
	for _, f := range pending {
		if f.kind == followUpLibrarySections && !final {
			continue
		}
		if f.exists {
			var err error
			switch f.kind {
			case followUpLibraryCreated:
				err = callFollowUp(ctx, s.targets.LibraryCreated, f.id)
			case followUpLibrarySections:
				err = callFollowUp(ctx, s.targets.LibrarySectionsCreated, f.id)
			case followUpPluginInstalled:
				err = callFollowUp(ctx, s.targets.PluginInstalled, f.id)
			case followUpCollectionCreated:
				err = callFollowUp(ctx, func(ctx context.Context, id int) error {
					if s.targets.TemplateCollectionCreated == nil {
						return errors.New("this server cannot finish a restored template collection")
					}
					return s.targets.TemplateCollectionCreated(ctx, strconv.Itoa(id))
				}, f.id)
			}
			if err != nil {
				slog.WarnContext(ctx, "restore follow-up failed; the next restore retries it", "component", "backup", "kind", f.kind, "id", f.id, "error", err)
				continue
			}
		}
		if _, err := s.opts.Pool.Exec(ctx, `DELETE FROM backup_restore_followups WHERE kind = $1 AND target_id = $2`, f.kind, f.id); err != nil {
			slog.WarnContext(ctx, "clearing a restore follow-up failed", "component", "backup", "kind", f.kind, "id", f.id, "error", err)
		}
	}
}

func callFollowUp(ctx context.Context, fn func(context.Context, int) error, id int) error {
	if fn == nil {
		return errors.New("this server cannot run restore follow-ups")
	}
	return fn(ctx, id)
}
