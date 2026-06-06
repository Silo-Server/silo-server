package autoscan

import (
	"context"
	"errors"
	"testing"
)

// fakeArrClient implements both RootFolderClient and ArrStatusProbe for the
// connection-test and suggester service tests.
type fakeArrClient struct {
	roots      []string
	rootsErr   error
	version    string
	statusErr  error
	gotBaseURL string
	gotAPIKey  string
}

func (f *fakeArrClient) RootFolders(_ context.Context, baseURL, apiKey string) ([]string, error) {
	f.gotBaseURL, f.gotAPIKey = baseURL, apiKey
	return f.roots, f.rootsErr
}

func (f *fakeArrClient) SystemStatus(_ context.Context, baseURL, apiKey string) (string, error) {
	f.gotBaseURL, f.gotAPIKey = baseURL, apiKey
	return f.version, f.statusErr
}

type fakeFolderLister struct{ paths []string }

func (f fakeFolderLister) ListFolderPaths(context.Context) ([]string, error) {
	return f.paths, nil
}

func TestTestConnectionOK(t *testing.T) {
	arr := &fakeArrClient{version: "4.1.2.3"}
	svc := &Service{connres: passthroughConnRes{}, rootFolders: arr}

	res, err := svc.TestConnection(context.Background(), Connection{BaseURL: "http://radarr:7878", APIKeyRef: "k"})
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if !res.OK || res.Version != "4.1.2.3" {
		t.Fatalf("expected ok with version, got %+v", res)
	}
}

func TestTestConnectionProbeFailureIsPayload(t *testing.T) {
	arr := &fakeArrClient{statusErr: errors.New("arr: HTTP 401")}
	svc := &Service{connres: passthroughConnRes{}, rootFolders: arr}

	res, err := svc.TestConnection(context.Background(), Connection{BaseURL: "http://radarr:7878"})
	if err != nil {
		t.Fatalf("probe failure must not be a method error: %v", err)
	}
	if res.OK || res.Err == "" {
		t.Fatalf("expected ok=false with error, got %+v", res)
	}
}

func TestTestConnectionNoProbeConfigured(t *testing.T) {
	svc := &Service{connres: passthroughConnRes{}}
	if _, err := svc.TestConnection(context.Background(), Connection{BaseURL: "http://x"}); err == nil {
		t.Fatal("expected error when no probe configured")
	}
}

func TestSuggestRewritesMatchesArrRootsToSiloFolders(t *testing.T) {
	store := &fakeStore{
		sources: []Source{
			{ID: "src-1", InstallationID: 1, CapabilityID: "arr", ConnectionID: strptr("c1")},
		},
	}
	arr := &fakeArrClient{roots: []string{"/data/tv"}}
	folders := fakeFolderLister{paths: []string{"/mnt/media/tv"}}
	svc := &Service{store: store, connres: passthroughConnRes{}, rootFolders: arr, folders: folders}

	sugg, err := svc.SuggestRewrites(context.Background(), "src-1")
	if err != nil {
		t.Fatalf("SuggestRewrites: %v", err)
	}
	if len(sugg.Proposed) != 1 || sugg.Proposed[0].From != "/data/tv" || sugg.Proposed[0].To != "/mnt/media/tv" {
		t.Fatalf("unexpected proposals: %+v", sugg.Proposed)
	}
}

func TestSuggestRewritesNoConnection(t *testing.T) {
	store := &fakeStore{
		sources: []Source{{ID: "src-1", InstallationID: 1, CapabilityID: "arr", ConnectionID: nil}},
	}
	svc := &Service{store: store, connres: passthroughConnRes{}, rootFolders: &fakeArrClient{}, folders: fakeFolderLister{}}

	if _, err := svc.SuggestRewrites(context.Background(), "src-1"); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("expected ErrNoConnection, got %v", err)
	}
}
