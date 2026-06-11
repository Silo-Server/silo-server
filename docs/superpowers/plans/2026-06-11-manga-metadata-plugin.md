# Manga Metadata Plugin + Series Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enrich `type='manga'` series with cover/synopsis/status/genres/author from AniList, via a new `silo-plugin-manga-metadata` plugin and a host `MangaEnricher`.

**Architecture:** A new `metadata_provider.v1` plugin (cloned from `silo-plugin-ebook-metadata`) with an AniList GraphQL source that does high-confidence title matching in-plugin. A host `MangaEnricher` (mirroring `internal/ebooks/enrichment.go`) claims `type='manga'` series, resolves the `manga` provider chain, queries the plugin with the series title, and persists the result. The plugin is the default-enabled manga source via its manifest's `default_priority { manga: 1 }`.

**Tech Stack:** Go 1.26 (plugin + host), `silo-plugin-sdk`, AniList GraphQL (`https://graphql.anilist.co`, no key), PostgreSQL (Goose). Host packages built/tested in `golang:1.26 + libvips-dev`; the plugin builds with plain `go` (no libvips). Commands assume the repository root is the cwd.

Two repos:
- **Plugin** at `/opt/silo-plugin-manga-metadata` (NEW — must be added to the path-guard allowlist in `/opt/silo/.claude/hooks/guard-paths.sh` before editing).
- **Host** at `/opt/silo` (branch `feat/manga-library-type`).

Container test command (host packages):
`docker run --rm -v /opt/silo:/src -w /src -v silo-gocache:/root/.cache/go-build -v silo-gomod:/go/pkg/mod -e GOFLAGS=-mod=mod golang:1.26 sh -c 'apt-get update -qq && apt-get install -y -qq libvips-dev pkg-config && go test <pkg>'`

Plugin test command (plain): `cd /opt/silo-plugin-manga-metadata && go test ./...`

---

## File structure

**Plugin (`/opt/silo-plugin-manga-metadata`)** — clone the ebook plugin's layout:
- `go.mod` — module `github.com/Silo-Server/silo-plugin-manga-metadata`, requires the published `silo-plugin-sdk`.
- `manifest.json` — capability `manga-metadata`, `default_priority { manga: 1 }`.
- `main.go` — runtime server (`GetManifest`/`Configure`/`Search`/`GetMetadata`) — copy from the ebook plugin's `main.go`, changing the capability id and the provider construction.
- `metadata/types.go` — `SearchQuery`, `Match` (copy from the ebook plugin; reuse as-is).
- `provider/provider.go` — `Source` interface + `Provider` registry (copy; `defaultSources` returns the AniList source).
- `provider/anilist.go` — the AniList GraphQL client + source (NEW, the core).
- `provider/anilist_match.go` — the pure high-confidence matcher (NEW, riskiest unit).
- `Makefile` — copy from the ebook plugin (builds the binary).

**Host (`/opt/silo`)**:
- Create `migrations/sql/<ts>_manga_enrichment_state.sql`.
- Create `internal/manga/enrichment.go` — `MangaEnricher` (mirrors `internal/ebooks/enrichment.go`).
- Create `internal/manga/enrichment_test.go`.
- Create `internal/taskmanager/tasks/sync_manga_metadata.go` — mirrors `sync_ebook_metadata.go`.
- Modify the wiring in `cmd/silo/main.go` (construct `MangaEnricher`, register `SyncMangaMetadataTask`, kick after a manga scan) — mirror the ebook enricher wiring.
- Modify the plugin-install path so installing a metadata plugin adds its chain entry to existing libraries of the matching type (the "existing libraries" gap).

Implement Phase A (plugin) first — it's testable standalone — then Phase B (host), then Phase C (deploy + live verify).

---

## Phase A — The plugin

### Task A1: Scaffold the plugin repo

**Files:** create `/opt/silo-plugin-manga-metadata/{go.mod,manifest.json,main.go,Makefile,metadata/types.go,provider/provider.go}`

- [ ] **Step 1:** Add `/opt/silo-plugin-manga-metadata` to the path-guard allowlist: in `/opt/silo/.claude/hooks/guard-paths.sh`, add a case line mirroring the existing sibling entries:
  `  /opt/silo-plugin-manga-metadata|/opt/silo-plugin-manga-metadata/*) exit 0 ;;`
- [ ] **Step 2:** Scaffold by copying the ebook plugin's reusable skeleton:
  ```bash
  mkdir -p /opt/silo-plugin-manga-metadata/provider /opt/silo-plugin-manga-metadata/metadata
  cp /opt/silo-plugin-ebook-metadata/metadata/types.go /opt/silo-plugin-manga-metadata/metadata/types.go
  cp /opt/silo-plugin-ebook-metadata/main.go /opt/silo-plugin-manga-metadata/main.go
  cp /opt/silo-plugin-ebook-metadata/provider/provider.go /opt/silo-plugin-manga-metadata/provider/provider.go
  cp /opt/silo-plugin-ebook-metadata/Makefile /opt/silo-plugin-manga-metadata/Makefile
  ```
- [ ] **Step 3:** Create `go.mod`:
  ```
  module github.com/Silo-Server/silo-plugin-manga-metadata

  go 1.26.0

  require (
      github.com/Silo-Server/silo-plugin-sdk v0.6.0
      google.golang.org/protobuf v1.36.11
  )
  ```
  (Match the SDK version pinned in `/opt/silo-plugin-ebook-metadata/go.mod` — check it; use that exact version.) Then `cd /opt/silo-plugin-manga-metadata && go mod tidy`.
- [ ] **Step 4:** Edit `main.go`: change `const capabilityID = "ebook-metadata"` → `"manga-metadata"`, and update the package import paths from `silo-plugin-ebook-metadata` → `silo-plugin-manga-metadata`. Leave the runtime-server logic identical (it delegates to `provider.Search`/`Fetch`).
- [ ] **Step 5:** Create `manifest.json`:
  ```json
  {
    "schema_version": 1,
    "id": "manga-metadata",
    "name": "Manga Metadata (AniList)",
    "version": "0.1.0",
    "capabilities": [
      {
        "id": "manga-metadata",
        "type": "metadata_provider.v1",
        "default_priority": { "manga": 1 }
      }
    ]
  }
  ```
  (Cross-check the exact manifest schema/field names against `/opt/silo-plugin-ebook-metadata/manifest.json` and match its structure precisely — especially how `default_priority` is keyed by content level.)
- [ ] **Step 6:** Edit `provider/provider.go` `defaultSources(...)` to return `[]Source{ NewAniListSource(...) }` (replacing the ebook sources). It won't compile until Task A4 — that's fine; this task ends at "files scaffolded + go.mod tidy succeeds for the copied files" (temporarily stub `defaultSources` to `return nil` to compile).
- [ ] **Step 7:** Commit (in the plugin repo): `git init && git add -A && git commit -m "chore: scaffold manga-metadata plugin from ebook template"`.

### Task A2: AniList GraphQL client

**Files:** create `/opt/silo-plugin-manga-metadata/provider/anilist.go` + `anilist_test.go`

- [ ] **Step 1: Write the failing test** (`anilist_test.go`) for response parsing against a recorded fixture:
  ```go
  package provider

  import "testing"

  const sampleAniListJSON = `{"data":{"Page":{"media":[
    {"id":98257,"title":{"romaji":"One Punch-Man","english":"One-Punch Man","native":"ワンパンマン"},
     "coverImage":{"extraLarge":"https://img/op.jpg"},"description":"A hero <b>for fun</b>.",
     "status":"RELEASING","genres":["Action","Comedy"],"format":"MANGA","startDate":{"year":2012},
     "staff":{"edges":[{"role":"Story","node":{"name":{"full":"ONE"}}},{"role":"Art","node":{"name":{"full":"Yusuke Murata"}}}]}}
  ]}}}`

  func TestParseAniListSearch(t *testing.T) {
      media, err := parseAniListSearch([]byte(sampleAniListJSON))
      if err != nil { t.Fatalf("parse: %v", err) }
      if len(media) != 1 { t.Fatalf("want 1 media, got %d", len(media)) }
      m := media[0]
      if m.ID != 98257 || m.Title.English != "One-Punch Man" || m.Format != "MANGA" { t.Fatalf("bad parse: %+v", m) }
      if m.Status != "RELEASING" || len(m.Genres) != 2 { t.Fatalf("bad fields: %+v", m) }
  }
  ```
- [ ] **Step 2:** Run `cd /opt/silo-plugin-manga-metadata && go test ./provider/ -run TestParseAniListSearch`; verify FAIL (undefined).
- [ ] **Step 3:** Implement `anilist.go` — the response types + `parseAniListSearch`, plus the HTTP `searchAniList`:
  ```go
  package provider

  import (
      "bytes"
      "context"
      "encoding/json"
      "fmt"
      "io"
      "net/http"
      "time"
  )

  const aniListEndpoint = "https://graphql.anilist.co"

  const aniListSearchQuery = `query ($search: String) {
    Page(perPage: 10) {
      media(search: $search, type: MANGA) {
        id
        title { romaji english native }
        coverImage { extraLarge large }
        description(asHtml: false)
        status
        genres
        format
        startDate { year }
        siteUrl
        staff { edges { role node { name { full } } } }
      }
    }
  }`

  type aniListMedia struct {
      ID    int `json:"id"`
      Title struct {
          Romaji  string `json:"romaji"`
          English string `json:"english"`
          Native  string `json:"native"`
      } `json:"title"`
      CoverImage struct {
          ExtraLarge string `json:"extraLarge"`
          Large      string `json:"large"`
      } `json:"coverImage"`
      Description string   `json:"description"`
      Status      string   `json:"status"`
      Genres      []string `json:"genres"`
      Format      string   `json:"format"`
      StartDate   struct{ Year int `json:"year"` } `json:"startDate"`
      SiteURL     string   `json:"siteUrl"`
      Staff       struct {
          Edges []struct {
              Role string `json:"role"`
              Node struct{ Name struct{ Full string `json:"full"` } `json:"name"` } `json:"node"`
          } `json:"edges"`
      } `json:"staff"`
  }

  func parseAniListSearch(body []byte) ([]aniListMedia, error) {
      var resp struct {
          Data struct {
              Page struct{ Media []aniListMedia `json:"media"` } `json:"Page"`
          } `json:"data"`
          Errors []struct{ Message string `json:"message"` } `json:"errors"`
      }
      if err := json.Unmarshal(body, &resp); err != nil {
          return nil, fmt.Errorf("anilist: decode: %w", err)
      }
      if len(resp.Errors) > 0 {
          return nil, fmt.Errorf("anilist: %s", resp.Errors[0].Message)
      }
      return resp.Data.Page.Media, nil
  }

  func searchAniList(ctx context.Context, client *http.Client, endpoint, search string) ([]aniListMedia, error) {
      payload, _ := json.Marshal(map[string]any{"query": aniListSearchQuery, "variables": map[string]string{"search": search}})
      req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
      if err != nil { return nil, err }
      req.Header.Set("Content-Type", "application/json")
      req.Header.Set("Accept", "application/json")
      resp, err := client.Do(req)
      if err != nil { return nil, fmt.Errorf("anilist: request: %w", err) }
      defer resp.Body.Close()
      raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
      if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
          return nil, fmt.Errorf("anilist: transient HTTP %d", resp.StatusCode)
      }
      if resp.StatusCode != http.StatusOK {
          return nil, fmt.Errorf("anilist: HTTP %d", resp.StatusCode)
      }
      return parseAniListSearch(raw)
  }

  var _ = time.Second // keep import if used for a client timeout in the source
  ```
- [ ] **Step 4:** Run `go test ./provider/ -run TestParseAniListSearch`; verify PASS.
- [ ] **Step 5:** Commit `feat: anilist graphql client + response parsing`.

### Task A3: High-confidence matcher (pure — the riskiest unit)

**Files:** create `/opt/silo-plugin-manga-metadata/provider/anilist_match.go` + `anilist_match_test.go`

- [ ] **Step 1: Write the failing test** covering exact, near-exact, ambiguous→no-match, NOVEL exclusion, and tie→no-match:
  ```go
  package provider

  import "testing"

  func med(id int, romaji, english, format string) aniListMedia {
      var m aniListMedia
      m.ID = id; m.Title.Romaji = romaji; m.Title.English = english; m.Format = format
      return m
  }

  func TestPickConfidentMatch(t *testing.T) {
      cands := []aniListMedia{med(1, "One Punch-Man", "One-Punch Man", "MANGA"), med(2, "Onepunch", "", "MANGA")}
      m := pickConfidentMatch("One-Punch Man", cands)
      if m == nil || m.ID != 1 { t.Fatalf("exact english match expected id 1, got %v", m) }

      // near-exact (punctuation/case) still matches
      if m := pickConfidentMatch("404 demons", []aniListMedia{med(3, "404 Demons", "", "MANGA")}); m == nil || m.ID != 3 {
          t.Fatalf("near-exact expected id 3, got %v", m)
      }
      // ambiguous: two equally-good matches → no match
      if m := pickConfidentMatch("Berserk", []aniListMedia{med(4, "Berserk", "", "MANGA"), med(5, "Berserk", "", "MANGA")}); m != nil {
          t.Fatalf("ambiguous tie should be no-match, got %v", m)
      }
      // NOVEL format excluded even on exact title
      if m := pickConfidentMatch("Overlord", []aniListMedia{med(6, "Overlord", "", "NOVEL")}); m != nil {
          t.Fatalf("NOVEL must be excluded, got %v", m)
      }
      // no candidates → nil
      if m := pickConfidentMatch("Nonexistent", nil); m != nil { t.Fatalf("expected nil, got %v", m) }
  }
  ```
- [ ] **Step 2:** Run; verify FAIL (undefined).
- [ ] **Step 3:** Implement `anilist_match.go`:
  ```go
  package provider

  import (
      "strings"
      "unicode"
  )

  var mangaFormats = map[string]bool{"MANGA": true, "MANHWA": true, "MANHUA": true, "ONE_SHOT": true}

  // normalizeTitle lowercases and strips all non-alphanumeric runes so that
  // punctuation/spacing differences ("One-Punch Man" vs "one punch man") match.
  func normalizeTitle(s string) string {
      var b strings.Builder
      for _, r := range strings.ToLower(s) {
          if unicode.IsLetter(r) || unicode.IsDigit(r) {
              b.WriteRune(r)
          }
      }
      return b.String()
  }

  // pickConfidentMatch returns the single candidate whose normalized title
  // (romaji/english/native) exactly equals the normalized query AND whose format
  // is a manga format. Returns nil unless exactly one such candidate exists
  // (no candidates, no exact match, a NOVEL-only match, or a tie all → nil).
  func pickConfidentMatch(query string, candidates []aniListMedia) *aniListMedia {
      want := normalizeTitle(query)
      if want == "" {
          return nil
      }
      var matches []*aniListMedia
      for i := range candidates {
          c := &candidates[i]
          if !mangaFormats[strings.ToUpper(c.Format)] {
              continue
          }
          if normalizeTitle(c.Title.Romaji) == want ||
              normalizeTitle(c.Title.English) == want ||
              normalizeTitle(c.Title.Native) == want {
              matches = append(matches, c)
          }
      }
      if len(matches) == 1 {
          return matches[0]
      }
      return nil
  }
  ```
- [ ] **Step 4:** Run; verify PASS.
- [ ] **Step 5:** Commit `feat: high-confidence anilist title matcher`.

### Task A4: The AniList Source (ID/Search/Fetch + field mapping)

**Files:** modify `provider/anilist.go`; create `provider/anilist_status.go` + `anilist_status_test.go`

- [ ] **Step 1: Write the failing status-mapping test** (`anilist_status_test.go`):
  ```go
  package provider
  import "testing"
  func TestMangaStatus(t *testing.T) {
      cases := map[string]string{"RELEASING":"continuing","FINISHED":"ended","HIATUS":"continuing","CANCELLED":"ended","NOT_YET_RELEASED":"upcoming","":""}
      for in, want := range cases {
          if got := mangaStatus(in); got != want { t.Fatalf("mangaStatus(%q)=%q want %q", in, got, want) }
      }
  }
  ```
  (Confirm the exact status vocabulary the host expects by reading how the ebook/series enricher sets status in `internal/ebooks/enrichment.go` `persist` / `catalog.MetadataUpdate`; adjust the `want` values to match the host's real status strings before implementing.)
- [ ] **Step 2:** Run; verify FAIL.
- [ ] **Step 3:** Implement `anilist_status.go` (`mangaStatus(string) string` per the mapping) and the `AniListSource` in `anilist.go`:
  - `type AniListSource struct { client *http.Client; endpoint string; preferredLang string }`
  - `func NewAniListSource(opts Options) *AniListSource` — `http.Client{Timeout: 15s}`, endpoint default `aniListEndpoint`.
  - `func (s *AniListSource) ID() string { return "anilist" }`
  - `Search(ctx, q)`: `media, err := searchAniList(ctx, s.client, s.endpoint, q.Title)`; `best := pickConfidentMatch(q.Title, media)`; if `best == nil` return `nil, nil` (no confident match is NOT an error); else return `[]metadata.Match{ s.toMatch(best) }`.
  - `Fetch(ctx, id)`: parse `anilist:<id>` → re-query by id (a `Media(id:)` GraphQL query) → `toMatch`. (Add an `aniListByIDQuery`; mirror `searchAniList`.)
  - `toMatch(*aniListMedia) metadata.Match`: map cover (`coverImage.extraLarge`||`large`), description→synopsis (already HTML-stripped via `asHtml:false`), `mangaStatus(status)`, genres, staff (Story→author, Art→artist) → people, the preferred title, and provider id `anilist` → `<id>`. Match the `metadata.Match` field names from `metadata/types.go`.
- [ ] **Step 4:** Run the status test + `go build ./...`; verify PASS + compiles. Update `defaultSources` (Task A1 stub) to `return []Source{ NewAniListSource(options) }`.
- [ ] **Step 5:** Commit `feat: anilist metadata source with field mapping`.

### Task A5: Wire main.go + build the binary

- [ ] **Step 1:** Ensure `main.go`'s content-type handling passes `manga` through (the ebook plugin maps item types; confirm `Search` uses `req.GetQuery()` as the title and doesn't hard-reject `manga`). Adjust `metadataItemFromMatch`/`providerSearchResultFromMatch` item-type handling so a `manga` item type is accepted.
- [ ] **Step 2:** `cd /opt/silo-plugin-manga-metadata && go vet ./... && go test ./... && make build` → a binary is produced. Verify the binary path (check the ebook plugin's Makefile output location).
- [ ] **Step 3:** Commit `feat: build manga-metadata plugin binary`.

---

## Phase B — The host enricher

### Task B1: `manga_enrichment_state` migration

- [ ] **Step 1:** `make migrate-create NAME=manga_enrichment_state` (in the libvips container if needed — see the manga-library-type plan Task 4 for the exact invocation).
- [ ] **Step 2:** Fill it (mirror `ebook_enrichment_state`):
  ```sql
  -- +goose Up
  -- +goose StatementBegin
  CREATE TABLE manga_enrichment_state (
      content_id text PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
      failures integer NOT NULL DEFAULT 0,
      updated_at timestamptz NOT NULL DEFAULT now()
  );
  -- +goose StatementEnd
  -- +goose Down
  -- +goose StatementBegin
  DROP TABLE manga_enrichment_state;
  -- +goose StatementEnd
  ```
- [ ] **Step 3:** Validate DDL (throwaway pg with a stubbed `media_items`, as in the manga-library-type plan Task 4). Commit `feat(db): manga_enrichment_state table`.

### Task B2: `MangaEnricher`

**Files:** create `internal/manga/enrichment.go` (+ `enrichment_test.go`). Mirror `internal/ebooks/enrichment.go` (read it in full first).

The deltas from the ebook `Enricher`:
- `claimBatchQuery`: `WHERE mi.type = 'manga'` (not `'ebook'`), join `manga_enrichment_state` instead of `ebook_enrichment_state`, same `poster_path empty AND last_refreshed IS NULL AND failures < cap` predicates.
- `ebookContentType()` → a manga content type (`"manga"`) used when calling the provider Search.
- Chain resolution: resolve the chain for the folder at content level `manga` (the same chain-resolver call the ebook enricher uses, with `"manga"`).
- `persist`: same as ebook (poster via image cacher, description, genres, people, status, provider ids, stamp `last_refreshed`).
- Failure/skip bookkeeping: write to `manga_enrichment_state`.

- [ ] **Step 1:** Write a pure unit test for the one piece of non-trivial mapping logic that isn't in the plugin — e.g. `mangaMetadataUpdateFromResult(result) catalog.MetadataUpdate` if you extract one — asserting status/genres/poster fields map across. If the enricher reuses the ebook mapping verbatim, instead write a test asserting `claimBatchQuery` contains `type = 'manga'` and references `manga_enrichment_state` (a string-contains guard so a copy-paste of the ebook query is caught). Watch it fail.
- [ ] **Step 2:** Implement `internal/manga/enrichment.go` by copying `internal/ebooks/enrichment.go` and applying the deltas above. Keep the package `manga`. Reuse shared types from `internal/metadata`/`internal/catalog` (image cacher, MetadataUpdate, chain resolver) — do NOT duplicate those.
- [ ] **Step 3:** Run the test + `go build ./internal/manga/`; verify PASS + compiles (libvips container).
- [ ] **Step 4:** Commit `feat(manga): series enricher (claims type='manga', resolves manga chain)`.

### Task B3: `sync_manga_metadata` task + wiring

**Files:** create `internal/taskmanager/tasks/sync_manga_metadata.go`; modify `cmd/silo/main.go`.

- [ ] **Step 1:** Create the task by mirroring `sync_ebook_metadata.go` exactly (interface `mangaMetadataEnricher{ Run(ctx)(int,error) }`, `Key()="sync_manga_metadata"`, `Name()="Sync Manga Metadata"`, `Category()=TaskCategoryMetadata`). No test needed beyond compile (it's a thin adapter; mirror the ebook task which has none).
- [ ] **Step 2:** In `cmd/silo/main.go`, find where the ebook `Enricher` is constructed + its `SyncEbookMetadataTask` registered + its image cacher set. Add the parallel manga wiring: construct `manga.NewEnricher(...)`, `SetImageCacher(...)`, register `NewSyncMangaMetadataTask(mangaEnricher)`. If the ebook scan completion kicks `sync_ebook_metadata`, kick `sync_manga_metadata` after a manga scan completes too (find the scan-completion hook).
- [ ] **Step 3:** `go build ./...` (libvips container) — compiles. Run `go test ./internal/taskmanager/...`.
- [ ] **Step 4:** Commit `feat(manga): sync_manga_metadata task + wiring`.

### Task B4: Existing-libraries chain enable on install

The manifest's `default_priority { manga: 1 }` makes the plugin default-enabled for manga libraries created AFTER it is installed (via the unchanged `seedDefaultChain`). Existing manga libraries need the chain entry added when the plugin is installed.

- [ ] **Step 1:** Read the plugin-install handler (`grep -rn "InstallBinaryUpload\|func.*Install" internal/...`/`Silo` install path) and find whether installing a metadata plugin already backfills chains for existing libraries. If it does, no change — note it and skip to Step 4.
- [ ] **Step 2:** If not, add a post-install step: for each existing library whose type maps to a content level the new capability declares a priority for, insert an enabled chain entry (reuse the same insert `seedDefaultChain` uses, with `enabled = true`, priority = the declared `default_priority`). Write the SQL/repo call mirroring `seedDefaultChain`.
- [ ] **Step 3:** Build + test (libvips container).
- [ ] **Step 4:** Commit `feat(plugins): enable a newly-installed metadata provider in existing libraries`.

---

## Phase C — Deploy + live verification

### Task C1: Build, install, deploy

- [ ] **Step 1:** Build the plugin binary (`make build` in the plugin repo).
- [ ] **Step 2:** Install it on the live box via the host plugin-install flow (mirror how the ebook plugin id 8 was installed — `docker cp` the binary into the container + the install API/cmd, or the throwaway install cmd used previously). Confirm it registers with capability `manga-metadata` and `default_priority { manga: 1 }`.
- [ ] **Step 3:** Rebuild the host image from the branch (`docker build -t silo-server:manga .`) and redeploy (`SILO_IMAGE=silo-server:manga docker compose up -d silo`). Confirm the `manga_enrichment_state` migration applied + health is green.
- [ ] **Step 4:** Ensure the manga library's `manga`-level chain has `manga-metadata` enabled (default for a fresh library; for the existing one, confirm Task B4 enabled it, or re-create the library).

### Task C2: Live verification

- [ ] **Step 1:** Trigger enrichment (the periodic task or kick a manga scan). Watch logs for `sync_manga_metadata` / manga enrichment.
- [ ] **Step 2:** Verify in the DB: a sample of `type='manga'` series now have `poster_path` set + an `anilist` provider id; spot-check that the matched titles are correct (e.g. `One-Punch Man`, `404 Demons`).
  ```sql
  SELECT title, (poster_path <> '') AS has_cover, (SELECT provider_id FROM media_item_provider_ids p WHERE p.content_id = mi.content_id AND p.provider='anilist') AS anilist_id
  FROM media_items mi WHERE type='manga' ORDER BY random() LIMIT 15;
  ```
- [ ] **Step 3:** Confirm no-match series (obscure/ambiguous) stay title-only and increment `manga_enrichment_state.failures` (back-off working), and that there is NO book-source storm (the chain resolves only to the manga plugin).
- [ ] **Step 4:** Eyeball the web UI: series cards now show covers; the series detail shows synopsis/status/genres. Note any client follow-up.

---

## Self-review notes

- **Spec coverage:** plugin/AniList source (A2/A4), high-confidence matching (A3), new plugin scaffold (A1/A5), host `MangaEnricher` claim/resolve/persist/back-off (B1/B2), `sync_manga_metadata` trigger (B3), default-enabled chain (B4 + the manifest, verified C1/C2), data flow + no-storm (C2/C3), testing (A2/A3/A4 fixtures + B2). All spec sections map to a task.
- **Riskiest task:** A3 (the matcher) — fully coded + tested here. A4's field mapping and B2's enricher deltas depend on reading the real `metadata.Match` / `catalog.MetadataUpdate` / ebook `Enricher` shapes; those tasks instruct the implementer to read and match them rather than guessing.
- **Cross-repo:** the plugin (Phase A) is independently testable; the host (Phase B) consumes it; Phase C ties them on the live box. Nothing is pushed (local-only per the project rule) until the user has tested.
