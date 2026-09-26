package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

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

func (h *CatalogSeedHandler) HandleExport(w http.ResponseWriter, r *http.Request) {
	var req exportCatalogSeedRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
			return
		}
	}

	data, err := h.ExportCatalog(r.Context(), catalogseed.ExportOptions{LibraryIDs: req.LibraryIDs})
	if err != nil {
		log.Printf("catalog seed export failed: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to export catalog seed")
		return
	}

	filename := "silo-catalog-seed-" + time.Now().UTC().Format("20060102T150405Z") + ".json.gz"
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *CatalogSeedHandler) HandleCreateExportJob(w http.ResponseWriter, r *http.Request) {
	if h.jobRepo == nil || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Catalog export jobs require configured storage")
		return
	}
	// The frozen v1 job projection only presigns, and the streaming artifact
	// route exists only under v2. On a store that cannot presign, a v1 client
	// could queue an export it has no way to retrieve, so v1 keeps refusing
	// exactly as it did before local storage existed.
	if presigner, ok := h.store.(interface{ SupportsPresign() bool }); ok && !presigner.SupportsPresign() {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Catalog export jobs require the private internal S3 bucket")
		return
	}

	var req exportCatalogSeedRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
			return
		}
	}

	job, err := h.CreateCatalogExportJob(r.Context(), currentAdminUserID(r), catalogseed.ExportOptions{LibraryIDs: req.LibraryIDs})
	if err != nil {
		var conflict *adminjob.ActiveJobConflictError
		switch {
		case errors.As(err, &conflict):
			var jobsHandler *AdminJobsHandler
			if h.jobRepo != nil {
				jobsHandler = NewAdminJobsHandler(h.jobRepo, h.store)
			}
			writeAdminJobConflict(w, "A catalog export is already queued or running", conflict.Job, jobsHandler, r)
		default:
			log.Printf("catalog seed export job creation failed: %v", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to queue catalog export")
		}
		return
	}

	jobsHandler := NewAdminJobsHandler(h.jobRepo, h.store)

	writeJSON(w, http.StatusAccepted, adminJobToResponse(r, job, jobsHandler.store))
}

func (h *CatalogSeedHandler) HandlePublishExportJob(w http.ResponseWriter, r *http.Request) {
	job, err := h.PublishCatalogExportJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, adminjob.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Job not found")
			return
		}
		writeCatalogTransferFailure(w, err, "Failed to create catalog export URL")
		return
	}
	writeJSON(w, http.StatusOK, adminJobToResponse(r, job, h.store))
}

func (h *CatalogSeedHandler) HandleListImportSources(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Catalog imports from stored artifacts require configured storage")
		return
	}

	objects, err := h.store.ListObjectInfos(r.Context(), h.store.Bucket(), catalogSeedImportPrefix)
	if err != nil {
		log.Printf("catalog seed import source listing failed: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list catalog seed import sources")
		return
	}

	sources := make([]catalogSeedImportSource, 0, len(objects))
	for _, obj := range objects {
		if !strings.HasSuffix(strings.ToLower(obj.Key), ".json.gz") {
			continue
		}
		sources = append(sources, catalogSeedImportSource{
			Key:          obj.Key,
			SizeBytes:    obj.SizeBytes,
			LastModified: obj.LastModified,
		})
	}

	sort.Slice(sources, func(i, j int) bool {
		left := sources[i].LastModified
		right := sources[j].LastModified
		switch {
		case left == nil && right == nil:
			return sources[i].Key > sources[j].Key
		case left == nil:
			return false
		case right == nil:
			return true
		case left.Equal(*right):
			return sources[i].Key > sources[j].Key
		default:
			return left.After(*right)
		}
	})

	writeJSON(w, http.StatusOK, listCatalogSeedImportSourcesResponse{Sources: sources})
}

func (h *CatalogSeedHandler) HandleCreateImportJob(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid multipart form")
		return
	}
	opts, err := parseCatalogImportOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid import options")
		return
	}
	source := catalogImportSelectionFromForm(r)
	job, err := h.CreateCatalogImportJob(r.Context(), currentAdminUserID(r), source, opts)
	if err != nil {
		if conflict, ok := errors.AsType[*adminjob.ActiveJobConflictError](err); ok {
			writeAdminJobConflict(w, "A catalog import is already queued or running", conflict.Job, NewAdminJobsHandler(h.jobRepo, h.store), r)
			return
		}
		writeCatalogTransferFailure(w, err, "Failed to queue catalog import")
		return
	}
	writeJSON(w, http.StatusAccepted, adminJobToResponse(r, job, h.store))
}

func catalogImportSelectionFromForm(r *http.Request) CatalogImportSourceSelection {
	return CatalogImportSourceSelection{LocalPath: r.FormValue("local_path"), ExportJobID: r.FormValue("export_job_id"), ArtifactKey: r.FormValue("artifact_key"), RemoteURL: r.FormValue("remote_url")}
}

func (h *CatalogSeedHandler) HandleListLocalImportSources(w http.ResponseWriter, r *http.Request) {
	dir := h.localImportDirectory()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, listCatalogSeedImportSourcesResponse{Sources: []catalogSeedImportSource{}})
			return
		}
		log.Printf("listing local import sources in %s: %v", dir, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list local import sources")
		return
	}

	sources := make([]catalogSeedImportSource, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json.gz") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		modTime := info.ModTime().UTC()
		sources = append(sources, catalogSeedImportSource{
			Key:          filepath.Join(dir, entry.Name()),
			SizeBytes:    info.Size(),
			LastModified: &modTime,
		})
	}

	sort.Slice(sources, func(i, j int) bool {
		if sources[i].LastModified == nil || sources[j].LastModified == nil {
			return sources[i].Key > sources[j].Key
		}
		return sources[i].LastModified.After(*sources[j].LastModified)
	})

	writeJSON(w, http.StatusOK, listCatalogSeedImportSourcesResponse{Sources: sources})
}

func (h *CatalogSeedHandler) HandleImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid multipart form")
		return
	}

	opts, err := parseCatalogImportOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid import options")
		return
	}

	result, err := h.ImportCatalog(r.Context(), catalogImportSelectionFromForm(r), opts)
	if err != nil {
		if apiErr, ok := errors.AsType[*APIError](err); ok {
			writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
			return
		}
		var unmatched *catalogseed.UnmatchedRootsError
		switch {
		case errors.As(err, &unmatched):
			writeCatalogSeedError(w, http.StatusBadRequest, "path_rewrite_required", "Catalog seed import requires additional path rewrites", unmatched.Roots)
		case errors.Is(err, catalogseed.ErrInvalidBundle):
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid catalog seed bundle")
		case errors.Is(err, catalogseed.ErrUnsupportedBundleVersion):
			writeError(w, http.StatusBadRequest, "bad_request", "Unsupported catalog seed version")
		case errors.Is(err, catalogseed.ErrInvalidConflictMode):
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid conflict mode")
		default:
			log.Printf("catalog seed import failed: %v", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to import catalog seed")
		}
		return
	}

	writeJSON(w, http.StatusOK, importCatalogSeedResponse{ImportResult: result})
}

func writeCatalogSeedError(w http.ResponseWriter, status int, code, message string, unmatchedRoots []string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(catalogSeedErrorResponse{
		Error:          code,
		Message:        message,
		UnmatchedRoots: unmatchedRoots,
	})
}

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

func parseCatalogImportOptions(r *http.Request) (catalogseed.ImportOptions, error) {
	var rewrites []catalogseed.PathRewrite
	if raw := r.FormValue("path_rewrites"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &rewrites); err != nil {
			return catalogseed.ImportOptions{}, errors.New("Invalid path_rewrites")
		}
	}

	return catalogseed.ImportOptions{
		ConflictMode: catalogseed.ConflictMode(r.FormValue("conflict_mode")),
		PathRewrites: rewrites,
	}, nil
}

func writeCatalogTransferFailure(w http.ResponseWriter, err error, message string) {
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		status := apiErr.Status
		code := apiErr.Code
		if status == http.StatusConflict {
			status = http.StatusBadRequest
			code = "bad_request"
		}
		writeError(w, status, code, apiErr.Message)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", message)
}
