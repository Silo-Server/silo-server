package plugins

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBinaryPlatformReadsThisHostsBinaries(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	goos, goarch, ok := binaryPlatform(self)
	if !ok {
		t.Skipf("test binary format not recognized on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if goos != runtime.GOOS || goarch != runtime.GOARCH {
		t.Fatalf("binaryPlatform(self) = %s/%s, want %s/%s", goos, goarch, runtime.GOOS, runtime.GOARCH)
	}
	if err := checkBinaryPlatform(self); err != nil {
		t.Fatalf("checkBinaryPlatform(self) = %v", err)
	}
}

func TestCheckBinaryPlatformIgnoresUnknownFormats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkBinaryPlatform(path); err != nil {
		t.Fatalf("script treated as a platform mismatch: %v", err)
	}
}

func TestCheckBinaryPlatformRejectsForeignELF(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		t.Skip("fixture is linux/arm64")
	}
	// Minimal ELF64 header: little-endian, EM_AARCH64 (0xB7).
	hdr := make([]byte, 64)
	copy(hdr, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	hdr[16] = 2    // ET_EXEC
	hdr[18] = 0xB7 // EM_AARCH64
	hdr[20] = 1    // EV_CURRENT
	hdr[52] = 64   // e_ehsize
	path := filepath.Join(t.TempDir(), "plugin")
	if err := os.WriteFile(path, hdr, 0o755); err != nil {
		t.Fatal(err)
	}
	goos, goarch, ok := binaryPlatform(path)
	if !ok || goos != "linux" || goarch != "arm64" {
		t.Fatalf("binaryPlatform = %s/%s ok=%v, want linux/arm64", goos, goarch, ok)
	}
	if err := checkBinaryPlatform(path); err == nil {
		t.Fatal("foreign ELF accepted")
	}
}
