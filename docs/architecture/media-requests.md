# Media requests

A user asks for a movie or series the server does not have, an admin approves it
(or the user's policy approves it automatically), a request-router plugin sends it
to a downstream service such as Sonarr or Radarr, and the request completes when
the media is in the library. The code lives in `internal/requests`; the reconcile
pass is the `reconcile_requests` task in `internal/taskmanager/tasks`.

## State

A request row carries two fields:

- `status`: `pending → approved → queued → downloading → completed`.
- `outcome`: `active`, or the terminal `declined`, `cancelled`, `failed`.

Once a request is submitted, it fans out into one `media_request_targets` row per
quality. Each target change recomputes the request's status and outcome in the
same transaction (`aggregateStatus`), so after submission the targets own the
request's state.

## Routing facts

Creating a request reads the title's TMDB detail once, after the cheap refusals
(a movie already in the library, a title already requested). The server's copy of the title and
year replaces the client's, and a snapshot of what routing can match on is
stored with the request as `routing_facts`: TMDB genre, keyword, network and
company IDs, original language, origin countries, year, rating, and whether
it is anime. IDs rather than names, because names follow the configured TMDB
language.

Anime means Japanese animation (`internal/requests/anime.go`). TMDB's anime
keyword alone misses about one anime series in eight and one film in three, so
a title also counts when TMDB files it as Animation in Japanese or from Japan,
or when an AniDB-based list names it (a series also needs to be in Japanese or
from Japan, since the list names some Western series and a series' anime flag
can set Sonarr's series type). The list is Kometa's Anime-IDs, matched
on the TVDB series ID (series) or IMDb ID TMDB reports; the "Refresh Anime
List" task (`internal/animeids`) downloads it daily into `anime_ids`, one
server at a time under a lease, keeping the stored copy when a download fails
or looks truncated. The request path only reads the table, and a failed
lookup counts as not listed. AniDB also lists Chinese and Korean animation,
which counts only when TMDB itself tags it anime; an admin routes it with a
genre and language rule instead.

The rating is the US one when the title has one. A title never rated in the
US falls back to its own country's (the first origin country TMDB rated it
in, strictest where it rated it more than once), stored with the country as a
prefix ("JP:PG12") so `access.Normalize` reads its age on that country's
scale; so does a title rated only "NR" in the US. The parental-control path
keeps to the US rating.

When TMDB cannot answer, the request is still created from the client's copy,
and the facts stay uncaptured until routing fetches them.

## Routing

Silo, not the router plugin, decides which server each quality tier of a request
goes to (`internal/requests/routing.go`). Routes (`request_routes`) belong to a
media type and hold conditions and a destination per tier: a server plus
overrides for its plugin config (root folder, quality profile, tags, series type,
minimum availability, ...). Conditions match on the request's routing facts and
requester: anime, genre, keyword, original language, origin country, year range,
network, studio, requesting account, and content rating ("at most PG"). Every
set condition must hold; a list matches any of its values, and each list has an
exclude form that matches when the title has none of them ("original language
is not English"). A title with no rating (US or its own country's) never
matches a rating condition, as a parental ceiling treats it. Ratings compare by
their own minimum ages ("TV-Y7 or lower" does not take TV-PG), and a title TMDB
had not rated yet is asked about again a day later. The rating is captured with
the other facts from the detail TMDB already returns; a request captured before
the rating was gets it at submission, only when a route checks ratings.

Each tier is decided on its own: the first enabled route, in position order,
whose conditions match and that has a destination for the tier wins, and the
media type's fallback route (no conditions) comes last. A route with no
destination for a tier lets that tier fall through; `skip_uhd` stops a matching
title from getting a 4K copy at all, even with `force_dual_quality`, and so
does Everything else with no 4K server: "no 4K copy" means the same on a rule
and on the fallback. The admin preview explains a decision route by route: the
conditions each failed and what it did per tier (sent, skipped, passed on, did
not match, came after the tier was decided).

A routed submission calls the plugin once per tier with only the chosen server.
Its config carries the route's overrides and marks it the tier's default in the
Sonarr/Radarr plugin's terms, with the plugin's own anime overlay off, so the
existing plugin follows the route without knowing about routing. Each target
records the route that sent it. A server a route sends to cannot be deleted
until the route stops using it, so deleting a server never silently reroutes
titles, and it cannot be switched to the type the route's media type cannot use
(Sonarr for movies, Radarr for series) or stop taking that media type. A chosen
server that is disabled, not set up (no installation, no key), of the wrong type
or not taking the media type anyway is an admin-fixable
problem: when nothing has been
sent yet, the submission retries with backoff; a later tier that fails that way
becomes a failed target. Status checks go through the plugin installation that
owns each target's server, and one plugin failing does not discard the statuses
another reported. A media type with no routes keeps the plugin's own routing:
every usable connection is handed over and the plugin picks.

Admins manage routes through `/api/v2/admin/request-routes` (in the web admin,
Settings › Requests, which also hides the server switches routing now owns).
Each media type
always has its fallback ("Everything else"); until it is saved it has no servers
and routing leaves the media type to the plugin. Saving it requires an HD server,
since a saved fallback moves the media type to Silo's routing. A rule cannot be added before
the fallback has an HD server, because the first rule switches the media type to
Silo's routing and titles no rule matches would otherwise have nowhere to go.
The first Radarr (Sonarr) server added becomes Everything else for movies
(series) in the same transaction, unless another enabled server already takes
that media type (another of the kind, or a Seerr connection) or the new one is
flagged 4K; a migration did the same for installs with exactly one usable
server of a kind, so a single-server setup needs no routing. Deleting the last server of a kind removes that Everything else with
it when no rule routes the media type; otherwise the delete is refused.
Rules must narrow (at least one condition) and must do something (a destination,
or skip 4K), and cannot override the config keys routing sets itself.
A Radarr or Sonarr takes one version only: 4K versions go only to a server
marked 4K (the plugin's `is_4k` switch, or its older `is_default_4k`), HD
versions only to one that is not. Saving a route that breaks this is refused,
and so, under Advanced, is changing a server's 4K switch while a route sends it
the other version. A server of another plugin (Seerr) takes either version.
Changing a server's type or media types while a route sends it a media type it
would no longer take is refused too. A route save and a server save each check
the other again with the server row locked, so two admins saving at once
cannot leave a route pointing at a server that no longer fits.

The migration that introduced routes carried the Sonarr/Radarr plugin's routing
over unchanged: each media type's first usable default and default-4K servers
(by name) became its fallback route, and a default server's anime settings
became an Anime route. Once a media type has routes, the connections' own
default and anime switches no longer decide anything.

A request created before facts were captured, or while TMDB was unreachable, has
them fetched when it is first routed; if TMDB still cannot answer, the
submission retries rather than route on missing facts, unless no enabled route
of its media type has a condition (Everything else alone decides), when it
goes without them. Standard needs only the anime fact, so without TMDB it
routes on the anime flag stored with the request.

### Standard and Advanced

`request_routing` holds the routing mode (`internal/requests/routing_mode.go`).
Advanced routes with the rules above. Standard pauses the rules (they stay
stored and apply again under Advanced) and sends each media type to its one
enabled server that is not marked 4K, and its 4K copies to its one enabled
server marked 4K (the Sonarr/Radarr plugin's `is_4k` switch), with each
server's own settings: Everything else's overrides do not apply. Anime series
(see "Routing facts") go to the same servers with Sonarr's anime series type,
as Seerr sends them. With no server
marked 4K there is no 4K copy, even with `force_dual_quality`. A media type
whose server is another plugin (Seerr) keeps that plugin's own routing. Targets
record the route as "Standard".

Standard needs at most one normal and one 4K enabled server per media type,
counting other plugins that take it. When one of them is a plugin that picks
its own server (Seerr), both must be connections of that one plugin: Standard
hands such a plugin the whole request, so a 4K Radarr beside it would never be
used. Switching to Standard is refused
otherwise, and adding or enabling a server that breaks the rule turns Advanced
on in the same transaction, with Everything else given the servers Standard was
using where it has none, so requests keep going where they went. Only Radarr
and Sonarr servers that still take the media type after the save are carried
over; a media type another plugin routed stays with it. When that media type
has no rule and another plugin installation would now also take it, the save
is refused: with no rule, the first connection by name would get its requests.
The admin switches to Advanced and sets Everything else first. Switching to
Advanced by hand carries servers over the same way. Standard lets a server's
4K switch change even while a paused route sends it the other version, since
those routes cannot be edited there; turning Advanced on, either way, first
clears every route destination whose server no longer fits its version, so that
version falls through to Everything else and its carried-over server. Nothing switches back to Standard on its own. Every
server write and mode switch takes one advisory lock first, so a server added
while Standard is being turned on cannot leave Standard on with two servers of
a kind; Standard read with two servers anyway routes with the rules. Under
Standard, Everything else does not keep a server from being deleted (the
reference is cleared); a paused rule still does. The migration put installs on
Advanced when Standard would send a request elsewhere (an enabled rule, two
servers of a kind, or an Everything else with overrides, a 4K server, an HD
server other than the media type's one Radarr or Sonarr, or no 4K server while
one marked 4K exists) and everyone else on Standard.

## Transitions are guarded

Every status or outcome write made by an admin, a user, or the reconcile pass
names the states it may start from (`StateGuard`), and the store applies it with
a single `UPDATE … WHERE status = ANY(…) AND outcome = ANY(…)`. A write whose row
has already moved fails with `ErrInvalidState`. Two admins approving at once, or
an approval racing a decline, therefore apply exactly one transition. Services
never check the state in Go and then write it unconditionally. The exception is
the target aggregate: a target change always recomputes the request's status from
its targets, because after submission the targets are the source of truth.

Decline and cancel apply to requests nothing has been sent for: `pending` ones,
and `approved` ones with no target and no live submission lease (waiting for the
library, or backing off after a failed send). Once a submission is in flight or
a target exists, the request stays in the pipeline until it completes or fails,
because withdrawing it could leave the downstream service's state diverged from
Silo's. Retry reopens a `failed` request to `approved` + `active` in one guarded
write.

## Submission is claimed

Approval commits before anything is sent. The submission itself runs only for
the caller that claims it: `ClaimSubmission` sets `submit_lease_until` and counts
the attempt, and succeeds only while the request is `approved` and `active`, no
lease is live, and its `next_submit_at` backoff has passed. Admin approval, auto-approval and the
reconcile pass on every server all go through the claim, so a request is never
submitted twice at once. The claim is a lease: if the claiming server dies
mid-call, the reconcile pass picks the request up after the lease.

A failed attempt keeps the approval. It records `last_error`, releases the lease,
schedules the next attempt in `next_submit_at` (5 minutes, doubling to an hour),
and answers the caller with
the approved request rather than an error. Only the attempt holding the current
lease can do this: one that outlived its lease while another server claimed the
request leaves the newer claim alone. After `maxSubmitAttempts` the request
is marked `failed` for an admin to retry, under the same lease check.

A successful attempt records its targets under that check too, in one
transaction with the status they imply. An attempt that outlived its lease while
the request was withdrawn, completed from the library, or claimed again drops
its result and leaves the request as it finds it. The router call itself carries
no idempotency key, so a service the stale call reached may still hold the title.

A submission converges the request's targets to the qualities it currently
wants. A failed target for a quality it no longer wants is deleted, but only
when that quality set was resolved without error. A failed entitlement lookup
or a connection skipped for a missing key makes the set look smaller than it
is, so in either case every failed target is kept for an admin to see. If
nothing is left to send, the remaining targets decide the status.

## Season requests

A series request names the seasons it wants (`seasons`). Season numbers start
at 1: a request naming season 0 (specials) or a negative number is refused with
`validation_failed`, never read as naming none. When the requester names none, the server asks for every aired regular season (TMDB's seasons that
have started airing, specials excluded) that is not complete in the library.
Requests from before season requests, and every v1 request, have no seasons:
they mean the whole series and keep the old rule that any episode in the
library fulfills them. A series with no aired season yet, and not in the
library, is requested whole unless the requester names seasons.

The request sent to a router plugin names the same seasons
(`RequestDescriptor.seasons`, empty for the whole series). A plugin whose
manifest declares `request_router.supports_seasons` acquires only those
seasons: it adds them to a series its download server already tracks and
leaves the other seasons alone, and a repeated request converges. Any other
plugin, including every one built before the flag existed, ignores the seasons
and takes the whole series. The host reads the flag from the capability
metadata stored at install, so checking it never launches the plugin.

A series in the library can be requested for the seasons it lacks (aired and
incomplete, or not aired yet) when no download server takes series, in which
case the library fulfills the request, or when every enabled download server
that takes series is bound to a plugin that declares `supports_seasons`. A
server on any other plugin would add the whole series again: refused by a
server that has it, every season downloaded by one that does not. So with such
a server, a series in the library stays `already_available`, as before season
requests. `missing_seasons_requestable` on the feature status reports the same
answer. Only the series detail applies this: search, discover and
recommendation cards report any series in the library as available.

Submission applies the rule per request. A season request for a series outside
the library goes to its server as usual. One for a series in the library goes
only where the servers chosen for it take seasons: without routing rules every
series server, with rules the servers the rules choose for the title, or every
server a series rule sends to while TMDB cannot supply its routing facts.
Otherwise it waits for the library, as when it was made before a server that
cannot take seasons was set up. One whose seasons are already complete in the
library when it is approved is not sent; the reconcile pass completes it. If routing facts read after the submission
claim choose a server that cannot, that tier is not sent and the attempt fails
with a message naming the server.

A season is complete when every aired episode of it has a file in an enabled
library, judged by the library's own provider metadata, so no external service
is involved. An episode has a file when one is linked to it
(`episode_libraries`) or when a multi-episode file of its season spans it (the
file links to its first episode only). Only aired episodes count toward an aired
one, so an episode present ahead of its air date cannot stand in for a missing
one. A season whose episodes have no air dates yet counts as complete once any
episode of it is present.

A season request is fulfilled, completed by the reconcile pass and notified,
when every requested season is complete; while only some are, its state is
`partially_available`. Once the download server reports the request done, a
season also counts when any episode of it is present: an episode the server
cannot find would otherwise hold the request and its notification open for
good. A season with no episode in the library never counts, since it may not
have aired yet. A library whose metadata provider numbers seasons differently
from TMDB (absolute or TVDB order) can therefore leave a season request
waiting; the fulfilled-notification pass stamps each request it checks without
notifying, so such requests rotate behind newer ones rather than starve them.

The series detail lists each regular season with its availability (`missing`,
`partial`, `available`) and whether the active request covers it. A title still
has at most one open request: a second profile that wants the same series while
a request is open follows it.

## Following a title

A profile that finds a title someone else already requested can follow it
instead of requesting it again (`PUT`/`DELETE
/api/v2/requests/follows/{media_type}/{tmdb_id}`). Following needs the same
access as requesting: requests enabled, the account allowed to request and not
blocked by its request limit, and the title within the profile's rating
ceiling. It is refused for a title with no open request (request it instead);
the insert itself checks for the open request and holds a share lock on its
row until the follow commits, so a follow cannot land just after the request
was declined, cancelled or completed, and miss that transition's follow
cleanup.

A follow belongs to a profile and the request that was open when it was made
(`media_request_follows`, keyed by account, profile id and request, since
profile ids repeat across accounts). A series can have completed requests still
waiting for the library beside a newer open request for other seasons; each
request's notification goes to its own follows, and a profile can follow each
of them. Unfollowing a title removes the profile's follows on all of them. A follow
survives its request failing: the title's next request takes over the follows
of a failed request, or one its requester replaced. Declining or cancelling a
request clears its follows: the title is no longer on its way, and the follower
can request it themselves. The requesting profile never needs a follow: the
fulfilled notification always reaches it. When a request's fulfilled
notification goes out, it is also sent to the request's followers, marked
`follower` so its wording does not say "your request", and those follows are
then cleared. A dispatch failure leaves
the follows for the retry, and the server-channel announcement waits until an
attempt has reached every recipient, so a retry does not repeat it.

Request state carries `following` (the viewer requested or follows the title)
and `requested_by_viewer` (the viewing profile made the request, so there is
nothing to follow); `GET /requests/status` advertises `follow_supported`.

## Without a router

Requests do not need Sonarr, Radarr or any other router plugin. When no
enabled router connection serves the request's media type, approval (by an
admin or by the requester's auto-approve policy) leaves the request `approved`,
and the reconcile pass completes it once the title is in the library. A
connection that exists but cannot be used (no API key, not bound to a plugin
installation) is a setup problem instead: the submission retries with backoff
and records the reason in `last_error`, so fixing the connection lets the
request through.

The library also completes requests that never reached a router: a `pending`
request whose title appears needs no approval any more, and a `failed` request
whose title appears is complete, unless it failed after delivering one quality,
in which case the failure stays for an admin to retry. Both go through the same
guarded write as any other completion, and the requester is notified.

## Reconcile

Every five minutes the reconcile pass submits approved requests, asks the router
for target status, and completes open and failed requests whose media is present
in the library.
Completing from the library skips an approved request while its submission lease
is live, so it cannot race a router call that is creating targets.
Every API process runs the task manager, so an advisory lock lets one server run
each pass. A pass has two rotations. In-flight requests (`approved`, `queued`,
`downloading`) get router calls. Requests only the library can complete
(`pending`, and `failed` in the last 30 days) get a presence check and nothing
else, so a backlog of them cannot slow the router polling. Each rotation takes
candidates in `last_reconciled_at` order, stamps each one when checked, even if
it errors, and looks presence up in one batch per media type. The 30-day bound
keeps an upgrade from completing, and notifying, a backlog of old failures.

## Re-requesting a failed title

Creating a request deletes the requester's own failed requests for the same
title inside the insert transaction, so the re-request replaces them. The quota
is checked only there, under the requester's advisory lock, and no failed
request counts against it (see [Who can request](#who-can-request)). Other
accounts' failed requests are left alone as those users' history. Retrying one
of them after someone else has requested the title answers
`ErrAlreadyRequested`, since only one active request per title may exist.

## Admin queue

The admin queue groups requests by what an admin does next, from status and
outcome alone so the database can filter and count them: needs approval
(pending), in progress (approved, queued or downloading), failed, and done
(completed, or closed by a decline or cancellation). An admin can retry a
failed request or close it, which moves it to done (v2 only; the v1 cancel
still refuses a failed request). A closed request stays closed: a target that
reports later updates only itself. A request's
history is its `media_request_events` rows. Target updates record the
request's status or outcome only when it changes, so neither a reconcile pass
nor a second target repeats an entry.

## Who can request

Whether an account may request, whether its requests need approval, and how
many it may make resolve in layers: the account's own settings, then its
access group's (`request_group_limits`), then the server-wide request
settings. A layer set to inherit defers to the next; admins are never capped
by a group. An account is blocked in one of two places only: requests turned
off server-wide, or the requests switch on the account or its access group
(`requests_allowed`). The older `blocked` limit and approval modes on an
account are still honored when an API client writes them, but the migration
that added group limits moved existing ones onto the account's switch and no
editor offers them.

The quota counts the requests an account made in the window, except those
declined or failed: those give their slot back. A cancelled request keeps
counting, or requesting and cancelling could repeat without limit. The one
exception is a failed request an admin closes from the queue: it keeps its
submission error and the slot its failure returned. Every other cancel clears
`last_error`, so withdrawing a request that is backing off after a failed
attempt still counts. The store
checks the quota under the requester's advisory lock, so concurrent creates
cannot both take the last slot.
