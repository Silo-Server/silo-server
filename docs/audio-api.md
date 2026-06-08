# First-Party Audio API

This API is the Silo-native contract for audiobook playback. Clients should use
it for Silo first-party audio experiences instead of the Audiobookshelf
compatibility API. Music can reuse the same session, track, and progress
concepts when the music UX lands.

## Authentication

All endpoints require normal Silo user authentication. Playback start, sync,
close, and bookmark writes also require the active profile headers used by the
first-party clients.

Track stream URLs are returned from the start response and are scoped to the
created audio session. The server validates that the session still belongs to
the authenticated user, and to the active profile when a profile is attached.

## Start Playback

`POST /api/v1/audio/playback/start`

Request:

```json
{
  "content_id": "128814341867700228",
  "start_position": 120,
  "restart": false
}
```

Response:

```json
{
  "session_id": "uuid",
  "content_id": "128814341867700228",
  "type": "audiobook",
  "title": "Book Title",
  "subtitle": "Author or series subtitle",
  "poster_url": "https://server/api/v1/images/...",
  "backdrop_url": "https://server/api/v1/images/...",
  "total_duration_seconds": 8746,
  "resume_position_seconds": 120,
  "tracks": [
    {
      "index": 0,
      "file_id": 123,
      "file_name": "Part 1.mp3",
      "duration_seconds": 3600,
      "start_offset_seconds": 0,
      "stream_url": "/api/v1/audio/playback/{session_id}/tracks/0",
      "stream_type": "progressive",
      "play_method": "direct",
      "codec": "mp3",
      "container": "mp3",
      "bitrate": 128000,
      "chapters": []
    }
  ],
  "chapters": [
    {
      "index": 0,
      "title": "Chapter 1",
      "start_seconds": 0,
      "end_seconds": 1800,
      "track_index": 0
    }
  ],
  "bookmarks": []
}
```

`total_duration_seconds`, `resume_position_seconds`, global chapter times, and
sync positions are cumulative book seconds, not per-file seconds. Clients map
global time to the active track by finding the last track whose
`start_offset_seconds` is less than or equal to the target time.

Tracks are ordered by presentation part index when available, then by path and
file id. The server direct-plays AVFoundation-safe formats and returns
`play_method: "aac"` for fallback streams that are remuxed/transcoded to an
Apple-safe AAC container.

## Stream Track

`GET /api/v1/audio/playback/{session_id}/tracks/{track_index}`

The response is the media stream for the selected track. Clients should request
the track for the current global time and seek within that track using:

`local_seconds = global_seconds - track.start_offset_seconds`

## Sync Progress

`PATCH /api/v1/audio/playback/{session_id}/sync`

Request:

```json
{
  "position": 1234,
  "duration": 8746,
  "is_paused": false
}
```

The server persists `position` as the audiobook progress position. The client
should send cumulative book seconds on a throttle while playing, on manual
seeks, and once more before closing the session.

## Close Playback

`POST /api/v1/audio/playback/{session_id}/close`

Request:

```json
{
  "position": 1234,
  "duration": 8746,
  "is_paused": true
}
```

Close performs a final progress sync and expires the in-memory audio session.

## Bookmarks

`GET /api/v1/audio/items/{content_id}/bookmarks`

`POST /api/v1/audio/items/{content_id}/bookmarks`

`DELETE /api/v1/audio/items/{content_id}/bookmarks/{time_seconds}`

Create request:

```json
{
  "time_seconds": 1234,
  "title": "Chapter note"
}
```

Bookmark positions are cumulative book seconds so they remain stable across
multi-file audiobooks and client platforms.
