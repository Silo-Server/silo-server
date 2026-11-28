package directconn

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/golang-jwt/jwt/v5"
)

const (
	testAuthSecret = "synthetic-direct-ticket-test-secret"
	// testSessionID is the login session that enrolls a device unless a test
	// names another.
	testSessionID = "session-1"
)

// testClock is a settable clock shared by the store, the coordination server
// and the ticket checks.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testNodeKey(b byte) protocol.NodeKey {
	var k protocol.NodeKey
	k[0], k[31] = b, 1
	return k
}

func newTestCoordination(t *testing.T, store CoordinationStore, clock *testClock) *Coordination {
	t.Helper()
	if store == nil {
		store = newMemoryStore()
	}
	c, err := NewCoordination(CoordinationConfig{
		Store: store, AuthSecret: testAuthSecret, Clock: clock.Now,
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func enrollTestDevice(t *testing.T, c *Coordination, uid int, device string, key protocol.NodeKey) Enrollment {
	t.Helper()
	return enrollTestDeviceSession(t, c, uid, testSessionID, device, key)
}

// enrollTestDeviceSession enrolls device as the login session sessionID.
func enrollTestDeviceSession(t *testing.T, c *Coordination, uid int, sessionID, device string, key protocol.NodeKey) Enrollment {
	t.Helper()
	e, err := c.Enroll(t.Context(), EnrollRequest{UserID: uid, ProfileID: "profile-1", SessionID: sessionID, DeviceID: device, NodeKey: key, ConnectionPath: "automatic"})
	if err != nil {
		t.Fatalf("Enroll(%d, %q, session %q) = %v", uid, device, sessionID, err)
	}
	return e
}

func TestTicket(t *testing.T) {
	clock := newTestClock()
	key := testNodeKey(1)
	signWith := func(signingKey []byte, mutate func(*ticketClaims)) string {
		claims := ticketClaims{
			Type: signalingTicketType, UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: "device-a", NodeKey: key.String(),
			RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(clock.Now()), ExpiresAt: jwt.NewNumericDate(clock.Now().Add(enrollmentLease))},
		}
		mutate(&claims)
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(signingKey)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	rows := []struct {
		name    string
		ticket  func(c *Coordination) string
		wantErr bool
	}{
		{name: "valid", ticket: func(c *Coordination) string { return enrollTestDevice(t, c, 7, "device-a", key).Ticket }},
		{name: "expired", ticket: func(c *Coordination) string {
			ticket := enrollTestDevice(t, c, 7, "device-a", key).Ticket
			clock.Advance(enrollmentLease + time.Minute)
			return ticket
		}, wantErr: true},
		{name: "wrong typ", ticket: func(c *Coordination) string {
			enrollTestDevice(t, c, 7, "device-a", key)
			return signWith(c.tickets.key, func(cl *ticketClaims) { cl.Type = "stream" })
		}, wantErr: true},
		{name: "signed with the stream-token key", ticket: func(c *Coordination) string {
			enrollTestDevice(t, c, 7, "device-a", key)
			return signWith([]byte(testAuthSecret), func(*ticketClaims) {})
		}, wantErr: true},
		{name: "entry deleted", ticket: func(c *Coordination) string {
			ticket := enrollTestDevice(t, c, 7, "device-a", key).Ticket
			if err := c.Withdraw(t.Context(), 7, "device-a"); err != nil {
				t.Fatal(err)
			}
			return ticket
		}, wantErr: true},
		{name: "entry with another node key", ticket: func(c *Coordination) string {
			ticket := enrollTestDevice(t, c, 7, "device-a", key).Ticket
			enrollTestDevice(t, c, 7, "device-a", testNodeKey(2))
			return ticket
		}, wantErr: true},
		// A ticket names the login session that enrolled the entry. The entry
		// belongs to the session that last enrolled or renewed it, so an older
		// issuer's ticket stops authorizing at the next renewal by another
		// session, though the node key is the same.
		{name: "same key renewed by another session", ticket: func(c *Coordination) string {
			ticket := enrollTestDeviceSession(t, c, 7, "session-a", "device-a", key).Ticket
			enrollTestDeviceSession(t, c, 7, "session-b", "device-a", key)
			return ticket
		}, wantErr: true},
		{name: "ticket without a session claim", ticket: func(c *Coordination) string {
			enrollTestDevice(t, c, 7, "device-a", key)
			return signWith(c.tickets.key, func(cl *ticketClaims) { cl.SessionID = "" })
		}, wantErr: true},
		{name: "ticket naming a session that did not enroll the entry", ticket: func(c *Coordination) string {
			enrollTestDevice(t, c, 7, "device-a", key)
			return signWith(c.tickets.key, func(cl *ticketClaims) { cl.SessionID = "session-other" })
		}, wantErr: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			clock.now = newTestClock().now
			c := newTestCoordination(t, nil, clock)
			ticket := row.ticket(c)
			principal, device, err := c.Authorize(t.Context(), ticket)
			if row.wantErr {
				// Every refusal is the same error, so a caller cannot tell which check failed.
				if !errors.Is(err, ErrInvalidTicket) {
					t.Fatalf("Authorize = %v, want exactly ErrInvalidTicket", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Authorize = %v", err)
			}
			wantRef := server.DeviceRef{Account: server.AccountID(strconv.Itoa(7)), Device: "device-a"}
			if principal.Ref != wantRef || principal.NodeKey != key || device.NodeKey != key {
				t.Fatalf("principal = %+v, device key = %v", principal, device.NodeKey)
			}
			if want := clock.Now().Add(enrollmentLease).Truncate(time.Second); !principal.Expires.Equal(want) {
				t.Fatalf("principal expires %v, want the lease end %v", principal.Expires, want)
			}
		})
	}
}

// A store outage is not a refusal: the caller answers 503, not 401.
func TestTicketStoreOutageIsNotARefusal(t *testing.T) {
	clock := newTestClock()
	store := &outageStore{CoordinationStore: newMemoryStore()}
	c := newTestCoordination(t, store, clock)
	ticket := enrollTestDevice(t, c, 7, "device-a", testNodeKey(1)).Ticket
	store.fail = true
	if _, _, err := c.Authorize(context.Background(), ticket); !errors.Is(err, server.ErrUnavailable) || errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("Authorize with the store down = %v, want server.ErrUnavailable", err)
	}
}

// outageStore fails the reads and writes a revocation or authorization makes
// once fail is set.
type outageStore struct {
	CoordinationStore
	fail bool
}

func (s *outageStore) Device(ctx context.Context, ref server.DeviceRef, now time.Time) (server.Device, error) {
	if s.fail {
		return server.Device{}, server.ErrUnavailable
	}
	return s.CoordinationStore.Device(ctx, ref, now)
}

func (s *outageStore) ListDevices(ctx context.Context, account server.AccountID, now time.Time) ([]server.Device, error) {
	if s.fail {
		return nil, server.ErrUnavailable
	}
	return s.CoordinationStore.ListDevices(ctx, account, now)
}

// The ticket key is a wire contract: every replica derives it from the auth
// secret with this label and no salt, so a change to either must fail here.
// Clients never hold the secret or the key. The expected value is HKDF-SHA256 computed independently of the code.
func TestTicketKeyDerivation(t *testing.T) {
	signer, err := newTicketSigner(testAuthSecret)
	if err != nil {
		t.Fatal(err)
	}
	const want = "1e2cc8d6f9fe8b133f62c904a6d280573e0b133938d0a97b408eb9c9e470d1c5"
	if got := hex.EncodeToString(signer.key); got != want {
		t.Fatal("ticket key differs from the independently derived HKDF-SHA256 value")
	}
}
