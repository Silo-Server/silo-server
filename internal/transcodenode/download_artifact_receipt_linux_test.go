//go:build linux

package transcodenode

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadDownloadArtifactReceiptStopsAtSizeLimit(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "artifact.mp4")
	receiptPath := downloadArtifactReceiptPath(outputPath)
	if err := syscall.Mkfifo(receiptPath, 0o600); err != nil {
		t.Fatal(err)
	}
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		file, err := os.OpenFile(receiptPath, os.O_WRONLY, 0)
		if err != nil {
			writerDone <- err
			return
		}
		_, err = file.Write(make([]byte, downloadArtifactReceiptMaxBytes+1))
		if err == nil {
			<-releaseWriter
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		writerDone <- err
	}()
	readDone := make(chan error, 1)
	go func() {
		_, err := readDownloadArtifactReceipt(outputPath)
		readDone <- err
	}()

	select {
	case err := <-readDone:
		close(releaseWriter)
		if err == nil {
			t.Fatal("oversized receipt was accepted")
		}
	case <-time.After(500 * time.Millisecond):
		close(releaseWriter)
		<-readDone
		t.Fatal("receipt reader waited for EOF after crossing its size limit")
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
}
