package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/pressly/goose/v3"

	"github.com/Silo-Server/silo-server/internal/naming"
)

// externalSubtitleNameBackfillVersion sorts after every migration that shipped
// before sidecar subtitle names were read for a language in any position and
// for hearing-impaired words.
const externalSubtitleNameBackfillVersion int64 = 20261014033149

// externalSubtitleNameBackfillBatchSize bounds how many media_files rows with
// sidecars one batch reads and rewrites.
const externalSubtitleNameBackfillBatchSize = 5000

// externalSubtitleNameBackfillMaxPasses bounds the convergence passes for the
// same reason as subtitleLanguageBackfillMaxPasses.
const externalSubtitleNameBackfillMaxPasses = 3

// externalSubtitleNameBackfillMigration re-reads the language and
// hearing-impaired flag of every stored sidecar subtitle from its file name,
// with the naming.SidecarSubtitleTags rule the scanner now applies.
//
// A rescan does not do this on its own: the scanner rewrites a file's sidecars
// only when the video or the set of sidecar paths changes. Without this
// repair, "Movie.sdh.en.srt" in an existing library keeps the language "sdh"
// and no hearing-impaired flag until its files change. A sidecar's language
// and flags come only from its path, so re-reading them stores exactly what a
// fresh scan of the same files would.
//
// Go rather than SQL because the rule checks each word against the registered
// language list in golang.org/x/text. RunDB rather than RunTx so each batch
// commits on its own.
func externalSubtitleNameBackfillMigration() *goose.Migration {
	m := goose.NewGoMigration(
		externalSubtitleNameBackfillVersion,
		&goose.GoFunc{RunDB: backfillExternalSubtitleNames},
		// The values the earlier parser stored are not recorded, so there is
		// nothing to restore.
		&goose.GoFunc{RunDB: func(context.Context, *sql.DB) error { return nil }},
	)
	m.Source = fmt.Sprintf("%d_external_subtitle_name_backfill.go", externalSubtitleNameBackfillVersion)
	return m
}

// backfillExternalSubtitleNames repeats the repair until a pass finds nothing
// to repair, so rows an older writer stored mid-pass are picked up too.
func backfillExternalSubtitleNames(ctx context.Context, db *sql.DB) error {
	for pass := 1; pass <= externalSubtitleNameBackfillMaxPasses; pass++ {
		stale, err := backfillExternalSubtitleNamesPass(ctx, db, pass)
		if err != nil {
			return err
		}
		if stale == 0 {
			return nil
		}
	}
	return fmt.Errorf("sidecar subtitle name repair still found stale rows after %d passes; retry after older writers stop", externalSubtitleNameBackfillMaxPasses)
}

type externalSubtitleNameRow struct {
	id        int64
	filePath  string
	subtitles []byte
}

// externalSubtitleNameRepair is one row's rewrite. Stored is the value the
// rewrite was computed from; the update applies only while it is unchanged.
type externalSubtitleNameRepair struct {
	ID       int64           `json:"id"`
	Stored   json.RawMessage `json:"stored"`
	Repaired json.RawMessage `json:"repaired"`
}

// backfillExternalSubtitleNamesPass repairs every row once and returns how many
// rows needed a repair, including rows skipped because they changed after they
// were read.
func backfillExternalSubtitleNamesPass(ctx context.Context, db *sql.DB, pass int) (int64, error) {
	var lastID, stale, changed int64
	batches := 0
	for {
		rows, err := loadExternalSubtitleNameBatch(ctx, db, lastID)
		if err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			break
		}
		lastID = rows[len(rows)-1].id

		var repairs []externalSubtitleNameRepair
		for _, row := range rows {
			repaired, ok, err := repairExternalSubtitleNames(row.filePath, row.subtitles)
			if err != nil {
				slog.WarnContext(ctx, "sidecar subtitle name repair: leaving a row it cannot decode untouched",
					"media_file_id", row.id, "error", err)
				continue
			}
			if ok {
				repairs = append(repairs, externalSubtitleNameRepair{ID: row.id, Stored: row.subtitles, Repaired: repaired})
			}
		}
		n, err := applyExternalSubtitleNameRepairs(ctx, db, repairs)
		if err != nil {
			return 0, err
		}
		stale += int64(len(repairs))
		changed += n
		batches++
		if n > 0 {
			slog.InfoContext(ctx, "sidecar subtitle name repair batch committed",
				"pass", pass, "batch", batches, "through_media_file_id", lastID, "rows", n)
		}
	}
	slog.InfoContext(ctx, "sidecar subtitle name repair pass finished",
		"pass", pass, "batches", batches, "rows", changed, "skipped_changed_rows", stale-changed)
	return stale, nil
}

// loadExternalSubtitleNameBatch returns the next rows after lastID that have at
// least one stored sidecar.
func loadExternalSubtitleNameBatch(ctx context.Context, db *sql.DB, lastID int64) ([]externalSubtitleNameRow, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, file_path, external_subtitles
  FROM public.media_files
 WHERE id > $1
   AND jsonb_typeof(external_subtitles) = 'array'
   AND external_subtitles <> '[]'::jsonb
 ORDER BY id
 LIMIT $2`, lastID, externalSubtitleNameBackfillBatchSize)
	if err != nil {
		return nil, fmt.Errorf("loading sidecar subtitles after media_files id %d: %w", lastID, err)
	}
	defer func() { _ = rows.Close() }()

	var batch []externalSubtitleNameRow
	for rows.Next() {
		var row externalSubtitleNameRow
		if err := rows.Scan(&row.id, &row.filePath, &row.subtitles); err != nil {
			return nil, fmt.Errorf("scanning sidecar subtitles after media_files id %d: %w", lastID, err)
		}
		batch = append(batch, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loading sidecar subtitles after media_files id %d: %w", lastID, err)
	}
	return batch, nil
}

// repairExternalSubtitleNames re-reads each sidecar's language and
// hearing-impaired flag from its path and reports whether any changed. Every
// other field is kept, as are elements without a path or whose name does not
// belong to the video.
func repairExternalSubtitleNames(videoPath string, stored []byte) ([]byte, bool, error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(stored, &elems); err != nil {
		return nil, false, err
	}
	changed := false
	for i, raw := range elems {
		var current struct {
			Path            string `json:"path"`
			Language        string `json:"language"`
			HearingImpaired bool   `json:"hearing_impaired"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &current) != nil || json.Unmarshal(raw, &fields) != nil || current.Path == "" {
			continue
		}
		tags, ok := naming.SidecarSubtitleTags(videoPath, current.Path)
		if !ok || (tags.Language == current.Language && tags.HearingImpaired == current.HearingImpaired) {
			continue
		}
		language, err := json.Marshal(tags.Language)
		if err != nil {
			return nil, false, err
		}
		fields["language"] = language
		fields["hearing_impaired"] = json.RawMessage(strconv.FormatBool(tags.HearingImpaired))
		if elems[i], err = json.Marshal(fields); err != nil {
			return nil, false, err
		}
		changed = true
	}
	if !changed {
		return nil, false, nil
	}
	repaired, err := json.Marshal(elems)
	if err != nil {
		return nil, false, err
	}
	return repaired, true, nil
}

// applyExternalSubtitleNameRepairs writes a batch of rewrites in one statement.
// A row whose sidecars changed since they were read is skipped; the next pass
// reads it again.
func applyExternalSubtitleNameRepairs(ctx context.Context, db *sql.DB, repairs []externalSubtitleNameRepair) (int64, error) {
	if len(repairs) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(repairs)
	if err != nil {
		return 0, fmt.Errorf("encoding sidecar subtitle repairs: %w", err)
	}
	result, err := db.ExecContext(ctx, `
UPDATE public.media_files mf
   SET external_subtitles = r.repaired
  FROM jsonb_to_recordset($1::jsonb) AS r(id bigint, stored jsonb, repaired jsonb)
 WHERE mf.id = r.id
   AND mf.external_subtitles = r.stored`, string(payload))
	if err != nil {
		return 0, fmt.Errorf("writing sidecar subtitle repairs through media_files id %d: %w", repairs[len(repairs)-1].ID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting sidecar subtitle repairs: %w", err)
	}
	return n, nil
}
