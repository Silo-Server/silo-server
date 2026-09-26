package events

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const SocketTicketTTL = 30 * time.Second
const SocketMaxLifetime = 5 * time.Minute

// SocketIdentity is a delegated login-session proof, never an independent login.
// ProfileToken retains the PIN proof captured when the ticket was requested.
// Neither it nor the opaque ticket may be included in logs or socket messages.
type SocketIdentity struct {
	ImpersonatorUserID *int      `json:"impersonator_user_id,omitempty"`
	EffectiveRole      string    `json:"effective_role"`
	AccessFingerprint  string    `json:"access_fingerprint"`
	UserID             int       `json:"user_id"`
	SessionID          string    `json:"session_id"`
	Role               string    `json:"role"`
	ProfileID          string    `json:"profile_id"`
	ProfileToken       string    `json:"profile_token"`
	AccessExpiresAt    time.Time `json:"access_expires_at"`
	TicketExpiresAt    time.Time `json:"ticket_expires_at"`
}

var ErrSocketTicket = errors.New("invalid or unavailable realtime credential")

type SocketTicketStore struct {
	redis   *redis.Client
	mu      sync.Mutex
	tickets map[string]SocketIdentity
}

func NewSocketTicketStore(client *redis.Client) *SocketTicketStore {
	return &SocketTicketStore{redis: client, tickets: make(map[string]SocketIdentity)}
}

func (s *SocketTicketStore) Mint(ctx context.Context, identity SocketIdentity) (string, error) {
	now := time.Now()
	if identity.AccessFingerprint == "" || identity.UserID <= 0 || identity.SessionID == "" || !identity.AccessExpiresAt.After(now) {
		return "", ErrSocketTicket
	}
	identity.TicketExpiresAt = now.Add(SocketTicketTTL)
	if identity.AccessExpiresAt.Before(identity.TicketExpiresAt) {
		identity.TicketExpiresAt = identity.AccessExpiresAt
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw)
	if s.redis != nil {
		payload, err := json.Marshal(identity)
		if err != nil {
			return "", err
		}
		ok, err := s.redis.SetArgs(ctx, "silo:events:v2:ticket:"+ticket, payload, redis.SetArgs{Mode: "NX", TTL: time.Until(identity.TicketExpiresAt)}).Result()
		if err != nil {
			return "", err
		}
		if ok != "OK" {
			return "", ErrSocketTicket
		}
	} else {
		s.mu.Lock()
		defer s.mu.Unlock()
		for key, v := range s.tickets {
			if !v.TicketExpiresAt.After(now) {
				delete(s.tickets, key)
			}
		}
		if len(s.tickets) >= 10000 {
			return "", ErrSocketTicket
		}
		s.tickets[ticket] = identity
	}
	return ticket, nil
}

// Consume burns proof atomically across nodes. Redis failures never fall back to
// another store; a memory deployment requires reconnecting to its minting node.
func (s *SocketTicketStore) Consume(ctx context.Context, ticket string) (SocketIdentity, error) {
	var identity SocketIdentity
	if len(ticket) != 43 {
		return identity, ErrSocketTicket
	}
	if _, err := base64.RawURLEncoding.DecodeString(ticket); err != nil {
		return identity, ErrSocketTicket
	}
	if s.redis != nil {
		data, err := s.redis.GetDel(ctx, "silo:events:v2:ticket:"+ticket).Bytes()
		if err != nil {
			return identity, ErrSocketTicket
		}
		if err := json.Unmarshal(data, &identity); err != nil {
			return SocketIdentity{}, ErrSocketTicket
		}
	} else {
		s.mu.Lock()
		identity = s.tickets[ticket]
		delete(s.tickets, ticket)
		s.mu.Unlock()
	}
	now := time.Now()
	if identity.AccessFingerprint == "" || identity.UserID <= 0 || identity.SessionID == "" || !identity.TicketExpiresAt.After(now) || !identity.AccessExpiresAt.After(now) {
		return SocketIdentity{}, ErrSocketTicket
	}
	return identity, nil
}
