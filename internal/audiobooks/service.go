package audiobooks

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/audiobooks/abssocket"
	"github.com/Silo-Server/silo-server/internal/catalog"
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
// the ABS-compat abs.Handler. All fields except Pool are optional; the handler
// degrades gracefully when any of them is nil.
type ABSHandlerDeps struct {
	Pool      *pgxpool.Pool
	Items     *catalog.ItemRepository
	Files     *scanner.FileRepository
	Settings  *catalog.ServerSettingsRepo
	Auth      absAuthAdapter // see BuildABSHandler below
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
	var mediaStore abs.MediaStore
	if deps.Items != nil && deps.Files != nil {
		mediaStore = &ABSMediaStore{
			Items: deps.Items,
			Files: deps.Files,
			Pool:  deps.Pool,
		}
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
		Config:               configProvider,
		Publisher:            nil, // EventPublisher: no-op stub; Socket.io handles realtime
		Recommender:          nil, // Recommender: stub returning empty
		ProgressStore:        progressStore,
		PlaybackSessionStore: playbackSessionStore,
		SocketIO:             socketServer,
	})
	s.ABSHandler = h
	return h
}

// Enabled reports whether the audiobooks feature flag (set by migration
// 142 and toggled by operators) is currently true. Any value other than
// the literal string "true" reads as false; this matches how silo
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
