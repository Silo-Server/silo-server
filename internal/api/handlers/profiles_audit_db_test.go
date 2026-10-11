package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestProfileAuditDB drives the shared profile service as v2 createProfile,
// updateProfile and deleteProfile do, against a migrated database: each
// access change records one domain row with the PIN as a set/not-set marker,
// and preference-only, refused and v1 writes record none.
func TestProfileAuditDB(t *testing.T) {
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
	name := fmt.Sprintf("profile-audit-%d", time.Now().UnixNano())
	var account int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, email, password_hash, role, enabled) VALUES ($1, $2, 'x', 'user', true) RETURNING id`,
		name, name+"@example.invalid").Scan(&account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, account) })
	h := NewProfileHandler(pgstore.NewPostgresProvider(pool))
	request := func(method, pattern, path string, optIn bool) context.Context {
		ctx, _ := audittest.Context(t, pool, audittest.Request{Method: method, Pattern: pattern, Path: path, UserID: &account, OptIn: optIn})
		return ctx
	}
	create := func(ctx context.Context, active string, req ProfileCreateRequest) ProfileView {
		t.Helper()
		view, err := h.CreateProfile(ctx, ProfileCreateCommand{UserID: account, ActiveProfileID: active, Request: req,
			VerifyProfile: func(string) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	parent := create(request(http.MethodPost, "/api/v2/profiles", "/api/v2/profiles", true), "", ProfileCreateRequest{Name: "Parent"})
	row := audittest.One(t, pool, auditmutation.ActionProfileCreated, auditmutation.TargetProfile, parent.ID)
	if *row.UserID != account || row.StatusCode != http.StatusCreated || audittest.Value(row.Change(t, "is_primary").After) != "true" ||
		audittest.Value(row.Change(t, "pin").After) != "false" || audittest.Value(row.Change(t, "name").After) != `"Parent"` {
		t.Fatalf("primary profile row: %s", row.Raw)
	}

	const pin, nextPIN = "4321", "9876"
	child := create(request(http.MethodPost, "/api/v2/profiles", "/api/v2/profiles", true), parent.ID,
		ProfileCreateRequest{Name: "Kid", IsChild: true, PIN: pin, MaxContentRating: "PG"})
	if !child.HasPIN {
		t.Fatal("created profile lost its PIN")
	}
	pinHash := func() string {
		var hash string
		if err := pool.QueryRow(ctx, `SELECT pin_hash FROM user_profiles WHERE id = $1`, child.ID).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	createdChild := audittest.One(t, pool, auditmutation.ActionProfileCreated, auditmutation.TargetProfile, child.ID)
	createdChild.AssertNoSecrets(t, pin, pinHash())
	if audittest.Value(createdChild.Change(t, "pin").After) != "true" || audittest.Value(createdChild.Change(t, "is_child").After) != "true" ||
		audittest.Value(createdChild.Change(t, "max_content_rating").After) != `"PG"` {
		t.Fatalf("child profile row: %s", createdChild.Raw)
	}

	update := func(optIn bool, req ProfileUpdateRequest) error {
		path := "/api/v2/profiles/" + child.ID
		pattern := "/api/v2/profiles/{id}"
		if !optIn {
			path, pattern = "/api/v1/profiles/"+child.ID, "/api/v1/profiles/{id}"
		}
		_, err := h.UpdateProfile(request(http.MethodPatch, pattern, path, optIn), ProfileUpdateCommand{UserID: account, ProfileID: child.ID,
			ActiveProfileID: parent.ID, Request: req, VerifyProfile: func(string) error { return nil }})
		return err
	}
	updates := func() []audittest.Row {
		return audittest.Rows(t, pool, auditmutation.ActionProfileUpdated, auditmutation.TargetProfile, child.ID)
	}
	// A playback preference is not an access change; a taken name is refused.
	if err := update(true, ProfileUpdateRequest{Language: new("de")}); err != nil {
		t.Fatal(err)
	}
	var apiErr *APIError
	if err := update(true, ProfileUpdateRequest{Name: new("Parent")}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("name conflict: %v", err)
	}
	if rows := updates(); len(rows) != 0 {
		t.Fatalf("no-op or refused update recorded: %+v", rows)
	}

	if err := update(true, ProfileUpdateRequest{PIN: new(nextPIN)}); err != nil {
		t.Fatal(err)
	}
	changed := updates()
	if len(changed) != 1 || len(changed[0].Changes) != 1 || audittest.Value(changed[0].Change(t, "pin").Before) != "true" ||
		audittest.Value(changed[0].Change(t, "pin").After) != "true" || changed[0].StatusCode != http.StatusOK {
		t.Fatalf("PIN change: %+v", changed)
	}
	changed[0].AssertNoSecrets(t, pin, nextPIN, pinHash())

	if err := update(true, ProfileUpdateRequest{PIN: new(""), Name: new("Kiddo"), IsChild: new(false)}); err != nil {
		t.Fatal(err)
	}
	cleared := updates()
	if len(cleared) != 2 || audittest.Value(cleared[1].Change(t, "pin").After) != "false" ||
		audittest.Value(cleared[1].Change(t, "name").Before) != `"Kid"` || audittest.Value(cleared[1].Change(t, "is_child").After) != "false" {
		t.Fatalf("clear PIN and rename: %+v", cleared)
	}
	// The frozen v1 route records no domain row.
	if err := update(false, ProfileUpdateRequest{Name: new("Kid")}); err != nil {
		t.Fatal(err)
	}
	if rows := updates(); len(rows) != 2 {
		t.Fatalf("v1 update recorded: %+v", rows)
	}

	err = h.DeleteProfile(request(http.MethodDelete, "/api/v2/profiles/{id}", "/api/v2/profiles/"+child.ID, true),
		ProfileDeleteCommand{UserID: account, ProfileID: child.ID, ActiveProfileID: parent.ID, VerifyProfile: func(string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	deleted := audittest.One(t, pool, auditmutation.ActionProfileDeleted, auditmutation.TargetProfile, child.ID)
	if audittest.Value(deleted.Change(t, "name").Before) != `"Kid"` || deleted.Change(t, "name").After != nil || deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("delete row: %s", deleted.Raw)
	}
}
