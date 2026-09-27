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
   start, even within 15 seconds of the end of the file, so a short scene
   after the credits keeps its own chapter; only the last chapter's end snaps
   to the end of the file. Chapter credits are authoritative.
2. **Version copy.** Another file of the same episode whose duration is
   within three seconds copies the chapter result of the closest such
   version, keeping its distance from the end of the file (`credits-version-copy:v1`, confidence 0.85). Credits
   that ran to the end of the source run to the end of the copy.
3. **Tail Chromaprint.** Each episode's tail window is fingerprinted once
   and cached, then compared across the season with the intro matcher's
   neighbor search and consensus. Only strong matches are written
   (`credits-audio:v1`): at least two partner episodes must agree, a match
   under 20 seconds must share the season's usual credits duration (within
   three seconds), and the match must reach the end of the file. A shorter
   or earlier shared passage is usually a recurring music cue. A match the
   season agrees on rates 0.90, others 0.65. Boundaries do not snap to
   chapters.

Before any credits marker other than a chapter's is written, it must start
inside the tail window and after the file's intro ends, last 15 to 450
seconds, end by the end of the file, and rate at least 0.55. A preview marker
that starts inside the credits ends them.

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
kinds again.

Automatic contribution to online providers stays intro-only; detected credits
are contributed only on request.

Credits versions and caches:

- Tail fingerprints are `credits_fingerprint` rows in the artifact table.
  Their `config_hash` is `ArtifactConfigHash` of the kind and the tail window
  parameters, so they never share a key with intro fingerprints. A tail with
  no audio is stored `unusable`, and a failed extraction `failed` with
  backoff.
- `CreditsAnalysisConfigHash` keys credits season state, apart from intro
  state. Bump `CreditsBehaviorVersion` to re-run every credits comparison over
  cached fingerprints.
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
