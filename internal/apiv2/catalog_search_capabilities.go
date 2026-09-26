package apiv2

import (
	"context"
	"net/http"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type CatalogSearchCapabilities struct {
	Capability
	PeopleMediaScope      bool   `json:"people_media_scope,omitzero" doc:"People search accepts media_scope and filters credits by viewer access"`
	PersonPrefetch        bool   `json:"person_prefetch,omitzero" doc:"Person reads accept prefetch=true for speculative reads that do not queue a provider refresh"`
	Provider              string `json:"provider,omitempty" enum:"postgres,meilisearch"`
	ResultWindowLimit     int    `json:"result_window_limit,omitzero" doc:"Maximum candidates in a Meilisearch ranked window; absent for PostgreSQL live queries"`
	SessionTTLSeconds     int    `json:"session_ttl_seconds,omitzero" doc:"Fixed Meilisearch ranking-session lifetime; requests do not extend it"`
	MaxSessionsPerAccount int    `json:"max_sessions_per_account,omitzero" doc:"Oldest ranking sessions expire when this retention bound is exceeded"`
}

type CatalogSearchCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         CatalogSearchCapabilities
}

func registerCatalogSearchCapabilities(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/catalog/search/capabilities", "getCatalogSearchCapabilities", "catalog", "Search continuation provider, result window, and session lifetime."), Class: ClassProfileScoped, ServiceBacked: true},
		func(ctx context.Context, _ *CapabilityInput) (*CatalogSearchCapabilitiesOutput, error) {
			service, ok := reg.deps.CatalogBrowse.(interface {
				SearchContinuationCapabilities(context.Context) (catalogpkg.SearchContinuationCapabilities, error)
			})
			if !ok {
				return &CatalogSearchCapabilitiesOutput{Body: CatalogSearchCapabilities{Capability: Capability{State: StateNotConfigured}}}, nil
			}
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			result, err := service.SearchContinuationCapabilities(ctx)
			if err != nil {
				return nil, catalogProblem(err, "query.source")
			}
			return &CatalogSearchCapabilitiesOutput{Body: CatalogSearchCapabilities{
				Capability: Capability{State: StateAvailable}, Provider: result.Provider,
				PeopleMediaScope:  reg.deps.People != nil && reg.deps.CatalogAccess != nil,
				PersonPrefetch:    reg.deps.People != nil,
				ResultWindowLimit: result.ResultWindowLimit, SessionTTLSeconds: result.SessionTTLSeconds, MaxSessionsPerAccount: result.MaxSessionsPerAccount,
			}}, nil
		})
}
