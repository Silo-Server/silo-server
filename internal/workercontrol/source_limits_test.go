package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestRuntimeAppliesConfiguredSourceLimits(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	ffmpeg := filepath.Join(scratch, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(t.Context(), Options{
		Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: "http://core.example.test", NodeURL: "http://worker.example.test", CorePublicKey: public, NodePrivateKey: private, AllowHTTP: true},
		Scratch: scratch, FFmpeg: ffmpeg, HWAccel: "none", MaxSourceReaders: 33, MaxInputs: 257,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	if runtime.inputs == nil {
		t.Fatal("runtime did not create input adapter")
	}
	if err := runtime.authority.SetNonce("source-limits"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.authority.Apply(workerwire.Snapshot{Version: 1, NodeID: 17, BootID: runtime.bootID, Nonce: "source-limits", Sequence: 1, Active: map[string]uint64{"work": 1}, ValidForMillis: 2000}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Check behavior through handle admission: the 129th input used to fail
	// regardless of the caller's requested runtime budget.
	for i := 0; i < 257; i++ {
		if _, err := runtime.inputs.Open(t.Context(), runtimeLimitSource()); err != nil {
			t.Fatalf("configured input capacity rejected source %d: %v", i+1, err)
		}
	}
	if handle, err := runtime.inputs.Open(t.Context(), runtimeLimitSource()); err == nil {
		_ = handle.Close()
		t.Fatal("runtime exceeded its configured source handle budget")
	}
}

func runtimeLimitSource() workerinput.Source {
	return workerinput.Source{ID: "source", Revision: "revision", URL: "http://core.example.test/source", Grant: "grant", ETag: `"revision"`, Size: 4, WorkID: "work", Generation: 1}
}
