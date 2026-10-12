package auditmutation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
)

const auditFieldLibraryIDs = "library_ids"

// Change contains canonical JSON representations of safe policy values. Secret
// changes have only a field name: no password, hash, token or request body.
type Change struct {
	Field  string  `json:"field"`
	Before *string `json:"before,omitempty"`
	After  *string `json:"after,omitempty"`
}

// Changes compares explicitly allowlisted values in stable field order.
func Changes(before, after map[string]any) []Change {
	keys := map[string]bool{}
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	names := make([]string, 0, len(keys))
	for key := range keys {
		names = append(names, key)
	}
	sort.Strings(names)
	result := make([]Change, 0, len(names))
	for _, key := range names {
		old, oldOK := before[key]
		next, nextOK := after[key]
		if oldOK == nextOK && reflect.DeepEqual(old, next) {
			continue
		}
		change := Change{Field: key}
		if oldOK {
			raw, _ := json.Marshal(old)
			change.Before = new(string(raw))
		}
		if nextOK {
			raw, _ := json.Marshal(next)
			change.After = new(string(raw))
		}
		result = append(result, change)
	}
	return result
}

// UserValues is the account audit allowlist. Never serialize models.User itself.
func UserValues(user *models.User) map[string]any {
	if user == nil {
		return nil
	}
	return map[string]any{
		"username": user.Username, "email": user.Email, "role": user.Role,
		"permissions": user.Permissions, "enabled": user.Enabled, "access_group_id": user.AccessGroupID,
		auditFieldLibraryIDs: user.LibraryIDs, "max_playback_quality": user.MaxPlaybackQuality,
		"max_streams": user.MaxStreams, "max_transcodes": user.MaxTranscodes,
		"max_remote_stream_bitrate_kbps": user.MaxRemoteStreamBitrateKbps,
		"max_local_stream_bitrate_kbps":  user.MaxLocalStreamBitrateKbps,
		"transcode_allowed":              user.TranscodeAllowed, "audio_transcode_allowed": user.AudioTranscodeAllowed,
		"download_allowed": user.DownloadAllowed, "download_transcode_allowed": user.DownloadTranscodeAllowed,
		"requests_allowed": user.RequestsAllowed, "max_profiles": user.MaxProfiles,
		"local_password_login_enabled": user.LocalPasswordLoginEnabled,
		"password_change_required":     user.PasswordChangeRequired, "break_glass": user.BreakGlass,
	}
}

// RecordMutation persists a successful domain action inside its owning
// transaction. The caller must call CommitMutation only after tx.Commit succeeds.
// V1 retains its frozen request-audit behavior.
func RecordMutation(ctx context.Context, tx pgx.Tx, action, targetType, targetID string, status int, changes []Change) (*Entry, error) {
	entries, err := RecordMutations(ctx, tx, action, targetType, []string{targetID}, status, changes)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	return entries[0], nil
}

// RecordMutations records one shared policy change for multiple targets in one
// database round trip. The owning transaction still commits every action together.
func RecordMutations(ctx context.Context, tx pgx.Tx, action, targetType string, targetIDs []string, status int, changes []Change) ([]*Entry, error) {
	lc := GetLogContext(ctx)
	if lc == nil || lc.Request == nil || !strings.HasPrefix(lc.Request.URL.Path, "/api/v2/admin/") {
		return nil, nil
	}
	if len(changes) == 0 || len(targetIDs) == 0 {
		return nil, nil
	}
	r := lc.Request
	path := r.URL.Path
	pattern := path
	if route := chi.RouteContext(ctx); route != nil && route.RoutePattern() != "" {
		pattern = route.RoutePattern()
	}
	entry := &Entry{Timestamp: time.Now().UTC(), ClientIP: clientip.FromContext(ctx), UserID: lc.UserID,
		ImpersonatorUserID: lc.ImpersonatorUserID, SessionID: lc.SessionID, RequestID: middleware.GetReqID(ctx),
		NodeID: lc.NodeID, Method: r.Method, Path: path, PathPattern: pattern, StatusCode: status,
		UserAgent: strings.ToValidUTF8(strings.ReplaceAll(r.UserAgent(), "\x00", ""), "�"), DurationMs: int(time.Since(lc.Started).Milliseconds()),
		Action: action, TargetType: targetType, Changes: changes}
	raw, err := json.Marshal(changes)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `INSERT INTO activity_log(timestamp,client_ip,user_id,impersonator_user_id,session_id,request_id,node_id,method,path,path_pattern,status_code,user_agent,duration_ms,action,target_type,target_id,changes)
 SELECT $1,$2::inet,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,target_id,$16 FROM unnest($17::text[]) AS target(target_id) RETURNING id,target_id`,
		entry.Timestamp, entry.ClientIP, entry.UserID, entry.ImpersonatorUserID, entry.SessionID, entry.RequestID,
		entry.NodeID, entry.Method, entry.Path, entry.PathPattern, entry.StatusCode, entry.UserAgent, entry.DurationMs,
		action, targetType, raw, targetIDs)
	if err != nil {
		return nil, fmt.Errorf("persisting audit actions: %w", err)
	}
	defer rows.Close()
	entries := make([]*Entry, 0, len(targetIDs))
	for rows.Next() {
		inserted := *entry
		if err := rows.Scan(&inserted.ID, &inserted.TargetID); err != nil {
			return nil, err
		}
		entries = append(entries, &inserted)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func RecordUserMutation(ctx context.Context, tx pgx.Tx, before, after *models.User, passwordChanged bool) (*Entry, error) {
	action, status, target := "user.updated", http.StatusNoContent, before
	if before == nil {
		action, status, target = "user.created", http.StatusCreated, after
	}
	if after == nil {
		action = "user.deleted"
	}
	changes := Changes(UserValues(before), UserValues(after))
	if passwordChanged {
		changes = append(changes, Change{Field: "password"})
	}
	return RecordMutation(ctx, tx, action, "user", strconv.Itoa(target.ID), status, changes)
}

func CommitMutation(ctx context.Context, entry *Entry) {
	if entry == nil {
		return
	}
	lc := GetLogContext(ctx)
	if lc == nil {
		return
	}
	lc.Committed = append(lc.Committed, *entry)
	// Persistence is authoritative. A missed live frame is recovered by history.
	if lc.Publish != nil {
		lc.Publish(*entry)
	}
}

// Actions is the capability inventory of supported successful domain actions.
func Actions() []string {
	return []string{"user.created", "user.updated", "user.deleted", "access_group.created", "access_group.updated", "access_group.deleted"}
}
