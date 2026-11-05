package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type CatalogSeedArtifactStore interface {
	AdminJobArtifactStore
	GetObject(ctx context.Context, bucket, key string) ([]byte, error)
	ListObjectInfos(ctx context.Context, bucket, prefix string) ([]s3client.ObjectInfo, error)
}

type CatalogSeedHandler struct {
	service        *catalogseed.Service
	jobRepo        *adminjob.Repository
	store          CatalogSeedArtifactStore
	localImportDir string
	RealtimeHub    *notifications.Hub
}

func NewCatalogSeedHandler(service *catalogseed.Service, jobRepo *adminjob.Repository, store CatalogSeedArtifactStore) *CatalogSeedHandler {
	return &CatalogSeedHandler{service: service, jobRepo: jobRepo, store: store}
}

type exportCatalogSeedRequest struct {
	LibraryIDs []int `json:"library_ids"`
}

type importCatalogSeedResponse struct {
	*catalogseed.ImportResult
}

type catalogSeedImportSource struct {
	Key          string     `json:"key"`
	SizeBytes    int64      `json:"size_bytes"`
	LastModified *time.Time `json:"last_modified,omitempty"`
}

type listCatalogSeedImportSourcesResponse struct {
	Sources []catalogSeedImportSource `json:"sources"`
}

type catalogSeedErrorResponse struct {
	Error          string   `json:"error"`
	Message        string   `json:"message"`
	UnmatchedRoots []string `json:"unmatched_roots,omitempty"`
}

const catalogSeedImportPrefix = "catalog-seeds/"
const remoteCatalogSeedTimeout = 10 * time.Minute
const catalogSeedPublishExpiry = 7 * 24 * time.Hour

const defaultLocalImportDir = "/catalog-seeds"

var (
	errCatalogSeedImportSourceUnavailable = errors.New("catalog imports from stored artifacts require configured storage")
	errCatalogSeedImportInvalidLocalPath  = errors.New("Local path must point to an existing .json.gz file")
	errCatalogSeedImportInvalidRemoteURL  = errors.New("Remote URL must point to an http(s) .json.gz file")
)

func (h *CatalogSeedHandler) SetLocalImportDir(dir string) {
	h.localImportDir = dir
}

func (h *CatalogSeedHandler) localImportDirectory() string {
	if h.localImportDir != "" {
		return h.localImportDir
	}
	return defaultLocalImportDir
}

func readLocalImportFile(localPath string) ([]byte, error) {
	cleaned := filepath.Clean(localPath)
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return nil, errCatalogSeedImportInvalidLocalPath
	}
	if !strings.HasSuffix(strings.ToLower(abs), ".json.gz") {
		return nil, errCatalogSeedImportInvalidLocalPath
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errCatalogSeedImportInvalidLocalPath
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("reading local import file: %w", err)
	}
	return data, nil
}

func (h *CatalogSeedHandler) readImportDataFromExportJob(ctx context.Context, jobID string) ([]byte, error) {
	bucket, key, err := h.resolveExportJobArtifactRef(ctx, jobID)
	if err != nil {
		return nil, err
	}
	data, err := h.store.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (h *CatalogSeedHandler) readImportDataFromArtifactKey(ctx context.Context, artifactKey string) ([]byte, error) {
	if h.store == nil {
		return nil, errCatalogSeedImportSourceUnavailable
	}
	return h.store.GetObject(ctx, h.store.Bucket(), artifactKey)
}

func readImportDataFromRemoteURL(ctx context.Context, remoteURL string) ([]byte, error) {
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return nil, errCatalogSeedImportInvalidRemoteURL
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errCatalogSeedImportInvalidRemoteURL
	}
	if !strings.HasSuffix(strings.ToLower(parsed.Path), ".json.gz") {
		return nil, errCatalogSeedImportInvalidRemoteURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building remote catalog seed request: %w", err)
	}

	client := &http.Client{Timeout: remoteCatalogSeedTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading remote catalog seed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading remote catalog seed: unexpected status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading remote catalog seed: %w", err)
	}
	return data, nil
}

func (h *CatalogSeedHandler) resolveExportJobArtifactRef(ctx context.Context, jobID string) (string, string, error) {
	if h.jobRepo == nil || h.store == nil {
		return "", "", errCatalogSeedImportSourceUnavailable
	}

	job, err := h.jobRepo.GetByID(ctx, jobID)
	if err != nil {
		return "", "", err
	}
	if job.JobType != adminjob.JobTypeCatalogExport || job.Status != adminjob.StatusCompleted || job.ArtifactBucket == "" || job.ArtifactKey == "" {
		return "", "", fmt.Errorf("catalog export job %s is not ready for import", jobID)
	}
	return job.ArtifactBucket, job.ArtifactKey, nil
}
