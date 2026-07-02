package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type adminSettingsConnectionCheckRequest struct {
	Values    map[string]string `json:"values"`
	DirtyKeys []string          `json:"dirty_keys"`
}

type connectionCheckResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type s3SettingsCheckClient interface {
	HeadBucket(ctx context.Context, bucket string) error
}

type redisSettingsCheckClient interface {
	Ping(ctx context.Context) error
	Close() error
}

type embeddingsSettingsCheckClient interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type redisSettingsCheckAdapter struct {
	client *redis.Client
}

func (a *redisSettingsCheckAdapter) Ping(ctx context.Context) error {
	return a.client.Ping(ctx).Err()
}

func (a *redisSettingsCheckAdapter) Close() error {
	return a.client.Close()
}

var newAdminS3SettingsCheckClient = func(cfg s3client.BucketConfig) s3SettingsCheckClient {
	return s3client.NewClient(cfg)
}

var newAdminRedisSettingsCheckClient = func(cfg config.RedisConfig) (redisSettingsCheckClient, error) {
	client, err := cache.NewRedisClient(cfg)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, nil
	}
	return &redisSettingsCheckAdapter{client: client}, nil
}

var newAdminEmbeddingsSettingsCheckClient = func(
	cfg embeddings.ClientConfig,
) embeddingsSettingsCheckClient {
	return embeddings.NewClient(cfg)
}

func (h *AdminHandler) HandleCheckSettingsConnection(w http.ResponseWriter, r *http.Request) {
	if h.SettingsRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Settings store not configured")
		return
	}

	kind := chi.URLParam(r, "kind")
	if strings.TrimSpace(kind) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Check kind is required")
		return
	}

	var req adminSettingsConnectionCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Values == nil {
		req.Values = map[string]string{}
	}

	effectiveSettings, err := h.effectiveSettingsForConnectionCheck(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load settings")
		return
	}

	cfg, err := config.LoadFromDB(effectiveSettings)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	var response connectionCheckResponse
	switch kind {
	case "s3_public", "s3_operational":
		response = checkS3PublicConnection(r.Context(), cfg)
	case "s3_private":
		response = checkS3PrivateConnection(r.Context(), cfg)
	case "redis":
		response = checkRedisConnection(r.Context(), cfg)
	case "recommendations_embedding":
		response = checkRecommendationsEmbeddingConnection(r.Context(), cfg)
	case "meilisearch":
		response = checkMeilisearchConnection(r.Context(), effectiveSettings)
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "Unsupported connection check kind")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func checkMeilisearchConnection(ctx context.Context, settings map[string]string) connectionCheckResponse {
	searchSettings, err := catalog.CatalogSearchSettingsFromMap(settings)
	if err != nil {
		return connectionCheckResponse{Success: false, Message: err.Error()}
	}
	if searchSettings.MeilisearchURL == "" {
		return connectionCheckResponse{Success: false, Message: "Meilisearch URL is required"}
	}
	searchSettings.Provider = catalog.SearchProviderMeilisearch
	indexer := catalog.NewCatalogSearchIndexer(nil, nil)
	if err := indexer.CheckConnection(ctx, searchSettings); err != nil {
		return connectionCheckResponse{Success: false, Message: err.Error()}
	}
	return connectionCheckResponse{Success: true, Message: "Meilisearch connection successful"}
}

func (h *AdminHandler) effectiveSettingsForConnectionCheck(
	ctx context.Context,
	req adminSettingsConnectionCheckRequest,
) (map[string]string, error) {
	settings, err := h.SettingsRepo.GetAll(ctx)
	if err != nil {
		return nil, err
	}

	merged := make(map[string]string, len(settings)+len(h.BootstrapSensitiveValues)+len(req.DirtyKeys))
	for key, value := range settings {
		merged[key] = value
	}
	for key, value := range h.BootstrapSensitiveValues {
		if value == "" {
			continue
		}
		merged[key] = value
	}
	for _, key := range req.DirtyKeys {
		merged[key] = req.Values[key]
	}

	return merged, nil
}

func checkS3PublicConnection(ctx context.Context, cfg *config.Config) connectionCheckResponse {
	if strings.TrimSpace(cfg.S3.Public.Endpoint) == "" {
		return connectionCheckResponse{Success: false, Message: "S3 endpoint is required."}
	}
	if strings.TrimSpace(cfg.S3.Public.Bucket) == "" {
		return connectionCheckResponse{Success: false, Message: "S3 bucket is required."}
	}

	urlAuth := strings.TrimSpace(cfg.S3.Public.URLAuth)
	switch urlAuth {
	case "", s3client.URLAuthPresigned:
	case s3client.URLAuthPublic:
		if strings.TrimSpace(cfg.S3.Public.ReadEndpoint) == "" {
			return connectionCheckResponse{
				Success: false,
				Message: "A public endpoint is required when URL auth is set to Public.",
			}
		}
	case s3client.URLAuthCloudflareToken:
		if strings.TrimSpace(cfg.S3.Public.ReadEndpoint) == "" {
			return connectionCheckResponse{
				Success: false,
				Message: "A public endpoint is required when URL auth is set to Cloudflare Token.",
			}
		}
		if strings.TrimSpace(cfg.S3.Public.TokenSecret) == "" {
			return connectionCheckResponse{
				Success: false,
				Message: "A token secret is required when URL auth is set to Cloudflare Token.",
			}
		}
	default:
		return connectionCheckResponse{
			Success: false,
			Message: fmt.Sprintf("Unsupported S3 URL auth method %q.", urlAuth),
		}
	}

	client := newAdminS3SettingsCheckClient(s3client.BucketConfig{
		Endpoint:       cfg.S3.Public.Endpoint,
		PublicEndpoint: cfg.S3.Public.ReadEndpoint,
		Region:         cfg.S3.Public.Region,
		Bucket:         cfg.S3.Public.Bucket,
		KeyPrefix:      cfg.S3.Public.KeyPrefix,
		AccessKey:      cfg.S3.Public.AccessKey,
		SecretKey:      cfg.S3.Public.SecretKey,
		PathStyle:      cfg.S3.Public.PathStyle,
		URLAuth:        cfg.S3.Public.URLAuth,
		TokenSecret:    cfg.S3.Public.TokenSecret,
		TokenParam:     cfg.S3.Public.TokenParam,
		TokenTTL:       cfg.S3.Public.TokenTTL,
	})

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := client.HeadBucket(checkCtx, cfg.S3.Public.Bucket); err != nil {
		return connectionCheckResponse{
			Success: false,
			Message: fmt.Sprintf("S3 connection check failed: %v", err),
		}
	}

	return connectionCheckResponse{
		Success: true,
		Message: "S3 connection successful.",
	}
}

func checkS3PrivateConnection(ctx context.Context, cfg *config.Config) connectionCheckResponse {
	if strings.TrimSpace(cfg.S3.Private.Endpoint) == "" {
		return connectionCheckResponse{Success: false, Message: "S3 endpoint is required."}
	}
	if strings.TrimSpace(cfg.S3.Private.Bucket) == "" {
		return connectionCheckResponse{Success: false, Message: "S3 bucket is required."}
	}

	client := newAdminS3SettingsCheckClient(s3client.BucketConfig{
		Endpoint:  cfg.S3.Private.Endpoint,
		Region:    cfg.S3.Private.Region,
		Bucket:    cfg.S3.Private.Bucket,
		KeyPrefix: cfg.S3.Private.KeyPrefix,
		AccessKey: cfg.S3.Private.AccessKey,
		SecretKey: cfg.S3.Private.SecretKey,
		PathStyle: cfg.S3.Private.PathStyle,
	})

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := client.HeadBucket(checkCtx, cfg.S3.Private.Bucket); err != nil {
		return connectionCheckResponse{
			Success: false,
			Message: fmt.Sprintf("S3 connection check failed: %v", err),
		}
	}

	return connectionCheckResponse{
		Success: true,
		Message: "S3 connection successful.",
	}
}

func checkRedisConnection(ctx context.Context, cfg *config.Config) connectionCheckResponse {
	if strings.TrimSpace(cfg.Redis.URL) == "" {
		return connectionCheckResponse{Success: false, Message: "Redis URL is required."}
	}

	client, err := newAdminRedisSettingsCheckClient(cfg.Redis)
	if err != nil {
		return connectionCheckResponse{
			Success: false,
			Message: fmt.Sprintf("Redis connection check failed: %v", err),
		}
	}
	if client == nil {
		return connectionCheckResponse{Success: false, Message: "Redis URL is required."}
	}
	defer client.Close()

	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(checkCtx); err != nil {
		return connectionCheckResponse{
			Success: false,
			Message: fmt.Sprintf("Redis connection check failed: %v", err),
		}
	}

	return connectionCheckResponse{
		Success: true,
		Message: "Redis connection successful.",
	}
}

func checkRecommendationsEmbeddingConnection(
	ctx context.Context,
	cfg *config.Config,
) connectionCheckResponse {
	if strings.TrimSpace(cfg.Recommendations.EmbeddingBaseURL) == "" {
		return connectionCheckResponse{
			Success: false,
			Message: "Embedding base URL is required.",
		}
	}
	if strings.TrimSpace(cfg.Recommendations.EmbeddingModel) == "" {
		return connectionCheckResponse{
			Success: false,
			Message: "Embedding model is required.",
		}
	}

	client := newAdminEmbeddingsSettingsCheckClient(embeddings.ClientConfig{
		BaseURL: cfg.Recommendations.EmbeddingBaseURL,
		Model:   cfg.Recommendations.EmbeddingModel,
		APIKey:  cfg.Recommendations.EmbeddingAuthToken,
	})

	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := client.Embed(checkCtx, []string{"silo connection test"}); err != nil {
		return connectionCheckResponse{
			Success: false,
			Message: fmt.Sprintf("Embedding connection check failed: %v", err),
		}
	}

	return connectionCheckResponse{
		Success: true,
		Message: "Embedding connection successful.",
	}
}
