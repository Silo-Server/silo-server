package autoscan

import (
	"context"
	"errors"
	"testing"
)

// fakeArrClient implements both RootFolderClient and ArrStatusProbe for the
// connection-test and suggester service tests.
type fakeArrClient struct {
	roots     []string
	statusErr error
}

func (f *fakeArrClient) RootFolders(context.Context, string, string) ([]string, error) {
	return f.roots, nil
}

func (f *fakeArrClient) SystemStatus(context.Context, string, string) (string, error) {
	return "", f.statusErr
}

type fakeFolderLister struct{ paths []string }

func (f fakeFolderLister) ListFolderPaths(context.Context) ([]string, error) {
	return f.paths, nil
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

func TestSuggestRewritesMatchesArrRootsToSiloFolders(t *testing.T) {
	store := &fakeStore{
		sources: []Source{
			{ID: "src-1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1")},
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
