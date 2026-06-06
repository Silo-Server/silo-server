# Audiobooks Absorption — Sub-plan 5: Podcasts (RSS feed refresher)

**Goal:** Port the plugin's `podcastfeed` package as a first-party scheduled task in silo, so RSS-subscribed podcasts get their episode lists kept up to date.

**Source plugin:** `../silo-plugin-audiobooks/internal/podcastfeed/refresher.go` (353 LOC) + `refresher_test.go` (295 LOC)

## Tasks

### Task 1: Port the refresher package
- Copy `refresher.go` + `refresher_test.go` from plugin to `internal/audiobooks/podcastfeed/`
- Update imports (drop plugin SDK; use silo's `pgxpool` + `log/slog`)
- Rewrite SQL queries against silo's `podcast_feeds` table (created in sub-plan 1 migration 140), `media_items` (type='podcast'), and `episodes`
- Drop the plugin's `presentation_libraries` reference; subscriptions are tracked by `podcast_feeds.media_item_id`

### Task 2: Wire as scheduled task
- Discovery D8 documented silo's task registration pattern: `taskMgr.Register(tasks.NewXxxTask(...))` in `cmd/silo/main.go`
- Add `tasks.NewSyncPodcastFeedsTask(refresher)` and register
- Default interval: 10 minutes (matches plugin)

### Task 3: ABS podcast endpoints (optional)
The plugin's `abs/podcast.go` + `abs/rss_feed_handler.go` + `abs/podcast_handler.go` expose:
- `GET /abs/api/libraries/{id}/podcasts/{podcastId}/episodes/{episodeId}`
- `POST /abs/api/libraries/{id}/podcasts` (add subscription)
- `GET /abs/api/podcast/feeds` (admin RSS feed browse)

If reach is required for ABS clients, port these. Otherwise stub 501 and ship. For initial release, stub.

### Task 4: Build + smoke
- Verify scheduled task registers on startup (`docker logs` shows the task)
- Verify nothing else breaks

## Risks

- Plugin's RSS parser may depend on third-party packages not in silo's go.mod. Check go.mod after porting and add if needed.
- `episodes` table in silo has a different shape than the plugin assumed. Re-read the plugin's INSERT statements and adapt.
