# Intro detection

The daily "Detect markers on this server" task finds intros in series libraries
with marker detection enabled. Code lives in `internal/intromarkers`.

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
- `AnalysisBehaviorVersion` is part of the season state key. Bump it to
  re-run every season comparison over cached fingerprints.
- Algorithm identifiers are stored with each marker. Between two scanner
  results, `markers.scannerAlgorithmPriority` decides which may replace the
  other before confidence is compared. A new identifier needs a rank above the
  one it supersedes, or re-analysis cannot overwrite what the old version
  wrote.
- `SilenceConfigHash` covers the silence settings. Changing them re-queues
  chapter files the backfill already tried.

## Measuring accuracy

Files with authored intro chapters are ground truth for the Chromaprint path.
Cached fingerprints for those files can be replayed through
`CompareFingerprints` offline, with chapters removed so snapping cannot read
the answer. Report the share of files whose start and end both fall within one
and three seconds of the chapter bounds, and the median and 90th percentile
error per boundary. Evaluate a matcher change this way before changing
constants.
