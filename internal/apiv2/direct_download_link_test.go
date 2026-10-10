package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/models"
)

const linkTestSecret = "synthetic-direct-download-link-key"

// linkFiles serves file 42 to everyone and hides file 43 (a mature title)
// from the TV-Y7 child profile, the way the catalog access filter would.
type linkFiles struct {
	handlers.DownloadService
	err      error
	served   int
	resolved int
	access   catalogpkg.AccessFilter
}

func (f *linkFiles) authorize(file int, a catalogpkg.AccessFilter) error {
	f.access = a
	if f.err != nil {
		return f.err
	}
	if file != 42 && file != 43 || file == 43 && a.ProfileID == "p-kid" {
		return catalogpkg.ErrItemNotFound
	}
	return nil
}

func (f *linkFiles) ServeDirect(_ context.Context, w http.ResponseWriter, r *http.Request, _ int, file int, _ string, a catalogpkg.AccessFilter) error {
	if err := f.authorize(file, a); err != nil {
		return err
	}
	f.served++
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "fixture.mp4", time.Unix(1700000000, 0), strings.NewReader("0123456789"))
	return nil
}

func (f *linkFiles) ResolveDirectFile(_ context.Context, _ int, file int, _ string, a catalogpkg.AccessFilter) (*downloads.FileTarget, error) {
	f.resolved++
	if err := f.authorize(file, a); err != nil {
		return nil, err
	}
	return &downloads.FileTarget{Path: "/fixture/original.mp4", MediaFileID: file}, nil
}

func (*linkFiles) ResolveManagedFile(context.Context, int, string, string, string, catalogpkg.AccessFilter) (*downloads.FileTarget, error) {
	panic("managed download must not run")
}

// linkTokens validates the fixed test credentials, and anything else as a
// real JWT under the link test key, so links are minted and checked by the
// production signer.
type linkTokens struct {
	fake fakeTokens
	jwt  *auth.JWTService
}

func (l linkTokens) ValidateToken(tok string) (*auth.Claims, error) {
	if c, ok := l.fake.claims[tok]; ok {
		return c, nil
	}
	return l.jwt.ValidateToken(tok)
}

func directLinkHandler(t *testing.T, files *linkFiles) (http.Handler, *auth.JWTService) {
	t.Helper()
	deps, _ := catalogDeps(t)
	signer := auth.NewJWTService(linkTestSecret, time.Hour, time.Hour)
	users := fakeUsers{map[int]*models.User{
		1:               {ID: 1, Role: "user", Enabled: true},
		householdUserID: {ID: householdUserID, Role: "user", Enabled: true},
	}}
	tokens := linkTokens{fake: fakeTokens{map[string]*auth.Claims{
		memberToken:    {UserID: 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess},
		householdToken: {UserID: householdUserID, Role: "user", SessionID: "s5", TokenType: auth.TokenTypeAccess},
	}}, jwt: signer}
	keys := fakeAPIKeys{map[string]*models.APIKey{householdAPIKeyToken: {ID: 11, UserID: householdUserID}}}
	deps.Auth = apimw.NewAuthMiddleware(tokens, fakeSessions{map[string]string{"s1": "user", "s5": "user"}}, keys, users)
	h := handlers.NewDownloadHandler(files)
	h.SetDirectDownloadLinks(signer)
	deps.DirectDownloads = &DirectDownloadHandlers{Original: http.HandlerFunc(h.HandleDirectDownload), Proxy: http.HandlerFunc(h.HandleDirectDownloadViaProxy)}
	deps.DirectDownloadLinks = h
	return newTestHandler(t, deps), signer
}

var (
	kidHeaders    = with(bearer(householdToken), "X-Profile-Id", "p-kid")
	parentHeaders = with(with(bearer(householdToken), "X-Profile-Id", "p-primary-locked"), "X-Profile-Token", "pin-proof")
)

func mintLink(t *testing.T, h http.Handler, fileID string, headers map[string]string) DirectDownloadLink {
	t.Helper()
	rec := do(t, h, http.MethodPost, Prefix+"/direct-download/links", `{"file_id":"`+fileID+`"}`, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("mint cache-control = %q", cc)
	}
	var link DirectDownloadLink
	if err := json.Unmarshal(rec.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	return link
}

// TestCreateDirectDownloadLink: minting needs a verified profile, applies
// that profile's catalog access and the download policy, and returns links
// for both direct-download routes that carry only file_id and dl.
func TestCreateDirectDownloadLink(t *testing.T) {
	files := new(linkFiles)
	h, signer := directLinkHandler(t, files)
	path := Prefix + "/direct-download/links"

	link := mintLink(t, h, "42", kidHeaders)
	for route, raw := range map[string]string{directDownloadPath: link.URL, directDownloadProxyPath: link.ProxyURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Path != Prefix+route || len(u.Query()) != 2 || u.Query().Get("file_id") != "42" {
			t.Fatalf("link %q", raw)
		}
		claims, err := signer.ValidateToken(u.Query().Get(apimw.DirectDownloadLinkParam))
		if err != nil || claims.TokenType != auth.TokenTypeDirectDownloadLink || claims.UserID != householdUserID ||
			claims.SessionID != "s5" || claims.ProfileID != "p-kid" || claims.FileID != 42 {
			t.Fatalf("claims %+v, %v", claims, err)
		}
	}
	if until := time.Until(link.ExpiresAt.Time); until <= 0 || until > auth.DirectDownloadLinkTTL {
		t.Fatalf("expires_at %v", link.ExpiresAt)
	}
	if files.access.ProfileID != "p-kid" || files.access.UserID != householdUserID || files.served != 0 {
		t.Fatalf("mint authorized as %+v, served %d", files.access, files.served)
	}

	// A locked profile mints only with its PIN proof.
	requireProblem(t, do(t, h, http.MethodPost, path, `{"file_id":"43"}`, with(bearer(householdToken), "X-Profile-Id", "p-primary-locked")), TypeProfileVerificationRequired)
	mintLink(t, h, "43", parentHeaders)

	before := files.resolved
	requireProfileHeaderProblem(t, requireProblem(t, do(t, h, http.MethodPost, path, `{"file_id":"42"}`, bearer(memberToken)), TypeValidationFailed))
	requireProblem(t, do(t, h, http.MethodPost, path, `{"file_id":"042"}`, kidHeaders), TypeValidationFailed)
	// A session-less API key cannot mint a session-bound link.
	requireProblem(t, do(t, h, http.MethodPost, path, `{"file_id":"42"}`, with(bearer(householdAPIKeyToken), "X-Profile-Id", "p-kid")), TypePermissionDenied)
	if files.resolved != before {
		t.Fatal("refused request reached the download service")
	}

	// Hidden from the child and unknown look the same.
	requireProblem(t, do(t, h, http.MethodPost, path, `{"file_id":"43"}`, kidHeaders), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"file_id":"99"}`, kidHeaders), TypeNotFound)
	for _, err := range []error{downloads.ErrDownloadNotAllowed, downloads.ErrFeatureDisabled} {
		files.err = err
		requireProblem(t, do(t, h, http.MethodPost, path, `{"file_id":"42"}`, kidHeaders), TypePermissionDenied)
	}
	files.err = nil

	deps, _ := catalogDeps(t)
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path, `{"file_id":"42"}`, viewerHeaders()), TypeDependencyUnavailable)
}

// TestDirectDownloadLinkDelivery: a link authorizes its file as the profile
// that minted it, on both routes, with the existing byte-range contract and
// without any header. Expired, tampered, revoked and other-file links fail.
func TestDirectDownloadLinkDelivery(t *testing.T) {
	files := new(linkFiles)
	h, signer := directLinkHandler(t, files)
	sign := func(session, profile string, file int) string {
		tok, _, err := signer.GenerateDirectDownloadLinkToken(householdUserID, "user", session, profile, nil, file)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	link := mintLink(t, h, "42", kidHeaders)
	for _, raw := range []string{link.URL, link.ProxyURL} {
		res := do(t, h, http.MethodGet, raw, "", nil)
		if res.Code != http.StatusOK || res.Body.String() != "0123456789" {
			t.Fatal(raw, res.Code, res.Body)
		}
		if files.access.UserID != householdUserID || files.access.ProfileID != "p-kid" {
			t.Fatalf("served as %+v", files.access)
		}
		res = do(t, h, http.MethodGet, raw, "", map[string]string{"Range": "bytes=2-4"})
		if res.Code != http.StatusPartialContent || res.Body.String() != "234" {
			t.Fatal(res.Code, res.Body)
		}
		if res = do(t, h, http.MethodHead, raw, "", nil); res.Code != http.StatusOK || res.Body.Len() != 0 {
			t.Fatal(res.Code, res.Body)
		}
		// The link names its profile; headers cannot widen it.
		res = do(t, h, http.MethodGet, raw, "", parentHeaders)
		if res.Code != http.StatusOK || files.access.ProfileID != "p-kid" {
			t.Fatal(res.Code, files.access)
		}
	}

	served := files.served
	base := Prefix + directDownloadPath + "?file_id="
	// A link for another file.
	requireProblem(t, do(t, h, http.MethodGet, base+"43&dl="+url.QueryEscape(sign("s5", "p-kid", 42)), "", nil), TypePermissionDenied)
	// The child's limits apply to its link even for a file it never minted.
	requireProblem(t, do(t, h, http.MethodGet, base+"43&dl="+url.QueryEscape(sign("s5", "p-kid", 43)), "", nil), TypeNotFound)
	// A revoked login session revokes its links.
	requireProblem(t, do(t, h, http.MethodGet, base+"42&dl="+url.QueryEscape(sign("s-gone", "p-kid", 42)), "", nil), TypeSessionExpired)
	// A session store that cannot answer is a retryable outage, not a
	// revoked session, on both routes and for HEAD as well as GET.
	storeDown := "?file_id=42&dl=" + url.QueryEscape(sign(storeDownSession, "p-kid", 42))
	for _, route := range []string{directDownloadPath, directDownloadProxyPath} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := do(t, h, method, Prefix+route+storeDown, "", nil)
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
				t.Fatalf("%s %s with the session store down: %d, Retry-After %q", method, route, rec.Code, rec.Header().Get("Retry-After"))
			}
			if method == http.MethodGet {
				requireProblem(t, rec, TypeDependencyUnavailable)
			}
		}
	}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{
		UserID: householdUserID, Role: "user", SessionID: "s5", ProfileID: "p-kid", FileID: 42, TokenType: auth.TokenTypeDirectDownloadLink,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Second))},
	}).SignedString([]byte(linkTestSecret))
	if err != nil {
		t.Fatal(err)
	}
	access, err := signer.GenerateAccessToken(householdUserID, "user", "s5")
	if err != nil {
		t.Fatal(err)
	}
	valid := sign("s5", "p-kid", 42)
	for name, dl := range map[string]string{
		"expired":      expired,
		"tampered":     valid[:len(valid)-2] + "xx",
		"access token": access,
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			requireProblem(t, do(t, h, http.MethodGet, base+"42&dl="+url.QueryEscape(dl), "", nil), TypeInvalidToken)
		})
	}
	// A link is not an account credential anywhere else.
	requireProblem(t, do(t, h, http.MethodGet, base+"42&token="+url.QueryEscape(valid), "", nil), TypeInvalidToken)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/capabilities/downloads", "", with(bearer(valid), "X-Profile-Id", "p-kid")), TypeInvalidToken)
	requireProblem(t, do(t, h, http.MethodGet, base+"42&token="+memberToken+"&dl="+url.QueryEscape(valid), "", nil), TypeValidationFailed)
	if files.served != served {
		t.Fatal("refused link reached the file")
	}
}

// TestDirectDownloadHouseholdGate: without a link or X-Profile-Id, an account
// whose household has a locked or restricted profile is refused; header
// callers, API keys and unrestricted households keep today's behavior.
func TestDirectDownloadHouseholdGate(t *testing.T) {
	files := new(linkFiles)
	h, _ := directLinkHandler(t, files)
	for _, route := range []string{directDownloadPath, directDownloadProxyPath} {
		t.Run(route, func(t *testing.T) {
			path := Prefix + route + "?file_id=43"
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				before := files.served
				rec := do(t, h, method, path, "", bearer(householdToken))
				if method == http.MethodGet {
					requireProfileHeaderProblem(t, requireProblem(t, rec, TypeValidationFailed))
				} else if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("HEAD: %d", rec.Code)
				}
				if rec := do(t, h, method, path+"&token="+householdToken, "", nil); rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("%s with account query token: %d %s", method, rec.Code, rec.Body)
				}
				if files.served != before {
					t.Fatal("refused request reached the file")
				}
			}
			requireProblem(t, do(t, h, http.MethodGet, path, "", kidHeaders), TypeNotFound)
			for name, headers := range map[string]map[string]string{
				"parent with PIN proof":   parentHeaders,
				"unrestricted household":  bearer(memberToken),
				"household API key":       bearer(householdAPIKeyToken),
				"child, all-ages file":    kidHeaders,
				"unrestricted with query": nil,
			} {
				p := path
				if name == "child, all-ages file" {
					p = Prefix + route + "?file_id=42"
				}
				if headers == nil {
					p += "&token=" + memberToken
				}
				if rec := do(t, h, http.MethodGet, p, "", headers); rec.Code != http.StatusOK || rec.Body.String() != "0123456789" {
					t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
				}
			}
		})
	}
}
