package apiv2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// demoKeyRows are the rows seedDemoKeyRows stored for one account: what a
// list must not show a demo visitor.
type demoKeyRows struct {
	active, pending, preapproval string
	// marks are the device names and the enrolled address the rows carry.
	marks []string
}

// seedDemoKeyRows stores, for userID, a person key, an active device key and
// a device key waiting for approval (both with an enrolled address), and a
// device sign-in approved in advance from the session token names. It runs
// with demo mode off, as rows that existed before demo mode was turned on.
func seedDemoKeyRows(t *testing.T, d *keyRoutesDB, userID int, token string) demoKeyRows {
	t.Helper()
	ctx := t.Context()
	tag := uuid.NewString()[:8]
	rows := demoKeyRows{active: uuid.NewString(), pending: uuid.NewString(),
		marks: []string{"Seeded phone " + tag, "Seeded TV " + tag, "Seeded approval " + tag, "198.51.100.77"}}
	random := func() []byte {
		b := make([]byte, 65)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if _, err := d.pool.Exec(ctx, `INSERT INTO auth_person_keys (id, user_id, public_key, key_id, enrolled_via, enrolled_ip_address)
		VALUES ($1::uuid, $2, $3, $4, 'session', '198.51.100.77')`, uuid.NewString(), userID, random(), "person-"+tag); err != nil {
		t.Fatal(err)
	}
	for _, k := range []struct{ id, name, status string }{
		{rows.active, rows.marks[0], models.DeviceKeyStatusActive},
		{rows.pending, rows.marks[1], models.DeviceKeyStatusPending},
	} {
		if _, err := d.pool.Exec(ctx, `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, enrolled_via, status, enrolled_ip_address)
			VALUES ($1::uuid, $2, $3, $4, 'device', $5, 'introduce', $6, '198.51.100.77')`, k.id, userID, random(), "key-"+k.id, k.name, k.status); err != nil {
			t.Fatal(err)
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	binding := sha256.Sum256(secret)
	approved, err := d.devices.Preapprove(auth.WithClaims(ctx, d.claims[token]), auth.DevicePreapprovalInput{
		DeviceName: rows.marks[2], Binding: base64.RawURLEncoding.EncodeToString(binding[:]),
	}, userID)
	if err != nil {
		t.Fatal(err)
	}
	rows.preapproval = approved.ID
	t.Cleanup(func() {
		_, _ = d.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM device_login_requests WHERE id = $1`, approved.ID)
	})
	return rows
}

// demoVisitorOpeners are the ways a demo visitor's session is opened. Each
// stores a session of userID and answers a bearer token for it. A session
// with no record of how it was opened is the least trusted kind outside demo
// mode (refused the key reads); a demo visitor's answer must not depend on it.
var demoVisitorOpeners = []struct {
	name string
	open func(t *testing.T, d *keyRoutesDB, userID int) string
}{
	{"password", openedAs(models.SessionOpenedPassword)},
	{"no recorded origin", openedAs("")},
	{"device sign-in", openedAs(models.SessionOpenedDeviceLogin)},
}

// openedAs opens a session of userID recorded as opened via openedVia.
func openedAs(openedVia string) func(t *testing.T, d *keyRoutesDB, userID int) string {
	return func(t *testing.T, d *keyRoutesDB, userID int) string {
		t.Helper()
		token := "tok-visitor-" + uuid.NewString()
		d.storeSession(t, token, userID, "user", openedVia, nil, nil)
		return token
	}
}

// sessionAccess reads getAppKeySessionAccess for token.
func sessionAccess(t *testing.T, h http.Handler, token string) AppKeySessionAccess {
	t.Helper()
	rec := do(t, h, http.MethodGet, Prefix+"/auth/keys/session", "", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("session access: status %d: %s", rec.Code, rec.Body.String())
	}
	var got AppKeySessionAccess
	decodeJSON(t, rec.Body, &got)
	return got
}

// demoKeyLists are the caller's own lists of the account's devices, keys and
// approvals.
var demoKeyLists = []string{"/auth/keys", "/auth/keys/pending", "/auth/device/preapprovals"}

// listCount reads a key list and counts its rows: the person key and the
// devices, or the items.
func listCount(t *testing.T, h http.Handler, path, token string) (int, string) {
	t.Helper()
	rec := do(t, h, http.MethodGet, Prefix+path, "", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body.String())
	}
	var body struct {
		PersonKey *json.RawMessage  `json:"person_key"`
		Devices   []json.RawMessage `json:"devices"`
		Items     []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	n := len(body.Devices) + len(body.Items)
	if body.PersonKey != nil {
		n++
	}
	return n, rec.Body.String()
}

// TestDemoVisitorKeyListsDB: on a demo server every visitor shares one
// account that is not an administrator, so the account's devices, waiting
// devices and sign-ins approved in advance are other visitors'. Keys and
// approvals stored before demo mode was turned on are listed empty to a
// visitor, however the visitor's session was opened, and
// getAppKeySessionAccess answers every such session alike. Regression: each
// visitor read every device's name, platform, times and approvals (only the
// enrolled address was hidden), and a session with no record of how it was
// opened was refused the lists and told to sign in again while a password
// session on the same account was not. The subtests pin that an administrator on
// the demo server and an ordinary account with demo mode off still get every
// row, that a setting that can't be read refuses rather than serve the rows,
// that another app's session and an administrator acting as the account are
// refused before the setting is read, and
// that a visitor's actions on those rows answer the same for a real row and
// an unknown id.
func TestDemoVisitorKeyListsDB(t *testing.T) {
	demo := &demoSwitch{}
	d := newKeyRoutesDB(t, func(deps *Dependencies) { deps.DemoSettings = demo })
	d.storeSession(t, "tok-admin", d.adminID, "admin", models.SessionOpenedPassword, nil, nil)
	visitorRows := seedDemoKeyRows(t, d, d.userID, "tok-phone")
	adminRows := seedDemoKeyRows(t, d, d.adminID, "tok-admin")
	demo.on = true
	t.Cleanup(func() { demo.on, demo.err = false, nil })

	for _, opener := range demoVisitorOpeners {
		t.Run("demo visitor by "+opener.name, func(t *testing.T) {
			token := opener.open(t, d, d.userID)
			for _, path := range demoKeyLists {
				n, body := listCount(t, d.handler, path, token)
				if n != 0 {
					t.Errorf("%s lists %d rows to a demo visitor: %s", path, n, body)
					continue
				}
				for _, mark := range visitorRows.marks {
					if strings.Contains(body, mark) {
						t.Errorf("%s shows %q to a demo visitor: %s", path, mark, body)
					}
				}
			}
			// The lists answer, so the page lists them; nothing changes a key,
			// and signing in again would not either.
			if got := sessionAccess(t, d.handler, token); got != (AppKeySessionAccess{List: true}) {
				t.Errorf("session access = %+v, want only list", got)
			}
		})
	}

	full := func(t *testing.T, token string, rows demoKeyRows) {
		t.Helper()
		// The person key and both device keys; the waiting key; the approval.
		want := map[string]int{"/auth/keys": 3, "/auth/keys/pending": 1, "/auth/device/preapprovals": 1}
		for _, path := range demoKeyLists {
			if n, body := listCount(t, d.handler, path, token); n < want[path] || !strings.Contains(body, "198.51.100.77") && path != "/auth/device/preapprovals" {
				t.Fatalf("%s: %d rows, want at least %d with the address: %s", path, n, want[path], body)
			}
		}
		if _, body := listCount(t, d.handler, "/auth/device/preapprovals", token); !strings.Contains(body, rows.marks[2]) {
			t.Fatalf("approval missing: %s", body)
		}
	}
	t.Run("administrator on the demo server", func(t *testing.T) { full(t, "tok-admin", adminRows) })
	t.Run("demo off", func(t *testing.T) {
		demo.on = false
		defer func() { demo.on = true }()
		full(t, "tok-phone", visitorRows)
		if got := sessionAccess(t, d.handler, "tok-phone"); !got.List || !got.Manage {
			t.Errorf("password session access = %+v, want list and manage", got)
		}
		// Outside demo mode a session with no record of how it was opened is
		// still refused.
		for _, path := range demoKeyLists {
			requireProblem(t, do(t, d.handler, http.MethodGet, Prefix+path, "", bearer("tok-old")), TypePermissionDenied)
		}
		if got := sessionAccess(t, d.handler, "tok-old"); got.List || got.Manage {
			t.Errorf("unrecorded session access = %+v, want neither list nor manage", got)
		}
	})

	t.Run("demo mode unreadable", func(t *testing.T) {
		demo.err = errors.New("settings store unreachable")
		defer func() { demo.err = nil }()
		requireProblem(t, do(t, d.handler, http.MethodGet, Prefix+"/auth/keys/session", "", bearer("tok-phone")), TypeInternalError)
		for _, path := range demoKeyLists {
			t.Run(path, func(t *testing.T) {
				rec := do(t, d.handler, http.MethodGet, Prefix+path, "", bearer("tok-phone"))
				requireProblem(t, rec, TypeInternalError)
				for _, mark := range visitorRows.marks {
					if strings.Contains(rec.Body.String(), mark) {
						t.Fatalf("the refusal shows %q", mark)
					}
				}
				// Another app's session and an administrator acting as the
				// account are refused as always, without reading the setting.
				for _, token := range []string{"tok-app", "tok-acting"} {
					requireProblem(t, do(t, d.handler, http.MethodGet, Prefix+path, "", bearer(token)), TypePermissionDenied)
				}
			})
		}
	})

	// A visitor learns nothing of a row it can't list from acting on it: a
	// row of the account and an unknown id get the same answer, and the row
	// stays.
	t.Run("actions on rows a visitor can't list", func(t *testing.T) {
		unknown := uuid.NewString()
		for _, tc := range []struct {
			name, method, format, body string
			status                     int
			problem                    ProblemType
			known                      string
		}{
			{"revoke device key", http.MethodDelete, "/auth/keys/devices/%s", "", http.StatusForbidden, TypePermissionDenied, visitorRows.active},
			{"deny waiting device", http.MethodPost, "/auth/keys/pending/%s/deny", "{}", http.StatusForbidden, TypePermissionDenied, visitorRows.pending},
			{"approve waiting device", http.MethodPost, "/auth/keys/pending/%s/approve", "{}", http.StatusConflict, TypeCapabilityDisabled, visitorRows.pending},
			{"withdraw approval", http.MethodDelete, "/auth/device/preapprovals/%s", "", http.StatusForbidden, TypePermissionDenied, visitorRows.preapproval},
		} {
			var answers []string
			for _, id := range []string{tc.known, unknown} {
				rec := do(t, d.handler, tc.method, Prefix+strings.Replace(tc.format, "%s", id, 1), tc.body, bearer("tok-phone"))
				p := requireProblem(t, rec, tc.problem)
				if rec.Code != tc.status {
					t.Fatalf("%s %s: status %d", tc.name, id, rec.Code)
				}
				answers = append(answers, p.Detail)
			}
			if answers[0] != answers[1] {
				t.Fatalf("%s: a real row answers %q, an unknown id %q", tc.name, answers[0], answers[1])
			}
		}
		var status string
		if err := d.pool.QueryRow(t.Context(), `SELECT status FROM auth_device_keys WHERE id = $1`, visitorRows.pending).Scan(&status); err != nil || status != models.DeviceKeyStatusPending {
			t.Fatalf("waiting key = %q (%v), want still pending", status, err)
		}
		if err := d.pool.QueryRow(t.Context(), `SELECT status FROM device_login_requests WHERE id = $1`, visitorRows.preapproval).Scan(&status); err != nil || status != auth.DeviceLoginStatusApproved {
			t.Fatalf("approval = %q (%v), want still approved", status, err)
		}
	})
}
