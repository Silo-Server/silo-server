package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/backup"
)

const interruptHelperEnv = "SILO_BACKUP_INTERRUPT_HELPER_ARGS"

// TestBackupInterruptHelper is the child process of
// TestBackupInterruptCleansUp: it runs `silo backup` as main does, with the
// arguments its parent passed, and fake PostgreSQL tools so a restore drill
// reaches its fetch.
func TestBackupInterruptHelper(t *testing.T) {
	raw := os.Getenv(interruptHelperEnv)
	if raw == "" {
		t.Skip("run by TestBackupInterruptCleansUp")
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatal(err)
	}
	findBackupTools = func(context.Context) (backup.Tools, error) {
		return backup.Tools{PgDump: "/bin/false", PgRestore: "/bin/false", Major: 99, Version: "99"}, nil
	}
	code := 0
	if err := runBackupMain(args, os.Stdout, os.Stderr); err != nil {
		code = 3
	}
	os.Exit(code)
}

// stallingBucket serves an encrypted backup whose objects send their first
// bytes and then stall until the client goes away, so a command is
// interrupted mid-download.
func stallingBucket(t *testing.T, key *backup.ArchiveKey) (*httptest.Server, chan string) {
	t.Helper()
	var ciphertext bytes.Buffer
	w, err := key.Encrypt(&ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(bytes.Repeat([]byte("plaintext "), 50_000))
	_ = w.Close()
	stored := ciphertext.Bytes()
	m := backup.Manifest{Format: "silo-backup", Version: 1, ID: "20261031T040000Z-manual", Kind: backup.KindManual, ServerID: "server",
		Encrypted: true, KeyCheck: key.Check, CreatedAt: time.Now(), CompletedAt: time.Now()}
	for _, name := range []string{"db.dump.age", "blobs.tar.age"} {
		m.Objects = append(m.Objects, backup.ObjectEntry{Name: name, Size: int64(len(stored)), SHA256: strings.Repeat("ab", 32)})
	}
	for _, table := range []string{"users", "user_profiles", "media_folders", "media_items", "media_files", "server_settings",
		"plugin_installations", "library_collections", "user_watch_history", "user_watch_progress"} {
		m.Tables = append(m.Tables, backup.TableCount{Name: table, Present: true})
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.ParseManifest(manifest); err != nil {
		t.Fatalf("the test manifest is not valid: %v", err)
	}
	stalled := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+backup.ManifestName):
			_, _ = w.Write(manifest)
		case strings.HasSuffix(r.URL.Path, ".age"):
			w.Header().Set("Content-Length", strconv.Itoa(len(stored)))
			_, _ = w.Write(stored[:len(stored)/2])
			w.(http.Flusher).Flush()
			stalled <- r.URL.Path
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, stalled
}

// TestBackupInterruptCleansUp: an interrupted `silo backup verify` removes
// the working folder of decrypted files it was fetching into, and an
// interrupted `decrypt` leaves neither its output nor its .partial file.
func TestBackupInterruptCleansUp(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a child process")
	}
	masterKey := "interrupt-test-secret-key-0123456789abcdef"
	key, err := backup.NewArchiveKey([]byte(masterKey))
	if err != nil {
		t.Fatal(err)
	}
	srv, stalled := stallingBucket(t, key)
	from := "s3://backups/folder"
	for name, args := range map[string][]string{
		"verify":  {"verify", "--from", from, "--scratch", "postgres://unused/db"},
		"decrypt": {"decrypt", from + "/db.dump.age", "-o", "db.dump"},
	} {
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			raw, _ := json.Marshal(append(args, "-env", work+"/no.env"))
			cmd := exec.Command(os.Args[0], "-test.run=^TestBackupInterruptHelper$")
			cmd.Dir = work
			cmd.Env = append(os.Environ(), interruptHelperEnv+"="+string(raw), "SECRET_KEY="+masterKey,
				envBackupS3Endpoint+"="+srv.URL, envBackupS3AccessKey+"=a", envBackupS3SecretKey+"=s", envBackupS3PathStyle+"=true")
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-stalled:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("the command never reached the download:\n%s", output.String())
			}
			// The download is under way: the decrypted file exists.
			deadline := time.Now().Add(10 * time.Second)
			for len(dirNames(work)) == 0 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond) //nolint:forbidigo // polls the child's working directory, which has no other signal
			}
			if len(dirNames(work)) == 0 {
				t.Fatalf("the command wrote nothing before the interrupt:\n%s", output.String())
			}
			if name == "verify" {
				if info, err := os.Stat(work + "/" + dirNames(work)[0]); err != nil || info.Mode().Perm() != 0o700 {
					t.Fatalf("working folder mode = %v, %v; want 0700", info.Mode().Perm(), err)
				}
			}
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatalf("the interrupted command succeeded:\n%s", output.String())
				}
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("the command did not stop after the interrupt:\n%s", output.String())
			}
			if left := dirNames(work); len(left) != 0 {
				t.Fatalf("the interrupted command left %v:\n%s", left, output.String())
			}
		})
	}
}
