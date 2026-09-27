package watchtogether

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/redis/go-redis/v9"
)

const RoomSocketTicketTTL = 30 * time.Second
const RoomSocketMaxLifetime = 5 * time.Minute
const roomSocketTicketPrefix = "silo:watch-together:v2:ticket:"

var ErrRoomSocketCredential = errors.New("invalid or unavailable room socket credential")

// RoomSocketCredential delegates existing authority, never an independent login.
// Session PIN proof and opaque credentials must never enter logs or messages.
// The handler must validate current login/profile/room authority before minting,
// after consumption and throughout the bounded connection lifetime.
type RoomSocketCredential struct {
	RoomID             string                `json:"room_id"`
	Session            events.SocketIdentity `json:"session"`
	RoomProofExpiresAt time.Time             `json:"room_proof_expires_at"`
	ExpiresAt          time.Time             `json:"expires_at"`
}

type RoomSocketCredentialStore struct{ redis *redis.Client }

// NewRoomSocketCredentialStore requires shared Redis. Failure never switches to
// local storage, so a delegated handshake can be consumed at most once cluster-wide.
func NewRoomSocketCredentialStore(client *redis.Client) *RoomSocketCredentialStore {
	return &RoomSocketCredentialStore{redis: client}
}

func validRoomSocketCredential(c RoomSocketCredential, now time.Time) bool {
	return c.RoomID != "" && c.Session.UserID > 0 && c.Session.SessionID != "" && c.Session.ProfileID != "" &&
		c.Session.AccessFingerprint != "" && c.Session.AccessExpiresAt.After(now) && c.RoomProofExpiresAt.After(now)
}

func (s *RoomSocketCredentialStore) Mint(ctx context.Context, credential RoomSocketCredential) (string, error) {
	now := time.Now()
	if s == nil || s.redis == nil || !validRoomSocketCredential(credential, now) {
		return "", ErrRoomSocketCredential
	}
	credential.ExpiresAt = now.Add(RoomSocketTicketTTL)
	for _, expiry := range []time.Time{credential.Session.AccessExpiresAt, credential.RoomProofExpiresAt} {
		if expiry.Before(credential.ExpiresAt) {
			credential.ExpiresAt = expiry
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw)
	payload, err := json.Marshal(credential)
	if err != nil {
		return "", err
	}
	ttl := time.Until(credential.ExpiresAt)
	if ttl <= 0 {
		return "", ErrRoomSocketCredential
	}
	result, err := s.redis.SetArgs(ctx, roomSocketTicketPrefix+ticket, payload, redis.SetArgs{Mode: "NX", TTL: ttl}).Result()
	if err != nil || result != "OK" {
		return "", ErrRoomSocketCredential
	}
	return ticket, nil
}

// Consume burns the credential even on a room mismatch. Storage consumption is
// not authority validation or admission to a live room.
func (s *RoomSocketCredentialStore) Consume(ctx context.Context, ticket, roomID string) (RoomSocketCredential, error) {
	var credential RoomSocketCredential
	if s == nil || s.redis == nil || len(ticket) != 43 {
		return credential, ErrRoomSocketCredential
	}
	if raw, err := base64.RawURLEncoding.DecodeString(ticket); err != nil || len(raw) != 32 {
		return credential, ErrRoomSocketCredential
	}
	data, err := s.redis.GetDel(ctx, roomSocketTicketPrefix+ticket).Bytes()
	if err != nil || json.Unmarshal(data, &credential) != nil {
		return RoomSocketCredential{}, ErrRoomSocketCredential
	}
	now := time.Now()
	if credential.RoomID != roomID || !validRoomSocketCredential(credential, now) || !credential.ExpiresAt.After(now) {
		return RoomSocketCredential{}, ErrRoomSocketCredential
	}
	return credential, nil
}
