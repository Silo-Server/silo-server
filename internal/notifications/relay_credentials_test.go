package notifications

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type relayDoerFunc func(*http.Request) (*http.Response, error)

func (f relayDoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

type relayRoundTripFunc func(*http.Request) (*http.Response, error)

func (f relayRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type lockedRelaySettings struct {
	mu       sync.Mutex
	values   map[string]string
	batchErr error
}

func (s *lockedRelaySettings) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *lockedRelaySettings) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *lockedRelaySettings) SetMany(_ context.Context, values map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.batchErr != nil {
		return s.batchErr
	}
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

func credentialJSON(deploymentID, apiKey string, expiresAt time.Time) string {
	return `{"request_id":"relay-request","deployment_id":"` + deploymentID +
		`","api_key":"` + apiKey + `","key_prefix":"cap_v1_test","expires_at":"` +
		expiresAt.UTC().Format(time.RFC3339) + `"}`
}

func relayResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestRotateRelayCredentialReplaysLostResponseWithStableIdempotency(t *testing.T) {
	current := PushRelayCredential{
		RelayURL:     DefaultPushRelayURL,
		DeploymentID: "deployment-rotate",
		APIKey:       "old.capability.value",
		ExpiresAt:    time.Now().Add(30 * 24 * time.Hour),
		KeyPrefix:    "cap_v1_old",
	}
	store := &lockedRelaySettings{values: map[string]string{}}
	settings := NewSettings(store)
	var keys []string
	calls := 0
	doer := relayDoerFunc(func(req *http.Request) (*http.Response, error) {
		keys = append(keys, req.Header.Get("Idempotency-Key"))
		calls++
		if calls == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return relayResponse(http.StatusOK, credentialJSON(current.DeploymentID, "new.capability.value", time.Now().Add(30*24*time.Hour))), nil
	})

	if _, err := RotateRelayCredential(context.Background(), settings, doer, current); err == nil {
		t.Fatal("first rotation unexpectedly succeeded")
	}
	result, err := RotateRelayCredential(context.Background(), settings, doer, current)
	if err != nil {
		t.Fatalf("rotation replay: %v", err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("rotation idempotency keys = %#v", keys)
	}
	if result.Credential.APIKey != "new.capability.value" {
		t.Fatalf("rotated capability = %q", result.Credential.APIKey)
	}
}

func TestRegisterRelayCredentialDoesNotPartiallyPersist(t *testing.T) {
	store := &lockedRelaySettings{
		values:   map[string]string{SettingPushRelayURL: "https://old.example"},
		batchErr: errors.New("commit failed"),
	}
	settings := NewSettings(store)
	doer := relayDoerFunc(func(*http.Request) (*http.Response, error) {
		return relayResponse(http.StatusOK, credentialJSON("deployment-new", "new.capability", time.Now().Add(30*24*time.Hour))), nil
	})
	if _, err := RegisterRelayCredential(context.Background(), settings, doer, DefaultPushRelayURL); err == nil {
		t.Fatal("registration unexpectedly persisted")
	}
	if got := store.values[SettingPushRelayURL]; got != "https://old.example" || len(store.values) != 1 {
		t.Fatalf("partial credential state = %#v", store.values)
	}
}

func TestPushSenderProactivelyRenewsOnceConcurrently(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := &lockedRelaySettings{values: map[string]string{
		SettingPushRelayURL:          DefaultPushRelayURL,
		SettingPushRelayDeploymentID: "deployment-renew",
		SettingPushRelayAPIKey:       "old.capability",
		SettingPushRelayExpiresAt:    now.Add(6 * 24 * time.Hour).Format(time.RFC3339),
		SettingPushRelayKeyPrefix:    "cap_v1_old",
	}}
	renewals := 0
	var mu sync.Mutex
	sender := newPushSender(nil, nil, nil, NewSettings(store))
	sender.now = func() time.Time { return now }
	sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != relayRenewPath {
			t.Fatalf("path = %q", req.URL.Path)
		}
		mu.Lock()
		renewals++
		mu.Unlock()
		return relayResponse(http.StatusOK, credentialJSON("deployment-renew", "renewed.capability", now.Add(30*24*time.Hour))), nil
	})}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			credential, err := sender.prepareRelayCredential(context.Background())
			if err != nil || credential.APIKey != "renewed.capability" {
				t.Errorf("credential = %+v, err = %v", credential, err)
			}
		}()
	}
	wg.Wait()
	if renewals != 1 {
		t.Fatalf("renewal requests = %d, want 1", renewals)
	}
}

func TestPushSenderMigratesOnlyLegacyRelayKeys(t *testing.T) {
	store := &lockedRelaySettings{values: map[string]string{
		SettingPushRelayURL:    DefaultPushRelayURL,
		SettingPushRelayAPIKey: "rk_legacy_database_key",
	}}
	registrations := 0
	sender := newPushSender(nil, nil, nil, NewSettings(store))
	sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != relayRegisterPath || req.Header.Get("Authorization") != "" {
			t.Fatalf("legacy migration request = %s auth=%q", req.URL.Path, req.Header.Get("Authorization"))
		}
		registrations++
		return relayResponse(http.StatusOK, credentialJSON("deployment-migrated", "modern.capability", time.Now().Add(30*24*time.Hour))), nil
	})}
	credential, err := sender.prepareRelayCredential(context.Background())
	if err != nil || credential.APIKey != "modern.capability" || registrations != 1 {
		t.Fatalf("credential = %+v, registrations = %d, err = %v", credential, registrations, err)
	}

	store.values[SettingPushRelayAPIKey] = "revoked.modern.capability"
	store.values[SettingPushRelayReregister] = "true"
	sender.settings.Invalidate(SettingPushRelayAPIKey, SettingPushRelayReregister)
	if _, err := sender.prepareRelayCredential(context.Background()); err == nil {
		t.Fatal("revoked modern capability silently re-registered")
	}
	if registrations != 1 {
		t.Fatalf("registrations after modern revocation = %d", registrations)
	}
}

func TestNormalizePushRelayURLRequiresAllowlistedOrigin(t *testing.T) {
	staging := "https://silo-push-relay-staging.arkyn.workers.dev"
	if got, err := NormalizePushRelayURL(staging, staging); err != nil || got != staging {
		t.Fatalf("staging override = %q, %v", got, err)
	}
	if _, err := NormalizePushRelayURL("https://attacker.example", staging); err == nil {
		t.Fatal("arbitrary relay origin accepted")
	}
}
