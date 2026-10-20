package realtimeadmission

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type storeFunc func(context.Context, string, ratelimit.WorkLimit) ratelimit.WorkResult

func (f storeFunc) AllowWork(ctx context.Context, key string, limit ratelimit.WorkLimit) ratelimit.WorkResult {
	return f(ctx, key, limit)
}

type workCall struct {
	key   string
	limit ratelimit.WorkLimit
}

func tinyController() (*Controller, *time.Duration) {
	elapsed := new(time.Duration)
	epoch := time.Unix(1_800_000_000, 0)
	config := defaults()
	config.now = func() time.Time { return epoch.Add(*elapsed) }
	config.events = ratelimit.WorkLimit{PerSecond: 1, PerMinute: 2}
	config.recovery = RoomLimits{
		Member:  ratelimit.WorkLimit{PerSecond: 1, PerMinute: 2},
		Account: ratelimit.WorkLimit{PerSecond: 2, PerMinute: 3},
	}
	config.actions = config.recovery
	store := ratelimit.NewMemoryWorkStoreWithClock(epoch, func() time.Duration { return *elapsed })
	return newController(store, "memory", config), elapsed
}

func requireAdmitted(t *testing.T, c *Controller, class Class, id Identity) {
	t.Helper()
	if err := c.Admit(t.Context(), class, id); err != nil {
		t.Fatalf("admission refused: %v", err)
	}
}

func requireRefusal(t *testing.T, err error, code string, retry time.Duration) {
	t.Helper()
	r, ok := errors.AsType[*Refusal](err)
	if !ok || r.Code != code || r.RetryAfter != retry || r.Error() == "" {
		t.Fatalf("refusal = %#v, want code=%s retry=%s", err, code, retry)
	}
}

func recordCalls(c *Controller) *[]workCall {
	calls := new([]workCall)
	store := c.store
	c.store = storeFunc(func(ctx context.Context, key string, limit ratelimit.WorkLimit) ratelimit.WorkResult {
		*calls = append(*calls, workCall{key, limit})
		return store.AllowWork(ctx, key, limit)
	})
	return calls
}

func TestAdmissionAccountSharingAndClassIsolation(t *testing.T) {
	c, elapsed := tinyController()
	id := Identity{UserID: 1, ProfileID: "synthetic-profile", RoomID: "synthetic-room"}
	requireAdmitted(t, c, Events, id)
	// Another version/socket/credential needs no distinct key: only authenticated
	// account identity participates in the events scope, regardless of profile.
	otherProfile := id
	otherProfile.ProfileID = "other-profile"
	reconnected := newController(c.store, "memory", c.config)
	requireRefusal(t, reconnected.Admit(t.Context(), Events, otherProfile), RateLimitedCode, time.Second)
	otherAccount := id
	otherAccount.UserID = 2
	requireAdmitted(t, c, Events, otherAccount)
	requireAdmitted(t, c, RoomRecovery, id)
	requireAdmitted(t, c, RoomAction, id)
	*elapsed += time.Second
	requireAdmitted(t, reconnected, Events, otherProfile)
	*elapsed += time.Second
	requireRefusal(t, c.Admit(t.Context(), Events, id), RateLimitedCode, 58*time.Second)
}

func TestAdmissionMemberBeforeAccountConservativelySpends(t *testing.T) {
	c, _ := tinyController()
	c.config.recovery.Member = ratelimit.WorkLimit{PerSecond: 2, PerMinute: 3}
	c.config.recovery.Account = ratelimit.WorkLimit{PerSecond: 1, PerMinute: 2}
	calls := recordCalls(c)
	id := Identity{UserID: 1, ProfileID: "profile-a", RoomID: "room"}
	started := 0
	operation := func(id Identity) error {
		if err := c.Admit(t.Context(), RoomRecovery, id); err != nil {
			return err
		}
		started++ // Synthetic downstream operation, reached only on admission.
		return nil
	}
	if err := operation(id); err != nil {
		t.Fatal(err)
	}
	id.ProfileID = "profile-b"
	for range 2 {
		requireRefusal(t, operation(id), RateLimitedCode, time.Second)
	}
	before := len(*calls)
	requireRefusal(t, operation(id), RateLimitedCode, time.Second)
	if len(*calls) != before+1 || !strings.Contains((*calls)[before].key, ":member:") || started != 1 {
		t.Fatalf("member refusal reached account/downstream: calls=%+v started=%d", *calls, started)
	}
	for i := range before {
		want := ":member:"
		if i%2 == 1 {
			want = ":account:"
		}
		if !strings.Contains((*calls)[i].key, want) {
			t.Fatalf("call %d out of order: %+v", i, (*calls)[i])
		}
	}
}

func TestAdmissionMemberTupleCannotCollide(t *testing.T) {
	c, _ := tinyController()
	calls := recordCalls(c)
	a := Identity{UserID: 1, ProfileID: "a:b", RoomID: "c"}
	b := Identity{UserID: 1, ProfileID: "a", RoomID: "b:c"}
	requireAdmitted(t, c, RoomAction, a)
	requireAdmitted(t, c, RoomAction, b)
	if (*calls)[0].key == (*calls)[2].key || (*calls)[1].key != (*calls)[3].key {
		t.Fatalf("member tuple/account encoding: %+v", *calls)
	}
	requireRefusal(t, c.Admit(t.Context(), RoomAction, a), RateLimitedCode, time.Second)
}

func TestAdmissionOutageRecoveryRetainsCounters(t *testing.T) {
	c, elapsed := tinyController()
	store := c.store
	down := false
	c.store = storeFunc(func(ctx context.Context, key string, limit ratelimit.WorkLimit) ratelimit.WorkResult {
		if down {
			return ratelimit.WorkResult{Unavailable: true}
		}
		return store.AllowWork(ctx, key, limit)
	})
	id := Identity{UserID: 1}
	requireAdmitted(t, c, Events, id)
	down = true
	requireRefusal(t, c.Admit(t.Context(), Events, id), UnavailableCode, 2*time.Second)
	down = false
	requireRefusal(t, c.Admit(t.Context(), Events, id), RateLimitedCode, time.Second)
	*elapsed += time.Second
	requireAdmitted(t, c, Events, id)
}

func TestAdmissionCanceledCompletionSpendsWithoutGrant(t *testing.T) {
	for _, cancelOn := range []int{1, 2} {
		t.Run(strconv.Itoa(cancelOn), func(t *testing.T) {
			c, _ := tinyController()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store, calls := c.store, 0
			c.store = storeFunc(func(ctx context.Context, key string, limit ratelimit.WorkLimit) ratelimit.WorkResult {
				calls++
				result := store.AllowWork(ctx, key, limit)
				if calls == cancelOn {
					cancel()
				}
				return result
			})
			id := Identity{UserID: 1, ProfileID: "profile", RoomID: "room"}
			requireRefusal(t, c.Admit(ctx, RoomRecovery, id), UnavailableCode, 2*time.Second)
			if calls != cancelOn {
				t.Fatalf("cancellation did not stop decisions: %d", calls)
			}
			requireRefusal(t, c.Admit(ctx, RoomRecovery, id), UnavailableCode, 2*time.Second)
			if calls != cancelOn {
				t.Fatal("already canceled attempt reached store")
			}
			requireRefusal(t, c.Admit(t.Context(), RoomRecovery, id), RateLimitedCode, time.Second)
		})
	}
}

func TestAdmissionOneTotalDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var deadline time.Time
		calls := 0
		store := storeFunc(func(ctx context.Context, _ string, _ ratelimit.WorkLimit) ratelimit.WorkResult {
			calls++
			got, ok := ctx.Deadline()
			if !ok || !got.Equal(start.Add(250*time.Millisecond)) {
				t.Fatalf("deadline = %v, present=%t", got, ok)
			}
			if calls == 1 {
				deadline = got
				<-time.After(200 * time.Millisecond)
				return ratelimit.WorkResult{Allowed: true}
			}
			if !got.Equal(deadline) {
				t.Fatal("account decision received a new deadline")
			}
			<-ctx.Done()
			return ratelimit.WorkResult{Allowed: true} // Ambiguous completion never grants.
		})
		c := New(store, redisBackend)
		id := Identity{UserID: 1, ProfileID: "profile", RoomID: "room"}
		requireRefusal(t, c.Admit(t.Context(), RoomAction, id), UnavailableCode, 2*time.Second)
		if calls != 2 || time.Since(start) != 250*time.Millisecond {
			t.Fatalf("deadline calls=%d elapsed=%s", calls, time.Since(start))
		}
	})
}

func TestAdmissionCallerDeadlineWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		c := New(storeFunc(func(ctx context.Context, _ string, _ ratelimit.WorkLimit) ratelimit.WorkResult {
			<-ctx.Done()
			return ratelimit.WorkResult{Unavailable: true}
		}), redisBackend)
		requireRefusal(t, c.Admit(ctx, Events, Identity{UserID: 1}), UnavailableCode, 2*time.Second)
		if time.Since(start) != 100*time.Millisecond {
			t.Fatalf("caller deadline ignored: %s", time.Since(start))
		}
	})
}

func TestAdmissionInvalidAndAmbiguousDecisions(t *testing.T) {
	c, _ := tinyController()
	calls := recordCalls(c)
	for _, tc := range []struct {
		class Class
		id    Identity
	}{
		{Events, Identity{}}, {Class("unknown"), Identity{UserID: 1}},
		{RoomAction, Identity{UserID: 1}},
		{RoomRecovery, Identity{UserID: 1, ProfileID: "profile"}},
	} {
		requireRefusal(t, c.Admit(t.Context(), tc.class, tc.id), UnavailableCode, 2*time.Second)
	}
	if len(*calls) != 0 {
		t.Fatal("invalid identity/class reached store")
	}
	for _, result := range []ratelimit.WorkResult{
		{}, {Allowed: true, Unavailable: true}, {Allowed: true, RetryAfter: time.Second},
		{RetryAfter: -time.Second},
	} {
		c.store = storeFunc(func(context.Context, string, ratelimit.WorkLimit) ratelimit.WorkResult { return result })
		requireRefusal(t, c.Admit(t.Context(), Events, Identity{UserID: 1}), UnavailableCode, 2*time.Second)
	}
	requireRefusal(t, New(nil, redisBackend).Admit(t.Context(), Events, Identity{UserID: 1}), UnavailableCode, 2*time.Second)
}

func TestAdmissionRetryRoundingAndBounds(t *testing.T) {
	for _, tc := range []struct{ input, want time.Duration }{
		{time.Nanosecond, 250 * time.Millisecond},
		{250*time.Millisecond + time.Nanosecond, 251 * time.Millisecond},
		{time.Minute + time.Nanosecond, time.Minute},
	} {
		c := New(storeFunc(func(context.Context, string, ratelimit.WorkLimit) ratelimit.WorkResult {
			return ratelimit.WorkResult{RetryAfter: tc.input}
		}), "memory")
		requireRefusal(t, c.Admit(t.Context(), Events, Identity{UserID: 1}), RateLimitedCode, tc.want)
	}
}

func TestAdmissionHTTPReloadCannotClearDedicatedWork(t *testing.T) {
	c, _ := tinyController()
	httpLimiter := ratelimit.NewMemoryLimiter()
	t.Cleanup(httpLimiter.Close)
	id := Identity{UserID: 1}
	requireAdmitted(t, c, Events, id)
	httpLimiter.Clear()
	requireRefusal(t, c.Admit(t.Context(), Events, id), RateLimitedCode, time.Second)
}
