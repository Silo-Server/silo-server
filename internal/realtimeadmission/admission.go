// Package realtimeadmission limits charged client work independently of HTTP
// settings. Handlers apply its local shield before decoding, classify supported
// operations, and check Admit before entering any downstream work.
package realtimeadmission

import (
	"context"
	"encoding/base64"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

// Class separates event snapshots, room recovery/reports and explicit actions.
type Class string

const (
	Events       Class = "events"
	RoomRecovery Class = "room_recovery"
	RoomAction   Class = "room_action"

	RateLimitedCode  = "realtime_rate_limited"
	UnavailableCode  = "realtime_unavailable"
	InputLimitedCode = "realtime_input_limited"
	InputCloseReason = "realtime input shield"

	admissionDeadline = 250 * time.Millisecond
	accountIdle       = 10 * time.Minute
)

// Identity must come from authenticated authority, not a claimed client type.
type Identity struct {
	UserID    int
	ProfileID string
	RoomID    string
}

// Refusal always precedes downstream work. It conveys no success or delivery
// guarantee and does not close a healthy socket.
type Refusal struct { //nolint:errname // Refusal is the public admission error contract.
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (r *Refusal) Error() string { return r.Message }

// Controller is one router-owned dependency shared by both socket types and API
// versions. Reusing it preserves account shielding across socket replacement.
type Controller struct {
	store     ratelimit.WorkStore
	status    Status
	config    configuration
	mu        sync.Mutex
	accounts  map[int]*inputAccount
	nextSweep time.Time
}

type configuration struct {
	now          func() time.Time
	events       ratelimit.WorkLimit
	recovery     RoomLimits
	actions      RoomLimits
	socketRate   float64
	socketBurst  int
	accountRate  float64
	accountBurst int
}

func defaults() configuration {
	return configuration{
		now:    time.Now,
		events: ratelimit.WorkLimit{PerSecond: 20, PerMinute: 120},
		recovery: RoomLimits{
			Member:  ratelimit.WorkLimit{PerSecond: 8, PerMinute: 360},
			Account: ratelimit.WorkLimit{PerSecond: 32, PerMinute: 1440},
		},
		actions: RoomLimits{
			Member:  ratelimit.WorkLimit{PerSecond: 4, PerMinute: 30},
			Account: ratelimit.WorkLimit{PerSecond: 8, PerMinute: 120},
		},
		socketRate: 16, socketBurst: 32, accountRate: 64, accountBurst: 128,
	}
}

// New uses a dedicated work store selected at startup. The store and backend
// must agree; HTTP enable/rate/reload settings never enter this controller.
func New(store ratelimit.WorkStore, backend string) *Controller {
	return newController(store, backend, defaults())
}

func newController(store ratelimit.WorkStore, backend string, config configuration) *Controller {
	c := &Controller{store: store, config: config, accounts: make(map[int]*inputAccount)}
	c.status = newStatus(backend, uuid.NewString(), config)
	return c
}

// Admit makes at most two sequential store decisions under one total deadline.
// A later denial, cancellation or ambiguous completion can spend earlier credit;
// it never grants a downstream permit and never refunds that credit.
func (c *Controller) Admit(ctx context.Context, class Class, identity Identity) error {
	if ctx.Err() != nil || c.store == nil || identity.UserID <= 0 || c.status.Scope == unavailableScope {
		return unavailable()
	}
	accountLimit, memberLimit, ok := c.limits(class)
	if !ok || (class != Events && (identity.ProfileID == "" || identity.RoomID == "")) {
		return unavailable()
	}
	ctx, cancel := context.WithTimeout(ctx, admissionDeadline)
	defer cancel()
	prefix := "realtime:" + string(class) + ":"
	user := strconv.Itoa(identity.UserID)
	if class != Events {
		// Independent base64url components cannot contain the ':' separator.
		member := prefix + "member:" + user + ":" +
			base64.RawURLEncoding.EncodeToString([]byte(identity.ProfileID)) + ":" +
			base64.RawURLEncoding.EncodeToString([]byte(identity.RoomID))
		if err := c.check(ctx, member, memberLimit); err != nil {
			return err
		}
	}
	return c.check(ctx, prefix+"account:"+user, accountLimit)
}

func (c *Controller) limits(class Class) (account, member ratelimit.WorkLimit, ok bool) {
	switch class {
	case Events:
		return c.config.events, member, true
	case RoomRecovery:
		return c.config.recovery.Account, c.config.recovery.Member, true
	case RoomAction:
		return c.config.actions.Account, c.config.actions.Member, true
	default:
		return account, member, false
	}
}

func (c *Controller) check(ctx context.Context, key string, limit ratelimit.WorkLimit) error {
	if ctx.Err() != nil {
		return unavailable()
	}
	result := c.store.AllowWork(ctx, key, limit)
	if ctx.Err() != nil || result.Unavailable {
		return unavailable()
	}
	if result.Allowed && result.RetryAfter == 0 {
		return nil
	}
	if result.Allowed || result.RetryAfter <= 0 {
		return unavailable()
	}
	retry := min(max(result.RetryAfter, 250*time.Millisecond), time.Minute)
	if remainder := retry % time.Millisecond; remainder != 0 {
		retry += time.Millisecond - remainder
	}
	return &Refusal{Code: RateLimitedCode, Message: "Realtime work limit reached; retry after the delay.", RetryAfter: retry}
}

func unavailable() *Refusal {
	return &Refusal{Code: UnavailableCode, Message: "Realtime work admission is temporarily unavailable.", RetryAfter: 2 * time.Second}
}
