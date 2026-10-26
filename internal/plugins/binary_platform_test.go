package plugins

import (
	"os"
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
