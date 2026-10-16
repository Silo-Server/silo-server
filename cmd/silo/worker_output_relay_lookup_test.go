package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type relayLookupSpy struct {
	workercontrolcore.OutputAuthorities
	findCalls, listCalls int
	find                 func(context.Context, int, string) (workercontrolcore.OutputAuthorityRecord, bool, error)
}

type relayLookupFixture struct {
	content    *contentFixture
	relay      workerOutputRelay
	spy        *relayLookupSpy
	registry   *workercontrolcore.MemoryOutputAuthorities
	node       workercontrolcore.Node
	store      *workercontrolcore.MemoryStore
	assignment workerwire.Assignment
	request    *http.Request
	now        time.Time
	secret     string
}

func newRelayLookupFixture(t *testing.T) *relayLookupFixture {
	t.Helper()
	f := &relayLookupFixture{content: newContentFixture(t), now: time.Now(), secret: "targeted-output-lookup-secret"}
	prepared := f.content.preparedClaims(t, "720p")
	if _, err := f.content.pool.Exec(t.Context(), "UPDATE download_artifacts SET file_size=10 WHERE id=$1", prepared.DownloadArtifactRowID); err != nil {
		t.Fatal(err)
	}
	f.node = workercontrolcore.Node{ID: prepared.RoutingExecutionNodeID, URL: prepared.TranscodeNode, Enabled: true, AllowHTTP: true}
	f.store = workercontrolcore.NewMemoryStore()
	var err error
	f.assignment, err = f.store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: f.node.ID, WorkID: prepared.DownloadArtifactID, Path: "/downloads/prepare"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Complete(t.Context(), f.assignment); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Activate(t.Context(), f.node.ID, f.assignment.WorkID, f.assignment.Generation); err != nil {
		t.Fatal(err)
	}
	f.registry = workercontrolcore.NewMemoryOutputAuthorities()
	f.spy = &relayLookupSpy{OutputAuthorities: f.registry, find: f.registry.Find}
	f.relay = workerOutputRelay{pool: f.content.pool, authorities: f.spy, assignments: f.store, secret: func() string { return f.secret }, now: func() time.Time { return f.now }}
	f.request = httptest.NewRequest(http.MethodGet, f.node.URL+"/downloads/artifacts/"+f.assignment.WorkID, nil)
	f.request = f.request.WithContext(apimw.SetProfileID(apimw.SetClaims(t.Context(), &auth.Claims{UserID: f.content.user, SessionID: "fixture-original-login", TokenType: auth.TokenTypeAccess}), "viewer"))
	return f
}

func (f *relayLookupFixture) register(t *testing.T, token string, key string) workercontrolcore.OutputAuthorityRecord {
	t.Helper()
	claims, err := streamtoken.Verify(token, f.secret)
	if err != nil {
		t.Fatal(err)
	}
	if key == "" {
		key = workercontrolcore.OutputAuthorityLookupKey(*claims, f.node.ID, f.assignment.WorkID, f.assignment.Generation, workeridentity.OutputEpoch(f.secret))
	}
	r := workercontrolcore.OutputAuthorityRecord{LookupKey: key, Token: token, NodeID: f.node.ID, WorkID: f.assignment.WorkID, Generation: f.assignment.Generation, Epoch: workeridentity.OutputEpoch(f.secret), ExpiresAt: claims.ExpiresAt.Unix()}
	if _, err = f.registry.Register(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	got, found, err := f.registry.Find(t.Context(), f.node.ID, key)
	if err != nil || !found {
		t.Fatalf("missing registered fixture: found=%v err=%v", found, err)
	}
	return got
}

func TestWorkerPreparedRelayLookupRejectsUntrustedIndexRecords(t *testing.T) {
	f := newRelayLookupFixture(t)
	token, err := f.relay.token(f.request, f.node)
	if err != nil {
		t.Fatal(err)
	}
	first := f.register(t, token, "")
	for name, mutate := range map[string]func(*workercontrolcore.OutputAuthorityRecord){
		"node":                func(r *workercontrolcore.OutputAuthorityRecord) { r.NodeID++ },
		"work":                func(r *workercontrolcore.OutputAuthorityRecord) { r.WorkID += "other" },
		"generation":          func(r *workercontrolcore.OutputAuthorityRecord) { r.Generation++ },
		"epoch":               func(r *workercontrolcore.OutputAuthorityRecord) { r.Epoch += "other" },
		"lookup_key":          func(r *workercontrolcore.OutputAuthorityRecord) { r.LookupKey = "wrong" },
		"record_id":           func(r *workercontrolcore.OutputAuthorityRecord) { r.ID = "wrong" },
		"expiry":              func(r *workercontrolcore.OutputAuthorityRecord) { r.ExpiresAt = f.now.Add(-time.Hour).Unix() },
		"jwt_expiry_mismatch": func(r *workercontrolcore.OutputAuthorityRecord) { r.ExpiresAt++ },
	} {
		t.Run(name, func(t *testing.T) {
			bad := first
			mutate(&bad)
			f.spy.find = func(context.Context, int, string) (workercontrolcore.OutputAuthorityRecord, bool, error) {
				return bad, true, nil
			}
			if _, err := f.relay.token(f.request, f.node); err == nil {
				t.Fatal("untrusted index record was reused or silently accepted")
			}
		})
	}
	claims, err := streamtoken.Verify(token, f.secret)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*streamtoken.Claims){
		"credential":  func(c *streamtoken.Claims) { c.OriginLoginSessionID += "other" },
		"profile":     func(c *streamtoken.Claims) { c.ProfileID += "other" },
		"row":         func(c *streamtoken.Claims) { c.DownloadArtifactRowID += "other" },
		"size":        func(c *streamtoken.Claims) { c.DownloadArtifactSize++ },
		"fingerprint": func(c *streamtoken.Claims) { c.DownloadExecutionFingerprint += "other" },
	} {
		t.Run(name, func(t *testing.T) {
			cloned := *claims
			mutate(&cloned)
			badToken, err := streamtoken.SignAt(cloned, f.secret, f.now, playback.MaxTokenTTL)
			if err != nil {
				t.Fatal(err)
			}
			bad := f.register(t, badToken, first.LookupKey)
			f.spy.find = func(context.Context, int, string) (workercontrolcore.OutputAuthorityRecord, bool, error) {
				return bad, true, nil
			}
			if _, err := f.relay.token(f.request, f.node); err == nil {
				t.Fatal("valid JWT for a different full claim identity was reused")
			}
		})
	}
	for _, future := range []bool{false, true} {
		issued, signingSecret := f.now, "unrelated-signing-secret"
		if future {
			issued, signingSecret = f.now.Add(time.Hour), f.secret
		}
		badToken, err := streamtoken.SignAt(*claims, signingSecret, issued, playback.MaxTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		// Store a valid record identity independently of JWT validity.
		r := first
		r.ID, r.Revision, r.Token, r.ExpiresAt = "", "", badToken, issued.Add(playback.MaxTokenTTL).Unix()
		if _, err = f.registry.Register(t.Context(), r); err != nil {
			t.Fatal(err)
		}
		bad, found, err := f.registry.Find(t.Context(), f.node.ID, first.LookupKey)
		if err != nil || !found {
			t.Fatal("missing malformed-JWT record")
		}
		f.spy.find = func(context.Context, int, string) (workercontrolcore.OutputAuthorityRecord, bool, error) {
			return bad, true, nil
		}
		if _, err = f.relay.token(f.request, f.node); err == nil {
			t.Fatalf("invalid signature or future-issued token reused: future=%v", future)
		}
	}
	f.spy.find = func(context.Context, int, string) (workercontrolcore.OutputAuthorityRecord, bool, error) {
		return workercontrolcore.OutputAuthorityRecord{}, false, errors.New("registry unavailable")
	}
	if _, err = f.relay.token(f.request, f.node); err == nil {
		t.Fatal("registry failure silently minted a new token")
	}
	if f.spy.listCalls != 0 {
		t.Fatal("untrusted lookup fell back to a node-wide scan")
	}
}

//nolint:gocyclo // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestWorkerPreparedRelayLookupRetainsFullDayAndFencesNewScopes(t *testing.T) {
	f := newRelayLookupFixture(t)
	first, err := f.relay.token(f.request, f.node)
	if err != nil {
		t.Fatal(err)
	}
	old := f.register(t, first, "")
	f.now = f.now.Add(6 * time.Minute)
	f.request.Method = http.MethodHead
	if got, err := f.relay.token(f.request, f.node); err != nil || got != first {
		t.Fatalf("HEAD after request-grant lifetime lost retained long token: equal=%v err=%v", got == first, err)
	}
	claims, err := streamtoken.Verify(first, f.secret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ExpiresAt.Sub(claims.IssuedAt.Time) != playback.MaxTokenTTL {
		t.Fatal("new relay token was shortened from its full-day lifetime")
	}
	nearExpiry, err := streamtoken.SignAt(*claims, f.secret, f.now.Add(-playback.MaxTokenTTL+4*time.Minute), playback.MaxTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	near := f.register(t, nearExpiry, old.LookupKey)
	fresh, err := f.relay.token(f.request, f.node)
	if err != nil || fresh == nearExpiry {
		t.Fatalf("near-boundary token was not renewed for a full day: %v", err)
	}
	full, err := streamtoken.Verify(fresh, f.secret)
	if err != nil || full.ExpiresAt.Unix() != f.now.Add(playback.MaxTokenTTL).Unix() {
		t.Fatalf("renewal inherited an expiring bucket boundary: %v", err)
	}
	if retained, found, err := f.registry.Find(t.Context(), f.node.ID, old.LookupKey); err != nil || !found || retained != near {
		t.Fatal("renewal removed the authority needed by an already-open body")
	}
	f.register(t, fresh, "")
	f.secret = "rotated-targeted-output-secret"
	rotated, err := f.relay.token(f.request, f.node)
	if err != nil || rotated == fresh {
		t.Fatalf("signing epoch rotation reused old token: %v", err)
	}
	f.register(t, rotated, "")
	if err := f.store.Stop(t.Context(), f.node.ID, f.assignment.WorkID); err != nil {
		t.Fatal(err)
	}
	oldAssignment := f.assignment
	f.assignment, err = f.store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: f.node.ID, WorkID: oldAssignment.WorkID, Path: "/downloads/prepare"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Complete(t.Context(), f.assignment); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Activate(t.Context(), f.node.ID, f.assignment.WorkID, f.assignment.Generation); err != nil {
		t.Fatal(err)
	}
	newKey := workercontrolcore.OutputAuthorityLookupKey(*full, f.node.ID, f.assignment.WorkID, f.assignment.Generation, workeridentity.OutputEpoch(f.secret))
	if _, found, err := f.registry.Find(t.Context(), f.node.ID, newKey); err != nil || found {
		t.Fatal("new generation resolved an old-generation record")
	}
	// Same-second JWT bytes can remain equal; record IDs, lookup keys and
	// worker grants are independently fenced by the authoritative generation.
	if _, err = f.relay.token(f.request, f.node); err != nil {
		t.Fatal(err)
	}
	if _, err = f.content.pool.Exec(t.Context(), "UPDATE download_artifacts SET file_size=11 WHERE origin_node_id=$1", f.node.ID); err != nil {
		t.Fatal(err)
	}
	changed, err := f.relay.token(f.request, f.node)
	if err != nil {
		t.Fatal(err)
	}
	changedClaims, err := streamtoken.Verify(changed, f.secret)
	if err != nil || changedClaims.DownloadArtifactSize != 11 {
		t.Fatalf("authoritative size edit reused stale token: %v", err)
	}
	row := uuid.NewString()
	if _, err = f.content.pool.Exec(t.Context(), "UPDATE download_artifacts SET id=$2 WHERE origin_node_id=$1", f.node.ID, row); err != nil {
		t.Fatal(err)
	}
	changed, err = f.relay.token(f.request, f.node)
	if err != nil {
		t.Fatal(err)
	}
	changedClaims, err = streamtoken.Verify(changed, f.secret)
	if err != nil || changedClaims.DownloadArtifactRowID != row {
		t.Fatalf("authoritative row edit reused stale token: %v", err)
	}
	if f.spy.listCalls != 0 {
		t.Fatal("scope changes scanned unrelated viewers")
	}
}

func TestWorkerPreparedRelayLookupAcceptsCanonicalRetainedOrigin(t *testing.T) {
	f := newRelayLookupFixture(t)
	first, err := f.relay.token(f.request, f.node)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := streamtoken.Verify(first, f.secret)
	if err != nil {
		t.Fatal(err)
	}
	// The node fixture uses explicit local HTTP; both forms name the same
	// authoritative origin and remain valid signed customer credentials.
	claims.TranscodeNode = "HTTP://" + strings.ToUpper(strings.TrimPrefix(f.node.URL, "http://")) + ":0080/"
	alias, err := streamtoken.SignAt(*claims, f.secret, f.now, playback.MaxTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	record := f.register(t, alias, "")
	if key := workercontrolcore.OutputAuthorityLookupKey(*claims, f.node.ID, f.assignment.WorkID, f.assignment.Generation, workeridentity.OutputEpoch(f.secret)); key != record.LookupKey {
		t.Fatal("retained alias did not use the canonical lookup identity")
	}
	f.request.Method = http.MethodHead
	got, err := f.relay.token(f.request, f.node)
	if err != nil || got != alias {
		t.Fatalf("equivalent retained origin denied the prepared relay: equal=%v err=%v", got == alias, err)
	}
	if f.spy.listCalls != 0 {
		t.Fatal("canonical alias fell back to a registry scan")
	}
}

func (s *relayLookupSpy) List(context.Context, int) ([]workercontrolcore.OutputAuthorityRecord, error) {
	s.listCalls++
	return nil, errors.New("whole-node output authority scan forbidden")
}

func (s *relayLookupSpy) Find(ctx context.Context, node int, key string) (workercontrolcore.OutputAuthorityRecord, bool, error) {
	s.findCalls++
	if s.find != nil {
		return s.find(ctx, node, key)
	}
	return workercontrolcore.OutputAuthorityRecord{}, false, nil
}

func TestWorkerPreparedRelayUsesTargetedAuthorityLookup(t *testing.T) {
	f := newContentFixture(t)
	prepared := f.preparedClaims(t, "720p")
	if _, err := f.pool.Exec(t.Context(), "UPDATE download_artifacts SET file_size=10 WHERE id=$1", prepared.DownloadArtifactRowID); err != nil {
		t.Fatal(err)
	}
	node := workercontrolcore.Node{ID: prepared.RoutingExecutionNodeID, URL: prepared.TranscodeNode, Enabled: true, AllowHTTP: true}
	store := workercontrolcore.NewMemoryStore()
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: node.ID, WorkID: prepared.DownloadArtifactID, Path: "/downloads/prepare"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), node.ID, a.WorkID, a.Generation); err != nil {
		t.Fatal(err)
	}
	spy := &relayLookupSpy{OutputAuthorities: workercontrolcore.NewMemoryOutputAuthorities()}
	now := time.Now()
	relay := workerOutputRelay{pool: f.pool, authorities: spy, assignments: store, secret: func() string { return "targeted-output-lookup-secret" }, now: func() time.Time { return now }}
	request := httptest.NewRequest(http.MethodGet, node.URL+"/downloads/artifacts/"+a.WorkID, nil)
	request = request.WithContext(apimw.SetProfileID(apimw.SetClaims(t.Context(), &auth.Claims{UserID: f.user, SessionID: "fixture-original-login", TokenType: auth.TokenTypeAccess}), "viewer"))
	if token, err := relay.token(request, node); err != nil || token == "" {
		t.Fatalf("indexed miss did not mint a fresh token without scanning: %v", err)
	}
	if spy.findCalls != 1 || spy.listCalls != 0 {
		t.Fatalf("Range relay scanned unrelated authorities: find=%d list=%d", spy.findCalls, spy.listCalls)
	}
}
