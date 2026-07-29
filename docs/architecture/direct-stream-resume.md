# Direct Stream Resumption

Protocol v3 advertises `direct_stream_resume_v1` for playback plans whose
delivery is `original_http`. The capability formalizes resumption of the
original file by issuing sequential authorized HTTP requests. It does not
change authentication, authorization, or playback-session ownership.

Original-file responses follow the HTTP byte-range contract:

- `Accept-Ranges: bytes` advertises byte addressing.
- A satisfiable `Range` request returns `206 Partial Content` with the selected
  interval in `Content-Range`.
- Open-ended and suffix byte ranges are supported.
- A range starting at or past end of file, or another invalid range, returns
  `416 Requested Range Not Satisfiable` with `Content-Range: bytes */<size>`.
- `HEAD` returns the same representation headers as `GET` without a body.

On Linux, macOS, and Windows, each response carries a strong, opaque `ETag`
derived from the open file's filesystem identity, change time, modification
time, and size. The validator is stable while the playback plan's original-file
entity is unchanged, but changes for same-size replacements even when their
modification time is preserved. Platforms that cannot expose a durable
filesystem revision omit the validator instead of hashing an entire media file
before each request. On those platforms an ETag-based `If-Range` request cannot
match and safely falls back to a full `200 OK` response.

A client resuming a transfer sends both `Range` and `If-Range` with the
validator. When it still matches, the server returns the requested `206`
response. When the entity changed, the server ignores the range and returns the
entire current entity as `200 OK`, preventing bytes from different revisions
from being combined. `If-None-Match` uses the same validator for ordinary
conditional requests.

Playback sessions already treat each transport request independently.
Sequential ranged requests refresh transport activity, and cleanup never
expires a session while one of those transport requests is active. A late
request within the paused-session grace remains valid under the same rules as
the initial request.

The capability does not apply to progressive remux delivery
`server_remux_progressive`, which is not byte-resumable. It applies only to
`original_http`; remux and transcode transports retain their existing
contracts.
