package handlers

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/embeddingvectors"
	"github.com/Silo-Server/silo-server/internal/logredact"
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

const adminTextAIConnectionFailureMessage = "Text AI connection failed. Check the model, reasoning level, service tier, account connection, and provider usage limits."

// checkMessage is a check result's English authored here, with the key of
// its translation catalog entry and the values of the entry's placeholders.
// The web admin shows the catalog's translation in a non-English interface.
type checkMessage struct {
	key  string
	text string
	args map[string]string
}

const checkMessageKeyPrefix = "web.admin.common.connectionCheck.result."

func newCheckMessage(name, text string) checkMessage {
	return checkMessage{key: checkMessageKeyPrefix + name, text: text}
}

var (
	checkMessageFailed                = newCheckMessage("failed", "Connection check failed. Verify the submitted settings and provider availability.")
	checkMessageChatGPTSignInRequired = newCheckMessage("chatGPTSignInRequired", adminChatGPTSignInRequiredMessage)
	checkMessageTextAIFailed          = newCheckMessage("textAIFailed", adminTextAIConnectionFailureMessage)
	checkMessageTextAIUnavailable     = newCheckMessage("textAIUnavailable", "Text AI provider is temporarily unavailable. Try again later.")
	checkMessageMeilisearchRedirected = newCheckMessage("meilisearchRedirected", adminMeilisearchRedirectMessage)
	checkMessageMeilisearchKey        = newCheckMessage("meilisearchKeyRejected", adminMeilisearchKeyRejectedMessage)
	checkMessageS3Verified            = newCheckMessage("s3Verified", "S3 connection and object read/write/delete permissions verified.")
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
	// messageKey and messageArgs name the catalog entry of the message the
	// native API returns: Message on success, safeMessage on failure.
	messageKey  string
	messageArgs map[string]string
}

func checkPassed(m checkMessage) connectionCheckResponse {
	return connectionCheckResponse{Success: true, Message: m.text, messageKey: m.key, messageArgs: m.args}
}

func checkFailed(m checkMessage) connectionCheckResponse {
	return connectionCheckResponse{Message: m.text, safeMessage: m.text, messageKey: m.key, messageArgs: m.args}
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
				return checkFailed(checkMessageChatGPTSignInRequired), nil
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
	return checkPassed(newCheckMessage("mdblistVerified", "MDBList API key verified."))
}

func aiClientConfig(cfg *config.Config) llm.Config {
	return llm.Config{
		AuthMode:        cfg.AI.AuthMode,
		ReasoningEffort: cfg.AI.ReasoningEffort,
		ServiceTier:     cfg.AI.ServiceTier,
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
			return textCheckFailed(ctx, err, "")
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
		return textCheckFailed(ctx, err, clientCfg.APIKey)
	}

	return checkPassed(newCheckMessage("textAIConnected", "Text AI connection successful."))
}

// textCheckFailed explains a failed text model check in authored words and
// logs the provider's error, which can carry response bodies, for the
// administrator to read in the server log. A provider that echoes the API key
// in its error body, code or parameter does not get it into the log.
func textCheckFailed(ctx context.Context, err error, apiKey string) connectionCheckResponse {
	apiKey = strings.TrimSpace(apiKey)
	redact := func(text string) string {
		if apiKey != "" {
			text = strings.ReplaceAll(text, apiKey, logredact.Placeholder)
		}
		return logredact.SanitizeText(text)
	}
	// A streamed ChatGPT or Responses API failure carries its cause only in
	// the provider's error code, not in the error text.
	attrs := []slog.Attr{slog.String(logComponentKey, "api"), slog.String("error", redact(err.Error()))}
	if httpErr, ok := errors.AsType[*llm.HTTPError](err); ok {
		// A failure reported inside a successful response has no HTTP status.
		if httpErr.InResponse {
			attrs = append(attrs, slog.Bool("in_response", true))
		} else {
			attrs = append(attrs, slog.Int("status", httpErr.StatusCode))
		}
		attrs = append(attrs, slog.String("provider_code", redact(httpErr.Code)), slog.String("provider_param", redact(httpErr.Param)))
	}
	slog.LogAttrs(ctx, slog.LevelWarn, "text AI connection check failed", attrs...)
	return checkFailed(textCheckFailureMessage(err))
}

// textCheckFailureMessage explains a classified provider failure, or falls
// back to the general text check failure.
func textCheckFailureMessage(err error) checkMessage {
	switch {
	case errors.Is(err, chatgpt.ErrSignInRequired):
		return checkMessageChatGPTSignInRequired
	case errors.Is(err, llm.ErrPlanIneligible):
		return newCheckMessage("textAIPlanIneligible", "The connected ChatGPT account can't share its plan usage. Check ChatGPT usage settings, or connect another account.")
	case errors.Is(err, chatgpt.ErrProvider):
		return checkMessageTextAIUnavailable
	}
	switch llm.Classify(err) {
	case llm.FailureQuotaExhausted, llm.FailurePaymentRequired:
		return newCheckMessage("textAIBilling", "Text AI provider has no available credit, or a usage or spending limit was reached. Check the provider balance and limits, or ChatGPT usage settings.")
	case llm.FailureUnauthorized:
		return newCheckMessage("textAIUnauthorized", "Text AI provider rejected the credentials. Replace the provider's API key, or sign in with ChatGPT again.")
	case llm.FailureForbidden:
		return newCheckMessage("textAIForbidden", "Text AI access was denied. Check that the API key or ChatGPT account may use this model.")
	case llm.FailureNotFound:
		return newCheckMessage("textAINotFound", "Text AI endpoint or model is unavailable. Check the provider's base URL and the model.")
	case llm.FailureRateLimited:
		return newCheckMessage("textAIRateLimited", "Text AI provider is rate limiting requests. Try again later.")
	case llm.FailureUnavailable:
		return checkMessageTextAIUnavailable
	case llm.FailureTimeout:
		return newCheckMessage("textAITimedOut", "Text AI connection check timed out. Check provider availability and try again.")
	case llm.FailureUnreachable:
		return newCheckMessage("textAIUnreachable", "Silo could not reach the text AI provider. Check the provider's base URL and that this server can connect to it.")
	case llm.FailureUnusableResponse:
		return newCheckMessage("textAIUnusableResponse", "Text model returned a response Silo cannot use. Choose another model.")
	case llm.FailureRejected, llm.FailureTooLarge:
		status, ok := errors.AsType[*llm.HTTPError](err)
		if ok && status.InResponse {
			// The provider answered, then reported the failure in its response,
			// so there is no HTTP status to show.
			return newCheckMessage("textAIRefused", "Text AI provider refused the check request. Check the model, the reasoning level, and the account's usage settings. The server log has the provider's error.")
		}
		if ok {
			m := newCheckMessage("textAIRejected", fmt.Sprintf("Text AI provider rejected the check request (status %d). Check the model, reasoning level, and service tier. The server log has the provider's error.", status.StatusCode))
			m.args = map[string]string{"status": strconv.Itoa(status.StatusCode)}
			return m
		}
	}
	return checkMessageTextAIFailed
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
		if message.key == "" {
			message = checkMessageFailed
		}
		// Both the legacy handler and native service consume this result.
		// Never put provider-controlled error text in the response.
		return checkFailed(message)
	}

	if result != nil {
		for _, segment := range result.Segments {
			if strings.TrimSpace(segment.Text) != "" && segment.Start >= 0 && segment.End > segment.Start {
				return checkPassed(newCheckMessage("speechConnected", "Speech-to-text connection successful: timestamped speech received."))
			}
		}
	}
	return connectionCheckResponse{
		Success: false,
		Message: "Speech-to-text connection check failed: the model did not return usable timestamped speech. Choose a model that supports segment timestamps.",
	}
}

// transcriptionCheckFailureMessage explains a classified provider failure, or
// returns the zero message for one it cannot explain.
func transcriptionCheckFailureMessage(err error) checkMessage {
	if errors.Is(err, llm.ErrTimedSegmentsUnsupported) {
		return newCheckMessage("speechNoTimestamps", "Speech-to-text model does not return timestamped segments, which Silo needs to make subtitles. Choose a model that supports segment timestamps, such as whisper-1 on OpenAI.")
	}
	switch llm.Classify(err) {
	case llm.FailureQuotaExhausted:
		return newCheckMessage("speechQuotaExhausted", "Speech-to-text provider quota is exhausted. Check your provider balance and limits.")
	case llm.FailureUnauthorized:
		return newCheckMessage("speechUnauthorized", "Speech-to-text authentication failed. Replace the provider's API key in AI Services.")
	case llm.FailureForbidden:
		return newCheckMessage("speechForbidden", "Speech-to-text access was denied. Check your API key permissions and allowed providers.")
	case llm.FailurePaymentRequired:
		return newCheckMessage("speechPaymentRequired", "Speech-to-text provider requires payment. Check your provider balance and spending limit.")
	case llm.FailureNotFound:
		return newCheckMessage("speechNotFound", "Speech-to-text endpoint or model is unavailable. Check the provider's base URL, the model, and allowed providers.")
	case llm.FailureRateLimited:
		return newCheckMessage("speechRateLimited", "Speech-to-text provider is rate limiting requests. Try again later.")
	case llm.FailureUnavailable:
		return newCheckMessage("speechUnavailable", "Speech-to-text provider is temporarily unavailable. Try again later.")
	case llm.FailureTimeout:
		return newCheckMessage("speechTimedOut", "Speech-to-text connection check timed out. Check provider availability and try again.")
	}
	return checkMessage{}
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
			return checkFailed(checkMessageMeilisearchRedirected)
		}
		if errors.Is(err, catalog.ErrMeilisearchCheckKeyRejected) {
			return checkFailed(checkMessageMeilisearchKey)
		}
		return connectionCheckResponse{Success: false, Message: err.Error()}
	}
	return checkPassed(newCheckMessage("meilisearchConnected", "Meilisearch connection successful"))
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

	return checkPassed(checkMessageS3Verified)
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

	return checkPassed(checkMessageS3Verified)
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

	return checkPassed(newCheckMessage("redisConnected", "Redis connection successful."))
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
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return checkFailed(newCheckMessage("embeddingNoVector", "The embedding provider returned no vector for the test text."))
	}
	dimensions := len(vectors[0])
	if dimensions > embeddingvectors.CanonicalDimensions {
		message := newCheckMessage("embeddingTooManyDimensions", fmt.Sprintf("The model returns %d dimensions; Silo stores at most %d.", dimensions, embeddingvectors.CanonicalDimensions))
		message.args = map[string]string{"dimensions": strconv.Itoa(dimensions), "max": strconv.Itoa(embeddingvectors.CanonicalDimensions)}
		return checkFailed(message)
	}
	lock, err := recommendations.ParseEmbeddingLock(rawLock)
	if err != nil {
		return checkFailed(newCheckMessage("embeddingLockUnreadable", "The stored embedding lock cannot be read. Reset embeddings to clear it."))
	}
	if lock != nil {
		conflict, key, args := recommendations.DescribeEmbeddingLockConflict(*lock, cfg.Recommendations.EmbeddingBaseURL, cfg.Recommendations.EmbeddingModel, dimensions)
		if conflict != "" {
			return checkFailed(checkMessage{key: key, text: conflict, args: args})
		}
	}

	message := newCheckMessage("embeddingConnected", fmt.Sprintf("Embedding connection successful. The model returns %d dimensions.", dimensions))
	message.args = map[string]string{"dimensions": strconv.Itoa(dimensions)}
	return checkPassed(message)
}
