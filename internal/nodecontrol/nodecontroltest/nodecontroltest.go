// Package nodecontroltest runs the core's node control endpoints in process
// for tests of a transcode node.
package nodecontroltest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// PathGrants is a grant issuer that mints a source's own path as its grant,
// for node tests whose source opener opens the grant as a path. It verifies
// nothing.
type PathGrants struct{}

func (PathGrants) ForFile(_ context.Context, _ int, file *models.MediaFile) (string, error) {
	return file.FilePath, nil
}

func (PathGrants) ForTheme(_ context.Context, _ int, _ int64, path string) (string, error) {
	return path, nil
}

func (PathGrants) Verify(string) (nodesource.Grant, error) {
	return nodesource.Grant{}, nodesource.ErrInvalidGrant
}

func (PathGrants) Renew(nodesource.Grant) (string, error) { return "", nodesource.ErrInvalidGrant }

// ActiveCatalog is a media file and theme catalog that has every source, for
// node tests that do not exercise the catalog check.
type ActiveCatalog struct{}

func (ActiveCatalog) IsActiveFile(context.Context, int, string) (bool, error)    { return true, nil }
func (ActiveCatalog) IsActiveTheme(context.Context, int64, string) (bool, error) { return true, nil }

// singleKey is the key source of a core that knows one node.
type singleKey struct {
	id  int
	key string
}

func (k singleKey) NodeKey(id int) (string, error) {
	if id != k.id {
		return "", errors.New("unknown node")
	}
	return k.key, nil
}

func (k singleKey) ReloadNodeKey(id int) (string, error) { return k.NodeKey(id) }

// Resolver serves the core's resolve endpoint for opts behind RequireNode, at
// the path the API router mounts it on, and returns the client of node nodeID
// signing with a key issued for it. Without opts.Grants it mints PathGrants,
// and without opts.Files or opts.Themes its catalog has every source
// (ActiveCatalog). The core closes with the test.
func Resolver(t testing.TB, nodeID int, opts nodecontrol.Options) *nodecontrol.Client {
	t.Helper()
	if opts.Grants == nil {
		opts.Grants = PathGrants{}
	}
	if opts.Files == nil {
		opts.Files = ActiveCatalog{}
	}
	if opts.Themes == nil {
		opts.Themes = ActiveCatalog{}
	}
	key, err := nodepool.GenerateNodeKey(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	resolve := nodecontrol.NewServer(opts).HandleResolve
	core := httptest.NewServer(nodecontrol.RequireNode(singleKey{id: nodeID, key: key})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != nodecontrol.PathResolve {
			http.NotFound(w, r)
			return
		}
		resolve(w, r)
	})))
	t.Cleanup(core.Close)
	client, err := nodecontrol.NewClient(core.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// SourceCore serves the core's source endpoints (open and source bytes) for
// opts behind RequireNode at the paths the API router mounts them on, and
// returns the core's URL and a key issued for node nodeID. The core closes
// with the test.
func SourceCore(t testing.TB, nodeID int, opts nodecontrol.Options) (coreURL, nodeKey string) {
	t.Helper()
	key, err := nodepool.GenerateNodeKey(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	server := nodecontrol.NewServer(opts)
	core := httptest.NewServer(nodecontrol.RequireNode(singleKey{id: nodeID, key: key})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == nodesource.PathOpen:
			server.HandleSourceOpen(w, r)
		case (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Path == nodesource.PathSources:
			server.HandleSource(w, r)
		default:
			http.NotFound(w, r)
		}
	})))
	t.Cleanup(core.Close)
	return core.URL, key
}
