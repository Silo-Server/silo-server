package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/invitations"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type invitationGuardMailer struct{ sends atomic.Int32 }

func (*invitationGuardMailer) Enabled(context.Context) bool               { return true }
func (m *invitationGuardMailer) Send(context.Context, mail.Message) error { m.sends.Add(1); return nil }

type invitationGuardSessions struct{}

func (invitationGuardSessions) LocalPasswordLoginAllowed(context.Context) (bool, error) {
	return true, nil
}
func (invitationGuardSessions) Login(context.Context, string, string, string, string) (*auth.TokenPair, *models.User, error) {
	return nil, nil, errors.New("fixture post-commit sign-in failure")
}

// Drive the registered Huma routes through the production native service and
// stores. The nonce-owned identities are removed by exact IDs/address; only
// session issuance and SMTP are replaced to make post-effect attempts observable.
func TestInvitationNonReplacingNativeHTTPDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires a migrated private PostgreSQL fixture")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	users := auth.NewUserRepository(pool)
	suffix := uuid.NewString()
	admin, err := users.Create(t.Context(), models.CreateUserInput{Username: "invite-guard-" + suffix, Email: "invite-admin-" + suffix + "@example.invalid", Password: "synthetic-password", Role: models.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	email := "invitee-" + suffix + "@example.invalid"
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		if _, err := pool.Exec(ctx, `DELETE FROM invitations WHERE invited_by=$1`, admin.ID); err != nil {
			t.Errorf("remove invitation fixture: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id=$1 OR email=$2`, admin.ID, email); err != nil {
			t.Errorf("remove invitation accounts: %v", err)
		}
		var remaining int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invitations WHERE invited_by=$1 OR email=$2)+(SELECT count(*) FROM users WHERE id=$1 OR email=$2)`, admin.ID, email).Scan(&remaining); err != nil || remaining != 0 {
			t.Errorf("invitation cleanup remaining=%d err=%v", remaining, err)
		}
	})
	mailer := &invitationGuardMailer{}
	repo := invitations.NewRepository(pool)
	service := invitations.NewService(repo, users, auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(pool)), invitationGuardSessions{}, mailer, nil, nil, "https://server.example.invalid")
	deps := parityDeps(false)
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{claims: map[string]*auth.Claims{adminToken: {UserID: admin.ID, Role: models.RoleAdmin, SessionID: "invitation-admin", TokenType: auth.TokenTypeAccess}}}, fakeSessions{roles: map[string]string{"invitation-admin": models.RoleAdmin}}, fakeAPIKeys{}, users)
	deps.Invitations = handlers.NewInvitationHandler(service)
	h := NewHandler(deps)
	body := `{"email":"` + email + `","delivery":"email","replace_existing":false,"max_profiles":2}`
	first := do(t, h, http.MethodPost, Prefix+"/admin/invitations", body, bearer(adminToken))
	if first.Code != http.StatusCreated {
		t.Fatal(first.Code, first.Body.String())
	}
	var delivery InvitationDelivery
	if err := json.Unmarshal(first.Body.Bytes(), &delivery); err != nil {
		t.Fatal(err)
	}
	sourceID, problem := invitationID(delivery.Invitation.ID)
	if problem != nil {
		t.Fatal(problem)
	}
	if delivery.DeliveryStatus != "sent" || mailer.sends.Load() != 1 {
		t.Fatal("fresh guarded creation was not delivered exactly once")
	}
	var before, after string
	readRows := func(dst *string) {
		t.Helper()
		if err := pool.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(i) ORDER BY id)::text FROM invitations i WHERE invited_by=$1`, admin.ID).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	for _, expired := range []bool{false, true} {
		if expired {
			if _, err := pool.Exec(t.Context(), `UPDATE invitations SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, sourceID); err != nil {
				t.Fatal(err)
			}
		}
		readRows(&before)
		requireProblem(t, do(t, h, http.MethodPost, Prefix+"/admin/invitations", body, bearer(adminToken)), TypeConflict)
		readRows(&after)
		if before != after || mailer.sends.Load() != 1 {
			t.Fatalf("conflict changed source or sent email: rows_equal=%v sends=%d", before == after, mailer.sends.Load())
		}
		var accounts int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE email=$1`, email).Scan(&accounts); err != nil || accounts != 0 {
			t.Fatalf("conflict provisioned account: count=%d err=%v", accounts, err)
		}
	}
	// Exact-source resend remains an explicit replacement even after expiration.
	resent := do(t, h, http.MethodPost, Prefix+"/admin/invitations/"+string(delivery.Invitation.ID)+"/resend", `{"delivery":"link"}`, bearer(adminToken))
	if resent.Code != http.StatusCreated {
		t.Fatal(resent.Code, resent.Body.String())
	}
	var replacement InvitationDelivery
	if err := json.Unmarshal(resent.Body.Bytes(), &replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.Invitation.MaxProfiles == nil || *replacement.Invitation.MaxProfiles != 2 || mailer.sends.Load() != 1 {
		t.Fatal("resend lost source cap or sent unrequested mail")
	}
	readRows(&before)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/admin/invitations/"+string(delivery.Invitation.ID)+"/resend", "", bearer(adminToken)), TypeConflict)
	readRows(&after)
	if before != after || mailer.sends.Load() != 1 {
		t.Fatal("stale resend changed replacement or sent mail")
	}
	// Public native acceptance commits the bound cap even when login fails.
	token := strings.TrimPrefix(replacement.ClaimURL, "https://server.example.invalid/invite/")
	accepted := do(t, h, http.MethodPost, Prefix+"/invitations/"+token+"/accept", `{"password":"synthetic-password"}`, nil)
	if accepted.Code != http.StatusCreated || !strings.Contains(accepted.Body.String(), `"login_status":"sign_in_required"`) {
		t.Fatal(accepted.Code, accepted.Body.String())
	}
	account, err := users.GetByEmail(t.Context(), email)
	if err != nil || account.MaxProfiles != 2 {
		t.Fatal("native acceptance lost account cap", err)
	}
	readRows(&before)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/admin/invitations", body, bearer(adminToken)), TypeConflict)
	readRows(&after)
	if before != after || mailer.sends.Load() != 1 {
		t.Fatal("existing-account refusal changed invitation history or sent mail")
	}
}
