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

// Ticket is what a websocket handshake ticket is bound to: the user, the
// profile it was minted for, and that profile's PIN proof (its profile token,
// "" for a profile without a PIN). The proof stays server-side; the socket
// re-verifies it for as long as the connection relies on the profile.
type Ticket struct {
	UserID       int
	ProfileID    string
	ProfileToken string
}

// maxTicketProofBytes is well above any signed profile token. The viewer gate
// does not read X-Profile-Token for a profile without a PIN, so the mint
// would otherwise keep whatever the caller sent.
const maxTicketProofBytes = 4 << 10

// bounded drops a profile token no signed one could be. A PIN-locked profile
// then fails its proof check, which closes rather than opens access.
func (t Ticket) bounded() Ticket {
	if len(t.ProfileToken) > maxTicketProofBytes {
		t.ProfileToken = ""
	}
	return t
}

// TicketStore mints and consumes single-use websocket handshake tickets.
type TicketStore interface {
	Mint(ctx context.Context, binding Ticket) (ticket string, ttl time.Duration, err error)
	// Consume validates and invalidates a ticket. ok is false for missing,
	// expired, or already-used tickets.
	Consume(ctx context.Context, ticket string) (binding Ticket, ok bool)
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
	binding   Ticket
	expiresAt time.Time
}

type memoryTicketStore struct {
	mu      sync.Mutex
	tickets map[string]memoryTicket
}

func (s *memoryTicketStore) Mint(_ context.Context, binding Ticket) (string, time.Duration, error) {
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
		binding:   binding.bounded(),
		expiresAt: now.Add(TicketTTL),
	}
	s.mu.Unlock()
	return ticket, TicketTTL, nil
}

func (s *memoryTicketStore) Consume(_ context.Context, ticket string) (Ticket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tickets[ticket]
	if !ok {
		return Ticket{}, false
	}
	delete(s.tickets, ticket) // single-use
	if time.Now().After(entry.expiresAt) {
		return Ticket{}, false
	}
	return entry.binding, true
}

type redisTicketStore struct {
	client *redis.Client
}

const redisTicketPrefix = "silo:events:ws-ticket:"

// redisTicketProofPrefix keys a ticket's profile token. The ticket key keeps
// the "userID|profileID" value every release reads, so a node from before the
// proof was kept still consumes a ticket minted by a newer one during a
// rolling deploy. The proof lives outside redisTicketPrefix: the caller
// chooses the token, so a proof key any ticket string could name would be
// read as a binding to whatever "userID|profileID" the caller sent, by this
// release and by older ones.
const redisTicketProofPrefix = "silo:events:ws-ticket-proof:"

func (s *redisTicketStore) Mint(ctx context.Context, binding Ticket) (string, time.Duration, error) {
	ticket, err := newTicketValue()
	if err != nil {
		return "", 0, err
	}
	binding = binding.bounded()
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, redisTicketPrefix+ticket, strconv.Itoa(binding.UserID)+"|"+binding.ProfileID, TicketTTL)
	if binding.ProfileToken != "" {
		pipe.Set(ctx, redisTicketProofPrefix+ticket, binding.ProfileToken, TicketTTL)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return "", 0, fmt.Errorf("store ticket: %w", err)
	}
	return ticket, TicketTTL, nil
}

// Consume reads a ticket without a proof key (a profile without a PIN, or a
// ticket an older node minted) as one without PIN proof.
func (s *redisTicketStore) Consume(ctx context.Context, ticket string) (Ticket, bool) {
	pipe := s.client.TxPipeline()
	valueCmd := pipe.GetDel(ctx, redisTicketPrefix+ticket)
	proofCmd := pipe.GetDel(ctx, redisTicketProofPrefix+ticket)
	// Exec reports a missing key as an error; each command's own result says
	// which key it was.
	_, _ = pipe.Exec(ctx)
	value, err := valueCmd.Result()
	if err != nil {
		return Ticket{}, false
	}
	parts := strings.SplitN(value, "|", 2)
	if len(parts) != 2 {
		return Ticket{}, false
	}
	userID, err := strconv.Atoi(parts[0])
	if err != nil || userID <= 0 {
		return Ticket{}, false
	}
	binding := Ticket{UserID: userID, ProfileID: parts[1]}
	if proof, err := proofCmd.Result(); err == nil {
		binding.ProfileToken = proof
	}
	return binding, true
}
