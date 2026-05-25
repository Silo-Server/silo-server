# Jellyfin Autoscan Scan Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Autoscan's stock Jellyfin target work against Silo by supporting Jellyfin library discovery and scan notification routes backed by Silo's existing scan queue.

**Architecture:** Extract native scan path resolution into a shared `internal/scantrigger` package, then use it from both the native `/api/v1/scan` handler and a new Jellyfin Autoscan compatibility handler. Add narrow admin API-key authentication for Autoscan routes without broadening normal Jellyfin playback/browse auth.

**Tech Stack:** Go, chi, existing Silo catalog/scanner/scanqueue/auth packages, focused Go unit tests.

---

## File Structure

- Create: `internal/scantrigger/scantrigger.go`
  - Owns shared library/path scan target resolution and queue enqueue helpers.
- Create: `internal/scantrigger/scantrigger_test.go`
  - Pins root, subtree, file, disabled library, missing path, and all-or-fail validation behavior.
- Modify: `internal/api/handlers/libraries.go`
  - Replaces local resolver/path helpers with `scantrigger`.
- Create: `internal/jellycompat/auth_api_key.go`
  - Validates Silo admin API keys from Jellyfin token locations and provides session-or-admin route middleware.
- Create: `internal/jellycompat/handlers_autoscan.go`
  - Handles Autoscan-facing `GET /Library/VirtualFolders` and `POST /Library/Media/Updated`.
- Create: `internal/jellycompat/handlers_autoscan_test.go`
  - Tests admin API-key auth, library locations, scan enqueue, and all-or-fail behavior.
- Modify: `internal/jellycompat/router.go`
  - Registers Autoscan compatibility routes and keeps existing session behavior for normal clients.
- Modify: `internal/jellycompat/server.go`
  - Adds dependencies for API-key validation and scan enqueueing.
- Modify: `cmd/silo/main.go`
  - Wires API-key repository, user repository, and scan queue into Jellyfin compatibility dependencies.
- Modify: `docs/scan-api.md`
  - Documents using Autoscan's stock Jellyfin target with a Silo admin API key.

---

### Task 1: Extract Shared Scan Target Resolution

**Files:**
- Create: `internal/scantrigger/scantrigger.go`
- Create: `internal/scantrigger/scantrigger_test.go`

- [ ] **Step 1: Write failing resolver tests**

Create `internal/scantrigger/scantrigger_test.go`:

```go
package scantrigger

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeFolderRepo struct {
	folders []*models.MediaFolder
}

func (r *fakeFolderRepo) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	for _, folder := range r.folders {
		if folder.ID == id {
			return folder, nil
		}
	}
	return nil, catalog.ErrFolderNotFound
}

func (r *fakeFolderRepo) List(context.Context) ([]*models.MediaFolder, error) {
	return r.folders, nil
}

func TestResolverClassifiesLibraryRoot(t *testing.T) {
	root := t.TempDir()
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      7,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	target, err := NewResolver(repo).Resolve(context.Background(), Request{Path: root})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if target.LibraryID != 7 || target.Mode != ModeLibrary || target.Path != "" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverClassifiesSubtree(t *testing.T) {
	root := t.TempDir()
	subtree := filepath.Join(root, "Show")
	if err := os.Mkdir(subtree, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      8,
		Name:    "TV",
		Enabled: true,
		Paths:   []string{root},
	}}}

	target, err := NewResolver(repo).Resolve(context.Background(), Request{Path: subtree})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if target.LibraryID != 8 || target.Mode != ModeSubtree || target.Path != filepath.Clean(subtree) {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverClassifiesVideoFile(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "Movie (2024).mkv")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      9,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	target, err := NewResolver(repo).Resolve(context.Background(), Request{Path: filePath})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if target.LibraryID != 9 || target.Mode != ModeFile || target.Path != filepath.Clean(filePath) {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverRejectsDisabledLibrary(t *testing.T) {
	root := t.TempDir()
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      10,
		Name:    "Disabled",
		Enabled: false,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).Resolve(context.Background(), Request{Path: root})
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Status != http.StatusConflict || reqErr.Code != "conflict" {
		t.Fatalf("unexpected error: %#v", reqErr)
	}
}

func TestResolveAllIsAllOrFail(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(valid, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      11,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).ResolveAll(context.Background(), []Request{
		{Path: valid},
		{Path: filepath.Join(root, "missing.mkv")},
	})
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Message != "Path does not exist" {
		t.Fatalf("unexpected error message: %q", reqErr.Message)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/scantrigger
```

Expected: fail because `internal/scantrigger` does not exist yet.

- [ ] **Step 3: Add shared resolver implementation**

Create `internal/scantrigger/scantrigger.go`:

```go
package scantrigger

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

const (
	ModeLibrary = "library"
	ModeSubtree = "subtree"
	ModeFile    = "file"
)

type FolderRepository interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
	List(ctx context.Context) ([]*models.MediaFolder, error)
}

type Queuer interface {
	EnqueueScan(ctx context.Context, folderID int, mode, path, trigger string) (bool, error)
}

type Request struct {
	LibraryID *int
	Path      string
	Trigger   string
}

type Target struct {
	Folder    *models.MediaFolder
	LibraryID int
	Mode      string
	Path      string
	Trigger   string
}

type RequestError struct {
	Status  int
	Code    string
	Message string
}

func (e *RequestError) Error() string {
	return e.Message
}

type Resolver struct {
	folders FolderRepository
}

func NewResolver(folders FolderRepository) *Resolver {
	return &Resolver{folders: folders}
}

func (r *Resolver) ResolveAll(ctx context.Context, requests []Request) ([]Target, error) {
	targets := make([]Target, 0, len(requests))
	for _, req := range requests {
		target, err := r.Resolve(ctx, req)
		if err != nil {
			return nil, err
		}
		targets = append(targets, *target)
	}
	return targets, nil
}

func (r *Resolver) Resolve(ctx context.Context, req Request) (*Target, error) {
	if r == nil || r.folders == nil {
		return nil, &RequestError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Scanner not available"}
	}
	if req.LibraryID == nil && strings.TrimSpace(req.Path) == "" {
		return nil, &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Either library_id or path is required"}
	}

	var folder *models.MediaFolder
	var err error
	if req.LibraryID != nil {
		folder, err = r.folders.GetByID(ctx, *req.LibraryID)
		if err != nil {
			if errors.Is(err, catalog.ErrFolderNotFound) {
				return nil, &RequestError{Status: http.StatusNotFound, Code: "not_found", Message: "Library not found"}
			}
			return nil, fmt.Errorf("fetching library for scan: %w", err)
		}
	}

	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	if strings.TrimSpace(req.Path) == "" {
		if folder != nil && !folder.Enabled {
			return nil, &RequestError{Status: http.StatusConflict, Code: "conflict", Message: "Library is disabled"}
		}
		return &Target{Folder: folder, LibraryID: folder.ID, Mode: ModeLibrary, Trigger: trigger}, nil
	}

	cleanPath := filepath.Clean(req.Path)
	var matchedRoot string
	if folder != nil {
		matchedRoot, err = LongestMatchingRoot(cleanPath, folder.Paths)
		if err != nil {
			return nil, err
		}
		if matchedRoot == "" {
			return nil, &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Path does not belong to the specified library"}
		}
	} else {
		folders, listErr := r.folders.List(ctx)
		if listErr != nil {
			return nil, fmt.Errorf("listing libraries for scan: %w", listErr)
		}
		folder, matchedRoot, err = MatchFolderForPath(cleanPath, folders)
		if err != nil {
			return nil, err
		}
	}
	if folder != nil && !folder.Enabled {
		return nil, &RequestError{Status: http.StatusConflict, Code: "conflict", Message: "Library is disabled"}
	}

	mode, err := ClassifyPath(cleanPath, matchedRoot)
	if err != nil {
		return nil, err
	}
	if trigger == "manual" {
		trigger = "path"
		if req.LibraryID != nil {
			trigger = "library_id_path"
		}
	}

	targetPath := cleanPath
	if mode == ModeLibrary {
		targetPath = ""
	}
	return &Target{Folder: folder, LibraryID: folder.ID, Mode: mode, Path: targetPath, Trigger: trigger}, nil
}

func EnqueueAll(ctx context.Context, queue Queuer, targets []Target) error {
	if queue == nil {
		return &RequestError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Scanner not available"}
	}
	for _, target := range targets {
		if _, err := queue.EnqueueScan(ctx, target.LibraryID, target.Mode, target.Path, target.Trigger); err != nil {
			return fmt.Errorf("queueing library scan: %w", err)
		}
	}
	return nil
}

func LongestMatchingRoot(targetPath string, roots []string) (string, error) {
	bestRoot := ""
	bestLen := -1
	for _, root := range roots {
		if !PathWithinRoot(targetPath, root) {
			continue
		}
		cleanRoot := filepath.Clean(root)
		rootLen := len(cleanRoot)
		if rootLen > bestLen {
			bestRoot = cleanRoot
			bestLen = rootLen
		}
	}
	return bestRoot, nil
}

func MatchFolderForPath(targetPath string, folders []*models.MediaFolder) (*models.MediaFolder, string, error) {
	var bestFolder *models.MediaFolder
	bestRoot := ""
	bestLen := -1
	ambiguous := false

	for _, folder := range folders {
		if folder == nil {
			continue
		}
		root, err := LongestMatchingRoot(targetPath, folder.Paths)
		if err != nil {
			return nil, "", err
		}
		if root == "" {
			continue
		}
		rootLen := len(root)
		if rootLen > bestLen {
			bestFolder = folder
			bestRoot = root
			bestLen = rootLen
			ambiguous = false
			continue
		}
		if rootLen == bestLen && bestFolder != nil && folder.ID != bestFolder.ID {
			ambiguous = true
		}
	}

	if ambiguous {
		return nil, "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Path matches multiple libraries"}
	}
	if bestFolder == nil {
		return nil, "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "No library matches the given path"}
	}
	return bestFolder, bestRoot, nil
}

func ClassifyPath(targetPath, matchedRoot string) (string, error) {
	if filepath.Clean(targetPath) == filepath.Clean(matchedRoot) {
		return ModeLibrary, nil
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Path does not exist"}
		case errors.Is(err, os.ErrPermission):
			return "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Permission denied for path"}
		default:
			return "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Path could not be inspected"}
		}
	}
	if info.IsDir() {
		return ModeSubtree, nil
	}
	if !info.Mode().IsRegular() {
		return "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Path must be a file or directory"}
	}
	if !scanner.SupportsVideoFile(targetPath) {
		return "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Unsupported media file extension"}
	}
	return ModeFile, nil
}

func PathWithinRoot(targetPath, rootPath string) bool {
	cleanTarget := filepath.Clean(targetPath)
	cleanRoot := filepath.Clean(rootPath)
	rel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil {
		return false
	}
	if rel == "." || rel == "" {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

- [ ] **Step 4: Run resolver tests**

Run:

```bash
go test ./internal/scantrigger
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scantrigger/scantrigger.go internal/scantrigger/scantrigger_test.go
git commit -m "refactor(scan): extract scan trigger resolver"
```

---

### Task 2: Use Shared Resolver in Native Scan API

**Files:**
- Modify: `internal/api/handlers/libraries.go`

- [ ] **Step 1: Update imports**

In `internal/api/handlers/libraries.go`, add:

```go
	"github.com/Silo-Server/silo-server/internal/scantrigger"
```

Remove now-unused imports after the refactor:

```go
	"os"
	"path/filepath"
```

Keep `strings` if other functions still use it in the file.

- [ ] **Step 2: Replace local scan mode constants and resolver types**

Remove the local `scanMode`, `resolvedScanTarget`, and `scanRequestError` declarations. Use `scantrigger.Target` and `scantrigger.RequestError` instead.

Update the start of `HandleScan` to:

```go
	target, err := scantrigger.NewResolver(h.folderRepo).Resolve(r.Context(), scantrigger.Request{
		LibraryID: req.LibraryID,
		Path:      req.Path,
	})
	if err != nil {
		var reqErr *scantrigger.RequestError
		if errors.As(err, &reqErr) {
			writeError(w, reqErr.Status, reqErr.Code, reqErr.Message)
			return
		}
		slog.Error("resolving scan target", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve scan target")
		return
	}
```

- [ ] **Step 3: Update enqueue and direct-scan dispatch**

In `HandleScan`, replace `target.folder`, `target.mode`, and `target.path` with exported fields:

```go
	if h.ScanQueue != nil {
		if _, err := h.ScanQueue.EnqueueScan(r.Context(), target.LibraryID, target.Mode, target.Path, target.Trigger); err != nil {
			slog.Error("queueing library scan", "library_id", target.LibraryID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to queue scan")
			return
		}
	} else if h.ingester != nil {
		scanID := ulid.Make().String()
		h.recordAcceptedScan(scanID, target)
		switch target.Mode {
		case scantrigger.ModeFile:
			h.runFileScanAsync(scanID, target.Folder, target.Path, target.Trigger)
		case scantrigger.ModeSubtree:
			h.runSubtreeScanAsync(scanID, target.Folder, target.Path, target.Trigger)
		default:
			h.runFolderScanAsync(scanID, target.Folder, target.Trigger)
		}
	} else {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Scanner not available")
		return
	}

	writeJSON(w, http.StatusAccepted, scanResponse{
		Status:    "accepted",
		Mode:      target.Mode,
		LibraryID: target.LibraryID,
	})
```

- [ ] **Step 4: Update `recordAcceptedScan` signature**

Change:

```go
func (h *LibraryHandler) recordAcceptedScan(scanID string, target *resolvedScanTarget) {
```

to:

```go
func (h *LibraryHandler) recordAcceptedScan(scanID string, target *scantrigger.Target) {
```

Inside it, use:

```go
if h == nil || h.ScanRegistry == nil || target == nil || target.Folder == nil {
	return
}
h.ScanRegistry.Upsert(evt.ScanRun{
	ID:        scanID,
	LibraryID: target.LibraryID,
	Mode:      target.Mode,
	Path:      target.Path,
	Trigger:   target.Trigger,
	Status:    "accepted",
})
```

Keep existing fields that are already in the local `evt.ScanRun` literal; only update the target field names.

- [ ] **Step 5: Delete old resolver helpers**

Remove these functions from `internal/api/handlers/libraries.go` after the native handler compiles against `scantrigger`:

```go
resolveScanTarget
longestMatchingRoot
matchFolderForPath
classifyScanPath
pathWithinRoot
```

- [ ] **Step 6: Run targeted tests**

Run:

```bash
go test ./internal/api/handlers ./internal/scantrigger
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/api/handlers/libraries.go internal/scantrigger/scantrigger.go internal/scantrigger/scantrigger_test.go
git commit -m "refactor(api): share scan target resolution"
```

---

### Task 3: Add Jellyfin Admin API-Key Auth for Autoscan

**Files:**
- Modify: `internal/jellycompat/server.go`
- Create: `internal/jellycompat/auth_api_key.go`
- Modify: `cmd/silo/main.go`

- [ ] **Step 1: Add dependency fields**

In `internal/jellycompat/server.go`, extend `Dependencies`:

```go
	// Autoscan / admin compatibility support.
	APIKeyValidator apiKeyValidator
	APIKeyUserLoader apiKeyUserLoader
	ScanQueue        scantrigger.Queuer
```

Add the import:

```go
	"github.com/Silo-Server/silo-server/internal/scantrigger"
```

- [ ] **Step 2: Add failing auth tests**

Add tests to `internal/jellycompat/auth_test.go`:

```go
func TestRequireAdminAPIKey_AcceptsAdminKey(t *testing.T) {
	authn := NewAdminAPIKeyAuthenticator(
		&fakeAPIKeyValidator{key: &models.APIKey{ID: 1, UserID: 2, Key: "sa_test"}},
		&fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: "admin", Enabled: true}},
	)
	req := httptest.NewRequest("GET", "/Library/VirtualFolders", nil)
	req.Header.Set("X-Emby-Token", "sa_test")
	rec := httptest.NewRecorder()

	authn.RequireAdminAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !AdminAPIKeyFromContext(r.Context()) {
			t.Fatal("expected admin API key marker in context")
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequireAdminAPIKey_RejectsNonAdminKey(t *testing.T) {
	authn := NewAdminAPIKeyAuthenticator(
		&fakeAPIKeyValidator{key: &models.APIKey{ID: 1, UserID: 2, Key: "sa_test"}},
		&fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: "user", Enabled: true}},
	)
	req := httptest.NewRequest("POST", "/Library/Media/Updated", nil)
	req.Header.Set("X-Emby-Token", "sa_test")
	rec := httptest.NewRecorder()

	authn.RequireAdminAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}
```

Add supporting fakes in the same file:

```go
type fakeAPIKeyValidator struct {
	key *models.APIKey
}

func (f *fakeAPIKeyValidator) GetByKey(_ context.Context, key string) (*models.APIKey, error) {
	if f.key != nil && f.key.Key == key {
		return f.key, nil
	}
	return nil, auth.ErrAPIKeyNotFound
}

func (f *fakeAPIKeyValidator) UpdateLastUsed(context.Context, int64) error {
	return nil
}

type fakeAPIKeyUserLoader struct {
	user *models.User
}

func (f *fakeAPIKeyUserLoader) GetByID(_ context.Context, id int) (*models.User, error) {
	if f.user != nil && f.user.ID == id {
		return f.user, nil
	}
	return nil, auth.ErrNotFound
}
```

Update imports in `auth_test.go`:

```go
	"context"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
```

- [ ] **Step 3: Run auth tests to verify failure**

Run:

```bash
go test ./internal/jellycompat -run 'TestRequireAdminAPIKey'
```

Expected: fail because `NewAdminAPIKeyAuthenticator` and `AdminAPIKeyFromContext` do not exist.

- [ ] **Step 4: Add API-key auth helper**

Create `internal/jellycompat/auth_api_key.go`:

```go
package jellycompat

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

type adminAPIKeyContextKey string

const adminAPIKeyKey adminAPIKeyContextKey = "jellycompat_admin_api_key"

type apiKeyValidator interface {
	GetByKey(ctx context.Context, key string) (*models.APIKey, error)
	UpdateLastUsed(ctx context.Context, id int64) error
}

type apiKeyUserLoader interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

type AdminAPIKeyAuthenticator struct {
	keys  apiKeyValidator
	users apiKeyUserLoader
}

type adminAPIKeyAuthResult struct {
	ctx    context.Context
	status int
	ok     bool
}

func NewAdminAPIKeyAuthenticator(keys apiKeyValidator, users apiKeyUserLoader) *AdminAPIKeyAuthenticator {
	if keys == nil || users == nil {
		return nil
	}
	return &AdminAPIKeyAuthenticator{keys: keys, users: users}
}

func AdminAPIKeyFromContext(ctx context.Context) bool {
	ok, _ := ctx.Value(adminAPIKeyKey).(bool)
	return ok
}

func (a *AdminAPIKeyAuthenticator) RequireAdminAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := a.authenticate(r)
		if !result.ok {
			writeError(w, result.status, authErrorCode(result.status), authErrorMessage(result.status))
			return
		}
		next.ServeHTTP(w, r.WithContext(result.ctx))
	})
}

func RequireSessionOrAdminAPIKey(sessionAuth *Authenticator, keyAuth *AdminAPIKeyAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := ExtractToken(r)
			if ok && strings.HasPrefix(token, "sa_") {
				result := keyAuth.authenticate(r)
				if !result.ok {
					writeError(w, result.status, authErrorCode(result.status), authErrorMessage(result.status))
					return
				}
				next.ServeHTTP(w, r.WithContext(result.ctx))
				return
			}
			sessionAuth.RequireSession(next).ServeHTTP(w, r)
		})
	}
}

func (a *AdminAPIKeyAuthenticator) authenticate(r *http.Request) adminAPIKeyAuthResult {
	if a == nil || a.keys == nil || a.users == nil {
		return adminAPIKeyAuthResult{ctx: r.Context(), status: http.StatusUnauthorized}
	}
	token, ok := ExtractToken(r)
	if !ok || !strings.HasPrefix(token, "sa_") {
		return adminAPIKeyAuthResult{ctx: r.Context(), status: http.StatusUnauthorized}
	}
	apiKey, err := a.keys.GetByKey(r.Context(), token)
	if err != nil {
		return adminAPIKeyAuthResult{ctx: r.Context(), status: http.StatusUnauthorized}
	}
	user, err := a.users.GetByID(r.Context(), apiKey.UserID)
	if err != nil || user == nil || !user.Enabled {
		return adminAPIKeyAuthResult{ctx: r.Context(), status: http.StatusUnauthorized}
	}
	if user.Role != "admin" {
		return adminAPIKeyAuthResult{ctx: context.WithValue(r.Context(), adminAPIKeyKey, false), status: http.StatusForbidden}
	}
	go func(id int64) {
		if err := a.keys.UpdateLastUsed(context.Background(), id); err != nil {
			slog.Debug("jellycompat api key last-used update failed", "id", id, "error", err)
		}
	}(apiKey.ID)
	return adminAPIKeyAuthResult{
		ctx:    context.WithValue(r.Context(), adminAPIKeyKey, true),
		status: http.StatusOK,
		ok:     true,
	}
}

func authErrorCode(status int) string {
	if status == http.StatusForbidden {
		return "Forbidden"
	}
	return "Unauthorized"
}

func authErrorMessage(status int) string {
	if status == http.StatusForbidden {
		return "Admin access required"
	}
	return "Invalid API key"
}
```

- [ ] **Step 5: Wire dependencies in `cmd/silo/main.go`**

Inside the compat DB wiring block, after `userRepo := auth.NewUserRepository(deps.DB)`, add:

```go
			compatDeps.APIKeyValidator = auth.NewAPIKeyRepository(deps.DB)
			compatDeps.APIKeyUserLoader = userRepo
			compatDeps.ScanQueue = deps.LibraryScanQueue
```

- [ ] **Step 6: Run auth tests**

Run:

```bash
go test ./internal/jellycompat -run 'TestRequireAdminAPIKey'
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/jellycompat/server.go internal/jellycompat/auth_api_key.go internal/jellycompat/auth_test.go cmd/silo/main.go
git commit -m "feat(jellycompat): accept admin api keys for autoscan"
```

---

### Task 4: Add Jellyfin Autoscan Handlers and Routes

**Files:**
- Create: `internal/jellycompat/handlers_autoscan.go`
- Create: `internal/jellycompat/handlers_autoscan_test.go`
- Modify: `internal/jellycompat/router.go`

- [ ] **Step 1: Write failing handler tests**

Create `internal/jellycompat/handlers_autoscan_test.go`:

```go
package jellycompat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAutoscanFolders struct {
	folders []*models.MediaFolder
}

func (f *fakeAutoscanFolders) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	for _, folder := range f.folders {
		if folder.ID == id {
			return folder, nil
		}
	}
	return nil, catalog.ErrFolderNotFound
}

func (f *fakeAutoscanFolders) List(context.Context) ([]*models.MediaFolder, error) {
	return f.folders, nil
}

type fakeAutoscanQueue struct {
	calls []queuedScan
}

type queuedScan struct {
	libraryID int
	mode      string
	path      string
	trigger   string
}

func (q *fakeAutoscanQueue) EnqueueScan(_ context.Context, folderID int, mode, path, trigger string) (bool, error) {
	q.calls = append(q.calls, queuedScan{libraryID: folderID, mode: mode, path: path, trigger: trigger})
	return true, nil
}

func TestAutoscanVirtualFoldersIncludesEnabledLocationsForAdminKey(t *testing.T) {
	enabledRoot := t.TempDir()
	disabledRoot := t.TempDir()
	handler := NewAutoscanHandler(&fakeAutoscanFolders{folders: []*models.MediaFolder{
		{ID: 1, Name: "Movies", Type: "movie", Enabled: true, Paths: []string{enabledRoot}},
		{ID: 2, Name: "Disabled", Type: "movie", Enabled: false, Paths: []string{disabledRoot}},
	}}, nil, NewResourceIDCodec(), nil)

	req := httptest.NewRequest(http.MethodGet, "/Library/VirtualFolders", nil)
	req = req.WithContext(context.WithValue(req.Context(), adminAPIKeyKey, true))
	rec := httptest.NewRecorder()

	handler.HandleVirtualFolders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []virtualFolderDTO
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one enabled library, got %d", len(got))
	}
	if got[0].Name != "Movies" || len(got[0].Locations) != 1 || got[0].Locations[0] != enabledRoot {
		t.Fatalf("unexpected folder response: %#v", got[0])
	}
}

func TestAutoscanMediaUpdatedEnqueuesResolvedPath(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	queue := &fakeAutoscanQueue{}
	handler := NewAutoscanHandler(&fakeAutoscanFolders{folders: []*models.MediaFolder{{
		ID:      3,
		Name:    "Movies",
		Type:    "movie",
		Enabled: true,
		Paths:   []string{root},
	}}}, queue, NewResourceIDCodec(), nil)

	body := []byte(`{"Updates":[{"path":` + strconv.Quote(filePath) + `,"updateType":"Modified"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/Library/Media/Updated", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleMediaUpdated(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected one queued scan, got %d", len(queue.calls))
	}
	if queue.calls[0].libraryID != 3 || queue.calls[0].mode != "file" || queue.calls[0].path != filePath || queue.calls[0].trigger != "jellyfin_autoscan" {
		t.Fatalf("unexpected queued scan: %#v", queue.calls[0])
	}
}

func TestAutoscanMediaUpdatedAllOrFail(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	queue := &fakeAutoscanQueue{}
	handler := NewAutoscanHandler(&fakeAutoscanFolders{folders: []*models.MediaFolder{{
		ID:      4,
		Name:    "Movies",
		Type:    "movie",
		Enabled: true,
		Paths:   []string{root},
	}}}, queue, NewResourceIDCodec(), nil)

	payload := map[string]any{"Updates": []map[string]string{
		{"path": filePath, "updateType": "Modified"},
		{"path": filepath.Join(root, "missing.mkv"), "updateType": "Modified"},
	}}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/Library/Media/Updated", bytes.NewReader(data))
	rec := httptest.NewRecorder()

	handler.HandleMediaUpdated(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(queue.calls) != 0 {
		t.Fatalf("expected no partial enqueue, got %#v", queue.calls)
	}
}
```

- [ ] **Step 2: Run handler tests to verify failure**

Run:

```bash
go test ./internal/jellycompat -run 'TestAutoscan'
```

Expected: fail because `NewAutoscanHandler` does not exist.

- [ ] **Step 3: Add Autoscan handler**

Create `internal/jellycompat/handlers_autoscan.go`:

```go
package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

const autoscanTrigger = "jellyfin_autoscan"

type autoscanFolderRepository interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
	List(ctx context.Context) ([]*models.MediaFolder, error)
}

type autoscanVirtualFolderFallback interface {
	HandleVirtualFolders(w http.ResponseWriter, r *http.Request)
}

type AutoscanHandler struct {
	folders  autoscanFolderRepository
	queue    scantrigger.Queuer
	codec    *ResourceIDCodec
	fallback autoscanVirtualFolderFallback
}

func NewAutoscanHandler(
	folders autoscanFolderRepository,
	queue scantrigger.Queuer,
	codec *ResourceIDCodec,
	fallback autoscanVirtualFolderFallback,
) *AutoscanHandler {
	if codec == nil {
		codec = NewResourceIDCodec()
	}
	return &AutoscanHandler{folders: folders, queue: queue, codec: codec, fallback: fallback}
}

func (h *AutoscanHandler) HandleVirtualFolders(w http.ResponseWriter, r *http.Request) {
	if !AdminAPIKeyFromContext(r.Context()) {
		if h.fallback != nil {
			h.fallback.HandleVirtualFolders(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	if h == nil || h.folders == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Library discovery not available")
		return
	}
	folders, err := h.folders.List(r.Context())
	if err != nil {
		slog.Error("jellycompat autoscan: listing libraries", "error", err)
		writeError(w, http.StatusInternalServerError, "InternalServerError", "Failed to list libraries")
		return
	}
	resp := make([]virtualFolderDTO, 0, len(folders))
	for _, folder := range folders {
		if folder == nil || !folder.Enabled {
			continue
		}
		resp = append(resp, virtualFolderDTO{
			Name:           folder.Name,
			Locations:      folder.Paths,
			CollectionType: libraryCollectionType(folder.Type),
			ItemID:         h.codec.EncodeIntID(EncodedIDLibrary, int64(folder.ID)),
			LibraryOptions: virtualLibraryOptDTO{
				Enabled:                 true,
				EnableRealtimeMonitor:   true,
				EnableInternetProviders: true,
				SeasonZeroDisplayName:   "Specials",
				TypeOptions:             []string{},
			},
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

type mediaUpdatedRequest struct {
	Updates []mediaUpdatedEntry `json:"Updates"`
}

type mediaUpdatedEntry struct {
	Path       string `json:"path"`
	UpdateType string `json:"updateType"`
}

func (h *AutoscanHandler) HandleMediaUpdated(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.folders == nil || h.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Scanner not available")
		return
	}
	var req mediaUpdatedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid request body")
		return
	}
	if len(req.Updates) == 0 {
		writeError(w, http.StatusBadRequest, "BadRequest", "Updates is required")
		return
	}
	scanRequests := make([]scantrigger.Request, 0, len(req.Updates))
	for _, update := range req.Updates {
		path := strings.TrimSpace(update.Path)
		if path == "" {
			writeError(w, http.StatusBadRequest, "BadRequest", "Update path is required")
			return
		}
		scanRequests = append(scanRequests, scantrigger.Request{
			Path:    path,
			Trigger: autoscanTrigger,
		})
	}
	targets, err := scantrigger.NewResolver(h.folders).ResolveAll(r.Context(), scanRequests)
	if err != nil {
		writeScanTriggerError(w, err)
		return
	}
	if err := scantrigger.EnqueueAll(r.Context(), h.queue, targets); err != nil {
		writeScanTriggerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeScanTriggerError(w http.ResponseWriter, err error) {
	var reqErr *scantrigger.RequestError
	if errors.As(err, &reqErr) {
		writeError(w, reqErr.Status, reqErr.Code, reqErr.Message)
		return
	}
	slog.Error("jellycompat autoscan: scan update failed", "error", err)
	writeError(w, http.StatusInternalServerError, "InternalServerError", fmt.Sprintf("Failed to process scan update: %v", err))
}
```

- [ ] **Step 4: Register routes**

In `internal/jellycompat/router.go`, after `itemsHandler` is created, add:

```go
	autoscanHandler := NewAutoscanHandler(deps.FolderRepo, deps.ScanQueue, deps.IDCodec, itemsHandler)
	adminAPIKeyAuth := NewAdminAPIKeyAuthenticator(deps.APIKeyValidator, deps.APIKeyUserLoader)
	autoscanVirtualFoldersRegistered := false
	if deps.Authenticator != nil && adminAPIKeyAuth != nil && autoscanHandler != nil {
		r.With(RequireSessionOrAdminAPIKey(deps.Authenticator, adminAPIKeyAuth)).
			Get("/Library/VirtualFolders", autoscanHandler.HandleVirtualFolders)
		r.With(adminAPIKeyAuth.RequireAdminAPIKey).
			Post("/Library/Media/Updated", autoscanHandler.HandleMediaUpdated)
		autoscanVirtualFoldersRegistered = true
	}
```

Inside the existing authenticated group, replace:

```go
			r.Get("/Library/VirtualFolders", itemsHandler.HandleVirtualFolders)
```

with:

```go
			if !autoscanVirtualFoldersRegistered {
				r.Get("/Library/VirtualFolders", itemsHandler.HandleVirtualFolders)
			}
```

- [ ] **Step 5: Run handler tests**

Run:

```bash
go test ./internal/jellycompat -run 'TestAutoscan'
```

Expected: PASS.

- [ ] **Step 6: Run broader compat tests**

Run:

```bash
go test ./internal/jellycompat
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/jellycompat/handlers_autoscan.go internal/jellycompat/handlers_autoscan_test.go internal/jellycompat/router.go internal/jellycompat/server.go
git commit -m "feat(jellycompat): add autoscan media update route"
```

---

### Task 5: Update Documentation and Full Verification

**Files:**
- Modify: `docs/scan-api.md`

- [ ] **Step 1: Update Autoscan documentation**

Replace the opening paragraph under `## Integration with Autoscan` in `docs/scan-api.md` with:

```markdown
[Autoscan](https://github.com/Cloudbox/autoscan) monitors Sonarr, Radarr, and
other sources for new downloads, then relays scan requests to media servers.
Silo supports Autoscan's stock Jellyfin target through the Jellyfin compatibility
server.

Use:

- URL: Silo's Jellyfin compatibility URL, usually `http://your-server:8096`
- Token: a Silo admin API key beginning with `sa_`
- Target type: Autoscan `jellyfin`

Autoscan discovers library roots from `GET /Library/VirtualFolders` and sends
changed paths to `POST /Library/Media/Updated`. The paths must be server-side
paths as Silo sees them.
```

Keep the custom script section below it, but rename the heading:

```markdown
### Alternative: Autoscan Custom Script Target
```

- [ ] **Step 2: Run final Go tests**

Run:

```bash
go test ./internal/scantrigger ./internal/api/handlers ./internal/jellycompat ./cmd/silo
```

Expected: PASS.

- [ ] **Step 3: Run formatting**

Run:

```bash
gofmt -w internal/scantrigger/scantrigger.go internal/scantrigger/scantrigger_test.go internal/api/handlers/libraries.go internal/jellycompat/auth_api_key.go internal/jellycompat/auth_test.go internal/jellycompat/handlers_autoscan.go internal/jellycompat/handlers_autoscan_test.go internal/jellycompat/router.go internal/jellycompat/server.go cmd/silo/main.go
```

Then rerun:

```bash
go test ./internal/scantrigger ./internal/api/handlers ./internal/jellycompat ./cmd/silo
```

Expected: PASS.

- [ ] **Step 4: Inspect diff**

Run:

```bash
git diff --stat
git diff -- internal/scantrigger internal/api/handlers/libraries.go internal/jellycompat cmd/silo/main.go docs/scan-api.md
```

Expected: only the planned scan resolver, Jellyfin Autoscan compatibility, and docs changes are present.

- [ ] **Step 5: Commit docs and final adjustments**

```bash
git add docs/scan-api.md internal/scantrigger internal/api/handlers/libraries.go internal/jellycompat cmd/silo/main.go
git commit -m "docs: document jellyfin autoscan setup"
```

---

## Plan Self-Review

- Spec coverage:
  - Jellyfin only: Task 4 registers `/Library/Media/Updated` without Emby aliases.
  - Admin Silo API keys as Jellyfin token: Task 3.
  - Real library locations: Task 4.
  - Existing scan behavior reused: Tasks 1 and 2.
  - All-or-fail multi-update validation: Tasks 1 and 4.
  - Tests and docs: Tasks 1, 3, 4, and 5.
- Marker scan: no incomplete-work markers are intentionally left in the tasks.
- Type consistency:
  - `scantrigger.Request`, `scantrigger.Target`, and `scantrigger.Queuer` are introduced before use.
  - `AdminAPIKeyFromContext`, `NewAdminAPIKeyAuthenticator`, and route middleware are introduced before handler routing.
