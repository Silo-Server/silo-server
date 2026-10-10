package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestUpdateCollectionSyncScheduleDB drives updateCollection's sync_schedule
// and the sync_cadence reads through the real collection handler and
// Postgres stores, for a regular account and an admin account.
func TestUpdateCollectionSyncScheduleDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	pg := pgstore.NewPostgresProvider(pool)
	// The test tokens name accounts 1 (member) and 2 (admin); each maps to a
	// fresh Postgres account.
	provider := collectionGuardHTTPProvider{stores: map[int]userstore.UserStore{}}
	suffix := time.Now().UnixNano()
	for tokenUser, role := range map[int]string{1: "user", 2: "admin"} {
		var account int
		if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,$2) RETURNING id`, fmt.Sprintf("v2-sync-schedule-%d-%d", suffix, tokenUser), role).Scan(&account); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id=$1`, account)
			_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM user_collection_revisions WHERE user_id=$1`, account)
		})
		store, err := pg.ForUser(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		provider.stores[tokenUser] = store
	}
	member, admin := provider.stores[1], provider.stores[2]
	memberHeaders, adminHeaders := viewerHeaders(), with(bearer(adminToken), "X-Profile-Id", "p-primary")
	if err := member.CreateProfile(ctx, userstore.Profile{ID: "p-owner", Name: "Owner"}); err != nil {
		t.Fatal(err)
	}
	if err := admin.CreateProfile(ctx, userstore.Profile{ID: "p-primary", Name: "Admin", IsPrimary: true}); err != nil {
		t.Fatal(err)
	}
	deps := pilotDeps(nil, nil)
	deps.PersonalCollections = handlers.NewCollectionHandler(provider)
	h := newTestHandler(t, deps)

	daily := usercollections.AllowedSyncSchedules["daily"]
	createSynced := func(t *testing.T, store userstore.UserStore, profile string) string {
		t.Helper()
		next := time.Now().Add(12 * time.Hour)
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
			CreatorProfileID: profile, Name: "Synced", CollectionType: "mdblist", QueryDefinition: "{}",
			SourceURL: "https://mdblist.com/lists/u/top", SourceConfig: `{"mode":"mdblist","url":"https://mdblist.com/lists/u/top"}`,
			SyncSchedule: &daily, NextSyncAt: &next,
		})
		if err != nil {
			t.Fatal(err)
		}
		return "/api/v2/collections/" + c.ID
	}
	type collection struct {
		SyncSchedule string  `json:"sync_schedule"`
		SyncCadence  *string `json:"sync_cadence"`
		NextSyncAt   *string `json:"next_sync_at"`
	}
	read := func(t *testing.T, path string, headers map[string]string) (collection, string) {
		t.Helper()
		rec := guardHTTPGet(t, h, path, headers)
		var c collection
		if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c, rec.Header().Get("ETag")
	}
	patch := func(t *testing.T, path, body string, headers map[string]string) int {
		t.Helper()
		_, tag := read(t, path, headers)
		return do(t, h, http.MethodPatch, path, body, with(headers, "If-Match", tag)).Code
	}
	requireSchedule := func(t *testing.T, path string, headers map[string]string, schedule, cadence string, next bool) {
		t.Helper()
		c, _ := read(t, path, headers)
		if c.SyncSchedule != schedule || c.SyncCadence == nil || *c.SyncCadence != cadence || (c.NextSyncAt != nil) != next {
			t.Fatalf("sync_schedule %q, sync_cadence %v, next_sync_at %v; want %q, %q, next %v", c.SyncSchedule, c.SyncCadence, c.NextSyncAt, schedule, cadence, next)
		}
	}
	requireRefused := func(t *testing.T, path, body string, headers map[string]string) {
		t.Helper()
		_, tag := read(t, path, headers)
		p := requireProblem(t, do(t, h, http.MethodPatch, path, body, with(headers, "If-Match", tag)), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != "body.sync_schedule" {
			t.Fatalf("errors = %+v", p.Errors)
		}
		if _, after := read(t, path, headers); after != tag {
			t.Fatalf("refused update changed the collection: ETag %s, was %s", after, tag)
		}
	}

	t.Run("reads report the cadence", func(t *testing.T) {
		path := createSynced(t, member, "p-owner")
		requireSchedule(t, path, memberHeaders, daily, "daily", true)
		// A stored expression no cadence name produces reads as custom.
		id := strings.TrimPrefix(path, "/api/v2/collections/")
		if _, err := pool.Exec(ctx, `UPDATE user_personal_collections SET sync_schedule='*/30 * * * *' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		requireSchedule(t, path, memberHeaders, "*/30 * * * *", "custom", true)
		manual := guardHTTPCreate(t, h, `{"name":"Manual","collection_type":"manual"}`)
		requireSchedule(t, manual, memberHeaders, "", "", false)
	})

	t.Run("a named cadence replaces the schedule", func(t *testing.T) {
		path := createSynced(t, member, "p-owner")
		for _, cadence := range []string{"weekly", "monthly", "daily"} {
			if code := patch(t, path, `{"sync_schedule":"`+cadence+`"}`, memberHeaders); code != http.StatusOK {
				t.Fatalf("%s: status %d", cadence, code)
			}
			requireSchedule(t, path, memberHeaders, usercollections.AllowedSyncSchedules[cadence], cadence, true)
		}
		if code := patch(t, path, `{"sync_schedule":""}`, memberHeaders); code != http.StatusOK {
			t.Fatalf("off: status %d", code)
		}
		requireSchedule(t, path, memberHeaders, "", "", false)
	})

	t.Run("cron expressions are refused for every account", func(t *testing.T) {
		for name, account := range map[string]struct {
			store   userstore.UserStore
			profile string
			headers map[string]string
		}{"regular": {member, "p-owner", memberHeaders}, "admin": {admin, "p-primary", adminHeaders}} {
			path := createSynced(t, account.store, account.profile)
			for _, body := range []string{`{"sync_schedule":"30 4 * * *"}`, `{"sync_schedule":"*/15 * * * *"}`, `{"sync_schedule":null}`} {
				t.Run(name+" "+body, func(t *testing.T) { requireRefused(t, path, body, account.headers) })
			}
			requireSchedule(t, path, account.headers, daily, "daily", true)
		}
	})

	t.Run("manual and smart collections take no schedule", func(t *testing.T) {
		for _, body := range []string{`{"name":"Manual","collection_type":"manual"}`, `{"name":"Smart","collection_type":"smart","query_definition":{"match":"all","groups":[]}}`} {
			path := guardHTTPCreate(t, h, body)
			requireRefused(t, path, `{"sync_schedule":"daily"}`, memberHeaders)
			requireSchedule(t, path, memberHeaders, "", "", false)
		}
	})

	t.Run("capabilities report the schedule as editable", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/api/v2/collections/capabilities", "", memberHeaders)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sync_schedule_editable":true`) {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
}
