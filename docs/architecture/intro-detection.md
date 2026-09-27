# Intro detection

The daily "Detect markers on this server" task finds intros and end credits in
series libraries with marker detection enabled. Code lives in
`internal/intromarkers`; its ffmpeg runs, capability check, and concurrency
limit come from `internal/mediasample` (see [media sampling](media-sampling.md)).
Credits detection is described [below](#credits).

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
3. **Dialogue.** When an external dialogue subtitle overlaps the first seconds
   of a Chromaprint intro, the start moves to the end of that dialogue
   (`chromaprint:dialogue:v4`).

Chromaprint points summarize a window that starts at the point's timestamp,
so raw matches start and end early. Fixed leads measured against authored
intro chapters move both boundaries back.

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
credits from a higher-priority source.

Credits start in the file's tail window: the last 450 seconds of an episode,
or its last 40 percent when that is shorter. They last 15 to 450 seconds. An
end within 15 seconds of the end of the file becomes the end of the file.

1. **Chapters.** The last chapter titled like credits (`Credits`,
   `End Credits`, `End Titles`, `Outro`, case-sensitive `ED`, `ED2`, `ED: …`,
   or `Ending`) is the credits (`credits-chapter:v1`, confidence 0.95).
   Titles that name an intro, a scene around the credits (`Post-Credits`,
   `Mid-Credits`, `After Credits`, `Pre-Credits`), the end of the credits
   (`Credits End`), or a generated `Chapter NN` are not credits, and neither
   is a match whose neighbor also matches. The end is the next chapter's
   start. Chapter credits are authoritative.
2. **Version copy.** Another file of the same episode whose duration is
   within three seconds copies the chapter result, keeping its distance from
   the end of the file (`credits-version-copy:v1`, confidence 0.85). Credits
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
episode with an intro and no credits runs the credits steps alone. Unlike an
intro group, a credits season group whose stored analysis still stands is not
compared again from playback: most episodes have no credits local analysis
can find, and every start would otherwise repeat the comparison. Admin
refresh compares both kinds again.

### Tail pass

One ffmpeg run per file (`mediasample` `Window` over the tail, keyframes
only) produces the fingerprint when it is not cached, silences of at least
0.5 seconds at -50 dB, and for each video keyframe: the share of pixels
below luma 20, 26, and 32, and luma and saturation statistics, measured on
the center 90 by 80 percent of the picture scaled to 480 pixels wide. Audio
is read in full either way, so the video adds decode time but no reads. The
pass runs only for files whose credits local analysis may write and that
have no chapter credits; other files of the season get an audio-only
fingerprint. A file without audio gets its pass without audio. If ffmpeg
lacks a filter the pass needs, credits come from chapters and audio alone.

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
- **Mostly black:** passes every lettered test except coverage, with 70 to 85
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

Automatic contribution to online providers stays intro-only; detected credits
are contributed only on request.

Credits versions and caches:

- Tail fingerprints are `credits_fingerprint` rows in the artifact table.
  Their `config_hash` is `ArtifactConfigHash` of the kind and the tail window
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

## Measuring accuracy

Files with authored intro chapters are ground truth for the Chromaprint path.
Cached fingerprints for those files can be replayed through
`CompareFingerprints` offline, with chapters removed so snapping cannot read
the answer. Report the share of files whose start and end both fall within one
and three seconds of the chapter bounds, and the median and 90th percentile
error per boundary. Evaluate a matcher change this way before changing
constants.
