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

// APIKeySocketMaxLifetime caps an events socket an API key opens. A key has
// no access token to expire with, and the socket's periodic recheck of the
// key, its scope and its account is what ends it early.
const APIKeySocketMaxLifetime = 4 * time.Hour

// Socket ticket audiences. Every socket that shares this store shape holds
// its tickets under one Redis prefix, so the audience a ticket was minted for
// is what keeps it from opening a different socket.
const (
	SocketAudienceEvents    = "events"
	SocketAudienceAdminLogs = "admin_logs"
)

// SocketIdentity is a delegated login-session proof, never an independent login,
// or, for the events socket only, the proof of one API key (APIKeyID set, no
// SessionID or profile).
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
	// DeviceID is the minting request's X-Silo-Device-Id, which the
	// acting-admin decision reads as the HTTP admin gate does.
	DeviceID string `json:"device_id,omitempty"`
	// Audience is the socket the ticket opens. The store stamps it at mint
	// and refuses a ticket minted for another audience at consume.
	Audience string `json:"audience"`
	// APIKeyID and APIKeyRevision name the key, and the configuration
	// revision it had at mint, for a ticket an API key minted.
	APIKeyID       int64 `json:"api_key_id,omitempty"`
	APIKeyRevision int64 `json:"api_key_revision,omitempty"`
}

// MaxLifetime is how long a socket opened with this identity may stay open.
func (i SocketIdentity) MaxLifetime() time.Duration {
	if i.APIKeyID != 0 {
		return APIKeySocketMaxLifetime
	}
	return SocketMaxLifetime
}

// holdsAuthority reports whether the identity names exactly one credential:
// a login session with the viewer scope it was minted under, or an API key
// at a known revision, which stands in for the scope.
func (i SocketIdentity) holdsAuthority() bool {
	if i.UserID <= 0 {
		return false
	}
	if i.APIKeyID != 0 {
		return i.APIKeyID > 0 && i.APIKeyRevision > 0 && i.SessionID == "" && i.ProfileID == ""
	}
	return i.SessionID != "" && i.AccessFingerprint != ""
}

var ErrSocketTicket = errors.New("invalid or unavailable realtime credential")

type SocketTicketStore struct {
	redis    *redis.Client
	audience string
	mu       sync.Mutex
	tickets  map[string]SocketIdentity
}

// NewSocketTicketStore mints and consumes tickets for one socket audience.
func NewSocketTicketStore(client *redis.Client, audience string) *SocketTicketStore {
	return &SocketTicketStore{redis: client, audience: audience, tickets: make(map[string]SocketIdentity)}
}

func (s *SocketTicketStore) Mint(ctx context.Context, identity SocketIdentity) (string, error) {
	now := time.Now()
	if s.audience == "" || !identity.holdsAuthority() || !identity.AccessExpiresAt.After(now) {
		return "", ErrSocketTicket
	}
	identity.Audience = s.audience
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
	if identity.Audience == "" || identity.Audience != s.audience || !identity.holdsAuthority() || !identity.TicketExpiresAt.After(now) || !identity.AccessExpiresAt.After(now) {
		return SocketIdentity{}, ErrSocketTicket
	}
	return identity, nil
}
