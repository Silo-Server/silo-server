package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mdblist"
)

// TestMDBListConfiguredFollowsTheSearchClient checks that the capability
// answer agrees with what MDBList search does: no client or no API key means
// search reports configured=false, and an admin settings change applies
// without a restart.
func TestMDBListConfiguredFollowsTheSearchClient(t *testing.T) {
	if NewUserCollectionImportHandler(nil, nil, nil, nil, nil, nil).MDBListConfigured() {
		t.Fatal("handler without an MDBList client reports search configured")
	}
	client := mdblist.NewClient("", nil)
	h := NewUserCollectionImportHandler(nil, nil, nil, nil, client, nil)
	if h.MDBListConfigured() {
		t.Fatal("client without an API key reports search configured")
	}
	if view, err := h.SearchMDBList(context.Background(), "top"); err != nil || view.Configured {
		t.Fatalf("search without an API key = %+v, %v", view, err)
	}
	client.SetAPIKey("key")
	if !h.MDBListConfigured() {
		t.Fatal("client with an API key reports search not configured")
	}
	client.SetAPIKey("  ")
	if h.MDBListConfigured() {
		t.Fatal("clearing the API key left search configured")
	}
}
