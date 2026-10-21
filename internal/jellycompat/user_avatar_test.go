package jellycompat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// seerrAvatarID is the shape Seerr's /avatarproxy/:jellyfinUserId accepts.
var seerrAvatarID = regexp.MustCompile(`^[a-f0-9]{32}$`)

const testAvatarRef = userstore.AvatarUploadPrefix + "profile-avatars/7/profile-1/original.webp"

var testAvatarBytes = []byte("RIFF\x10\x00\x00\x00WEBPVP8 avatar")

type userAvatarFixture struct {
	store    userstore.UserStore
	sessions *SessionStore
	session  Session
	avatars  *compatUserAvatars
	router   chi.Router
}

func newUserAvatarFixture(t *testing.T, avatarRef string) userAvatarFixture {
	t.Helper()
	store := newJellycompatUserStore(t)
	if avatarRef != "" {
		if err := store.UpdateProfile(t.Context(), "profile-1", userstore.UpdateProfileInput{Avatar: &avatarRef}); err != nil {
			t.Fatalf("UpdateProfile: %v", err)
		}
	}
	blobs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	if err := blobs.Put(t.Context(), "profile-avatars/7/profile-1/w256.webp", testAvatarBytes); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sessions := NewSessionStore(time.Hour, nil)
	session := Session{
		Token:           "token-1",
		Username:        "admin",
		ProfileID:       "profile-1",
		PseudoUserID:    PseudoUserID(7, "profile-1"),
		StreamAppUserID: 7,
		LoginSessionID:  "login-1",
	}
	if err := sessions.Put(session); err != nil {
		t.Fatalf("Put session: %v", err)
	}
	avatars := newCompatUserAvatars(sessions, compatTestUserStoreProvider{store: store}, blobs)
	images := &ImagesHandler{userAvatars: avatars}
	r := chi.NewRouter()
	r.Get(compatUserImageRoute, images.HandleUserImage)
	r.Method(http.MethodHead, compatUserImageRoute, http.HandlerFunc(images.HandleUserImage))
	r.Get(compatUserImageQueryRoute, images.HandleUserImage)
	r.Method(http.MethodHead, compatUserImageQueryRoute, http.HandlerFunc(images.HandleUserImage))
	return userAvatarFixture{store: store, sessions: sessions, session: session, avatars: avatars, router: r}
}

func (f userAvatarFixture) serve(method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func (f userAvatarFixture) authHandler() *AuthHandler {
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: f.store})
	h.avatars = f.avatars
	return h
}

func TestCompatUserIDUsesJellyfinGuidForm(t *testing.T) {
	id := PseudoUserID(7, "profile-1")
	got := compatUserID(id)
	if !seerrAvatarID.MatchString(got) {
		t.Fatalf("compatUserID = %q, want 32 lowercase hex digits", got)
	}
	if got != strings.ReplaceAll(id.String(), "-", "") {
		t.Fatalf("compatUserID = %q, want the dashless form of %s", got, id)
	}
	for _, raw := range []string{got, id.String(), strings.ToUpper(got), " " + got + " "} {
		if !isPseudoUser(raw, id) {
			t.Errorf("isPseudoUser(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"", "Me", compatUserID(PseudoUserID(7, "profile-2"))} {
		if isPseudoUser(raw, id) {
			t.Errorf("isPseudoUser(%q) = true, want false", raw)
		}
	}

	// Routes and queries carrying either form reach the signed-in user.
	session := &Session{PseudoUserID: id}
	for _, raw := range []string{got, id.String()} {
		if !validatePseudoUser(httptest.NewRecorder(), raw, session) {
			t.Errorf("validatePseudoUser(%q) = false, want true", raw)
		}
	}
}

func TestUserDTOReportsAvatarTagAndDashlessID(t *testing.T) {
	f := newUserAvatarFixture(t, testAvatarRef)
	h := f.authHandler()

	dto, err := h.resolvedUserDTO(t.Context(), &f.session)
	if err != nil {
		t.Fatalf("resolvedUserDTO: %v", err)
	}
	if dto.ID != compatUserID(f.session.PseudoUserID) {
		t.Fatalf("Id = %q, want %q", dto.ID, compatUserID(f.session.PseudoUserID))
	}
	if dto.PrimaryImageTag == "" {
		t.Fatal("PrimaryImageTag is empty for a profile with an uploaded avatar")
	}

	// An sa_ API key session is never stored, so its avatar could not be served.
	apiKeySession := f.session
	apiKeySession.LoginSessionID = ""
	if keyed, err := h.resolvedUserDTO(t.Context(), &apiKeySession); err != nil || keyed.PrimaryImageTag != "" {
		t.Fatalf("API key session PrimaryImageTag = %q, %v; want none", keyed.PrimaryImageTag, err)
	}

	if info := h.sessionInfo(&f.session); info.UserID != dto.ID {
		t.Fatalf("SessionInfo.UserId = %q, want %q", info.UserID, dto.ID)
	}
}

// A re-upload keeps the blob key, so the tag has to follow the profile row for
// clients to drop the cached image.
func TestUserAvatarTagFollowsProfileRow(t *testing.T) {
	avatars := &compatUserAvatars{}
	before := avatars.primaryImageTag(&userstore.Profile{Avatar: testAvatarRef, UpdatedAt: "2026-10-18T10:00:00Z"})
	after := avatars.primaryImageTag(&userstore.Profile{Avatar: testAvatarRef, UpdatedAt: "2026-10-18T10:05:00Z"})
	if before == "" || after == "" || before == after {
		t.Fatalf("tags before/after re-upload = %q/%q, want two different non-empty tags", before, after)
	}
	var disabled *compatUserAvatars
	if tag := disabled.primaryImageTag(&userstore.Profile{Avatar: testAvatarRef}); tag != "" {
		t.Fatalf("tag without an avatar store = %q, want empty", tag)
	}
}

func TestUserDTOOmitsTagWithoutUploadedAvatar(t *testing.T) {
	for name, ref := range map[string]string{
		"none":   "",
		"preset": "preset:avatar-3",
	} {
		t.Run(name, func(t *testing.T) {
			f := newUserAvatarFixture(t, ref)
			dto, err := f.authHandler().resolvedUserDTO(t.Context(), &f.session)
			if err != nil {
				t.Fatalf("resolvedUserDTO: %v", err)
			}
			body, _ := json.Marshal(dto)
			if strings.Contains(string(body), "PrimaryImageTag") {
				t.Fatalf("user JSON carries a PrimaryImageTag without an uploaded avatar: %s", body)
			}
			if rec := f.serve(http.MethodGet, "/UserImage?UserId="+compatUserID(f.session.PseudoUserID)); rec.Code != http.StatusNotFound {
				t.Fatalf("image status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestHandleUserImageServesProfileAvatar(t *testing.T) {
	f := newUserAvatarFixture(t, testAvatarRef)
	dashless := compatUserID(f.session.PseudoUserID)
	dashed := f.session.PseudoUserID.String()

	for _, target := range []string{
		"/UserImage?UserId=" + dashless, // Seerr's avatar proxy
		"/UserImage?userId=" + dashed + "&tag=abc",
		"/Users/" + dashless + "/Images/Primary",
		"/Users/" + dashed + "/Images/Primary?quality=90",
	} {
		rec := f.serve(http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200: %s", target, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/webp" {
			t.Errorf("GET %s Content-Type = %q, want image/webp", target, ct)
		}
		if rec.Header().Get("Last-Modified") == "" {
			t.Errorf("GET %s has no Last-Modified; Seerr versions avatars by it", target)
		}
		if !bytes.Equal(rec.Body.Bytes(), testAvatarBytes) {
			t.Errorf("GET %s served %q, want the stored avatar", target, rec.Body.Bytes())
		}
	}

	head := f.serve(http.MethodHead, "/UserImage?UserId="+dashless)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD status = %d body = %d bytes, want 200 with no body", head.Code, head.Body.Len())
	}
}

func TestHandleUserImageNotFound(t *testing.T) {
	f := newUserAvatarFixture(t, testAvatarRef)
	for _, target := range []string{
		"/UserImage",
		"/UserImage?UserId=not-a-guid",
		"/UserImage?UserId=" + compatUserID(PseudoUserID(8, "profile-1")),
		"/Users/" + compatUserID(PseudoUserID(7, "other")) + "/Images/Primary",
	} {
		if rec := f.serve(http.MethodGet, target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", target, rec.Code)
		}
	}

	// Signing out the login session behind the only compat session removes
	// the route from the id to the profile, though the compat row remains.
	logins := &loginSessionsFake{live: map[string]bool{f.session.LoginSessionID: true}}
	f.sessions.WithLoginSessions(logins)
	target := "/UserImage?UserId=" + compatUserID(f.session.PseudoUserID)
	if rec := f.serve(http.MethodGet, target); rec.Code != http.StatusOK {
		t.Fatalf("status with a live login session = %d, want 200", rec.Code)
	}
	logins.live[f.session.LoginSessionID] = false
	if rec := f.serve(http.MethodGet, target); rec.Code != http.StatusNotFound {
		t.Fatalf("status after sign-out = %d, want 404", rec.Code)
	}

	// No avatar store configured: no user has an avatar.
	rec := httptest.NewRecorder()
	(&ImagesHandler{}).HandleUserImage(rec, httptest.NewRequest(http.MethodGet, "/UserImage?UserId="+compatUserID(f.session.PseudoUserID), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status without avatar store = %d, want 404", rec.Code)
	}
}

func TestImageProxyRewriteLeavesUserObjectsAlone(t *testing.T) {
	user := `{"Id":"0123456789abcdef0123456789abcdef","PrimaryImageTag":"abcdef0123456789"}`
	for path, wantRewrite := range map[string]bool{
		"/Users/Me": false,
		"/Users":    false,
		"/Users/0123456789abcdef0123456789abcdef":       false,
		"/Users/0123456789abcdef0123456789abcdef/Items": true,
	} {
		handler := compatImageProxyTagVariantMiddleware(NewResourceIDCodec())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(user))
		}))
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("User-Agent", "Infuse-Direct/8.0")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		rewritten := strings.Contains(rec.Body.String(), compatImageProxyTagSuffix)
		if rewritten != wantRewrite {
			t.Errorf("GET %s rewritten = %v, want %v: %s", path, rewritten, wantRewrite, rec.Body.String())
		}
	}
}
