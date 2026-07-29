# Response parity reference

## Queries JellyCon sends

| Query | Purpose | Key params |
|-------|---------|-----------|
| Random widgets | Home screen | `SortBy=Random&IsPlayed=False&ImageTypeLimit=0` (no `Fields`) |
| Library browse | Movie/TV list | `Fields=Overview,Genres,Studios,…&SortBy=SortName&ParentId=LIB` |
| Recently added | New content | `Fields=…&SortBy=DateCreated&IsPlayed=False&Limit=20` |
| Recently played | Watched | `IsPlayed=True&SortBy=DatePlayed&Limit=20` |
| In progress | Resume | `Filters=IsResumable&SortBy=DatePlayed&Limit=20` |
| Item detail | Single item | `GET /Items/{id}` |
| Views | Libraries | `GET /Users/{id}/Views` |

## Expected shape

Structural, per response:

- `TotalRecordCount` is the real count, not a ceiling.
- `Items` length matches `Limit`, or the total when fewer exist.
- List items run ~0.5–1.5KB each without `Fields`, ~2–3KB with them.

Fields real Jellyfin returns on a list item with no `Fields` parameter:

```
Name, Id, ServerId, Type, IsFolder, ProductionYear, OfficialRating,
CommunityRating, RunTimeTicks, UserData, ImageTags, ChannelId, MediaType,
LocationType, VideoType, Container, HasSubtitles, CriticRating, PremiereDate,
ImageBlurHashes
```

With an explicit `Fields` parameter: every requested field appears, unrequested ones do
not, `ImageTags` is present even when empty (`{}`), and `People` and `MediaSources`
appear only on detail views — never in a list.

Parameter handling worth asserting directly:

- `ImageTypeLimit=0` → `ImageTags: {}` and no `BackdropImageTags`; `=1` → normal tags.
- `SortBy=Random` → a different order per request.
- `IsPlayed=False` / `True` → filtered accordingly, and the `True` case is fast.
- `Filters=IsResumable` → only items with `PlaybackPositionTicks > 0`.

## Symptoms already traced once

| Symptom | Cause | Where |
|---------|-------|-------|
| `TotalRecordCount` pinned at 10000 | count ceiling | `internal/catalog/browse.go` |
| Same items every request | `SortBy` value unmapped | `query.go:mapSortBy` |
| Parameter ignored (`IsPlayed`, `ImageTypeLimit`) | not parsed | `itemsQuery` / `parseItemsQuery` in `query.go` |
| Items over ~5KB each | fields ungated, detail fetched per item | `mapping.go` |
| Multi-second empty response | over-fetch loop scanning the catalog | needs a dedicated handler |
| Missing Studios/Taglines/Countries | not carried through the intermediate type | `upstream_types.go`, `mediaItemToListItem` |
| `People`/`MediaSources` in a list | resume path built items from the detail mapper | `handleResumeResponse` |

## Files

| File | Role |
|------|------|
| `internal/jellycompat/query.go` | query parsing, field and sort mapping |
| `internal/jellycompat/mapping.go` | DTO construction, field gating |
| `internal/jellycompat/handlers_items.go` | dispatch, `ImageTypeLimit` |
| `internal/jellycompat/content_direct.go` | browse post-filtering, user-data enrichment |
| `internal/jellycompat/dto.go` | JSON tags — `omitempty` decides field presence |
| `internal/jellycompat/upstream_types.go` | catalog-to-DTO intermediate types |
| `internal/jellycompat/logging.go` | debug log middleware (`JELLYCOMPAT_DEBUG_LOG`) |
| `internal/catalog/browse.go` | SQL building, sort, count, pagination |
