package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
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
