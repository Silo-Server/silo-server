package notifications

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// TicketTTL is how long a websocket ticket stays valid. Browsers cannot set
// custom headers on websocket handshakes, so profile identity is carried by a
// short-lived single-use ticket in the query string. Reverse-proxy access
// logs commonly capture query strings; a logged ticket that expired seconds
// after minting is harmless where a logged profile token would not be.
const TicketTTL = 30 * time.Second

// TicketStore mints and consumes single-use websocket handshake tickets
// bound to a (user, profile).
type TicketStore interface {
	Mint(ctx context.Context, userID int, profileID string) (ticket string, ttl time.Duration, err error)
	// Consume validates and invalidates a ticket. ok is false for missing,
	// expired, or already-used tickets.
	Consume(ctx context.Context, ticket string) (userID int, profileID string, ok bool)
}

// NewTicketStore returns a Redis-backed store when a Redis client is
// available (multi-node websocket serving) and an in-memory store otherwise.
func NewTicketStore(redisClient *redis.Client) TicketStore {
	if redisClient != nil {
		return &redisTicketStore{client: redisClient}
	}
	return &memoryTicketStore{tickets: make(map[string]memoryTicket)}
}

func newTicketValue() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate ticket: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type memoryTicket struct {
	userID    int
	profileID string
	expiresAt time.Time
}

type memoryTicketStore struct {
	mu      sync.Mutex
	tickets map[string]memoryTicket
}

func (s *memoryTicketStore) Mint(_ context.Context, userID int, profileID string) (string, time.Duration, error) {
	ticket, err := newTicketValue()
	if err != nil {
		return "", 0, err
	}
	now := time.Now()
	s.mu.Lock()
	// Opportunistic sweep keeps the map bounded without a janitor goroutine.
	for key, entry := range s.tickets {
		if now.After(entry.expiresAt) {
			delete(s.tickets, key)
		}
	}
	s.tickets[ticket] = memoryTicket{
		userID:    userID,
		profileID: profileID,
		expiresAt: now.Add(TicketTTL),
	}
	s.mu.Unlock()
	return ticket, TicketTTL, nil
}

func (s *memoryTicketStore) Consume(_ context.Context, ticket string) (int, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tickets[ticket]
	if !ok {
		return 0, "", false
	}
	delete(s.tickets, ticket) // single-use
	if time.Now().After(entry.expiresAt) {
		return 0, "", false
	}
	return entry.userID, entry.profileID, true
}

type redisTicketStore struct {
	client *redis.Client
}

const redisTicketPrefix = "silo:events:ws-ticket:"

func (s *redisTicketStore) Mint(ctx context.Context, userID int, profileID string) (string, time.Duration, error) {
	ticket, err := newTicketValue()
	if err != nil {
		return "", 0, err
	}
	value := strconv.Itoa(userID) + "|" + profileID
	if err := s.client.Set(ctx, redisTicketPrefix+ticket, value, TicketTTL).Err(); err != nil {
		return "", 0, fmt.Errorf("store ticket: %w", err)
	}
	return ticket, TicketTTL, nil
}

func (s *redisTicketStore) Consume(ctx context.Context, ticket string) (int, string, bool) {
	value, err := s.client.GetDel(ctx, redisTicketPrefix+ticket).Result()
	if err != nil {
		return 0, "", false
	}
	parts := strings.SplitN(value, "|", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	userID, err := strconv.Atoi(parts[0])
	if err != nil || userID <= 0 {
		return 0, "", false
	}
	return userID, parts[1], true
}
