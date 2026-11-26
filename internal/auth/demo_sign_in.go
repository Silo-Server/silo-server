package auth

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/models"
)

// DemoSignInRefreshInterval is how often DemoSignIn.Run re-reads which
// names belong to the demo account.
const DemoSignInRefreshInterval = 30 * time.Second

// DemoSignIn exempts the demo's shared account from the per-name limit on
// failed password sign-ins (SignInAttemptLimiter's name lane), which every
// visitor of a public demo shares. The exemption applies while demo mode is
// on (demomode.SettingKey) and demomode.AccountSettingKey names an existing,
// enabled account that is not an administrator and signs in with its local
// password (DemoAccountUsable), and covers that account's own sign-in names
// only (ownSignInKeys), checked by the local provider. Anything else,
// including a settings or account read that fails, leaves the ordinary
// per-name limit in place. Limits on the client address are not this
// type's and still apply.
//
// The settings are read straight from the store, not through the
// remembered demomode.Setting the demo refusals use. The refusals tighten
// what a visitor may do, so on a failed read they keep the last value read;
// the exemption loosens a limit, so it rests only on a read that just
// succeeded.
//
// An attempt for any other name must do what it does without the exemption,
// with no query whose presence or timing depends on the name. So the names
// to exempt are worked out off the sign-in path (Run, Refresh) and an
// attempt only compares its budget key with them in memory. An attempt
// whose key matches re-reads the settings and the account before it is
// exempted, and is exempted only when that read succeeds, so an account
// promoted to administrator, disabled or deleted, demo mode turned off, or
// keys left from an earlier refresh never grant the exemption by
// themselves.
//
// One DemoSignIn serves every Service a process runs (SetDemoSignIn). It is
// safe for concurrent use; a nil *DemoSignIn exempts no name.
type DemoSignIn struct {
	users    *UserRepository
	settings demomode.Reader
	// keys are the budget keys Refresh last found for the demo account;
	// nil or empty exempts no name.
	keys atomic.Pointer[[]string]
}

// NewDemoSignIn returns a DemoSignIn reading accounts from users and the
// demo settings from settings, a store that returns its read errors (not
// demomode.Setting).
func NewDemoSignIn(users *UserRepository, settings demomode.Reader) *DemoSignIn {
	return &DemoSignIn{users: users, settings: settings}
}

// SetDemoSignIn makes password sign-in skip the per-name budget for the
// demo account d names. Nil exempts no name.
func (s *Service) SetDemoSignIn(d *DemoSignIn) {
	s.demoSignIn = d
}

// Run refreshes the exempt names now and every DemoSignInRefreshInterval
// until ctx is done.
func (d *DemoSignIn) Run(ctx context.Context) {
	d.Refresh(ctx)
	ticker := time.NewTicker(DemoSignInRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.Refresh(ctx)
		}
	}
}

// Refresh re-reads which names belong to the demo account. When a read
// fails it holds no names, so nothing is exempted until a read succeeds;
// that includes a read cut short by ctx ending (shutdown), which leaves
// nothing that grants.
// The names only select which attempts read again in any case: an attempt
// at one of them is exempted only when that read succeeds and still finds
// the name.
func (d *DemoSignIn) Refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, signInLookupTimeout)
	defer cancel()
	keys, ok := d.accountKeys(ctx)
	if !ok {
		keys = nil
	}
	d.keys.Store(&keys)
}

// exempts reports whether spend's name skips the per-name budget, for a
// sign-in through providerID ("" lets the name pick it). Only a password
// the local provider checks against the demo account itself is exempt: a
// named provider other than the local one, or a budget name derived from
// what was typed (which can sign in to another account), keeps the budget.
// The account's own names find it, and an account with local password
// sign-in routes them locally (passwordRoute). A key the database did not
// fold must not decide whose budget a name is (signInNameKeys), so it never
// does.
func (d *DemoSignIn) exempts(ctx context.Context, spend *signInSpend, providerID string) bool {
	if d == nil || !spend.folded || spend.derived || (providerID != "" && providerID != LocalProviderID) {
		return false
	}
	if known := d.keys.Load(); known == nil || !slices.Contains(*known, spend.key) {
		return false
	}
	readCtx, cancel := context.WithTimeout(ctx, signInLookupTimeout)
	defer cancel()
	keys, ok := d.accountKeys(readCtx)
	if !ok && ctx.Err() != nil {
		// The request itself ended (its client went away): this attempt is
		// not exempted, and the names every other attempt relies on stay as
		// they were. Only a read that failed for another reason clears them.
		return false
	}
	if !ok {
		keys = nil
	}
	d.keys.Store(&keys)
	return ok && slices.Contains(keys, spend.key)
}

// accountKeys are the budget keys of the demo account's sign-in names, and
// whether the settings and the account could be read; any failed read is
// false, which exempts no name. No demo mode, no
// account named, or an account that is missing, disabled or an
// administrator's has none.
func (d *DemoSignIn) accountKeys(ctx context.Context) ([]string, bool) {
	if d.settings == nil || d.users == nil {
		return nil, false
	}
	enabled, err := d.settings.Get(ctx, demomode.SettingKey)
	if err != nil {
		return nil, false
	}
	if enabled != demomode.OnValue {
		return nil, true
	}
	raw, err := d.settings.Get(ctx, demomode.AccountSettingKey)
	if err != nil {
		return nil, false
	}
	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || id <= 0 {
		return nil, true
	}
	user, err := d.users.GetByID(ctx, id)
	if IsNotFound(err) {
		return nil, true
	}
	if err != nil {
		slog.WarnContext(ctx, "the demo account could not be read", "component", "auth", "error", err)
		return nil, false
	}
	if !DemoAccountUsable(user) {
		return nil, true
	}
	return ownSignInKeys(ctx, d.users, user)
}

// DemoAccountUsable reports whether user may be the demo account the
// per-name sign-in limit skips: enabled, not an administrator, and signing
// in with its local password, the one a demo shares with its visitors.
func DemoAccountUsable(user *models.User) bool {
	return user != nil && user.Enabled && user.Role != models.RoleAdmin && user.LocalPasswordLoginEnabled
}

// Refusals of a demomode.AccountSettingKey value.
var (
	ErrDemoAccountNotFound = errors.New("demo.account_id must name an existing account")
	ErrDemoAccountAdmin    = errors.New("demo.account_id must not name an administrator")
)

// CheckDemoAccountSetting refuses a demomode.AccountSettingKey value that
// names no account (ErrDemoAccountNotFound) or an administrator's
// (ErrDemoAccountAdmin). Empty names none and is accepted. A disabled
// account, or one without local password sign-in, is accepted: it is
// exempted from nothing until DemoAccountUsable holds.
func CheckDemoAccountSetting(ctx context.Context, db rowQuerier, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return ErrDemoAccountNotFound
	}
	user, err := userByID(ctx, db, id)
	if IsNotFound(err) {
		return ErrDemoAccountNotFound
	}
	if err != nil {
		return err
	}
	if user.Role == models.RoleAdmin {
		return ErrDemoAccountAdmin
	}
	return nil
}
