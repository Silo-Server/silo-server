# 1.0 compatibility validation matrix

Release-candidate certification for the Jellyfin and Audiobookshelf compatibility surfaces.
This is an execution record, not an implementation checklist. Fill in exact client versions,
server tag, validator, date, environment, and evidence before changing any result to `pass`.

## Release candidate

| Field | Value |
| --- | --- |
| Silo server tag and commit | TBD |
| Silo container digest | TBD |
| RC release manifest | TBD |
| Frozen OpenAPI contract | TBD |
| Website compatibility-boundary commit | TBD |
| Validator | TBD |
| Date | TBD |
| Environment | TBD |
| Validation library snapshot | TBD |

## Named clients

Choose one actively maintained, representative client for each protocol before RC1. Record its
exact public release version and platform here. Changing a named client or version after RC1
requires a new matrix run.

| Protocol | Client | Platform | Version | Selection owner |
| --- | --- | --- | --- | --- |
| Jellyfin | TBD | TBD | TBD | Release owner |
| Audiobookshelf | TBD | TBD | TBD | Release owner |

## Required flows

Use `pass`, `fail`, or `not supported`. Every pass links evidence. A `not supported` result is
allowed only when the limitation appears in the published compatibility boundary and does not
prevent authentication, browse, or playback.

| Protocol | Flow | Expected result | Result | Evidence / issue |
| --- | --- | --- | --- | --- |
| Jellyfin | Authenticate and reconnect | Login succeeds; a restart does not create an unusable session | TBD | TBD |
| Jellyfin | Browse libraries and hierarchy | Allowed libraries, series, seasons, episodes, and artwork render | TBD | TBD |
| Jellyfin | Search | Accessible matching titles appear; restricted titles do not | TBD | TBD |
| Jellyfin | Direct play | A compatible reference title starts, seeks, and reports progress | TBD | TBD |
| Jellyfin | Transcode fallback | An incompatible reference title starts and remains seekable | TBD | TBD |
| Jellyfin | Resume and completion | Resume position and watched state round-trip to Silo | TBD | TBD |
| Jellyfin | Profile restrictions | Restricted content is absent from browse, search, and playback | TBD | TBD |
| Audiobookshelf | Authenticate and reconnect | Login succeeds; a restart does not create an unusable session | TBD | TBD |
| Audiobookshelf | Browse libraries and books | Allowed audiobook libraries, books, covers, and authors render | TBD | TBD |
| Audiobookshelf | Search | Accessible matching books appear; restricted books do not | TBD | TBD |
| Audiobookshelf | Playback | A chaptered reference book starts, seeks, and changes chapter | TBD | TBD |
| Audiobookshelf | Resume and completion | Exact-second resume and completion state round-trip to Silo | TBD | TBD |
| Audiobookshelf | Profile restrictions | Restricted content is absent from browse, search, and playback | TBD | TBD |

## Published compatibility boundary

Before certification closes, link the public Jellyfin and Audiobookshelf compatibility pages
and verify that they distinguish:

- supported authentication, browse, search, playback, progress, and artwork behavior;
- known client-specific limitations;
- intentionally unsupported administrative, social, live-TV, DVR, and server-management APIs;
- the exact named clients and versions certified above.

| Boundary | Link | Reviewed against RC |
| --- | --- | --- |
| Jellyfin | TBD | TBD |
| Audiobookshelf | TBD | TBD |

## Certification result

The compatibility feature passes only when every required flow is `pass` or is explicitly
documented as `not supported`, no open failure violates authentication, browse, playback, or
profile access, and both public compatibility boundaries match the observed behavior.

| Final result | Release owner | Evidence summary |
| --- | --- | --- |
| TBD | TBD | TBD |
