# Verify affected behavior in a pull request

Pick the cases your change can affect. Start from the app, API, or plugin
that uses the changed path. Use a disposable Silo deployment, test accounts,
and fixture media. For external services, use a test mailbox, sandbox, or
provider stub that can return a known result or a controlled error.

Run the relevant [automated checks](../../CONTRIBUTING.md#validate-your-change),
including DB contracts and query budget pins when the change reaches those
paths. In the [PR evidence](../../CONTRIBUTING.md#show-visible-changes), record
the build or commit, fixture, steps, expected and observed outcomes, and any
case you could not exercise. Include the relevant capture or trimmed response.

## Playback and client interoperability

- **Player and worker.** Play a fixture title with multiple tracks by direct
  play and HLS as applicable. Seek across a segment boundary, switch quality
  during playback, then reopen the title. Check that the player reaches the
  requested position, keeps the chosen tracks, and saves the actual resume
  position. For a routing or worker change, use two disposable workers. Drain
  one or interrupt its stream and verify the expected fallback, one active
  plan, and release of the old reservation.
- **Subtitles and markers.** Use a title with embedded text and image tracks
  and a fixture provider for downloaded subtitles. Select a track, seek,
  adjust delay, and switch titles while a provider response is pending.
  Check that cues follow the selected track and a late result cannot replace
  the next title's subtitles. For marker changes, load a provider suggestion
  and make a manual correction, then refresh and verify the visible range and
  the stored correction. When changing subtitle timestamp mapping, verify
  live fMP4 with video starting at zero and live MPEG-TS with video starting
  after zero. Compare subtitle cues with the same dialogue before and after a seek.
- **Watch Party and external watch sync.** Join a party in two client
  sessions, start, pause and seek as host, then disconnect and rejoin the
  guest. Verify room phase, position and title on both clients. For watch
  provider changes, use a disposable provider account and a profile with one
  watched and one partially watched title. Sync in the affected direction,
  repeat the sync, and verify the same titles, positions and counts on both
  sides. If retry behavior changed, make the provider reject one attempt and
  verify the recovered state.
- **Jellyfin compatibility.** Browse a mixed library in a compatible client
  and request the next collection page with the same filters. For track
  preferences, save a subtitle language and mode, reopen the title and
  compare the displayed default in item details, playback info and the
  running player. For Quick Connect changes, start a disposable session,
  poll before approval, and complete it under the intended profile.

## Catalog and personal libraries

- **Scans, catalog, and metadata.** Scan a series fixture with two sibling
  folders, artwork, subtitles and an NFO file. Change a sidecar in one folder
  and run a scoped rescan or metadata refresh. Check the affected item's
  details, artwork and provider match, and compare the untouched sibling.
  For changes to catalog navigation, use a title with two versions or a
  series with multiple seasons. Page or sort the results as two profiles
  with different access and verify membership, ordering and allowed items.
- **Home and collections.** Create a manual or smart collection with a
  matching title in a fixture library. Add a Home row for it, reorder the
  rows, switch profiles and reload. Check saved membership and row order and
  that the second profile's layout is unaffected. For collection browsing
  changes, open a populated and an empty library, switch the affected tab or
  filter, and check the result, empty state and any saved selection. For
  paging or save changes, fail one request and verify earlier rows or the
  existing saved value remain available for retry.
- **Reader and audiobooks.** Open two fixture EPUB or CBZ files, leave one
  during a pending search and reopen it at its saved position. Check that a
  late search result cannot update the other book and that progress stays
  with the profile. For audio changes, resume a multipart audiobook in a
  later chapter and verify the displayed chapter, actual position and
  progress after closing the player.

## Accounts, operations, and integrations

- **Accounts and admin settings.** In a test mailbox, open an invite or reset
  link, check the affected account, and try the link a second time. For access
  changes, use two profiles with different library permissions and check both
  an allowed item and a denied item. For settings changes, save a fixture
  value, including explicit false where relevant, and read it back after
  reload. Check the effective behavior and its scope from a second account,
  profile, or device when that scope can change.
- **History imports, requests, and notifications.** Import fixture history
  with a played episode, a resume point and an unmatched episode, then repeat
  it. Check the target profile, counts, dates and unmatched report. When
  changing Plex or Emby pagination, have a disposable provider stub report
  a large total and return one item per request. Check that the importer
  stops at the page cap, reports the limit, and sends no further request.
  For Jellyfin, verify that a listing of full pages with no reported total
  stops at the item cap. For requests, submit a season from a scoped profile,
  approve it with a sandbox router, and compare the queue and catalog after
  polling. Simulate a refusal
  if error handling changed. For notifications, deliver to a test destination,
  verify the addressed profile's inbox and unread count, then check that
  another profile does not receive it.
- **Backup and downloads.** Restore a fixture backup to a separate disposable
  instance and check the selected users or data, the reported restore outcome
  and the state of excluded data. For downloads, prepare an original or a
  transcode with a selected subtitle track. Fetch it, interrupt and resume
  with a Range request, then verify the artifact, byte range and owning
  device. Exercise cancellation or retry if the change reaches those paths.
- **Plugins.** Install a fixture plugin and replace it with another version.
  Check that the existing installation keeps its settings and serves a real
  request from the replacement. For provider configuration or status changes,
  simulate a failed connection check or startup. Compare the displayed status,
  saved configuration and result of a retry.
