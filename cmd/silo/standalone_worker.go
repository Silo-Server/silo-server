package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Silo-Server/silo-server/internal/workercontrol"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

const workerDatabaseURL = "DATABASE_URL"

// runStandaloneWorker executes before bootstrap, including .env loading,
// decryption-key validation and infrastructure client construction.
func runStandaloneWorker(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	bindingPath := flags.String("binding", "", "protected HTTP worker binding file")
	listen := flags.String("listen", ":8097", "worker HTTP listen address")
	scratch := flags.String("scratch", "", "local temporary output directory")
	ffmpeg := flags.String("ffmpeg", "", "local FFmpeg executable")
	ffprobe := flags.String("ffprobe", "", "local FFprobe executable")
	hwAccel := flags.String("hw-accel", "auto", "local hardware acceleration policy")
	hwDevice := flags.String("hw-device", "", "local GPU render device")
	maxJobs := flags.Int("max-jobs", 0, "operator encoding ceiling; zero is unlimited")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *bindingPath == "" {
		return errors.New("worker requires --binding and no positional arguments")
	}
	for _, name := range []string{workerDatabaseURL, "REDIS_URL", "SECRET_KEY", workerJWTSecret} {
		if os.Getenv(name) != "" {
			return fmt.Errorf("worker refuses infrastructure credential environment variable %s", name)
		}
	}
	binding, err := workeridentity.LoadBinding(*bindingPath)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(*scratch) {
		return errors.New("worker scratch must be an absolute local directory")
	}
	temporary := filepath.Join(*scratch, ".tmp")
	if err := os.MkdirAll(temporary, 0700); err != nil {
		return err
	}
	if err := os.Setenv("TMPDIR", temporary); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	runtime, err := workercontrol.New(ctx, workercontrol.Options{Binding: binding, Scratch: *scratch, FFmpeg: *ffmpeg, FFprobe: *ffprobe, HWAccel: *hwAccel, HWDevice: *hwDevice, MaxJobs: *maxJobs})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: runtime.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	runtime.Run()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Close(shutdownCtx)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Close(shutdownCtx)
		return server.Shutdown(shutdownCtx)
	}
}
