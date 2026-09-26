---
title: Chapter Thumbnails
description: How to enable chapter preview images, what generates them, and why a library produces none.
summary: Artwork storage, the per-library switch, extraction triggers, playback settings, and log lines to check when nothing appears.
tags:
  - silo
  - docs
  - wiki
  - libraries
  - playback
  - admin
audience:
  - operator
last_reviewed: 2026-09-23
related:
  - artwork-storage.md
  - ../../s3-storage-setup.md
---

# Chapter thumbnails

Chapter thumbnails are the preview images shown at chapter positions in the web
player's seek bar. They are off for every library until you turn them on, and a
library with them off produces none silently.

## Before you start: artwork storage

Silo stores chapter thumbnails with artwork: on local disk by default, or in
the configured public asset S3 bucket. Keep the local artwork directory
persistent in Docker. For a deployment that uses S3, configure the public
bucket before enabling thumbnails. See [Artwork storage](artwork-storage.md)
for the backend choices and [S3 storage setup](../../s3-storage-setup.md)
if you use S3.

## Turn it on per library

Open the library in **Settings → Libraries**, expand **Advanced**, and enable
**Generate chapter thumbnails**. The switch is per library, so a server with a
Movies and a Shows library needs it set on each one you want covered.

Chapter markers and chapter menus work without thumbnails. The switch only
controls the preview images.

## What starts extraction

Opening a watch page and starting playback queue extraction. A scheduled task
also processes files with missing thumbnails:

| Trigger | What it covers |
| --- | --- |
| Opening a title's watch page | Queues that title's files at normal priority |
| Starting playback | Queues the playing file at the current position, ahead of the rest of the queue |
| **Chapter Thumbnail Backfill** task | Scheduled every 6 hours; checks up to 25 eligible files in opted-in libraries per run |

The backfill task is hidden on the Tasks page because it runs on its own
schedule and needs no operator input.

## Files need chapter markers

Extraction reads the chapters already in the file's metadata; Silo does not
detect scene changes. A file with no chapter markers is skipped with
`no_chapters`, and that is not a failure. Remux such a file with
chapters, or accept that it has no previews.

## Playback settings

**Settings → Playback** holds the rest:

| Setting | Effect |
| --- | --- |
| **Chapter thumbnail workers** | Parallel background extraction jobs per server process. Requires a restart. |
| **Generate chapter thumbnails on** | Run extraction locally or on a usable transcode node. The field warns when no usable transcode node is available. |
| **HDR handling** | Generate thumbnails from HDR sources when possible, or skip HDR and Dolby Vision sources. |
| **Software HDR tone mapping** | Tone map on the CPU when the GPU cannot. Slow, and unavailable when HDR handling is set to skip. |

HDR frames need color conversion before a thumbnail looks right, and that
normally runs on the GPU. On a host without suitable hardware, either enable
software tone mapping and accept slower extraction, or set HDR handling to skip
those sources. With neither, extraction can fail with `tonemap_unsupported`
and retry after the configured delay.

## When nothing appears

Filter the log by the `chapterthumbs` component. The reason on a skipped or
failed request tells you which case you are in:

| Log line | Meaning |
| --- | --- |
| `request skipped … reason=folder_disabled` | The library switch is off, or the folder itself is disabled |
| `request skipped … reason=no_chapters` | The file has no chapter markers |
| `request skipped … reason=no_eligible_chapters` | Every chapter has an image or is waiting for a retry |
| `request skipped … reason=hdr_policy_disabled` | HDR handling is set to skip, and this source needs tone mapping |
| `extract failed … reason=tonemap_unsupported` | HDR source that could not be tone mapped with the current settings |
| `probe failed … reason=probe_failed` | Silo could not probe the file's chapter metadata |
| `extract failed … reason=ffmpeg_probe_failed` | FFmpeg's filter probe failed during frame extraction |
| `upload failed` | Extraction worked, but the image could not be stored. Check local artwork storage permissions and free space, or the public bucket's credentials and endpoint if using S3 |

A failed chapter becomes eligible for another attempt after 15 minutes, then
1 hour, 6 hours, and 24 hours after successive failures. Later failures keep
the 24-hour delay. Opening the watch page, starting playback, or the scheduled
backfill can trigger an eligible retry without a rescan; a backfill backlog can
delay it.

## Source References

- [`internal/chapterthumbs/service.go`](../../../internal/chapterthumbs/service.go)
- [`internal/taskmanager/tasks/chapter_thumbnail_backfill.go`](../../../internal/taskmanager/tasks/chapter_thumbnail_backfill.go)
- [`web/src/components/admin/libraries/LibraryFormSections.tsx`](../../../web/src/components/admin/libraries/LibraryFormSections.tsx)
- [`web/src/pages/admin-settings/PlaybackSettings.tsx`](../../../web/src/pages/admin-settings/PlaybackSettings.tsx)
- [S3 storage setup](../../s3-storage-setup.md)
