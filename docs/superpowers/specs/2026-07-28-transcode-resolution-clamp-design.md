# Transcode Resolution Clamp Design

## Problem

When video encoding is requested for a 2160p file while 4K transcoding is
disabled, the playback handler selects a lower-resolution alternate file.
Today it continues using the target resolution supplied for the original file.
That lets a 1080p alternate reach FFmpeg with a 2160p target, causing an
unnecessary upscale and making the persisted playback activity claim that
2160p was delivered.

Production logs confirmed this exact path with a 1080p H.264 alternate and a
QSV `scale_vaapi` filter targeting 2160 lines.

## Decision

The server owns the output-resolution invariant. After all alternate-file and
probe selection has completed, and before planning or starting any local or
remote transport, it will normalize an encoded-video target so it cannot
exceed the effective source file's resolution.

The normalization rules are:

- Do not apply the new clamp to video-copy requests; preserve their existing
  downstream recipe normalization.
- Preserve an empty or unrecognized requested resolution unchanged.
- Preserve an empty or unrecognized effective source resolution unchanged.
- Preserve targets at or below the effective source resolution.
- Clamp a recognized target above a recognized effective source resolution to
  the effective source resolution.
- Do not alter target bitrate. The observed 10 Mbps value is already a valid
  1080p-high encoding target and is not the demonstrated defect.

Recognized resolutions are the same tiers already supported by the transcode
filter builders: 2160p, 1080p, 720p, 480p, 420p, and 328p.

## Architecture and Data Flow

A small pure helper in `internal/api/handlers/playback.go` compares requested
and effective-source resolution tiers. `HandleStartTranscode` calls it once
after the 4K alternate guard has selected and probed the effective file.

The normalized value replaces `req.TargetResolution`. Existing downstream
paths then automatically use one consistent value for:

- transcode-node planning and requests;
- integrated/local `playback.TranscodeOpts`;
- playback-session replacement and activity reporting;
- later transport reconstruction and restart recipes.

This boundary is preferred over a web-only correction because every client can
call the endpoint, and over an FFmpeg-only correction because session state and
remote transcode requests must remain truthful.

## Compatibility and Error Handling

The change is additive enforcement inside the existing `/api/v1` endpoint. It
does not change request or response shapes, status codes, codec selection,
alternate-file selection, or copy-video behavior. Unsupported resolution
strings retain their existing behavior rather than being silently reinterpreted.

No server settings, migrations, client changes, or production deployment
changes are part of this work.

## Verification

Tests will establish:

1. A 2160p encode request switched to a 1080p alternate reaches the remote
   transcode node as 1080p and persists 1080p in the playback session.
2. The same normalized request reaches the local transcode option boundary.
3. Targets already below the source remain unchanged.
4. Copy-video and unknown-resolution requests remain unchanged.
5. Existing playback-handler and playback-package suites remain green.

The regression must fail against unmodified main before the implementation is
added.

## Security and Operational Impact

The server currently permits an authenticated user with transcoding permission
to amplify compute and bandwidth by requesting a recognized output larger than
the effective source. The clamp removes that unnecessary amplification while
retaining all authorized transcoding behavior.

Rollback is a single code commit. No data rollback is required.
