package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeenrollment"
)

func TestCoreEnrollmentRequiresOperatorOptIn(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://customer.invalid/silo")
	t.Setenv("REDIS_URL", "redis://customer.invalid/2")
	t.Setenv("SECRET_KEY", strings.Repeat("s", 48))
	t.Setenv("MODE", "integrated")
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "customer-jwt-secret"
	for _, value := range []string{"", "false", "true"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SILO_NODE_ENROLLMENT_ENABLED", value)
			bc, err := config.LoadBootstrap("")
			if err != nil {
				t.Fatal(err)
			}
			client := nodeEnrollmentClient(bc, cfg)
			if value != "true" {
				if client != nil {
					t.Fatal("application admin can access host secrets without operator opt-in")
				}
				return
			}
			if client == nil || client.Bootstrap.DatabaseURL != bc.DatabaseURL || client.Bootstrap.RedisURL != bc.RedisURL || client.Bootstrap.SecretKey != string(bc.SecretKey) || client.JWTSecret != cfg.Auth.JWTSecret {
				t.Fatal("opted-in core cannot hand off customer configuration")
			}
		})
	}
}

func TestPersistedBindingResumesCustomerStartup(t *testing.T) {
	b := nodeenrollment.Binding{DatabaseURL: "postgres://customer.invalid/silo", RedisURL: "redis://customer.invalid/2", SecretKey: strings.Repeat("s", 48), NodeURL: "https://worker.example", NodeName: "Customer node"}
	store := nodeenrollment.Store{Path: filepath.Join(t.TempDir(), "private", "binding.json")}
	if err := store.Save(b); err != nil {
		t.Fatal(err)
	}
	bc := &config.BootstrapConfig{Mode: "transcode", BindingFile: store.Path, TranscodeMaxJobs: 4}
	if err := completeNodeBootstrap(t.Context(), bc); err != nil {
		t.Fatal(err)
	}
	if bc.DatabaseURL != b.DatabaseURL || string(bc.SecretKey) != b.SecretKey || bc.NodeURL != b.NodeURL || bc.TranscodeMaxJobs != 4 {
		t.Fatal("restart lost binding or changed provider ceiling")
	}
	bc.DatabaseURL = "postgres://another.invalid/silo"
	if err := completeNodeBootstrap(t.Context(), bc); err == nil {
		t.Fatal("binding silently changed customer")
	}
}
func TestUnboundListenerHandsCredentialsToStartup(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bc := &config.BootstrapConfig{Mode: "transcode", EnrollmentToken: strings.Repeat("p", 48), BindingFile: filepath.Join(t.TempDir(), "private", "binding.json")}
	done := make(chan error, 1)
	go func() {
		done <- waitForNodeBinding(t.Context(), bc, nodeenrollment.Store{Path: bc.BindingFile}, func(context.Context, nodeenrollment.Binding) error { return nil }, ln)
	}()
	// The core client verifies normal runtime readiness after binding. Here the
	// pre-DB listener alone is exercised directly with a successful handoff.
	if err := postTestBinding(ln.Addr().String(), bc.EnrollmentToken); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker never resumed startup")
	}
	if bc.DatabaseURL == "" || len(bc.SecretKey) == 0 {
		t.Fatal("bootstrap did not receive customer credentials")
	}
}

func postTestBinding(address, token string) error {
	b := nodeenrollment.Binding{DatabaseURL: "postgres://customer.invalid/silo", SecretKey: strings.Repeat("s", 48), NodeURL: "http://" + address, NodeName: "Customer node"}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+address+nodeenrollment.EnrollmentPath, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return errors.New("binding handoff rejected")
	}
	return nil
}

func TestUnboundBindingConflictDoesNotConsumeToken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	bc := &config.BootstrapConfig{Mode: "transcode", EnrollmentToken: strings.Repeat("p", 48), RedisURL: "redis://provider.internal", BindingFile: filepath.Join(t.TempDir(), "private", "binding.json")}
	done := make(chan error, 1)
	go func() {
		done <- waitForNodeBinding(ctx, bc, nodeenrollment.Store{Path: bc.BindingFile}, func(context.Context, nodeenrollment.Binding) error { return nil }, ln)
	}()
	b := nodeenrollment.Binding{DatabaseURL: "postgres://customer.invalid/silo", RedisURL: "redis://customer.internal", SecretKey: strings.Repeat("s", 48), NodeURL: "http://" + ln.Addr().String(), NodeName: "Sold node"}
	post := func(b nodeenrollment.Binding) int {
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.NodeURL+nodeenrollment.EnrollmentPath, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bc.EnrollmentToken)
		resp, err := (&http.Client{Timeout: time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if status := post(b); status != http.StatusUnprocessableEntity {
		t.Fatalf("conflicting bootstrap was saved: %d", status)
	}
	if binding, err := (nodeenrollment.Store{Path: bc.BindingFile}).Load(); err != nil || binding != nil {
		t.Fatal("failed validation consumed binding", err)
	}
	b.RedisURL = bc.RedisURL
	if status := post(b); status != http.StatusOK {
		t.Fatal("corrected request could not use token", status)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
