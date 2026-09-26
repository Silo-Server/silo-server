package historyimport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/netguard"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

type staticSettings map[string]string

func (s staticSettings) Get(_ context.Context, key string) (string, error) { return s[key], nil }

type failingSettings struct{}

func (failingSettings) Get(context.Context, string) (string, error) {
	return "true", errors.New("settings unavailable")
}

// allowLocalNetworkForEveryone is the policy with the admin setting on.
func allowLocalNetworkForEveryone() *LocalNetworkAccess {
	return NewLocalNetworkAccess(staticSettings{SettingAllowPrivateDestinations: "true"}, nil)
}

func TestLocalNetworkAccessIsOffUnlessTheSettingIsOn(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		access *LocalNetworkAccess
		want   bool
	}{
		{"no policy", nil, false},
		{"setting absent", NewLocalNetworkAccess(staticSettings{}, nil), false},
		{"setting off", NewLocalNetworkAccess(staticSettings{SettingAllowPrivateDestinations: "false"}, nil), false},
		{"setting unreadable", NewLocalNetworkAccess(failingSettings{}, nil), false},
		{"setting on", allowLocalNetworkForEveryone(), true},
	}
	for _, tc := range cases {
		if got := tc.access.Allowed(ctx, 7); got != tc.want {
			t.Errorf("%s: Allowed = %v, want %v", tc.name, got, tc.want)
		}
		if got := netguard.PrivateAccess(tc.access.Context(ctx, 7)); got != tc.want {
			t.Errorf("%s: Context private access = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLocalNetworkSettingIsARegisteredAdminBoolDefaultingOff(t *testing.T) {
	if got := config.EffectiveAdminSettings(nil)[SettingAllowPrivateDestinations]; got != "false" {
		t.Fatalf("default = %q, want false", got)
	}
	if got, err := config.NormalizeAdminSetting(SettingAllowPrivateDestinations, " true "); err != nil || got != "true" {
		t.Fatalf("NormalizeAdminSetting(true) = %q, %v", got, err)
	}
	if _, err := config.NormalizeAdminSetting(SettingAllowPrivateDestinations, "sometimes"); err == nil {
		t.Fatal("a non-boolean value was accepted")
	}
}

// The advisory's case: a non-admin points a Jellyfin import at an address on
// the server's network. Nothing may reach it, and the refusal must read as a
// policy decision, not an unreachable server.
func TestPersonalJellyfinImportRefusesLocalAddressWithoutTrust(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer upstream.Close()
	svc := &Service{jellyfin: NewJellyfinClient(), localNetwork: NewLocalNetworkAccess(staticSettings{}, nil)}

	_, err := svc.preparePersonalRun(t.Context(), 7, CreateRunInput{
		Source: SourceTypeJellyfin, ProfileID: "target",
		JellyfinBaseURL: upstream.URL, JellyfinUsername: "x", JellyfinPassword: "x",
	})
	if !errors.Is(err, netguard.ErrPrivateDestination) {
		t.Fatalf("error = %v, want ErrPrivateDestination", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the refused server received a request")
	}
	if message, ok := ServerAddressMessage(err); !ok || message != PrivateAddressMessage {
		t.Fatalf("ServerAddressMessage = %q, %v", message, ok)
	}
	if errors.Is(tagUnreachable(err), ErrSourceUnreachable) {
		t.Fatal("a refused address was reported as unreachable")
	}
}

// The transport refuses on its own too: a server that passes the admission
// check but later resolves locally, or a request that bypasses admission,
// still cannot connect.
func TestJellyfinClientRefusesLocalAddressWithoutTrust(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer upstream.Close()

	_, err := NewJellyfinClient().AuthenticateServerUser(context.Background(), upstream.URL, "x", "x")
	if !errors.Is(err, netguard.ErrPrivateDestination) {
		t.Fatalf("error = %v, want ErrPrivateDestination", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the refused server received a request")
	}
}

func TestPersonalPlexImportChecksAddressAtAdmission(t *testing.T) {
	input := CreateRunInput{Source: SourceTypePlex, ProfileID: "target", PlexBaseURL: "http://192.168.1.10:32400", PlexToken: "token"}

	untrusted := &Service{localNetwork: NewLocalNetworkAccess(staticSettings{}, nil)}
	if _, err := untrusted.preparePersonalRun(t.Context(), 7, input); !errors.Is(err, netguard.ErrPrivateDestination) {
		t.Fatalf("setting off: error = %v, want ErrPrivateDestination", err)
	}

	trusted := &Service{localNetwork: allowLocalNetworkForEveryone()}
	if _, err := trusted.preparePersonalRun(t.Context(), 7, input); err != nil {
		t.Fatalf("setting on: %v", err)
	}
}

// Metadata and other blocked addresses stay refused with the setting on.
func TestPersonalImportRefusesMetadataAddressEvenWhenTrusted(t *testing.T) {
	svc := &Service{jellyfin: NewJellyfinClient(), localNetwork: allowLocalNetworkForEveryone()}
	inputs := []CreateRunInput{
		{Source: SourceTypeJellyfin, ProfileID: "target", JellyfinBaseURL: "http://169.254.169.254", JellyfinUsername: "x", JellyfinPassword: "x"},
		{Source: SourceTypePlex, ProfileID: "target", PlexBaseURL: "http://169.254.169.254", PlexToken: "token"},
		{Source: SourceTypePlex, ProfileID: "target", PlexBaseURL: "http://[fd00:ec2::254]", PlexToken: "token"},
	}
	for _, input := range inputs {
		_, err := svc.preparePersonalRun(t.Context(), 7, input)
		if !errors.Is(err, netguard.ErrBlockedDestination) {
			t.Fatalf("%s %s: error = %v, want ErrBlockedDestination", input.Source, input.JellyfinBaseURL+input.PlexBaseURL, err)
		}
		if message, _ := ServerAddressMessage(err); message != BlockedAddressMessage {
			t.Fatalf("message = %q", message)
		}
	}
}

// Runs fetch through privateNetworkProvider only when the server was admin
// configured or the user is trusted; otherwise the fetch is refused and the
// run fails with the address message.
func TestRunFetchNeedsTrustForLocalServer(t *testing.T) {
	server := newJellyfinFetchServer(t, map[string][]jellyfinItem{"IsPlayed": nil, "IsFavorite": nil}, map[string]jellyfinItem{})
	provider := NewJellyfinProvider(NewJellyfinClient(), jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"})

	_, _, err := provider.Fetch(context.Background())
	if !errors.Is(err, netguard.ErrPrivateDestination) {
		t.Fatalf("untrusted fetch error = %v, want ErrPrivateDestination", err)
	}
	if got := userFacingRunError(ExecutionSummary{}, err); got != PrivateAddressMessage {
		t.Fatalf("run error = %q, want the address message", got)
	}

	if _, _, err := (privateNetworkProvider{provider}).Fetch(context.Background()); err != nil {
		t.Fatalf("trusted fetch: %v", err)
	}
}

func TestPublicRunReplacesDiagnostics(t *testing.T) {
	stored := Run{
		ID:           "run-1",
		ErrorMessage: `plex http 403: {"secret":"internal body"}`,
		Warnings: []string{
			`failed to fetch watched movies from section "Movies": plex http 403: internal body`,
			warnJellyfinFavoritesUnavailable,
		},
		UnmatchedSamples: []UnmatchedSample{{Kind: KindMovie, Title: "Heat", Reason: "db: connection reset"}},
	}

	public := PublicRun(stored)

	if public.ErrorMessage != GenericRunError {
		t.Fatalf("error message = %q", public.ErrorMessage)
	}
	for _, warning := range public.Warnings {
		if strings.Contains(warning, "internal body") {
			t.Fatalf("warning leaked a diagnostic: %q", warning)
		}
	}
	if public.Warnings[0] != GenericRunWarning || public.Warnings[1] != jellyfinFavoritesUnavailableSummary {
		t.Fatalf("warnings = %q", public.Warnings)
	}
	if public.UnmatchedSamples[0].Reason != GenericUnmatchedReason || public.UnmatchedSamples[0].Title != "Heat" {
		t.Fatalf("sample = %+v", public.UnmatchedSamples[0])
	}
	if stored.UnmatchedSamples[0].Reason != "db: connection reset" {
		t.Fatal("PublicRun modified the stored run")
	}

	for _, message := range []string{"", PrivateAddressMessage, BlockedAddressMessage, RunErrorNotCompleted} {
		if got := PublicRunError(message); got != message {
			t.Errorf("PublicRunError(%q) = %q, want it unchanged", message, got)
		}
	}
	if got := PublicRun(Run{}); got.Warnings != nil || got.UnmatchedSamples != nil {
		t.Fatal("nil slices must stay nil")
	}
}

// addUserRoles gives the personal queue fixture's users table the columns the
// local network policy reads; every account starts as an enabled non-admin.
func addUserRoles(t *testing.T, repo *Repository) {
	t.Helper()
	if _, err := repo.pool.Exec(t.Context(), `ALTER TABLE users ADD COLUMN role text NOT NULL DEFAULT 'user',
 ADD COLUMN enabled boolean NOT NULL DEFAULT true`); err != nil {
		t.Fatal(err)
	}
}

func makeUserOneAdmin(t *testing.T, repo *Repository) {
	t.Helper()
	addUserRoles(t, repo)
	if _, err := repo.pool.Exec(t.Context(), `UPDATE users SET role = 'admin' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
}

func TestLocalNetworkAccessTrustsOnlyEnabledAdmins(t *testing.T) {
	repo := personalQueueRepository(t)
	makeUserOneAdmin(t, repo)
	if _, err := repo.pool.Exec(t.Context(), `INSERT INTO users(id, role, enabled) VALUES (3, 'admin', false)`); err != nil {
		t.Fatal(err)
	}
	access := NewLocalNetworkAccess(staticSettings{}, repo)
	for userID, want := range map[int]bool{1: true, 2: false, 3: false, 99: false} {
		if got := access.Allowed(t.Context(), userID); got != want {
			t.Errorf("user %d: Allowed = %v, want %v", userID, got, want)
		}
	}
	// The setting extends the same trust to every account.
	everyone := NewLocalNetworkAccess(staticSettings{SettingAllowPrivateDestinations: "true"}, repo)
	if !everyone.Allowed(t.Context(), 2) {
		t.Error("setting on: a non-admin account was refused")
	}
}

// A run admitted while the setting was on must not reach the local server if
// the admin turns the setting off before the run executes.
func TestPersonalRunnerRereadsLocalNetworkPolicy(t *testing.T) {
	repo := personalEffectRepository(t)
	addUserRoles(t, repo)
	var fetchCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/Users/AuthenticateByName" {
			_, _ = w.Write([]byte(`{"AccessToken":"server-token","User":{"Id":"external"}}`))
			return
		}
		fetchCalls.Add(1)
		_, _ = w.Write([]byte(`{"Items":[]}`))
	}))
	defer upstream.Close()

	admitCtx, stopAdmit := context.WithCancel(t.Context())
	admitting := NewService(admitCtx, repo, pgstore.NewPostgresProvider(repo.pool))
	admitting.SetLocalNetworkAccess(NewLocalNetworkAccess(staticSettings{SettingAllowPrivateDestinations: "true"}, repo))
	run, err := admitting.CreateRun(t.Context(), 1, CreateRunInput{
		Source: SourceTypeJellyfin, ProfileID: "p",
		JellyfinBaseURL: upstream.URL, JellyfinUsername: "user", JellyfinPassword: "password",
	})
	stopAdmit()
	if err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	observed := make(chan Run, 16)
	runner := NewService(ctx, NewRepository(repo.pool, repo.cipher), pgstore.NewPostgresProvider(repo.pool))
	runner.SetLocalNetworkAccess(NewLocalNetworkAccess(staticSettings{SettingAllowPrivateDestinations: "false"}, repo))
	runner.SetStableIdentityResolver(watchstate.NewStableIdentityResolver(startupIdentityItems{}, nil, startupIdentityProviders{}))
	runner.AddObserver(queueObserverFunc(func(run Run) { observed <- run }))
	runner.StartBackgroundWork()

	finished := waitPersonalTerminal(t, observed, run.ID)
	if finished.Status != RunStatusFailed || finished.ErrorMessage != PrivateAddressMessage {
		t.Fatalf("run = %s %q, want failed with the address message", finished.Status, finished.ErrorMessage)
	}
	if fetchCalls.Load() != 0 {
		t.Fatalf("the local server received %d fetches after the setting was turned off", fetchCalls.Load())
	}
}
