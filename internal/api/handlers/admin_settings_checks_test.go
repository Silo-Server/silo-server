package handlers

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type fakeServerSettingsStore struct {
	values       map[string]string
	setCalls     int
	setManyCalls int
	atomicCalls  int
}

func (f *fakeServerSettingsStore) Get(_ context.Context, key string) (string, error) {
	return f.values[key], nil
}

func (f *fakeServerSettingsStore) Set(_ context.Context, key, value string) error {
	f.setCalls++
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return nil
}

func (f *fakeServerSettingsStore) SetMany(_ context.Context, values map[string]string) error {
	f.setManyCalls++
	if f.values == nil {
		f.values = map[string]string{}
	}
	for key, value := range values {
		f.values[key] = value
	}
	return nil
}

func (f *fakeServerSettingsStore) UpdateAtomic(
	ctx context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	f.atomicCalls++
	current, err := f.GetAll(ctx)
	if err != nil {
		return err
	}
	writes, err := update(current)
	if err != nil || len(writes) == 0 {
		return err
	}
	return f.SetMany(ctx, writes)
}

func (f *fakeServerSettingsStore) GetAll(context.Context) (map[string]string, error) {
	cloned := make(map[string]string, len(f.values))
	for key, value := range f.values {
		cloned[key] = value
	}
	return cloned, nil
}

type fakeS3SettingsCheckClient struct {
	headBucket func(ctx context.Context, bucket string) error
	putObject  func(ctx context.Context, bucket, key string, data []byte) error
	getObject  func(ctx context.Context, bucket, key string) ([]byte, error)
	delete     func(ctx context.Context, bucket, key string) error
	objects    map[string][]byte
}

func (f *fakeS3SettingsCheckClient) PutObject(
	ctx context.Context,
	bucket,
	key string,
	data []byte,
) error {
	if f.putObject != nil {
		return f.putObject(ctx, bucket, key, data)
	}
	if f.objects == nil {
		f.objects = make(map[string][]byte)
	}
	f.objects[key] = append([]byte(nil), data...)
	return nil
}

func (f *fakeS3SettingsCheckClient) GetObject(
	ctx context.Context,
	bucket,
	key string,
) ([]byte, error) {
	if f.getObject != nil {
		return f.getObject(ctx, bucket, key)
	}
	return append([]byte(nil), f.objects[key]...), nil
}

func (f *fakeS3SettingsCheckClient) DeleteObject(ctx context.Context, bucket, key string) error {
	if f.delete != nil {
		return f.delete(ctx, bucket, key)
	}
	delete(f.objects, key)
	return nil
}

func (f *fakeS3SettingsCheckClient) HeadBucket(ctx context.Context, bucket string) error {
	if f.headBucket != nil {
		return f.headBucket(ctx, bucket)
	}
	return nil
}

func TestCheckS3ObjectPermissionsCleansUpAfterAmbiguousPutFailure(t *testing.T) {
	client := &fakeS3SettingsCheckClient{}
	client.putObject = func(_ context.Context, _, key string, data []byte) error {
		if client.objects == nil {
			client.objects = make(map[string][]byte)
		}
		client.objects[key] = append([]byte(nil), data...)
		return errors.New("response lost")
	}

	err := checkS3ObjectPermissions(context.Background(), client, "silo")

	if err == nil || !strings.Contains(err.Error(), "write probe object: response lost") {
		t.Fatalf("error = %v, want ambiguous write failure", err)
	}
	if len(client.objects) != 0 {
		t.Fatalf("probe objects = %#v, want ambiguous write cleaned up", client.objects)
	}
}

func TestCheckS3ObjectPermissionsUsesFreshContextForFailureCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cleanupContextErr error
	cleanupCalled := false
	client := &fakeS3SettingsCheckClient{
		getObject: func(context.Context, string, string) ([]byte, error) {
			cancel()
			return nil, context.Canceled
		},
		delete: func(ctx context.Context, _, _ string) error {
			cleanupCalled = true
			cleanupContextErr = ctx.Err()
			return nil
		},
	}

	err := checkS3ObjectPermissions(ctx, client, "silo")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if !cleanupCalled || cleanupContextErr != nil {
		t.Fatalf("cleanup called = %v, context error = %v; want cleanup on a live context", cleanupCalled, cleanupContextErr)
	}
}

type fakeEmbeddingsSettingsCheckClient struct {
	embed func(ctx context.Context, texts []string) ([][]float32, error)
}

func (f *fakeEmbeddingsSettingsCheckClient) Embed(
	ctx context.Context,
	texts []string,
) ([][]float32, error) {
	if f.embed != nil {
		return f.embed(ctx, texts)
	}
	return [][]float32{{0.1, 0.2}}, nil
}

type fakeAISettingsCheckClient struct {
	chat       func(ctx context.Context, messages []llm.Message, jsonObject bool) (string, error)
	transcribe func(ctx context.Context, req llm.TranscribeRequest) (*llm.Transcription, error)
}

func (f *fakeAISettingsCheckClient) Chat(
	ctx context.Context,
	messages []llm.Message,
	jsonObject bool,
) (string, error) {
	if f.chat != nil {
		return f.chat(ctx, messages, jsonObject)
	}
	return `{"status":"ok"}`, nil
}

func (f *fakeAISettingsCheckClient) Transcribe(
	ctx context.Context,
	req llm.TranscribeRequest,
) (*llm.Transcription, error) {
	if f.transcribe != nil {
		return f.transcribe(ctx, req)
	}
	return &llm.Transcription{Segments: []llm.TranscriptionSegment{{Start: 0, End: 1, Text: "Subtitle test."}}}, nil
}

// testAIProviders encodes an ai.providers registry for a settings fixture.
func testAIProviders(t *testing.T, providers ...config.AIProvider) string {
	t.Helper()
	return config.AIProviderRegistry{Version: 1, Providers: providers}.Encode()
}

func TestAITranscriptionConnectionRequiresTimestampedSpeech(t *testing.T) {
	originalFactory := newAdminAISettingsCheckClient
	t.Cleanup(func() { newAdminAISettingsCheckClient = originalFactory })
	for _, tc := range []struct {
		name   string
		result *llm.Transcription
		want   bool
	}{
		{name: "nil response"},
		{name: "empty segments", result: &llm.Transcription{}},
		{name: "blank text", result: &llm.Transcription{Segments: []llm.TranscriptionSegment{{Start: 0, End: 1, Text: " "}}}},
		{name: "no timestamps", result: &llm.Transcription{Segments: []llm.TranscriptionSegment{{Text: "Test"}}}},
		{name: "timestamped speech", result: &llm.Transcription{Segments: []llm.TranscriptionSegment{{Start: 0, End: 1, Text: "Test"}}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newAdminAISettingsCheckClient = func(llm.Config) aiSettingsCheckClient {
				return &fakeAISettingsCheckClient{transcribe: func(_ context.Context, req llm.TranscribeRequest) (*llm.Transcription, error) {
					if !bytes.Equal(req.Audio[:4], []byte("RIFF")) || len(bytes.Trim(req.Audio[44:], "\x00")) == 0 {
						t.Fatal("connection probe must contain audio, not silence")
					}
					return tc.result, nil
				}}
			}
			cfg := &config.Config{}
			cfg.AI.ASRBaseURL = "https://openrouter.ai/api/v1"
			cfg.AI.ASRModel = "openai/whisper-large-v3"
			if got := checkAITranscriptionConnection(t.Context(), cfg); got.Success != tc.want {
				t.Fatalf("connection result = %+v, want success = %v", got, tc.want)
			}
		})
	}
}

func TestAdminGetEffectiveSettingsReturnsRuntimeDefaultsAndRedactsSecrets(t *testing.T) {
	handler := &AdminHandler{
		SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
			"server.log_level":         "debug",
			"tmdb.api_key":             "never-return-this",
			"clientip.trusted_proxies": "10.0.0.0/8",
		}},
		BootstrapSensitiveConfigured: map[string]bool{
			"clientip.trusted_proxies": true,
			"redis.url":                true,
		},
		BootstrapSensitiveValues: map[string]string{
			"clientip.trusted_proxies": "192.0.2.0/24, 2001:db8::/32",
			"redis.url":                "redis://private.example.invalid:6379",
		},
	}

	values := effectiveSettingsOf(t, handler)

	if values["server.log_level"] != "debug" {
		t.Fatalf("server.log_level = %q, want debug", values["server.log_level"])
	}
	if values["database.max_connections"] != "20" || values["playback.transcode_enabled"] != "true" {
		t.Fatalf("runtime defaults missing: max_connections=%q transcode_enabled=%q", values["database.max_connections"], values["playback.transcode_enabled"])
	}
	if _, leaked := values["tmdb.api_key"]; leaked {
		t.Fatal("effective settings leaked tmdb.api_key")
	}
	if got := values["clientip.trusted_proxies"]; got != "192.0.2.0/24, 2001:db8::/32" {
		t.Fatalf("clientip.trusted_proxies = %q, want active environment value", got)
	}
}

// The artwork reconcile checkpoint is written only by the server: no read
// returns it and no write accepts it.
func TestAdminSettingsMachineManagedCheckpoint(t *testing.T) {
	const (
		storedCheckpoint      = `{"baseline_identity":"old","target_identity":"new"}`
		replacementCheckpoint = `{"baseline_identity":"wrong","target_identity":"wrong"}`
		key                   = config.ArtworkStorageReconcileCheckpointKey
	)
	newHandler := func() (*AdminHandler, *fakeServerSettingsStore) {
		store := &fakeServerSettingsStore{values: map[string]string{"server.log_level": "debug", key: storedCheckpoint}}
		return &AdminHandler{SettingsRepo: store}, store
	}

	handler, _ := newHandler()
	for _, effective := range []bool{false, true} {
		values, err := handler.InspectAdminSettings(t.Context(), effective)
		if err != nil {
			t.Fatal(err)
		}
		if _, leaked := values[key]; leaked || values["server.log_level"] != "debug" {
			t.Fatalf("effective=%v read = %#v, want the checkpoint hidden", effective, values)
		}
	}
	_, err := handler.ReadAdminSetting(t.Context(), key)
	requireAdminError(t, err, http.StatusNotFound, "")

	for _, single := range []bool{false, true} {
		handler, store := newHandler()
		requireAdminError(t, saveAdminSetting(t, handler, key, replacementCheckpoint, single), http.StatusBadRequest, "")
		if store.values[key] != storedCheckpoint || store.atomicCalls != 0 {
			t.Fatalf("single=%v: checkpoint = %q after %d atomic calls, want it untouched", single, store.values[key], store.atomicCalls)
		}
	}
}

// Validation sees values the environment supplies, so a write next to one
// that completes a pair succeeds; a write of the environment's own key does
// not.
func TestAdminSettingsEnvironmentManagedValues(t *testing.T) {
	for _, single := range []bool{false, true} {
		store := &fakeServerSettingsStore{values: map[string]string{"watchsync.simkl.client_id": "configured-client-id"}}
		handler := &AdminHandler{
			SettingsRepo:                 store,
			BootstrapSensitiveConfigured: map[string]bool{"watchsync.simkl.client_secret": true},
			BootstrapSensitiveValues:     map[string]string{"watchsync.simkl.client_secret": "clawrouter-e2e-secret"},
		}
		if err := saveAdminSetting(t, handler, "branding.server_name", "Casa", single); err != nil {
			t.Fatalf("single=%v: %v", single, err)
		}
		if store.values["branding.server_name"] != "Casa" {
			t.Fatalf("single=%v: branding.server_name = %q, want Casa", single, store.values["branding.server_name"])
		}

		store = &fakeServerSettingsStore{values: map[string]string{}}
		handler = &AdminHandler{SettingsRepo: store, BootstrapSensitiveConfigured: map[string]bool{"clientip.trusted_proxies": true}}
		requireAdminError(t, saveAdminSetting(t, handler, "clientip.trusted_proxies", "10.0.0.0/8", single), http.StatusBadRequest, "managed_by_environment")
		if store.setManyCalls != 0 || store.setCalls != 0 {
			t.Fatalf("single=%v: environment-managed value was written", single)
		}
	}
}

func TestAdminUpdateSettingsCommitsOneValidatedBatch(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	handler := &AdminHandler{SettingsRepo: settings, RestartStatus: NewServerRestartStatusTracker()}

	response, err := handler.UpdateAdminSettings(t.Context(), map[string]string{"database.max_connections": " 40 ", "branding.server_name": "Casa"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if settings.setManyCalls != 1 || settings.values["database.max_connections"] != "40" || settings.values["branding.server_name"] != "Casa" {
		t.Fatalf("SetMany calls = %d, stored = %#v", settings.setManyCalls, settings.values)
	}
	if !response.RestartRequired || len(response.RestartRequiredKeys) != 1 || response.RestartRequiredKeys[0] != "database.max_connections" {
		t.Fatalf("response = %#v", response)
	}
}

func TestAdminUpdateSettingsRejectsWholeBatchBeforeWrite(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{"branding.server_name": "Silo"}}
	handler := &AdminHandler{SettingsRepo: settings}

	_, err := handler.UpdateAdminSettings(t.Context(), map[string]string{"branding.server_name": "Casa", "database.max_connections": "0"}, nil)
	requireAdminError(t, err, http.StatusBadRequest, "")
	if settings.setManyCalls != 0 || settings.values["branding.server_name"] != "Silo" {
		t.Fatalf("valid sibling value was partially persisted: %#v", settings.values)
	}
}

func TestAdminUpdateSettingValidatesRoutingAgainstStoredPeer(t *testing.T) {
	for _, test := range []struct {
		name      string
		storedKey string
		stored    string
		key       string
		value     string
	}{
		{
			name: "remux execution against proxy-only egress", storedKey: config.PlaybackRoutingRemuxEgressSettingKey,
			stored: string(config.PlaybackEgressProxyOnly), key: config.PlaybackRoutingRemuxExecutionSettingKey,
			value: string(config.PlaybackExecutionAPIOnly),
		},
		{
			name: "remux egress against API-only execution", storedKey: config.PlaybackRoutingRemuxExecutionSettingKey,
			stored: string(config.PlaybackExecutionAPIOnly), key: config.PlaybackRoutingRemuxEgressSettingKey,
			value: string(config.PlaybackEgressProxyOnly),
		},
		{
			name: "video execution against proxy-only egress", storedKey: config.PlaybackRoutingVideoTranscodeEgressSettingKey,
			stored: string(config.PlaybackEgressProxyOnly), key: config.PlaybackRoutingVideoTranscodeExecutionSettingKey,
			value: string(config.PlaybackExecutionAPIOnly),
		},
		{
			name: "video egress against API-only execution", storedKey: config.PlaybackRoutingVideoTranscodeExecutionSettingKey,
			stored: string(config.PlaybackExecutionAPIOnly), key: config.PlaybackRoutingVideoTranscodeEgressSettingKey,
			value: string(config.PlaybackEgressProxyOnly),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: map[string]string{test.storedKey: test.stored}}
			handler := &AdminHandler{SettingsRepo: settings}

			_, err := handler.UpdateAdminSetting(t.Context(), test.key, test.value, nil)
			requireAdminError(t, err, http.StatusBadRequest, adminInvalidSettingsCode)
			if settings.atomicCalls != 1 || settings.setManyCalls != 0 {
				t.Fatalf("atomic calls=%d writes=%d, want 1 and 0", settings.atomicCalls, settings.setManyCalls)
			}
			if _, exists := settings.values[test.key]; exists {
				t.Fatalf("invalid routing setting was persisted: %#v", settings.values)
			}
			if _, err := config.LoadFromDB(settings.values); err != nil {
				t.Fatalf("rejected write left restart-invalid settings: %v", err)
			}
		})
	}
}

func TestAdminUpdateSettingCanRepairLegacyInvalidRoutingPair(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{
		config.PlaybackRoutingRemuxExecutionSettingKey: string(config.PlaybackExecutionAPIOnly),
		config.PlaybackRoutingRemuxEgressSettingKey:    string(config.PlaybackEgressProxyOnly),
	}}
	handler := &AdminHandler{SettingsRepo: settings}

	if _, err := handler.UpdateAdminSetting(t.Context(), config.PlaybackRoutingRemuxExecutionSettingKey, "worker_only", nil); err != nil {
		t.Fatalf("repair refused: %v", err)
	}
	if _, err := config.LoadFromDB(settings.values); err != nil {
		t.Fatalf("repaired settings are restart-invalid: %v", err)
	}
}

func TestAdminGenericSettingsWritesRejectUnsafeRateLimitValues(t *testing.T) {
	for _, single := range []bool{false, true} {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		err := saveAdminSetting(t, &AdminHandler{SettingsRepo: settings}, "ratelimit.global.requests_per_second", "1e308", single)
		requireAdminError(t, err, http.StatusBadRequest, "")
		if settings.setManyCalls != 0 || settings.setCalls != 0 {
			t.Fatalf("single=%v: invalid rate wrote settings", single)
		}
	}
}

func TestAdminSettingsRejectClearingOnlyRedisTransport(t *testing.T) {
	newStore := func() *fakeServerSettingsStore {
		return &fakeServerSettingsStore{values: map[string]string{
			"ratelimit.backend": "redis",
			"redis.url":         "redis://cache.example.invalid:6379",
		}}
	}
	for _, single := range []bool{false, true} {
		settings := newStore()
		requireAdminError(t, saveAdminSetting(t, &AdminHandler{SettingsRepo: settings}, "redis.url", "", single), http.StatusBadRequest, "")
		if settings.setManyCalls != 0 || settings.values["redis.url"] == "" {
			t.Fatalf("single=%v: invalid clear was persisted: %#v", single, settings.values)
		}
	}

	// A Redis URL from the environment keeps the transport, so the stored one
	// may be cleared.
	settings := newStore()
	handler := &AdminHandler{SettingsRepo: settings, RedisBootstrapAvailable: true}
	if err := saveAdminSetting(t, handler, "redis.url", "", false); err != nil {
		t.Fatal(err)
	}
	if settings.setManyCalls != 1 || settings.values["redis.url"] != "" {
		t.Fatalf("clear with bootstrap transport was not persisted: %#v", settings.values)
	}
}

func TestAdminUpdateSettingsSkipsFunctionalNoOp(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	handler := &AdminHandler{SettingsRepo: settings, RestartStatus: NewServerRestartStatusTracker()}

	response, err := handler.UpdateAdminSettings(t.Context(), map[string]string{"database.max_connections": "20"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if settings.setManyCalls != 0 {
		t.Fatalf("SetMany calls = %d, want 0 for an effective-default no-op", settings.setManyCalls)
	}
	if response.RestartRequired || len(response.RestartRequiredKeys) != 0 {
		t.Fatalf("no-op response requested restart: %#v", response)
	}
}

func TestAdminUpdateSettingsReturnsEffectiveDefaultAfterClear(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{"branding.server_name": "Casa"}}
	var callbackValue string
	handler := &AdminHandler{
		SettingsRepo:           settings,
		OnServerSettingUpdated: func(_ context.Context, _ string, value string) { callbackValue = value },
	}

	response, err := handler.UpdateAdminSettings(t.Context(), map[string]string{"branding.server_name": ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Values["branding.server_name"] != "Silo" || callbackValue != "Silo" {
		t.Fatalf("response values = %#v, callback = %q, want the effective default", response.Values, callbackValue)
	}
	if settings.values["branding.server_name"] != "" {
		t.Fatalf("stored value = %q, want cleared override", settings.values["branding.server_name"])
	}
}

func TestAdminUpdateSettingPersistsClearWhenOverrideEqualsDefault(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{"branding.server_name": "Silo"}}
	handler := &AdminHandler{SettingsRepo: settings}

	response, err := handler.UpdateAdminSetting(t.Context(), "branding.server_name", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if settings.setManyCalls != 1 || settings.values["branding.server_name"] != "" {
		t.Fatalf("writes = %d, stored = %q, want one explicit clear", settings.setManyCalls, settings.values["branding.server_name"])
	}
	if response.Value != "Silo" || response.RestartRequired {
		t.Fatalf("response = %#v, want unchanged effective default without restart", response)
	}
}

func TestAdminUpdateSettingPreservesLegacyPairedWriteFlow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial map[string]string
		key     string
		value   string
	}{
		{name: "establish first half of pair", key: "s3.public_endpoint", value: "https://s3.example.invalid"},
		{
			name:    "unrelated update with legacy partial pair",
			initial: map[string]string{"s3.public_endpoint": "https://s3.example.invalid"},
			key:     "branding.server_name", value: "Casa",
		},
		{
			name: "clear first half of pair",
			initial: map[string]string{
				"watchsync.simkl.client_id":     "configured-client-id",
				"watchsync.simkl.client_secret": "clawrouter-e2e-secret",
			},
			key: "watchsync.simkl.client_id", value: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: maps.Clone(tc.initial)}
			if settings.values == nil {
				settings.values = map[string]string{}
			}
			if _, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSetting(t.Context(), tc.key, tc.value, nil); err != nil {
				t.Fatal(err)
			}
			if settings.values[tc.key] != tc.value {
				t.Fatalf("stored %s = %q, want %q", tc.key, settings.values[tc.key], tc.value)
			}
		})
	}
}

// A check draft selects a provider by id, so a stored key reaches only the
// address saved with it. A draft cannot substitute its own registry.
func TestCheckSettingsConnectionAIUsesOnlyStoredProviders(t *testing.T) {
	originalAI, originalEmbeddings := newAdminAISettingsCheckClient, newAdminEmbeddingsSettingsCheckClient
	t.Cleanup(func() {
		newAdminAISettingsCheckClient, newAdminEmbeddingsSettingsCheckClient = originalAI, originalEmbeddings
	})
	var capturedAI llm.Config
	var capturedEmbeddings embeddings.ClientConfig
	newAdminAISettingsCheckClient = func(cfg llm.Config) aiSettingsCheckClient {
		capturedAI = cfg
		return &fakeAISettingsCheckClient{}
	}
	newAdminEmbeddingsSettingsCheckClient = func(cfg embeddings.ClientConfig) embeddingsSettingsCheckClient {
		capturedEmbeddings = cfg
		return &fakeEmbeddingsSettingsCheckClient{embed: func(context.Context, []string) ([][]float32, error) {
			return [][]float32{make([]float32, 8)}, nil
		}}
	}
	both := []config.AICapability{config.AICapabilityChat, config.AICapabilityEmbeddings, config.AICapabilitySpeech}
	stored := map[string]string{
		config.AIProvidersSettingKey: testAIProviders(t,
			config.AIProvider{ID: "saved", Type: config.AIProviderCustom, Name: "Saved", BaseURL: "https://saved.example.test", APIKey: "saved-secret", Capabilities: both},
			config.AIProvider{ID: "other", Type: config.AIProviderCustom, Name: "Other", BaseURL: "https://other.example.test", APIKey: "other-secret", Capabilities: both},
		),
		config.AITextProviderSettingKey:    "saved",
		config.AISpeechProviderSettingKey:  "saved",
		config.EmbeddingProviderSettingKey: "saved",
		"ai.chat_model":                    "chat-model",
		"ai.asr_model":                     "whisper-model",
		"recommendations.embedding_model":  "embed-model",
		// Rows older builds wrote are no longer read.
		"recommendations.embedding_base_url":   "https://legacy.example.test",
		"recommendations.embedding_auth_token": "legacy-secret",
	}
	forged := testAIProviders(t, config.AIProvider{ID: "saved", Type: config.AIProviderCustom, Name: "Saved", BaseURL: "https://attacker.example.test", Capabilities: both})
	for _, tc := range []struct {
		name, kind, wantURL, wantKey string
		values                       map[string]string
	}{
		{name: "text uses the bound provider", kind: "ai_chat", wantURL: "https://saved.example.test", wantKey: "saved-secret"},
		{name: "text draft switches provider", kind: "ai_chat", values: map[string]string{config.AITextProviderSettingKey: "other"}, wantURL: "https://other.example.test", wantKey: "other-secret"},
		{name: "text draft registry ignored", kind: "ai_chat", values: map[string]string{config.AIProvidersSettingKey: forged}, wantURL: "https://saved.example.test", wantKey: "saved-secret"},
		{name: "speech uses the bound provider", kind: "ai_transcription", wantURL: "https://saved.example.test", wantKey: "saved-secret"},
		{name: "speech draft switches provider", kind: "ai_transcription", values: map[string]string{config.AISpeechProviderSettingKey: "other"}, wantURL: "https://other.example.test", wantKey: "other-secret"},
		{name: "speech draft registry ignored", kind: "ai_transcription", values: map[string]string{config.AIProvidersSettingKey: forged}, wantURL: "https://saved.example.test", wantKey: "saved-secret"},
		{name: "embeddings use the bound provider", kind: "recommendations_embedding", wantURL: "https://saved.example.test", wantKey: "saved-secret"},
		{name: "embeddings draft switches provider", kind: "recommendations_embedding", values: map[string]string{config.EmbeddingProviderSettingKey: "other"}, wantURL: "https://other.example.test", wantKey: "other-secret"},
		{name: "embeddings draft registry ignored", kind: "recommendations_embedding", values: map[string]string{config.AIProvidersSettingKey: forged}, wantURL: "https://saved.example.test", wantKey: "saved-secret"},
		{name: "embeddings draft legacy endpoint ignored", kind: "recommendations_embedding", values: map[string]string{"recommendations.embedding_base_url": "https://attacker.example.test"}, wantURL: "https://saved.example.test", wantKey: "saved-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capturedAI, capturedEmbeddings = llm.Config{}, embeddings.ClientConfig{}
			handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: maps.Clone(stored)}}
			dirty := slices.Collect(maps.Keys(tc.values))
			result, err := handler.CheckAdminSettingsConnection(t.Context(), tc.kind, tc.values, dirty)
			if err != nil || !result.Success {
				t.Fatalf("result = %+v, %v", result, err)
			}
			gotURL, gotKey := capturedAI.BaseURL, capturedAI.APIKey
			switch tc.kind {
			case "ai_transcription":
				gotURL, gotKey = capturedAI.ASRBaseURL, capturedAI.ASRAPIKey
			case "recommendations_embedding":
				gotURL, gotKey = capturedEmbeddings.BaseURL, capturedEmbeddings.APIKey
			}
			if gotURL != tc.wantURL || gotKey != tc.wantKey {
				t.Fatalf("checked %q with %q, want %q with %q", gotURL, gotKey, tc.wantURL, tc.wantKey)
			}
		})
	}
}

func TestCheckSettingsConnectionAITranscriptionProbe(t *testing.T) {
	originalFactory := newAdminAISettingsCheckClient
	t.Cleanup(func() { newAdminAISettingsCheckClient = originalFactory })

	var request llm.TranscribeRequest
	newAdminAISettingsCheckClient = func(llm.Config) aiSettingsCheckClient {
		return &fakeAISettingsCheckClient{transcribe: func(ctx context.Context, req llm.TranscribeRequest) (*llm.Transcription, error) {
			request = req
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) < req.Timeout || time.Until(deadline) > 80*time.Second {
				t.Error("outer check deadline does not cover the transcription request")
			}
			return &llm.Transcription{Segments: []llm.TranscriptionSegment{{Start: 0, End: 1, Text: "Subtitle test."}}}, nil
		}}
	}
	handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
		config.AIProvidersSettingKey: testAIProviders(t, config.AIProvider{
			ID: "whisper", Type: config.AIProviderWhisper, Name: "Whisper", BaseURL: "http://whisper.example.test",
			Capabilities: []config.AICapability{config.AICapabilitySpeech},
		}),
		config.AISpeechProviderSettingKey: "whisper",
		"ai.asr_model":                    "whisper-model",
	}}}

	result, err := handler.CheckAdminSettingsConnection(t.Context(), "ai_transcription", nil, nil)
	if err != nil || !result.Success {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if len(request.Audio) == 0 || request.Filename == "" || request.Timeout <= 60*time.Second || request.Timeout > 75*time.Second {
		t.Fatalf("transcription probe request = %+v, want bounded WAV probe", request)
	}
}

// An unchanged secret comes from storage, so a draft with blank credentials
// still checks with the saved ones.
func TestCheckSettingsConnectionS3UsesPersistedSensitiveValues(t *testing.T) {
	originalFactory := newAdminS3SettingsCheckClient
	t.Cleanup(func() { newAdminS3SettingsCheckClient = originalFactory })
	var captured s3client.BucketConfig
	newAdminS3SettingsCheckClient = func(cfg s3client.BucketConfig) s3SettingsCheckClient {
		captured = cfg
		return &fakeS3SettingsCheckClient{}
	}
	handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
		"s3.public_endpoint":   "https://persisted.example.test",
		"s3.public_bucket":     "silo",
		"s3.public_key_prefix": "persisted/prefix",
		"s3.public_access_key": "persisted-access",
		"s3.public_secret_key": "persisted-secret",
	}}}

	result, err := handler.CheckAdminSettingsConnection(t.Context(), "s3_public", map[string]string{
		"s3.public_endpoint":   "https://draft.example.test",
		"s3.public_access_key": "",
		"s3.public_secret_key": "",
	}, []string{"s3.public_endpoint"})
	if err != nil || !result.Success {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if captured.Endpoint != "https://draft.example.test" || captured.KeyPrefix != "persisted/prefix" ||
		captured.AccessKey != "persisted-access" || captured.SecretKey != "persisted-secret" {
		t.Fatalf("checked %+v, want the draft endpoint with the persisted prefix and credentials", captured)
	}
}

// A sensitive setting is never read back, and a write of one answers without
// the value.
func TestAdminSettingsSensitiveValuesStayOutOfResponses(t *testing.T) {
	const key = "watchsync.simkl.client_secret"
	handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{key: "stored-watch-provider-secret"}}}
	_, err := handler.ReadAdminSetting(t.Context(), key)
	requireAdminError(t, err, http.StatusNotFound, "")

	const submittedSecret = "submitted-watch-provider-secret"
	settings := &fakeServerSettingsStore{values: map[string]string{"watchsync.simkl.client_id": "configured-client-id"}}
	response, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSetting(t.Context(), key, submittedSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if settings.values[key] != submittedSecret {
		t.Fatalf("stored value = %q, want submitted secret", settings.values[key])
	}
	if response.Key != key || response.Value != "" {
		t.Fatalf("response = %+v, want key with empty value", response)
	}
	batch, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSettings(t.Context(), map[string]string{key: submittedSecret + "-2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := batch.Values[key]; leaked {
		t.Fatalf("batch response = %#v, want the secret left out", batch.Values)
	}
}

func TestAdminUpdateSettingReportsRestartRequired(t *testing.T) {
	for _, tc := range []struct {
		key             string
		value           string
		restartRequired bool
	}{
		// Infrastructure settings are captured at startup.
		{key: "database.max_connections", value: "40", restartRequired: true},
		{key: "s3.public_bucket", value: "assets", restartRequired: true},
		{key: "jellyfin_compat.enabled", value: "false", restartRequired: true},
		// Branding is read live from the settings repo per request.
		{key: "branding.server_name", value: "Casa"},
		{key: "policy.editor_enabled", value: "true"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			initial := map[string]string{}
			if tc.key == "s3.public_bucket" {
				initial["s3.public_endpoint"] = "https://s3.example.test"
			}
			restartStatus := NewServerRestartStatusTracker()
			handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: initial}, RestartStatus: restartStatus}

			response, err := handler.UpdateAdminSetting(t.Context(), tc.key, tc.value, nil)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := restartStatus.Snapshot()
			if response.RestartRequired != tc.restartRequired || snapshot.RestartRequired != tc.restartRequired {
				t.Fatalf("restart required: response %v tracker %v, want %v", response.RestartRequired, snapshot.RestartRequired, tc.restartRequired)
			}
			if !tc.restartRequired && snapshot.RestartRequiredReason != "" {
				t.Fatalf("tracker reason = %q, want empty", snapshot.RestartRequiredReason)
			}
		})
	}
}

// A binding is validated against the registry even though the batch does not
// carry the registry, which the dependency group pulls into validation.
func TestAdminSettingsValidateProviderBindings(t *testing.T) {
	registry := testAIProviders(t,
		config.AIProvider{ID: "router", Type: config.AIProviderOpenRouter, Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", APIKey: "k",
			Capabilities: []config.AICapability{config.AICapabilityChat, config.AICapabilitySpeech}},
		config.AIProvider{ID: "chat-only", Type: config.AIProviderCustom, Name: "Chat", BaseURL: "http://llm.lan", Capabilities: []config.AICapability{config.AICapabilityChat}},
	)
	newHandler := func() *AdminHandler {
		return &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{config.AIProvidersSettingKey: registry}}}
	}
	for value, wantErr := range map[string]bool{"router": false, "chat-only": true, "missing": true} {
		if _, err := newHandler().UpdateAdminSetting(t.Context(), config.AISpeechProviderSettingKey, value, nil); (err != nil) != wantErr {
			t.Fatalf("single write of speech provider %q: err = %v, want error %v", value, err, wantErr)
		}
	}
	for _, tc := range []struct {
		values  map[string]string
		wantErr bool
	}{
		{values: map[string]string{"ai.speech_provider_id": "router", "ai.asr_model": "openai/whisper-large-v3", "subtitle_ai.transcribe_enabled": "true"}},
		{values: map[string]string{"ai.speech_provider_id": "chat-only"}, wantErr: true},
		{values: map[string]string{"ai.text_provider_id": "missing"}, wantErr: true},
		{values: map[string]string{"ai.asr_base_url": "https://openrouter.ai/api/v1"}, wantErr: true},
	} {
		_, err := newHandler().UpdateAdminSettings(t.Context(), tc.values, nil)
		if !tc.wantErr {
			if err != nil {
				t.Fatalf("%v: %v", tc.values, err)
			}
			continue
		}
		requireAdminError(t, err, http.StatusBadRequest, "")
	}
}
