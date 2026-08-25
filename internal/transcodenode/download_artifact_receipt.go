package transcodenode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
)

const (
	downloadArtifactReceiptSuffix   = ".receipt.json"
	downloadArtifactReceiptMaxBytes = 4 << 10
	directorySyncUnsupportedGOOS    = "windows"
)

func downloadArtifactReceiptPath(outputPath string) string {
	return outputPath + downloadArtifactReceiptSuffix
}

func writeDownloadArtifactReceipt(outputPath string, receipt downloadprepare.Result) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode download artifact receipt: %w", err)
	}
	data = append(data, '\n')
	if len(data) > downloadArtifactReceiptMaxBytes {
		return fmt.Errorf("download artifact receipt is too large")
	}
	receiptPath := downloadArtifactReceiptPath(outputPath)
	// A unique temp file in the receipt's directory keeps concurrent writers
	// from clobbering each other's partial data; the rename below publishes
	// whichever writer completes last as the atomic current receipt.
	temp, err := os.CreateTemp(filepath.Dir(receiptPath), filepath.Base(receiptPath)+".")
	if err != nil {
		return fmt.Errorf("write download artifact receipt: %w", err)
	}
	tempPath := temp.Name()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("write download artifact receipt: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("sync download artifact receipt: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close download artifact receipt: %w", err)
	}
	if err := os.Rename(tempPath, receiptPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("publish download artifact receipt: %w", err)
	}
	if err := syncDownloadArtifactDirectory(filepath.Dir(receiptPath)); err != nil {
		return fmt.Errorf("sync download artifact receipt directory: %w", err)
	}
	return nil
}

func syncDownloadArtifactDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil && runtime.GOOS != directorySyncUnsupportedGOOS {
		return err
	}
	return nil
}

func readDownloadArtifactReceipt(outputPath string) (downloadprepare.Result, error) {
	file, err := os.Open(downloadArtifactReceiptPath(outputPath))
	if err != nil {
		return downloadprepare.Result{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, downloadArtifactReceiptMaxBytes+1))
	if err != nil {
		return downloadprepare.Result{}, err
	}
	if len(data) > downloadArtifactReceiptMaxBytes {
		return downloadprepare.Result{}, fmt.Errorf("download artifact receipt is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var receipt downloadprepare.Result
	if err := decoder.Decode(&receipt); err != nil {
		return downloadprepare.Result{}, fmt.Errorf("decode download artifact receipt: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return downloadprepare.Result{}, fmt.Errorf("decode download artifact receipt: trailing data")
	}
	return receipt, nil
}

func invalidateDownloadArtifactReceipt(outputPath string) error {
	receiptPath := downloadArtifactReceiptPath(outputPath)
	if err := os.Remove(receiptPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove download artifact receipt: %w", err)
	}
	return nil
}
