# Metadata Curation Permission Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one assignable `metadata_curation` account permission that lets non-admin users edit, refresh, and rematch metadata only for items in libraries they are allowed to access.

**Architecture:** Store durable permission keys on `users.permissions` and keep server authorization out of JWT claims so permission changes take effect from current database state. Add a shared item-scoped permission middleware that grants admins all access, grants `metadata_curation` users only when every library containing the target item is inside `users.library_ids`, and leaves unrelated admin surfaces admin-only. Frontend item-detail metadata controls use effective permissions from `/auth/me`; admin user-management screens edit the assigned permission array.

**Tech Stack:** Go, chi middleware, PostgreSQL migrations, pgx, React, TypeScript, TanStack Query, existing Silo admin/user APIs.

---

## Commands

Commands assume the repository root is the cwd.

---

## File Structure

- Create `migrations/140_user_permissions.up.sql`
  - Add `users.permissions text[] NOT NULL DEFAULT '{}'::text[]`.

- Create `migrations/140_user_permissions.down.sql`
  - Drop `users.permissions`.

- Modify `migrations/001_schema.up.sql`
  - Add `permissions text[] DEFAULT '{}'::text[] NOT NULL` to the base `users` table.

- Modify `internal/database/testdata/migrations/001_create_users.up.sql`
  - Keep lightweight test schema aligned with `models.User` scanning.

- Modify `internal/models/user.go`
  - Add assigned permission fields to user models and create/update inputs.

- Create `internal/auth/permissions.go`
  - Define permission constants, validation, assigned/effective permission helpers.

- Create `internal/auth/permissions_test.go`
  - Cover permission validation, de-duplication, and admin effective permissions.

- Modify `internal/auth/repository.go`
  - Read/write `users.permissions`.
  - Bump `access_policy_revision` when permissions change.

- Modify `internal/api/handlers/auth.go`
  - Add effective `permissions` to `/auth/me` and login responses.

- Modify `internal/api/handlers/admin.go`
  - Add assigned `permissions` to admin user create/update/list/detail APIs.
  - Include permission changes in session revocation.

- Create `internal/api/middleware/permissions.go`
  - Add item-scoped metadata curation authorization middleware and PostgreSQL target-library resolver.

- Create `internal/api/middleware/permissions_test.go`
  - Unit test authorization behavior without a database by faking user and target-library resolvers.

- Modify `internal/api/router.go`
  - Instantiate the permission middleware.
  - Move item metadata edit/refresh/match routes out from the admin-only group and behind metadata curation middleware.
  - Keep image, people, marker, library, settings, users, jobs list, and full admin routes admin-only.

- Modify `internal/api/handlers/admin_jobs.go`
  - Allow non-admin callers to read only their own `item_refresh` job by ID so refresh polling works.
  - Keep list access admin-only.

- Create or modify `internal/api/handlers/admin_jobs_test.go`
  - Test the job read predicate.

- Modify `web/src/api/types.ts`
  - Add `permissions` to `User`, `AdminUser`, `CreateUserRequest`, and `UpdateUserRequest`.

- Create `web/src/lib/permissions.ts`
  - Add shared frontend permission constants and helpers.

- Modify `web/src/pages/AdminUsers.tsx`
  - Add a Metadata Curation switch to create/edit user forms.

- Modify `web/src/pages/AdminUserDetail.tsx`
  - Display and edit assigned Metadata Curation permission on the user detail page.

- Modify `web/src/pages/ItemDetail/components/ActionBar.tsx`
  - Split full-admin overflow actions from metadata-curation actions.

- Modify item detail content files:
  - `web/src/pages/ItemDetail/MovieContent.tsx`
  - `web/src/pages/ItemDetail/SeriesContent.tsx`
  - `web/src/pages/ItemDetail/SeasonContent.tsx`
  - `web/src/pages/ItemDetail/EpisodeContent.tsx`
  - Use metadata curation permission for refresh/edit/match controls while preserving admin-only controls such as media locations, play history, and intro marker redetection.

Do not add permission groups. Do not make metadata curation a profile setting. Do not broaden this first pass to people metadata, image selection, marker refresh, or library-wide refresh.

---

## Behavioral Contract

- Admin users can do everything they can do today.
- Non-admin users with assigned `metadata_curation` can:
  - `PATCH /api/v1/admin/items/{id}/metadata`
  - `POST /api/v1/admin/items/{id}/refresh-metadata`
  - `POST /api/v1/admin/items/{id}/match/search`
  - `POST /api/v1/admin/items/{id}/match/apply`
- Non-admin metadata curators cannot use:
  - library-wide metadata refresh
  - image apply/search routes
  - people metadata routes
  - marker/intro refresh routes
  - full admin navigation/routes
  - admin job list
- `users.library_ids IS NULL` means unrestricted library access.
- `users.library_ids = '{}'` means no library access.
- A non-admin curator may mutate an item only when every library containing the target item is inside `users.library_ids`.
- For seasons and episodes, the target library set is resolved from the parent series library membership.
- Permission checks load current user policy from the database. JWTs continue to carry only coarse `role`.
- `/auth/me` returns effective permissions for UI decisions. Admin role implies `metadata_curation` in that effective list.
- Admin user APIs return assigned permissions, not effective permissions, so admins can see what is explicitly granted.

---

### Task 1: Add Permission Storage And Domain Helpers

**Files:**
- Create: `migrations/140_user_permissions.up.sql`
- Create: `migrations/140_user_permissions.down.sql`
- Modify: `migrations/001_schema.up.sql`
- Modify: `internal/database/testdata/migrations/001_create_users.up.sql`
- Modify: `internal/models/user.go`
- Create: `internal/auth/permissions.go`
- Create: `internal/auth/permissions_test.go`
- Modify: `internal/auth/repository.go`

- [ ] **Step 1: Add the database migration**

Create `migrations/140_user_permissions.up.sql`:

```sql
ALTER TABLE public.users
    ADD COLUMN IF NOT EXISTS permissions text[] NOT NULL DEFAULT '{}'::text[];

UPDATE public.users
SET permissions = '{}'::text[]
WHERE permissions IS NULL;
```

Create `migrations/140_user_permissions.down.sql`:

```sql
ALTER TABLE public.users
    DROP COLUMN IF EXISTS permissions;
```

Update `migrations/001_schema.up.sql` so the base `public.users` definition contains:

```sql
    role text,
    permissions text[] DEFAULT '{}'::text[] NOT NULL,
    enabled boolean DEFAULT true,
```

Update `internal/database/testdata/migrations/001_create_users.up.sql` so its `users` table has the same `permissions text[] DEFAULT '{}'::text[] NOT NULL` column near `role`.

- [ ] **Step 2: Add permission fields to user models**

Update `internal/models/user.go`:

```go
type User struct {
	ID                        int
	Email                     string
	Username                  string
	PasswordHash              string
	LocalPasswordLoginEnabled bool
	Role                      string
	Permissions               []string
	Enabled                   bool
	LibraryIDs                []int // nullable in PG (nil = all libraries)
	MaxPlaybackQuality        string
	AccessPolicyRevision      int64
	MaxStreams                int
	MaxTranscodes             int
	MaxProfiles               int
	DownloadAllowed           bool
	DownloadTranscodeAllowed  bool
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}
```

Add permissions to create/update inputs:

```go
type CreateUserInput struct {
	Email                     string
	Username                  string
	Password                  string
	LocalPasswordLoginEnabled *bool
	Role                      string
	Permissions               []string
	LibraryIDs                []int
	MaxPlaybackQuality        string
	MaxStreams                *int
	MaxTranscodes             *int
	MaxProfiles               *int
	DownloadAllowed           *bool
	DownloadTranscodeAllowed  *bool
}

type UpdateUserInput struct {
	Email                     *string
	Username                  *string
	Password                  *string
	LocalPasswordLoginEnabled *bool
	Role                      *string
	Permissions               *[]string
	Enabled                   *bool
	LibraryIDs                *[]int
	MaxPlaybackQuality        *string
	MaxStreams                *int
	MaxTranscodes             *int
	MaxProfiles               *int
	DownloadAllowed           *bool
	DownloadTranscodeAllowed  *bool
}
```

- [ ] **Step 3: Add permission constants and validation**

Create `internal/auth/permissions.go`:

```go
package auth

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

type Permission string

const PermissionMetadataCuration Permission = "metadata_curation"

var assignablePermissions = map[Permission]struct{}{
	PermissionMetadataCuration: {},
}

var effectiveAdminPermissions = []string{
	string(PermissionMetadataCuration),
}

func NormalizePermissions(values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{}, nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		permission := Permission(key)
		if _, ok := assignablePermissions[permission]; !ok {
			return nil, fmt.Errorf("unknown permission %q", key)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

func HasAssignedPermission(user *models.User, permission Permission) bool {
	if user == nil {
		return false
	}
	for _, value := range user.Permissions {
		if value == string(permission) {
			return true
		}
	}
	return false
}

func HasEffectivePermission(user *models.User, permission Permission) bool {
	if user == nil || !user.Enabled {
		return false
	}
	if user.Role == "admin" {
		return true
	}
	return HasAssignedPermission(user, permission)
}

func EffectivePermissions(user *models.User) []string {
	if user == nil || !user.Enabled {
		return []string{}
	}
	if user.Role == "admin" {
		return append([]string(nil), effectiveAdminPermissions...)
	}
	permissions, err := NormalizePermissions(user.Permissions)
	if err != nil {
		return []string{}
	}
	return permissions
}
```

- [ ] **Step 4: Add permission helper tests**

Create `internal/auth/permissions_test.go`:

```go
package auth

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNormalizePermissions_DeduplicatesAndSorts(t *testing.T) {
	got, err := NormalizePermissions([]string{
		" metadata_curation ",
		"metadata_curation",
		"",
	})
	if err != nil {
		t.Fatalf("NormalizePermissions returned error: %v", err)
	}
	want := []string{"metadata_curation"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %#v, want %#v", got, want)
	}
}

func TestNormalizePermissions_RejectsUnknownPermission(t *testing.T) {
	if _, err := NormalizePermissions([]string{"server_owner"}); err == nil {
		t.Fatal("expected unknown permission error")
	}
}

func TestHasEffectivePermission_AdminImpliesMetadataCuration(t *testing.T) {
	user := &models.User{Role: "admin", Enabled: true}
	if !HasEffectivePermission(user, PermissionMetadataCuration) {
		t.Fatal("admin should have metadata curation")
	}
}

func TestHasEffectivePermission_UserRequiresAssignedPermission(t *testing.T) {
	user := &models.User{Role: "user", Enabled: true}
	if HasEffectivePermission(user, PermissionMetadataCuration) {
		t.Fatal("plain user should not have metadata curation")
	}
	user.Permissions = []string{"metadata_curation"}
	if !HasEffectivePermission(user, PermissionMetadataCuration) {
		t.Fatal("assigned user should have metadata curation")
	}
}
```

- [ ] **Step 5: Update `internal/auth/repository.go` scanning and writes**

Update `allColumns`:

```go
const allColumns = `id, email, username, password_hash, local_password_login_enabled, role, permissions, enabled,
	library_ids, max_playback_quality, access_policy_revision,
	max_streams, max_transcodes, max_profiles, download_allowed,
	download_transcode_allowed, created_at, updated_at`
```

Add `&u.Permissions` immediately after `&u.Role` in both `scanUser` and `scanUsers`.

In `Create`, normalize permissions and insert them:

```go
permissions, err := NormalizePermissions(input.Permissions)
if err != nil {
	return nil, err
}

cols := []string{"email", "username", "password_hash", "local_password_login_enabled", "role", "permissions", "library_ids", "max_playback_quality"}
args := []any{
	input.Email,
	input.Username,
	string(hash),
	localPasswordLoginEnabled,
	input.Role,
	permissions,
	input.LibraryIDs,
	input.MaxPlaybackQuality,
}
```

In `Update`, add:

```go
if input.Permissions != nil {
	permissions, err := NormalizePermissions(*input.Permissions)
	if err != nil {
		return err
	}
	setClauses = append(setClauses, fmt.Sprintf("permissions = $%d", argIndex))
	args = append(args, permissions)
	argIndex++
}
```

Before appending `updated_at = NOW()`, bump policy revision when access policy changes:

```go
if input.Role != nil ||
	input.Enabled != nil ||
	input.LibraryIDs != nil ||
	input.MaxPlaybackQuality != nil ||
	input.Permissions != nil {
	setClauses = append(setClauses, "access_policy_revision = access_policy_revision + 1")
}
```

- [ ] **Step 6: Run focused auth tests**

Run:

```bash
go test ./internal/auth -run 'TestNormalizePermissions|TestHasEffectivePermission' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add migrations/140_user_permissions.up.sql migrations/140_user_permissions.down.sql migrations/001_schema.up.sql internal/database/testdata/migrations/001_create_users.up.sql internal/models/user.go internal/auth/permissions.go internal/auth/permissions_test.go internal/auth/repository.go
git commit -m "feat(auth): add assignable user permissions"
```

---

### Task 2: Surface Permissions In Auth And Admin User APIs

**Files:**
- Modify: `internal/api/handlers/auth.go`
- Modify: `internal/api/handlers/admin.go`
- Modify: `web/src/api/types.ts`

- [ ] **Step 1: Add permissions to auth user responses**

In `internal/api/handlers/auth.go`, update `userResponse`:

```go
type userResponse struct {
	ID              int                    `json:"id"`
	Username        string                 `json:"username"`
	Email           string                 `json:"email"`
	Role            string                 `json:"role"`
	Permissions     []string               `json:"permissions"`
	DownloadAllowed bool                   `json:"download_allowed"`
	Impersonation   *impersonationResponse `json:"impersonation,omitempty"`
}
```

Update `buildUserResponse`:

```go
resp := userResponse{
	ID:              user.ID,
	Username:        user.Username,
	Email:           user.Email,
	Role:            user.Role,
	Permissions:     auth.EffectivePermissions(user),
	DownloadAllowed: user.DownloadAllowed,
}
```

- [ ] **Step 2: Add assigned permissions to admin user requests/responses**

In `internal/api/handlers/admin.go`, add `Permissions []string` to `createUserRequest`:

```go
type createUserRequest struct {
	Username                 string   `json:"username"`
	Email                    string   `json:"email"`
	Password                 string   `json:"password"`
	Role                     string   `json:"role"`
	Permissions              []string `json:"permissions"`
	CreateDefaultProfile     bool     `json:"create_default_profile"`
	DefaultProfileName       string   `json:"default_profile_name,omitempty"`
	LibraryIDs               []int    `json:"library_ids"`
	MaxPlaybackQuality       string   `json:"max_playback_quality"`
	MaxStreams               *int     `json:"max_streams,omitempty"`
	MaxTranscodes            *int     `json:"max_transcodes,omitempty"`
	MaxProfiles              *int     `json:"max_profiles,omitempty"`
	DownloadAllowed          *bool    `json:"download_allowed,omitempty"`
	DownloadTranscodeAllowed *bool    `json:"download_transcode_allowed,omitempty"`
}
```

Add a reusable JSON field for optional string slices:

```go
type updateStringSliceField struct {
	Set   bool
	Value []string
}

func (f *updateStringSliceField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Value = []string{}
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}

func (f updateStringSliceField) Ptr() *[]string {
	if !f.Set {
		return nil
	}
	value := append([]string(nil), f.Value...)
	return &value
}
```

Add it to `updateUserRequest`:

```go
Permissions updateStringSliceField `json:"permissions,omitempty"`
```

Add assigned permissions to `adminUserResponse`:

```go
Permissions []string `json:"permissions"`
```

Update `toAdminUserResponse`:

```go
Permissions: append([]string(nil), u.Permissions...),
```

- [ ] **Step 3: Validate and persist admin user permissions**

In `HandleCreateUser`, normalize before calling the provisioner:

```go
permissions, err := auth.NormalizePermissions(req.Permissions)
if err != nil {
	writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	return
}
```

Pass into `models.CreateUserInput`:

```go
Permissions: permissions,
```

In `HandleUpdateUser`, normalize only when present:

```go
var permissions *[]string
if req.Permissions.Set {
	normalized, err := auth.NormalizePermissions(req.Permissions.Value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	permissions = &normalized
}
```

Pass into `models.UpdateUserInput`:

```go
Permissions: permissions,
```

Update session revocation:

```go
func updateRequiresSessionRevocation(req updateUserRequest) bool {
	return req.Password != nil ||
		req.Role != nil ||
		req.Enabled != nil ||
		req.LibraryIDs.Set ||
		req.Permissions.Set ||
		req.MaxPlaybackQuality != nil
}
```

- [ ] **Step 4: Update frontend API types**

In `web/src/api/types.ts`, update `User`:

```ts
export interface User {
  id: number;
  username: string;
  email: string;
  role: string;
  permissions: string[];
  download_allowed: boolean;
  impersonation?: ImpersonationInfo | null;
}
```

Update `AdminUser`:

```ts
export interface AdminUser {
  id: number;
  username: string;
  email: string;
  role: string;
  permissions: string[];
  enabled: boolean;
  library_ids: number[] | null;
  max_playback_quality: string;
  max_streams: number;
  max_transcodes: number;
  max_profiles: number;
  download_allowed: boolean;
  download_transcode_allowed: boolean;
  created_at: string;
  updated_at: string;
  last_active_at?: string;
}
```

Update request types:

```ts
export interface CreateUserRequest {
  username: string;
  email: string;
  password: string;
  role: string;
  permissions?: string[];
  create_default_profile?: boolean;
  default_profile_name?: string;
  library_ids?: number[] | null;
  max_playback_quality?: string;
  max_streams?: number;
  max_transcodes?: number;
  max_profiles?: number;
  download_allowed?: boolean;
  download_transcode_allowed?: boolean;
}

export interface UpdateUserRequest {
  username?: string;
  email?: string;
  password?: string;
  role?: string;
  permissions?: string[];
  enabled?: boolean;
  library_ids?: number[] | null;
  max_playback_quality?: string;
  max_streams?: number;
  max_transcodes?: number;
  max_profiles?: number;
  download_allowed?: boolean;
  download_transcode_allowed?: boolean;
}
```

- [ ] **Step 5: Run focused compile checks**

Run:

```bash
go test ./internal/api/handlers -run 'TestNonExistent' -count=1
```

Expected: package compiles and reports no tests to run or PASS.

Run:

```bash
cd web && pnpm exec tsc --noEmit
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/handlers/auth.go internal/api/handlers/admin.go web/src/api/types.ts
git commit -m "feat(auth): expose user permissions"
```

---

### Task 3: Add Item-Scoped Metadata Curation Middleware

**Files:**
- Create: `internal/api/middleware/permissions.go`
- Create: `internal/api/middleware/permissions_test.go`

- [ ] **Step 1: Write middleware tests first**

Create `internal/api/middleware/permissions_test.go`:

```go
package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakePermissionUserLoader struct {
	user *models.User
	err  error
}

func (f fakePermissionUserLoader) GetByID(context.Context, int) (*models.User, error) {
	return f.user, f.err
}

type fakeTargetLibraryResolver struct {
	ids []int
	err error
}

func (f fakeTargetLibraryResolver) ResolveMetadataTargetLibraryIDs(context.Context, string) ([]int, error) {
	return f.ids, f.err
}

func requestWithItemID(role string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/admin/items/item-1/refresh-metadata", nil)
	ctx := SetClaims(req.Context(), &auth.Claims{UserID: 7, Role: role, TokenType: auth.TokenTypeAccess})
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "item-1")
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	return req.WithContext(ctx)
}

func runMetadataCurationMiddleware(user *models.User, libraryIDs []int, role string) int {
	mw := NewPermissionMiddleware(
		fakePermissionUserLoader{user: user},
		fakeTargetLibraryResolver{ids: libraryIDs},
	)
	next := mw.RequireMetadataCurationForItem(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, requestWithItemID(role))
	return rec.Code
}

func TestRequireMetadataCurationForItem_AllowsAdmin(t *testing.T) {
	code := runMetadataCurationMiddleware(nil, nil, "admin")
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireMetadataCurationForItem_RejectsUserWithoutPermission(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: []int{1}, Permissions: nil}
	code := runMetadataCurationMiddleware(user, []int{1}, "user")
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", code, http.StatusForbidden)
	}
}

func TestRequireMetadataCurationForItem_AllowsUnrestrictedCurator(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: nil, Permissions: []string{"metadata_curation"}}
	code := runMetadataCurationMiddleware(user, []int{1, 2}, "user")
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireMetadataCurationForItem_AllowsWhenAllTargetLibrariesAreAllowed(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: []int{1, 2, 3}, Permissions: []string{"metadata_curation"}}
	code := runMetadataCurationMiddleware(user, []int{1, 3}, "user")
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireMetadataCurationForItem_RejectsWhenAnyTargetLibraryIsOutsideAccess(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: []int{1}, Permissions: []string{"metadata_curation"}}
	code := runMetadataCurationMiddleware(user, []int{1, 2}, "user")
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", code, http.StatusForbidden)
	}
}

func TestRequireMetadataCurationForItem_NotFoundWhenTargetHasNoLibraries(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: nil, Permissions: []string{"metadata_curation"}}
	code := runMetadataCurationMiddleware(user, nil, "user")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", code, http.StatusNotFound)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail to compile**

Run:

```bash
go test ./internal/api/middleware -run 'TestRequireMetadataCurationForItem' -count=1
```

Expected: FAIL because `NewPermissionMiddleware` does not exist.

- [ ] **Step 3: Implement middleware and target-library resolver**

Create `internal/api/middleware/permissions.go`:

```go
package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type PermissionUserLoader interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

type MetadataTargetLibraryResolver interface {
	ResolveMetadataTargetLibraryIDs(ctx context.Context, contentID string) ([]int, error)
}

type PermissionMiddleware struct {
	users     PermissionUserLoader
	libraries MetadataTargetLibraryResolver
}

func NewPermissionMiddleware(users PermissionUserLoader, libraries MetadataTargetLibraryResolver) *PermissionMiddleware {
	return &PermissionMiddleware{users: users, libraries: libraries}
}

func (m *PermissionMiddleware) RequireMetadataCurationForItem(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil {
			writeUnauthorized(w, "Authentication required")
			return
		}
		if claims.Role == "admin" {
			next.ServeHTTP(w, r)
			return
		}
		if m == nil || m.users == nil || m.libraries == nil {
			writeForbidden(w, "Metadata curation permission required")
			return
		}

		contentID := chi.URLParam(r, "id")
		if contentID == "" {
			writePermissionError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
			return
		}

		user, err := m.users.GetByID(r.Context(), claims.UserID)
		if err != nil || user == nil || !user.Enabled {
			writeForbidden(w, "Metadata curation permission required")
			return
		}
		if !auth.HasEffectivePermission(user, auth.PermissionMetadataCuration) {
			writeForbidden(w, "Metadata curation permission required")
			return
		}

		targetLibraries, err := m.libraries.ResolveMetadataTargetLibraryIDs(r.Context(), contentID)
		if err != nil {
			writePermissionError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve item libraries")
			return
		}
		if len(targetLibraries) == 0 {
			writePermissionError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		if !metadataTargetWithinUserLibraries(user.LibraryIDs, targetLibraries) {
			writeForbidden(w, "Item is outside your assigned libraries")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func metadataTargetWithinUserLibraries(allowed []int, target []int) bool {
	if allowed == nil {
		return true
	}
	if len(target) == 0 {
		return false
	}
	allowedSet := make(map[int]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	for _, id := range target {
		if _, ok := allowedSet[id]; !ok {
			return false
		}
	}
	return true
}

type PGMetadataTargetLibraryResolver struct {
	Pool *pgxpool.Pool
}

func NewPGMetadataTargetLibraryResolver(pool *pgxpool.Pool) *PGMetadataTargetLibraryResolver {
	return &PGMetadataTargetLibraryResolver{Pool: pool}
}

func (r *PGMetadataTargetLibraryResolver) ResolveMetadataTargetLibraryIDs(ctx context.Context, contentID string) ([]int, error) {
	if r == nil || r.Pool == nil {
		return nil, fmt.Errorf("database not configured")
	}
	rows, err := r.Pool.Query(ctx, `
		WITH target_root AS (
			SELECT mi.content_id
			FROM media_items mi
			WHERE mi.content_id = $1
			UNION
			SELECT s.series_id
			FROM seasons s
			WHERE s.content_id = $1
			UNION
			SELECT e.series_id
			FROM episodes e
			WHERE e.content_id = $1
		)
		SELECT DISTINCT mil.media_folder_id
		FROM target_root tr
		JOIN media_item_libraries mil ON mil.content_id = tr.content_id
		ORDER BY mil.media_folder_id`, contentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func writePermissionError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: code, Message: message})
}
```

- [ ] **Step 4: Run middleware tests**

Run:

```bash
go test ./internal/api/middleware -run 'TestRequireMetadataCurationForItem' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/middleware/permissions.go internal/api/middleware/permissions_test.go
git commit -m "feat(api): authorize item metadata curation"
```

---

### Task 4: Wire Routes And Scoped Job Polling

**Files:**
- Modify: `internal/api/router.go`
- Modify: `internal/api/handlers/admin_jobs.go`
- Create or modify: `internal/api/handlers/admin_jobs_test.go`

- [ ] **Step 1: Add job access predicate tests**

Create `internal/api/handlers/admin_jobs_test.go` if it does not exist, or append to it:

```go
package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCanReadAdminJob_AdminCanReadAnyJob(t *testing.T) {
	claims := &auth.Claims{UserID: 1, Role: "admin"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeCatalogExport}
	if !canReadAdminJob(claims, job) {
		t.Fatal("admin should be allowed to read any job")
	}
}

func TestCanReadAdminJob_CreatorCanReadOwnItemRefreshJob(t *testing.T) {
	claims := &auth.Claims{UserID: 2, Role: "user"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeItemRefresh}
	if !canReadAdminJob(claims, job) {
		t.Fatal("creator should be allowed to read own item refresh job")
	}
}

func TestCanReadAdminJob_CreatorCannotReadOwnNonItemRefreshJob(t *testing.T) {
	claims := &auth.Claims{UserID: 2, Role: "user"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeCatalogExport}
	if canReadAdminJob(claims, job) {
		t.Fatal("non-admin should not read non-item-refresh jobs")
	}
}

func TestCanReadAdminJob_OtherUserCannotReadItemRefreshJob(t *testing.T) {
	claims := &auth.Claims{UserID: 3, Role: "user"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeItemRefresh}
	if canReadAdminJob(claims, job) {
		t.Fatal("non-admin should not read another user's item refresh job")
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
go test ./internal/api/handlers -run 'TestCanReadAdminJob' -count=1
```

Expected: FAIL because `canReadAdminJob` does not exist.

- [ ] **Step 3: Implement scoped job reads**

In `internal/api/handlers/admin_jobs.go`, update `HandleGet` after loading the job:

```go
claims := apimw.GetClaims(r.Context())
if !canReadAdminJob(claims, job) {
	writeError(w, http.StatusForbidden, "forbidden", "Admin access required")
	return
}

response := adminJobToResponse(r, job, h.store)
if claims == nil || claims.Role != "admin" {
	response.RequestPayload = json.RawMessage(`{}`)
	response.PublicURL = ""
	response.DownloadURL = ""
	response.DownloadExpiresAt = nil
}
writeJSON(w, http.StatusOK, response)
```

Add the helper near `currentAdminUserID`:

```go
func canReadAdminJob(claims *auth.Claims, job *models.AdminJob) bool {
	if claims == nil || job == nil {
		return false
	}
	if claims.Role == "admin" {
		return true
	}
	return job.JobType == adminjob.JobTypeItemRefresh && job.CreatedByUserID == claims.UserID
}
```

Add the `auth` import if it is not already present:

```go
"github.com/Silo-Server/silo-server/internal/auth"
```

- [ ] **Step 4: Instantiate permission middleware in the router**

In `internal/api/router.go`, after `viewerAccessMiddleware` setup, add:

```go
var permissionMiddleware *apimw.PermissionMiddleware
if userRepo != nil && deps.DB != nil {
	permissionMiddleware = apimw.NewPermissionMiddleware(
		userRepo,
		apimw.NewPGMetadataTargetLibraryResolver(deps.DB),
	)
}
```

- [ ] **Step 5: Split `/admin` routes**

Replace the single admin route group:

```go
r.Route("/admin", func(r chi.Router) {
	r.Use(apimw.RequireAdmin)
	// current admin route declarations
})
```

with this shape:

```go
r.Route("/admin", func(r chi.Router) {
	metadataItemAccess := apimw.RequireAdmin
	if permissionMiddleware != nil {
		metadataItemAccess = permissionMiddleware.RequireMetadataCurationForItem
	}

	r.Group(func(r chi.Router) {
		r.Use(metadataItemAccess)
		r.Post("/items/{id}/refresh-metadata", adminHandler.HandleRefreshItemMetadata)
		r.Patch("/items/{id}/metadata", adminHandler.HandleUpdateItemMetadata)
		if adminMatchHandler != nil {
			r.Post("/items/{id}/match/search", adminMatchHandler.HandleSearchItemMatchCandidates)
			r.Post("/items/{id}/match/apply", adminMatchHandler.HandleApplyItemMatch)
		}
	})

	if adminJobsHandler != nil {
		r.Get("/jobs/{id}", adminJobsHandler.HandleGet)
	}

	r.Group(func(r chi.Router) {
		r.Use(apimw.RequireAdmin)

		r.Get("/users", adminHandler.HandleListUsers)
		r.Post("/users", adminHandler.HandleCreateUser)
		r.Get("/users/{id}", adminHandler.HandleGetUser)
		r.Put("/users/{id}", adminHandler.HandleUpdateUser)
		r.Delete("/users/{id}", adminHandler.HandleDeleteUser)
		r.Post("/users/{id}/impersonate", adminHandler.HandleImpersonateUser)

		// Move these existing route declarations into this admin-only group
		// without changing their handler names:
		// users, user profiles/settings/device settings, devices, sessions,
		// playback history, unmatched, stats, settings, section settings,
		// item marker/intro refresh, people refresh/update, item images,
		// filesystem browse, catalog seed import/export, plugins, logs,
		// subtitle providers, tasks, task metrics, scans, nodes, requests,
		// history imports, sections, collections, collection groups,
		// recommendation admin routes, system routes, API keys, and rate limits.
		//
		// Do not duplicate /items/{id}/refresh-metadata,
		// /items/{id}/metadata, /items/{id}/match/search,
		// /items/{id}/match/apply, or /jobs/{id}.

		if adminJobsHandler != nil {
			r.Route("/jobs", func(r chi.Router) {
				r.Get("/", adminJobsHandler.HandleList)
			})
		}
	})
})
```

When moving route declarations, compare against the current `r.Route("/admin", ...)` block and keep every admin-only path not listed in the duplication warning in the `RequireAdmin` group with the same path and handler.

- [ ] **Step 6: Run focused backend checks**

Run:

```bash
go test ./internal/api/handlers -run 'TestCanReadAdminJob' -count=1
```

Expected: PASS.

Run:

```bash
go test ./internal/api/middleware -run 'TestRequireMetadataCurationForItem' -count=1
```

Expected: PASS.

Run:

```bash
go test ./internal/api -run 'TestNonExistent' -count=1
```

Expected: package compiles and reports no tests to run or PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/api/router.go internal/api/handlers/admin_jobs.go internal/api/handlers/admin_jobs_test.go
git commit -m "feat(api): route metadata curation by permission"
```

---

### Task 5: Add Frontend Permission Helpers

**Files:**
- Create: `web/src/lib/permissions.ts`

- [ ] **Step 1: Add shared helper**

Create `web/src/lib/permissions.ts`:

```ts
import type { User } from "@/api/types";

export const PERMISSION_METADATA_CURATION = "metadata_curation";

export function hasPermission(
  user: Pick<User, "role" | "permissions"> | null | undefined,
  permission: string,
) {
  if (!user) return false;
  if (user.role === "admin") return true;
  return Array.isArray(user.permissions) && user.permissions.includes(permission);
}

export function canCurateMetadata(user: Pick<User, "role" | "permissions"> | null | undefined) {
  return hasPermission(user, PERMISSION_METADATA_CURATION);
}
```

- [ ] **Step 2: Run frontend type check**

Run:

```bash
cd web && pnpm exec tsc --noEmit
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/permissions.ts
git commit -m "feat(web): add permission helpers"
```

---

### Task 6: Add Metadata Curation Toggle To User Management

**Files:**
- Modify: `web/src/pages/AdminUsers.tsx`
- Modify: `web/src/pages/AdminUserDetail.tsx`

- [ ] **Step 1: Add helpers local to each user form file**

In both files, import:

```ts
import { PERMISSION_METADATA_CURATION } from "@/lib/permissions";
```

Add local helpers near other small helpers:

```ts
function hasAssignedPermission(permissions: string[] | undefined, permission: string) {
  return Array.isArray(permissions) && permissions.includes(permission);
}

function setAssignedPermission(permissions: string[], permission: string, enabled: boolean) {
  const next = new Set(permissions);
  if (enabled) {
    next.add(permission);
  } else {
    next.delete(permission);
  }
  return Array.from(next).sort();
}
```

- [ ] **Step 2: Update `AdminUsers.tsx` create/edit form state and submit bodies**

Inside `UserForm`, add:

```ts
const [permissions, setPermissions] = useState<string[]>(user?.permissions ?? []);
const metadataCurationId = useId();
```

In the update body:

```ts
permissions,
```

In the create body:

```ts
permissions,
```

In the Access tab, after `LibraryAccessSelector`, add:

```tsx
<div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
  <div>
    <Label htmlFor={metadataCurationId}>Metadata Curation</Label>
    <p className="text-muted-foreground text-xs">
      Edit, refresh, and rematch metadata within assigned libraries.
    </p>
  </div>
  <Switch
    id={metadataCurationId}
    checked={hasAssignedPermission(permissions, PERMISSION_METADATA_CURATION)}
    onCheckedChange={(checked) =>
      setPermissions((current) =>
        setAssignedPermission(current, PERMISSION_METADATA_CURATION, checked),
      )
    }
  />
</div>
```

- [ ] **Step 3: Update `AdminUserDetail.tsx` edit form and summary**

In the user detail summary near role/library/download rows, add a row:

```tsx
<DetailRow
  label="Metadata Curation"
  value={hasAssignedPermission(user.permissions, PERMISSION_METADATA_CURATION) ? "Allowed" : "Not allowed"}
/>
```

Inside `EditUserForm`, add:

```ts
const [permissions, setPermissions] = useState<string[]>(user.permissions ?? []);
const metadataCurationId = useId();
```

In the update body:

```ts
permissions,
```

In the Access tab, after `LibraryAccessSelector`, add:

```tsx
<div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
  <div>
    <Label htmlFor={metadataCurationId}>Metadata Curation</Label>
    <p className="text-muted-foreground text-xs">
      Edit, refresh, and rematch metadata within assigned libraries.
    </p>
  </div>
  <Switch
    id={metadataCurationId}
    checked={hasAssignedPermission(permissions, PERMISSION_METADATA_CURATION)}
    onCheckedChange={(checked) =>
      setPermissions((current) =>
        setAssignedPermission(current, PERMISSION_METADATA_CURATION, checked),
      )
    }
  />
</div>
```

- [ ] **Step 4: Run frontend lint/type check**

Run:

```bash
cd web && pnpm exec tsc --noEmit
```

Expected: PASS.

Run:

```bash
cd web && pnpm run lint
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/AdminUsers.tsx web/src/pages/AdminUserDetail.tsx
git commit -m "feat(web): assign metadata curation permission"
```

---

### Task 7: Show Item Metadata Controls For Curators

**Files:**
- Modify: `web/src/pages/ItemDetail/components/ActionBar.tsx`
- Modify: `web/src/pages/ItemDetail/MovieContent.tsx`
- Modify: `web/src/pages/ItemDetail/SeriesContent.tsx`
- Modify: `web/src/pages/ItemDetail/SeasonContent.tsx`
- Modify: `web/src/pages/ItemDetail/EpisodeContent.tsx`

- [ ] **Step 1: Split ActionBar full-admin and metadata-curation actions**

In `ActionBarProps`, add:

```ts
canCurateMetadata?: boolean;
```

Destructure it:

```ts
canCurateMetadata = false,
```

Add derived booleans near `hasOverflowActions`:

```ts
const hasAdminActions = Boolean(
  isAdmin && (contentId || onRedetectIntro),
);
const hasMetadataActions = Boolean(
  canCurateMetadata && (onRefresh || onEditMetadata || onMatchItem),
);
```

Replace the existing `{isAdmin && (...)}` block in the overflow menu with:

```tsx
{(hasAdminActions || hasMetadataActions) && (
  <>
    {hasOverflowActions && <DropdownMenuSeparator />}
    {isAdmin && contentId && (
      <DropdownMenuItem
        onSelect={() =>
          navigate(`/admin/history?media_item_id=${encodeURIComponent(contentId)}`)
        }
      >
        View Play History
      </DropdownMenuItem>
    )}
    {canCurateMetadata && onRefresh && (
      <DropdownMenuItem
        disabled={isRefreshing}
        onSelect={() => {
          setRefreshDialogOpen(true);
        }}
      >
        {isRefreshing && <RefreshCw className="size-4 animate-spin" />}
        Refresh Metadata
      </DropdownMenuItem>
    )}
    {isAdmin && onRedetectIntro && (
      <DropdownMenuItem disabled={isRedetectingIntro} onSelect={onRedetectIntro}>
        <RefreshCw className={`size-4 ${isRedetectingIntro ? "animate-spin" : ""}`} />
        Re-detect Intro Markers
      </DropdownMenuItem>
    )}
    {canCurateMetadata && onEditMetadata && (
      <DropdownMenuItem onSelect={onEditMetadata}>
        <Pencil className="size-4" />
        Edit Metadata
      </DropdownMenuItem>
    )}
    {canCurateMetadata && onMatchItem && (
      <DropdownMenuItem onSelect={onMatchItem}>
        <Search className="size-4" />
        Match Item
      </DropdownMenuItem>
    )}
  </>
)}
```

Keep `RefreshMetadataDialog` mounted as it is today.

- [ ] **Step 2: Update movie item detail**

In `web/src/pages/ItemDetail/MovieContent.tsx`, import:

```ts
import { canCurateMetadata as canCurateMetadataForUser } from "@/lib/permissions";
```

After `isAdmin`:

```ts
const canCurateMetadata = canCurateMetadataForUser(user);
```

Update `ActionBar` props:

```tsx
isAdmin={isAdmin}
canCurateMetadata={canCurateMetadata}
onEditMetadata={canCurateMetadata ? () => setEditOpen(true) : undefined}
onMatchItem={canCurateMetadata ? () => setMatchOpen(true) : undefined}
```

Update dialog rendering:

```tsx
{canCurateMetadata && <EditMetadataDialog item={item} open={editOpen} onOpenChange={setEditOpen} />}
{canCurateMetadata && (
  <MatchItemDialog
    key={item.content_id}
    item={item}
    open={matchOpen}
    onOpenChange={setMatchOpen}
  />
)}
```

Keep media locations admin-only:

```tsx
{isAdmin && <MediaLocations title="Media locations" versions={item.versions} />}
```

- [ ] **Step 3: Update series item detail**

In `web/src/pages/ItemDetail/SeriesContent.tsx`, import:

```ts
import { canCurateMetadata as canCurateMetadataForUser } from "@/lib/permissions";
```

After `isAdmin`:

```ts
const canCurateMetadata = canCurateMetadataForUser(user);
```

Update `ActionBar`:

```tsx
isAdmin={isAdmin}
canCurateMetadata={canCurateMetadata}
onEditMetadata={canCurateMetadata ? () => setEditOpen(true) : undefined}
onMatchItem={canCurateMetadata ? () => setMatchOpen(true) : undefined}
```

Update dialog rendering:

```tsx
{canCurateMetadata && <EditMetadataDialog item={item} open={editOpen} onOpenChange={setEditOpen} />}
{canCurateMetadata && (
  <MatchItemDialog
    key={item.content_id}
    item={item}
    open={matchOpen}
    onOpenChange={setMatchOpen}
  />
)}
```

- [ ] **Step 4: Update season item detail**

In `web/src/pages/ItemDetail/SeasonContent.tsx`, import:

```ts
import { canCurateMetadata as canCurateMetadataForUser } from "@/lib/permissions";
```

After `isAdmin`:

```ts
const canCurateMetadata = canCurateMetadataForUser(user);
```

Update `ActionBar`:

```tsx
isAdmin={isAdmin}
canCurateMetadata={canCurateMetadata}
onEditMetadata={canCurateMetadata ? () => setEditOpen(true) : undefined}
```

Update dialog rendering:

```tsx
{canCurateMetadata && <EditMetadataDialog item={item} open={editOpen} onOpenChange={setEditOpen} />}
```

- [ ] **Step 5: Update episode item detail**

In `web/src/pages/ItemDetail/EpisodeContent.tsx`, import:

```ts
import { canCurateMetadata as canCurateMetadataForUser } from "@/lib/permissions";
```

After `isAdmin`:

```ts
const canCurateMetadata = canCurateMetadataForUser(user);
```

Update `ActionBar`:

```tsx
isAdmin={isAdmin}
canCurateMetadata={canCurateMetadata}
onRedetectIntro={isAdmin ? () => redetectIntroMutation.mutate(item.content_id) : undefined}
onEditMetadata={canCurateMetadata ? () => setEditOpen(true) : undefined}
```

Keep media locations and intro redetection admin-only. Update dialog rendering to use `canCurateMetadata`.

- [ ] **Step 6: Run frontend checks**

Run:

```bash
cd web && pnpm exec tsc --noEmit
```

Expected: PASS.

Run:

```bash
cd web && pnpm run lint
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add web/src/pages/ItemDetail/components/ActionBar.tsx web/src/pages/ItemDetail/MovieContent.tsx web/src/pages/ItemDetail/SeriesContent.tsx web/src/pages/ItemDetail/SeasonContent.tsx web/src/pages/ItemDetail/EpisodeContent.tsx
git commit -m "feat(web): show metadata tools to curators"
```

---

### Task 8: End-To-End Verification

**Files:**
- No new files.

- [ ] **Step 1: Run focused backend tests**

Run:

```bash
go test ./internal/auth ./internal/api/middleware ./internal/api/handlers -run 'TestNormalizePermissions|TestHasEffectivePermission|TestRequireMetadataCurationForItem|TestCanReadAdminJob' -count=1
```

Expected: PASS.

- [ ] **Step 2: Run broader API compile/test check**

Run:

```bash
go test ./internal/api/... ./internal/auth/... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run frontend checks**

Run:

```bash
cd web && pnpm exec tsc --noEmit
```

Expected: PASS.

Run:

```bash
cd web && pnpm run lint
```

Expected: PASS.

- [ ] **Step 4: Verify local path hygiene**

Run:

```bash
make verify-local-paths
```

Expected: PASS.

- [ ] **Step 5: Manual behavior verification**

Use an admin account to create or edit a normal user with:

```text
permissions = ["metadata_curation"]
library_ids = [one library containing a known item]
```

Then verify:

```text
1. The user can open that item.
2. The item detail overflow menu shows Refresh Metadata, Edit Metadata, and Match Item.
3. The user can save a small metadata edit for that item.
4. The user can search match candidates for that item.
5. The user can queue a metadata refresh and the web UI observes the job completion.
6. The same user cannot edit, refresh, or rematch an item whose target library set includes a library outside their assigned library IDs.
7. The same user cannot open full admin pages such as /admin/users or /admin/settings.
8. The same user cannot call image apply, people update, marker refresh, library refresh, or admin job list endpoints.
9. An admin account can still use all existing admin metadata and non-metadata routes.
```

- [ ] **Step 6: Commit verification-only fixes if any**

If verification exposes small follow-up fixes, commit them with a scoped message:

```bash
git add <changed-files>
git commit -m "fix(auth): tighten metadata curation access"
```

---

## Acceptance Criteria

- `users.permissions` stores assigned account permission keys.
- `metadata_curation` is the only assignable permission in this first pass.
- `/auth/me` and login responses include effective permissions.
- Admin user APIs include assigned permissions and reject unknown permission keys.
- Non-admin users without `metadata_curation` remain forbidden from item metadata mutation routes.
- Non-admin users with `metadata_curation` can edit, refresh, and match only items fully contained by their account-level allowed libraries.
- Seasons and episodes inherit library scope from their parent series.
- Metadata refresh polling works for curators without exposing the admin job list.
- Full admin UI and unrelated admin APIs remain admin-only.
- Frontend item metadata controls appear for admins and metadata curators; admin-only controls remain admin-only.

---

## Risks And Notes

- Existing access tokens still carry `role`, but permission checks must load the user from the database. Do not add permission claims to JWTs for server authorization.
- Revoking sessions on permission changes follows the existing admin user update pattern and prevents stale frontend auth state from lingering.
- Item metadata is global. The subset check must require all target libraries to be allowed, not merely one matching library.
- Do not use profile library restrictions for this authorization check. This is an account-level permission bounded by `users.library_ids`.
- The first pass intentionally excludes custom permission groups. The `users.permissions text[]` shape is enough to add future permission keys without redesigning storage.
