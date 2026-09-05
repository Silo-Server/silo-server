// Package executor runs tier-1 scenario catalogs against the real API router.
//
// Requests go through api.NewRouter with the production middleware stack in
// an in-process httptest server. Two router variants exist:
//
//   - offline: no database pool that can connect, so only the routes that
//     register without one exist. Public discovery, health, and readiness
//     failure cases run here in plain CI.
//   - live: a real Postgres named by SILO_SCENARIO_DATABASE_URL, migrated and
//     seeded with a deterministic synthetic household. Everything else runs
//     here and is skipped when the variable is unset.
//
// SILO_SCENARIO_DATABASE_URL is deliberately not SILO_TEST_DATABASE_URL: the
// executor TRUNCATEs users, access groups, invite codes, and invitations on
// every reseed (restoring only the migration-seeded default group), which
// would break the other DB-gated packages sharing the test database (and two
// executors sharing one database collide). Point it at
// an empty database the executor owns. Before touching anything, including
// running migrations, the executor inspects the database and refuses one
// that holds any media item, media file, or library folder; any account
// whose email is missing or not a fixture address; any server_settings row
// whose key looks secret-bearing (secret, password, api_key, access_key,
// token_secret, jwt) with a non-empty value; or a branding.server_name other
// than the fixture's. Other settings rows are not a refusal signal.
//
// Principals are minted from the synthetic fixture set at run time; catalogs
// never carry credentials. Scenario bodies reference minted values through
// ${...} placeholders (see placeholders in run.go).
package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apiv2 "github.com/Silo-Server/silo-server/contracts/api/v2"
	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/contractledger"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/migrations"
)

// DatabaseEnv names the environment variable that points the executor at
// its own scratch database. See the package documentation for why it is not
// SILO_TEST_DATABASE_URL.
const DatabaseEnv = "SILO_SCENARIO_DATABASE_URL"

// fixtureProviderID names the executor's second login provider.
const fixtureProviderID = "fixture-directory"

// rejectingProvider is a credentials provider that never authenticates; it
// exists so /auth/providers lists more than one entry.
type rejectingProvider struct{}

func (rejectingProvider) Authenticate(context.Context, auth.Credentials) (*models.User, error) {
	return nil, auth.ErrInvalidCredentials
}

func (rejectingProvider) ValidateSession(context.Context, string) (bool, error) { return false, nil }

// Fixture account names, as catalogs reference them in principal.user.
const (
	fixtureAdmin    = "admin"
	fixtureMember   = "member"
	fixtureGrouped  = "grouped"
	fixtureDisabled = "disabled"
)

// Synthetic fixture constants. Every value is reserved or fictional.
const (
	jwtSecret     = "scenario-catalog-fixture-jwt-secret-0000000000000000"
	masterKey     = "scenario-catalog-fixture-master-key-000000000000000"
	serverName    = "Silo Fixture"
	serverID      = "00000000-0000-4000-8000-0000000000f1"
	publicURL     = "https://silo.example.test"
	adminUsername = "fixture-admin"
	adminEmail    = "fixture-admin@silo.example.test"
	adminPassword = "fixture-admin-password"
	memberUser    = "fixture-member"
	memberEmail   = "fixture-member@silo.example.test"
	memberPass    = "fixture-member-password"
	groupedUser   = "fixture-grouped"
	groupedEmail  = "fixture-grouped@silo.example.test"
	groupedPass   = "fixture-grouped-password"
	disabledUser  = "fixture-disabled"
	disabledEmail = "fixture-disabled@silo.example.test"
	disabledPass  = "fixture-disabled-password"
	lockedPIN     = "2468"
	inviteCode    = "FIXTURE1"
	inviteCodeOff = "FIXTURE0"
	inviteFull    = "FIXTURE9"
	inviteToken   = "fixture-invitation-token-pending"
	inviteTokenUs = "fixture-invitation-token-accepted"
	deviceCode    = "fixture-device-code-pending"
	browserCode   = "fixture-browser-code-pending"
	userCode      = "FIXTURE1"
	deviceCodeRem = "fixture-device-code-remote"
	browserCodeRm = "fixture-browser-code-remote"
	userCodeRem   = "FIXTURE2"
	deviceCodeDen = "fixture-device-code-denied"
	browserCodeDn = "fixture-browser-code-denied"
	userCodeDen   = "FIXTURE3"
	deviceCodeExp = "fixture-device-code-expired"
	browserCodeEx = "fixture-browser-code-expired"
	userCodeExp   = "FIXTURE4"
	deviceCodeApp = "fixture-device-code-approved"
	browserCodeAp = "fixture-browser-code-approved"
	userCodeApp   = "FIXTURE5"
	deviceCodeRA  = "fixture-device-code-remote-approved"
	browserCodeRA = "fixture-browser-code-remote-approved"
	userCodeRA    = "FIXTURE6"
	deviceIDA     = "fixture-device-a"
	deviceIDB     = "fixture-device-b"

	// adminLockedPIN locks a profile on the admin account so a cross-account
	// verify-pin probe with the right PIN can only 404 through the account
	// scope, never through the no-PIN branch.
	adminLockedPIN = "1357"

	// Profile IDs are fixed UUIDs so catalogs can name them directly.
	profileAdminPrimary   = "00000000-0000-4000-8000-0000000000a1"
	profileAdminSecondary = "00000000-0000-4000-8000-0000000000a2"
	profileAdminLocked    = "00000000-0000-4000-8000-0000000000a3"
	profilePrimary        = "00000000-0000-4000-8000-0000000000b1"
	profileSecondary      = "00000000-0000-4000-8000-0000000000b2"
	profileChild          = "00000000-0000-4000-8000-0000000000b3"
	profileLocked         = "00000000-0000-4000-8000-0000000000b4"
	profileGroupedPrimary = "00000000-0000-4000-8000-0000000000c1"
	profileMissing        = "00000000-0000-4000-8000-0000000000ff"
)

// Env is one executor environment: an in-process server, its variants, and
// the synthetic fixture identities the principals draw on.
type Env struct {
	t   testing.TB
	ctx context.Context

	// Offline router (no reachable database) for CI-runnable scenarios, and
	// the method+pattern set it registered: a row absent from it can only be
	// exercised on the live router.
	offline       *httptest.Server
	offlineRoutes *scenariocatalog.OfflineRouteSet
	// ledger tells the executor which rows are the rate-limited
	// registration variant, so their scenarios run on liveLimited.
	ledger map[contractledger.Key]scenariocatalog.LedgerEntry
	// Live routers, only when a database is configured.
	live        *httptest.Server
	liveLimited *httptest.Server
	pool        *pgxpool.Pool
	settings    catalog.SettingsStore
	stores      userstore.UserStoreProvider
	jwt         *auth.JWTService
	auth        *auth.Service
	profileTok  *access.ProfileTokenService
	limiter     *ratelimit.Middleware
	policy      *policy.System

	users    map[string]*models.User
	sessions map[string]string // fixture user -> session id
	apiKeys  map[string]string // scope list key -> api key
	fixtures map[string]string // placeholder -> value
}

// HasDatabase reports whether database-gated scenarios can run.
func (e *Env) HasDatabase() bool { return e.pool != nil }

// OfflineHas reports whether the offline router registered the row at all.
// Rows behind a user store, policy system, or auth middleware do not exist
// there, so their public scenarios (missing bearer, and so on) still need
// the live router.
//
// The answer comes from contracts/api/v2/offline-routes.txt rather than a
// walk: api.NewRouter returns a sealed handler that no caller can recover a
// router from, so TestOfflineRouteSet in internal/api walks the same wiring
// through the unexported constructor and commits the result. New checks the
// file's wiring line against the Dependencies it builds, so a golden from a
// different wiring is refused rather than trusted.
func (e *Env) OfflineHas(method, pattern string) bool {
	return e.offlineRoutes.Has(method, pattern)
}

// New builds the environment. Database wiring happens only when
// SILO_SCENARIO_DATABASE_URL is set; the offline router always exists.
func New(t testing.TB) *Env {
	t.Helper()
	ctx := context.Background()
	e := &Env{t: t, ctx: ctx, users: map[string]*models.User{}, sessions: map[string]string{}, apiKeys: map[string]string{}, fixtures: map[string]string{}}
	ledger, err := scenariocatalog.LoadLedger()
	if err != nil {
		t.Fatalf("scenario executor: %v", err)
	}
	e.ledger = ledger

	cfg := e.config()
	// A pool whose target never answers: the router registers the
	// DB-dependent routes (the pool is non-nil) but every query fails, which
	// is the "database unreachable" state readiness must report.
	deadPool, err := pgxpool.New(ctx, "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("scenario executor: build offline pool: %v", err)
	}
	t.Cleanup(deadPool.Close)
	cipher, err := secret.New([]byte(masterKey))
	if err != nil {
		t.Fatalf("scenario executor: cipher: %v", err)
	}
	offlineDeps := api.Dependencies{
		Config:           cfg,
		AppContext:       ctx,
		DB:               deadPool,
		SecretCipher:     cipher,
		ClientIPResolver: clientip.NewResolver(nil),
		NodeID:           "fixture-node",
		PublicURL:        publicURL,
	}
	e.offline = httptest.NewServer(api.NewRouter(offlineDeps))
	t.Cleanup(e.offline.Close)
	e.offlineRoutes, err = scenariocatalog.LoadOfflineRoutes()
	if err != nil {
		t.Fatalf("scenario executor: %v", err)
	}
	if got := scenariocatalog.WiringFields(offlineDeps); !slices.Equal(got, e.offlineRoutes.Wiring) {
		t.Fatalf("scenario executor: %s was generated for wiring %v but the offline router is built with %v; "+
			"align TestOfflineRouteSet (internal/api) with this constructor and run make offline-routes",
			apiv2.OfflineRoutesPath, e.offlineRoutes.Wiring, got)
	}

	dsn := os.Getenv(DatabaseEnv)
	if dsn == "" {
		return e
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("scenario executor: connect %s: %v", DatabaseEnv, err)
	}
	t.Cleanup(pool.Close)
	e.pool = pool
	// Inspect before migrating: a mistargeted DSN must not be moved forward
	// to this branch's schema, let alone truncated.
	e.guardScratchDatabase()
	if err := database.RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("scenario executor: migrate: %v", err)
	}
	e.settings = catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher)
	e.stores = pgstore.NewPostgresProvider(pool)
	e.jwt = auth.NewJWTService(cfg.Auth.JWTSecret, cfg.Auth.AccessTokenExpiry, cfg.Auth.RefreshTokenExpiry)
	e.profileTok = access.NewProfileTokenService(cfg.Auth.JWTSecret, 0)
	// The same service the router builds, so impersonation tokens come from
	// the real StartImpersonation path rather than a hand-built JWT.
	userRepo := auth.NewUserRepository(pool)
	sessionRepo := auth.NewSessionRepository(pool)
	e.auth = auth.NewService(auth.NewLocalProvider(userRepo, sessionRepo), e.jwt, sessionRepo, userRepo,
		auth.NewInviteCodeRepository(pool), e.settings, e.stores)

	e.policy = policy.NewSystem(policy.NewPolicyStore(pool), nil, slog.Default())
	if err := e.policy.Start(ctx); err != nil {
		t.Fatalf("scenario executor: policy system: %v", err)
	}
	t.Cleanup(e.policy.Stop)

	perKey := ratelimit.NewMemoryLimiter()
	global := ratelimit.NewMemoryLimiter()
	t.Cleanup(perKey.Close)
	t.Cleanup(global.Close)
	e.limiter = ratelimit.NewMiddleware(perKey, global, e.settings, true)

	deps := func(limited bool) api.Dependencies {
		d := api.Dependencies{
			Config:            cfg,
			AppContext:        ctx,
			DB:                pool,
			SecretCipher:      cipher,
			ClientIPResolver:  clientip.NewResolver(nil),
			NodeID:            "fixture-node",
			PublicURL:         publicURL,
			UserStoreProvider: e.stores,
			PolicySystem:      e.policy,
			// A second credentials provider whose display name sorts after
			// "Local" makes the providers list ordering observable (default
			// first, then display name); it accepts no login.
			AuthProviders: []auth.RegisteredProvider{{
				Info:     auth.LoginProviderInfo{ID: fixtureProviderID, DisplayName: "Fixture Directory", Mode: "credentials"},
				Provider: rejectingProvider{},
			}},
		}
		if limited {
			d.RateLimitMW = e.limiter
		}
		return d
	}
	e.live = httptest.NewServer(api.NewRouter(deps(false)))
	t.Cleanup(e.live.Close)
	e.liveLimited = httptest.NewServer(api.NewRouter(deps(true)))
	t.Cleanup(e.liveLimited.Close)

	e.Reseed()
	return e
}

func (e *Env) config() *config.Config {
	cfg, err := config.LoadFromDB(map[string]string{
		"auth.jwt_secret":             jwtSecret,
		"jellyfin_compat.server_name": serverName,
		"jellyfin_compat.server_id":   serverID,
	})
	if err != nil {
		e.t.Fatalf("scenario executor: config: %v", err)
	}
	return cfg
}

// Reseed wipes the synthetic household and recreates it. The scratch
// database is owned by the executor; it refuses to run against a database
// that holds anything it did not create.
func (e *Env) Reseed() {
	e.t.Helper()
	ctx := e.ctx
	e.guardScratchDatabase()

	// Truncating users cascades to sessions, api keys, profiles, devices,
	// device logins (SET NULL), invitations, and everything else keyed by
	// user_id. Rows with no user FK are cleared explicitly.
	for _, stmt := range []string{
		`TRUNCATE TABLE users RESTART IDENTITY CASCADE`,
		`TRUNCATE TABLE device_login_requests`,
		`TRUNCATE TABLE invite_codes RESTART IDENTITY CASCADE`,
		`TRUNCATE TABLE invitations RESTART IDENTITY CASCADE`,
		`TRUNCATE TABLE access_groups RESTART IDENTITY CASCADE`,
		// Truncating access_groups also removes the seeded default group
		// (migrations/sql/20260702173000_default_access_group.sql), and
		// users.Create assigns that group to every new non-admin account.
		// Restore the migration's exact row before any fixture user exists so
		// the scratch database matches a real install.
		`INSERT INTO access_groups (
			name, description, is_default, library_ids, max_playback_quality,
			download_allowed, download_transcode_allowed, max_streams, max_transcodes,
			allowed_permissions, requests_allowed
		) VALUES (
			'Default Group', 'Applied automatically to newly created users.', true, NULL, '',
			true, false, 5, 5,
			ARRAY['marker_edit'], true
		)`,
		`DELETE FROM server_settings WHERE key IN ('demo.enabled','signup.enabled','notifications.email.external_url','branding.server_name','sections.allow_profile_custom_sections')`,
	} {
		if _, err := e.pool.Exec(ctx, stmt); err != nil {
			e.t.Fatalf("scenario executor: reseed %q: %v", stmt, err)
		}
	}
	if err := ratelimit.SeedDefaults(ctx, e.settings); err != nil {
		e.t.Fatalf("scenario executor: seed rate limits: %v", err)
	}
	if err := e.limiter.Reload(ctx); err != nil {
		e.t.Fatalf("scenario executor: reload rate limits: %v", err)
	}
	e.mustSetting("branding.server_name", serverName)
	e.mustSetting("signup.enabled", "true")
	// Media requests on, so the onboarding flow's requests step is present
	// for non-child profiles and the child filter has something to remove.
	e.mustExec(`INSERT INTO request_settings (id, requests_enabled) VALUES (true, true)
		ON CONFLICT (id) DO UPDATE SET requests_enabled = true`)
	// Minted lazily per fixture state; see impersonationToken.
	delete(e.fixtures, "impersonation_token")

	users := auth.NewUserRepository(e.pool)
	sessions := auth.NewSessionRepository(e.pool)
	apiKeys := auth.NewAPIKeyRepository(e.pool)
	groups := access.NewGroupStore(e.pool)

	create := func(name, username, email, password, role string) *models.User {
		u, err := users.Create(ctx, models.CreateUserInput{Username: username, Email: email, Password: password, Role: role})
		if err != nil {
			e.t.Fatalf("scenario executor: create user %s: %v", name, err)
		}
		e.users[name] = u
		return u
	}
	admin := create(fixtureAdmin, adminUsername, adminEmail, adminPassword, models.RoleAdmin)
	member := create(fixtureMember, memberUser, memberEmail, memberPass, models.RoleUser)
	grouped := create(fixtureGrouped, groupedUser, groupedEmail, groupedPass, models.RoleUser)
	disabled := create(fixtureDisabled, disabledUser, disabledEmail, disabledPass, models.RoleUser)
	off := false
	if err := users.Update(ctx, disabled.ID, models.UpdateUserInput{Enabled: &off}); err != nil {
		e.t.Fatalf("scenario executor: disable user: %v", err)
	}
	disabled.Enabled = false

	group, err := groups.Create(ctx, access.CreateGroupInput{
		Name: "Fixture Group", LibraryIDs: []int{}, DownloadAllowed: false, TranscodeAllowed: true, AudioTranscodeAllowed: true,
	})
	if err != nil {
		e.t.Fatalf("scenario executor: create access group: %v", err)
	}
	if err := users.Update(ctx, grouped.ID, models.UpdateUserInput{AccessGroupID: models.SetValue(group.ID)}); err != nil {
		e.t.Fatalf("scenario executor: assign access group: %v", err)
	}

	// Profiles. The first profile per account becomes primary.
	e.mustProfile(admin.ID, userstore.Profile{ID: profileAdminPrimary, Name: "Fixture Admin"})
	e.mustProfile(admin.ID, userstore.Profile{ID: profileAdminSecondary, Name: "Fixture Admin Two"})
	// The admin's locked profile is only ever named in a path by another
	// account; admin_secondary stays PIN-free because scenarios declare it
	// as the acting profile and would otherwise stop at profile_unverified.
	e.mustProfile(admin.ID, userstore.Profile{ID: profileAdminLocked, Name: "Fixture Admin Locked"})
	adminPIN := adminLockedPIN
	adminStore, err := e.stores.ForUser(ctx, admin.ID)
	if err != nil {
		e.t.Fatalf("scenario executor: admin store: %v", err)
	}
	if err := adminStore.UpdateProfile(ctx, profileAdminLocked, userstore.UpdateProfileInput{PIN: &adminPIN}); err != nil {
		e.t.Fatalf("scenario executor: lock admin profile: %v", err)
	}
	e.mustProfile(member.ID, userstore.Profile{ID: profilePrimary, Name: "Fixture Parent"})
	e.mustProfile(member.ID, userstore.Profile{ID: profileSecondary, Name: "Fixture Teen"})
	e.mustProfile(member.ID, userstore.Profile{ID: profileChild, Name: "Fixture Kid", IsChild: true, MaxContentRating: "PG"})
	e.mustProfile(member.ID, userstore.Profile{ID: profileLocked, Name: "Fixture Locked"})
	pin := lockedPIN
	store, err := e.stores.ForUser(ctx, member.ID)
	if err != nil {
		e.t.Fatalf("scenario executor: member store: %v", err)
	}
	if err := store.UpdateProfile(ctx, profileLocked, userstore.UpdateProfileInput{PIN: &pin}); err != nil {
		e.t.Fatalf("scenario executor: lock profile: %v", err)
	}
	e.mustProfile(grouped.ID, userstore.Profile{ID: profileGroupedPrimary, Name: "Fixture Grouped"})

	// Devices seen by two member profiles, with deterministic last-seen order.
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		e.t.Fatalf("scenario executor: user store has no device registry")
	}
	for i, d := range []userstore.DeviceEntry{
		{ProfileID: profilePrimary, DeviceID: deviceIDA, DeviceName: "Fixture TV", DevicePlatform: "tvos"},
		{ProfileID: profilePrimary, DeviceID: deviceIDB, DeviceName: "Fixture Phone", DevicePlatform: "ios"},
		{ProfileID: profileSecondary, DeviceID: "fixture-device-c", DeviceName: "Fixture Tablet", DevicePlatform: "android"},
	} {
		if err := registry.RegisterDevice(ctx, d); err != nil {
			e.t.Fatalf("scenario executor: register device: %v", err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE user_devices SET last_seen_at = $1 WHERE user_id = $2 AND device_id = $3`,
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i)*time.Hour), member.ID, d.DeviceID); err != nil {
			e.t.Fatalf("scenario executor: device last_seen: %v", err)
		}
	}

	// One login session per account for bearer principals.
	for name, u := range e.users {
		id := fmt.Sprintf("00000000-0000-4000-8000-0000000d%04d", u.ID)
		if err := sessions.Create(ctx, models.AuthSession{ID: id, UserID: u.ID, DeviceName: "fixture-client", IPAddress: "127.0.0.1", ExpiresAt: time.Now().Add(24 * time.Hour)}); err != nil {
			e.t.Fatalf("scenario executor: session for %s: %v", name, err)
		}
		e.sessions[name] = id
	}
	// A second, already-revoked session for the member so revoked_at has a
	// non-null case in the session list.
	revoked := "00000000-0000-4000-8000-00000000dead"
	if err := sessions.Create(ctx, models.AuthSession{ID: revoked, UserID: member.ID, DeviceName: "fixture-old-client", IPAddress: "127.0.0.1", ExpiresAt: time.Now().Add(24 * time.Hour)}); err != nil {
		e.t.Fatalf("scenario executor: revoked session: %v", err)
	}
	if err := sessions.Revoke(ctx, revoked); err != nil {
		e.t.Fatalf("scenario executor: revoke: %v", err)
	}
	e.fixtures["member_revoked_session_id"] = revoked

	// API keys: unscoped and scoped, both owned by the admin.
	unscoped, err := apiKeys.Create(ctx, admin.ID, "fixture-unscoped", nil)
	if err != nil {
		e.t.Fatalf("scenario executor: api key: %v", err)
	}
	e.apiKeys[""] = unscoped.Key
	scoped, err := apiKeys.Create(ctx, admin.ID, "fixture-scoped", []string{auth.ScopeAdminUsers})
	if err != nil {
		e.t.Fatalf("scenario executor: scoped api key: %v", err)
	}
	e.apiKeys[auth.ScopeAdminUsers] = scoped.Key
	memberKey, err := apiKeys.Create(ctx, member.ID, "fixture-member-key", nil)
	if err != nil {
		e.t.Fatalf("scenario executor: member api key: %v", err)
	}
	e.fixtures["admin_api_key_id"] = strconv.FormatInt(unscoped.ID, 10)
	e.fixtures["member_api_key_id"] = strconv.FormatInt(memberKey.ID, 10)

	// Invite codes: usable, disabled, exhausted.
	codes := auth.NewInviteCodeRepository(e.pool)
	usable, err := codes.Create(ctx, models.CreateInviteCodeInput{Code: inviteCode, Label: "fixture usable", MaxUses: 5, CreatedBy: admin.ID})
	if err != nil {
		e.t.Fatalf("scenario executor: invite code: %v", err)
	}
	offCode, err := codes.Create(ctx, models.CreateInviteCodeInput{Code: inviteCodeOff, Label: "fixture disabled", MaxUses: 5, CreatedBy: admin.ID})
	if err != nil {
		e.t.Fatalf("scenario executor: invite code: %v", err)
	}
	if err := codes.Update(ctx, offCode.ID, models.UpdateInviteCodeInput{Enabled: &off}); err != nil {
		e.t.Fatalf("scenario executor: disable invite code: %v", err)
	}
	full, err := codes.Create(ctx, models.CreateInviteCodeInput{Code: inviteFull, Label: "fixture exhausted", MaxUses: 1, CreatedBy: admin.ID})
	if err != nil {
		e.t.Fatalf("scenario executor: invite code: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE invite_codes SET use_count = max_uses WHERE id = $1`, full.ID); err != nil {
		e.t.Fatalf("scenario executor: exhaust invite code: %v", err)
	}
	e.fixtures["invite_code_id"] = strconv.Itoa(usable.ID)
	e.fixtures["invite_code_disabled_id"] = strconv.Itoa(offCode.ID)

	// Invitations: one pending, one accepted, one expired.
	e.mustExec(`INSERT INTO invitations (email, token_hash, role, create_profile, show_tour, note, invited_by, expires_at)
		VALUES ($1, $2, 'user', true, true, 'fixture pending', $3, now() + interval '7 days')`,
		"fixture-invitee@silo.example.test", hashInvite(inviteToken), admin.ID)
	e.mustExec(`INSERT INTO invitations (email, token_hash, role, create_profile, show_tour, note, invited_by, expires_at, accepted_at, accepted_user_id)
		VALUES ($1, $2, 'user', true, true, 'fixture accepted', $3, now() + interval '7 days', now() - interval '1 day', $4)`,
		memberEmail, hashInvite(inviteTokenUs), admin.ID, member.ID)
	e.mustExec(`INSERT INTO invitations (email, token_hash, role, create_profile, show_tour, note, invited_by, expires_at)
		VALUES ($1, $2, 'user', true, true, 'fixture expired', $3, now() - interval '1 day')`,
		"fixture-expired@silo.example.test", hashInvite("fixture-invitation-token-expired"), admin.ID)
	var pendingID, acceptedID int64
	if err := e.pool.QueryRow(ctx, `SELECT id FROM invitations WHERE token_hash = $1`, hashInvite(inviteToken)).Scan(&pendingID); err != nil {
		e.t.Fatalf("scenario executor: pending invitation id: %v", err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT id FROM invitations WHERE token_hash = $1`, hashInvite(inviteTokenUs)).Scan(&acceptedID); err != nil {
		e.t.Fatalf("scenario executor: accepted invitation id: %v", err)
	}
	e.fixtures["invitation_pending_id"] = strconv.FormatInt(pendingID, 10)
	e.fixtures["invitation_accepted_id"] = strconv.FormatInt(acceptedID, 10)

	// Device-login requests in every state the handlers distinguish.
	insertDeviceLogin := func(id, dev, browser, user, status, purpose string, temporary bool, expires string) {
		e.mustExec(`INSERT INTO device_login_requests
			(id, device_code_hash, browser_code_hash, user_code_hash, match_code, device_name, device_platform, ip_address,
			 status, client_purpose, temporary, expires_at)
			VALUES ($1, $2, $3, $4, '42', 'Fixture TV', 'tvos', '127.0.0.1', $5, $6, $7, now() + $8::interval)`,
			id, hashDevice(dev), hashDevice(browser), hashDevice(user), status, purpose, temporary, expires)
	}
	insertDeviceLogin("00000000-0000-4000-8000-0000000000e1", deviceCode, browserCode, userCode, "pending", "device_login", false, "10 minutes")
	insertDeviceLogin("00000000-0000-4000-8000-0000000000e2", deviceCodeRem, browserCodeRm, userCodeRem, "pending", "remote_playback", true, "10 minutes")
	insertDeviceLogin("00000000-0000-4000-8000-0000000000e3", deviceCodeDen, browserCodeDn, userCodeDen, "denied", "device_login", false, "10 minutes")
	insertDeviceLogin("00000000-0000-4000-8000-0000000000e4", deviceCodeExp, browserCodeEx, userCodeExp, "pending", "device_login", false, "-1 minutes")
	insertDeviceLogin("00000000-0000-4000-8000-0000000000e5", deviceCodeApp, browserCodeAp, userCodeApp, "approved", "device_login", false, "10 minutes")
	e.mustExec(`UPDATE device_login_requests SET approved_by_user_id = $1, approved_at = now() WHERE id = '00000000-0000-4000-8000-0000000000e5'`, member.ID)
	insertDeviceLogin("00000000-0000-4000-8000-0000000000e6", deviceCodeRA, browserCodeRA, userCodeRA, "approved", "remote_playback", true, "10 minutes")
	e.mustExec(`UPDATE device_login_requests SET approved_by_user_id = $1, approved_profile_id = $2, approved_at = now() WHERE id = '00000000-0000-4000-8000-0000000000e6'`, member.ID, profilePrimary)

	e.fixtures["admin_user_id"] = strconv.Itoa(admin.ID)
	e.fixtures["member_user_id"] = strconv.Itoa(member.ID)
	e.fixtures["grouped_user_id"] = strconv.Itoa(grouped.ID)
	e.fixtures["member_session_id"] = e.sessions[fixtureMember]
	e.fixtures["admin_session_id"] = e.sessions[fixtureAdmin]
	e.fixtures["access_group_id"] = strconv.FormatInt(group.ID, 10)
	e.fixtures["locked_profile_token"] = e.mintProfileToken(member, profileLocked)
}

// guardScratchDatabase refuses to touch a database that looks like anything
// other than the executor's own scratch space. It runs before migrations
// (so it tolerates tables that do not exist yet) and before every reseed.
// Foreign signals: any media item, file, or library folder; any account
// whose email is not a fixture address (NULL and empty count as foreign,
// since real deployments allow both); any server setting that carries a
// secret-looking value the seeder never writes, or a branding name that is
// not the fixture's.
func (e *Env) guardScratchDatabase() {
	e.t.Helper()
	const fixtureEmailPattern = "%@silo.example.test"
	checks := []struct {
		what  string
		table string
		where string
		args  []any
	}{
		{"media items", "media_items", "", nil},
		{"media files", "media_files", "", nil},
		{"library folders", "media_folders", "", nil},
		{"non-fixture users", "users", "email IS NULL OR btrim(email::text) = '' OR email::text NOT ILIKE $1", []any{fixtureEmailPattern}},
		// ratelimit.* keys are budgets the server seeds on startup, and
		// ratelimit.auth.password_change.* would otherwise match `password`.
		{"non-fixture server settings", "server_settings",
			"(key NOT LIKE 'ratelimit.%' AND key ~ '(secret|password|api_key|access_key|token_secret|jwt)' AND value <> '') OR (key = 'branding.server_name' AND value <> $1)",
			[]any{serverName}},
	}
	var found []string
	for _, c := range checks {
		n, err := e.countIfTableExists(c.table, c.where, c.args...)
		if err != nil {
			e.t.Fatalf("scenario executor: inspect %s: %v", c.table, err)
		}
		if n > 0 {
			found = append(found, fmt.Sprintf("%d %s", n, c.what))
		}
	}
	if len(found) > 0 {
		e.t.Fatalf("scenario executor: %s points at a database with %s; refusing to migrate or reseed a database that holds data the executor did not create",
			DatabaseEnv, strings.Join(found, ", "))
	}
}

// countIfTableExists counts rows matching where in table, or returns 0 when
// the table has not been created yet (a fresh database before migrations).
func (e *Env) countIfTableExists(table, where string, args ...any) (int, error) {
	var exists bool
	if err := e.pool.QueryRow(e.ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	q := `SELECT count(*) FROM ` + table
	if where != "" {
		q += ` WHERE ` + where
	}
	var n int
	if err := e.pool.QueryRow(e.ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (e *Env) mustExec(sql string, args ...any) {
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("scenario executor: %s: %v", strings.Fields(sql)[0], err)
	}
}

func (e *Env) mustSetting(key, value string) {
	if err := e.settings.Set(e.ctx, key, value); err != nil {
		e.t.Fatalf("scenario executor: setting %s: %v", key, err)
	}
}

func (e *Env) mustProfile(userID int, p userstore.Profile) {
	store, err := e.stores.ForUser(e.ctx, userID)
	if err != nil {
		e.t.Fatalf("scenario executor: store: %v", err)
	}
	if err := store.CreateProfile(e.ctx, p); err != nil {
		e.t.Fatalf("scenario executor: profile %s: %v", p.Name, err)
	}
}

func (e *Env) mintProfileToken(u *models.User, profileID string) string {
	token, _, err := e.profileTok.Mint(access.ProfileTokenClaims{
		UserID: u.ID, SessionID: e.sessions[userName(u)], ProfileID: profileID, PolicyRevision: e.currentRevision(u.ID),
	})
	if err != nil {
		e.t.Fatalf("scenario executor: profile token: %v", err)
	}
	return token
}

func (e *Env) currentRevision(userID int) int64 {
	var rev int64
	if err := e.pool.QueryRow(e.ctx, `SELECT access_policy_revision FROM users WHERE id = $1`, userID).Scan(&rev); err != nil {
		e.t.Fatalf("scenario executor: policy revision: %v", err)
	}
	return rev
}

func userName(u *models.User) string {
	switch u.Username {
	case adminUsername:
		return fixtureAdmin
	case memberUser:
		return fixtureMember
	case groupedUser:
		return fixtureGrouped
	case disabledUser:
		return fixtureDisabled
	}
	return u.Username
}

// accessToken mints a bearer for the fixture account's seeded session.
func (e *Env) accessToken(name string) string {
	u, ok := e.users[name]
	if !ok {
		e.t.Fatalf("scenario executor: unknown fixture user %q", name)
	}
	token, err := e.jwt.GenerateAccessToken(u.ID, u.Role, e.sessions[name])
	if err != nil {
		e.t.Fatalf("scenario executor: access token: %v", err)
	}
	return token
}

func (e *Env) refreshToken(name string) string {
	return e.refreshTokenFor(name, e.sessions[name])
}

func (e *Env) refreshTokenFor(name, sessionID string) string {
	u := e.users[name]
	token, err := e.jwt.GenerateRefreshToken(u.ID, u.Role, sessionID)
	if err != nil {
		e.t.Fatalf("scenario executor: refresh token: %v", err)
	}
	return token
}

// impersonationToken starts a real impersonation of the member by the
// seeded admin session through auth.Service.StartImpersonation (the path
// behind POST /admin/users/{id}/impersonate) and returns its access token,
// so /auth/me and /auth/impersonation/end see the token shape and session
// row the product issues. One impersonation session per fixture state: a
// repeated request reuses the token, and Reseed drops it. Scenarios that
// reference the token carry fresh_state because it adds a session row.
func (e *Env) impersonationToken() string {
	if token, ok := e.fixtures["impersonation_token"]; ok {
		return token
	}
	admin, member := e.users[fixtureAdmin], e.users[fixtureMember]
	adminClaims := &auth.Claims{UserID: admin.ID, Role: admin.Role, SessionID: e.sessions[fixtureAdmin], TokenType: auth.TokenTypeAccess}
	pair, _, _, err := e.auth.StartImpersonation(auth.WithClaims(e.ctx, adminClaims), admin.ID, member.ID, "fixture-client", "127.0.0.1")
	if err != nil {
		e.t.Fatalf("scenario executor: start impersonation: %v", err)
	}
	e.fixtures["impersonation_token"] = pair.AccessToken
	return pair.AccessToken
}

// server picks the router variant for a scenario.
func (e *Env) server(needsDB, dbUnavailable, rateLimited bool) (*httptest.Server, error) {
	switch {
	case dbUnavailable:
		return e.offline, nil
	case !needsDB && !rateLimited:
		// Public scenarios that only need the router prefer the live server
		// when one exists (its routes are all registered) and fall back to
		// the offline router otherwise.
		if e.live != nil {
			return e.live, nil
		}
		return e.offline, nil
	case e.live == nil:
		return nil, errors.New(DatabaseEnv + " is not set")
	case rateLimited:
		return e.liveLimited, nil
	}
	return e.live, nil
}
