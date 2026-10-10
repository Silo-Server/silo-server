package access

import (
	"context"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/jackc/pgx/v5"
)

const auditFieldDefault = "is_default"

func groupAuditValues(group *Group) map[string]any {
	if group == nil {
		return nil
	}
	return map[string]any{
		"name": group.Name, "description": group.Description, "library_ids": group.LibraryIDs,
		"max_playback_quality": group.MaxPlaybackQuality, "download_allowed": group.DownloadAllowed,
		"download_transcode_allowed": group.DownloadTranscodeAllowed, "transcode_allowed": group.TranscodeAllowed,
		"audio_transcode_allowed": group.AudioTranscodeAllowed, "max_streams": group.MaxStreams,
		"max_transcodes": group.MaxTranscodes, "max_remote_stream_bitrate_kbps": group.MaxRemoteStreamBitrateKbps,
		"max_local_stream_bitrate_kbps": group.MaxLocalStreamBitrateKbps, "allowed_permissions": group.AllowedPermissions,
		"requests_allowed": group.RequestsAllowed, auditFieldDefault: group.IsDefault,
	}
}
func recordGroupMutation(ctx context.Context, tx pgx.Tx, before, after *Group) (*auditmutation.Entry, error) {
	target, action, status := before, "access_group.updated", 200
	if before == nil {
		target, action, status = after, "access_group.created", 201
	}
	if after == nil {
		action, status = "access_group.deleted", 204
	}
	return auditmutation.RecordMutation(ctx, tx, action, "access_group", strconv.FormatInt(target.ID, 10), status,
		auditmutation.Changes(groupAuditValues(before), groupAuditValues(after)))
}

// clearPreviousDefaults records the implicit demotion caused by promoting a
// different group. The writer lock is already held by the caller.
func clearPreviousDefaults(ctx context.Context, tx pgx.Tx, except int64, status int) ([]*auditmutation.Entry, error) {
	rows, err := tx.Query(ctx, `UPDATE access_groups SET is_default=false WHERE is_default AND id<>$1 RETURNING id`, except)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var audits []*auditmutation.Entry
	for _, id := range ids {
		entry, err := auditmutation.RecordMutation(ctx, tx, "access_group.updated", "access_group", strconv.FormatInt(id, 10), status,
			auditmutation.Changes(map[string]any{auditFieldDefault: true}, map[string]any{auditFieldDefault: false}))
		if err != nil {
			return nil, err
		}
		audits = append(audits, entry)
	}
	return audits, nil
}
