# Hero clips

A hero clip is a short cut of a title's own trailer, played muted behind the
featured row's backdrop on Home and library pages. `internal/heroclips` owns it.

## What it is, and what it is not

A clip is a **derived asset**, the same kind of thing a seek-preview sheet or a
chapter thumbnail is: made once, stored like artwork, and served to clients as a
URL. Nothing is generated on a page load — the point of a clip is that the hero
downloads a finished file the way it downloads a finished image.

A clip is **not** a playback session. It never touches the transcode or streaming
path, it is never counted as a view, and it never appears in Continue Watching. It
is also not a second copy of the trailer's source: what is stored is already cut
to the recipe, so serving it costs one ranged file read.

## The recipe

The recipe is the clip's identity: `AlgorithmVersion`, width, CRF and audio
bitrate, joined into one string (`v1-w1280-crf28-a128k`). A stored row carries the
recipe that made it, so **bumping `AlgorithmVersion` rebuilds clips in place** —
the new object gets a new key beside the old one, and the sweep collects the old
one. That is the whole migration story for a recipe change.

Bounds are enforced rather than assumed: a source whose duration or size exceeds
`MaxDurationSeconds` or `MaxBytes` is refused and the title is recorded as failed,
because a provider's "trailer" can be a full programme.

## Making one

`Generator.Create` runs four steps in a fixed order:

1. **Download** the trailer for the item's video reference (a YouTube id today)
   with `yt-dlp`, capped at 1080p because a 1280-wide clip cannot use more.
2. **Encode** with ffmpeg: scale to the recipe width, H.264 CRF 28, stereo AAC,
   `+faststart` so the hero can start before the whole file arrives. The audio
   track is kept in the file and the *element* plays muted — a file with no audio
   can never be unmuted, which is what made the same toggle look broken in the
   project this feature came from.
3. **Upload** to the assets store under the clip's key.
4. **Publish** the row that names it.

Publishing last is the invariant that matters: a client is never handed a URL for
an object that is not there yet, and a run that dies mid-flight leaves an
unreferenced object for the sweep rather than a row pointing at nothing.

A failure is recorded **on the title** (state, error, timestamp) and reported as a
successful run. One unreachable trailer must not stop the others, and the row is
what makes the generator back off instead of retrying every pass. A failed retry
never clears a working clip: `RecordFailure` keeps `state = 'ready'` when one is
already published.

## Where clips live, and why the sweep does not eat them

Keys are `hero-clips/<content-id hash>/<recipe>.mp4`. The item directory is a
hash because a content id carries a colon (`movie:tmdb-447332`), which local
storage refuses in a filename and a URL has to escape.

`blobgc` groups a clip **on its own key**, not on its item's directory. That is
deliberate: the recipe is part of the key, so when a recipe change writes a new
object the superseded one stops being published and is collected on its own.
Grouping by item would leave it stranded inside a live directory forever.

Two consequences to keep in mind when changing any of this:

- A new prefix a namespace may hand the GC queue has to be admitted by the
  `blob_gc_queue_prefix_check` constraint, or the sweep's insert fails. The
  migration that added `hero_clips` extends it; a new namespace pattern means
  another migration.
- Liveness needs no join on `media_items`: `hero_clips` cascades from it, so a row
  whose item is gone cannot exist, and an object left behind by a deleted item is
  exactly what the sweep is for.

## Delivering one

The server puts the clip's URL on the section card it belongs to:
`hero_video_url`, resolved in one bulk query per row and signed through the same
machinery as artwork, with a longer lifetime — a clip plays for minutes and the
element re-requests as it goes. Guideposts:

- The field is **v2 only**. Frozen `/api/v1` cards carry no such member; the
  shared card view keeps it behind `json:"-"` and v2 maps it under its own name.
- The URL is absent when a title has no clip, which is the ordinary case. A client
  treats that as normal, never as an error, and never waits for it.
- A failure to resolve one URL drops that item's clip and keeps the rest of the
  row: one bad key must not blank a hero.

## Generating them, and who pays for it

`GenerateHeroClipsTask` is where clips get made: startup, then every six hours,
visible in the admin task list, boundable per run. It targets the titles the
**featured discover rows hold** — read from the same published snapshot the hero
renders — not every title in a library, so the work follows what viewers are
actually shown.

An administrator turns generation off by disabling the task; nothing else needs a
switch. Whether a *viewer* sees moving heroes is a separate, per-profile choice:
`hero.trailers_enabled` in the settings contract, default **off**, with an admin
default so one decision can cover a household. Playback is what that setting
gates, so a deployment that never generates clips behaves exactly as it did
before this feature existed.

The downloader is `yt-dlp`, installed in the image, and `SILO_YTDLP_PATH` points
at somewhere else when a deployment keeps its own. A server whose IP a provider
refuses records failures per title and retries them a day later; it does not spin.
