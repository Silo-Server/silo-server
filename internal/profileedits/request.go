// Package profileedits records edits made on this server, outside a profile
// set, to profiles that came from one (docs/architecture/app-keys.md, "Edits
// made on this server"). The person's own edit is a mark the app reads and
// merges; an administrator's edit is an override the set leaves alone.
//
// The store's shared write helpers call the recorder inside their
// transaction. Who made an edit comes from the request: the auth middleware
// names the signed-in account on the request's holder, and a write with no
// holder, no account named, or a context marked WithoutRecording (the set's
// own apply, a restore, a profile import) records nothing.
package profileedits

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
)

// Who made an edit.
const (
	// ByAccount is the person's own edit: their login session, API key or
	// Jellyfin session acting on their own account.
	ByAccount = "account"
	// ByOwner is an administrator's edit: an admin route acting on another
	// account, or an administrator signed in as the account.
	ByOwner = "owner"
)

// Request is one HTTP request's edit state: who is acting, and which
// accounts got an edit, for the wake after the request.
type Request struct {
	mu           sync.Mutex
	actorKnown   bool
	actorUserID  int
	actorIsAdmin bool
	impersonator int
	accounts     []int
	// firstRevision is, per account, the lowest edit revision this request
	// recorded: a row at or above it exists only if an edit committed.
	firstRevision map[int]int64
}

type requestKey struct{}

type withoutRecordingKey struct{}

// WithRequest gives ctx a new request holder.
func WithRequest(ctx context.Context) (context.Context, *Request) {
	req := &Request{}
	return context.WithValue(ctx, requestKey{}, req), req
}

// FromContext is ctx's request holder; nil when it has none. A nil
// *Request ignores SetActor.
func FromContext(ctx context.Context) *Request {
	req, _ := ctx.Value(requestKey{}).(*Request)
	return req
}

// SetActor names the account the request acts as. admin is whether that
// account acts as a server administrator, the fact the admin routes' gate
// checks. impersonator is the administrator signed in as that account, nil
// for none.
func (r *Request) SetActor(userID int, admin bool, impersonator *int) {
	if r == nil || userID <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actorKnown, r.actorUserID, r.actorIsAdmin, r.impersonator = true, userID, admin, 0
	if impersonator != nil {
		r.impersonator = *impersonator
	}
}

// noteAccount remembers that account got an edit in this request, written
// at revision.
func (r *Request) noteAccount(account int, revision int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.accounts, account) {
		r.accounts = append(r.accounts, account)
	}
	if first, ok := r.firstRevision[account]; !ok || revision < first {
		if r.firstRevision == nil {
			r.firstRevision = map[int]int64{}
		}
		r.firstRevision[account] = revision
	}
}

// FirstRevision is the lowest edit revision this request recorded for
// account; false when it recorded none. Revisions come from a sequence, so
// an edit row of account at or above it exists only when one of the
// request's edits committed, or another edit came after them.
func (r *Request) FirstRevision(account int) (int64, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	revision, ok := r.firstRevision[account]
	return revision, ok
}

// Accounts lists the accounts that got an edit in this request.
func (r *Request) Accounts() []int {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.accounts)
}

// WithoutRecording marks ctx so the writes made under it record nothing:
// the profile set's own apply, a restore and a profile import.
func WithoutRecording(ctx context.Context) context.Context {
	return context.WithValue(ctx, withoutRecordingKey{}, true)
}

// Actor is who made an edit to an account's profiles.
type Actor struct {
	// By is ByAccount or ByOwner.
	By string
	// UserID is the account that acted: the account itself, or the
	// administrator's.
	UserID int
}

// ActorFor reports how ctx's edits to account's profiles are recorded;
// false when they are not recorded at all.
//
// The owner is an administrator: one signed in as the account, or one
// acting on another account (the admin routes). An edit to another account's
// profiles by an account that is not an administrator is not recorded: no
// route makes one today, and recording it as either side would be wrong, a
// mark the person never made or an override no administrator set.
func ActorFor(ctx context.Context, account int) (Actor, bool) {
	if off, _ := ctx.Value(withoutRecordingKey{}).(bool); off {
		return Actor{}, false
	}
	req := FromContext(ctx)
	if req == nil {
		return Actor{}, false
	}
	req.mu.Lock()
	defer req.mu.Unlock()
	switch {
	case !req.actorKnown:
		return Actor{}, false
	case req.impersonator != 0:
		return Actor{By: ByOwner, UserID: req.impersonator}, true
	case req.actorUserID == account:
		return Actor{By: ByAccount, UserID: account}, true
	case req.actorIsAdmin:
		return Actor{By: ByOwner, UserID: req.actorUserID}, true
	}
	return Actor{}, false
}

// Waker tells an account's apps that its profiles changed on this server.
// It reports whether a wake was queued for at least one device. A nil
// Waker queues nothing. Called with a request's context after an edit, it
// wakes only when the edit committed (Request.FirstRevision).
//
// A caller that needs the answer, to word a message, calls it after its
// write commits and waits for it: the server's waker returns once the wake is
// queued, within five seconds, and sends it in the background.
type Waker func(ctx context.Context, userID int) bool

// Wake calls w, answering false for a nil Waker.
func (w Waker) Wake(ctx context.Context, userID int) bool {
	if w == nil {
		return false
	}
	return w(ctx, userID)
}

// Middleware gives each request a holder, and after the handler returns
// wakes every account that got an edit. The wakes run on their own goroutine,
// detached from the request's cancellation, so the response never waits for
// them; the waker bounds its own work. It calls the waker for an edit whose
// transaction rolled back too; the waker reads FirstRevision from the context
// and wakes only when an edit committed.
func Middleware(wake Waker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, req := WithRequest(r.Context())
			next.ServeHTTP(w, r.WithContext(ctx))
			accounts := req.Accounts()
			if wake == nil || len(accounts) == 0 {
				return
			}
			wakeCtx := context.WithoutCancel(ctx)
			go func() {
				for _, account := range accounts {
					wakeRecovered(wakeCtx, wake, account)
				}
			}()
		})
	}
}

// wakeRecovered calls wake for account, logging a panic instead of letting
// it end the process: the wake runs outside the HTTP recoverer. It logs the
// panic's type only: the server's waker handles push tokens and the relay
// credential, so the panic's text could carry either.
func wakeRecovered(ctx context.Context, wake Waker, account int) {
	defer func() {
		if p := recover(); p != nil {
			slog.ErrorContext(ctx, "profile set wake panicked", "user_id", account, "panic_type", fmt.Sprintf("%T", p))
		}
	}()
	wake.Wake(ctx, account)
}
