package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerproxy"
	"github.com/Silo-Server/silo-server/internal/workerregistration"
	"github.com/Silo-Server/silo-server/internal/workersource"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type coreSourceAdapter struct{ authority *workersource.Authority }

func (a coreSourceAdapter) Acquire(ctx context.Context, node int, work string, generation uint64, path, kind string) (workerwire.Source, error) {
	return a.authority.Acquire(ctx, node, work, generation, path, kind)
}
func (a coreSourceAdapter) Release(ctx context.Context, node int, work string, generation uint64) error {
	return a.authority.RevokeWork(ctx, node, work, generation)
}
func (a coreSourceAdapter) Renew(ctx context.Context, node int, work string, generation uint64, grant string) (workerwire.Source, error) {
	return a.authority.Renew(ctx, node, work, generation, grant)
}

// workerCoreOrigin canonicalizes the configured public URL. Admin settings keep
// the operator's spelling, including host case and an explicit default port,
// and every worker protocol component compares the canonical origin exactly.
func workerCoreOrigin(publicURL string) (string, bool, error) {
	raw := strings.TrimRight(publicURL, "/")
	allowHTTP := strings.HasPrefix(strings.ToLower(raw), "http://")
	origin, err := workeridentity.ValidateOrigin(raw, allowHTTP)
	return origin, allowHTTP, err
}

func initializeHTTPWorkers(ctx context.Context, pool *pgxpool.Pool, cipher *secret.Cipher, rdb *redis.Client, cfg *config.Config, contentAuthority *workerContentAuthority, live ...func() *config.Config) (http.Handler, error) {
	liveConfig := func() *config.Config { return cfg }
	if len(live) > 0 && live[0] != nil {
		liveConfig = live[0]
	}
	if pool == nil {
		return nil, nil
	}
	if rdb == nil {
		// An enrolled worker must never receive the legacy source-path protocol,
		// and its work cannot be fenced without the shared store.
		configured, err := hasHTTPWorkers(ctx, pool)
		if err != nil {
			return nil, err
		}
		if configured {
			return nil, errors.New("standalone workers are enrolled but Redis is unavailable")
		}
		return nil, nil
	}
	origin, allowHTTP, err := workerCoreOrigin(cfg.Server.PublicURL)
	if err != nil {
		// Legacy deployments may use a prefixed public URL. The standalone
		// protocol needs an exact origin, but must not break an installation
		// that has no enrolled standalone worker.
		configured, lookupErr := hasHTTPWorkers(ctx, pool)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if !configured {
			return nil, nil
		}
		return nil, err
	}
	repo := workerregistration.New(pool, cipher)
	key, err := repo.SigningKey(ctx)
	if err != nil {
		return nil, err
	}
	registry := &coreWorkerRegistry{repo: repo}
	if err := registry.refresh(ctx); err != nil {
		return nil, err
	}
	store := workercontrolcore.NewRedisStore(rdb, "")
	recipes := noderecipe.NewStore(rdb, 0)
	deny := playback.NewStreamDeny(rdb)
	var service *workercontrolcore.Service
	sources, err := workersource.New(workersource.Options{Origin: origin, Store: workersource.NewRedisStore(rdb, ""), PathApprover: func(ctx context.Context, path, kind string) error {
		var active bool
		var err error
		switch kind {
		case "media":
			err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM media_files m JOIN media_folders f ON f.id=m.media_folder_id WHERE m.file_path=$1 AND m.missing_since IS NULL AND f.enabled)", path).Scan(&active)
		case "theme":
			err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM item_theme_songs t JOIN media_folders f ON f.id=t.media_folder_id WHERE t.file_path=$1 AND f.enabled)", path).Scan(&active)
		default:
			return workersource.ErrDenied
		}
		if err != nil {
			return err
		}
		if !active {
			return workersource.ErrDenied
		}
		return nil
	}, Active: func(ctx context.Context, node int, work string, generation uint64) bool {
		return service != nil && service.WorkActive(ctx, node, work, generation)
	}, VerifyRequest: func(request *http.Request, node int) error {
		if service == nil {
			return workersource.ErrUnavailable
		}
		return service.VerifyRequest(request, node, workeridentity.DomainSourceRequest)
	}})
	if err != nil {
		return nil, err
	}
	reportSink := &workerReportSink{rdb: rdb, nodes: registry, recipes: recipes, assignments: store, service: func() *workercontrolcore.Service { return service }}
	outputAuthorities := workercontrolcore.NewRedisOutputAuthorities(rdb, "")
	service, err = workercontrolcore.New(workercontrolcore.Options{Origin: origin, AllowHTTP: allowHTTP, PrivateKey: key, Nodes: registry, Store: store, OutputAuthorities: outputAuthorities, Sources: coreSourceAdapter{sources}, SourceHandler: sources.Handler(), RecipeStore: recipes, VerifyToken: func(ctx context.Context, token string) (*streamtoken.Claims, error) {
		return verifyWorkerOutputToken(ctx, pool, contentAuthority, token, liveConfig().Auth.JWTSecret)
	}, Admit: func(ctx context.Context, node workercontrolcore.Node, a workerwire.Assignment) error {
		return admitWorkerDownload(ctx, pool, node, a)
	}, ExecutionConfig: func(ctx context.Context, nodeID int) (workerwire.ExecutionConfig, error) {
		current := liveConfig()
		settings := workerwire.ExecutionConfig{SegmentRetentionSeconds: current.Playback.SegmentRetentionSeconds, SubtitleSyncCapacity: max(1, current.Playback.SubtitleSyncNodeCapacity), OutputAuthorizationEpoch: workeridentity.OutputEpoch(current.Auth.JWTSecret)}
		if err := pool.QueryRow(ctx, "SELECT drain_requested FROM stream_nodes WHERE id=$1 AND enabled", nodeID).Scan(&settings.Drain); err != nil {
			return settings, err
		}
		encoded, err := json.Marshal(settings)
		if err != nil {
			return settings, err
		}
		digest := sha256.Sum256(encoded)
		settings.Revision = hex.EncodeToString(digest[:])
		return settings, settings.Validate()
	}, ExecutionActive: func(ctx context.Context, node workercontrolcore.Node, assignment workerwire.Assignment) bool {
		if assignment.Path == workerPreparePath {
			return workerDownloadActive(ctx, pool, node.ID, assignment)
		}
		if deny.Denied(ctx, assignment.WorkID) {
			return false
		}
		card, found := recipes.Get(ctx, assignment.WorkID)
		if found && card != nil {
			return workeridentity.SameOrigin(card.TranscodeNodeURL, node.URL, node.AllowHTTP) && !deny.Denied(ctx, card.SessionID) && contentAuthority.allowed(ctx, card)
		}
		return service != nil && service.HasInFlight(node.ID, assignment.WorkID)
	}, Report: reportSink.accept,
	})
	if err != nil {
		return nil, err
	}
	enrollment, err := workerregistration.NewHandler(workerregistration.Options{Repository: repo, Origin: origin, PrivateKey: key, AllowHTTP: allowHTTP})
	if err != nil {
		return nil, err
	}
	outputs, err := workerproxy.New(origin, cfg.Auth.JWTSecret, registry)
	if err != nil {
		return nil, err
	}
	outputs.SecretValue = func() string { return liveConfig().Auth.JWTSecret }
	relay := workerOutputRelay{pool: pool, recipes: recipes, authorities: outputAuthorities, assignments: store, secret: outputs.SecretValue}
	outputs.ResolveOutput = relay.token
	outputs.Grant = func(ctx context.Context, token, nodeURL string) (workercontrolcore.GrantReply, error) {
		return service.IssueOutputGrant(ctx, token, nodeURL, liveConfig().Auth.JWTSecret)
	}
	nodehttp.Configure(func(base http.RoundTripper) http.RoundTripper {
		return outputs.WrapOutputs(base, service.WrapTransport(base))
	}, service.RequiresRecipe)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = registry.refresh(ctx)
			}
		}
	}()
	coreHandler := service.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/internal/workers/output-grant" {
			currentSecret := liveConfig().Auth.JWTSecret
			if request.Method != http.MethodPost || nodeauth.Verify(request, currentSecret) != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var input struct {
				Token   string `json:"token"`
				NodeURL string `json:"node_url"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 128<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil || len(input.Token) > 64<<10 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			reply, err := service.IssueOutputGrant(request.Context(), input.Token, input.NodeURL, currentSecret)
			if err != nil {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(reply)
			return
		}
		if request.URL.Path == "/api/internal/workers/enroll" {
			enrollment.ServeHTTP(w, request)
			return
		}
		coreHandler.ServeHTTP(w, request)
	}), nil
}

func initializeHTTPWorkerProxy(ctx context.Context, pool *pgxpool.Pool, cipher *secret.Cipher, cfg *config.Config, live func() *config.Config) error {
	registry := &coreWorkerRegistry{repo: workerregistration.New(pool, cipher)}
	if err := registry.refresh(ctx); err != nil {
		return err
	}
	transport, err := workerproxy.New(cfg.Server.PublicURL, cfg.Auth.JWTSecret, registry)
	if err != nil {
		configured, lookupErr := hasHTTPWorkers(ctx, pool)
		if lookupErr != nil {
			return lookupErr
		}
		if !configured {
			return nil
		}
		return err
	}
	transport.SecretValue = func() string { return live().Auth.JWTSecret }
	nodehttp.Configure(transport.Wrap)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = registry.refresh(ctx)
			}
		}
	}()
	return nil
}

func hasHTTPWorkers(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var configured bool
	err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM worker_http_nodes)").Scan(&configured)
	return configured, err
}
