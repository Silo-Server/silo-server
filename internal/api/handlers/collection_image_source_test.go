package handlers

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/netguard"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// countingImageServer serves a small JPEG on loopback and counts requests.
func countingImageServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	poster := testCollectionPosterJPEG(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(poster)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

// closedLoopbackURL returns a loopback URL with nothing listening on it.
func closedLoopbackURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return "http://" + addr + "/poster.jpg"
}

// A poster_source_url any profile supplies reaches public addresses only.
// Loopback, private, link-local and IPv4-mapped forms are refused before a
// connection is made, and every refusal is the same error, so an open port, a
// closed port and an unrouted LAN address all answer alike.
func TestPersonalPosterSourceRefusesLocalAddresses(t *testing.T) {
	server, hits := countingImageServer(t)
	port := strconv.Itoa(server.Listener.Addr().(*net.TCPAddr).Port)
	client := NewCollectionHandler(nil).HTTPClient

	for _, source := range []string{
		server.URL + "/poster.jpg",
		closedLoopbackURL(t),
		"http://localhost:" + port + "/poster.jpg",
		"http://[::ffff:127.0.0.1]:" + port + "/poster.jpg",
		"http://[::1]:" + port + "/poster.jpg",
		"http://10.20.30.40/poster.jpg",
		"http://192.168.1.10/poster.jpg",
		"http://100.64.0.10/poster.jpg",
		"http://169.254.10.10/poster.jpg",
		"http://[fe80::1]/poster.jpg",
		"http://0.0.0.0:" + port + "/poster.jpg",
	} {
		data, err := downloadCollectionImageURL(t.Context(), client, source)
		if !errors.Is(err, errCollectionImageSourceNotAllowed) || err.Error() != errCollectionImageSourceNotAllowed.Error() {
			t.Errorf("download %s = %d bytes, %v; want the uniform refusal", source, len(data), err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("loopback server received %d requests, want 0", n)
	}
}

// Every personal surface that takes a poster URL answers an open loopback
// port and a closed one with the same error, and never connects: the /api/v1
// create and update, the /api/v2 create, and the /api/v2 poster operation.
func TestPersonalPosterSourceRefusalIsUniformDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "owner", Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	h := NewCollectionHandler(provider)
	server, hits := countingImageServer(t)
	open, closed := server.URL+"/poster.jpg", closedLoopbackURL(t)
	create := func(req PersonalCollectionCreateRequest) (PersonalCollectionView, error) {
		return h.CreatePersonalCollection(ctx, PersonalCollectionCreateCommand{UserID: f.account, ProfileID: "owner", Request: req})
	}
	existing := func(name string) string {
		c, err := create(PersonalCollectionCreateRequest{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}

	surfaces := map[string]func(source string) error{
		"create": func(source string) error {
			_, err := create(PersonalCollectionCreateRequest{Name: "Poster", PosterSourceURL: source})
			return err
		},
		"v1 update": func(source string) error {
			_, err := h.UpdatePersonalCollection(ctx, PersonalCollectionUpdateCommand{UserID: f.account, ProfileID: "owner", CollectionID: existing("Update"), Request: PersonalCollectionUpdateRequest{PosterSourceURL: &source}})
			return err
		},
		"poster source": func(source string) error {
			_, err := h.SetPersonalCollectionPosterSource(ctx, f.account, "owner", existing("Source"), source)
			return err
		},
	}
	for name, call := range surfaces {
		t.Run(name, func(t *testing.T) {
			openErr, closedErr := call(open), call(closed)
			if openErr == nil || closedErr == nil {
				t.Fatalf("open = %v, closed = %v; want both refused", openErr, closedErr)
			}
			if openErr.Error() != closedErr.Error() {
				t.Fatalf("open port answered %q, closed port %q; want the same answer", openErr, closedErr)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("loopback server received %d requests, want 0", n)
	}
}

// redirectFirstHop answers requests to one public-looking host with a
// redirect, without dialing, and sends every other request through next.
type redirectFirstHop struct {
	host, location string
	next           http.RoundTripper
}

func (rt redirectFirstHop) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != rt.host {
		return rt.next.RoundTrip(req)
	}
	return &http.Response{
		StatusCode: http.StatusFound,
		Header:     http.Header{"Location": []string{rt.location}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// A public URL that redirects to the server's own network is refused at the
// redirect hop: the guarded transport checks every address it dials.
func TestPersonalPosterSourceRefusesRedirectToLocalAddress(t *testing.T) {
	server, hits := countingImageServer(t)
	guarded := NewCollectionHandler(nil).HTTPClient
	client := &http.Client{
		Timeout:   guarded.Timeout,
		Transport: redirectFirstHop{host: "images.example", location: server.URL + "/poster.jpg", next: guarded.Transport},
	}

	_, err := downloadCollectionImageURL(t.Context(), client, "http://images.example/poster.jpg")
	if !errors.Is(err, errCollectionImageSourceNotAllowed) {
		t.Fatalf("redirect to loopback = %v, want the uniform refusal", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("loopback server received %d requests, want 0", n)
	}
}

// Admin collection artwork is trusted with the local network, as an image an
// admin applies to an item is; blocked ranges stay refused.
func TestAdminCollectionArtworkSourceReachesLocalNetwork(t *testing.T) {
	server, hits := countingImageServer(t)
	h := NewLibraryCollectionHandler(nil, nil, nil, nil)
	ctx := adminCollectionImageContext(t.Context())

	data, err := downloadCollectionImageURL(ctx, h.httpClient, server.URL+"/poster.jpg")
	if err != nil || len(data) == 0 {
		t.Fatalf("admin download from loopback = %d bytes, %v", len(data), err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("loopback server received %d requests, want 1", n)
	}
	if _, err := downloadCollectionImageURL(ctx, h.httpClient, "http://169.254.10.10/poster.jpg"); !errors.Is(err, errCollectionImageSourceNotAllowed) {
		t.Fatalf("admin download from link-local = %v, want the uniform refusal", err)
	}
	if _, err := downloadCollectionImageURL(t.Context(), h.httpClient, server.URL+"/poster.jpg"); !errors.Is(err, errCollectionImageSourceNotAllowed) {
		t.Fatalf("download without the admin context = %v, want the uniform refusal", err)
	}
}

// A missing client fails closed instead of falling back to an unguarded one.
func TestCollectionImageDownloadsNeedAClient(t *testing.T) {
	server, hits := countingImageServer(t)
	if _, err := downloadCollectionImageURL(t.Context(), nil, server.URL+"/poster.jpg"); err == nil {
		t.Fatal("download without a client succeeded")
	}
	var h CollectionHandler
	if _, err := downloadCollectionImageURL(t.Context(), h.HTTPClient, server.URL+"/poster.jpg"); err == nil {
		t.Fatal("download with a zero-value handler's client succeeded")
	}
	if _, err := (collectionCollageComposer{}).fetchImage(t.Context(), server.URL+"/a.png"); err == nil {
		t.Fatal("collage fetch without a client succeeded")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server received %d requests, want 0", n)
	}
}

// Collage sources are catalog posters the server resolves, often to artwork
// storage on its own network, so the default personal collage generator still
// fetches them from loopback. Blocked ranges stay refused.
func TestPersonalCollageFetchesLocalArtworkStorage(t *testing.T) {
	var poster bytes.Buffer
	if err := png.Encode(&poster, image.NewNRGBA(image.Rect(0, 0, 20, 30))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(poster.Bytes())
	}))
	t.Cleanup(server.Close)

	detail := &catalog.DetailService{}
	detail.SetImageResolver(collageSourceResolver{})
	gen := NewPersonalCollectionCollageGenerator(blobstoretest.New(), detail, nil)
	if gen == nil {
		t.Fatal("no collage generator")
	}

	const key = "0123456789abcdef"
	if _, _, err := gen.ComposeCollectionCollage(t.Context(), "c1", key, []string{server.URL + "/a.png", server.URL + "/b.png"}); err != nil {
		t.Fatalf("compose from loopback storage: %v", err)
	}
	_, _, err := gen.ComposeCollectionCollage(t.Context(), "c1", key, []string{server.URL + "/a.png", "http://169.254.10.10/b.png"})
	if !errors.Is(err, netguard.ErrBlockedDestination) {
		t.Fatalf("collage with a link-local source = %v, want ErrBlockedDestination", err)
	}
}
