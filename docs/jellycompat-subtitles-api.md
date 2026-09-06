# Jellyfin text subtitle delivery

Jellyfin playback negotiation accepts `DeviceProfile.SubtitleProfiles`. For text
tracks delivered separately from video, it prefers a matching external format
and otherwise uses an advertised external WebVTT format. This applies to embedded
text, external sidecars, and downloaded subtitles. Embedded tracks retain
`IsExternal: false`; `DeliveryMethod: "External"` describes delivery rather than
where the track is stored. A selected embedded track keeps Embed delivery when
the profile supports it; otherwise its negotiated external format is persisted
with the playback source so reconstruction produces the same delivery URL.

The authenticated subtitle route
`/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/stream.{format}` supports the
Jellyfin `.js` representation for text tracks. Its JSON object contains a
`TrackEvents` array. Each event contains `Id`, `Text`, `StartPositionTicks`, and
`EndPositionTicks`; one tick is 100 nanoseconds. Ordinary VTT, SRT, and ASS routes
retain their respective text representations. JSON delivery honors the same
start/end window and timestamp-copy options as text delivery. A window containing
no cues returns an empty `TrackEvents` array.

Complete embedded text extractions use the node-local subtitle cache. Cache
identity includes source path, modification time, size, track ordinal, and
output format. A source change during extraction prevents publication. Cache
failures do not prevent delivering a successful extraction, and incomplete
extracts are not published. A node's first request may still require a complete
file demux.

Repeated unstarted playback negotiations replace only negotiations with the same
source and track-selection variant. Distinct audio or subtitle selections keep
their own playback grants until they start or expire.

These are Jellyfin compatibility behaviors. Native Silo client API contracts are
unchanged.
