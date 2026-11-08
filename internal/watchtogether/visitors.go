package watchtogether

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Visitors are people without a Silo account who join one room through an
// invite link the host created. A visitor's login session belongs to the
// host's account and is bound to the host's profile, the room and the visitor
// (auth.SessionPurposeWatchPartyVisitor). Inside the room the visitor is its
// own member, keyed by VisitorMemberProfileID, so it never shares the host's
// roster entry, votes or suggestions. See
// docs/architecture/watch-party-visitors.md.

const (
	// VisitorInviteTTL is how long a new invite link can be used.
	VisitorInviteTTL = 6 * time.Hour
	// VisitorSessionTTL is the longest a visitor's session lasts. Closing the
	// room or removing the visitor ends it sooner.
	VisitorSessionTTL = 12 * time.Hour
	// MaxVisitorsPerRoom bounds the visitors a room admits at once.
	MaxVisitorsPerRoom = 10
	// MaxActiveVisitorInvites bounds the usable invite links of one room.
	MaxActiveVisitorInvites = 5
	// MaxVisitorNameRunes bounds a visitor's display name.
	MaxVisitorNameRunes = 40

	visitorMemberPrefix = "visitor:"
)

var (
	// ErrVisitorInviteNotFound covers an unknown, revoked or expired invite
	// and an invite whose room closed, so a lookup reveals nothing about
	// which.
	ErrVisitorInviteNotFound = errors.New("watch party visitor invite not found")
	// ErrVisitorLimitReached refuses a visitor or invite beyond the room's
	// limits.
	ErrVisitorLimitReached = errors.New("watch party visitor limit reached")
	// ErrInvalidVisitorName refuses an empty, overlong or control-character
	// display name.
	ErrInvalidVisitorName = errors.New("watch party visitor name is invalid")
	// ErrVisitorNotFound is a visitor id that is not in the room.
	ErrVisitorNotFound = errors.New("watch party visitor not found")

	errVisitorStoreUnavailable = errors.New("watch party visitor store unavailable")
	errVisitorsUnavailable     = errors.New("watch party visitors unavailable")
	errServiceUnavailable      = errors.New("watch together service unavailable")
)

// VisitorMemberProfileID is the member profile id a visitor has inside its
// room. It cannot collide with a real profile id.
func VisitorMemberProfileID(visitorID string) string {
	return visitorMemberPrefix + visitorID
}

// VisitorIDFromMember returns the visitor id of a visitor member profile id.
func VisitorIDFromMember(profileID string) (string, bool) {
	id, ok := strings.CutPrefix(profileID, visitorMemberPrefix)
	return id, ok && id != ""
}

// IsVisitorMember reports whether profileID is a visitor's member profile id.
func IsVisitorMember(profileID string) bool {
	_, ok := VisitorIDFromMember(profileID)
	return ok
}

// NormalizeVisitorName trims a display name and checks its length and
// characters.
func NormalizeVisitorName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxVisitorNameRunes {
		return "", ErrInvalidVisitorName
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", ErrInvalidVisitorName
		}
	}
	return name, nil
}

// VisitorInvite is an invite link's metadata. The token itself is only ever
// returned by CreateInvite.
type VisitorInvite struct {
	ID        string
	RoomID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// VisitorInvitePreview is what someone holding an invite link may learn
// before joining.
type VisitorInvitePreview struct {
	RoomID        string
	HostUserID    int
	HostProfileID string
	ExpiresAt     time.Time
}

// AcceptedVisitor is a visitor that AcceptInvite admitted, with the login
// session the caller creates in the same transaction.
type AcceptedVisitor struct {
	VisitorID     string
	SessionID     string
	RoomID        string
	HostUserID    int
	HostProfileID string
	DisplayName   string
	SessionEndsAt time.Time
}

// VisitorStore keeps invite links and visitors in Postgres.
type VisitorStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewVisitorStore returns a VisitorStore over pool.
func NewVisitorStore(pool *pgxpool.Pool) *VisitorStore {
	return &VisitorStore{pool: pool, now: time.Now}
}

func (s *VisitorStore) available() bool { return s != nil && s.pool != nil }

func visitorTokenDigest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func newVisitorToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating visitor invite token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// CreateInvite creates an invite link to room for its host and returns the
// link's metadata and its raw token. The caller has checked that the caller
// is the room's host. The room must be open and have fewer than
// MaxActiveVisitorInvites usable links.
func (s *VisitorStore) CreateInvite(ctx context.Context, room Room) (VisitorInvite, string, error) {
	if !s.available() {
		return VisitorInvite{}, "", errVisitorStoreUnavailable
	}
	token, err := newVisitorToken()
	if err != nil {
		return VisitorInvite{}, "", err
	}
	now := s.now().UTC()
	invite := VisitorInvite{ID: uuid.NewString(), RoomID: room.ID, CreatedAt: now, ExpiresAt: now.Add(VisitorInviteTTL)}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var open bool
		if err := tx.QueryRow(ctx, `SELECT closed_at IS NULL AND phase <> 'ended' FROM watch_together_rooms WHERE id = $1 FOR UPDATE`, room.ID).Scan(&open); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRoomNotFound
			}
			return err
		}
		if !open {
			return ErrRoomClosed
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM watch_together_visitor_invites
			WHERE room_id = $1 AND revoked_at IS NULL AND expires_at > $2`, room.ID, now).Scan(&active); err != nil {
			return err
		}
		if active >= MaxActiveVisitorInvites {
			return ErrVisitorLimitReached
		}
		_, err := tx.Exec(ctx, `INSERT INTO watch_together_visitor_invites
			(id, room_id, token_digest, created_by_user_id, created_by_profile_id, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			invite.ID, room.ID, visitorTokenDigest(token), room.HostUserID, room.HostProfileID, invite.CreatedAt, invite.ExpiresAt)
		return err
	})
	if err != nil {
		return VisitorInvite{}, "", err
	}
	return invite, token, nil
}

// ListInvites returns room's usable invite links, newest first.
func (s *VisitorStore) ListInvites(ctx context.Context, roomID string) ([]VisitorInvite, error) {
	if !s.available() {
		return nil, errVisitorStoreUnavailable
	}
	rows, err := s.pool.Query(ctx, `SELECT id, room_id, created_at, expires_at FROM watch_together_visitor_invites
		WHERE room_id = $1 AND revoked_at IS NULL AND expires_at > $2
		ORDER BY created_at DESC, id DESC`, roomID, s.now().UTC())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (VisitorInvite, error) {
		var invite VisitorInvite
		err := row.Scan(&invite.ID, &invite.RoomID, &invite.CreatedAt, &invite.ExpiresAt)
		return invite, err
	})
}

// RevokeInvite stops an invite link of room from admitting anyone else.
// Visitors who already joined through it stay. Revoking an unknown or
// already revoked link is not an error.
func (s *VisitorStore) RevokeInvite(ctx context.Context, roomID, inviteID string) error {
	if !s.available() {
		return errVisitorStoreUnavailable
	}
	_, err := s.pool.Exec(ctx, `UPDATE watch_together_visitor_invites SET revoked_at = COALESCE(revoked_at, $3)
		WHERE id = $1 AND room_id = $2`, inviteID, roomID, s.now().UTC())
	return err
}

// PreviewInvite returns what an invite token leads to while it can still be
// used. Every unusable token is ErrVisitorInviteNotFound.
func (s *VisitorStore) PreviewInvite(ctx context.Context, token string) (VisitorInvitePreview, error) {
	if !s.available() {
		return VisitorInvitePreview{}, errVisitorStoreUnavailable
	}
	var preview VisitorInvitePreview
	err := s.pool.QueryRow(ctx, `SELECT r.id, r.host_user_id, r.host_profile_id, i.expires_at
		FROM watch_together_visitor_invites i
		JOIN watch_together_rooms r ON r.id = i.room_id
		WHERE i.token_digest = $1 AND i.revoked_at IS NULL AND i.expires_at > $2 AND r.closed_at IS NULL AND r.phase <> 'ended'`,
		visitorTokenDigest(strings.TrimSpace(token)), s.now().UTC()).Scan(&preview.RoomID, &preview.HostUserID, &preview.HostProfileID, &preview.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return VisitorInvitePreview{}, ErrVisitorInviteNotFound
	}
	return preview, err
}

// AcceptInvite admits a visitor named displayName through token. In one
// transaction it checks the link and its open room, enforces
// MaxVisitorsPerRoom under the room row's lock (so accepts through different
// links cannot overshoot it), inserts the visitor, and calls createSession
// with the same transaction so the visitor's login session commits with it. Every
// unusable token is ErrVisitorInviteNotFound.
func (s *VisitorStore) AcceptInvite(ctx context.Context, token, displayName string, createSession func(context.Context, pgx.Tx, AcceptedVisitor) error) (AcceptedVisitor, error) {
	if !s.available() || createSession == nil {
		return AcceptedVisitor{}, errVisitorStoreUnavailable
	}
	name, err := NormalizeVisitorName(displayName)
	if err != nil {
		return AcceptedVisitor{}, err
	}
	now := s.now().UTC()
	accepted := AcceptedVisitor{VisitorID: uuid.NewString(), SessionID: uuid.NewString(), DisplayName: name, SessionEndsAt: now.Add(VisitorSessionTTL)}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID string
		err := tx.QueryRow(ctx, `SELECT i.id, r.id, r.host_user_id, r.host_profile_id
			FROM watch_together_visitor_invites i
			JOIN watch_together_rooms r ON r.id = i.room_id
			WHERE i.token_digest = $1 AND i.revoked_at IS NULL AND i.expires_at > $2 AND r.closed_at IS NULL AND r.phase <> 'ended'
			FOR UPDATE OF r, i`,
			visitorTokenDigest(strings.TrimSpace(token)), now).Scan(&inviteID, &accepted.RoomID, &accepted.HostUserID, &accepted.HostProfileID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVisitorInviteNotFound
		}
		if err != nil {
			return err
		}
		// Visitors who left or were removed, or whose session ended, no
		// longer count.
		var present int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM watch_together_visitors v
			JOIN auth_sessions s ON s.id = v.auth_session_id
			WHERE v.room_id = $1 AND v.removed_at IS NULL AND s.revoked_at IS NULL AND s.expires_at > $2`,
			accepted.RoomID, now).Scan(&present); err != nil {
			return err
		}
		if present >= MaxVisitorsPerRoom {
			return ErrVisitorLimitReached
		}
		if _, err := tx.Exec(ctx, `INSERT INTO watch_together_visitors (id, room_id, invite_id, display_name, auth_session_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, accepted.VisitorID, accepted.RoomID, inviteID, name, accepted.SessionID, now); err != nil {
			return err
		}
		return createSession(ctx, tx, accepted)
	})
	if err != nil {
		return AcceptedVisitor{}, err
	}
	return accepted, nil
}

// RemoveVisitor removes a visitor from room and ends its login session in
// the same transaction.
func (s *VisitorStore) RemoveVisitor(ctx context.Context, roomID, visitorID string) error {
	if !s.available() {
		return errVisitorStoreUnavailable
	}
	now := s.now().UTC()
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var sessionID string
		err := tx.QueryRow(ctx, `UPDATE watch_together_visitors SET removed_at = COALESCE(removed_at, $3)
			WHERE id = $1 AND room_id = $2 RETURNING auth_session_id`, visitorID, roomID, now).Scan(&sessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVisitorNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, sessionID, now)
		return err
	})
}

// VisitorName returns a visitor's display name, or "" when it is unknown.
func (s *VisitorStore) VisitorName(ctx context.Context, visitorID string) string {
	if !s.available() {
		return ""
	}
	var name string
	if err := s.pool.QueryRow(ctx, `SELECT display_name FROM watch_together_visitors WHERE id = $1`, visitorID).Scan(&name); err != nil {
		return ""
	}
	return name
}

// visitorAwareNames resolves a visitor member's display name from the
// visitor store and every other member's through names.
type visitorAwareNames struct {
	names    ProfileNameResolver
	visitors *VisitorStore
}

func (r visitorAwareNames) ProfileDisplayName(ctx context.Context, userID int, profileID string) string {
	if visitorID, ok := VisitorIDFromMember(profileID); ok {
		if name := r.visitors.VisitorName(ctx, visitorID); name != "" {
			return name
		}
		return fallbackMemberName
	}
	if r.names == nil {
		return fallbackMemberName
	}
	return r.names.ProfileDisplayName(ctx, userID, profileID)
}

// SetVisitorStore enables visitor invites. Visitor members' display names
// come from the store.
func (s *Service) SetVisitorStore(store *VisitorStore) {
	if s == nil || store == nil {
		return
	}
	s.visitors = store
	s.profileNames = visitorAwareNames{names: s.profileNames, visitors: store}
}

// VisitorsAvailable reports whether visitor invites are enabled.
func (s *Service) VisitorsAvailable() bool { return s != nil && s.visitors.available() }

// hostRoom loads an open room that (user, profile) hosts.
func (s *Service) hostRoom(ctx context.Context, roomID string, user int, profile string) (*Room, error) {
	if !s.VisitorsAvailable() {
		return nil, errVisitorsUnavailable
	}
	room, err := s.repo.GetRoomByID(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if room.ClosedAt != nil {
		return nil, ErrRoomClosed
	}
	if room.HostUserID != user || room.HostProfileID != profile || IsVisitorMember(profile) {
		return nil, ErrRoomForbidden
	}
	return room, nil
}

// CreateVisitorInvite creates an invite link to roomID as its host.
func (s *Service) CreateVisitorInvite(ctx context.Context, roomID string, user int, profile string) (VisitorInvite, string, error) {
	room, err := s.hostRoom(ctx, roomID, user, profile)
	if err != nil {
		return VisitorInvite{}, "", err
	}
	return s.visitors.CreateInvite(ctx, *room)
}

// ListVisitorInvites lists roomID's usable invite links as its host.
func (s *Service) ListVisitorInvites(ctx context.Context, roomID string, user int, profile string) ([]VisitorInvite, error) {
	if _, err := s.hostRoom(ctx, roomID, user, profile); err != nil {
		return nil, err
	}
	return s.visitors.ListInvites(ctx, roomID)
}

// RevokeVisitorInvite revokes one of roomID's invite links as its host.
func (s *Service) RevokeVisitorInvite(ctx context.Context, roomID, inviteID string, user int, profile string) error {
	if _, err := s.hostRoom(ctx, roomID, user, profile); err != nil {
		return err
	}
	return s.visitors.RevokeInvite(ctx, roomID, inviteID)
}

// RemoveVisitor removes a visitor from roomID as its host and ends its
// session. The visitor's room socket closes at its next authority check.
func (s *Service) RemoveVisitor(ctx context.Context, roomID, visitorID string, user int, profile string) error {
	if _, err := s.hostRoom(ctx, roomID, user, profile); err != nil {
		return err
	}
	return s.visitors.RemoveVisitor(ctx, roomID, visitorID)
}

// PreviewVisitorInvite returns where an invite token leads.
func (s *Service) PreviewVisitorInvite(ctx context.Context, token string) (VisitorInvitePreview, error) {
	if !s.VisitorsAvailable() {
		return VisitorInvitePreview{}, errVisitorsUnavailable
	}
	return s.visitors.PreviewInvite(ctx, token)
}

// AcceptVisitorInvite admits a visitor through token; see
// VisitorStore.AcceptInvite.
func (s *Service) AcceptVisitorInvite(ctx context.Context, token, displayName string, createSession func(context.Context, pgx.Tx, AcceptedVisitor) error) (AcceptedVisitor, error) {
	if !s.VisitorsAvailable() {
		return AcceptedVisitor{}, errVisitorsUnavailable
	}
	return s.visitors.AcceptInvite(ctx, token, displayName, createSession)
}

// VisitorRoomFile returns the media file roomID has selected, which is the
// only file a visitor of that room may play.
func (s *Service) VisitorRoomFile(ctx context.Context, roomID string) (int, error) {
	if s == nil || s.repo == nil {
		return 0, errServiceUnavailable
	}
	room, err := s.repo.GetRoomByID(ctx, roomID)
	if err != nil {
		return 0, err
	}
	if room.ClosedAt != nil {
		return 0, ErrRoomClosed
	}
	if room.SelectedFileID == nil || *room.SelectedFileID <= 0 {
		return 0, ErrInvalidSelection
	}
	return *room.SelectedFileID, nil
}

// MemberDisplayName is the display name the room shows for a member.
func (s *Service) MemberDisplayName(ctx context.Context, userID int, profileID string) string {
	if s == nil || s.profileNames == nil {
		return fallbackMemberName
	}
	return s.profileNames.ProfileDisplayName(ctx, userID, profileID)
}
