---
title: Chapter Thumbnails
description: How to enable chapter preview images, what generates them, and why a library produces none.
summary: The public-storage prerequisite, the per-library switch, what queues extraction, the playback settings that shape it, and how to read the log lines when nothing appears.
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

## Before you start: public asset storage

Thumbnails are stored in the public asset S3 bucket, so that bucket must be
configured first. Until it is, the per-library switch stays disabled and the
form shows *"Public asset S3 storage is required before this can be enabled."*
See [S3 storage setup](../../s3-storage-setup.md) to configure it, and
[Artwork storage](artwork-storage.md) for how the same bucket serves posters
and backdrops.

## Turn it on per library

Open the library in **Settings → Libraries**, expand **Advanced**, and enable
**Generate chapter thumbnails**. The switch is per library, so a server with a
Movies and a Shows library needs it set on each one you want covered.

Chapter markers and chapter menus work without thumbnails. The switch only
controls the preview images.

## What queues extraction

Three things queue work, so thumbnails usually appear before the periodic sweep
reaches a file:

| Trigger | What it covers |
| --- | --- |
| Opening a title's watch page | Queues that title's files at normal priority |
| Starting playback | Queues the playing file at the current position, ahead of the rest of the queue |
| **Chapter Thumbnail Backfill** task | Every 6 hours, works through files that are still missing thumbnails in opted-in libraries |

The backfill task is hidden on the Tasks page because it runs on its own
schedule and needs no operator input.

## Files need chapter markers

Extraction reads the chapters already in the file's metadata; Silo does not
detect scene changes. A file with no chapter markers is skipped with
`no_eligible_chapters`, and that is not a failure. Remux such a file with
chapters, or accept that it has no previews.

## Playback settings

**Settings → Playback** holds the rest:

| Setting | Effect |
| --- | --- |
| **Chapter thumbnail workers** | Parallel extraction jobs per library scan. Requires a restart. |
| **Generate chapter thumbnails on** | Run extraction locally or on a connected transcode node. The field warns when no transcode nodes are connected. |
| **HDR handling** | Generate HDR thumbnails when possible, or skip HDR and Dolby Vision sources. |
| **Software HDR tone mapping** | Tone map on the CPU when the GPU cannot. Slow, and unavailable when HDR handling is set to skip. |

HDR frames need color conversion before a thumbnail looks right, and that
normally runs on the GPU. On a host without suitable hardware, either enable
software tone mapping and accept slower extraction, or set HDR handling to skip
those sources. With neither, HDR files are marked `tonemap_unsupported` and
skipped after their retries run out.

## When nothing appears

Filter the log by the `chapterthumbs` component. The reason on a skipped or
failed request tells you which case you are in:

| Log line | Meaning |
| --- | --- |
| `request skipped … reason=folder_disabled` | The library switch is off, or the folder itself is disabled |
| `request skipped … reason=no_eligible_chapters` | The file has no chapter markers, or every chapter already has an image |
| `request skipped … reason=hdr_policy_disabled` | HDR handling is set to skip, and this source needs tone mapping |
| `extract failed … reason=tonemap_unsupported` | HDR source that could not be tone mapped with the current settings |
| `extract failed … reason=probe_failed` | FFmpeg could not read the file |
| `upload failed` | Extraction worked, but the image could not be stored. Check the public bucket's credentials, endpoint, and that any proxy in front of it leaves signed requests untouched |

A failed chapter is retried on a widening schedule — after 15 minutes, then 1
hour, 6 hours, and 24 hours — so a file that failed while storage was
misconfigured recovers on its own within a day of the fix, without a rescan.

## Source References

- [`internal/chapterthumbs/service.go`](../../../internal/chapterthumbs/service.go)
- [`internal/taskmanager/tasks/chapter_thumbnail_backfill.go`](../../../internal/taskmanager/tasks/chapter_thumbnail_backfill.go)
- [`web/src/components/admin/libraries/LibraryFormSections.tsx`](../../../web/src/components/admin/libraries/LibraryFormSections.tsx)
- [`web/src/pages/admin-settings/PlaybackSettings.tsx`](../../../web/src/pages/admin-settings/PlaybackSettings.tsx)
- [S3 storage setup](../../s3-storage-setup.md)
