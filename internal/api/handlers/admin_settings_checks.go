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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"golang.org/x/net/idna"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/embeddingvectors"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// adminMeilisearchRedirectMessage answers a credentialed Meilisearch check
// that was redirected to another scheme, host or port, which it does not follow.
const adminMeilisearchRedirectMessage = "Meilisearch redirected the check to another address. Enter the address it redirects to, such as its https:// URL, and enter the API key again."

// adminMeilisearchKeyRejectedMessage answers a check whose key, or missing
// key, Meilisearch refused for an authenticated request.
const adminMeilisearchKeyRejectedMessage = "Meilisearch refused the API key, or requires one. Enter a key that can manage indexes and documents, such as the Default Admin API Key."

const adminTextAIConnectionFailureMessage = "Text AI connection failed. Check the model, reasoning level, account connection, and provider usage limits."

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

var newAdminS3SettingsCheckClient = func(cfg s3client.BucketConfig) s3SettingsCheckClient {
	cfg.Role = "checks"
	return s3client.NewClient(cfg)
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

var checkAdminMeilisearchSettingsConnection = func(ctx context.Context, settings catalog.CatalogSearchSettings) error {
	return catalog.NewCatalogSearchIndexer(nil, nil).CheckConnection(ctx, settings)
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

	response, err := h.runAdminSettingsConnectionCheck(r.Context(), kind, cfg, effectiveSettings)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Unsupported connection check kind")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *AdminHandler) runAdminSettingsConnectionCheck(ctx context.Context, kind string, cfg *config.Config, effectiveSettings map[string]string) (connectionCheckResponse, error) {
	var response connectionCheckResponse
	switch kind {
	case "s3_public", "s3_operational":
		response = checkS3PublicConnection(ctx, cfg)
	case "s3_private":
		response = checkS3PrivateConnection(ctx, cfg)
	case "redis":
		response = checkRedisConnection(ctx, cfg)
	case "recommendations_embedding":
		response = checkRecommendationsEmbeddingConnection(ctx, cfg, effectiveSettings[recommendations.EmbeddingLockSettingKey])
	case "ai_chat":
		if cfg.AI.AuthMode == llm.AuthModeChatGPT {
			if h.ChatGPT == nil {
				return connectionCheckResponse{Message: adminChatGPTSignInRequiredMessage, safeMessage: adminChatGPTSignInRequiredMessage}, nil
			}
			response = checkAIChatConnectionWithToken(ctx, cfg, h.ChatGPT)
		} else {
			response = checkAIChatConnection(ctx, cfg)
		}
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
		AuthMode:        cfg.AI.AuthMode,
		ReasoningEffort: cfg.AI.ReasoningEffort,
		BaseURL:         strings.TrimSpace(cfg.AI.BaseURL),
		APIKey:          cfg.AI.APIKey,
		ChatModel:       strings.TrimSpace(cfg.AI.ChatModel),
		ASRBaseURL:      strings.TrimSpace(cfg.AI.ASRBaseURL),
		ASRAPIKey:       cfg.AI.ASRAPIKey,
		ASRModel:        strings.TrimSpace(cfg.AI.ASRModel),
	}
}

func checkAIChatConnection(ctx context.Context, cfg *config.Config) connectionCheckResponse {
	return checkAIChatConnectionWithToken(ctx, cfg, nil)
}

func checkAIChatConnectionWithToken(ctx context.Context, cfg *config.Config, connection *chatgpt.Service) connectionCheckResponse {
	if cfg.AI.AuthMode != llm.AuthModeChatGPT && strings.TrimSpace(cfg.AI.BaseURL) == "" {
		return connectionCheckResponse{Success: false, Message: "Choose a text provider."}
	}
	if strings.TrimSpace(cfg.AI.ChatModel) == "" {
		return connectionCheckResponse{Success: false, Message: "Chat model is required."}
	}

	clientCfg := aiClientConfig(cfg)
	if connection != nil {
		checkConfig, err := connection.CheckTextConfig(ctx)
		if err != nil {
			return connectionCheckResponse{Message: adminTextAIConnectionFailureMessage, safeMessage: adminTextAIConnectionFailureMessage}
		}
		checkConfig.ChatModel = clientCfg.ChatModel
		checkConfig.ReasoningEffort = clientCfg.ReasoningEffort
		clientCfg = checkConfig
	}
	client := newAdminAISettingsCheckClient(clientCfg)
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := client.Chat(checkCtx, []llm.Message{
		{Role: "system", Content: "Return a JSON object with status set to ok."},
		{Role: "user", Content: "Check this Silo text translation connection."},
	}, true); err != nil {
		return connectionCheckResponse{
			Success:     false,
			Message:     fmt.Sprintf("Text AI connection check failed: %v", err),
			safeMessage: adminTextAIConnectionFailureMessage,
		}
	}

	return connectionCheckResponse{Success: true, Message: "Text AI connection successful."}
}

func checkAITranscriptionConnection(ctx context.Context, cfg *config.Config) connectionCheckResponse {
	if strings.TrimSpace(cfg.AI.ASRBaseURL) == "" {
		return connectionCheckResponse{
			Success: false,
			Message: "Choose a speech-to-text provider.",
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
			return "Speech-to-text authentication failed. Replace the provider's API key in AI Services."
		case http.StatusForbidden:
			return "Speech-to-text access was denied. Check your API key permissions and allowed providers."
		case http.StatusPaymentRequired:
			return "Speech-to-text provider requires payment. Check your provider balance and spending limit."
		case http.StatusNotFound:
			return "Speech-to-text endpoint or model is unavailable. Check the provider's base URL, the model, and allowed providers."
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
	if err := checkAdminMeilisearchSettingsConnection(ctx, searchSettings); err != nil {
		if errors.Is(err, catalog.ErrMeilisearchCheckRedirected) {
			return connectionCheckResponse{Message: adminMeilisearchRedirectMessage, safeMessage: adminMeilisearchRedirectMessage}
		}
		if errors.Is(err, catalog.ErrMeilisearchCheckKeyRejected) {
			return connectionCheckResponse{Message: adminMeilisearchKeyRejectedMessage, safeMessage: adminMeilisearchKeyRejectedMessage}
		}
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

	storedMeilisearchURL := merged[catalog.SearchSettingMeilisearchURL]
	for _, key := range req.DirtyKeys {
		// The lock is server state the embedding check compares a draft
		// against, and the provider registry holds stored keys a draft could
		// otherwise send elsewhere. AI and embedding drafts select a provider
		// by id, which reaches only that provider's own address.
		if key == recommendations.EmbeddingLockSettingKey || machineManagedSettingKeys[key] {
			continue
		}
		merged[key] = req.Values[key]
	}
	if kind == "meilisearch" &&
		(storedMeilisearchURL == "" || endpointAuthority(storedMeilisearchURL) != endpointAuthority(merged[catalog.SearchSettingMeilisearchURL])) &&
		!hasExplicitDraftSecret(req, catalog.SearchSettingMeilisearchAPIKey) {
		merged[catalog.SearchSettingMeilisearchAPIKey] = ""
	}

	return merged, nil
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
	scheme := strings.ToLower(parsed.Scheme)
	// Compare the name net/http dials. It converts a non-ASCII host with IDNA
	// lookup, which Unicode lowercasing does not match: "İ" lowercases to "i"
	// but dials a punycode name. A host IDNA rejects equals only itself.
	host := parsed.Hostname()
	if strings.ContainsFunc(host, func(r rune) bool { return r >= utf8.RuneSelf }) {
		ascii, idnaErr := idna.Lookup.ToASCII(host)
		if idnaErr != nil {
			return trimmed
		}
		host = ascii
	}
	host = strings.ToLower(host)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := parsed.Port()
	if number, _ := strconv.Atoi(port); (scheme == eventsSchemeHTTPS && number == 443) || (scheme == eventsSchemeHTTP && number == 80) {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
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

	if err := probeRedis(ctx, cfg.Redis); err != nil {
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

// errRedisChannelsRefused wraps the error Redis answers a subscription to the
// event bus channels with.
var errRedisChannelsRefused = errors.New("redis refused a subscription to Silo's event channels")

// errRedisURLUnreadable wraps the error a redis.url the Redis clients cannot
// read gives, such as a bare host:port an older build saved.
var errRedisURLUnreadable = errors.New("redis URL cannot be read")

// probeRedis does what a starting server needs from the Redis cfg names, with
// the client Silo's services use: it connects, sends a PING and subscribes to
// the event bus channels. Connecting selects the database number, so a number
// the server does not have fails as well, and the subscription fails for a
// user whose ACL does not grant the channels. The Redis connection check and a
// save that changes the connection both run it.
var probeRedis = func(ctx context.Context, cfg config.RedisConfig) error {
	// The deadline client stops waiting when the check's time is up, even
	// for a URL whose read_timeout disables the socket timeout.
	client, err := cache.NewDeadlineRedisClientForRole(cfg, "checks")
	if err != nil {
		return fmt.Errorf("%w: %w", errRedisURLUnreadable, err)
	}
	if client == nil {
		return errors.New("redis URL is required")
	}
	defer func() { _ = cache.CloseRedisClient(client) }()

	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(checkCtx).Err(); err != nil {
		return err
	}
	if err := cache.CheckEventBusChannels(checkCtx, client); err != nil {
		return fmt.Errorf("%w: %w", errRedisChannelsRefused, err)
	}
	return nil
}

// checkRecommendationsEmbeddingConnection embeds a test string and checks
// that Silo can store what the model returns and that the stored embedding
// lock (rawLock, empty when none) accepts the configuration. Its failure
// messages are authored here and never quote the base URL, so the v2 boundary
// returns them as they are.
func checkRecommendationsEmbeddingConnection(
	ctx context.Context,
	cfg *config.Config,
	rawLock string,
) connectionCheckResponse {
	if strings.TrimSpace(cfg.Recommendations.EmbeddingBaseURL) == "" {
		return connectionCheckResponse{
			Success: false,
			Message: "Choose an embedding provider.",
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
	vectors, err := client.Embed(checkCtx, []string{"silo connection test"})
	if err != nil {
		return connectionCheckResponse{
			Success: false,
			Message: fmt.Sprintf("Embedding connection check failed: %v", err),
		}
	}
	failed := func(message string) connectionCheckResponse {
		return connectionCheckResponse{Success: false, Message: message, safeMessage: message}
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return failed("The embedding provider returned no vector for the test text.")
	}
	dimensions := len(vectors[0])
	if dimensions > embeddingvectors.CanonicalDimensions {
		return failed(fmt.Sprintf("The model returns %d dimensions; Silo stores at most %d.", dimensions, embeddingvectors.CanonicalDimensions))
	}
	lock, err := recommendations.ParseEmbeddingLock(rawLock)
	if err != nil {
		return failed("The stored embedding lock cannot be read. Reset embeddings to clear it.")
	}
	if lock != nil {
		if conflict := recommendations.DescribeEmbeddingLockConflict(*lock, cfg.Recommendations.EmbeddingBaseURL, cfg.Recommendations.EmbeddingModel, dimensions); conflict != "" {
			return failed(conflict)
		}
	}

	return connectionCheckResponse{
		Success: true,
		Message: fmt.Sprintf("Embedding connection successful. The model returns %d dimensions.", dimensions),
	}
}
