package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestAdminAPIKeyCreationHoldsOwnerRulesAgainstPromotionDB: a key minted for
// another account follows the Owner rules on both transports, and a non-Owner
// admin minting one while the Owner promotes that account is refused. The
// promotion deletes the account's keys in its own transaction, so a key
// inserted after it on a check read before it would leave the admin holding
// a bearer for another administrator.
func TestAdminAPIKeyCreationHoldsOwnerRulesAgainstPromotionDB(t *testing.T) {
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
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	insert := func(label, role string, owner bool) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled, is_owner)
			VALUES ($1, $2, 'x', $3, true, $4) RETURNING id`, label+"-"+suffix, label+"-"+suffix+"@example.invalid", role, owner).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM api_keys WHERE user_id = $1`, id)
			_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
		})
		return id
	}
	var ownerID int
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE is_owner`).Scan(&ownerID); err != nil {
		ownerID = insert("owner", models.RoleAdmin, true)
	}
	adminID := insert("admin", models.RoleAdmin, false)
	otherAdminID := insert("other-admin", models.RoleAdmin, false)
	userID := insert("user", models.RoleUser, false)

	users := auth.NewUserRepository(pool)
	h := NewAPIKeyHandler(auth.NewAPIKeyRepository(pool))
	h.Owners = users
	// Each transport returns "" when it created the key, else the error code.
	transports := map[string]func(actorID, targetID int) string{
		"v2": func(actorID, targetID int) string {
			_, err := h.CreateAdminAPIKey(claimsCtx(actorID), targetID, "owner-rules", nil)
			if apiErr, ok := errors.AsType[*APIError](err); ok {
				return apiErr.Code
			}
			if err != nil {
				return err.Error()
			}
			return ""
		},
		"v1": func(actorID, targetID int) string {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/api-keys", strings.NewReader(fmt.Sprintf(`{"label":"owner-rules","user_id":%d}`, targetID)))
			req = req.WithContext(claimsCtx(actorID))
			rec := httptest.NewRecorder()
			h.HandleAdminCreateAPIKey(rec, req)
			if rec.Code == http.StatusCreated {
				return ""
			}
			var body errorResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			return fmt.Sprintf("%d %s", rec.Code, body.Error)
		},
	}
	refused := map[string]string{"v2": codeOwnerProtected, "v1": "403 " + codeOwnerProtected}

	for name, create := range transports {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name              string
				actorID, targetID int
				allowed           bool
			}{
				{"admin for an ordinary account", adminID, userID, true},
				{"admin for itself", adminID, adminID, true},
				{"owner for an admin", ownerID, otherAdminID, true},
				{"admin for another admin", adminID, otherAdminID, false},
				{"admin for the owner", adminID, ownerID, false},
			} {
				want := ""
				if !tc.allowed {
					want = refused[name]
				}
				if got := create(tc.actorID, tc.targetID); got != want {
					t.Errorf("%s: got %q, want %q", tc.name, got, want)
				}
			}

			targetID := insert(name+"-promoted", models.RoleUser, false)
			if got := createDuringPromotion(t, pool, users, targetID, func() string { return create(adminID, targetID) }); got != refused[name] {
				t.Errorf("creation racing the promotion: got %q, want %q", got, refused[name])
			}
			var keys int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM api_keys WHERE user_id = $1`, targetID).Scan(&keys); err != nil {
				t.Fatal(err)
			}
			if keys != 0 {
				t.Fatalf("promoted account holds %d API keys a non-Owner admin minted", keys)
			}
		})
	}
}

// createDuringPromotion promotes account targetID to admin and runs create
// once it is blocked on the promotion's lock on the account, then lets the
// promotion commit and returns create's result.
func createDuringPromotion(t *testing.T, pool *pgxpool.Pool, users *auth.UserRepository, targetID int, create func() string) string {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	locked, release := make(chan int, 1), make(chan struct{})
	promoted := make(chan error, 1)
	go func() {
		_, err := users.MutateAdminAccount(waitCtx, targetID, -1, &models.UpdateUserInput{Role: new(models.RoleAdmin)}, func(_ *models.User, tx pgx.Tx) (bool, error) {
			var pid int
			if err := tx.QueryRow(waitCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return false, err
			}
			locked <- pid
			select {
			case <-release:
			case <-waitCtx.Done():
			}
			return false, nil
		})
		promoted <- err
	}()
	var promotionPID int
	select {
	case promotionPID = <-locked:
	case err := <-promoted:
		t.Fatalf("promotion failed before locking the account: %v", err)
	case <-waitCtx.Done():
		t.Fatal("promotion did not lock the account")
	}
	created := make(chan string, 1)
	go func() { created <- create() }()
	for {
		var blocked bool
		if err := pool.QueryRow(waitCtx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE datname = current_database()
			AND $1 = ANY(pg_blocking_pids(pid)))`, promotionPID).Scan(&blocked); err != nil {
			close(release)
			t.Fatalf("waiting for key creation to block on the promotion: %v", err)
		}
		if blocked {
			break
		}
	}
	close(release)
	if err := <-promoted; err != nil {
		t.Fatalf("promotion: %v", err)
	}
	select {
	case got := <-created:
		return got
	case <-waitCtx.Done():
		t.Fatal("key creation did not finish after the promotion")
	}
	return ""
}
