package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// TestHouseholdSessionsForAnotherAppDB: the household playback list shows
// every device's streams in full, so a session another app's key opened
// gets only the streams its own login session started. Through the real
// session rule and the real list read: such a session sees its own stream,
// not the password session's, and not a stream with no recorded login
// session (a Jellyfin-compatible play); a password session and a device
// sign-in session of the same account still see all three. Regression: the
// list ignored how the caller's session was opened, so an app the person
// allowed read every device's address, client and title.
func TestHouseholdSessionsForAnotherAppDB(t *testing.T) {
	profiles := handlers.NewProfileHandler(fakeHouseholds{})
	d := newKeyRoutesDB(t, func(deps *Dependencies) { deps.Profiles = profiles })
	ctx, pool, h := t.Context(), d.pool, d.handler
	// The real list read, on the pool newKeyRoutesDB opened.
	profiles.SessionsReader = handlers.NewPlaybackSessionsLoader(pool, nil, nil)

	var folder, file int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('movies',$1) RETURNING id`, "reach-"+uuid.NewString()).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanup, `DELETE FROM playback_sessions_sync WHERE user_id = $1`, d.userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, folder)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folder, "/reach/"+uuid.NewString()+".mkv").Scan(&file); err != nil {
		t.Fatal(err)
	}
	stream := func(loginSession *string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO playback_sessions_sync (session_id, user_id, profile_id, media_file_id, play_method, reporting_node, started_at, updated_at, last_sync_at, is_paused)
			VALUES ($1, $2, 'p-owner', $3, 'direct_play', 'node-1', NOW(), NOW(), NOW(), FALSE)`, id, d.userID, file); err != nil {
			t.Fatal(err)
		}
		if loginSession == nil {
			return id
		}
		if _, err := pool.Exec(ctx, `INSERT INTO playback_v3_attempts
			(playback_attempt_id, session_id, user_id, profile_id, requested_media_file_id, effective_media_file_id,
			 current_plan_id, current_plan, normalized_request, expires_at, login_session_id)
			VALUES ($1, $2::uuid, $3, 'p-owner', $4, $4, 'plan-1', '{}', '{}', NOW() + interval '1 hour', $5)`,
			uuid.NewString(), id, d.userID, file, *loginSession); err != nil {
			t.Fatal(err)
		}
		return id
	}
	own := stream(&d.claims["tok-app"].SessionID)
	phone := stream(&d.claims["tok-phone"].SessionID)
	compat := stream(nil)
	all := []string{own, phone, compat}
	slices.Sort(all)

	for _, tc := range []struct {
		token string
		want  []string
	}{
		{token: "tok-app", want: []string{own}},
		{token: "tok-phone", want: all},
		// A device sign-in keeps the whole list: the rule is only for
		// another app.
		{token: "tok-tv", want: all},
	} {
		t.Run(tc.token, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, Prefix+"/profiles/household/sessions", "", with(bearer(tc.token), "X-Profile-Id", "p-owner"))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			var body PlaybackSessionCollection
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(body.Items))
			for _, item := range body.Items {
				got = append(got, string(item.ID))
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("listed %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWebPushRefusesAnotherAppDB: a profile's web push subscriptions belong
// to every browser it registered, and subscribing takes an endpoint's
// registration over, so a session another app's key opened is refused all
// four routes with 403 permission_denied before the service runs. A
// password session and a device sign-in session reach the service as
// before. Regression: the routes ignored how the caller's session was
// opened, so an app the person allowed listed, removed or took over the
// person's browser registrations.
func TestWebPushRefusesAnotherAppDB(t *testing.T) {
	destinations := new(fakeNotificationDestinations)
	d := newKeyRoutesDB(t, func(deps *Dependencies) { deps.NotificationDestinations = destinations })
	subscribe := `{"endpoint":"https://push.example.test/1","keys":{"p256dh":"p","auth":"a"}}`
	routes := []struct {
		method, path, body string
		status             int
	}{
		{http.MethodGet, "/notifications/web-push/subscriptions", "", http.StatusOK},
		{http.MethodPost, "/notifications/web-push/subscriptions", subscribe, http.StatusCreated},
		{http.MethodPost, "/notifications/web-push/unsubscribe", `{"endpoint":"https://push.example.test/1"}`, http.StatusNoContent},
		{http.MethodDelete, "/notifications/web-push/subscriptions/row-one", "", http.StatusNoContent},
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			headers := func(token string) map[string]string {
				return with(bearer(token), "X-Profile-Id", "p-owner")
			}
			// The list counts no call; it records the page size it ran with.
			destinations.limit = 0
			before := destinations.calls
			if p := requireProblem(t, do(t, d.handler, r.method, Prefix+r.path, r.body, headers("tok-app")), TypePermissionDenied); p.Detail != handlers.SessionNotAllowedDetail {
				t.Fatalf("detail = %q", p.Detail)
			}
			if destinations.calls != before || destinations.limit != 0 {
				t.Fatal("the service ran for another app's session")
			}
			for _, token := range []string{"tok-phone", "tok-tv"} {
				if rec := do(t, d.handler, r.method, Prefix+r.path, r.body, headers(token)); rec.Code != r.status {
					t.Fatalf("%s: status = %d, want %d: %s", token, rec.Code, r.status, rec.Body.String())
				}
			}
		})
	}
}

// TestWatchTogetherRefusesAnotherAppDB: a room's host is the account and
// profile, so a session another app's key opened, acting as the host's
// profile, could start, stop, select for and close a room the person's own
// device hosts, and a member can switch the room's source for everyone. Each
// of those routes refuses such a session with 403 permission_denied before
// the service is reached. A password session and a device sign-in session
// pass the rule and reach the service (unwired here, so 503). The socket
// ticket route tells the room socket which caller it is minting for.
// Regression: the routes ignored how the caller's session was opened.
func TestWatchTogetherRefusesAnotherAppDB(t *testing.T) {
	socket := new(fakeRoomSocket)
	d := newKeyRoutesDB(t, func(deps *Dependencies) { deps.WatchTogetherSocket = socket })
	room := "/watch-together/rooms/" + uuid.NewString()
	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/watch-together/rooms", `{"room_id":"` + uuid.NewString() + `","selection_mode":"host_pick"}`},
		{http.MethodDelete, room, ""},
		{http.MethodPost, room + "/playback/start", ""},
		{http.MethodPost, room + "/playback/stop", ""},
		{http.MethodPatch, room + "/policy", `{"guest_control_policy":"guest_play_pause"}`},
		{http.MethodPut, room + "/selection", `{"content_id":"movie-1"}`},
		{http.MethodPatch, room + "/selection-mode", `{"selection_mode":"vote"}`},
		{http.MethodPut, room + "/staged-selection", `{"content_id":"movie-1"}`},
		{http.MethodPost, room + "/suggestions/promote", `{"suggestion_id":"s-1"}`},
		{http.MethodPost, room + "/source-fallback", `{"selection_revision":1,"failed_file_id":"7","reason":"no_alternate_version"}`},
	}
	headers := func(token string) map[string]string {
		return with(with(bearer(token), "X-Profile-Id", "p-owner"), "X-Room-Token", "room-proof")
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if p := requireProblem(t, do(t, d.handler, r.method, Prefix+r.path, r.body, headers("tok-app")), TypePermissionDenied); p.Detail != handlers.SessionNotAllowedDetail {
				t.Fatalf("detail = %q", p.Detail)
			}
			for _, token := range []string{"tok-phone", "tok-tv"} {
				requireProblem(t, do(t, d.handler, r.method, Prefix+r.path, r.body, headers(token)), TypeDependencyUnavailable)
			}
		})
	}
	ticket := Prefix + room + "/ws-ticket"
	// A socket ticket needs an access token with a bounded expiry.
	for _, claims := range d.claims {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
	}
	for token, otherApp := range map[string]bool{"tok-app": true, "tok-phone": false, "tok-tv": false} {
		if rec := do(t, d.handler, http.MethodPost, ticket, "", headers(token)); rec.Code != http.StatusOK || socket.otherApp != otherApp {
			t.Fatalf("%s: ticket status %d, minted as another app %v: %s", token, rec.Code, socket.otherApp, rec.Body.String())
		}
	}
}

// TestNativePushRefusesAnotherAppDB: an Android registration with no ordered
// record yet is bound only to the account and device id, and an Apple reset
// needs no installation key, so a session another app's key opened could
// remove or take over another device's push registration. Each write refuses
// such a session with 403 permission_denied before the service is reached; a
// password session and a device sign-in session pass the rule and reach the
// service (unwired here, so 503). Regression: the routes ignored how the
// caller's session was opened.
func TestNativePushRefusesAnotherAppDB(t *testing.T) {
	d := newKeyRoutesDB(t)
	for _, claims := range d.claims {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
	}
	key := strings.Repeat("k", 43)
	token := strings.Repeat("a", 64)
	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/notifications/push/devices", `{"device_id":"phone-1","platform":"android","token":"` + token + `"}`},
		{http.MethodDelete, "/notifications/push/devices/phone-1", ""},
		{http.MethodPost, "/devices/push/apple", `{"device_id":"phone-1","apns_token":"` + token + `","apns_environment":"production","apns_topic":"org.siloserver.silo"}`},
		{http.MethodPost, "/devices/push/apple/reset", `{"device_id":"phone-1"}`},
	}
	headers := func(token string) map[string]string {
		return with(with(with(bearer(token), "X-Profile-Id", "p-owner"), "X-Push-Installation-Key", key), "X-Push-Generation", "1")
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if p := requireProblem(t, do(t, d.handler, r.method, Prefix+r.path, r.body, headers("tok-app")), TypePermissionDenied); p.Detail != handlers.SessionNotAllowedDetail {
				t.Fatalf("detail = %q", p.Detail)
			}
			for _, token := range []string{"tok-phone", "tok-tv"} {
				requireProblem(t, do(t, d.handler, r.method, Prefix+r.path, r.body, headers(token)), TypeDependencyUnavailable)
			}
		})
	}
}

// suggestionDeletes records whether a deletion was limited to the caller's
// own suggestions.
type suggestionDeletes struct{ ownOnly []bool }

func (*suggestionDeletes) CheckSuggestionRoomProof(string, int, string, string) error { return nil }
func (f *suggestionDeletes) DeleteRoomSuggestion(_ context.Context, _, _ string, _ int, _ string, ownOnly bool) error {
	f.ownOnly = append(f.ownOnly, ownOnly)
	return nil
}

// TestSuggestionDeleteForAnotherAppDB: the host may delete any suggestion,
// and the host is the account and profile, so a session another app opened
// would remove other members' suggestions in the person's own room and change
// which one wins a vote. Through the real session rule, only such a session
// loses the host's power; a password and a device sign-in session keep it.
func TestSuggestionDeleteForAnotherAppDB(t *testing.T) {
	deletes := new(suggestionDeletes)
	d := newKeyRoutesDB(t, func(deps *Dependencies) { deps.WatchTogetherSuggestionDelete = deletes })
	path := Prefix + "/watch-together/rooms/" + uuid.NewString() + "/suggestions/s-1"
	for _, token := range []string{"tok-app", "tok-phone", "tok-tv"} {
		if rec := do(t, d.handler, http.MethodDelete, path, "", with(with(bearer(token), "X-Profile-Id", "p-owner"), "X-Room-Token", "room-proof")); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: status %d: %s", token, rec.Code, rec.Body.String())
		}
	}
	if !slices.Equal(deletes.ownOnly, []bool{true, false, false}) {
		t.Fatalf("own-only per caller = %v, want only another app's", deletes.ownOnly)
	}
}
