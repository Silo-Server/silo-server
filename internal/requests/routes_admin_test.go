package requests

import (
	"context"
	"errors"
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
