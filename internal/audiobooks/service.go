package audiobooks

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/audiobooks/abssocket"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/scanner"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SettingsReader is the minimal slice of the server-settings store that
// the audiobooks service needs. The production implementation is
// internal/catalog.ServerSettingsRepo (or whatever silo names that helper at
// wiring time); tests pass a fake.
type SettingsReader interface {
	GetString(ctx context.Context, key string) (string, error)
}

// Service is the audiobooks feature's top-level orchestrator. Sub-plan 1
// exposes only Enabled(); subsequent sub-plans hang additional methods
// off Service as new capabilities (scanner branches, ABS handlers, etc.)
// come online.
type Service struct {
	settings   SettingsReader
	ABSHandler *abs.Handler
}

// New constructs a Service. The constructor takes the dependencies it
// will actually use; current sub-plan needs only the settings reader.
func New(settings SettingsReader) *Service {
	return &Service{settings: settings}
}

// ABSHandlerDeps bundles the concrete silo dependencies needed to construct
// the ABS-compat abs.Handler. Pool, Items, and Files are required — the ABS
// handler dereferences MediaStore from many request paths and a nil store
// would panic on the first request. BuildABSHandler validates this at startup.
type ABSHandlerDeps struct {
	Pool           *pgxpool.Pool
	Items          *catalog.ItemRepository
	Files          *scanner.FileRepository
	Settings       *catalog.ServerSettingsRepo
	Auth           absAuthAdapter // see BuildABSHandler below
	AccessResolver abs.AccessResolver
	// Recs is the recommendations repository used to power the
	// /items/{id}/similar endpoint via embedding nearest-neighbor search.
	// Optional; when nil, ABSRecommender falls back to the shared-genre
	// SQL path. When the full chain is unwired (Recs and Pool both nil),
	// the ABS Recommender field stays nil and similar returns empty.
	Recs *recommendations.Repo
	// Detail resolves audiobook poster S3 paths into fully-qualified URLs
	// that ABS clients can fetch. Optional; when nil, /api/items/{id}/cover
	// 404s rather than redirecting to an unreachable storage path.
	Detail *catalog.DetailService
}

// absAuthAdapter is the narrow slice of internal/auth that BuildABSHandler
// needs. Defined as an interface to avoid an import cycle between the
// audiobooks package and internal/auth. main.go satisfies it with the
// concrete *auth.Service + *pgxpool.Pool pair via SiloCredValidator.
type absAuthAdapter = abs.ProfileCredentialValidator

// BuildABSHandler wires all production adapters and returns a ready-to-mount
// *abs.Handler. Callers pass the handler to service.ABSHandler and to the
// HTTP router (via api.Dependencies.ABSHandler).
func (s *Service) BuildABSHandler(deps ABSHandlerDeps) *abs.Handler {
	if deps.Items == nil || deps.Files == nil {
		panic("audiobooks.BuildABSHandler: Items and Files repositories are required")
	}
	mediaStore := &ABSMediaStore{
		Items: deps.Items,
		Files: deps.Files,
		Pool:  deps.Pool,
	}

	var tokenStore abs.TokenStore
	if deps.Pool != nil {
		tokenStore = &ABSSessionStore{Pool: deps.Pool}
	}

	var progressStore abs.ProgressStore
	if deps.Pool != nil {
		progressStore = &ABSProgressStore{Pool: deps.Pool}
	}

	var playbackSessionStore abs.ABSPlaybackSessionStore
	if deps.Pool != nil {
		playbackSessionStore = &ABSPlaybackSessionStore{Pool: deps.Pool}
	}

	var bookmarkStore abs.BookmarkStore
	if deps.Pool != nil {
		bookmarkStore = &ABSBookmarkStore{Pool: deps.Pool}
	}

	var collectionStore abs.CollectionStore
	if deps.Pool != nil {
		collectionStore = &ABSCollectionStore{Pool: deps.Pool}
	}

	var playlistStore abs.PlaylistStore
	if deps.Pool != nil {
		playlistStore = &ABSPlaylistStore{Pool: deps.Pool}
	}

	var smartCollectionStore abs.SmartCollectionStore
	if deps.Pool != nil {
		smartCollectionStore = &ABSSmartCollectionStore{Pool: deps.Pool}
	}

	var rssFeedStore abs.RSSFeedStore
	if deps.Pool != nil {
		rssFeedStore = &ABSRSSFeedStore{Pool: deps.Pool}
	}

	var configProvider abs.ConfigProvider
	if deps.Settings != nil {
		configProvider = &ABSConfigProvider{Settings: deps.Settings}
	}

	// Socket.io server: secretFn reads the JWT secret at connection time so
	// a secret-rotate takes effect without a restart.
	var socketServer *abssocket.Server
	if configProvider != nil && tokenStore != nil {
		secretFn := func() []byte {
			secret, _ := configProvider.JWTSecret(context.Background())
			return secret
		}
		var tokenValidator abssocket.TokenValidator
		if ts := tokenStore; ts != nil {
			tokenValidator = func(ctx context.Context, jti string) (bool, error) {
				tok, err := ts.GetTokenByJTI(ctx, jti)
				if err != nil {
					return false, err
				}
				return tok.RevokedAt != nil, nil
			}
		}
		socketServer = abssocket.New(secretFn, tokenValidator, nil, nil)
	}

	h := abs.New(abs.Dependencies{
		MediaStore:           mediaStore,
		TokenStore:           tokenStore,
		CredValidator:        deps.Auth,
		AccessResolver:       deps.AccessResolver,
		Config:               configProvider,
		Publisher:            nil, // EventPublisher: no-op stub; Socket.io handles realtime
		Recommender:          buildABSRecommender(deps),
		ProgressStore:        progressStore,
		PlaybackSessionStore: playbackSessionStore,
		BookmarkStore:        bookmarkStore,
		CollectionStore:      collectionStore,
		PlaylistStore:        playlistStore,
		SmartCollectionStore: smartCollectionStore,
		RSSFeedStore:         rssFeedStore,
		SocketIO:             socketServer,
		CoverResolver: func(ctx context.Context, path, variant string) string {
			if deps.Detail == nil {
				return ""
			}
			return deps.Detail.PresignURL(ctx, path, variant)
		},
	})
	s.ABSHandler = h
	return h
}

// buildABSRecommender returns the abs.Recommender adapter when at least one
// of its data sources is available. Nil result means /items/{id}/similar
// keeps returning an empty list (the route's documented degradation path).
func buildABSRecommender(deps ABSHandlerDeps) abs.Recommender {
	if deps.Pool == nil && deps.Recs == nil {
		return nil
	}
	return &ABSRecommender{Pool: deps.Pool, Recs: deps.Recs}
}

// Enabled reports whether the audiobooks feature flag (set by
// 160_audiobooks_feature_flag and toggled by operators) is currently true.
// Any value other than the literal string "true" reads as false; this matches how silo
// treats other boolean server_settings rows.
func (s *Service) Enabled(ctx context.Context) (bool, error) {
	if s == nil || s.settings == nil {
		return false, nil
	}
	value, err := s.settings.GetString(ctx, "audiobooks.enabled")
	if err != nil {
		return false, fmt.Errorf("read audiobooks.enabled: %w", err)
	}
	return value == "true", nil
}
