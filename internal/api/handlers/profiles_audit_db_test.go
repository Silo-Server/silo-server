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

type profileAuditDB struct {
	t       *testing.T
	pool    *pgxpool.Pool
	h       *ProfileHandler
	account int
}

func (d profileAuditDB) request(method, pattern, path string) context.Context {
	ctx, _ := audittest.Context(d.t, d.pool, audittest.Request{Method: method, Pattern: pattern, Path: path, UserID: &d.account, OptIn: true})
	return ctx
}

func (d profileAuditDB) create(active string, req ProfileCreateRequest) ProfileView {
	d.t.Helper()
	view, err := d.h.CreateProfile(d.request(http.MethodPost, "/api/v2/profiles", "/api/v2/profiles"), ProfileCreateCommand{
		UserID: d.account, ActiveProfileID: active, Request: req, VerifyProfile: func(string) error { return nil }})
	if err != nil {
		d.t.Fatal(err)
	}
	return view
}

func (d profileAuditDB) pinHash(id string) string {
	d.t.Helper()
	var hash string
	if err := d.pool.QueryRow(d.t.Context(), `SELECT pin_hash FROM user_profiles WHERE id = $1`, id).Scan(&hash); err != nil {
		d.t.Fatal(err)
	}
	return hash
}

// TestProfileAuditDB drives the shared profile service as v2 createProfile,
// updateProfile and deleteProfile do, against a migrated database: each
// access change records one domain row with the PIN as a set/not-set marker,
// and preference-only and refused writes record none.
func TestProfileAuditDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	name := fmt.Sprintf("profile-audit-%d", time.Now().UnixNano())
	d := profileAuditDB{t: t, pool: pool, h: NewProfileHandler(pgstore.NewPostgresProvider(pool))}
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username, email, password_hash, role, enabled) VALUES ($1, $2, 'x', 'user', true) RETURNING id`,
		name, name+"@example.invalid").Scan(&d.account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, d.account) })

	parent := d.create("", ProfileCreateRequest{Name: "Parent"})
	row := audittest.One(t, pool, auditmutation.ActionProfileCreated, auditmutation.TargetProfile, parent.ID)
	if *row.UserID != d.account || row.StatusCode != http.StatusCreated || audittest.Value(row.Change(t, "is_primary").After) != "true" ||
		audittest.Value(row.Change(t, "pin").After) != "false" || audittest.Value(row.Change(t, "name").After) != `"Parent"` {
		t.Fatalf("primary profile row: %s", row.Raw)
	}

	const pin = "4321"
	child := d.create(parent.ID, ProfileCreateRequest{Name: "Kid", IsChild: true, PIN: pin, MaxContentRating: "PG"})
	if !child.HasPIN {
		t.Fatal("created profile lost its PIN")
	}
	createdChild := audittest.One(t, pool, auditmutation.ActionProfileCreated, auditmutation.TargetProfile, child.ID)
	createdChild.AssertNoSecrets(t, pin, d.pinHash(child.ID))
	if audittest.Value(createdChild.Change(t, "pin").After) != "true" || audittest.Value(createdChild.Change(t, "is_child").After) != "true" ||
		audittest.Value(createdChild.Change(t, "max_content_rating").After) != `"PG"` {
		t.Fatalf("child profile row: %s", createdChild.Raw)
	}

	checkProfileUpdateAudit(t, d, parent.ID, child.ID, pin)

	err = d.h.DeleteProfile(d.request(http.MethodDelete, "/api/v2/profiles/{id}", "/api/v2/profiles/"+child.ID),
		ProfileDeleteCommand{UserID: d.account, ProfileID: child.ID, ActiveProfileID: parent.ID, VerifyProfile: func(string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	deleted := audittest.One(t, pool, auditmutation.ActionProfileDeleted, auditmutation.TargetProfile, child.ID)
	if audittest.Value(deleted.Change(t, "name").Before) != `"Kiddo"` || deleted.Change(t, "name").After != nil || deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("delete row: %s", deleted.Raw)
	}
}

// checkProfileUpdateAudit changes the child profile's PIN, then clears it and
// renames the profile, after a preference-only and a refused update.
func checkProfileUpdateAudit(t *testing.T, d profileAuditDB, parentID, childID, pin string) {
	t.Helper()
	update := func(req ProfileUpdateRequest) error {
		_, err := d.h.UpdateProfile(d.request(http.MethodPatch, "/api/v2/profiles/{id}", "/api/v2/profiles/"+childID), ProfileUpdateCommand{
			UserID: d.account, ProfileID: childID, ActiveProfileID: parentID, Request: req, VerifyProfile: func(string) error { return nil }})
		return err
	}
	updates := func() []audittest.Row {
		return audittest.Rows(t, d.pool, auditmutation.ActionProfileUpdated, auditmutation.TargetProfile, childID)
	}
	// A playback preference is not an access change; a taken name is refused.
	if err := update(ProfileUpdateRequest{Language: new("de")}); err != nil {
		t.Fatal(err)
	}
	var apiErr *APIError
	if err := update(ProfileUpdateRequest{Name: new("Parent")}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("name conflict: %v", err)
	}
	if rows := updates(); len(rows) != 0 {
		t.Fatalf("no-op or refused update recorded: %+v", rows)
	}

	const nextPIN = "9876"
	if err := update(ProfileUpdateRequest{PIN: new(nextPIN)}); err != nil {
		t.Fatal(err)
	}
	changed := updates()
	if len(changed) != 1 || len(changed[0].Changes) != 1 || audittest.Value(changed[0].Change(t, "pin").Before) != "true" ||
		audittest.Value(changed[0].Change(t, "pin").After) != "true" || changed[0].StatusCode != http.StatusOK {
		t.Fatalf("PIN change: %+v", changed)
	}
	changed[0].AssertNoSecrets(t, pin, nextPIN, d.pinHash(childID))

	if err := update(ProfileUpdateRequest{PIN: new(""), Name: new("Kiddo"), IsChild: new(false)}); err != nil {
		t.Fatal(err)
	}
	cleared := updates()
	if len(cleared) != 2 || audittest.Value(cleared[1].Change(t, "pin").After) != "false" ||
		audittest.Value(cleared[1].Change(t, "name").Before) != `"Kid"` || audittest.Value(cleared[1].Change(t, "is_child").After) != "false" {
		t.Fatalf("clear PIN and rename: %+v", cleared)
	}
}
