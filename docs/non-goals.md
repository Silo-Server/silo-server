# Non-goals

Silo is an early WIP and most of its scope is open. This document is the short list of things
that are **not** open — features that will not be accepted, in core, in a plugin, or in a client,
regardless of implementation quality.

These are product-direction decisions, not "not yet" or "needs a better design." A well-written
proposal or a working PR does not change the answer. Please read this before opening an issue or
writing code in these areas, so nobody's time is wasted.

## Live TV, OTA tuners, IPTV, and remote stream shortcuts

**Out of scope permanently.** This covers, at minimum:

- Live TV of any kind, including OTA tuners (HDHomeRun and equivalents) and DVB devices.
- IPTV: M3U/M3U8 playlist ingestion, provider portals, Xtream-style APIs, stream relays.
- EPG/guide ingestion, XMLTV import, guide-provider sync, and channel lineup management.
- DVR: scheduled recordings, series rules, tuner arbitration.
- `.strm` files and any equivalent shortcut format whose contents are a remote media URL to be
  fetched, proxied, redirected to, or transcoded by the server.

### Why

**App store distribution risk.** Silo's first-party clients ship on the Apple and Google stores.
Review of media apps in these categories is strict, and an app whose server plays arbitrary
remote stream URLs or IPTV playlists is read as a conduit for unlicensed content no matter what
the intended use is. The exposure is not scoped to the feature — a rejection or a takedown lands
on the entire client suite, for every user, including those who only ever play files they own.
No single feature is worth that trade.

**Product scope.** Silo indexes and plays media that exists on disk in a library the operator
controls. Live TV is a different product with its own always-on reliability burden: tuner
allocation, guide-provider contracts and merge semantics, schedule conflict resolution, and
recording lifecycle. Building it competes directly with making the core playback path fast and
correct, which is the stated priority.

**`.strm` is the same feature with a smaller diff.** A `.strm` file is a text file containing a
remote URL. Supporting it turns the server into a fetcher/transcoder/redirector for arbitrary
remote streams. The small change footprint is exactly what makes it worth naming explicitly here:
it is the capability without the label.

### Not available as a plugin either

The plugin runtime is not an escape hatch for this. Anything in this list needs first-party
client UI to be usable, which puts it back in the app store listings and reproduces the exact
problem above. Not in core, not in a plugin, not in a client.

### What to do instead

Silo does not assume it is the only media server on the box. Run Jellyfin, Plex, or a dedicated
tuner/IPTV backend alongside it for live TV, and use Silo for the on-disk library. That
separation also keeps the risk where it belongs, on software whose distribution model can absorb
it.

## Deleting or managing media files

**Out of scope permanently.** Silo is a media server, not a media manager. It reads the files in
a library; it does not delete, move, rename, or reorganize them. This covers, at minimum:

- Deleting a media file or item from disk, through any API, admin option, or client action.
- Moving, renaming, or sorting files into a folder layout.
- Automatic cleanup, such as removing an episode once it has been watched.

### Why

**The library belongs to the operator.** Silo only needs read access to media; the shipped Compose file mounts libraries read-only. A server that can
delete files needs write access to every library, and then one bug, compromised account, or
mistaken tap can destroy media that may not be backed up. Keeping Silo read-only removes that
whole class of failure.

**Other tools already do this well.** Library managers such as Sonarr and Radarr, and the
operator's own file manager or shell, own file layout and cleanup. When they change files, Silo's
scanner picks up the result.

### Not available as a plugin either

A plugin that deletes or moves library files brings back the same write access and the same risk.
Not in core, not in a plugin, not in a client. The Jellyfin compatibility layer reports items as
not deletable for the same reason.

### What to do instead

Manage files with the tools built for it: a library manager, a file manager, or the shell. Silo
reflects the change on its next scan.
