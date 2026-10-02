package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
)

// adminNodeSessionTiebreaker orders node sessions within a node.
const adminNodeSessionTiebreaker = "session"

type AdminNodeSessionService interface {
	Available() bool
	Read(context.Context, int) (nodesessions.ListResult, error)
}
type AdminNodeSession struct {
	SessionID         ID       `json:"session_id"`
	NodeURL           string   `json:"node_url"`
	NodeName          string   `json:"node_name"`
	UserID            string   `json:"user_id,omitempty" doc:"Display label, not the account identifier."`
	AuthUserID        ID       `json:"auth_user_id"`
	ProfileID         ID       `json:"profile_id"`
	MediaFileID       ID       `json:"media_file_id"`
	MediaItemID       string   `json:"media_item_id,omitempty"`
	MediaTitle        string   `json:"media_title,omitempty"`
	Type              string   `json:"type"`
	CodecVideo        string   `json:"codec_video,omitempty"`
	CodecAudio        string   `json:"codec_audio,omitempty"`
	Resolution        string   `json:"resolution,omitempty"`
	HWAccel           string   `json:"hw_accel,omitempty"`
	ToneMapMode       string   `json:"tone_map_mode,omitempty"`
	StartedAt         *Instant `json:"started_at"`
	StartedAtUnixNano string   `json:"started_at_unix_nano" doc:"Original diagnostic precision as a decimal string; zero means absent."`
	StartedAtSource   string   `json:"started_at_source,omitempty"`
}
type AdminNodeSessionsInput struct {
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
	NodeID ID     `query:"node_id"`
}
type AdminNodeSessionsOutput struct {
	Body struct {
		Collection[AdminNodeSession]
		Undecodable int `json:"undecodable" doc:"Unreadable records in the source enumeration, before node filtering."`
	}
}

func adminNodeSessionOf(v nodesessions.SessionInfo) AdminNodeSession {
	return AdminNodeSession{SessionID: ID(v.SessionID), NodeURL: v.NodeURL, NodeName: v.NodeName, UserID: v.UserID, AuthUserID: IDFromInt(int64(v.AuthUserID)), ProfileID: ID(v.ProfileID), MediaFileID: IDFromInt(int64(v.MediaFileID)), MediaItemID: v.MediaItemID, MediaTitle: v.MediaTitle, Type: v.Type, CodecVideo: v.CodecVideo, CodecAudio: v.CodecAudio, Resolution: v.Resolution, HWAccel: v.HWAccel, ToneMapMode: v.ToneMapMode, StartedAt: instantOfRFC3339(&v.StartedAt), StartedAtUnixNano: strconv.FormatInt(v.StartedAtUnixNano, 10), StartedAtSource: v.StartedAtSource}
}

// One observation per node and session; the pair orders the page.
func adminNodeSessionKey(v nodesessions.SessionInfo) string {
	b, _ := json.Marshal(struct{ Node, Session string }{v.NodeURL, v.SessionID})
	return string(b)
}
func registerAdminNodeSessions(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/node-sessions", "listAdminNodeSessions", "admin", "Read best-effort Redis observations, not authoritative playback sessions. Each page enumerates current records; expired or unreadable values may be absent."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminNodeSessionsInput) (*AdminNodeSessionsOutput, error) {
		if reg.deps.AdminNodeSessions == nil || !reg.deps.AdminNodeSessions.Available() {
			return nil, unavailable("node session observations")
		}
		node := 0
		if in.NodeID != "" {
			value, p := in.NodeID.positive("query.node_id")
			if p != nil {
				return nil, p
			}
			node = value
		}
		scope := CursorScope{OperationID: "listAdminNodeSessions", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: string(in.NodeID) + "/" + strconv.Itoa(in.Limit), Sort: "node," + adminNodeSessionTiebreaker, Tiebreaker: adminNodeSessionTiebreaker}
		var after string
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		result, err := reg.deps.AdminNodeSessions.Read(ctx, node)
		if err != nil {
			return nil, serviceProblem(err)
		}
		rows := slices.Clone(result.Sessions)
		slices.SortFunc(rows, func(a, b nodesessions.SessionInfo) int {
			return cmp.Compare(adminNodeSessionKey(a), adminNodeSessionKey(b))
		})
		items := make([]AdminNodeSession, 0, in.Limit)
		next, last := "", ""
		for _, row := range rows {
			key := adminNodeSessionKey(row)
			if key <= after || key == last {
				continue
			}
			if len(items) == in.Limit {
				next, err = cursors.Encode(scope, last)
				if err != nil {
					return nil, serviceProblem(err)
				}
				break
			}
			items = append(items, adminNodeSessionOf(row))
			last = key
		}
		out := new(AdminNodeSessionsOutput)
		out.Body.Collection = Paginated(items, next)
		out.Body.Undecodable = result.Undecodable
		return out, nil
	})
}
