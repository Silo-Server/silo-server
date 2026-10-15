package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func nodeKeyFor(t *testing.T, id int) string {
	t.Helper()
	key, err := nodepool.GenerateNodeKey(id)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestLoadTranscodeNodeEnv(t *testing.T) {
	key := nodeKeyFor(t, 6)
	env := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	for name, values := range map[string]map[string]string{
		"no core URL":    {envNodeKey: key},
		"no key":         {envCoreURL: "http://core:8080"},
		"malformed key":  {envCoreURL: "http://core:8080", envNodeKey: "snk1_6_not-a-key"},
		"not a node key": {envCoreURL: "http://core:8080", envNodeKey: "a-shared-secret"},
	} {
		if _, err := loadTranscodeNodeEnv(env(values)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	got, err := loadTranscodeNodeEnv(env(map[string]string{
		envCoreURL: " http://core:8080 ", envNodeKey: key + "\n", "PORT": "9100",
		"SECRET_KEY": "shared-env-file", "NODE_NAME": "gpu-6", "DATABASE_URL": "postgres://interim", "REDIS_URL": "redis://shared",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.coreURL != "http://core:8080" || got.nodeKey != key || got.listen != ":9100" {
		t.Fatalf("env = %+v", got)
	}
	if !slices.Equal(got.ignored, []string{"SECRET_KEY", "NODE_NAME", "REDIS_URL", "DATABASE_URL"}) {
		t.Fatalf("ignored = %v, want the set variables a node no longer reads", got.ignored)
	}
	if defaults, _ := loadTranscodeNodeEnv(env(map[string]string{envCoreURL: "http://core", envNodeKey: key})); defaults.listen != ":8080" {
		t.Fatalf("default listen = %q", defaults.listen)
	}
}

// A node that cannot even name itself to the core exits instead of serving.
func TestTranscodeNodeExitsWithoutAUsableKey(t *testing.T) {
	if os.Getenv("SILO_TEST_TRANSCODE_NODE_STARTUP") == "1" {
		runTranscodeNode()
		os.Exit(0)
	}
	for name, key := range map[string]string{"missing key": "", "malformed key": "snk1_6_not-a-key"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTranscodeNodeExitsWithoutAUsableKey$")
		cmd.Env = append(os.Environ(), "SILO_TEST_TRANSCODE_NODE_STARTUP=1", "MODE=transcode",
			envCoreURL+"=http://127.0.0.1:1", envNodeKey+"="+key)
		output, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() == 0 {
			t.Fatalf("%s: process ended with %v, want a failed exit\n%s", name, err, output)
		}
		if !strings.Contains(string(output), envNodeKey) {
			t.Fatalf("%s: output does not name %s:\n%s", name, envNodeKey, output)
		}
	}
}

// fakeNodeCore serves a node's configuration and records what it posts.
type fakeNodeCore struct {
	*httptest.Server
	mu     sync.Mutex
	logs   []string
	health chan struct{}
	doc    nodecontrol.NodeConfig
}

func newFakeNodeCore(t *testing.T, nodeID int) *fakeNodeCore {
	t.Helper()
	core := &fakeNodeCore{health: make(chan struct{}, 4), doc: nodecontrol.NodeConfig{Revision: "r1", NodeConfigBody: nodecontrol.NodeConfigBody{
		Node:    nodecontrol.NodeIdentity{ID: nodeID, Name: "gpu-6", URL: "http://gpu-6:8080"},
		Logging: nodecontrol.LoggingConfig{Level: "info", Format: "text", CaptureLevel: "info"},
	}}}
	core.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Silo-Node-HMAC-SHA256 n=6,") {
			http.Error(w, "unsigned", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case nodecontrol.PathConfig:
			_ = json.NewEncoder(w).Encode(core.doc)
			return
		case nodecontrol.PathLogs:
			var batch nodecontrol.LogBatch
			_ = json.NewDecoder(r.Body).Decode(&batch)
			core.mu.Lock()
			for _, entry := range batch.Entries {
				core.logs = append(core.logs, entry.Message)
			}
			core.mu.Unlock()
		case nodecontrol.PathHealthChanged:
			core.health <- struct{}{}
		default:
			_, _ = io.Copy(io.Discard, r.Body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(core.Close)
	return core
}

// The node captures its own records and FFmpeg's stderr as an API server
// does, and ships them to the core instead of Postgres; stderr keeps them too.
func TestTranscodeNodeShipsItsLogsToTheCore(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	core := newFakeNodeCore(t, 6)
	client, err := nodecontrol.NewClient(core.URL, nodeKeyFor(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	remote := nodeconfig.NewRemote(client, ":0")
	if err := remote.Load(ctx); err != nil {
		t.Fatal(err)
	}
	stop := configureTranscodeNodeLogging(ctx, remote.Config(), remote, client, remote.NodeName())
	slog.Info("node says hello", "component", "transcodenode")
	playback.NewSlogFFmpegLogSink(slog.Default(), remote.NodeName()).WriteLine(ctx, "session-1", playback.FFmpegLogAttrs{}, "frame=1")
	slog.Debug("below the capture level")
	stop()

	core.mu.Lock()
	defer core.mu.Unlock()
	if !slices.Contains(core.logs, "node says hello") || !slices.Contains(core.logs, "ffmpeg stderr") {
		t.Fatalf("core received %v, want the node's record and FFmpeg's stderr", core.logs)
	}
	if slices.Contains(core.logs, "below the capture level") {
		t.Fatal("a record below the capture level was shipped")
	}
}

func TestNodeHealthNotifierTellsTheCore(t *testing.T) {
	core := newFakeNodeCore(t, 6)
	client, err := nodecontrol.NewClient(core.URL, nodeKeyFor(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	nodeHealthNotifier(context.Background(), client)()
	select {
	case <-core.health:
	case <-time.After(5 * time.Second):
		t.Fatal("the core heard no health change")
	}
}
