package requests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

var routeAdmin = Viewer{UserID: 1, IsAdmin: true}

func fieldErrors(t *testing.T, err error) map[string]string {
	t.Helper()
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	return verr.FieldErrors
}

func TestRouteAdministrationRequiresAdmin(t *testing.T) {
	svc := newTestService(newFakeStore())
	member := Viewer{UserID: 2}
	if _, err := svc.ListRoutesAdmin(context.Background(), member); !errors.Is(err, ErrForbidden) {
		t.Fatalf("list: err = %v", err)
	}
	if _, err := svc.CreateRoute(context.Background(), member, Route{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("create: err = %v", err)
	}
	if _, err := svc.PreviewRoute(context.Background(), member, MediaTypeMovie, 1, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("preview: err = %v", err)
	}
}

func TestPreviewRoute(t *testing.T) {
	store := routingStore(RoutingFacts{})
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{detail: &tmdb.MediaDetail{ID: 129, KeywordIDs: []int{210024}}})

	preview, err := svc.PreviewRoute(context.Background(), routeAdmin, MediaTypeMovie, 129, 7)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !preview.Facts.Anime || len(preview.Tiers) != 2 {
		t.Fatalf("preview = %+v", preview)
	}
	hd, uhd := preview.Tiers[0], preview.Tiers[1]
	if hd.RouteName != "Anime" || hd.IntegrationName != "radarr-anime" || uhd.RouteName != "Everything else" {
		t.Fatalf("tiers = %+v", preview.Tiers)
	}
}

func TestPreviewRouteTellsMissingFromUnreachable(t *testing.T) {
	store := routingStore(RoutingFacts{})
	missing := newTestServiceWithTMDB(store, &fakeTMDBClient{detailErr: fmt.Errorf("lookup: %w", tmdb.ErrNotFound)})
	if _, err := missing.PreviewRoute(context.Background(), routeAdmin, MediaTypeMovie, 1, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing title: err = %v, want ErrNotFound", err)
	}
	down := newTestServiceWithTMDB(store, &fakeTMDBClient{detailErr: errors.New("tmdb: server error 503 after 3 retries")})
	if _, err := down.PreviewRoute(context.Background(), routeAdmin, MediaTypeMovie, 1, 0); !errors.Is(err, ErrIntegrationUnreachable) {
		t.Fatalf("TMDB down: err = %v, want ErrIntegrationUnreachable", err)
	}
}

// Switching a server to the other type would fail every request its routes
// send it, so the switch is refused while a route uses the server, and the
// preview flags a server already switched.
func TestServerTypeSwitchKeepsRoutesWorking(t *testing.T) {
	store := routingStore(RoutingFacts{})
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{detail: &tmdb.MediaDetail{ID: 129, KeywordIDs: []int{210024}}})

	switched := store.integrations[2] // radarr-anime, which the movie rule "Anime" sends to
	switched.PluginConfig = map[string]any{"service_kind": "sonarr", "root_folder": "/tv"}
	_, err := svc.UpdateIntegration(context.Background(), routeAdmin, switched)
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.FieldErrors["plugin_config.service_kind"], "Anime") {
		t.Fatalf("switch: err = %v, want a service_kind field error naming the route", err)
	}

	store.integrations[2] = switched
	preview, err := svc.PreviewRoute(context.Background(), routeAdmin, MediaTypeMovie, 129, 7)
	if err != nil {
		t.Fatal(err)
	}
	if hd := preview.Tiers[0]; !strings.Contains(hd.Reason, "a sonarr server") {
		t.Fatalf("hd tier = %+v, want the type mismatch noted", hd)
	}
}

// A route cannot send a media type to a server that does not take it, and a
// server a route uses cannot stop taking the route's media type.
func TestRoutesRespectSupportedMediaTypes(t *testing.T) {
	store := routingStore(RoutingFacts{})
	seriesOnly := routerInst("generic-series")
	seriesOnly.SupportedMediaTypes = []string{"series"}
	store.integrations = append(store.integrations, seriesOnly)
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{})

	route := Route{MediaType: MediaTypeMovie, Name: "Anime", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true)}, HD: RouteDestination{IntegrationID: "generic-series"}}
	if fields := fieldErrors(t, svc.validateRoute(context.Background(), &route)); !strings.Contains(fields["hd.integration_id"], "does not take movies") {
		t.Fatalf("field errors = %v, want hd.integration_id refused", fields)
	}

	narrowed := store.integrations[2] // radarr-anime, which the movie rule "Anime" sends to
	narrowed.SupportedMediaTypes = []string{"series"}
	_, err := svc.UpdateIntegration(context.Background(), routeAdmin, narrowed)
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.FieldErrors["supported_media_types"], "Anime") {
		t.Fatalf("narrow: err = %v, want a supported_media_types field error naming the route", err)
	}
}

// HD versions go only to servers not marked 4K, and 4K versions only to
// servers marked 4K; a server can't flip its 4K switch while routes rely on
// the other.
func TestRoutesKeepHDAnd4KServersApart(t *testing.T) {
	store := routingStore(RoutingFacts{})
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{})
	ctx := context.Background()

	route := Route{MediaType: MediaTypeMovie, Name: "Anime", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true)},
		HD:         RouteDestination{IntegrationID: "radarr-4k"}, UHD: RouteDestination{IntegrationID: "radarr-anime"}}
	fields := fieldErrors(t, svc.validateRoute(ctx, &route))
	if !strings.Contains(fields["hd.integration_id"], "marked 4K") {
		t.Fatalf("hd error = %q, want the 4K server refused for HD", fields["hd.integration_id"])
	}
	if !strings.Contains(fields["uhd.integration_id"], "isn't marked 4K") {
		t.Fatalf("uhd error = %q, want the HD server refused for 4K", fields["uhd.integration_id"])
	}

	route.HD.IntegrationID, route.UHD.IntegrationID = "radarr-anime", "radarr-4k"
	if err := svc.validateRoute(ctx, &route); err != nil {
		t.Fatalf("matching tiers: %v", err)
	}

	// Everything else sends 4K versions to radarr-4k; turning its switch off
	// would leave that 4K version on an HD server.
	unmarked := store.integrations[1]
	unmarked.PluginConfig = map[string]any{"service_kind": "radarr", "root_folder": "/movies", "is_4k": false}
	_, err := svc.UpdateIntegration(ctx, routeAdmin, unmarked)
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.FieldErrors["plugin_config.is_4k"], "Everything else") {
		t.Fatalf("unmark: err = %v, want an is_4k field error naming the route", err)
	}

	// The Anime rule sends HD versions to radarr-anime, so it can't be marked 4K.
	marked := store.integrations[2]
	marked.PluginConfig = map[string]any{"service_kind": "radarr", "root_folder": "/movies", "is_4k": true}
	_, err = svc.UpdateIntegration(ctx, routeAdmin, marked)
	if !errors.As(err, &verr) || !strings.Contains(verr.FieldErrors["plugin_config.is_4k"], "can't be marked 4K") {
		t.Fatalf("mark: err = %v, want an is_4k field error", err)
	}
}

// A server of another plugin (Seerr) has no 4K switch of ours and takes both
// versions, so routing can send it either.
func TestRoutesSendEitherVersionToSelfRoutedServers(t *testing.T) {
	store := routingStore(RoutingFacts{})
	seerr := routerInst("seerr")
	seerr.CapabilityID, seerr.PluginConfig = "seerr", map[string]any{}
	seerr.SupportedMediaTypes = []string{"movie"}
	store.integrations = append(store.integrations, seerr)
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{})

	route := Route{MediaType: MediaTypeMovie, Name: "Anime", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true)},
		HD:         RouteDestination{IntegrationID: "seerr"}, UHD: RouteDestination{IntegrationID: "seerr"}}
	if err := svc.validateRoute(context.Background(), &route); err != nil {
		t.Fatalf("Seerr for both versions: %v", err)
	}
}

// Standard pauses the stored routes and the editor hides them, so a server's
// 4K switch can change there even when a paused route sends it the other
// version.
func TestStandardRoutingLeavesThe4KSwitchFree(t *testing.T) {
	store := &modeStore{fakeStore: routingStore(RoutingFacts{}), mode: RoutingStandard}
	svc := modeService(store, &fakeTMDBClient{})
	ctx := context.Background()

	// The paused Anime rule sends HD versions to radarr-anime.
	marked := store.integrations[2]
	marked.PluginConfig = map[string]any{"service_kind": "radarr", "root_folder": "/movies", "is_4k": true}
	if _, err := svc.UpdateIntegration(ctx, routeAdmin, marked); err != nil {
		t.Fatalf("mark under Standard: %v", err)
	}

	store.mode = RoutingAdvanced
	unmarked := store.integrations[1]
	unmarked.PluginConfig = map[string]any{"service_kind": "radarr", "root_folder": "/movies", "is_4k": false}
	_, err := svc.UpdateIntegration(ctx, routeAdmin, unmarked)
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.FieldErrors["plugin_config.is_4k"] == "" {
		t.Fatalf("unmark under Advanced: err = %v, want an is_4k field error", err)
	}
}
