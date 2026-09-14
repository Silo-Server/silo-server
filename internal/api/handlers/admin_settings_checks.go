package handlers

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/mdblist"
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
	// safeMessage contains only diagnostics authored here, never provider text.
	safeMessage string
}

type s3SettingsCheckClient interface {
	HeadBucket(ctx context.Context, bucket string) error
	PutObject(ctx context.Context, bucket, key string, data []byte) error
	GetObject(ctx context.Context, bucket, key string) ([]byte, error)
	DeleteObject(ctx context.Context, bucket, key string) error
}

type redisSettingsCheckClient interface {
	Ping(ctx context.Context) error
	Close() error
}

type embeddingsSettingsCheckClient interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type mdblistSettingsCheckClient interface {
	Check(ctx context.Context) error
}

type aiSettingsCheckClient interface {
	Chat(ctx context.Context, messages []llm.Message, jsonObject bool) (string, error)
	Transcribe(ctx context.Context, req llm.TranscribeRequest) (*llm.Transcription, error)
}

type redisSettingsCheckAdapter struct {
	client *redis.Client
}

func (a *redisSettingsCheckAdapter) Ping(ctx context.Context) error {
	return a.client.Ping(ctx).Err()
}

func (a *redisSettingsCheckAdapter) Close() error {
	return cache.CloseRedisClient(a.client)
}

var newAdminS3SettingsCheckClient = func(cfg s3client.BucketConfig) s3SettingsCheckClient {
	cfg.Role = "checks"
	return s3client.NewClient(cfg)
}

var newAdminRedisSettingsCheckClient = func(cfg config.RedisConfig) (redisSettingsCheckClient, error) {
	client, err := cache.NewRedisClientForRole(cfg, "checks")
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

var newAdminMDBListSettingsCheckClient = func(apiKey string) mdblistSettingsCheckClient {
	return mdblist.NewClient(apiKey, nil)
}

var newAdminAISettingsCheckClient = func(cfg llm.Config) aiSettingsCheckClient {
	return llm.NewClient(cfg)
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

	effectiveSettings, err := h.effectiveSettingsForConnectionCheck(r.Context(), kind, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load settings")
		return
	}

	cfg, err := config.LoadFromDB(effectiveSettings)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	response, err := runAdminSettingsConnectionCheck(r.Context(), kind, cfg, effectiveSettings)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Unsupported connection check kind")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func runAdminSettingsConnectionCheck(ctx context.Context, kind string, cfg *config.Config, effectiveSettings map[string]string) (connectionCheckResponse, error) {
	var response connectionCheckResponse
	switch kind {
	case "s3_public", "s3_operational":
		response = checkS3PublicConnection(ctx, cfg)
	case "s3_private":
		response = checkS3PrivateConnection(ctx, cfg)
	case "redis":
		response = checkRedisConnection(ctx, cfg)
	case "recommendations_embedding":
		response = checkRecommendationsEmbeddingConnection(ctx, cfg)
	case "ai_chat":
		response = checkAIChatConnection(ctx, cfg)
	case "ai_transcription":
		response = checkAITranscriptionConnection(ctx, cfg)
	case "meilisearch":
		response = checkMeilisearchConnection(ctx, effectiveSettings)
	case "mdblist":
		response = checkMDBListConnection(ctx, cfg)
	default:
		return connectionCheckResponse{}, ErrAdminSettingsCheckKind
	}

	return response, nil
}

func checkMDBListConnection(ctx context.Context, cfg *config.Config) connectionCheckResponse {
	if strings.TrimSpace(cfg.MDBListAPIKey) == "" {
		return connectionCheckResponse{Success: false, Message: "MDBList API key is required."}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := newAdminMDBListSettingsCheckClient(cfg.MDBListAPIKey).Check(checkCtx); err != nil {
		return connectionCheckResponse{Success: false, Message: fmt.Sprintf("MDBList connection check failed: %v", err)}
	}
	return connectionCheckResponse{Success: true, Message: "MDBList API key verified."}
}

func aiClientConfig(cfg *config.Config) llm.Config {
	return llm.Config{
		BaseURL:    strings.TrimSpace(cfg.AI.BaseURL),
		APIKey:     cfg.AI.APIKey,
		ChatModel:  strings.TrimSpace(cfg.AI.ChatModel),
		ASRBaseURL: strings.TrimSpace(cfg.AI.ASRBaseURL),
		ASRAPIKey:  cfg.AI.ASRAPIKey,
		ASRModel:   strings.TrimSpace(cfg.AI.ASRModel),
	}
}

func checkAIChatConnection(ctx context.Context, cfg *config.Config) connectionCheckResponse {
	if strings.TrimSpace(cfg.AI.BaseURL) == "" {
		return connectionCheckResponse{Success: false, Message: "Text AI base URL is required."}
	}
	if strings.TrimSpace(cfg.AI.ChatModel) == "" {
		return connectionCheckResponse{Success: false, Message: "Chat model is required."}
	}

	client := newAdminAISettingsCheckClient(aiClientConfig(cfg))
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := client.Chat(checkCtx, []llm.Message{
		{Role: "system", Content: "Return a JSON object with status set to ok."},
		{Role: "user", Content: "Check this Silo text translation connection."},
	}, true); err != nil {
		return connectionCheckResponse{
			Success: false,
			Message: fmt.Sprintf("Text AI connection check failed: %v", err),
		}
	}

	return connectionCheckResponse{Success: true, Message: "Text AI connection successful."}
}

func checkAITranscriptionConnection(ctx context.Context, cfg *config.Config) connectionCheckResponse {
	effectiveBaseURL := strings.TrimSpace(cfg.AI.ASRBaseURL)
	if effectiveBaseURL == "" {
		effectiveBaseURL = strings.TrimSpace(cfg.AI.BaseURL)
	}
	if effectiveBaseURL == "" {
		return connectionCheckResponse{
			Success: false,
			Message: "Speech-to-text base URL is required.",
		}
	}
	if strings.TrimSpace(cfg.AI.ASRModel) == "" {
		return connectionCheckResponse{
			Success: false,
			Message: "Transcription model is required.",
		}
	}

	client := newAdminAISettingsCheckClient(aiClientConfig(cfg))
	// Allow the provider's 60-second processing window plus network overhead.
	checkCtx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	result, err := client.Transcribe(checkCtx, llm.TranscribeRequest{
		Filename: "silo-connection-check.wav",
		Audio:    transcriptionCheckWAV,
		Timeout:  75 * time.Second,
	})
	if err != nil {
		message := transcriptionCheckFailureMessage(err)
		if message == "" {
			message = "Connection check failed. Verify the submitted settings and provider availability."
		}
		// Both the legacy handler and native service consume this result.
		// Never put provider-controlled error text in the response.
		return connectionCheckResponse{Success: false, Message: message, safeMessage: message}
	}

	if result != nil {
		for _, segment := range result.Segments {
			if strings.TrimSpace(segment.Text) != "" && segment.Start >= 0 && segment.End > segment.Start {
				return connectionCheckResponse{Success: true, Message: "Speech-to-text connection successful: timestamped speech received."}
			}
		}
	}
	return connectionCheckResponse{
		Success: false,
		Message: "Speech-to-text connection check failed: the model did not return usable timestamped speech. Choose a model that supports segment timestamps.",
	}
}

func transcriptionCheckFailureMessage(err error) string {
	if errors.Is(err, llm.ErrQuotaExhausted) {
		return "Speech-to-text provider quota is exhausted. Check your provider balance and limits."
	}
	if status, ok := errors.AsType[*llm.HTTPError](err); ok {
		switch status.StatusCode {
		case http.StatusUnauthorized:
			return "Speech-to-text authentication failed. Replace the speech-to-text API key, or clear it to use the Text AI key."
		case http.StatusForbidden:
			return "Speech-to-text access was denied. Check your API key permissions and allowed providers."
		case http.StatusPaymentRequired:
			return "Speech-to-text provider requires payment. Check your provider balance and spending limit."
		case http.StatusNotFound:
			return "Speech-to-text endpoint or model is unavailable. Check the base URL, model, and allowed providers."
		case http.StatusTooManyRequests:
			return "Speech-to-text provider is rate limiting requests. Try again later."
		}
		if status.StatusCode >= 500 {
			return "Speech-to-text provider is temporarily unavailable. Try again later."
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Speech-to-text connection check timed out. Check provider availability and try again."
	}
	return ""
}

// transcriptionCheckWAV contains synthetic speech: "This is a subtitle test."
// A silent probe cannot prove that a model returns usable timestamped speech.
//
//go:embed testdata/transcription-check.wav
var transcriptionCheckWAV []byte

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
	kind string,
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

	var storedAIConfig *config.Config
	if kind == "ai_chat" || kind == "ai_transcription" {
		storedAIConfig, err = config.LoadFromDB(merged)
		if err != nil {
			return nil, err
		}
	}
	for _, key := range req.DirtyKeys {
		merged[key] = req.Values[key]
	}
	if storedAIConfig != nil {
		draftAIConfig, loadErr := config.LoadFromDB(merged)
		if loadErr != nil {
			return nil, loadErr
		}
		protectAIConnectionCheckSecrets(kind, req, storedAIConfig, draftAIConfig, merged)
	}

	return merged, nil
}

func protectAIConnectionCheckSecrets(
	kind string,
	req adminSettingsConnectionCheckRequest,
	storedCfg *config.Config,
	draftCfg *config.Config,
	settings map[string]string,
) {
	storedEndpoint := storedCfg.AI.BaseURL
	draftEndpoint := draftCfg.AI.BaseURL
	if kind == "ai_transcription" {
		if strings.TrimSpace(storedCfg.AI.ASRBaseURL) != "" {
			storedEndpoint = storedCfg.AI.ASRBaseURL
		}
		if strings.TrimSpace(draftCfg.AI.ASRBaseURL) != "" {
			draftEndpoint = draftCfg.AI.ASRBaseURL
		}
	}
	if endpointAuthority(storedEndpoint) == endpointAuthority(draftEndpoint) {
		return
	}

	if kind == "ai_transcription" && !hasExplicitDraftSecret(req, "ai.asr_api_key") {
		settings["ai.asr_api_key"] = ""
	}
	if !hasExplicitDraftSecret(req, "ai.api_key") {
		settings["ai.api_key"] = ""
		settings["subtitle_ai.api_key"] = ""
	}
}

func hasExplicitDraftSecret(req adminSettingsConnectionCheckRequest, key string) bool {
	for _, dirtyKey := range req.DirtyKeys {
		if dirtyKey == key {
			return strings.TrimSpace(req.Values[key]) != ""
		}
	}
	return false
}

func endpointAuthority(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.ToLower(trimmed)
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host)
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
	if err := checkS3ObjectPermissions(checkCtx, client, cfg.S3.Public.Bucket); err != nil {
		return connectionCheckResponse{Success: false, Message: fmt.Sprintf("S3 object permission check failed: %v", err)}
	}

	return connectionCheckResponse{
		Success: true,
		Message: "S3 connection and object read/write/delete permissions verified.",
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
	if err := checkS3ObjectPermissions(checkCtx, client, cfg.S3.Private.Bucket); err != nil {
		return connectionCheckResponse{Success: false, Message: fmt.Sprintf("S3 object permission check failed: %v", err)}
	}

	return connectionCheckResponse{
		Success: true,
		Message: "S3 connection and object read/write/delete permissions verified.",
	}
}

func checkS3ObjectPermissions(
	ctx context.Context,
	client s3SettingsCheckClient,
	bucket string,
) (resultErr error) {
	key := fmt.Sprintf(".silo-admin-connection-check/%d", time.Now().UnixNano())
	payload := []byte("silo-storage-check")
	deleted := false
	defer func() {
		if deleted {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := client.DeleteObject(cleanupCtx, bucket, key); err != nil {
			cleanupErr := fmt.Errorf("cleanup probe object: %w", err)
			if resultErr == nil {
				resultErr = cleanupErr
			} else {
				resultErr = errors.Join(resultErr, cleanupErr)
			}
		}
	}()

	if err := client.PutObject(ctx, bucket, key, payload); err != nil {
		return fmt.Errorf("write probe object: %w", err)
	}

	read, err := client.GetObject(ctx, bucket, key)
	if err != nil {
		return fmt.Errorf("read probe object: %w", err)
	}
	if !bytes.Equal(read, payload) {
		return fmt.Errorf("read probe object returned unexpected content")
	}
	if err := client.DeleteObject(ctx, bucket, key); err != nil {
		return fmt.Errorf("delete probe object: %w", err)
	}
	deleted = true
	return nil
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
