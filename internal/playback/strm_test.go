package playback

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTranscodeInputPathSTRM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.strm")
	const want = "https://media.example.test/play/movie.mkv?token=secret"
	if err := os.WriteFile(path, []byte(want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveTranscodeInputPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved input = %q, want %q", got, want)
	}
}

func TestResolveTranscodeInputPathRejectsInvalidSTRM(t *testing.T) {
	for name, content := range map[string]string{
		"empty":    " \n",
		"multiple": "https://one.example/file\nhttps://two.example/file",
		"local":    "file:///etc/passwd",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "movie.strm")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := resolveTranscodeInputPath(path); err == nil {
				t.Fatal("expected invalid .strm to be rejected")
			}
		})
	}
}

func TestResolveTranscodeInputPathLeavesMediaPathAlone(t *testing.T) {
	const path = "/media/movie.mkv"
	got, err := resolveTranscodeInputPath(path)
	if err != nil || got != path {
		t.Fatalf("resolved input = %q, err = %v", got, err)
	}
}
