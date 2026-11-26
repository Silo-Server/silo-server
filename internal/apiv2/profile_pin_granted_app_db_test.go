package apiv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// grantPINDB drives verifyProfilePIN through the router against sessions and
// a PIN-protected profile stored in Postgres, with the production limiters'
// policies, so a session another app's key opened is recognized by the same
// session lookup as in production.
type grantPINDB struct {
	*reachDB
	userID    int
	profileID string
	tokens    *access.ProfileTokenService
}

const grantPINRight, grantPINWrong = "4321", "0000"

func newGrantPINDB(t *testing.T) *grantPINDB {
	t.Helper()
	d := &grantPINDB{reachDB: newReachDB(t), tokens: access.NewProfileTokenService("synthetic-profile-token-secret", time.Hour)}
	d.userID = d.account(t)
	stores := pgstore.NewPostgresProvider(d.pool)
	store, err := stores.ForUser(t.Context(), d.userID)
	if err != nil {
		t.Fatal(err)
	}
	pinHash, err := bcrypt.GenerateFromPassword([]byte(grantPINRight), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	d.profileID = uuid.NewString()
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: d.profileID, Name: "Kid", PINHash: string(pinHash)}); err != nil {
		t.Fatal(err)
	}
	profiles := handlers.NewProfileHandler(stores)
	profiles.PINAttempts = ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy)
	profiles.GrantedAppPINAttempts = ratelimit.NewMemoryAttemptLimiter(ratelimit.GrantedAppPINPolicy)
	profiles.ProfileTokens = d.tokens
	profiles.UserRepo = auth.NewUserRepository(d.pool)
	deps := d.deps
	deps.Profiles = profiles
	d.handler = newTestHandler(t, deps)
	return d
}

// grantedSession stores a session opened by the app device key keyID, or by
// a new grant-enrolled key when keyID is empty, and returns its token and
// the key's id.
func (d *grantPINDB) grantedSession(t *testing.T, keyID string) (token, key string) {
	t.Helper()
	if keyID == "" {
		token = d.store(t, d.userID, reachSession{opened: models.SessionOpenedAppKey, keyVia: models.AppKeyEnrolledGrant})
	} else {
		token = d.store(t, d.userID, reachSession{opened: models.SessionOpenedAppKey})
		if _, err := d.pool.Exec(t.Context(), `UPDATE auth_sessions SET device_key_id = $2 WHERE id = $1`, d.claims[token].SessionID, keyID); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.pool.QueryRow(t.Context(), `SELECT device_key_id::text FROM auth_sessions WHERE id = $1`, d.claims[token].SessionID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return token, key
}

func (d *grantPINDB) verify(t *testing.T, token, pin string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, d.handler, http.MethodPost, Prefix+"/profiles/"+d.profileID+"/verify-pin", `{"pin":"`+pin+`"}`, bearer(token))
}

// requireValid checks the right PIN unlocks the profile for this caller and
// returns the minted token's claims.
func (d *grantPINDB) requireValid(t *testing.T, label, token string) *access.ProfileTokenClaims {
	t.Helper()
	rec := d.verify(t, token, grantPINRight)
	var body ProfileVerification
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || !body.Valid || body.ProfileToken == "" {
		t.Fatalf("%s: right PIN = %d %s, want valid with a token", label, rec.Code, rec.Body.String())
	}
	claims, err := d.tokens.Validate(body.ProfileToken)
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func (d *grantPINDB) requireWrong(t *testing.T, label, token string, n int) {
	t.Helper()
	for i := range n {
		if rec := d.verify(t, token, grantPINWrong); rec.Code != http.StatusOK || rec.Body.String() != `{"valid":false,"expires_at":null}`+"\n" {
			t.Fatalf("%s: wrong PIN %d = %d %s, want valid false", label, i+1, rec.Code, rec.Body.String())
		}
	}
}

// requireLocked checks a PIN check is refused with 429 and a Retry-After of
// at most window.
func (d *grantPINDB) requireLocked(t *testing.T, label, token string, window time.Duration) {
	t.Helper()
	rec := d.verify(t, token, grantPINRight)
	requireProblem(t, rec, TypeRateLimited)
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retry < 1 || time.Duration(retry)*time.Second > window {
		t.Fatalf("%s: Retry-After = %q, want 1..%v", label, rec.Header().Get("Retry-After"), window)
	}
}

// appGuesses sends as many wrong PINs from the app as lock the household's
// own count and returns each answer's status, for requireAppGuessCodes to
// check after the household's devices have had their turn.
func (d *grantPINDB) appGuesses(t *testing.T, app string) []int {
	t.Helper()
	var codes []int
	for range ratelimit.ProfilePINPolicy.MaxAttempts {
		codes = append(codes, d.verify(t, app, grantPINWrong).Code)
	}
	return codes
}

// requireAppGuessCodes checks the app's allowance: its first guesses are
// checked, the rest refused.
func requireAppGuessCodes(t *testing.T, codes []int) {
	t.Helper()
	for i, code := range codes {
		want := http.StatusOK
		if i >= ratelimit.GrantedAppPINPolicy.MaxAttempts {
			want = http.StatusTooManyRequests
		}
		if code != want {
			t.Fatalf("app guess %d = %d, want %d", i+1, code, want)
		}
	}
}

// TestGrantedAppPINAllowanceDB: a session another app's key opened counts
// its PIN attempts under its own key and allowance, so it cannot lock a
// household's profile for the household's own devices, nor reset its own
// lock by signing in again.
func TestGrantedAppPINAllowanceDB(t *testing.T) {
	appWindow := ratelimit.GrantedAppPINPolicy.Window

	// The defect: the app's wrong guesses locked the profile for the
	// person's own phone. After the app has used up its allowance, the
	// person's devices verify the right PIN at once.
	t.Run("app guesses never lock the household", func(t *testing.T) {
		d := newGrantPINDB(t)
		app, _ := d.grantedSession(t, "")
		password := d.store(t, d.userID, reachSession{opened: models.SessionOpenedPassword})
		deviceLogin := d.store(t, d.userID, reachSession{opened: models.SessionOpenedDeviceLogin, platform: "tvos"})
		codes := d.appGuesses(t, app)
		d.requireValid(t, "password session", password)
		d.requireValid(t, "device sign-in session", deviceLogin)
		requireAppGuessCodes(t, codes)
		d.requireLocked(t, "app past its allowance", app, appWindow)
	})

	// Nor do they spend the household's count without locking it: straight
	// after them, before any household success could clear the count, the
	// household still has all five attempts.
	t.Run("app guesses never charge the household", func(t *testing.T) {
		d := newGrantPINDB(t)
		app, _ := d.grantedSession(t, "")
		password := d.store(t, d.userID, reachSession{opened: models.SessionOpenedPassword})
		codes := d.appGuesses(t, app)
		d.requireWrong(t, "password session", password, ratelimit.ProfilePINPolicy.MaxAttempts-1)
		d.requireValid(t, "password session on its fifth attempt", password)
		requireAppGuessCodes(t, codes)
	})

	t.Run("the allowance follows the key, not the session", func(t *testing.T) {
		d := newGrantPINDB(t)
		app, key := d.grantedSession(t, "")
		d.requireWrong(t, "app", app, ratelimit.GrantedAppPINPolicy.MaxAttempts)
		again, _ := d.grantedSession(t, key)
		d.requireLocked(t, "new session of the same key", again, appWindow)
		other, _ := d.grantedSession(t, "")
		d.requireWrong(t, "another app's key", other, 1)
		d.requireValid(t, "another app's key", other)
	})

	// The stricter choice: while the household's own devices have locked the
	// profile, the app is refused too, with the household's retry time.
	t.Run("household lock refuses the app", func(t *testing.T) {
		d := newGrantPINDB(t)
		password := d.store(t, d.userID, reachSession{opened: models.SessionOpenedPassword})
		app, _ := d.grantedSession(t, "")
		d.requireWrong(t, "password session", password, ratelimit.ProfilePINPolicy.MaxAttempts)
		d.requireLocked(t, "app while the household is locked", app, ratelimit.ProfilePINPolicy.Window)
	})

	t.Run("right PIN clears the app's count and binds the token to its session", func(t *testing.T) {
		d := newGrantPINDB(t)
		app, _ := d.grantedSession(t, "")
		allowance := ratelimit.GrantedAppPINPolicy.MaxAttempts
		d.requireWrong(t, "app", app, allowance-1)
		claims := d.requireValid(t, "app", app)
		if claims.SessionID != d.claims[app].SessionID || claims.ProfileID != d.profileID || claims.UserID != d.userID {
			t.Fatalf("token claims = %+v, want bound to the app's own login session and the profile", claims)
		}
		d.requireWrong(t, "app after the clear", app, allowance-1)
		d.requireValid(t, "app after the clear", app)
	})
}
