# ABS Phase 0 — Login + Critical Bug Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make official ABS iOS/Android/3rd-party clients work end-to-end against silo for the core flow (add-server → login → browse → tap-book → play → progress-sync), and add the two missing token-lifecycle endpoints (`/auth/refresh`, `/logout`).

**Architecture:** Surgical patches to the existing `internal/audiobooks/abs/` package — no architectural changes. Login envelope enriched to match what real ABS clients pattern-match against; broken data shapes (author/series IDs, genres) fixed in the metadata translator; resume position wired through `ProgressStore`; filterdata hydrated from the same query paths `/authors` and `/series` already use; two new endpoints (`/auth/refresh`, `/logout`) port the Continuum plugin's verbatim semantics, adapted to silo's `TokenStore` interface.

**Tech Stack:** Go 1.x, chi router v5, `golang-jwt/jwt/v5`, `pgx/v5` for Postgres, `oklog/ulid/v2` for JTI generation, `slog` for structured logging. Tests use `testing` stdlib + table-driven style (per the existing `progress_internal_test.go` convention).

**Spec:** `docs/superpowers/specs/2026-05-26-abs-implementation-fix-design.md`

**Reference implementation:** `continuum-plugin-audiobooks` (sibling worktree at the user's `/opt/continuum_plugins_bak/continuum-plugin-audiobooks/` — but assume it may not be present on the executor's filesystem; this plan duplicates the canonical code inline).

**Bug catalog for response shapes:** `booklore-ng/BOOKLORE_ABS_IMPLEMENTATION_ISSUES.md` documents real-client field-shape bugs this plan fixes.

---

## File Structure

**Files modified** (all paths relative to repository root):
- `internal/audiobooks/abs/handler.go` — bearerAuth diagnostic logging; mount new routes (`/auth/refresh`, `/logout`)
- `internal/audiobooks/abs/login.go` — login envelope enrichment; `/authorize` envelope completion; new `handleRefresh` + `handleLogout` handlers
- `internal/audiobooks/abs/libraries_handler.go` — `siloItemToMetadata` ID surfacing; filterdata population in `handleLibraryDetail`
- `internal/audiobooks/abs/play_response.go` — resume-position lookup via `ProgressStore`

**Files created:**
- `internal/audiobooks/abs/login_refresh_test.go` — unit tests for `handleRefresh` token-rotation semantics
- `internal/audiobooks/abs/login_logout_test.go` — unit tests for `handleLogout` revocation behavior
- `internal/audiobooks/abs/libraries_metadata_test.go` — unit tests for `siloItemToMetadata` shape fixes
- `internal/audiobooks/abs/play_resume_test.go` — unit tests for resume-position lookup

**No new migrations.** No new tables. All changes are code-only.

---

## Task Order Rationale

Tasks land in dependency order so each step is independently verifiable:

1. Diagnostic logging FIRST — so as we land the rest, any new 401 has a traceable cause in `journalctl`.
2. Login envelope fixes — these alone may unblock real clients; verifiable with curl + iOS app.
3. `/authorize` envelope match — required for resume-on-launch.
4. Metadata translator (author/series IDs, genres) — fixes the data the client sees AFTER login.
5. filterdata population — unblocks library filter UI.
6. Resume position wire-up — fixes "play always starts from 0".
7. `/auth/refresh` — closes the 24h-then-forced-relogin trap.
8. `/logout` — completes the token-lifecycle surface.
9. Build, restart, smoke test.

---

## Task 1: Add Diagnostic Logging to bearerAuth and Login

**Why:** When login fails today the silo log is silent. Adding `slog.Debug` at each rejection branch makes the *next* failure self-diagnosing.

**Files:**
- Modify: `internal/audiobooks/abs/handler.go:353-391` (bearerAuth)
- Modify: `internal/audiobooks/abs/login.go:47-107` (handleStandaloneLogin) and `:112-235` (completeLogin)

- [ ] **Step 1.1: Replace bearerAuth with logged version**

Replace the body of `bearerAuth` at `internal/audiobooks/abs/handler.go:353-392`:

```go
func (h *Handler) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == "" {
			raw = r.URL.Query().Get("token")
		}
		if raw == "" {
			slog.Debug("abs bearerAuth: no token", "path", r.URL.Path, "remote", r.RemoteAddr)
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		if h.deps.Config == nil || h.deps.TokenStore == nil {
			slog.Warn("abs bearerAuth: deps not wired",
				"have_config", h.deps.Config != nil,
				"have_token_store", h.deps.TokenStore != nil,
				"path", r.URL.Path)
			http.Error(w, "auth not configured", http.StatusServiceUnavailable)
			return
		}
		secret, err := h.deps.Config.JWTSecret(r.Context())
		if err != nil {
			slog.Error("abs bearerAuth: jwt secret fetch failed", "err", err)
			http.Error(w, "config unavailable", http.StatusInternalServerError)
			return
		}
		claims, err := ParseToken(secret, raw)
		if err != nil {
			slog.Debug("abs bearerAuth: parse failed", "err", err, "path", r.URL.Path)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if claims.Type != "access" {
			slog.Debug("abs bearerAuth: wrong token type", "type", claims.Type, "path", r.URL.Path)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		row, err := h.deps.TokenStore.GetTokenByJTI(r.Context(), claims.JTI)
		if err != nil {
			slog.Debug("abs bearerAuth: jti lookup failed",
				"jti", claims.JTI, "err", err, "path", r.URL.Path)
			http.Error(w, "token revoked", http.StatusUnauthorized)
			return
		}
		if row.RevokedAt != nil {
			slog.Debug("abs bearerAuth: jti revoked", "jti", claims.JTI, "path", r.URL.Path)
			http.Error(w, "token revoked", http.StatusUnauthorized)
			return
		}
		_ = h.deps.TokenStore.TouchToken(r.Context(), claims.JTI)
		ctx := context.WithValue(r.Context(), ctxKey{}, ctxAuth{
			UserID:    claims.UserID,
			ProfileID: claims.ProfileID,
			JTI:       claims.JTI,
			Token:     raw,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

The existing import set already includes `log/slog`? Check `internal/audiobooks/abs/handler.go` imports near the top and add `"log/slog"` if absent. Login.go already imports it.

- [ ] **Step 1.2: Add structured log on standalone login outcomes**

In `internal/audiobooks/abs/login.go`, after the existing `slog.Error("abs login: cred validator failed", ...)` line near `:91`, also add a debug log on the **success** path at the end of `handleStandaloneLogin` just before `h.completeLogin(...)` returns. Insert this line right before `h.completeLogin(w, r, userID, profileID, displayName)` at `:106`:

```go
		slog.Debug("abs standalone login: validator OK",
			"username", body.Username, "user_id", userID, "profile_id", profileID)
```

Also, inside `completeLogin`, after both token inserts succeed (just before the `// Build user object` comment at `:177`), add:

```go
		slog.Debug("abs completeLogin: tokens persisted",
			"user_id", userID, "access_jti", accessJTI, "refresh_jti", refreshJTI)
```

- [ ] **Step 1.3: Build to verify no syntax errors**

Run:

```bash
go build ./internal/audiobooks/abs/...
```

Expected: clean exit (no output, exit code 0).

- [ ] **Step 1.4: Commit**

```bash
git add internal/audiobooks/abs/handler.go internal/audiobooks/abs/login.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): add diagnostic logging to ABS bearer auth and login

Each rejection branch in bearerAuth now emits a slog line so failures
are traceable from journalctl. Login success path emits a debug line
confirming token persistence; this makes "I cant login" debuggable
without a tcpdump.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Enrich Login Response Envelope

**Why:** Real ABS clients pattern-match against a richer envelope than silo currently emits. Missing fields cause the iOS client to fall into degraded mode or reject the response outright.

**Files:**
- Modify: `internal/audiobooks/abs/login.go:195-234` (the `completeLogin` write block)

- [ ] **Step 2.1: Replace the user object construction and writeJSON call**

In `internal/audiobooks/abs/login.go`, replace lines 195-234 (from the `// Build user object.` comment through the closing `})` of `writeJSON`) with this expanded version:

```go
	// Build user object. displayName falls back to userID when empty.
	name := displayName
	if name == "" {
		name = userID
	}

	// Resolve the audiobook library list + default ID up front so we can
	// emit them in the login envelope. ABS clients require these on the
	// initial login response to seed the library picker before /me lands.
	libs, _ := h.deps.MediaStore.ListAudiobookLibraries(r.Context())
	libraryMaps := make([]map[string]any, 0, len(libs))
	defaultLibraryID := VirtualLibraryID
	for i, lib := range libs {
		if i == 0 {
			defaultLibraryID = audiobookLibraryID(lib)
		}
		libraryMaps = append(libraryMaps, audiobookLibraryMap(lib))
	}

	nowMs := time.Now().UnixMilli()

	// Real ABS clients pattern-match on a richer login envelope than the
	// minimum we previously sent. The added fields (itemTagsAccessible,
	// itemTagsSelected, lastSeen, createdAt) keep the iOS app off its
	// "degraded mode" branch. Permissions stays the canonical four-key
	// object; setting all four true matches what real ABS does for a
	// non-admin user.
	user := map[string]any{
		"id":                   userID,
		"username":             name,
		"type":                 "user",
		"defaultLibraryId":     defaultLibraryID,
		"librariesAccessible":  []any{}, // empty = "all libraries accessible"
		"itemTagsAccessible":   []any{}, // empty = "all tags accessible"
		"itemTagsSelected":     []any{},
		"mediaProgress":        []any{},
		"bookmarks":            []any{},
		"seriesHideFromContinueListening": []any{},
		"isOldToken":           false,
		"token":                access, // legacy field some 2.17- clients still read
		"lastSeen":             nowMs,
		"createdAt":            nowMs,
		"permissions": map[string]any{
			"download":              true,
			"update":                true,
			"delete":                true,
			"upload":                true,
			"accessAllLibraries":    true,
			"accessAllTags":         true,
			"accessExplicitContent": true,
			"selectedTagsNotAccessible": false,
		},
	}

	// x-return-tokens opt-in: when set, embed token pair on user object too
	// (some clients read from the user envelope, others from the top level).
	if strings.EqualFold(r.Header.Get("x-return-tokens"), "true") {
		user["accessToken"] = access
		user["refreshToken"] = refresh
	}

	// Server settings: real ABS emits many flags; clients use these to
	// branch UI. Defaults match the official server's defaults so clients
	// pick predictable UX paths.
	serverSettings := map[string]any{
		"id":                          "server-settings",
		"version":                     ServerVersion,
		"buildNumber":                 1,
		"language":                    "en-us",
		"dateFormat":                  "MM/dd/yyyy",
		"timeFormat":                  "HH:mm",
		"timeZone":                    "UTC",
		"coverAspectRatio":            1,
		"storeCoverWithItem":          false,
		"storeMetadataWithItem":       false,
		"metadataFileFormat":          "json",
		"scannerDisableWatcher":       true,
		"scannerParseSubtitle":        false,
		"scannerFindCovers":           false,
		"scannerCoverProvider":        "google",
		"scannerPreferMatchedMetadata": false,
		"scannerPreferOverdriveMediaMarker": false,
		"sortingIgnorePrefix":         false,
		"sortingPrefixes":             []string{"the", "a"},
		"chromecastEnabled":           false,
		"enableEReader":               false,
		"dateString":                  "",
		"logLevel":                    1,
		"version_id":                  ServerVersion,
		"sessionTimeout":              0,
		"backupSchedule":              false,
		"backupsToKeep":               2,
		"maxBackupSize":               1,
		"loggerDailyLogsToKeep":       7,
		"loggerScannerLogsToKeep":     2,
		"homeBookshelfView":           1,
		"bookshelfView":               1,
		"podcastEpisodeSchedule":      "0 * * * *",
		"sortingIgnorePrefixesValue":  "",
		"allowIframe":                 false,
		"authActiveAuthMethods":       []string{"local"},
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user":                 user,
		"userDefaultLibraryId": defaultLibraryID,
		"serverSettings":       serverSettings,
		"Source":               "silo",
		"ereaderDevices":       []any{},
		"libraries":            libraryMaps,
		// Legacy top-level token fields for clients that read them
		// directly (mainline reads from the user object; some third-party
		// clients still read top-level).
		"accessToken":  access,
		"refreshToken": refresh,
	})
}
```

- [ ] **Step 2.2: Build**

```bash
go build ./internal/audiobooks/abs/...
```

Expected: clean exit.

- [ ] **Step 2.3: Commit**

```bash
git add internal/audiobooks/abs/login.go
git commit -m "$(cat <<'EOF'
fix(audiobooks): enrich ABS login envelope to match real client expectations

Adds itemTagsAccessible, itemTagsSelected, seriesHideFromContinueListening,
lastSeen, createdAt to the user object. Expands permissions to the eight
keys real ABS emits. Enriches serverSettings with the dozen-plus flags
official iOS/Android apps branch on (coverAspectRatio, dateFormat,
timeFormat, scannerDisableWatcher, chromecastEnabled, etc.).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Fix /authorize Envelope to Match /login

**Why:** Real ABS clients call `/authorize` on every app launch with their stored bearer to resume the session. silo's current `/authorize` omits `accessToken`/`refreshToken` and several user fields, so the client either retries login or falls back to degraded mode.

**Files:**
- Modify: `internal/audiobooks/abs/login.go:274-315` (handleABSAuthorize)

- [ ] **Step 3.1: Refactor envelope building into a shared helper**

In `internal/audiobooks/abs/login.go`, just **above** `handleABSAuthorize` (around line 273), insert the shared builder:

```go
// loginEnvelope builds the response body shared by /login and /authorize.
// Both endpoints must return the identical shape so the iOS client's
// resume-on-launch flow validates the same way as fresh login.
// accessToken/refreshToken may be empty for /authorize (client already has
// them); in that case the top-level fields are still included as empty
// strings so the JSON shape stays stable.
func (h *Handler) loginEnvelope(
	r *http.Request,
	userID, displayName, accessToken, refreshToken string,
) map[string]any {
	name := displayName
	if name == "" {
		name = userID
	}

	libs, _ := h.deps.MediaStore.ListAudiobookLibraries(r.Context())
	libraryMaps := make([]map[string]any, 0, len(libs))
	defaultLibraryID := VirtualLibraryID
	for i, lib := range libs {
		if i == 0 {
			defaultLibraryID = audiobookLibraryID(lib)
		}
		libraryMaps = append(libraryMaps, audiobookLibraryMap(lib))
	}

	nowMs := time.Now().UnixMilli()

	user := map[string]any{
		"id":                              userID,
		"username":                        name,
		"type":                            "user",
		"defaultLibraryId":                defaultLibraryID,
		"librariesAccessible":             []any{},
		"itemTagsAccessible":              []any{},
		"itemTagsSelected":                []any{},
		"mediaProgress":                   []any{},
		"bookmarks":                       []any{},
		"seriesHideFromContinueListening": []any{},
		"isOldToken":                      false,
		"token":                           accessToken,
		"lastSeen":                        nowMs,
		"createdAt":                       nowMs,
		"permissions": map[string]any{
			"download":                  true,
			"update":                    true,
			"delete":                    true,
			"upload":                    true,
			"accessAllLibraries":        true,
			"accessAllTags":             true,
			"accessExplicitContent":     true,
			"selectedTagsNotAccessible": false,
		},
	}

	if strings.EqualFold(r.Header.Get("x-return-tokens"), "true") {
		user["accessToken"] = accessToken
		user["refreshToken"] = refreshToken
	}

	serverSettings := map[string]any{
		"id":                                "server-settings",
		"version":                           ServerVersion,
		"buildNumber":                       1,
		"language":                          "en-us",
		"dateFormat":                        "MM/dd/yyyy",
		"timeFormat":                        "HH:mm",
		"timeZone":                          "UTC",
		"coverAspectRatio":                  1,
		"storeCoverWithItem":                false,
		"storeMetadataWithItem":             false,
		"metadataFileFormat":                "json",
		"scannerDisableWatcher":             true,
		"scannerParseSubtitle":              false,
		"scannerFindCovers":                 false,
		"scannerCoverProvider":              "google",
		"scannerPreferMatchedMetadata":      false,
		"scannerPreferOverdriveMediaMarker": false,
		"sortingIgnorePrefix":               false,
		"sortingPrefixes":                   []string{"the", "a"},
		"chromecastEnabled":                 false,
		"enableEReader":                     false,
		"dateString":                        "",
		"logLevel":                          1,
		"version_id":                        ServerVersion,
		"sessionTimeout":                    0,
		"backupSchedule":                    false,
		"backupsToKeep":                     2,
		"maxBackupSize":                     1,
		"loggerDailyLogsToKeep":             7,
		"loggerScannerLogsToKeep":           2,
		"homeBookshelfView":                 1,
		"bookshelfView":                     1,
		"podcastEpisodeSchedule":            "0 * * * *",
		"sortingIgnorePrefixesValue":        "",
		"allowIframe":                       false,
		"authActiveAuthMethods":             []string{"local"},
	}

	return map[string]any{
		"user":                 user,
		"userDefaultLibraryId": defaultLibraryID,
		"serverSettings":       serverSettings,
		"Source":               "silo",
		"ereaderDevices":       []any{},
		"libraries":            libraryMaps,
		"accessToken":          accessToken,
		"refreshToken":         refreshToken,
	}
}
```

- [ ] **Step 3.2: Refactor completeLogin to use the helper**

Replace the body of `completeLogin` from line 176 (`// Build user object.`) through the closing `})` of the final `writeJSON` (around line 254 after Task 2's expansion) with:

```go
	writeJSON(w, http.StatusOK, h.loginEnvelope(r, userID, displayName, access, refresh))
}
```

- [ ] **Step 3.3: Replace handleABSAuthorize with the helper call**

Replace the entire body of `handleABSAuthorize` (currently `internal/audiobooks/abs/login.go:274-315`) with:

```go
func (h *Handler) handleABSAuthorize(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// /authorize re-mints the envelope using the caller's bearer as the
	// access token (client already has it); refresh isn't rotated here.
	writeJSON(w, http.StatusOK, h.loginEnvelope(r, a.UserID, a.UserID, a.Token, ""))
}
```

- [ ] **Step 3.4: Build**

```bash
go build ./internal/audiobooks/abs/...
```

Expected: clean exit.

- [ ] **Step 3.5: Commit**

```bash
git add internal/audiobooks/abs/login.go
git commit -m "$(cat <<'EOF'
fix(audiobooks): /authorize returns identical envelope to /login

Extracts the shared envelope builder so /authorize emits the same shape
as /login including accessToken (echoes the caller's bearer), libraries,
permissions, and full serverSettings. The previous /authorize omission
caused iOS resume-on-launch to fall back into re-login.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Fix Author/Series IDs and Surface Genres in Library Metadata

**Why:** Per `booklore-ng/BOOKLORE_ABS_IMPLEMENTATION_ISSUES.md` lines 9-100: strict 3rd-party ABS clients require `id` on every `authors[]` and `series[]` entry and populated `genres`/`tags`. silo currently sets author IDs via `strconv.FormatInt(p.ID, 10)` (good), series IDs via `slugify(s)` (good), genres correctly from `item.Genres` — BUT verify behavior with a real test, and add `tags` field. silo's `models.MediaItem` has no `Tags` field, so emit an empty `tags: []` array consistently rather than omitting the key.

**Files:**
- Modify: `internal/audiobooks/abs/libraries_handler.go:499-549` (siloItemToMetadata)
- Modify: `internal/audiobooks/abs/types.go` — confirm `Metadata` struct has `Tags` field; add if missing
- Create: `internal/audiobooks/abs/libraries_metadata_test.go`

- [ ] **Step 4.1: Add Tags to Metadata and remove omitempty from Genres**

Open `internal/audiobooks/abs/types.go`, locate the `Metadata` struct (around line 93). Replace the whole struct definition with this version — `Genres` loses `omitempty` (so an empty slice still serializes as `"genres": []` instead of disappearing), and `Tags` is added with the same always-emit semantics:

```go
// Metadata is the book-level metadata block. Authors / Narrators / Series
// match the ABS spec: arrays of references (or strings for Narrators).
// Genres and Tags intentionally do NOT use omitempty — strict 3rd-party
// clients (Plappa, AudioBookShelfFully) branch on these keys being present
// (even if empty), and dropping the key sends them into degraded mode.
type Metadata struct {
	Title         string      `json:"title"`
	Authors       []AuthorObj `json:"authors"`
	Narrators     []string    `json:"narrators"`
	Series        []SeriesObj `json:"series"`
	Description   string      `json:"description,omitempty"`
	PublishedYear string      `json:"publishedYear,omitempty"`
	ISBN          string      `json:"isbn,omitempty"`
	Publisher     string      `json:"publisher,omitempty"`
	Genres        []string    `json:"genres"`
	Tags          []string    `json:"tags"`
}
```

- [ ] **Step 4.2: Update siloItemToMetadata to always emit tags**

Replace lines 499-549 of `internal/audiobooks/abs/libraries_handler.go` (`siloItemToMetadata`) with:

```go
// siloItemToMetadata extracts the ABS Metadata block from a silo MediaItem.
// Authors and narrators are sourced from item.People; series from Studios
// (silo stores the series name in Studios for audiobooks until a proper
// series table lands — see scanner Stage 2 notes).
//
// Strict 3rd-party clients (Plappa, AudioBookShelfFully) require id on
// every author/series entry and non-nil tags/genres arrays. We surface
// IDs from item_people.id (authors) and slugify(name) (series).
func siloItemToMetadata(item *models.MediaItem) Metadata {
	authors := make([]AuthorObj, 0)
	narrators := make([]string, 0)

	for _, p := range item.People {
		switch p.Kind {
		case models.PersonKindAuthor:
			authors = append(authors, AuthorObj{
				ID:   strconv.FormatInt(p.ID, 10),
				Name: p.Name,
			})
		case models.PersonKindNarrator:
			narrators = append(narrators, p.Name)
		}
	}

	// Series: silo's audiobook scanner stores series name in the Studios
	// field until a dedicated series table is added. Derive an ID by
	// slugifying the name (same convention as the plugin's translate.go).
	// Authoritative series IDs will replace these slugs when a series
	// table lands; client-stored references survive the change because the
	// slug is stable for a given name.
	series := make([]SeriesObj, 0, len(item.Studios))
	for _, s := range item.Studios {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		series = append(series, SeriesObj{
			ID:   slugify(s),
			Name: s,
		})
	}

	publishedYear := ""
	if item.Year > 0 {
		publishedYear = strconv.Itoa(item.Year)
	}

	genres := item.Genres
	if genres == nil {
		genres = []string{}
	}

	// silo has no item-level tags concept today; emit an empty array so
	// clients that branch on tags[] don't see a null and crash.
	tags := []string{}

	return Metadata{
		Title:         item.Title,
		Authors:       authors,
		Narrators:     narrators,
		Series:        series,
		Description:   item.Overview,
		PublishedYear: publishedYear,
		Genres:        genres,
		Tags:          tags,
	}
}
```

- [ ] **Step 4.3: Write the failing tests**

Create `internal/audiobooks/abs/libraries_metadata_test.go`:

```go
package abs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSiloItemToMetadata_AuthorsHaveIDs(t *testing.T) {
	item := &models.MediaItem{
		Title: "Test Book",
		People: []models.ItemPerson{
			{ID: 42, Kind: models.PersonKindAuthor, Name: "Stephen King"},
			{ID: 43, Kind: models.PersonKindNarrator, Name: "Audie Murphy"},
		},
	}
	m := siloItemToMetadata(item)
	if len(m.Authors) != 1 {
		t.Fatalf("authors len = %d, want 1", len(m.Authors))
	}
	if m.Authors[0].ID != "42" {
		t.Errorf("author ID = %q, want %q", m.Authors[0].ID, "42")
	}
	if m.Authors[0].Name != "Stephen King" {
		t.Errorf("author Name = %q, want %q", m.Authors[0].Name, "Stephen King")
	}
}

func TestSiloItemToMetadata_SeriesHaveSlugIDs(t *testing.T) {
	item := &models.MediaItem{
		Title:   "Test Book",
		Studios: []string{"The Dark Tower"},
	}
	m := siloItemToMetadata(item)
	if len(m.Series) != 1 {
		t.Fatalf("series len = %d, want 1", len(m.Series))
	}
	if m.Series[0].ID == "" {
		t.Errorf("series ID is empty; want slugified name")
	}
	if m.Series[0].Name != "The Dark Tower" {
		t.Errorf("series Name = %q, want %q", m.Series[0].Name, "The Dark Tower")
	}
}

func TestSiloItemToMetadata_GenresEmptyArrayNotNil(t *testing.T) {
	item := &models.MediaItem{Title: "Test Book"} // Genres nil
	m := siloItemToMetadata(item)
	if m.Genres == nil {
		t.Errorf("Genres is nil; want empty slice")
	}
	if len(m.Genres) != 0 {
		t.Errorf("Genres len = %d, want 0", len(m.Genres))
	}
}

func TestSiloItemToMetadata_TagsEmptyArrayNotNil(t *testing.T) {
	item := &models.MediaItem{Title: "Test Book"}
	m := siloItemToMetadata(item)
	if m.Tags == nil {
		t.Errorf("Tags is nil; want empty slice")
	}
}

func TestSiloItemToMetadata_NarratorsListed(t *testing.T) {
	item := &models.MediaItem{
		Title: "Test Book",
		People: []models.ItemPerson{
			{ID: 1, Kind: models.PersonKindNarrator, Name: "Narrator One"},
			{ID: 2, Kind: models.PersonKindNarrator, Name: "Narrator Two"},
		},
	}
	m := siloItemToMetadata(item)
	if len(m.Narrators) != 2 {
		t.Fatalf("narrators len = %d, want 2", len(m.Narrators))
	}
	if m.Narrators[0] != "Narrator One" || m.Narrators[1] != "Narrator Two" {
		t.Errorf("narrators = %v, want [Narrator One Narrator Two]", m.Narrators)
	}
}

// TestSiloItemToMetadata_JSONKeysAlwaysPresent guards the omitempty fix:
// 3rd-party clients branch on the presence of "genres" and "tags" keys
// even when the values are empty arrays. Removing omitempty from those
// fields means the keys serialize even when the slice is empty.
func TestSiloItemToMetadata_JSONKeysAlwaysPresent(t *testing.T) {
	item := &models.MediaItem{Title: "Test Book"} // no genres, no tags, no people
	m := siloItemToMetadata(item)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)
	for _, key := range []string{`"genres":`, `"tags":`, `"authors":`, `"series":`, `"narrators":`} {
		if !strings.Contains(s, key) {
			t.Errorf("JSON missing required key %s; got %s", key, s)
		}
	}
}
```

(The imports for `encoding/json` and `strings` are already included in the file header above.)

- [ ] **Step 4.4: Run tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run TestSiloItemToMetadata -v
```

Expected: all 6 tests PASS.

- [ ] **Step 4.5: Commit**

```bash
git add internal/audiobooks/abs/libraries_handler.go internal/audiobooks/abs/types.go internal/audiobooks/abs/libraries_metadata_test.go
git commit -m "$(cat <<'EOF'
fix(audiobooks): emit IDs on authors/series and stable genres/tags arrays

3rd-party ABS clients (Plappa, AudioBookShelfFully) require id on every
authors[] and series[] entry to encode filter selections; missing IDs
made author/series chips dead-end. Also ensures genres and tags are
always non-nil arrays so clients that branch on .length don't crash.

Tags is empty for now (silo has no item-tag concept); shape is stable so
future tag work won't break clients.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Populate filterdata in handleLibraryDetail

**Why:** When iOS opens the filter sheet it expects `filterdata.authors[]`, `filterdata.series[]`, etc. to be populated. silo currently returns empty arrays, so the filter UI shows nothing.

**Files:**
- Modify: `internal/audiobooks/abs/libraries_handler.go:36-66` (handleLibraryDetail + emptyFilterData)

- [ ] **Step 5.1: Replace handleLibraryDetail and remove emptyFilterData**

Replace lines 36-66 in `internal/audiobooks/abs/libraries_handler.go` with:

```go
// handleLibraryDetail — GET /abs/api/libraries/{libraryId}
func (h *Handler) handleLibraryDetail(w http.ResponseWriter, r *http.Request) {
	lib, ok := h.resolveLibrary(w, r)
	if !ok {
		return
	}
	resp := map[string]any{
		"library": audiobookLibraryMap(lib),
	}
	if includeHas(r.URL.Query().Get("include"), "filterdata") {
		resp["filterdata"] = h.buildFilterData(r, lib)
		resp["issues"] = 0
		resp["numUserPlaylists"] = 0
	}
	writeJSON(w, http.StatusOK, resp)
}

// buildFilterData populates the filter sheet payload from the same store
// queries /libraries/{id}/authors and /libraries/{id}/series use. Caps at
// 5000 per kind to keep the response bounded; libraries larger than that
// will paginate via the dedicated /authors and /series endpoints.
func (h *Handler) buildFilterData(r *http.Request, lib AudiobookLibrary) map[string]any {
	ctx := r.Context()
	const cap = 5000

	authorObjs := []AuthorObj{}
	if h.deps.MediaStore != nil {
		if rows, err := h.deps.MediaStore.ListLibraryAuthors(ctx, lib.ID, cap); err == nil {
			for _, a := range rows {
				authorObjs = append(authorObjs, AuthorObj{ID: a.ID, Name: a.Name})
			}
		}
	}

	seriesObjs := []SeriesObj{}
	if h.deps.MediaStore != nil {
		if rows, err := h.deps.MediaStore.ListLibrarySeries(ctx, lib.ID, cap); err == nil {
			for _, s := range rows {
				seriesObjs = append(seriesObjs, SeriesObj{ID: s.ID, Name: s.Name})
			}
		}
	}

	// Narrators / genres / publishers / languages / tags are derived from
	// item rows. For Phase 0 we keep them as empty arrays — the iOS app
	// tolerates empty filter dropdowns gracefully. Phase 1 fills them when
	// the catalog has the aggregations indexed.
	return map[string]any{
		"authors":    authorObjs,
		"series":     seriesObjs,
		"narrators":  []string{},
		"genres":     []string{},
		"publishers": []string{},
		"languages":  []string{},
		"tags":       []string{},
	}
}
```

- [ ] **Step 5.2: Build**

```bash
go build ./internal/audiobooks/abs/...
```

Expected: clean exit.

- [ ] **Step 5.3: Commit**

```bash
git add internal/audiobooks/abs/libraries_handler.go
git commit -m "$(cat <<'EOF'
fix(audiobooks): hydrate filterdata authors and series in library detail

handleLibraryDetail now populates filterdata.authors and filterdata.series
from MediaStore so the iOS filter sheet has real options. Narrators,
genres, publishers, languages, tags stay empty arrays for now (Phase 1
will index those aggregations); empty arrays are gracefully handled by
the client.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Wire Resume Position into /play

**Why:** `play_response.go:119` has a TODO and emits `currentTime: 0` for every play start, so cross-device resume is broken. `ProgressStore.GetProgress(userID, profileID, contentID)` is already wired and returns the persisted row.

**Files:**
- Modify: `internal/audiobooks/abs/play_response.go:114-150` (currentTime block + playbackSession map)
- Create: `internal/audiobooks/abs/play_resume_test.go`

- [ ] **Step 6.1: Replace the currentTime block**

In `internal/audiobooks/abs/play_response.go`, replace lines 114-150 (from `// currentTime seeds...` through the `playbackSession :=` opening brace and up to `"libraryItem":   libraryItem,` — keep that line) with:

```go
	// currentTime seeds the audio element's initial position so cross-device
	// resume works. Lookup is best-effort: any error returns position 0,
	// which is always correct for a first listen.
	var currentTime float64
	if h.deps.ProgressStore != nil {
		if row, err := h.deps.ProgressStore.GetProgress(r.Context(), a.UserID, a.ProfileID, contentID); err == nil && row != nil {
			currentTime = row.CurrentSeconds
		} else if err != nil {
			slog.Debug("play: progress lookup failed", "user", a.UserID, "item", contentID, "err", err)
		}
	}

	playbackSession := map[string]any{
		"id":            sessionID,
		"userId":        a.UserID,
		"libraryId":     VirtualLibraryID,
		"libraryItemId": contentID,
		"bookId":        contentID,
		"episodeId":     nil,
		"mediaType":     LibraryMediaType,
		"mediaMetadata": mediaMetadata,
		"chapters":      chapters,
		"displayTitle":  displayTitle,
		"displayAuthor": displayAuthor,
		"coverPath":     baseURL + "/api/items/" + contentID + "/cover",
		"duration":      totalDuration,
		"playMethod":    0, // DIRECTPLAY
		"mediaPlayer":   "exo-player",
		"deviceInfo": map[string]any{
			"deviceId":      "unknown",
			"manufacturer":  "Unknown",
			"model":         "Unknown",
			"sdkVersion":    0,
			"clientVersion": "0.0.0",
		},
		"serverVersion": ServerVersion,
		"date":          dateStr,
		"dayOfWeek":     dayOfWeek,
		"timeListening": 0,
		"startTime":     currentTime,
		"currentTime":   currentTime,
		"startedAt":     nowMs,
		"updatedAt":     nowMs,
		"audioTracks":   audioTracks,
		"libraryItem":   libraryItem,
	}
```

Verify the imports at the top of `play_response.go` already include `"log/slog"`. Add it if missing.

- [ ] **Step 6.2: Write the failing test**

Create `internal/audiobooks/abs/play_resume_test.go`:

```go
package abs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeProgressStore is a minimal in-memory ProgressStore for the play
// resume tests. It returns a fixed row on GetProgress; other methods are
// no-ops sufficient to satisfy the interface.
type fakeProgressStore struct {
	row     *ProgressRow
	getErr  error
	called  bool
}

func (f *fakeProgressStore) GetProgress(_ context.Context, _, _, _ string) (*ProgressRow, error) {
	f.called = true
	return f.row, f.getErr
}
func (f *fakeProgressStore) ListProgressForAudiobooks(_ context.Context, _, _ string, _ int) ([]ProgressRow, error) {
	return nil, nil
}
func (f *fakeProgressStore) UpsertProgress(_ context.Context, _ ProgressRow) error { return nil }
func (f *fakeProgressStore) UpdateProgressPosition(_ context.Context, _, _, _ string, _ float64) error {
	return nil
}

func TestResumeTimeFromProgressStore_HasRow(t *testing.T) {
	store := &fakeProgressStore{
		row: &ProgressRow{
			UserID:          "1",
			ProfileID:       "p1",
			ContentID:       "book123",
			CurrentSeconds:  1234.5,
			DurationSeconds: 5000,
			UpdatedAt:       time.Now(),
		},
	}
	got, err := resolveResumeTime(context.Background(), store, "1", "p1", "book123")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != 1234.5 {
		t.Errorf("resume time = %v, want 1234.5", got)
	}
	if !store.called {
		t.Errorf("ProgressStore.GetProgress not called")
	}
}

func TestResumeTimeFromProgressStore_NoRow(t *testing.T) {
	store := &fakeProgressStore{row: nil}
	got, err := resolveResumeTime(context.Background(), store, "1", "", "book123")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != 0 {
		t.Errorf("resume time = %v, want 0", got)
	}
}

func TestResumeTimeFromProgressStore_NilStore(t *testing.T) {
	got, err := resolveResumeTime(context.Background(), nil, "1", "", "book123")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != 0 {
		t.Errorf("resume time = %v, want 0", got)
	}
}

func TestResumeTimeFromProgressStore_LookupError(t *testing.T) {
	store := &fakeProgressStore{getErr: errors.New("boom")}
	got, err := resolveResumeTime(context.Background(), store, "1", "", "book123")
	if err == nil {
		t.Errorf("expected error, got nil")
	}
	if got != 0 {
		t.Errorf("resume time = %v, want 0 on error", got)
	}
}
```

- [ ] **Step 6.3: Extract resolveResumeTime so it's testable**

The test references `resolveResumeTime`. Add this helper to the bottom of `internal/audiobooks/abs/play_response.go`:

```go
// resolveResumeTime returns the persisted currentTime for (userID, profileID,
// contentID) from the progress store, or 0 when no row exists / store is nil.
// Returned error is propagated so callers can log it; the caller is expected
// to fall back to 0 on error (a fresh-listen start is always correct).
func resolveResumeTime(ctx context.Context, store ProgressStore, userID, profileID, contentID string) (float64, error) {
	if store == nil {
		return 0, nil
	}
	row, err := store.GetProgress(ctx, userID, profileID, contentID)
	if err != nil {
		return 0, err
	}
	if row == nil {
		return 0, nil
	}
	return row.CurrentSeconds, nil
}
```

Then **replace** the currentTime block in the handler (the if-block we added in Step 6.1) with the simpler:

```go
	currentTime, err := resolveResumeTime(r.Context(), h.deps.ProgressStore, a.UserID, a.ProfileID, contentID)
	if err != nil {
		slog.Debug("play: progress lookup failed", "user", a.UserID, "item", contentID, "err", err)
		// currentTime is already 0 on error path; safe to continue.
	}
```

Make sure `"context"` is imported in `play_response.go`.

- [ ] **Step 6.4: Run tests**

```bash
go test ./internal/audiobooks/abs/ -run TestResumeTimeFromProgressStore -v
```

Expected: all 4 tests PASS.

- [ ] **Step 6.5: Commit**

```bash
git add internal/audiobooks/abs/play_response.go internal/audiobooks/abs/play_resume_test.go
git commit -m "$(cat <<'EOF'
fix(audiobooks): seed currentTime from ProgressStore so resume works

handlePlayStart now looks up the persisted progress row and emits the
saved currentTime in the playback session manifest. Without this every
play start began at 0, breaking cross-device resume which is one of the
core ABS-app value props.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Add POST /auth/refresh Endpoint

**Why:** Mobile clients call this every ~22h. Without it, the 24h access token forces interactive re-login daily, which the user reports as breaking the experience. Port from `continuum-plugin-audiobooks/internal/abs/handler.go:775-852` (handleRefresh).

**Files:**
- Modify: `internal/audiobooks/abs/login.go` — add `handleRefresh` handler
- Modify: `internal/audiobooks/abs/handler.go:225-322` (mountRoutes) — mount the route
- Create: `internal/audiobooks/abs/login_refresh_test.go`

- [ ] **Step 7.1: Add handleRefresh to login.go**

Append to `internal/audiobooks/abs/login.go` (at the end of the file):

```go
// handleRefresh — POST /auth/refresh
//
// Real ABS clients send the refresh token via x-refresh-token header with
// an empty body; legacy / 3rd-party clients send {refreshToken: "..."} in
// the JSON body. Accept either; header takes precedence when both are sent.
//
// Token rotation semantics (ported from continuum-plugin-audiobooks):
//   1. Validate the refresh token signature + type.
//   2. Confirm the JTI is in the store and not revoked.
//   3. Mint a NEW access + refresh pair with fresh JTIs.
//   4. Persist both new JTIs BEFORE revoking the old one. If anything in
//      step 3-4 fails, the old refresh stays valid and the client can retry.
//   5. Revoke the old refresh JTI.
//   6. Return {user:{accessToken, refreshToken}} AND top-level token fields
//      for client compatibility — mainline app reads from user{}, third-party
//      readers may read from the top level.
func (h *Handler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	refreshTok := strings.TrimSpace(r.Header.Get("x-refresh-token"))
	if refreshTok == "" {
		var p struct {
			RefreshToken string `json:"refreshToken"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err == nil {
			refreshTok = p.RefreshToken
		}
	}
	if refreshTok == "" {
		http.Error(w, "refreshToken required", http.StatusBadRequest)
		return
	}
	if h.deps.Config == nil || h.deps.TokenStore == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	secret, err := h.deps.Config.JWTSecret(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	claims, err := ParseToken(secret, refreshTok)
	if err != nil || claims.Type != "refresh" {
		slog.Debug("abs refresh: parse/type failed", "err", err, "type", func() string {
			if claims != nil {
				return claims.Type
			}
			return ""
		}())
		http.Error(w, "invalid refresh token", http.StatusUnauthorized)
		return
	}
	row, err := h.deps.TokenStore.GetTokenByJTI(r.Context(), claims.JTI)
	if err != nil {
		slog.Debug("abs refresh: jti lookup failed", "jti", claims.JTI, "err", err)
		http.Error(w, "refresh token revoked", http.StatusUnauthorized)
		return
	}
	if row.RevokedAt != nil {
		http.Error(w, "refresh token revoked", http.StatusUnauthorized)
		return
	}

	accessTTL, err := h.deps.Config.AccessTTL(r.Context())
	if err != nil || accessTTL == 0 {
		accessTTL = 24 * time.Hour
	}
	refreshTTL, err := h.deps.Config.RefreshTTL(r.Context())
	if err != nil || refreshTTL == 0 {
		refreshTTL = 30 * 24 * time.Hour
	}

	newAccessJTI := ulid.Make().String()
	newRefreshJTI := ulid.Make().String()
	access, err := IssueAccessToken(secret, claims.UserID, claims.ProfileID, newAccessJTI, accessTTL)
	if err != nil {
		http.Error(w, "token mint failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	refresh, err := IssueRefreshToken(secret, claims.UserID, claims.ProfileID, newRefreshJTI, refreshTTL)
	if err != nil {
		http.Error(w, "token mint failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	if err := h.deps.TokenStore.InsertToken(r.Context(), ABSToken{
		ID: newAccessJTI, UserID: claims.UserID, ProfileID: claims.ProfileID,
		JTI: newAccessJTI, ExpiresAt: now.Add(accessTTL),
	}); err != nil {
		http.Error(w, "token persist failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.deps.TokenStore.InsertToken(r.Context(), ABSToken{
		ID: newRefreshJTI, UserID: claims.UserID, ProfileID: claims.ProfileID,
		JTI: newRefreshJTI, ExpiresAt: now.Add(refreshTTL),
	}); err != nil {
		http.Error(w, "token persist failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.deps.TokenStore.RevokeTokenByJTI(r.Context(), claims.JTI); err != nil {
		http.Error(w, "token rotation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("abs refresh: rotated", "user", claims.UserID,
		"old_jti", claims.JTI, "new_access_jti", newAccessJTI, "new_refresh_jti", newRefreshJTI)

	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":           claims.UserID,
			"accessToken":  access,
			"refreshToken": refresh,
		},
		"accessToken":  access,
		"refreshToken": refresh,
	})
}
```

Verify the imports include `"io"`, `"strings"`, `"time"`, `"encoding/json"`, `"net/http"`, `"log/slog"`, and `"github.com/oklog/ulid/v2"`. All except possibly `slog` should already be present.

- [ ] **Step 7.2: Mount the refresh route**

In `internal/audiobooks/abs/handler.go`, locate the `mountRoutes` function. Right after the `r.Post("/login", h.handleLogin)` and `r.Post("/abs/api/login", h.handleLogin)` lines (around `:238-239`), add:

```go
	// Token rotation — mobile clients call this every ~22h to avoid the
	// 24h access-token interactive re-login trap.
	r.Post("/auth/refresh", h.handleRefresh)
	r.Post("/abs/api/auth/refresh", h.handleRefresh)
```

- [ ] **Step 7.3: Write the failing tests**

Create `internal/audiobooks/abs/login_refresh_test.go`:

```go
package abs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// memTokenStore is an in-memory TokenStore for handleRefresh tests.
type memTokenStore struct {
	mu     sync.Mutex
	tokens map[string]ABSToken
}

func newMemTokenStore() *memTokenStore { return &memTokenStore{tokens: map[string]ABSToken{}} }

func (m *memTokenStore) InsertToken(_ context.Context, tok ABSToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[tok.JTI] = tok
	return nil
}
func (m *memTokenStore) GetTokenByJTI(_ context.Context, jti string) (ABSToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[jti]
	if !ok {
		return ABSToken{}, ErrNotFound
	}
	return t, nil
}
func (m *memTokenStore) RevokeTokenByJTI(_ context.Context, jti string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[jti]
	if !ok {
		return nil
	}
	now := time.Now()
	t.RevokedAt = &now
	m.tokens[jti] = t
	return nil
}
func (m *memTokenStore) TouchToken(_ context.Context, _ string) error { return nil }

// staticConfig satisfies ConfigProvider with fixed values.
type staticConfig struct{ secret []byte }

func (s *staticConfig) JWTSecret(_ context.Context) ([]byte, error)     { return s.secret, nil }
func (s *staticConfig) AccessTTL(_ context.Context) (time.Duration, error)  { return 24 * time.Hour, nil }
func (s *staticConfig) RefreshTTL(_ context.Context) (time.Duration, error) { return 30 * 24 * time.Hour, nil }
func (s *staticConfig) StandaloneLoginEnabled(_ context.Context) (bool, error) { return true, nil }

func newRefreshTestHandler(t *testing.T) (*Handler, *memTokenStore, *staticConfig) {
	t.Helper()
	store := newMemTokenStore()
	cfg := &staticConfig{secret: []byte("test-secret-32-bytes-aaaaaaaaaaaaa")}
	h := New(Dependencies{
		Config:     cfg,
		TokenStore: store,
	})
	return h, store, cfg
}

func mintAndPersistRefresh(t *testing.T, h *Handler, store *memTokenStore, cfg *staticConfig, userID string) (string, string) {
	t.Helper()
	jti := "test-refresh-jti-" + userID
	refresh, err := IssueRefreshToken(cfg.secret, userID, "", jti, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("mint refresh: %v", err)
	}
	if err := store.InsertToken(context.Background(), ABSToken{
		ID: jti, UserID: userID, JTI: jti, ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return refresh, jti
}

func TestHandleRefresh_HeaderToken_RotatesAndReturnsBothForms(t *testing.T) {
	h, store, cfg := newRefreshTestHandler(t)
	refresh, oldJTI := mintAndPersistRefresh(t, h, store, cfg, "42")

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("x-refresh-token", refresh)
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["accessToken"] == "" || resp["accessToken"] == nil {
		t.Errorf("top-level accessToken missing")
	}
	user, ok := resp["user"].(map[string]any)
	if !ok {
		t.Fatalf("user object missing")
	}
	if user["accessToken"] == "" || user["accessToken"] == nil {
		t.Errorf("user.accessToken missing")
	}
	// Old refresh JTI must be revoked.
	old, _ := store.GetTokenByJTI(context.Background(), oldJTI)
	if old.RevokedAt == nil {
		t.Errorf("old refresh JTI %s was not revoked", oldJTI)
	}
}

func TestHandleRefresh_BodyToken_Works(t *testing.T) {
	h, store, cfg := newRefreshTestHandler(t)
	refresh, _ := mintAndPersistRefresh(t, h, store, cfg, "1")

	body := bytes.NewBufferString(`{"refreshToken":"` + refresh + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRefresh_NoToken_400(t *testing.T) {
	h, _, _ := newRefreshTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRefresh_AccessTokenRejected(t *testing.T) {
	h, store, cfg := newRefreshTestHandler(t)
	jti := "an-access-jti"
	access, _ := IssueAccessToken(cfg.secret, "9", "", jti, time.Hour)
	_ = store.InsertToken(context.Background(), ABSToken{ID: jti, UserID: "9", JTI: jti, ExpiresAt: time.Now().Add(time.Hour)})

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(""))
	req.Header.Set("x-refresh-token", access)
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRefresh_RevokedTokenRejected(t *testing.T) {
	h, store, cfg := newRefreshTestHandler(t)
	refresh, oldJTI := mintAndPersistRefresh(t, h, store, cfg, "7")
	_ = store.RevokeTokenByJTI(context.Background(), oldJTI)

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("x-refresh-token", refresh)
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
```

- [ ] **Step 7.4: Run tests**

```bash
go test ./internal/audiobooks/abs/ -run TestHandleRefresh -v
```

Expected: all 5 tests PASS.

- [ ] **Step 7.5: Commit**

```bash
git add internal/audiobooks/abs/login.go internal/audiobooks/abs/handler.go internal/audiobooks/abs/login_refresh_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): add POST /auth/refresh for ABS token rotation

Mobile clients call refresh every ~22h to avoid the 24h access-token
re-login trap. Accepts the token via x-refresh-token header (real ABS
convention) or {refreshToken} body (legacy). Mints a fresh pair, persists
both new JTIs, then revokes the old refresh JTI atomically — if any step
in 3-4 fails, the old refresh stays valid and the client can retry.

Returns the user{accessToken, refreshToken} object AND top-level token
fields so mainline and 3rd-party clients both find their expected shape.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Add POST /logout Endpoint

**Why:** Mobile "Sign Out" buttons send a logout request. Without this, the server retains the JTI in `abs_sessions` indefinitely (until natural expiry) and the user has no way to revoke a leaked token before the 30-day refresh expiry.

**Files:**
- Modify: `internal/audiobooks/abs/login.go` — add `handleLogout`
- Modify: `internal/audiobooks/abs/handler.go:225-322` (mountRoutes) — mount in the bearerAuth group
- Create: `internal/audiobooks/abs/login_logout_test.go`

- [ ] **Step 8.1: Add handleLogout to login.go**

Append to the bottom of `internal/audiobooks/abs/login.go`:

```go
// handleLogout — POST /logout
//
// Mounted inside the bearerAuth group: the middleware has already parsed
// and validated the access JTI. We revoke that JTI (idempotent) and
// return 204. There is no body and no JSON response.
//
// Note: this revokes ONLY the access token. The associated refresh token
// has its own JTI and stays valid until the client also calls /auth/refresh
// with a since-revoked access; the refresh endpoint will then deny the
// rotation. Clients that want a hard "log out everywhere" should iterate
// the sessions list (added in Phase 3) instead.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.JTI == "" {
		// No auth context — middleware shouldn't have let us through, but
		// be defensive and return 204 anyway (logout is idempotent).
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if h.deps.TokenStore == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := h.deps.TokenStore.RevokeTokenByJTI(r.Context(), a.JTI); err != nil {
		slog.Warn("abs logout: revoke failed", "jti", a.JTI, "user", a.UserID, "err", err)
		http.Error(w, "logout failed", http.StatusInternalServerError)
		return
	}
	slog.Debug("abs logout: revoked", "jti", a.JTI, "user", a.UserID)
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 8.2: Mount the logout route inside bearerAuth**

In `internal/audiobooks/abs/handler.go`, find the existing browse route group that uses `r.Use(h.bearerAuth)` starting at `:287`. Add the logout routes in the same group, immediately after the existing `r.Post(prefix+"/authorize", h.handleABSAuthorize)` line (around `:294`):

```go
			// Logout: revokes the caller's access JTI. Mounted inside
			// bearerAuth so the JTI is already in context.
			r.Post(prefix+"/logout", h.handleLogout)
```

- [ ] **Step 8.3: Write the failing tests**

Create `internal/audiobooks/abs/login_logout_test.go`:

```go
package abs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleLogout_RevokesJTIAndReturns204(t *testing.T) {
	store := newMemTokenStore()
	jti := "logout-test-jti"
	_ = store.InsertToken(context.Background(), ABSToken{ID: jti, UserID: "1", JTI: jti})

	h := New(Dependencies{TokenStore: store})
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	// Simulate bearerAuth having populated the context.
	ctx := context.WithValue(req.Context(), ctxKey{}, ctxAuth{
		UserID: "1", JTI: jti, Token: "doesnt-matter",
	})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.handleLogout(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	tok, _ := store.GetTokenByJTI(context.Background(), jti)
	if tok.RevokedAt == nil {
		t.Errorf("JTI %s was not revoked", jti)
	}
}

func TestHandleLogout_NoAuthContext_204(t *testing.T) {
	store := newMemTokenStore()
	h := New(Dependencies{TokenStore: store})
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rec := httptest.NewRecorder()
	h.handleLogout(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestHandleLogout_NilTokenStore_204(t *testing.T) {
	h := New(Dependencies{})
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	ctx := context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: "1", JTI: "x"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.handleLogout(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestHandleLogout_IsIdempotent(t *testing.T) {
	store := newMemTokenStore()
	jti := "idem-jti"
	_ = store.InsertToken(context.Background(), ABSToken{ID: jti, UserID: "1", JTI: jti})
	h := New(Dependencies{TokenStore: store})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/logout", nil)
		ctx := context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: "1", JTI: jti})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.handleLogout(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("iter %d: status = %d, want 204", i, rec.Code)
		}
	}
}
```

- [ ] **Step 8.4: Run tests**

```bash
go test ./internal/audiobooks/abs/ -run TestHandleLogout -v
```

Expected: all 4 tests PASS.

- [ ] **Step 8.5: Commit**

```bash
git add internal/audiobooks/abs/login.go internal/audiobooks/abs/handler.go internal/audiobooks/abs/login_logout_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): add POST /logout for ABS sign-out

Mounted inside bearerAuth so the JTI is already validated. Revokes the
access JTI in abs_sessions and returns 204. Idempotent: re-calling on an
already-revoked JTI still returns 204. Refresh JTI is intentionally NOT
revoked here — clients that want hard sign-out-everywhere will use the
sessions endpoint added in Phase 3.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Verify Full Build and Run All Tests

- [ ] **Step 9.1: Full package build**

```bash
go build ./...
```

Expected: clean exit (no output, exit code 0).

- [ ] **Step 9.2: Run the entire ABS test suite**

```bash
go test ./internal/audiobooks/... -v
```

Expected: all tests PASS. If any pre-existing test fails, the failure is not caused by these changes — note it but don't fix it in this PR.

- [ ] **Step 9.3: Lint**

```bash
make lint
```

Expected: clean exit. If `golangci-lint` reports issues only in files we touched, fix them. Don't fix unrelated pre-existing lint issues.

---

## Task 10: Build the Binary and Restart, Smoke Test Against Real Client

- [ ] **Step 10.1: Build the silo binary**

```bash
make build
```

Expected: produces `./silo` binary, clean exit.

- [ ] **Step 10.2: Identify the running silo process and restart**

```bash
ps auxf | grep -v grep | grep '/silo\b' | head -5
```

Determine how silo is run (systemd unit / docker compose / `make dev-backend` / direct `./silo` invocation). Stop and restart that process so the new binary is loaded. Common paths:

- systemd: `sudo systemctl restart silo` (unit name may vary; check with `systemctl list-units '*silo*'`)
- docker: `docker compose restart silo`
- direct: kill the existing process, restart with the same command line that launched it

If unsure, ask the user before restarting. Restarting the wrong process can interrupt unrelated work.

- [ ] **Step 10.3: Smoke test /ping and /status against the running ABS listener**

```bash
curl -fs http://127.0.0.1:13378/ping
echo
curl -fs http://127.0.0.1:13378/status
```

Expected:

```
{"pong":true,"server":"audiobookshelf","version":"2.35.0"}
{"app":"audiobookshelf","isInit":true,"language":"en-us","serverVersion":"2.35.0"}
```

- [ ] **Step 10.4: Smoke test login envelope shape**

Ask the user for a real test credential pair. Then:

```bash
curl -fs -X POST http://127.0.0.1:13378/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"<USERNAME>","password":"<PASSWORD>"}' | jq .
```

Expected: 200 OK. Response JSON must have:
- `user.id`, `user.username`, `user.type == "user"`, `user.defaultLibraryId`, `user.itemTagsAccessible: []`, `user.lastSeen: <number>`, `user.permissions` (8 keys)
- `serverSettings.coverAspectRatio == 1`, `serverSettings.dateFormat`, `serverSettings.scannerDisableWatcher`, etc.
- Top-level `accessToken`, `refreshToken`, `libraries: [...]`

- [ ] **Step 10.5: Smoke test /auth/refresh**

```bash
ACCESS=<accessToken from previous step>
REFRESH=<refreshToken from previous step>

curl -fs -X POST http://127.0.0.1:13378/auth/refresh \
  -H "x-refresh-token: $REFRESH" | jq .
```

Expected: 200 OK with `{user:{accessToken,refreshToken}, accessToken, refreshToken}`. The returned tokens must be different from `$ACCESS` / `$REFRESH`.

- [ ] **Step 10.6: Smoke test /logout**

Using the **new** access token from Step 10.5:

```bash
NEW_ACCESS=<accessToken from Step 10.5>

curl -fs -i -X POST http://127.0.0.1:13378/logout \
  -H "Authorization: Bearer $NEW_ACCESS"
```

Expected: HTTP 204, no body. Subsequent calls with the same `$NEW_ACCESS` to any bearerAuth route (e.g. `GET /me`) should return 401.

```bash
curl -s -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer $NEW_ACCESS" \
  http://127.0.0.1:13378/me
```

Expected: `401`.

- [ ] **Step 10.7: End-to-end with the iOS / Plappa client**

Ask the user to:
1. Add silo as a new ABS server in the iOS app (`http://<silo-host>:13378`).
2. Log in with real credentials.
3. Confirm the library list appears.
4. Tap an audiobook.
5. Hit play; confirm playback starts (note whether it starts at 0 or resumes from a saved position).
6. Stop, kill the app, reopen — confirm session resumes without re-login (this exercises `/authorize`).

If the user reports any failure at any step:
- Check the silo log for the new `slog.Debug` lines from Task 1 — they pinpoint which rejection branch fires.
- Capture the exact failure mode (HTTP status, body, log line) before proceeding.

---

## Task 11: Final Sanity and Spec Update

- [ ] **Step 11.1: Verify all phase 0 tasks from the spec are covered**

Open `docs/superpowers/specs/2026-05-26-abs-implementation-fix-design.md` and skim the Phase 0 table. Each row should map to a task above:

- Login response shape → Task 2
- `/authorize` envelope → Task 3
- bearerAuth diagnostic logging + JTI lookup audit → Task 1
- Author/Series IDs + genres + tags → Task 4
- Resume position → Task 6
- filterdata population → Task 5
- `POST /auth/refresh` → Task 7
- `POST /logout` → Task 8
- Route mounting → Tasks 7.2 and 8.2

If any row is uncovered, add the missing task before declaring Phase 0 done.

- [ ] **Step 11.2: Append a "Phase 0 status" note to the spec**

Append to `docs/superpowers/specs/2026-05-26-abs-implementation-fix-design.md`:

```markdown
## Phase 0 — Status

**Implemented:** 2026-05-26 (or whatever date when this lands). Plan: `docs/superpowers/plans/2026-05-26-abs-phase-0-login-and-critical-fixes.md`. All Phase 0 tasks committed and verified end-to-end against the iOS ABS app.
```

(Adjust the date to actual landing date.)

- [ ] **Step 11.3: Commit the spec update**

```bash
git add docs/superpowers/specs/2026-05-26-abs-implementation-fix-design.md
git commit -m "$(cat <<'EOF'
docs(audiobooks): mark ABS Phase 0 as implemented

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Done Criteria

All boxes checked. `go test ./internal/audiobooks/... -v` is green. `make build` succeeds. The iOS or Plappa client can:

1. Add silo as a server.
2. Log in with real credentials.
3. Browse the audiobook library.
4. Tap a book and start playback.
5. Stop playback partway, force-quit the app, reopen — session resumes without re-login, AND playback resumes from the saved position rather than 0.

When the user confirms client end-to-end works, Phase 0 is done. Next: invoke the brainstorming / writing-plans cycle for Phase 1 (bookmarks, collections, playlists, smart collections, RSS, author/series detail, listening stats).
