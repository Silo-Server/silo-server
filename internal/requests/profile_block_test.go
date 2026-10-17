package requests

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// profileBlockStores serves the household's profiles to the request service.
// Only GetProfile is implemented; the gate reads nothing else.
type profileBlockStores struct {
	profiles map[string]*userstore.Profile
	err      error
}

func (p profileBlockStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return profileBlockStore{stores: p}, nil
}

func (profileBlockStores) Close() error { return nil }

type profileBlockStore struct {
	userstore.UserStore
	stores profileBlockStores
}

func (s profileBlockStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	if s.stores.err != nil {
		return nil, s.stores.err
	}
	return s.stores.profiles[id], nil
}

// One account, two profiles: the parent may request, the kid is blocked.
func newProfileBlockService(store *fakeStore) *Service {
	service := newTestServiceWithTMDB(store, &fakeTMDBClient{
		page:         &tmdb.MediaPage{Page: 1, TotalPages: 1},
		discoverPage: &tmdb.MediaPage{Page: 1, TotalPages: 1},
		detail:       &tmdb.MediaDetail{ID: 550},
	})
	service.SetGroupPolicyProvider(requestGroupProvider{group: &access.GroupPolicy{RequestsAllowed: true}})
	service.SetProfileStores(profileBlockStores{profiles: map[string]*userstore.Profile{
		"parent": {ID: "parent"},
		"kid":    {ID: "kid", RequestsBlocked: true},
	}})
	return service
}

var (
	parentViewer = Viewer{UserID: 1, ProfileID: "parent"}
	kidViewer    = Viewer{UserID: 1, ProfileID: "kid"}
)

// A blocked profile is refused by every request and discovery operation,
// while another profile on the same account keeps them.
func TestBlockedProfileIsKeptOutOfRequestsAndDiscovery(t *testing.T) {
	ctx := context.Background()
	operations := map[string]func(*Service, Viewer) error{
		"capability": func(s *Service, v Viewer) error {
			allowed, err := s.RequestCapabilityAllowed(ctx, v)
			if err == nil && !allowed {
				return ErrForbidden
			}
			return err
		},
		"create": func(s *Service, v Viewer) error {
			_, err := s.CreateRequest(ctx, v, CreateRequestInput{MediaType: MediaTypeMovie, TMDBID: 550, Title: "Fight Club"})
			return err
		},
		"follow": func(s *Service, v Viewer) error {
			_, err := s.Follow(ctx, v, MediaTypeMovie, 550)
			if errors.Is(err, ErrNotRequested) {
				// Nothing to follow is past the gate.
				return nil
			}
			return err
		},
		"search": func(s *Service, v Viewer) error {
			_, err := s.Search(ctx, v, "fight", MediaTypeMovie, 1)
			return err
		},
		"discover": func(s *Service, v Viewer) error {
			_, err := s.Discover(ctx, v, "trending_movies", 1)
			return err
		},
		"discover all": func(s *Service, v Viewer) error {
			_, err := s.DiscoverAll(ctx, v)
			return err
		},
		"detail": func(s *Service, v Viewer) error {
			_, err := s.GetDetail(ctx, v, MediaTypeMovie, 550)
			return err
		},
		"studios": func(s *Service, v Viewer) error {
			_, err := s.ListStudios(ctx, v)
			return err
		},
		"networks": func(s *Service, v Viewer) error {
			_, err := s.ListNetworks(ctx, v)
			return err
		},
		"genres": func(s *Service, v Viewer) error {
			_, err := s.ListGenres(ctx, v)
			return err
		},
		"browse studio": func(s *Service, v Viewer) error {
			_, err := s.BrowseStudio(ctx, v, BundledStudios[0].Slug, "", 1)
			return err
		},
		"browse network": func(s *Service, v Viewer) error {
			_, err := s.BrowseNetwork(ctx, v, BundledNetworks[0].Slug, "", 1)
			return err
		},
		"browse genre": func(s *Service, v Viewer) error {
			_, err := s.BrowseGenre(ctx, v, BundledGenres[0].Slug, MediaTypeMovie, "", 1)
			return err
		},
	}
	for name, run := range operations {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			service := newProfileBlockService(store)
			if err := run(service, kidViewer); !errors.Is(err, ErrForbidden) {
				t.Fatalf("blocked profile: err = %v, want ErrForbidden", err)
			}
			if len(store.created) != 0 {
				t.Fatalf("blocked profile created %d requests", len(store.created))
			}
			if err := run(service, parentViewer); err != nil {
				t.Fatalf("allowed profile on the same account: %v", err)
			}
		})
	}
}

// The profile block only narrows the account's switch: an account denied
// requests stays denied whatever its profile says, and keeps discovery as
// it did before profiles could be blocked.
func TestProfileBlockLayersOnTheAccountSwitch(t *testing.T) {
	ctx := context.Background()
	service := newProfileBlockService(newFakeStore())
	deny := false
	groupID := int64(1)
	service.SetUserRepository(requestUserRepo{user: &models.User{ID: 1, AccessGroupID: &groupID, RequestsAllowed: &deny}})

	if allowed, err := service.RequestCapabilityAllowed(ctx, parentViewer); err != nil || allowed {
		t.Fatalf("denied account, allowed profile: allowed=%v err=%v, want false", allowed, err)
	}
	if _, err := service.Search(ctx, parentViewer, "fight", MediaTypeMovie, 1); err != nil {
		t.Fatalf("denied account keeps discovery: %v", err)
	}
}

// A blocked profile still reads the requests it made before the block, and
// only those: the account's other profiles' requests are TMDB titles the
// block keeps it out of. An allowed profile keeps the account's list.
func TestBlockedProfileKeepsItsRequestList(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	service := newProfileBlockService(store)
	own := &Request{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, RequestedByUserID: 1, RequestedByProfileID: "kid", Status: StatusPending, Outcome: OutcomeActive}
	parents := &Request{ID: "req-2", MediaType: MediaTypeMovie, TMDBID: 603, RequestedByUserID: 1, RequestedByProfileID: "parent", Status: StatusPending, Outcome: OutcomeActive}
	store.requests[own.ID] = own
	store.requests[parents.ID] = parents
	store.mine = []*Request{parents, own}

	ids := func(reqs []*Request) []string {
		out := make([]string, 0, len(reqs))
		for _, req := range reqs {
			out = append(out, req.ID)
		}
		return out
	}
	listed, err := service.ListMine(ctx, kidViewer, ListFilter{RequestedByProfileID: "parent"})
	if err != nil || len(listed) != 1 || listed[0].ID != own.ID {
		t.Fatalf("blocked ListMine = %v, %v; want only req-1", ids(listed), err)
	}
	if listed, err = service.ListMine(ctx, parentViewer, ListFilter{}); err != nil || len(listed) != 2 {
		t.Fatalf("allowed ListMine = %v, %v; want the account's two", ids(listed), err)
	}

	if _, err := service.GetRequest(ctx, kidViewer, own.ID); err != nil {
		t.Fatalf("GetRequest own: %v", err)
	}
	if _, err := service.GetRequest(ctx, kidViewer, parents.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("GetRequest another profile's: err = %v, want ErrForbidden", err)
	}
	if _, err := service.Cancel(ctx, kidViewer, parents.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Cancel another profile's: err = %v, want ErrForbidden", err)
	}
	if _, err := service.GetRequest(ctx, parentViewer, own.ID); err != nil {
		t.Fatalf("allowed profile GetRequest another profile's: %v", err)
	}
}

// A profile that no longer exists fails closed, and a store error is
// reported rather than read as allowed.
func TestProfileBlockFailsClosed(t *testing.T) {
	ctx := context.Background()
	service := newProfileBlockService(newFakeStore())
	if _, err := service.Search(ctx, Viewer{UserID: 1, ProfileID: "gone"}, "fight", MediaTypeMovie, 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing profile: err = %v, want ErrForbidden", err)
	}

	broken := errors.New("store down")
	service.SetProfileStores(profileBlockStores{err: broken})
	if _, err := service.Search(ctx, parentViewer, "fight", MediaTypeMovie, 1); !errors.Is(err, broken) {
		t.Fatalf("store error: err = %v, want it reported", err)
	}
	if allowed, err := service.RequestCapabilityAllowed(ctx, parentViewer); !errors.Is(err, broken) || allowed {
		t.Fatalf("capability on a store error: allowed=%v err=%v", allowed, err)
	}
}

// A watchlist title is not requestable from a blocked profile.
func TestWatchlistStatesReportABlockedProfile(t *testing.T) {
	service := newProfileBlockService(newFakeStore())
	title := WatchlistTitle{MediaType: MediaTypeMovie, TMDBID: 949, Title: "Heat"}
	key := WatchlistKey{MediaType: MediaTypeMovie, TMDBID: 949}

	states, err := service.WatchlistRequestStates(context.Background(), kidViewer, []WatchlistTitle{title})
	if err != nil {
		t.Fatal(err)
	}
	if got := states[key]; got.Requestable || got.Reason != "blocked" {
		t.Fatalf("blocked profile state = %+v, want blocked", got)
	}
	states, err = service.WatchlistRequestStates(context.Background(), parentViewer, []WatchlistTitle{title})
	if err != nil {
		t.Fatal(err)
	}
	if got := states[key]; !got.Requestable {
		t.Fatalf("allowed profile state = %+v, want requestable", got)
	}
}

// The watchlist's titles outside the library come from TMDB, so a blocked
// profile loses them with discovery, and the feature status says so.
func TestBlockedProfileLosesWatchlistTitles(t *testing.T) {
	ctx := context.Background()
	service := newProfileBlockService(newFakeStore())

	if _, err := service.WatchlistCeiling(ctx, kidViewer); !errors.Is(err, ErrForbidden) {
		t.Fatalf("blocked WatchlistCeiling: err = %v, want ErrForbidden", err)
	}
	if _, err := service.WatchlistTitleDetail(ctx, kidViewer, MediaTypeMovie, 550); !errors.Is(err, ErrForbidden) {
		t.Fatalf("blocked WatchlistTitleDetail: err = %v, want ErrForbidden", err)
	}
	if _, err := service.WatchlistCeiling(ctx, parentViewer); err != nil {
		t.Fatalf("allowed WatchlistCeiling: %v", err)
	}

	for viewer, want := range map[Viewer]bool{kidViewer: true, parentViewer: false} {
		status, err := service.GetFeatureStatus(ctx, viewer)
		if err != nil || status.ProfileRequestsBlocked != want {
			t.Fatalf("%s: ProfileRequestsBlocked = %v, %v; want %v", viewer.ProfileID, status.ProfileRequestsBlocked, err, want)
		}
	}
}
