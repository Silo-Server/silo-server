package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// viewerReadTracer counts the plain statements and the batches a pool sends.
type viewerReadTracer struct {
	mu                sync.Mutex
	statements, sends int
}

func (c *viewerReadTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements++
	return ctx
}

func (c *viewerReadTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *viewerReadTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sends++
	return ctx
}

func (c *viewerReadTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (c *viewerReadTracer) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}

func (c *viewerReadTracer) take() (statements, sends int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	statements, sends = c.statements, c.sends
	c.statements, c.sends = 0, 0
	return statements, sends
}

// viewerBatchFixture is a migrated database seeded with accounts, an access
// group, libraries and profiles covering what a viewer scope depends on.
type viewerBatchFixture struct {
	pool                      *pgxpool.Pool
	tracer                    *viewerReadTracer
	provider                  *pgstore.PostgresProvider
	groups                    *access.GroupStore
	tokens                    *access.ProfileTokenService
	suffix                    string
	libraries                 [3]int
	admin, member, staleAdmin int
	locked                    *userstore.Profile
}

func newViewerBatchFixture(t *testing.T, ctx context.Context) *viewerBatchFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	f := &viewerBatchFixture{tracer: &viewerReadTracer{}, suffix: strconv.FormatInt(time.Now().UnixNano(), 10)}
	config.ConnConfig.Tracer = f.tracer
	if f.pool, err = pgxpool.NewWithConfig(ctx, config); err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(f.pool.Close)
	f.provider = pgstore.NewPostgresProvider(f.pool)
	f.groups = access.NewGroupStore(f.pool)
	f.tokens = access.NewProfileTokenService("viewer-batch-profile-secret", 0)

	for i := range f.libraries {
		if err := f.pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`,
			fmt.Sprintf("viewer-batch-%s-%d", f.suffix, i)).Scan(&f.libraries[i]); err != nil {
			t.Fatalf("seed library: %v", err)
		}
		f.cleanup(t, `DELETE FROM media_folders WHERE id = $1`, f.libraries[i])
	}
	group, err := f.groups.Create(ctx, access.CreateGroupInput{
		Name: "viewer-batch-" + f.suffix, LibraryIDs: []int{f.libraries[0], f.libraries[1]},
		MaxPlaybackQuality: access.PlaybackQualityStandard, RequestsAllowed: true,
	})
	if err != nil {
		t.Fatalf("seed access group: %v", err)
	}
	f.cleanup(t, `DELETE FROM access_groups WHERE id = $1`, group.ID)
	f.admin = f.seedUser(t, ctx, "admin", "admin", nil)
	f.member = f.seedUser(t, ctx, "member", "user", &group.ID)
	// An admin row that still names a group resolves as if it did not.
	f.staleAdmin = f.seedUser(t, ctx, "stale-admin", "admin", &group.ID)

	f.seedProfile(t, ctx, f.admin, userstore.Profile{ID: "open-" + f.suffix, Name: "open"}, map[string]string{
		settingskeys.UiDisabledLibraryIds:             fmt.Sprintf("[%d]", f.libraries[2]),
		settingskeys.CatalogMetadataLanguage:          `"de"`,
		settingskeys.CatalogMetadataLanguageOverrides: `{"no":"x-silo-original"}`,
		settingskeys.UiNextUpMode:                     `"separate"`,
		settingskeys.UiNextUpNewReleasesFirst:         `true`,
		settingskeys.HomeHideWatchedItems:             `true`,
	})
	f.locked = f.seedProfile(t, ctx, f.admin, userstore.Profile{
		ID: "locked-" + f.suffix, Name: "locked", PINHash: "pin-hash", MaxContentRating: "PG-13",
	}, nil)
	f.seedProfile(t, ctx, f.admin, userstore.Profile{
		ID: "limited-" + f.suffix, Name: "limited", LibraryRestrictionsEnabled: true,
		AllowedLibraryIDs: []int{f.libraries[2], f.libraries[0]},
	}, nil)
	f.seedProfile(t, ctx, f.member, userstore.Profile{
		ID: "kid-" + f.suffix, Name: "kid", MaxContentRating: "PG", MaxAdvisoryAge: 10, RequireAdvisoryAge: true,
	}, map[string]string{settingskeys.UiDisabledLibraryIds: fmt.Sprintf("[%d]", f.libraries[1])})
	return f
}

func (f *viewerBatchFixture) cleanup(t *testing.T, sql string, id any) {
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), sql, id) })
}

func (f *viewerBatchFixture) seedUser(t *testing.T, ctx context.Context, label, role string, groupID *int64) int {
	t.Helper()
	var id int
	if err := f.pool.QueryRow(ctx, `INSERT INTO users (username, role, access_group_id) VALUES ($1, $2, $3) RETURNING id`,
		fmt.Sprintf("viewer-batch-%s-%s", label, f.suffix), role, groupID).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	f.cleanup(t, `DELETE FROM users WHERE id = $1`, id)
	return id
}

func (f *viewerBatchFixture) seedProfile(
	t *testing.T, ctx context.Context, userID int, profile userstore.Profile, settings map[string]string,
) *userstore.Profile {
	t.Helper()
	store, err := f.provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	if err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	for key, value := range settings {
		if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
			Key: key, Scope: settingscontract.ScopeProfile, ProfileID: profile.ID,
		}, json.RawMessage(value)); err != nil {
			t.Fatalf("store %s: %v", key, err)
		}
	}
	stored, err := store.GetProfile(ctx, profile.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetProfile(%s) = %v, %v", profile.ID, stored, err)
	}
	return stored
}

// profileToken mints a token for the PIN-locked profile at pinRevision.
func (f *viewerBatchFixture) profileToken(t *testing.T, pinRevision int64) string {
	t.Helper()
	token, _, err := f.tokens.Mint(access.ProfileTokenClaims{
		UserID: f.admin, SessionID: "sess", ProfileID: f.locked.ID, PINRevision: pinRevision,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	return token
}

type viewerBatchCase struct {
	name  string
	input access.ResolveInput
	// wantErr is the refusal both resolvers must give; errAny accepts any
	// internal failure.
	wantErr error
	errAny  bool
	// valid reports whether the scope carries what the case is about, so a
	// case cannot pass by both resolvers losing it.
	valid func(access.Scope) bool
}

func viewerBatchCases(t *testing.T, f *viewerBatchFixture) []viewerBatchCase {
	libs := f.libraries
	input := func(userID int, profileID string) access.ResolveInput {
		return access.ResolveInput{UserID: userID, SessionID: "sess", ProfileID: profileID}
	}
	contentOnly := input(f.admin, "open-"+f.suffix)
	contentOnly.ContentAccessOnly = true
	stale, verified, skipped := input(f.admin, f.locked.ID), input(f.admin, f.locked.ID), input(f.admin, f.locked.ID)
	stale.ProfileToken = f.profileToken(t, f.locked.PINRevision+1)
	verified.ProfileToken = f.profileToken(t, f.locked.PINRevision)
	skipped.SkipPINVerification = true
	return []viewerBatchCase{
		{name: "admin without a profile", input: input(f.admin, "")},
		{name: "profile preferences", input: input(f.admin, "open-"+f.suffix), valid: func(s access.Scope) bool {
			return s.NextUpMode == "separate" && s.NextUpNewReleasesFirst && s.HideWatchedItemsOnHome &&
				s.PreferredMetadataLanguage == "de" && reflect.DeepEqual(s.DisabledLibraryIDs, []int{libs[2]})
		}},
		{name: "content access only", input: contentOnly, valid: func(s access.Scope) bool { return s.DisabledLibraryIDs == nil }},
		{name: "PIN-locked profile without a token", input: input(f.admin, f.locked.ID), wantErr: access.ErrProfileUnverified},
		{name: "PIN-locked profile with a stale token", input: stale, wantErr: access.ErrProfileUnverified},
		{name: "PIN-locked profile with a token", input: verified, valid: func(s access.Scope) bool {
			return s.ProfileVerified && !s.PINVerificationSkipped && s.MaxContentRating == "PG-13"
		}},
		{name: "PIN-locked profile skipping verification", input: skipped, valid: func(s access.Scope) bool { return s.PINVerificationSkipped }},
		{name: "profile library limit", input: input(f.admin, "limited-"+f.suffix), valid: func(s access.Scope) bool {
			return s.LibrariesRestricted && reflect.DeepEqual(s.AllowedLibraryIDs, []int{libs[0], libs[2]})
		}},
		{name: "another account's profile", input: input(f.admin, "kid-"+f.suffix), wantErr: access.ErrProfileNotFound},
		{name: "missing profile", input: input(f.admin, "missing-"+f.suffix), wantErr: access.ErrProfileNotFound},
		{name: "group member's profile", input: input(f.member, "kid-"+f.suffix), valid: func(s access.Scope) bool {
			return reflect.DeepEqual(s.AllowedLibraryIDs, []int{libs[0]}) && s.MaxPlaybackQuality == access.PlaybackQualityStandard &&
				s.MaxContentRating == "PG" && s.RequireAdvisoryAge
		}},
		{name: "group member without a profile", input: input(f.member, "")},
		{name: "admin row naming a group", input: input(f.staleAdmin, ""), valid: func(s access.Scope) bool {
			return s.AllowedLibraryIDs == nil && s.MaxPlaybackQuality == ""
		}},
		{name: "missing account", input: input(-1, ""), errAny: true},
	}
}

// TestViewerResolverBatchedReadsMatchEachDB pins the viewer gate's database
// cost: with the user store in the pool's shared tables, a scope costs one
// batch and no other statement, whatever the request carries. The batch must
// answer exactly what reading each fact on its own answers, for every scope
// and every refusal: PIN-locked and foreign profiles, library limits on the
// account, its group and its profile, and the viewer preferences.
func TestViewerResolverBatchedReadsMatchEachDB(t *testing.T) {
	ctx := context.Background()
	f := newViewerBatchFixture(t, ctx)
	pdp := newViewerResolverTestPDP(t, ctx)
	each := NewViewerResolver(auth.NewUserRepository(f.pool), f.provider, f.tokens, pdp, f.groups)
	batched := NewViewerResolver(auth.NewUserRepository(f.pool), f.provider, f.tokens, pdp, f.groups).WithSharedTables(f.pool)
	// A store outside the pool's shared tables, like the SQLite bridge's,
	// keeps reading each fact through its repository.
	outside := NewViewerResolver(auth.NewUserRepository(f.pool), viewerResolverStoreProvider{store: viewerResolverTestStore{}},
		f.tokens, pdp, f.groups).WithSharedTables(f.pool)
	if outside.shared != nil {
		t.Fatal("WithSharedTables enabled batching for a store outside the pool")
	}

	for _, tt := range viewerBatchCases(t, f) {
		t.Run(tt.name, func(t *testing.T) {
			f.tracer.take()
			want, wantErr := each.Resolve(ctx, tt.input)
			if statements, sends := f.tracer.take(); statements == 0 || sends != 0 {
				t.Fatalf("each-fact reads sent %d statements and %d batches", statements, sends)
			}
			got, gotErr := batched.Resolve(ctx, tt.input)
			if statements, sends := f.tracer.take(); statements != 0 || sends != 1 {
				t.Fatalf("batched reads sent %d statements and %d batches, want 0 and 1", statements, sends)
			}
			switch {
			case tt.errAny:
				if wantErr == nil || gotErr == nil || errors.Is(gotErr, access.ErrProfileNotFound) || errors.Is(gotErr, access.ErrProfileUnverified) {
					t.Fatalf("errors = each %v, batched %v; want internal failures", wantErr, gotErr)
				}
			case tt.wantErr != nil:
				if !errors.Is(wantErr, tt.wantErr) || !errors.Is(gotErr, tt.wantErr) {
					t.Fatalf("errors = each %v, batched %v; want %v", wantErr, gotErr, tt.wantErr)
				}
			case wantErr != nil || gotErr != nil:
				t.Fatalf("errors = each %v, batched %v", wantErr, gotErr)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("scope mismatch\nbatched: %#v\neach:    %#v", got, want)
			}
			if tt.valid != nil && !tt.valid(got) {
				t.Fatalf("scope = %#v, which does not carry what the case sets up", got)
			}
		})
	}
}
