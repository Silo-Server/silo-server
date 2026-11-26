package apiv2

import (
	"context"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// sessionReachListDescription and sessionReachDeleteDescription describe
// listSessions and deleteSession for a session limited to itself.
const (
	sessionReachListDescription = "A session opened by device sign-in (a code a signed-in person approved, Jellyfin Quick Connect, or a remote-playback handoff), " +
		"and a session from before this rule was recorded whose X-Silo-Device-Platform was tvOS, android-tv or roku, sees only itself in full. " +
		"It still gets every live session of the account, but another session's item carries only its id and current: device_name and ip_address are empty, " +
		"device_id and device_platform are absent, last_seen_at is null, and created_at and expires_at repeat the caller's own. " +
		"For such a caller the page is ordered by id, not newest first, and a cursor from the other kind of caller is invalid_cursor."
	sessionReachDeleteDescription = "A session opened by device sign-in (see listSessions) may revoke only itself: " +
		"another session of the account is 403 permission_denied, and a session of another account or an unknown id is 404 as for every caller."
)

// loginSessionIDCursorSort is the listSessions order for a session limited
// to itself (auth.LimitedToItself). It is part of the cursor scope, so a
// cursor minted for one kind of caller is invalid_cursor for the other.
const loginSessionIDCursorSort = "id"

// loginSessionIDPosition is that listing's cursor payload: only the id of the
// last session the previous page emitted, which the page already showed.
type loginSessionIDPosition struct {
	ID string `json:"i"`
}

// limitedLoginSessionView is another session of the account as a session
// limited to itself sees it: its id, and the caller's own created_at and
// expires_at in the members that have no null form. It is built from those
// alone, so a member added to LoginSession stays empty here until decided.
func limitedLoginSessionView(id string, own LoginSession) LoginSession {
	return LoginSession{ID: ID(id), CreatedAt: own.CreatedAt, ExpiresAt: own.ExpiresAt}
}

// limitedLoginSessionPage is listSessions for a session limited to itself.
// It lists every live session of the account, as for any caller, but only
// the caller's own item and current_session are complete; the page order is
// by id and the cursor carries only an id, so neither tells which of the
// other sessions are newer.
func (reg *Registry) limitedLoginSessionPage(ctx context.Context, cursors *Cursors, claims *auth.Claims, in *LoginSessionListInput) (*LoginSessionCollectionOutput, error) {
	scope := CursorScope{
		OperationID: opListSessions,
		Security:    strconv.Itoa(claims.UserID),
		Sort:        loginSessionIDCursorSort,
		Tiebreaker:  "id",
	}
	afterID := ""
	if in.Cursor != "" {
		var pos loginSessionIDPosition
		if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
			return nil, p
		}
		if pos.ID == "" {
			return nil, NewProblem(TypeInvalidCursor, "The cursor is malformed, tampered with, or belongs to a different query.")
		}
		afterID = pos.ID
	}
	current, err := reg.deps.Sessions.CurrentLoginSession(ctx, claims.UserID, claims.SessionID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	if current == nil {
		return nil, NewProblem(TypeSessionExpired, "This login session has ended.")
	}
	own := loginSessionView(current, claims.SessionID)
	sessions, hasMore, err := reg.deps.Sessions.ListSessionsPageByID(ctx, claims.UserID, afterID, in.Limit)
	if err != nil {
		return nil, serviceProblem(err)
	}
	next := ""
	if hasMore && len(sessions) > 0 {
		if next, err = cursors.Encode(scope, loginSessionIDPosition{ID: sessions[len(sessions)-1].ID}); err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	items := make([]LoginSession, 0, len(sessions))
	for _, s := range sessions {
		if s.ID == claims.SessionID {
			items = append(items, loginSessionView(s, claims.SessionID))
			continue
		}
		items = append(items, limitedLoginSessionView(s.ID, own))
	}
	out := &LoginSessionCollectionOutput{Body: LoginSessionCollection{Collection: Paginated(items, next)}}
	out.Body.CurrentSession.Value = &own
	return out, nil
}
