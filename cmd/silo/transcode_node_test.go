package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

const testTranscodeNodeID = 6

func nodeKeyFor(t *testing.T, id int) string {
	t.Helper()
	key, err := nodepool.GenerateNodeKey(id)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// A node's key can come from a mounted secret file, as a secrets manager
// delivers it, instead of from the environment itself.
func TestLoadTranscodeNodeEnvReadsKeyFile(t *testing.T) {
	key := nodeKeyFor(t, testTranscodeNodeID)
	path := filepath.Join(t.TempDir(), "node-key")
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{transcodenode.EnvCoreURL: "http://core:8080", transcodenode.EnvNodeKey + "_FILE": path}
	got, err := loadTranscodeNodeEnv(func(name string) string { return env[name] })
	if err != nil || got.nodeKey != key {
		t.Fatalf("node key = %q, %v; want the file's key", got.nodeKey, err)
	}
}

func TestLoadTranscodeNodeEnv(t *testing.T) {
	key := nodeKeyFor(t, testTranscodeNodeID)
	env := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	for name, values := range map[string]map[string]string{
		"no core URL":       {transcodenode.EnvNodeKey: key},
		"relative core URL": {transcodenode.EnvCoreURL: "core:8080", transcodenode.EnvNodeKey: key},
		"no key":            {transcodenode.EnvCoreURL: "http://core:8080"},
		"malformed key":     {transcodenode.EnvCoreURL: "http://core:8080", transcodenode.EnvNodeKey: "snk1_6_not-a-key"},
		"not a node key":    {transcodenode.EnvCoreURL: "http://core:8080", transcodenode.EnvNodeKey: "a-shared-secret"},
	} {
		if _, err := loadTranscodeNodeEnv(env(values)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	got, err := loadTranscodeNodeEnv(env(map[string]string{
		transcodenode.EnvCoreURL: " http://core:8080 ", transcodenode.EnvNodeKey: key + "\n", "PORT": "9100",
		envSecretKey: "shared-env-file", envNodeName: "gpu-6", envDatabaseURL: "postgres://interim", envRedisURL: "redis://shared",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.coreURL != "http://core:8080" || got.nodeKey != key || got.listen != ":9100" || !got.insecureCoreURL {
		t.Fatalf("env = %+v", got)
	}
	if !slices.Equal(got.ignored, []string{envDatabaseURL, envRedisURL, envSecretKey, envNodeName}) {
		t.Fatalf("ignored = %v, want the set variables a node no longer reads", got.ignored)
	}
	for coreURL, insecure := range map[string]bool{
		"https://core.example": false, "http://127.0.0.1:8080": false, "http://localhost:8080": false, "http://[::1]:8080": false,
		"http://192.168.1.10:8080": true,
	} {
		loaded, err := loadTranscodeNodeEnv(env(map[string]string{transcodenode.EnvCoreURL: coreURL, transcodenode.EnvNodeKey: key}))
		if err != nil || loaded.insecureCoreURL != insecure || loaded.listen != ":8080" {
			t.Fatalf("%s: insecure = %v, listen %q (%v); want insecure %v on :8080", coreURL, loaded.insecureCoreURL, loaded.listen, err, insecure)
		}
	}
}

// fakeNodeCore serves a transcode node's configuration through the core's
// real signature check, and records the log entries the node ships.
type fakeNodeCore struct {
	*httptest.Server
	key  string
	mu   sync.Mutex
	logs []string
	doc  nodecontrol.NodeConfig
}

func (c *fakeNodeCore) NodeKey(id int) (string, error) {
	if id != testTranscodeNodeID {
		return "", nodepool.ErrNodeNotFound
	}
	return c.key, nil
}

func (c *fakeNodeCore) ReloadNodeKey(id int) (string, error) { return c.NodeKey(id) }

func newFakeNodeCore(t *testing.T, key string) *fakeNodeCore {
	t.Helper()
	core := &fakeNodeCore{key: key, doc: nodecontrol.NodeConfig{Revision: "r1", NodeConfigBody: nodecontrol.NodeConfigBody{
		Node:     nodecontrol.NodeIdentity{ID: testTranscodeNodeID, Name: "gpu-6", URL: "http://gpu-6:8080"},
		Playback: nodecontrol.PlaybackConfig{FFmpegPath: "/nonexistent/ffmpeg", HWAccel: playback.HWAccelNone, TranscodeDir: t.TempDir()},
		Logging:  nodecontrol.LoggingConfig{Level: "info", Format: "text", CaptureLevel: "info"},
	}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+nodecontrol.PathConfig, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(core.doc)
	})
	mux.HandleFunc("PUT "+nodecontrol.PathRegistration, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(nodecontrol.RegistrationResult{Node: core.doc.Node, EnvManaged: []string{}})
	})
	mux.HandleFunc("POST "+nodecontrol.PathLogs, func(w http.ResponseWriter, r *http.Request) {
		var batch nodecontrol.LogBatch
		_ = json.NewDecoder(r.Body).Decode(&batch)
		core.mu.Lock()
		for _, entry := range batch.Entries {
			core.logs = append(core.logs, entry.Message)
		}
		core.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	})
	core.Server = httptest.NewServer(nodecontrol.RequireNode(core)(mux))
	t.Cleanup(core.Close)
	return core
}

// The node starts with SILO_CORE_URL and SILO_NODE_KEY alone, with no
// database, Redis or secret in its environment: it reads its configuration
// from the core, answers health naming the node control protocol, and stops
// cleanly on SIGTERM. A malformed key refuses startup.
func TestTranscodeNodeStartsFromTheCoreAlone(t *testing.T) {
	if os.Getenv("SILO_TEST_TRANSCODE_NODE") == "1" {
		runTranscodeNode()
		os.Exit(0)
	}
	if testing.Short() {
		t.Skip("starts a node process")
	}
	nodeEnv := func(extra ...string) []string {
		var env []string
		for _, kv := range os.Environ() {
			name, _, _ := strings.Cut(kv, "=")
			if !slices.Contains(append(slices.Clone(transcodeNodeIgnoredEnv), transcodenode.EnvCoreURL, transcodenode.EnvNodeKey, transcodenode.EnvNodeKey+"_FILE", "PORT"), name) {
				env = append(env, kv)
			}
		}
		return append(append(env, "SILO_TEST_TRANSCODE_NODE=1", "MODE=transcode", transcodenode.EnvDrainTimeout+"=0"), extra...)
	}

	for name, key := range map[string]string{"missing key": "", "malformed key": "snk1_6_not-a-key"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTranscodeNodeStartsFromTheCoreAlone$")
		cmd.Env = nodeEnv(transcodenode.EnvCoreURL+"=http://127.0.0.1:1", transcodenode.EnvNodeKey+"="+key)
		output, err := cmd.CombinedOutput()
		if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() == 0 {
			t.Fatalf("%s: process ended with %v, want a failed exit\n%s", name, err, output)
		}
		if !strings.Contains(string(output), transcodenode.EnvNodeKey) {
			t.Fatalf("%s: output does not name %s:\n%s", name, transcodenode.EnvNodeKey, output)
		}
	}

	key := nodeKeyFor(t, testTranscodeNodeID)
	core := newFakeNodeCore(t, key)
	port := freePort(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestTranscodeNodeStartsFromTheCoreAlone$")
	cmd.Env = nodeEnv(transcodenode.EnvCoreURL+"="+core.URL, transcodenode.EnvNodeKey+"="+key, "PORT="+port)
	var output syncBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	health := waitForNodeHealth(t, "http://127.0.0.1:"+port+"/api/v1/health", exited, &output)
	if health.NodeControlVersion != nodepool.NodeControlVersion || health.Status != transcodenode.HealthStatusOK {
		t.Fatalf("health = %+v", health)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("node exited with %v after SIGTERM\n%s", err, output.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("node did not stop on SIGTERM\n%s", output.String())
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

// waitForNodeHealth polls the node's health until it answers 200, the
// process exits, or a deadline passes.
func waitForNodeHealth(t *testing.T, url string, exited <-chan error, output *syncBuffer) transcodenode.HealthResponse {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-exited:
			t.Fatalf("node exited before it served health: %v\n%s", err, output.String())
		default:
		}
		if resp, err := http.Get(url); err == nil {
			var health transcodenode.HealthResponse
			decodeErr := json.NewDecoder(resp.Body).Decode(&health)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && decodeErr == nil {
				return health
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("node did not serve health\n%s", output.String())
		}
		time.Sleep(50 * time.Millisecond) //nolint:forbidigo // polls another process's health, which is the observable state
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The node captures its own records and FFmpeg's stderr as an API server
// does, and ships them to the core instead of Postgres; stderr keeps them too.
func TestTranscodeNodeShipsItsLogsToTheCore(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	key := nodeKeyFor(t, testTranscodeNodeID)
	core := newFakeNodeCore(t, key)
	client, err := nodecontrol.NewClient(core.URL, key)
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
