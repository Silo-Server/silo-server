package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/nodeenrollment"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// This optional integration test uses only disposable customer services. The
// child starts without DB/Redis/SECRET_KEY, then runs the actual worker binary.
func TestAutomaticNodeEnrollmentProcessAndRestart(t *testing.T) {
	dsn, redisURL, binary := os.Getenv("SILO_TEST_DATABASE_URL"), os.Getenv("SILO_TEST_REDIS_URL"), os.Getenv("SILO_NODE_ENROLLMENT_TEST_BINARY")
	if dsn == "" || redisURL == "" || binary == "" {
		t.Skip("disposable DB, Redis and built worker required")
	}
	const master = "enrollment-test-master-key-only-0123456789"
	const jwt = "enrollment-test-jwt-secret-only-0123456789"
	const token = "enrollment-test-provider-token-0123456789"
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pool, err := database.NewPoolForRole(ctx, config.DatabaseConfig{URL: dsn, MaxConnections: 4}, "application")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cipher, err := secret.New([]byte(master))
	if err != nil {
		t.Fatal(err)
	}
	settings := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher)
	for k, v := range map[string]string{"auth.jwt_secret": jwt, "redis.url": redisURL, "playback.transcode_dir": filepath.Join(t.TempDir(), "transcodes"), "playback.hw_accel": "none"} {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	b := nodeenrollment.Binding{DatabaseURL: dsn, SecretKey: master, NodeURL: "http://127.0.0.1:1", NodeName: "Sold node"}
	if err := validateCustomerBinding(ctx, b); err != nil {
		t.Fatal("valid DB settings and DB-derived Redis rejected", err)
	}
	b.SecretKey = strings.Repeat("x", 48)
	if err := validateCustomerBinding(ctx, b); err == nil {
		t.Fatal("wrong shared key accepted")
	}
	b.SecretKey = master
	b.RedisURL = "redis://127.0.0.1:1"
	rejectedCtx, rejectedCancel := context.WithTimeout(ctx, 2*time.Second)
	if err := validateCustomerBinding(rejectedCtx, b); err == nil {
		t.Fatal("unreachable Redis accepted")
	}
	rejectedCancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	_ = listener.Close()
	url := "http://" + addr
	directory := t.TempDir()
	bindingPath := filepath.Join(directory, "private", "binding.json")
	start := func(enrollmentToken string) func() {
		t.Helper()
		logFile, err := os.CreateTemp(directory, "worker-*.log")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, binary, "-env", filepath.Join(directory, "missing.env"))
		cmd.Dir = directory
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			switch key {
			case "DATABASE_URL", "SECRET_KEY", "REDIS_URL", "MODE", "PORT", "NODE_URL", "NODE_NAME", "SILO_NODE_BINDING_FILE", "SILO_NODE_ENROLLMENT_TOKEN", "SILO_TRANSCODE_MAX_JOBS":
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
		cmd.Env = append(cmd.Env, "MODE=transcode", "PORT="+port, "SILO_NODE_BINDING_FILE="+bindingPath, "SILO_NODE_ENROLLMENT_TOKEN="+enrollmentToken, "SILO_TRANSCODE_MAX_JOBS=3")
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(7 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			_ = logFile.Close()
		}
		t.Cleanup(stop)
		httpClient := &http.Client{Timeout: time.Second}
		deadline := time.Now().Add(15 * time.Second)
		for {
			resp, err := httpClient.Get(url + "/api/v1/health")
			if err == nil {
				var health struct {
					Status string `json:"status"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&health)
				_ = resp.Body.Close()
				if health.Status != "" {
					break
				}
			}
			if time.Now().After(deadline) {
				stop()
				logs, _ := os.ReadFile(logFile.Name())
				t.Fatalf("worker did not listen: %s", logs)
			}
			select {
			case <-ctx.Done():
				stop()
				t.Fatal(ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
		return stop
	}
	stop := start(token)
	store := nodepool.NewAdminConfigurationStore(pool)
	h := &handlers.NodeHandler{}
	h.SetConfigurationStore(store)
	client := &nodeenrollment.Client{Bootstrap: nodeenrollment.Binding{DatabaseURL: dsn, SecretKey: master}, JWTSecret: jwt, ReadyTimeout: 20 * time.Second}
	h.SetEnrollmentClient(client)
	requested := 99
	input := nodepool.CreateNodeInput{Name: "Sold node", Type: "transcode", URL: url, MaxJobs: &requested}
	node, err := h.EnrollAdminNode(ctx, input, nodeenrollment.Input{Token: token})
	if err != nil {
		stop()
		t.Fatal("automatic handoff failed", err)
	}
	defer func() { _ = store.Delete(context.Background(), node.ID, func(int64) error { return nil }) }()
	if node.MaxJobs == nil || *node.MaxJobs != 3 {
		t.Fatal("provider ceiling not stored", node.MaxJobs)
	}
	saved, err := (nodeenrollment.Store{Path: bindingPath}).Load()
	if err != nil || saved == nil || saved.DatabaseURL != dsn || saved.SecretKey != master {
		t.Fatal("customer binding not persisted")
	}
	stop()
	stop = start("")
	defer stop()
	retry, err := h.EnrollAdminNode(ctx, input, nodeenrollment.Input{Token: "retired token is not needed"})
	if err != nil || retry.ID != node.ID || *retry.MaxJobs != 3 {
		t.Fatal("restart/retry did not recover registration", err)
	}
	wrong := &nodeenrollment.Client{Bootstrap: nodeenrollment.Binding{SecretKey: strings.Repeat("z", 48)}, JWTSecret: "another-customer"}
	if _, err := wrong.Enroll(ctx, url, "Other", nodeenrollment.Input{Token: token}); !errors.Is(err, nodeenrollment.ErrOtherCustomer) {
		t.Fatal("other customer accepted", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+nodeenrollment.EnrollmentPath, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatal("retired enrollment endpoint still available", resp.StatusCode)
	}
}
