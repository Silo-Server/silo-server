# Intro detection

The daily "Detect markers on this server" task finds intros and end credits in
episodes of series and mixed libraries with marker detection enabled, then end
credits in movies of movie and mixed libraries with it enabled. Code lives in
`internal/intromarkers`; its ffmpeg runs, capability check, and concurrency
limit come from `internal/mediasample` (see [media sampling](media-sampling.md)).
Credits detection is described [below](#credits), and movies
[after it](#movie-credits).

`markers.detect_intros` and `markers.detect_credits` (both default `true`)
choose the kinds, and are read at the start of each run, playback analysis,
and admin request. With intros off, the task skips intro chapters, intro
season groups, and the chapter silence backfill. With credits off, it skips
credits chapters, credits season groups, and movies. Playback analyzes only
the missing kinds that are on and skips files with none left. v2
`redetect-markers` and `refresh-markers` narrow their kinds the same way;
`redetect-markers` answers `409` when none of the requested kinds are on, and
`refresh-markers` does too in `local` mode; in `both` mode the online refresh
still runs. v2 `redetect-intro` and the v1 routes predate the settings and
ignore them. Turning a kind off never deletes its markers.

Every API server runs the task manager, so the task takes a PostgreSQL
advisory lock before it analyzes anything. While one server holds it, a run on
another server succeeds without analyzing and records `skipped` in its result
data. The admin UI does not show that result data yet, so a skipped run
appears there as a completed run. A server whose ffmpeg cannot compute
Chromaprint fingerprints runs its chapter-only episode pass without the lock,
so it never makes a server that can compare season groups skip. It then tries
the lock for the movie pass, which needs no Chromaprint: if it gets the lock
it analyzes movies under it, and if another server holds it, it skips the
movies, which the lock holder analyzes. A single server without Chromaprint
therefore still analyzes movies. With credits off it has no movie pass, so it
stops after the episodes without trying the lock. If ffmpeg's capability listing itself fails,
the task keeps the lock for the whole run. Analysis started from playback or
for a single item does not take the lock.

## Pipeline

1. **Chapters.** A file whose chapters include one titled like an intro or
   opening takes that chapter as its intro (`chapter:v1`). A silence found
   shortly after the chapter end may extend it (`chapter:silence:v2`); the
   extension is capped at five seconds because longer ones mostly overshot into
   the episode. Other files of the same episode with a matching duration copy
   the result (`episode-version-copy:v1`).
2. **Chromaprint.** Remaining files are grouped by library, season, and
   presentation (edition and audio language). Each file's opening audio
   (25 percent of the runtime, at most ten minutes) is fingerprinted once and
   cached. Each file is compared with the next eight episodes in episode order,
   and a file none of them matched with up to 48 more. Matches must last 12
   seconds to 3 minutes.
   A file's intro is the median of the pair results that agree with its
   most-confirmed one (`chromaprint:v4`).

Earlier versions moved a Chromaprint start past subtitle cues in its first
15 seconds (`chromaprint:dialogue:v4`). Most such cues belonged to the opening
itself: song lyrics, often in a translated or karaoke subtitle, title-sequence
narration, or caption credits. The start moved a median 13 to 15 seconds later
and agreed with TheIntroDB and Plex markers far less often. Moving it only past
cues that overlap the start, or begin within half a second after it, still
agreed with TheIntroDB less often than the unrefined start, so
`chromaprint:v4` no longer uses subtitles, and re-analysis replaces
`chromaprint:dialogue:v4` markers under the same-version rule in
`markers.CanWriteMarkerUpdate`.

Chromaprint points summarize a window that starts at the point's timestamp,
so raw matches start and end early. Fixed leads measured against authored
intro chapters move both boundaries later.

Every ffmpeg attempt that detection makes (fingerprints, tail passes, and
silence detection) has a deadline of two minutes plus the running time of
the media it reads, so a ten-minute opening gets twelve minutes. Storage
that can play a file reads it at least as fast as it plays, so a run still
going at its deadline has almost always stalled; a host busy enough to
starve background decoding can also reach it. ffmpeg is killed and the run
fails as a `timeout`, which is not permanent: a fingerprint counts as a
failed extraction and leaves its group partial, a tail pass is stored
`failed` with backoff, and the other files carry on. A tail pass whose
hardware attempt times out tries software on a deadline of its own.

## Confidence

Chromaprint confidence is the rate at which markers of the same kind covered
at least 80 percent of the authored intro chapter in replay:

| Marker | Confidence |
|---|---|
| At least 20 seconds, confirmed by two or more pairs, and within 1.5 seconds of the season's usual intro duration, which at least half the season's episodes share | 0.90 |
| Other markers of at least 20 seconds | 0.65 |
| Shorter than 20 seconds | 0.30 |

Recurring music cues mistaken for intros are short and rarely shared by most
of a season, so they fall in the lower rows. Chapter-based markers keep 0.95,
and silence-extended chapters 0.98. Contribution to online providers compares
these values with each provider's minimum confidence.

### Chapters and online markers

Between online providers, `markers.mergeProviderResults` picks one provider per
kind and keeps every range that provider returned for the kind. Each provider
is ranked by its best marker of that kind, compared with
`markers.mergeCandidate.better`: the highest confidence, then the lower
`fetch_priority`, then the higher submission count, then the provider ID. A
marker without a confidence, or with a non-finite one or one outside 0 to 1,
counts as 0 and loses to any rated one, but is still used when no other
provider has that kind. Confidences are compared as
each provider reports them, on their own scales; a marker below its provider's
`fetch_min_confidence` is left out before the comparison. `selectResults`
applies the current settings to fetched and cached answers alike, so a
settings change applies without a refetch.

Source priority puts online and plugin markers above every local one. The
marker writer, `markers.CanWriteMarkerUpdate`, makes one exception for markers
placed from the file's own chapters (`chapter:v1`, `chapter:silence:v1`,
`chapter:silence:v2`, and `credits-chapter:v1`), which were authored for that
exact file while a provider answers from other copies: between such a marker
and an online one, the higher confidence wins and a tie goes to the online
marker. So an online marker rated below the chapter does not replace it, and
the chapter step replaces an online marker rated below it. Copies from another
version of the episode, Chromaprint, and audio or video credits keep source
priority, since their confidence scale is not comparable with a provider's.

The episode chapter steps, for intros and credits, consider files whose marker
came online and offer the chapter when the writer would take it, judged on the
unrefined chapter, whose confidence silence refinement can only raise. Once
the chapter is stored, a later lookup returning the same online marker leaves
it in place, so neither side replaces the other on the next run. A file whose
online marker arrived first, through the 03:00 sync or lazy playback, gets its
chapter the next time local analysis of that kind covers its episode: the
scheduled run, a re-detection, or a playback or admin refresh that starts local
analysis because a file of the episode lacks a marker. Playback analyzes the
kinds the played file lacks; admin refresh then analyzes every enabled kind.
Both cover every version of the episode. The nightly movie pass does not list
movies whose online credits are a range, so a movie's credits chapter replaces
weaker online credits only on re-detection. It does list movies whose credits
are a provider's absent answer (below), since their bounds are NULL; there a
stronger credits chapter replaces the absent answer on the nightly run, and a
movie without one is skipped before its file is read.

### Absent answers

The host sets `accepts_absent` on every marker fetch, so a provider may answer
that an item has no segment of a kind: a `MarkerSegment` with `absent` set and
no bounds. `markerFromPluginSegment` maps it to a `Marker` with `Absent` set,
never to a range; a segment without bounds would otherwise default to the
whole file. Between providers an absent answer competes for its kind like a
marker, through the same comparator and minimum confidence. A provider that
returns both a range and an absent answer for one kind answered with the
range.

The writer stores an absent answer as a tombstone, the same shape a manual
clear stores: NULL bounds and no ranges, with the provider's source, ID,
algorithm, and confidence. `SegmentPayload.Absent` names that shape. Under
`CanWriteMarkerUpdate` it holds its kind like the provider's marker would: a
manual marker or clear keeps it out, it replaces scanner results, and a
chapter marker and an absent answer are weighed by confidence under the rule
above. With `markers.online_storage` set to `on_demand`, `ApplyResult` and
`OverlayOnline` project it the same way without saving it. A refreshed
provider that no longer gives the answer withdraws the tombstone like any
withdrawn range.

Local analysis treats a provider tombstone as that provider's marker: the
analyzer leaves the kind alone (`Candidate.hasHigherPriority`) unless the
chapter rule lets a chapter replace it, and lazy playback does not ask local
analysis for the kind. Playback, jellycompat, and the APIs read bounds, so a
tombstone is no marker to clients, as a manual clear is.

## Versions and caches

- `AlgorithmVersion` and `Config.ConfigHash` key the fingerprint cache.
  Changing either discards every cached fingerprint, and re-reading the audio
  of a large library takes days. Change them only when the fingerprint itself
  changes. `ConfigHash` covers only the analysis window; the intro duration
  bounds it once included are hashed as fixed legacy values.
- Fingerprints are `intro_fingerprint` rows in the per-file analysis
  artifact table, `media_intro_fingerprints`; see
  [artifact storage](media-sampling.md#artifact-storage) for its keys and
  statuses. Intro detection writes only `complete` rows.
- `AnalysisBehaviorVersion` is part of the season state key. Bump it to
  re-run every season comparison over cached fingerprints.
- Algorithm identifiers are stored with each marker. Between two scanner
  results, `markers.scannerAlgorithmPriority` decides which may replace the
  other before confidence is compared. A new identifier needs a rank above the
  one it supersedes, or re-analysis cannot overwrite what the old version
  wrote.
- `SilenceConfigHash` covers the silence settings. Changing them re-queues
  chapter files the backfill already tried.

## Credits

Credits detection runs beside intro detection in the same task, playback
analysis, and admin refresh. Intro and credits season groups share one work
list, the ffmpeg limit, and the Chromaprint capability check. Each kind is
judged on its own: a file whose intro came from an online provider or a
manual edit can still get local credits, and local analysis never replaces
credits from a higher-priority source, except online credits rated below the
file's credits chapter ([above](#chapters-and-online-markers)).

Credits start in the file's tail window: the last 450 seconds of an episode,
or its last 40 percent when that is shorter. They last 15 to 450 seconds. An
end within 15 seconds of the end of the file becomes the end of the file.

1. **Chapters.** The last run of adjacent chapters titled like credits
   (`Credits`, `End Credits`, `End Titles`, `Outro`, case-sensitive `ED`,
   `ED2`, `ED: …`, or `Ending`) is the credits (`credits-chapter:v1`,
   confidence 0.95), so an `ED` song followed by a `Credits` chapter is one
   segment. Titles that name an intro, a scene around the credits
   (`Post-Credits`, `Mid-Credits`, `After Credits`, `Pre-Credits`), the end
   of the credits (`Credits End`), or a generated `Chapter NN` are not
   credits. Neither is a credits or `Ending` title that names a scene
   (`Credits Scene`, `Stinger`, `Tag`, `Bonus`), though an `ED: …` song
   title may hold those words. A chapter that counts as credits only by
   `Ending` or `Outro` may last at most 180 seconds, in episodes and movies
   alike; a longer one is taken for the story's final scene, so it is not
   credits and does not join a run. The whole run must fit the tail window and
   length limits. The end is the next chapter's start, even within 15
   seconds of the end of the file, so a short scene after the credits keeps
   its own chapter; only a run that ends with the last chapter snaps to the
   end of the file. Chapter credits are authoritative, so they outrank every
   audio and video result. When a file's chapters no longer produce the
   chapter or version-copy credits stored for it, such as after a remux or a
   change to these title rules, analysis withdraws them and analyzes the
   file's season again, even when its stored analysis still stands, so audio
   or video can replace them.
2. **Version copy.** Another file of the same episode whose duration is
   within three seconds copies the chapter result of the closest such
   version, keeping its distance from the end of the file (`credits-version-copy:v1`, confidence 0.85). Credits
   that ran to the end of the source run to the end of the copy.
3. **Tail audio and video.** Each episode's tail is read once and cached: a
   Chromaprint fingerprint of its audio, its silences, and statistics of
   every video keyframe (see [Tail pass](#tail-pass)). The fingerprints are
   compared across the season with the intro matcher's neighbor search and
   consensus; boundaries do not snap to chapters. The audio match and the
   keyframes are then combined per file (see [Combining](#combining)).

Before any credits marker other than a chapter's is written, it must start
inside the tail window and after the file's intro ends, last 15 to 450
seconds, end by the end of the file, and rate at least 0.55. A preview marker
that starts inside the credits ends them.

An episode alone in its season group has no partner to compare audio with;
with the tail pass available it still gets credits from chapters and video.

Playback analysis looks only for the kinds the played file lacks, so an
episode with an intro and no credits runs the credits steps alone. With
`markers.online_storage` set to `on_demand`, the played file includes the
online markers looked up for this playback, which are never saved; every
marker update sent to players during the analysis lays them back over the
stored row, where a manual marker still wins, and leaves out a stored
provider marker the lookup withdrew. Unlike an intro group, a
credits season group whose stored analysis still stands is not compared again
from playback: most episodes have no credits local analysis can find, and
every start would otherwise repeat the comparison. Admin refresh compares both
kinds again, and admin re-detection compares again the kinds it asks for:
the intro, the credits, or both.

### Tail pass

One ffmpeg run per file (`mediasample` `Window` over the tail, keyframes
only) produces the fingerprint when it is not cached, silences of at least
0.5 seconds at -50 dB, and for each video keyframe: the share of pixels
below luma 20, 26, and 32, and luma and saturation statistics, measured on
the center 90 by 80 percent of the picture scaled to 480 pixels wide. Audio
is read in full either way, so the video adds decode time but no reads. The
pass runs only for files whose credits local analysis may write and that
have no chapter credits; other files of the season get an audio-only
fingerprint. A file without audio gets its pass without audio. ffmpeg fails
the whole run when an output finds no stream, so when probe metadata names
audio the file cannot give, the pass runs again on the video alone and the
fingerprint is stored as having no audio. A pass that still finds no stream
lacks video, and an audio-only run decides the fingerprint. If ffmpeg lacks
a filter the pass needs, credits come from chapters and audio alone.

Video decoding dominates the pass's cost. On a host whose `playback.hw_accel`
resolves to VAAPI, QSV, or VideoToolbox (as chapter thumbnails resolve it,
on the `playback.hw_device` devices), the episode and movie tail passes
decode keyframes on the GPU first and in software if that fails (see
[media sampling](media-sampling.md#hardware-decode)). A GPU attempt that
fails because an output finds no stream is not a GPU failure: it logs no
hardware warning and skips software, which would fail the same way, so the
retry on the video alone runs on the GPU. Settings changes apply
to the next pass without a restart. A resolved backend is kept until the
playback probe cache is invalidated; when `auto` finds no hardware, as a
smoke probe that fails under GPU contention reports, it is asked again after
five minutes. On a shared VAAPI host, the episode pass
over a 4K HEVC episode took 12 seconds instead of 54 and about 38 seconds of
CPU instead of 87, audio included; a 1080p H.264 episode took 5 seconds
instead of 12. Tails are cached under the same key whichever decoder read
them.

Each keyframe is classified against the tail's black level, the 1st
percentile of its 10th-percentile luma, capped at 30:

- **Lettered:** a true-black background with text. At least 85 percent of
  the picture is below luma 32, the background is within 2 of the black
  level, saturation is near zero, and at least 75 percent is below a strict
  threshold about four levels above black (20, 26, or 32 by black level),
  with something at least 60 levels brighter. Dark scenes pass the loose
  test but not the strict one.
- **Black:** the same background without text.
- **Card:** a flat background at least 24 levels above black, with text at
  least 60 levels from it and moderate saturation.
- **Mostly black:** passes every lettered test except coverage and contrast, with 70 to 85
  percent below luma 32. Dense columns of names and logos on black look like
  this.
- Anything else is story.

A run starts at a text keyframe (lettered or card), continues over text,
black, and mostly black keyframes no more than 20 seconds apart, and ends at
its last text keyframe. It counts when it spans at least 15 seconds, holds at
least three text keyframes, and text is more than half of its keyframes other
than mostly black ones. Mostly black keyframes only join text: they never
start or end a run, and a start never moves back over them.

### Combining

1. Chapter credits win (step 1).
2. A strong audio match (two or more confirming partners, and at least 20
   seconds or the season's usual duration) grows over runs that overlap it or
   lie within 20 seconds. If the first such run starts up to 60 seconds after
   the match and only story or mostly black keyframes lie between, the
   credits start there: the match often begins on music over the last shot.
   Credits that video moved rate 0.05 higher, up to 0.95
   (`credits-audio:video:v1`); otherwise the match keeps its rating
   (`credits-audio:v1`: 0.90 when the season agrees on its duration, 0.65
   otherwise).
3. A run reaching the end of the file that starts more than 20 seconds after
   an audio match that stops early wins over the match, which is most likely
   a recurring cue.
4. A match one partner confirmed counts only with a run near it (0.65). A
   match no run corroborates must reach the end of the file.
5. Without usable audio, the last cluster of runs is the credits
   (`credits-video:v1`, 0.60 when at least half its text is on black, 0.55
   otherwise). It must end within 120 seconds of the end of the file; an
   earlier run was a dark scene or a title card inside the story. Its start
   moves back over the true-black keyframes just before it, then to the end
   of a silence that lies between the last story keyframe and the credits.
6. An end within 15 seconds of the end of the file moves to it, then the
   guards above apply.

These rules were validated on frame-checked episodes and movies from several
series; every start they placed early was checked against frames and cut no
story.

Automatic contribution to online providers shares detected episode credits as
it does intros, when they meet the provider's minimum confidence. At the default 0.95,
that is chapter credits and season-agreed audio credits that video moved.

Credits versions and caches:

- Tail fingerprints are `credits_fingerprint` rows in the artifact table.
  Their `config_hash` is `mediaartifact.ConfigHash` of the kind and the tail window
  parameters, so they never share a key with intro fingerprints. A tail with
  no audio is stored `unusable`, and a failed extraction `failed` with
  backoff.
- Tail passes are `credits_tail` rows, keyed the same way by the pass
  parameters. Their payload (`credits-tail:v1`) holds an 18-byte record per
  keyframe (offset, the three black shares, and the luma and saturation
  statistics) followed by the silences. A tail is not decoded when the
  file's probe metadata shows no video or an all-intra codec such as ProRes
  or MJPEG. That check runs again on every analysis and is not stored, since
  a probe repair can correct the codec without changing the file; the credits
  season state's input signature covers it too. A tail is stored `unusable`
  after decoding when it has more than 5000 keyframes (`too_many_keyframes`)
  or fewer than one per 30 seconds (`sparse`), and when ffmpeg fails in a way
  the file itself causes (`invalid_data`, `no_stream`). Other failures are
  stored `failed` with backoff.
- `CreditsAnalysisConfigHash` keys credits season state, apart from intro
  state. It covers the fingerprint key and, when the analysis ran tail
  passes, the tail key, so a group settled while ffmpeg could not run tail
  passes is analyzed again once it can. A run without tail passes also skips
  a group a tail-capable run settled, so it cannot replace that run's audio
  and video credits with audio-only ones. Bump `CreditsBehaviorVersion` to
  re-run every credits comparison over cached fingerprints and tails.
- Credits algorithms rank in `markers.scannerAlgorithmPriority` as
  `credits-chapter:v1` (30), `credits-version-copy:v1` (24),
  `credits-audio:video:v1` (22), `credits-audio:v1` (21), and
  `credits-video:v1` (12). Like Chromaprint intros, the latest
  `credits-audio:` result of one version replaces the stored one even at a
  lower confidence.

## Movie credits

Movies get local end credits, never intros, on a best-effort basis. A movie
has no season to compare audio with, so its credits come from chapters and
video alone. In validation, video found credits in 30 of 42 movies with known
credits: 13 within five seconds early to ten seconds late, 13 later than that,
and 4 early, all of which were checked against frames and cut no story. The
web UI labels the library switch "Detect credits markers (best effort)" for
movie libraries and leaves it off for new ones; series and mixed libraries
default to on. Mixed libraries cover both episodes and movies.

A movie file is analyzed when its item is a movie, it is not an episode or an
extra, it is not a part of a multi-part film, it is at least 1200 seconds
long, it is not missing, and its library is enabled, of type movies or mixed,
with marker detection on. Credits from a higher-priority source, such as an
online provider or a manual edit, are left alone, except online credits rated
below the movie's credits chapter, which re-detection replaces
([above](#chapters-and-online-markers)).

- **Chapters.** A credits chapter, by the episode title rules without
  `Ending`, which in a movie is as likely the story's ending, places the
  credits (`credits-chapter:v1`). It needs no ffmpeg. Chapter credits the
  movie's chapters no longer produce are withdrawn, and the tail pass runs
  in their place.
- **Tail pass.** Otherwise the movie's tail window, its last 900 seconds or
  last quarter when that is shorter, is sampled every three seconds: the
  keyframe at or before each time, with the episode tail pass's statistics
  and no audio (a `mediasample` `Samples` request). Thinning episode
  keyframes to one per three or four seconds left their accuracy unchanged,
  and in Matroska, MP4, and AVI files only the sampled stretches are read;
  other containers, such as MPEG-TS, read the whole tail for its keyframes
  (see [media sampling](media-sampling.md)). The result is stored
  as a `credits_tail` artifact with its own `config_hash`, so it never shares
  a key with an episode tail, under the same statuses and backoff. A usable
  tail is stored only after the credits placed from it are written, or none
  are found, so a movie whose analysis is canceled or fails before then
  keeps no complete tail and stays eligible for the next scheduled run.
- **Placing.** Keyframes are classified and grouped into runs as for
  episodes, and the last cluster of runs is the credits (`credits-video:v1`).
  It must end within 180 seconds of the end of the file and last 15 to 900
  seconds. Its start moves back over true black, then to the end of a
  silence between the story and the credits: for movies ffmpeg reads only
  the audio from ten seconds before to two seconds after the start video
  placed, and a silence cut off at the start of that read is not used. The
  silence read is not cached. Guards are the episode guards with the movie
  bounds.

The scheduled run analyzes movies after episodes, under the same cluster
lock and ffmpeg limit. It lists movies whose movie tail is not stored for the
file as it is now (complete, unusable, or failed on this server and still
backing off), newest first; files retried after a failure come last. Once a movie's tail is stored, the run does not
look at it again until the file changes; admin refresh and playback place its
credits again from the stored tail. As for episodes, a tail ruled out by probe
metadata (no video, or an all-intra codec) is decided on every analysis and
never stored, and an unusable row an earlier build stored that way does not
keep a movie out. Such movies, like movies whose credits come from a chapter,
are listed on every run, but analyzing them reads no artifact and runs no
ffmpeg. Once the probe metadata no longer rules the tail out, a failed tail
pass replaces such a row, so the movie backs off like any other failure. The run lists movies 200 at a time, each page starting after the
last movie of the one before in that order, so a movie analyzed in this run
that is still eligible is not listed again in it. The run starts no new
movie, and lists no further page, once 60 minutes have passed since it
started listing movies, and then reports `movie_budget_exhausted`; the
remaining movies wait for the next run. Run summaries count
`movies_considered` (the movies the run listed) and
`movie_credits_markers_written`; the credits tail counters include movies.

Playback analysis of a movie without credits analyzes that file alone, with
playback priority. Admin refresh of a movie analyzes each of its files.

## Contribution

Providers with contribution enabled receive this server's markers through
`markers.ContributionService`: a manual save sends the saved kinds at once,
and the daily "Share detected markers" task (`contribute_markers`) sends the
scanner intros and credits of episodes, not specials or movies, at or above
each provider's minimum confidence to providers with automatic sharing on. Online
and plugin markers are never sent, so a provider's data does not reach
another provider through this server. Automated "nothing found" results are
never sent either: a detector that finds nothing is no evidence of absence.

A manual clear is sent as an absent submission (`absent` set, no times,
source `manual`, algorithm `manual:v1`, confidence 1), only to providers whose
marker capability declares `supports_absent`. Every clear path sends it: the
v1 segment clear (`HandleClearFileSegment`) and the set-markers clear that the
v1 and v2 marker writes share. Its content hash and claim carry NULL times,
which `currentAmendHashSQL` hashes as `ContentHash` does, so the amend check
below matches them against the stored tombstone.

Each attempt first claims a target in `marker_contributions`, keyed by
provider, kind, and `target_key`; a partial unique index allows one active
claim per target. The target key is the item kind, the provider's first
required external ID (or else TMDB, TVDB, then IMDb), and the season and
episode. An active claim blocks every later payload for that target, whatever
times it carries, because most providers keep one submission per account for
each item. A stale in-flight claim, a retryable error, and an `invalid`
refusal older than 30 days free the target.

A provider whose marker capability declares `supports_amend` replaces its
earlier submission instead. For it, the target key is `file:<media_file_id>`,
so each file is its own target. The key names no external ID, so a submission
still in flight when the item's IDs change holds the target, and the next one
waits for it. The content hash also covers the episode's own IDs, the
numbering it follows, the file's hash and size, and the marker's source,
algorithm, and confidence in whole thousandths, so a corrected episode ID, a
file replaced at the same path, a new detection version, or a rescore is a
change. A claim whose
content hash differs from the new one is taken over once it is settled, even
as a `conflict`. Before taking over a target, `Claim` checks under its lock
that the payload's content hash is still the one the file's current state
gives (`currentAmendHashSQL`: the stored marker, the file's duration, hash
and size, and the item's IDs and episode numbering as the resolver reads
them), so an older save, or a run that read
the file before a save, a new detection, a re-probe, or a metadata change,
never replaces a newer payload at the provider.

`Claim` and both daily candidate queries use `currentAmendHashSQL`, which
must hash exactly as `newContributionRow` does; the database contract tests
`TestContributionStoreAmendResubmitsChangedMarkers` and
`TestContributionStoreAmendSendsAnUnsentManualCorrection` check that. The
queries offer a marker again when it no longer matches its active claim.

A save's background contribution tries each provider once and never waits,
and stops at the first rate limit. At a provider that supports amend,
the daily task sends what it did not: a skip behind another submission in
flight, a rate limit or other retryable error, or a run lost when its server
stopped. `CandidateManualCorrections` offers a manual marker of any kind, on
a movie or an episode, when the provider already has a `marker_contributions`
row for that file and segment and none of its active claims there has the
marker's current content hash. A change to the item's IDs therefore sends a
correction again. It offers a manual clear the same way, but only to a
provider that declares `supports_absent`. The run uses
`ContributeOptions.Corrections`, which `Claim` accepts only with such a row
(`ContributionRow.Correction`). It runs for every provider with contribution
enabled that supports amend, whether or not automatic sharing is on, before
the scanner passes. The state is in the database, so this works whichever
server saved. A manual marker or clear at a provider that holds nothing for
the file is never offered. Providers without
`supports_amend` get only scanner markers from the daily task.

A manual save of a kind whose bounds are both within a second of a marker an
online or plugin provider supplied, as the user was shown it, may repeat that
provider's data (`markers.KeepsProviderMarker`, which checks every occurrence
of the kind). The save stores that kind with source `manual` and algorithm
`manual:withheld:v1` instead of `manual:v1`, and no contribution path sends
it: not the save's own run, the daily task, an admin contribution, or an amend
resend, to any provider, including the one whose marker it kept. A save that
keeps an earlier withheld marker stays withheld. A manual clear of a kind
that, as the user was shown it, held an online or plugin provider's absent
answer, or an earlier withheld clear, repeats that answer
(`markers.KeepsProviderAbsence`); its tombstone stores `manual:withheld:v1`
the same way. The comparison uses the stored row, which is what a player is
shown unless online lookups run with `markers.online_storage` set to
`on_demand` (`PopulationService.ShowsStoredMarkers`). In that mode a player is
shown provider answers that each replica caches in its own memory for a while
and never stores, and a provider's answer can change between playback and the
save, so no lookup at save time can say which marker or answer the user saw.
Every kind such a save sets or clears is withheld, except a kind the file
already held as a `manual:v1` marker: no provider marker replaces one, and
since every other save in that mode is withheld, the kind already held it when
any editor still open loaded the file. A withheld marker or a manual clear
does not exempt a save, because an editor opened before the save that wrote it
may still show a provider's marker. Provenance is written with the marker in
one statement, so it survives restarts, races between saves, and changes to
the item's metadata. Everywhere else, including write priority, local
detection, the API, and jellycompat, `manual:withheld:v1` is a manual marker
like `manual:v1`. `/api/v2` reports the stored algorithm; the frozen `/api/v1`
reports `manual:v1` for both.

With stored markers, a save is compared with the row as it is when the save
arrives, not with what the editor showed, which the API does not send. If the
stored kind changes while an editor is open, through a provider refresh or
another user's save, a save that keeps the provider marker or answer the
editor showed can be stored as `manual:v1` and shared. Closing this needs the
save to name the state it edited, such as an `If-Match` precondition on the
`/api/v2` marker write.

Builds from before this rule do not know `manual:withheld:v1`. During a
rolling upgrade from one, an older replica can still send a withheld marker
when an admin contribution of that file reaches it, as it sends its own saves
that keep a provider's marker, until the last older replica is replaced.

The rule exists because TheIntroDB's terms forbid using its data to seed other
databases, so Silo must not pass a provider's marker on as if a person had
found it. It applies to every provider, the one the marker came from
included, so it stays one simple rule.

## Measuring accuracy

Files with authored intro chapters are ground truth for the Chromaprint path.
Cached fingerprints for those files can be replayed through
`CompareFingerprints` offline, with chapters removed so snapping cannot read
the answer. Report the share of files whose start and end both fall within one
and three seconds of the chapter bounds, and the median and 90th percentile
error per boundary. Evaluate a matcher change this way before changing
constants.

TheIntroDB's markers are a second reference. Look up a sample of episodes with
the plugin's own client, so the request matches what the server sends, and
compare them with a replay over cached fingerprints rather than stored
markers, which a provider marker replaces. Count only answers for a release
within two seconds of the file's duration, since other releases shift the
timings, and report entries with a confidence of at least 0.5 and two or more
submissions apart: single submissions are often wrong, and TheIntroDB credits
include anime openings, which `CheckCreditsPlacement` rejects. On 162 such
entries, almost all anime with chapter-snapped starts, intros without subtitle
refinement had both boundaries within three seconds for 93 percent of files.
Plex's own markers for the same media files are a third reference, with
different coverage: on 13,211
replayed files from a mostly non-anime library, 69 percent were within three
seconds and 90 percent within five; Plex intros end about 1.7 seconds earlier.
