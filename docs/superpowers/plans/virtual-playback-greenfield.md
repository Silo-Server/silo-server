# Greenfield Virtual Playback

This branch starts from upstream Silo Server and targets smooth playback of
Stremio-compatible provider streams on every client. Official clients do not
depend on a custom picker.

## Ownership

The provider plugin owns manifest access, provider IDs, result discovery,
quality-profile filtering, ranking, URL refresh, and provider-specific errors.

Silo owns canonical catalog IDs, virtual media lifecycle, collection jobs,
client capability detection, playback planning, direct play/remux/transcode,
session state, and automatic failover.

## Playback contract

Plugins return normalized, short-lived stream candidates containing a provider
identifier, temporary URI, expiration, resolution, codecs, HDR/Dolby Vision,
audio/subtitle languages, bitrate, size, container, and rank.

Silo automatically selects the best candidate for the client's capabilities
and retries the remaining candidates when startup or transport fails. The web
application may expose the candidates as an optional picker; official clients
receive one ordinary local-like playback session.

## Phases

1. Add the additive SDK/plugin result contract and host virtual-media ingest.
2. Add provider-neutral virtual catalog records and background collection jobs.
3. Add just-in-time candidate resolution and automatic selection/failover.
4. Route virtual sessions through the existing capability-aware playback
   planner and transcode engines.
5. Add the optional web picker, diagnostics, and focused integration tests.

Provider URLs are never persisted and collection sync never prewarms streams.
