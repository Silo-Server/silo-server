package workerregistration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerproxy"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type originRoundTripper func(*http.Request) (*http.Response, error)

func (f originRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

//nolint:funlen,gocyclo // One enrolled node must preserve its canonical origins across refresh, command dispatch, and output authorization.
func TestEnrolledCanonicalOriginsRefreshAndRouteCommandsAndOutput(t *testing.T) {
	for _, tc := range []struct {
		name, stored, canonical, explicit string
		allowHTTP                         bool
	}{
		{"trailing_slash", "https://worker.example.test/", "https://worker.example.test", "https://worker.example.test:443", false},
		{"host_case", "https://WORKER.Example.Test/", "https://worker.example.test", "https://WORKER.Example.Test:443", false},
		{"scheme_case", "HTTPS://Worker.Example.Test/", "https://worker.example.test", "https://Worker.Example.Test:443", false},
		{"default_https_port", "https://WORKER.Example.Test:443/", "https://worker.example.test", "https://worker.example.test:443", false},
		{"default_http_port", "http://WORKER.Example.Test:80/", "http://worker.example.test", "http://worker.example.test:80", true},
		{"ipv6", "https://[2001:DB8::1]:443/", "https://[2001:db8::1]", "https://[2001:DB8::1]:443", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := registrationDB(t)
			if _, err := repo.pool.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=17", tc.stored); err != nil {
				t.Fatal(err)
			}
			ticket, err := repo.Issue(t.Context(), 17, "https://core.example.test", tc.allowHTTP)
			if err != nil {
				t.Fatal(err)
			}
			public, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.Redeem(t.Context(), ticket, public); err != nil {
				t.Fatal(err)
			}
			var persisted string
			if err := repo.pool.QueryRow(t.Context(), "SELECT url FROM stream_nodes WHERE id=17").Scan(&persisted); err != nil || persisted != tc.canonical {
				t.Fatalf("enrollment did not persist canonical origin: %q err%v", persisted, err)
			}
			// Equivalent edits and older registrations are normalized when read,
			// while the complete stored URL still undergoes origin validation.
			if _, err := repo.pool.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=17", tc.stored); err != nil {
				t.Fatal(err)
			}
			node, found, err := repo.ByID(t.Context(), 17)
			if err != nil || !found || node.URL != tc.canonical {
				t.Fatalf("read identity not canonical: node%+v found%v err%v", node, found, err)
			}
			coreKey, err := repo.SigningKey(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			store := workercontrolcore.NewMemoryStore()
			a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 17, WorkID: "artifact", Path: "/downloads/prepare"})
			if err != nil {
				t.Fatal(err)
			}
			original, worker := strings.Repeat("a", 64), strings.Repeat("b", 64)
			if err := store.BindInputs(t.Context(), 17, a.WorkID, a.Generation, map[string]string{"receipt:original": original, "receipt:worker": worker}); err != nil {
				t.Fatal(err)
			}
			if err := store.Complete(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if err := store.Activate(t.Context(), 17, a.WorkID, a.Generation); err != nil {
				t.Fatal(err)
			}
			const secret = "canonical-origin-output-test-secret"
			service, err := workercontrolcore.New(workercontrolcore.Options{Origin: ticket.CoreURL, PrivateKey: coreKey, Nodes: repo, Store: store, OutputAuthorities: workercontrolcore.NewMemoryOutputAuthorities(), VerifyToken: func(_ context.Context, token string) (*streamtoken.Claims, error) {
				return streamtoken.Verify(token, secret)
			}})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, ticket.CoreURL+"/api/internal/workers/17/snapshot", strings.NewReader(`{"boot_id":"boot","nonce":"nonce","sequence":0}`))
			if err := workeridentity.Sign(request, ticket.CoreURL, workeridentity.DomainNodeProof, private, 17, false); err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("enrolled origin cannot refresh signed authority: status%d body%s", response.Code, response.Body.String())
			}
			corePublic := coreKey.Public().(ed25519.PublicKey)
			for _, lookup := range []string{tc.stored, tc.canonical, tc.explicit} {
				registered, found, err := repo.ByURL(t.Context(), lookup)
				if err != nil || !found || registered.ID != 17 || registered.URL != tc.canonical {
					t.Fatalf("equivalent origin lookup failed: %q node%+v found%v err%v", lookup, registered, found, err)
				}
				command := httptest.NewRequest(http.MethodGet, strings.TrimRight(lookup, "/")+"/status", nil)
				out, err := service.PrepareRequest(command)
				if err != nil {
					t.Fatalf("equivalent origin cannot dispatch: %q: %v", lookup, err)
				}
				verifier := &workeridentity.Verifier{Origin: tc.canonical, Domain: workeridentity.DomainCoreCommand, AllowHTTP: tc.allowHTTP, ResolveKey: func(int) (ed25519.PublicKey, error) { return corePublic, nil }}
				if err := verifier.Verify(out, 17); err != nil {
					t.Fatalf("command not scoped to canonical origin: %v", err)
				}
				token, err := streamtoken.Sign(streamtoken.Claims{OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: "canonical-origin-fixture-login"}, DownloadArtifactID: a.WorkID, TranscodeNode: lookup, RoutingExecutionNodeID: 17, PlayMethod: streamtoken.PlayMethodToneMapDownload, DownloadExecutionFingerprint: original, DownloadArtifactSize: 10, UserID: 5, MediaFileID: 9}, secret, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				proxy, err := workerproxy.New(ticket.CoreURL, secret, repo)
				if err != nil {
					t.Fatal(err)
				}
				proxy.Grant = func(ctx context.Context, token, url string) (workercontrolcore.GrantReply, error) {
					return service.IssueOutputGrant(ctx, token, url, secret)
				}
				calls := 0
				base := originRoundTripper(func(r *http.Request) (*http.Response, error) {
					calls++
					grant, err := workeridentity.DecodeGrant(r.Header.Get("X-Silo-Stream-Token"), workeridentity.DomainProxyOutputGrant, tc.canonical, corePublic, time.Now())
					var authority workeridentity.OutputAuthority
					if err != nil || json.Unmarshal(grant.Body, &authority) != nil {
						t.Fatalf("proxy output grant invalid: %v", err)
					}
					var claims streamtoken.Claims
					if err := json.Unmarshal(authority.Claims, &claims); err != nil || claims.TranscodeNode != tc.canonical {
						t.Fatalf("worker wire claim does not match its canonical binding: %q err%v", claims.TranscodeNode, err)
					}
					verifier := &workeridentity.Verifier{Origin: tc.canonical, Domain: workeridentity.DomainProxyOutputRequest, AllowHTTP: tc.allowHTTP}
					if err := verifier.VerifyKey(r, 17, authority.ProxyPublicKey); err != nil {
						t.Fatalf("proxy output proof invalid: %v", err)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Silo-Download-Execution-Fingerprint": []string{worker}}, Body: io.NopCloser(strings.NewReader("artifact")), Request: r}, nil
				})
				output := httptest.NewRequest(http.MethodGet, strings.TrimRight(lookup, "/")+"/downloads/artifacts/artifact", nil)
				output.Header.Set("X-Silo-Stream-Token", token)
				result, err := proxy.Wrap(base).RoundTrip(output)
				if err != nil {
					t.Fatalf("equivalent origin cannot deliver authorized output: %q: %v", lookup, err)
				}
				if err := result.Body.Close(); err != nil {
					t.Fatal(err)
				}
				if calls != 1 || result.Header.Get("X-Silo-Download-Execution-Fingerprint") != original {
					t.Fatal("output skipped scoped proxy or prepared receipt bridge")
				}
			}
		})
	}
}
