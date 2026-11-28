package watchsync

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
)

const testMDBListURL = "https://mdblist.com/lists/alice/picks"

// mdblistListsFixture is a service with the MDBList provider registered and
// profile p1 of account 1 connected to it.
func mdblistListsFixture(t *testing.T, client *fakeWatchSyncPluginClient, providesLists bool) (*Service, *serviceFakeRepo) {
	t.Helper()
	repo := newServiceFakeRepo()
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4,
		ProviderKey:    mdblistProviderKey,
		CapabilityID:   mdblistProviderKey,
		DisplayName:    "MDBList",
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{
			AuthMethods:         []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
			ExportWatched:       true,
			MaxBatchSize:        25,
			ProvidesCustomLists: providesLists,
		},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) { return client, nil },
		Repository:    repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertConnection(context.Background(), Connection{
		ID: "conn-mdblist", Provider: mdblistProviderKey, UserID: 1, ProfileID: "p1",
		ProviderAccountID: "42", AccessToken: "profile-key",
	}); err != nil {
		t.Fatal(err)
	}
	return NewService(repo, registry), repo
}

// The profile's own connection reads every page of the list, and the twins'
// items come back in rank order; an item without any ID is dropped.
func TestMDBListProfileListsReadsEveryPageWithTheProfilesConnection(t *testing.T) {
	t.Parallel()

	movie := func(rank int32, tmdb string) *pluginv1.WatchSyncCustomListItem {
		return &pluginv1.WatchSyncCustomListItem{Rank: rank, Media: &pluginv1.WatchSyncMedia{
			MediaType: pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE, ExternalIds: map[string]string{"tmdb": tmdb},
		}}
	}
	client := &fakeWatchSyncPluginClient{customItemResponses: []*pluginv1.WatchSyncListCustomListItemsResponse{
		{Items: []*pluginv1.WatchSyncCustomListItem{movie(1, "10"), movie(3, "30")}, NextPageToken: "t2"},
		{Items: []*pluginv1.WatchSyncCustomListItem{
			{Rank: 2, Media: &pluginv1.WatchSyncMedia{
				MediaType: pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES, ExternalIds: map[string]string{"tvdb": "77", "imdb": "tt2"},
			}},
			{Rank: 4, Media: &pluginv1.WatchSyncMedia{MediaType: pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE}},
		}},
	}}
	svc, _ := mdblistListsFixture(t, client, true)

	entries, err := svc.MDBListProfileLists().ListItems(context.Background(), 1, "p1", testMDBListURL, 0)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	var ranks []int
	for _, e := range entries {
		ranks = append(ranks, e.Rank)
	}
	if !slices.Equal(ranks, []int{1, 2, 3}) {
		t.Fatalf("ranks = %v, want [1 2 3]", ranks)
	}
	if show := entries[1]; show.ItemType() != "series" || show.TVDBID == nil || *show.TVDBID != 77 || show.IMDbID != "tt2" {
		t.Fatalf("show entry = %+v", show)
	}
	if len(client.customItemRequests) != 2 {
		t.Fatalf("requests = %d, want 2", len(client.customItemRequests))
	}
	for i, req := range client.customItemRequests {
		if req.GetContext().GetCredentials().GetAccessToken() != "profile-key" || req.GetListUrl() != testMDBListURL ||
			req.GetPageToken() != []string{"", "t2"}[i] || req.GetMaxItems() != collectionutil.MDBListMaxEntries {
			t.Fatalf("request %d = %v", i, req)
		}
	}
}

// Each reason a profile can't read a list maps to what the collection sync
// tells the profile to change; a rate limit also defers the connection's own
// watch sync, and a deferred connection isn't asked again but still counts as
// connected.
func TestMDBListProfileListsReportsWhyItCannotRead(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(time.Hour)
	fault := func(code pluginv1.WatchSyncFaultCode, message string) *pluginv1.WatchSyncListCustomListItemsResponse {
		return &pluginv1.WatchSyncListCustomListItemsResponse{Fault: &pluginv1.WatchSyncFault{
			Code: code, SafeMessage: message, RetryAfter: durationpb.New(30 * time.Minute),
		}}
	}
	for name, tc := range map[string]struct {
		providesLists bool
		profile       string
		rateLimited   bool
		response      *pluginv1.WatchSyncListCustomListItemsResponse
		rpcErr        error
		wantErr       error
		wantKind      collectionutil.MDBListReadErrorKind
		wantReason    string
		wantCalls     int
		wantDeferred  bool
	}{
		"old plugin":       {profile: "p1", wantErr: collectionutil.ErrMDBListProviderTooOld},
		"no connection":    {providesLists: true, profile: "p2", wantErr: collectionutil.ErrMDBListNoConnection},
		"deferred":         {providesLists: true, profile: "p1", rateLimited: true, wantKind: collectionutil.MDBListRateLimited},
		"unimplemented":    {providesLists: true, profile: "p1", rpcErr: status.Error(codes.Unimplemented, "nope"), wantErr: collectionutil.ErrMDBListProviderTooOld, wantCalls: 1},
		"rejected key":     {providesLists: true, profile: "p1", response: fault(pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL, "MDBList rejected the API key"), wantKind: collectionutil.MDBListKeyRejected, wantReason: "MDBList rejected the API key", wantCalls: 1},
		"unreadable list":  {providesLists: true, profile: "p1", response: fault(pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_PERMISSION_DENIED, "MDBList won't show this list to this account"), wantKind: collectionutil.MDBListListUnreadable, wantReason: "MDBList won't show this list to this account", wantCalls: 1},
		"rate limited now": {providesLists: true, profile: "p1", response: fault(pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED, ""), wantKind: collectionutil.MDBListRateLimited, wantCalls: 1, wantDeferred: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := &fakeWatchSyncPluginClient{customErr: tc.rpcErr}
			if tc.response != nil {
				client.customItemResponses = []*pluginv1.WatchSyncListCustomListItemsResponse{tc.response}
			}
			svc, repo := mdblistListsFixture(t, client, tc.providesLists)
			if tc.rateLimited {
				conn := repo.connections[connectionKey(mdblistProviderKey, 1, "p1")]
				conn.RateLimitedUntil = &future
				repo.connections[connectionKey(mdblistProviderKey, 1, "p1")] = conn
			}

			_, err := svc.MDBListProfileLists().ListItems(context.Background(), 1, tc.profile, testMDBListURL, 0)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantKind != 0 {
				var readErr *collectionutil.MDBListReadError
				if !errors.As(err, &readErr) || readErr.Kind != tc.wantKind || readErr.Reason != tc.wantReason {
					t.Fatalf("err = %#v, want kind %d reason %q", err, tc.wantKind, tc.wantReason)
				}
			}
			if len(client.customItemRequests) != tc.wantCalls {
				t.Fatalf("plugin calls = %d, want %d", len(client.customItemRequests), tc.wantCalls)
			}
			// The editor's availability check asks no plugin and ignores a
			// deferral, so a profile waiting out the daily limit keeps its lists;
			// only the reasons found before a plugin call hide them.
			wantCheck := tc.wantErr
			if tc.wantCalls > 0 {
				wantCheck = nil
			}
			if check := svc.MDBListProfileLists().Check(context.Background(), 1, tc.profile); !errors.Is(check, wantCheck) {
				t.Fatalf("Check = %v, want %v", check, wantCheck)
			}
			deferred := repo.connections[connectionKey(mdblistProviderKey, 1, "p1")].RateLimitedUntil
			if tc.wantDeferred && (deferred == nil || !deferred.After(time.Now())) {
				t.Fatalf("connection not deferred: %v", deferred)
			}
		})
	}
}
