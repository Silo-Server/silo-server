package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerproxy"
)

type outputRelayNodes struct{ node workercontrolcore.Node }

func (n outputRelayNodes) ByID(_ context.Context, id int) (workercontrolcore.Node, bool, error) {
	return n.node, id == n.node.ID, nil
}
func (n outputRelayNodes) ByURL(_ context.Context, origin string) (workercontrolcore.Node, bool, error) {
	return n.node, origin == n.node.URL, nil
}

//nolint:funlen,gocyclo // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestWorkerNativeRelayCannotBorrowCoreAuthorityAfterOriginLogout(t *testing.T) {
	for _, headerAuth := range []bool{false, true} {
		t.Run(fmt.Sprintf("header_auth_%v", headerAuth), func(t *testing.T) {
			const secret = "internal-relay-fixture"
			public, private, _ := ed25519.GenerateKey(rand.Reader)
			var mu sync.Mutex
			active := map[string]bool{"login-A": true, "login-B": true}
			viewers := map[string]string{}
			grantTokens := map[string]map[string]bool{}
			commands := 0
			var origin string
			coreVerifier := &workeridentity.Verifier{Domain: workeridentity.DomainCoreCommand, AllowHTTP: true, ResolveKey: func(int) (ed25519.PublicKey, error) { return public, nil }}
			proxyVerifier := &workeridentity.Verifier{Domain: workeridentity.DomainProxyOutputRequest, AllowHTTP: true}
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Header.Get(workeridentity.HeaderDomain) {
				case workeridentity.DomainCoreCommand:
					if err := coreVerifier.Verify(r, 17); err != nil {
						http.Error(w, "bad core proof", http.StatusUnauthorized)
						return
					}
					mu.Lock()
					commands++
					mu.Unlock()
				case workeridentity.DomainProxyOutputRequest:
					grant, err := workeridentity.DecodeGrant(r.Header.Get("X-Silo-Stream-Token"), workeridentity.DomainProxyOutputGrant, origin, public, time.Now())
					if err != nil {
						http.Error(w, "bad grant", http.StatusUnauthorized)
						return
					}
					var body workeridentity.OutputAuthority
					if json.Unmarshal(grant.Body, &body) != nil || proxyVerifier.VerifyKey(r, 17, body.ProxyPublicKey) != nil {
						http.Error(w, "bad viewer proof", http.StatusUnauthorized)
						return
					}
					mu.Lock()
					login, known := viewers[body.AuthorityID]
					allowed := known && active[login]
					mu.Unlock()
					if !allowed {
						http.Error(w, "viewer revoked", http.StatusUnauthorized)
						return
					}
				default:
					http.Error(w, "missing authority", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nstream.m3u8\n"))
			}))
			t.Cleanup(worker.Close)
			origin = worker.URL
			coreVerifier.Origin, proxyVerifier.Origin = origin, origin
			nodes := outputRelayNodes{workercontrolcore.Node{ID: 17, URL: origin, PublicKey: public, Enabled: true, AllowHTTP: true}}
			core, err := workercontrolcore.New(workercontrolcore.Options{Origin: "https://core.example.test", PrivateKey: private, Nodes: nodes, Store: workercontrolcore.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			outputs, err := workerproxy.New("https://core.example.test", secret, nodes)
			if err != nil {
				t.Fatal(err)
			}
			outputs.Grant = func(_ context.Context, token, nodeURL string) (workercontrolcore.GrantReply, error) {
				claims, err := streamtoken.Verify(token, secret)
				if err != nil || !claims.Bound() || claims.TranscodeNode != nodeURL {
					return workercontrolcore.GrantReply{}, workercontrolcore.ErrDenied
				}
				mu.Lock()
				defer mu.Unlock()
				if !active[claims.OriginLoginSessionID] {
					return workercontrolcore.GrantReply{}, workercontrolcore.ErrOutputRevoked
				}
				if grantTokens[claims.SessionID] == nil {
					grantTokens[claims.SessionID] = map[string]bool{}
				}
				grantTokens[claims.SessionID][token] = true
				digest := sha256.Sum256([]byte(token))
				id := hex.EncodeToString(digest[:])
				viewers[id] = claims.OriginLoginSessionID
				grant := workeridentity.Grant{Version: 1, Domain: workeridentity.DomainProxyOutputGrant, Audience: origin, NodeID: 17, WorkID: claims.TranscodeTransportID, Generation: 1, ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
				key, err := workeridentity.OutputPrivateKey(secret, grant)
				if err != nil {
					return workercontrolcore.GrantReply{}, err
				}
				copied := *claims
				copied.OriginCredential = streamtoken.OriginCredential{}
				inner, _ := json.Marshal(copied)
				grant.Body, _ = json.Marshal(workeridentity.OutputAuthority{AuthorityID: id, ProxyPublicKey: key.Public().(ed25519.PublicKey), Epoch: workeridentity.OutputEpoch(secret), Claims: inner})
				encoded, err := workeridentity.EncodeGrant(grant, private)
				return workercontrolcore.GrantReply{Grant: encoded}, err
			}
			nodehttp.Configure(func(base http.RoundTripper) http.RoundTripper {
				return outputs.WrapOutputs(base, core.WrapTransport(base))
			}, func(_ context.Context, node string) (bool, error) { return node == origin, nil })
			t.Cleanup(func() { nodehttp.Configure(nil) })
			manager := playback.NewSessionManager(0, 0)
			handler := handlers.NewPlaybackHandler(manager)
			handler.JWTSecret = secret
			recipes := &outputRelayRecipes{cards: map[string]playback.RecipeCard{}}
			handler.NodeRecipeStore = recipes
			for _, suffix := range []string{"A", "B"} {
				s := &playback.Session{ID: "play-" + suffix, UserID: 7, ProfileID: "profile", MediaFileID: 42, PlayMethod: playback.PlayTranscode, TranscodeNodeURL: origin, TranscodeTransportID: "transport-" + suffix, RequireMediaAuthorization: headerAuth, StartedAt: time.Now().Add(-time.Minute), OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: "login-" + suffix}}
				manager.RegisterReconstructed(s)
				card := playback.NewRecipeCard(s.UserID, s.ProfileID, s.MediaFileID, origin, playback.TranscodeOpts{SessionID: s.ID, TranscodeTransportID: s.TranscodeTransportID, InputPath: "/private/source.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac"})
				card.OriginCredential = s.OriginCredential
				card.OriginalStartedAt = s.StartedAt
				recipes.cards[s.TranscodeTransportID] = card
			}
			relay := workerOutputRelay{recipes: recipes, secret: func() string { return secret }}
			outputs.ResolveOutput = relay.token
			read := func(id string) *httptest.ResponseRecorder {
				req := outputRelayRouteParam(httptest.NewRequest(http.MethodGet, "/api/v1/playback/transcode/"+id+"/master.m3u8", nil), "session_id", id)
				if headerAuth {
					req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7, ProfileID: "profile", SessionID: "login-B", TokenType: auth.TokenTypeAccess, Role: "user"}))
				}
				rr := httptest.NewRecorder()
				handler.HandleGetTranscodeManifest(rr, req)
				if strings.Contains(rr.Body.String(), "st=") || strings.Contains(rr.Body.String(), secret) || strings.Contains(rr.Body.String(), "login-") {
					t.Fatal("internal credential escaped into public manifest")
				}
				return rr
			}
			for _, id := range []string{"play-A", "play-B"} {
				if rr := read(id); rr.Code != 200 {
					t.Fatalf("positive %s: %d %s", id, rr.Code, rr.Body.String())
				}
			}
			//nolint:forbidigo // Cross the cached authorization lifetime before checking that the revoked login is denied.
			time.Sleep(1100 * time.Millisecond)
			if rr := read("play-A"); rr.Code != 200 {
				t.Fatalf("warm read failed: %d", rr.Code)
			}
			mu.Lock()
			active["login-A"] = false
			mu.Unlock()
			if rr := read("play-A"); rr.Code < 400 {
				t.Errorf("revoked source login still served tokenless API relay: status=%d body=%s", rr.Code, rr.Body.String())
			}
			if rr := read("play-B"); rr.Code != 200 {
				t.Errorf("other login's playback stopped: %d %s", rr.Code, rr.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if commands != 0 {
				t.Errorf("customer output borrowed core-command authority %d times", commands)
			}
			if len(grantTokens["play-A"]) != 1 || len(grantTokens["play-B"]) != 1 {
				t.Errorf("expected stable internal token per play, got A=%d B=%d", len(grantTokens["play-A"]), len(grantTokens["play-B"]))
			}
		})
	}
}

type outputRelayRecipes struct {
	cards map[string]playback.RecipeCard
}

func (s *outputRelayRecipes) Enabled() bool { return true }
func (s *outputRelayRecipes) Get(_ context.Context, id string) (*playback.RecipeCard, bool) {
	c, ok := s.cards[id]
	return &c, ok
}
func (s *outputRelayRecipes) Put(_ context.Context, id string, c playback.RecipeCard) error {
	s.cards[id] = c
	return nil
}
func (s *outputRelayRecipes) Delete(_ context.Context, id string) error {
	delete(s.cards, id)
	return nil
}
func outputRelayRouteParam(r *http.Request, key, value string) *http.Request {
	c := chi.NewRouteContext()
	c.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, c))
}

func (s *outputRelayRecipes) RevokeNode(_ context.Context, node string) error {
	for id, c := range s.cards {
		if c.TranscodeNodeURL == node {
			delete(s.cards, id)
		}
	}
	return nil
}

func TestWorkerRelayRecipeIdentityAndRenewal(t *testing.T) {
	const secret = "recipe-renewal-secret"
	node := workercontrolcore.Node{ID: 17, URL: "https://worker.example.test", Enabled: true}
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	now := start.Add(time.Hour)
	card := playback.NewRecipeCard(7, "profile", 42, node.URL, playback.TranscodeOpts{SessionID: "play", TranscodeTransportID: "work", InputPath: "/private/source.mkv", TargetCodecVideo: "h264"})
	card.OriginLoginSessionID, card.OriginalStartedAt = "original-login", start
	card.RoutingExecutionNodeID = node.ID
	recipes := &outputRelayRecipes{cards: map[string]playback.RecipeCard{"work": card}}
	relay := workerOutputRelay{recipes: recipes, secret: func() string { return secret }, now: func() time.Time { return now }}
	request := httptest.NewRequest(http.MethodGet, node.URL+"/transcode/work/master.m3u8", nil)
	request = request.WithContext(apimw.SetClaims(request.Context(), &auth.Claims{UserID: 7, SessionID: "different-login", TokenType: auth.TokenTypeAccess}))
	first, err := relay.token(request, node)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	if next, err := relay.token(request, node); err != nil || next != first {
		t.Fatalf("stable recipe minted a different token: %v", err)
	}
	changed := card
	changed.AudioTrackIndex = 3
	recipes.cards["work"] = changed
	next, err := relay.token(request, node)
	if err != nil || next == first {
		t.Fatalf("replan did not replace internal token: %v", err)
	}
	claims, err := streamtoken.Verify(next, secret)
	if err != nil || claims.AudioTrackIndex != 3 || claims.OriginLoginSessionID != "original-login" {
		t.Fatalf("replan changed authority: %+v %v", claims, err)
	}
	now = start.Add(4*playback.MaxTokenTTL + time.Minute)
	renewed, err := relay.token(request, node)
	if err != nil || renewed == next {
		t.Fatalf("long-lived play did not renew: %v", err)
	}
	parsed := &streamtoken.Claims{}
	if _, err = jwt.ParseWithClaims(renewed, parsed, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithTimeFunc(func() time.Time { return now })); err != nil || parsed.OriginLoginSessionID != "original-login" {
		t.Fatalf("renewal invalid or borrowed caller: %v", err)
	}
	for name, mutate := range map[string]func(*playback.RecipeCard){
		"unbound":       func(c *playback.RecipeCard) { c.OriginCredential = streamtoken.OriginCredential{} },
		"ambiguous":     func(c *playback.RecipeCard) { c.OriginAPIKeyID = 5 },
		"no start":      func(c *playback.RecipeCard) { c.OriginalStartedAt = time.Time{} },
		"wrong work":    func(c *playback.RecipeCard) { c.TranscodeTransportID = "other" },
		"wrong node id": func(c *playback.RecipeCard) { c.RoutingExecutionNodeID = 18 },
		"wrong origin":  func(c *playback.RecipeCard) { c.TranscodeNodeURL = "https://other.example.test" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := card
			mutate(&bad)
			recipes.cards["work"] = bad
			if _, err := relay.token(request, node); err == nil {
				t.Fatal("invalid stored recipe admitted")
			}
		})
	}
	delete(recipes.cards, "work")
	if _, err := relay.token(request, node); err == nil {
		t.Fatal("missing recipe borrowed core command authority")
	}
}

func TestWorkerRelayFractionalDayBoundary(t *testing.T) {
	const secret = "fractional-boundary-secret"
	node := workercontrolcore.Node{ID: 17, URL: "https://worker.example.test", Enabled: true}
	start := time.Now().Truncate(time.Second).Add(-playback.MaxTokenTTL + 800*time.Millisecond)
	now := start.Add(playback.MaxTokenTTL - 400*time.Millisecond)
	card := playback.NewRecipeCard(7, "profile", 42, node.URL, playback.TranscodeOpts{SessionID: "play", TranscodeTransportID: "work", InputPath: "/private/source.mkv"})
	card.OriginLoginSessionID, card.OriginalStartedAt = "original-login", start
	relay := workerOutputRelay{recipes: &outputRelayRecipes{cards: map[string]playback.RecipeCard{"work": card}}, secret: func() string { return secret }, now: func() time.Time { return now }}
	token, err := relay.token(httptest.NewRequest(http.MethodGet, node.URL+"/transcode/work/master.m3u8", nil), node)
	if err != nil {
		t.Fatal(err)
	}
	claims := &streamtoken.Claims{}
	if _, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithTimeFunc(func() time.Time { return now })); err != nil {
		t.Fatalf("resolver minted an expired token within the live renewal boundary: %v (expiry=%v now=%v)", err, claims.ExpiresAt, now)
	}
}
