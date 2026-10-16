package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/nodeenrollment"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// nodeEnrollmentClient exposes host-owned bootstrap secrets to the admin
// enrollment flow only when the core operator explicitly permits handoff.
func nodeEnrollmentClient(bc *config.BootstrapConfig, getConfig func() *config.Config) *nodeenrollment.Client {
	if !bc.EnrollmentEnabled {
		return nil
	}
	return &nodeenrollment.Client{
		Bootstrap: nodeenrollment.Binding{DatabaseURL: bc.DatabaseURL, RedisURL: bc.RedisURL, SecretKey: string(bc.SecretKey)},
		CurrentJWTSecret: func() string {
			cfg := getConfig()
			if cfg == nil {
				return ""
			}
			return cfg.Auth.JWTSecret
		},
	}
}

func applyCustomerBinding(bc *config.BootstrapConfig, b nodeenrollment.Binding) error {
	if (bc.DatabaseURL != "" && bc.DatabaseURL != b.DatabaseURL) || (len(bc.SecretKey) > 0 && string(bc.SecretKey) != b.SecretKey) || (bc.RedisURL != "" && bc.RedisURL != b.RedisURL) {
		return errors.New("bootstrap conflicts with saved customer binding; provider reset required")
	}
	bc.DatabaseURL = b.DatabaseURL
	bc.RedisURL = b.RedisURL
	bc.SecretKey = []byte(b.SecretKey)
	bc.NodeURL = b.NodeURL
	bc.NodeName = b.NodeName
	return nil
}
func completeNodeBootstrap(ctx context.Context, bc *config.BootstrapConfig) error {
	if bc.Mode != transcodeMode || (bc.DatabaseURL != "" && bc.EnrollmentToken == "" && bc.BindingFile == "") {
		return nil
	}
	path := bc.BindingFile
	if path == "" {
		path = nodeenrollment.DefaultBindingFile
	}
	bc.BindingFile = path
	store := nodeenrollment.Store{Path: path}
	binding, err := store.Load()
	if err != nil {
		return err
	}
	if binding != nil {
		if err := store.ConfirmDurability(*binding); err != nil {
			return err
		}
		return applyCustomerBinding(bc, *binding)
	}
	if bc.DatabaseURL != "" {
		return nil
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", bc.Listen)
	if err != nil {
		return errors.New("node enrollment listener unavailable")
	}
	return waitForNodeBinding(ctx, bc, store, validateCustomerBinding, listener)
}
func waitForNodeBinding(ctx context.Context, bc *config.BootstrapConfig, store nodeenrollment.Store, validate func(context.Context, nodeenrollment.Binding) error, listener net.Listener) error {
	enrollment, err := nodeenrollment.NewServer(bc.EnrollmentToken, store, func(ctx context.Context, b nodeenrollment.Binding) error {
		// Check operator bootstrap conflicts before publication, so correcting the
		// handoff can still use its token instead of stranding a saved binding.
		candidate := *bc
		if err := applyCustomerBinding(&candidate, b); err != nil {
			return err
		}
		return validate(ctx, b)
	})
	if err != nil {
		_ = listener.Close()
		return err
	}
	server := &http.Server{Handler: enrollment.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	select {
	case binding := <-enrollment.Done():
		return applyCustomerBinding(bc, binding)
	case <-ctx.Done():
		return ctx.Err()
	case <-stopped:
		return errors.New("node enrollment listener stopped")
	}
}
func validateCustomerBinding(ctx context.Context, b nodeenrollment.Binding) error {
	cipher, err := secret.New([]byte(b.SecretKey))
	if err != nil {
		return errors.New("invalid shared secret")
	}
	pool, err := database.NewPoolForRole(ctx, config.DatabaseConfig{URL: b.DatabaseURL, MaxConnections: 2}, "application")
	if err != nil {
		return errors.New("customer database unavailable")
	}
	defer pool.Close()
	settings := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher)
	values, err := settings.GetAll(ctx)
	if err != nil {
		return errors.New("customer settings could not be decrypted")
	}
	cfg, err := config.LoadFromDB(values)
	if err != nil || cfg.Auth.JWTSecret == "" {
		return errors.New("customer settings are incomplete")
	}
	redisConfig := cfg.Redis.WithBootstrapURL(b.RedisURL)
	client, err := cache.NewDeadlineRedisClientForRole(redisConfig, "application")
	if err != nil {
		return errors.New("invalid customer Redis configuration")
	}
	if client == nil {
		return errors.New("customer Redis is required for transcode mode")
	}
	defer func() { _ = cache.CloseRedisClient(client) }()
	if client.Ping(ctx).Err() != nil {
		return errors.New("customer Redis unavailable")
	}
	return nil
}
