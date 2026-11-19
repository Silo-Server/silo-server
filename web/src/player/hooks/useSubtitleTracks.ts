import { useEffect, useRef, useState } from "react";
import {
  decodeVTTText,
  parseVTT,
  type ParsedCue,
  type SubtitleCueAlign,
  type SubtitleCueLine,
} from "../utils/parseVTT";
import type { PlayerSubtitleInfo } from "../types";
import { isASSCodec, isBitmapCodec } from "../utils/subtitleCodecs";
import {
  subtitlePlayheadSeconds,
  subtitleRetryAfterMs,
  subtitleRetryDelayMs,
  subtitleWindowStartForPlayhead,
} from "../utils/subtitleWindows";
import { isSubtitleSourceChanged } from "../utils/subtitleSourceChanged";
import { isPlaybackSessionGoneResponse } from "../playback-errors";

// Explicitly bound each subtitle fetch to this many source-time seconds. A
// cold remote 600 s window takes ~18-24 s to read; the client displays cues as
// they stream, but the long read still holds the relay while the next seek or
// track change is waiting. Two minutes is the text/ASS window; bitmap PGS
// uses a shorter 30 s first slice in usePGSSubtitles. The server's legacy
// implicit window stays at ten minutes for native clients that do not request
// their own slices. The prefetch lead still schedules the next slice before
// playback reaches the end.
const WINDOW_DURATION = 120;
// Start fetching the next window this many seconds before the current
// one's requested end, so the new cues are already on hand by the time
// playback crosses into them.
const PREFETCH_LEAD = 30;
// Overlap consecutive windows by a few seconds so ffmpeg boundary
// rounding can't drop a cue that straddles the join.
const WINDOW_OVERLAP = 5;
// The window grid (cell length and seek backoff) lives in subtitleWindows so
// every consumer of a track snaps to the same start.
// Abort a window fetch when the response goes this long without delivering
// a chunk. Extraction streams cues progressively, so a healthy-but-slow
// ffmpeg keeps resetting the clock; only a genuinely hung one trips it.
// Without this, one hung fetch blocks every future window for the session.
// 60s matches the server relay's first-byte budget (30s) plus headroom for
// a slow-but-progressing Usenet/altmount extraction.
const FETCH_STALL_TIMEOUT_MS = 60_000;
// The backoff ladder after a failed window fetch lives in subtitleWindows
// (subtitleRetryDelayMs): doubling, capped, and raised by the server's
// Retry-After so a refusal it is already recovering from is not asked again too
// early.
// Consecutive window failures tolerated before the fetcher stops scheduling
// retries for this track mount — the same terminal-after-budget policy the
// ASS path (useASSSubtitles) applies to its text fetches. Without it a source
// that fails every extraction (dead provider relay, broken file) keeps one
// ffmpeg spawn alive per backoff window for as long as playback runs. A
// successful window fetch resets the counter; a superseded (seek-replaced)
// fetch never counts.
const FETCH_MAX_CONSECUTIVE_FAILURES = 4;
// Consecutive explicitly-retryable failures tolerated before the fetcher goes
// terminal for this mount. The server marks 408/429/5xx (except 501/505/506,
// which signal unimplemented paths rather than transient strain) as
// retry-after-backoff: burning the 4-deep terminal budget on them turns a
// seconds-long upstream flap into a permanently stuck "Retrying…". These keep
// the same backoff loop on a separate, generous budget so a flap recovers
// while a truly dead source still terminates instead of polling forever.
const FETCH_MAX_RETRYABLE_FAILURES = 12;
// After this many consecutive retryable refusals with no cue on screen the
// mount reports `stalled` while it keeps retrying: the second refusal is the
// one that disproves the server's promise that a budget refusal's retry is
// served from the recovery extraction. See subtitleWindows.ts for the same
// threshold on the windowed (bitmap) path.
const RETRYABLE_STALL_REPORT_FAILURES = 2;
// Coverage end for a whole-track (sidecar/downloaded) fetch when the media
// duration is not known yet: large enough that no playhead can outrun it, so
// the track never looks like it needs a windowed refetch.
const WHOLE_TRACK_SENTINEL = Number.MAX_SAFE_INTEGER;

/** A cue on screen, with the placement its source cue declared. */
export interface ActiveSubtitleCue {
  text: string;
  align?: SubtitleCueAlign;
  line?: SubtitleCueLine;
  position?: number;
  size?: number;
  vertical?: "rl" | "lr";
}

type CuePlacement = Pick<ParsedCue, "align" | "line" | "position" | "size" | "vertical">;

/** True for statuses the server marks retry-after-backoff rather than failed. */
function isRetryableSubtitleStatus(status: number): boolean {
  return status === 408 || status === 429 || status === 502 || status === 503 || status === 504;
}

/**
 * Cues (in source time) and window coverage snapshotted from a track that is
 * being torn down, so a rebuild against a reloaded <video> element (stream
 * restart) can restore them instead of refetching — window extraction costs
 * a multi-second ffmpeg run per request.
 */
interface SubtitleTrackCarryover {
  url: string | null;
  cueRevision: number;
  cues: ParsedCue[];
  coverageStart: number;
  windowEnd: number;
  atEOF: boolean;
  hasFetched: boolean;
  /** Source generation the carried cues were fetched under. */
  sourceGeneration: number;
}

/** Copy the cue's placement onto the VTT cue when the implementation has it. */
function applyCuePlacement(cue: VTTCue, placement: CuePlacement): void {
  if (placement.align) {
    try {
      cue.align = placement.align;
    } catch {
      // Implementations without the cue-setting setters keep the placement in
      // the display object instead.
    }
  }
  if (placement.line) {
    try {
      // A VTT line is a line count while snapToLines is on and a percentage
      // otherwise. Top cues snap to the first line; middle/bottom cues use a
      // percentage of the viewport.
      cue.snapToLines = placement.line === "top";
      cue.line = placement.line === "top" ? 0 : placement.line === "middle" ? 50 : 100;
    } catch {
      // See above.
    }
  }
  if (placement.position !== undefined) {
    try {
      cue.position = placement.position;
    } catch {
      // See above.
    }
  }
  if (placement.size !== undefined) {
    try {
      cue.size = placement.size;
    } catch {
      // See above.
    }
  }
  if (placement.vertical) {
    try {
      cue.vertical = placement.vertical;
    } catch {
      // See above.
    }
  }
}

/** Project a VTT cue onto the display object the overlay renders. */
function toActiveSubtitleCue(
  cue: VTTCue,
  placements: WeakMap<VTTCue, CuePlacement>,
): ActiveSubtitleCue {
  // Parsed VTT text is already decoded. Live cues are normalized once when
  // they enter the track, so do not strip again here: that would delete
  // literal text that came from `&lt;i&gt;`.
  const display: ActiveSubtitleCue = { text: cue.text };
  const placement = placements.get(cue);
  if (!placement) return display;
  if (placement.align) display.align = placement.align;
  if (placement.line) display.line = placement.line;
  if (placement.position !== undefined) display.position = placement.position;
  if (placement.size !== undefined) display.size = placement.size;
  if (placement.vertical) display.vertical = placement.vertical;
  return display;
}

/**
 * Add parsed cues to a TextTrack, applying the stream origin and user delay and
 * deduping against `seen`. Shared by the URL fetcher and the live-cue path.
 */
function addCuesToTrack(
  track: TextTrack,
  cues: ParsedCue[],
  origin: number,
  delaySec: number,
  seen: Set<string>,
  placements: WeakMap<VTTCue, CuePlacement>,
): void {
  for (const parsed of cues) {
    if (parsed.end <= parsed.start) continue;
    const startTime = Math.max(0, parsed.start - origin + delaySec);
    const endTime = parsed.end - origin + delaySec;
    if (endTime <= 0) continue;
    // Key on the source cue times, not the delay-shifted display times: the
    // delay can change between overlapping fetch windows, and a delay-relative
    // key would let the same source cue re-add itself after a shift.
    const key = `${parsed.start}|${parsed.end}|${parsed.text}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const cue = new VTTCue(startTime, endTime, parsed.text);
    applyCuePlacement(cue, parsed);
    placements.set(cue, parsed);
    track.addCue(cue);
  }
}

/**
 * Round a source position down to whole seconds, matching the ASS fetcher.
 * The server keys a window on its exact `position` parameter, so two polls at
 * 100.2 s and 100.8 s must produce the same request to share its cached slice.
 */
function wholeSecondPosition(position: number): number {
  return Math.max(0, Math.floor(position));
}

/**
 * Snap a fetch anchor to the window grid (see subtitleWindowStartForPlayhead).
 * The request starts up to one grid cell behind the playhead, so the current
 * cue stays covered while every position in that cell shares one server
 * window. The math is shared with the bitmap page fetcher so the two grids
 * cannot drift apart.
 */
function alignWindowStart(position: number): number {
  return subtitleWindowStartForPlayhead(position);
}

/** Request the same bounded interval used by the coverage tracker. */
function appendPosition(url: string, position: number): string {
  const sep = url.includes("?") ? "&" : "?";
  return `${url}${sep}position=${wholeSecondPosition(position)}&duration=${WINDOW_DURATION}`;
}

/**
 * Manages subtitle display by delegating cue-against-time matching to the
 * browser's TextTrack, while keeping render control on our side for the
 * subtitle appearance panel.
 *
 * We create a programmatic TextTrack in `hidden` mode, stream WebVTT from
 * the server incrementally, and push each parsed cue as a `VTTCue` onto
 * the track. The browser then fires `cuechange` in sync with the media
 * clock — the same clock that dictates which video frame is on screen —
 * so subtitles display exactly when they should regardless of any PTS
 * normalization hls.js has applied to the underlying segments.
 *
 * A sliding-window fetcher prefetches the next window as playback nears
 * the tail of the current one and aborts in-flight fetches on seeks
 * outside coverage. The fetch scheduling is independent from cue display.
 *
 * `subtitleDelayMs` lets the user nudge sync without a refetch: new cues
 * are added with the current delay baked in, and existing cues are shifted
 * in place whenever the value changes. Positive = show subtitles later.
 * `streamOriginSeconds` changes (a copy-mode session restarting at a new
 * position) are handled the same way: existing cues shift in place onto
 * the new timeline instead of tearing down the track.
 */
export function useSubtitleTracks(
  videoRef: React.RefObject<HTMLVideoElement | null>,
  subtitleUrls: PlayerSubtitleInfo[],
  activeSubtitleIndex: number | null,
  streamOriginSeconds: number,
  subtitleDelayMs: number,
  // Known media duration in source-time seconds (0 when unknown). Used to
  // decide when a fetch window has reached end-of-input.
  durationRef: React.RefObject<number>,
  // Media-time position playback is heading to (pending seek target, or the
  // session's start position). While the element has no media loaded yet
  // (session start or a stream restart), `video.currentTime` still reads 0
  // rather than the resume/seek target — fetching the window at 0 wastes an
  // ffmpeg run and, because only one fetch is in flight at a time, blocks
  // the correct window until it completes.
  fetchAnchorRef: React.RefObject<number>,
  liveCues?: ParsedCue[] | null,
  // Identifies the current live translation job. Changing it (a new job) rebuilds
  // the track and resets the dedup set so cues from a prior run never linger.
  liveTrackKey?: string | null,
  // Bumped whenever the underlying media stream is (re)started — a quality,
  // audio, or subtitle-burn-in switch tears down the <video> element and
  // reloads it. A programmatic TextTrack built moments before that reload is
  // orphaned by the browser (it survives quality switches only because it was
  // built against an already-stable stream). Changing this rebuilds the track
  // against the settled new stream, so selecting a text track in the same
  // action that restarts the transcode (turning off bitmap burn-in) still
  // renders. Initial media loading also bumps it because HLS can clear cues
  // fetched before the first media metadata arrives.
  streamGeneration = 0,
  onLoadState?: (
    state: "idle" | "loading" | "refreshing" | "ready" | "stalled" | "error" | "unavailable",
  ) => void,
  // Fired when the server answers a window fetch with `subtitle_source_changed`
  // (409): a virtual release rotated under this plan, so every URL for the
  // active track is stale. The caller must refresh the plan's subtitle
  // inventory; retrying the same URL can never succeed.
  onSourceChanged?: () => void,
  // The effective subtitle source generation. It survives plan swaps and only
  // changes when the underlying source identity changes, so the marker below
  // can suppress the refresh -> replan -> 409 cycle that a new plan id would
  // otherwise re-arm.
  sourceGeneration = 0,
  // Bumped when the server retimed the active track's content (subtitle sync
  // or a timing reset) behind an unchanged URL. Changing it refetches the cues
  // instead of restoring the carried-over ones.
  cueRevision = 0,
  // Bumped after a `subtitle_source_changed` refresh lands a new plan. The
  // rebuilt effect retries the current window once against the refreshed URL
  // instead of staying parked on the stale one, and re-arms the one-shot
  // source-change signal for the new inventory.
  refreshRevision = 0,
  // Bumped when the engine transport restarts or a replay rebuilds the
  // decoder. Either can replace the media element's cue list or clock without
  // changing the stream/source generations, so the text track must be rebuilt
  // too.
  transportRevision = 0,
  replayRevision = 0,
  // Fired once when a window fetch is answered that the playback session no
  // longer exists (a 410 on a session-scoped subtitle route, or a 404 naming
  // the session). No retry of the same URL can succeed, so the fetcher parks
  // and the caller offers a fresh start instead of polling the dead session.
  onSessionGone?: () => void,
): ActiveSubtitleCue[] {
  const [activeCues, setActiveCues] = useState<ActiveSubtitleCue[]>([]);
  const onLoadStateRef = useRef(onLoadState);
  onLoadStateRef.current = onLoadState;
  const onSourceChangedRef = useRef(onSourceChanged);
  onSourceChangedRef.current = onSourceChanged;
  const onSessionGoneRef = useRef(onSessionGone);
  onSessionGoneRef.current = onSessionGone;
  // The source generation that already emitted a source-changed signal. Held in
  // a ref (unlike the per-effect `sourceChangedSignaled`) so a rebuilt track
  // from the refresh's own replan does not signal the same rotation again.
  const signaledSourceGenerationRef = useRef<number | null>(null);
  // The refresh revision the current build was made for. A bump means the
  // previous source-changed 409 has been answered by a fresh plan, so the
  // rebuilt mount retries once and may signal again.
  const lastRefreshRevisionRef = useRef(refreshRevision);
  // Placement metadata for cues this hook added. VTTCue text alone cannot
  // carry it for implementations (or test fakes) without cue-setting setters.
  const cuePlacementsRef = useRef<WeakMap<VTTCue, CuePlacement>>(new WeakMap());

  // Latest stream origin, readable from stable callbacks (maybeFetch) without
  // retriggering the main effect.
  const streamOriginRef = useRef(streamOriginSeconds);
  streamOriginRef.current = streamOriginSeconds;
  // Origin currently baked into the track's VTTCue times. Cue-add paths read
  // this (not the live origin) so the track never mixes bases; the rebase
  // effect below is the only place it advances, shifting existing cues along.
  const appliedOriginRef = useRef(streamOriginSeconds);

  const activeSub =
    activeSubtitleIndex !== null
      ? (subtitleUrls.find((s) => s.index === activeSubtitleIndex) ?? null)
      : null;
  const activeUrl = activeSub?.url ?? null;
  const activeCodec = activeSub?.codec;
  const activeLang = activeSub?.language ?? "";
  // A live track's cues arrive over the websocket (liveCues) instead of from a
  // URL; the main effect builds the track but skips the sliding-window fetcher.
  const activeIsLive = activeSub?.live === true;
  // Sidecar and downloaded subtitles are complete text artifacts, not windows
  // extracted from the container: fetch them whole once. Only embedded tracks
  // need position/duration slices.
  const activeIsWholeTrack =
    !activeIsLive && (activeSub?.source === "external" || activeSub?.source === "downloaded");

  // Track which delay is currently baked into the VTTCues on the active track,
  // so the delay-update effect below can compute the exact shift to apply.
  // Cue-add paths also read this to keep new cues aligned with existing ones.
  const appliedDelayMsRef = useRef(0);
  const trackRef = useRef<TextTrack | null>(null);
  // Cue dedup set, held in a ref so the live-cue effect and the URL fetcher
  // share it. Reset whenever a fresh track is built.
  const seenCueKeysRef = useRef<Set<string>>(new Set());
  // How many of `liveCues` have already been pushed onto the live track. Lets the
  // live-cue effect add only the new tail each batch instead of rescanning the
  // whole (growing) array. Reset on every track rebuild.
  const processedLiveCuesRef = useRef(0);
  // Snapshot of the previous track's cues and coverage, written by the main
  // effect's cleanup and consumed by the next build when the subtitle URL is
  // unchanged (a rebuild forced by a stream reload rather than a track switch).
  const carryoverRef = useRef<SubtitleTrackCarryover | null>(null);
  // URL of a track the server answered 404 for. A 404 on a subtitle fetch means
  // the track does not exist for this file — an embedded index the plan never
  // probed resolves to nothing — so every window of the same track identity
  // (same URL, hence the same file_id, embedded_stream_index, and track
  // ordinal) answers 404 too. Held in a ref rather than per-mount state so a
  // rebuild that keeps the URL (stream reload, source refresh, retime) stays
  // parked, while a track, version, or session change — each of which mints a
  // new URL — clears the latch as a deliberate fresh attempt.
  const parkedTrackUrlRef = useRef<string | null>(null);

  // A stream reload replaces the <video> element, so a build can land in the
  // gap where the ref is null. `videoRef` is stable, so nothing re-runs the
  // effect when the element arrives, and a build that returned early installed
  // no timeupdate listener either: the track would fetch nothing and never
  // load. Watch for the element while it is missing, then rebuild once.
  const [elementEpoch, setElementEpoch] = useState(0);
  useEffect(() => {
    if (videoRef.current) return;
    let frame = 0;
    let attempts = 0;
    const look = () => {
      if (videoRef.current) {
        setElementEpoch((epoch) => epoch + 1);
        return;
      }
      // Bound the wait to about five seconds of frames: a player that goes
      // away for good must not leave a frame loop running.
      if (++attempts > 300) return;
      frame = requestAnimationFrame(look);
    };
    frame = requestAnimationFrame(look);
    return () => cancelAnimationFrame(frame);
  }, [videoRef, elementEpoch]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    const videoEl: HTMLVideoElement = video;

    // A parked (404) track is parked only for its own identity. Selecting a
    // different track, version, or playback session mints a different URL, so
    // its mount clears the latch and fetches normally; selecting the same one
    // again after switching away is a fresh attempt too, because the mount in
    // between already cleared it. Rebuilds that keep the URL (seek-driven
    // window advance, stream reload, source refresh, retime) leave it parked.
    if (parkedTrackUrlRef.current !== null && parkedTrackUrlRef.current !== activeUrl) {
      parkedTrackUrlRef.current = null;
    }
    // Reads the park latch against the identity this mount fetches for.
    const isParked = () => activeUrl !== null && parkedTrackUrlRef.current === activeUrl;

    // A refresh revision bump means the inventory behind this track was just
    // re-read after a source-changed 409. Consume it here so only the rebuild
    // it caused forces a retry, and re-arm the one-shot signal: this build may
    // legitimately report the next rotation.
    const refreshBumped = lastRefreshRevisionRef.current !== refreshRevision;
    lastRefreshRevisionRef.current = refreshRevision;

    // A retime of the track on screen (same URL, new cue revision) swaps its
    // cues in place: the current ones stay up until the corrected ones arrive.
    const previous = carryoverRef.current;
    const retiming =
      previous !== null &&
      previous.url === activeUrl &&
      previous.cueRevision !== cueRevision &&
      previous.hasFetched &&
      !activeIsLive;
    if (!retiming) {
      setActiveCues([]);
      onLoadStateRef.current?.("idle");
    }

    // Skip entirely for ASS/SSA (JASSUB renders those via useASSSubtitles)
    // and bitmap codecs (PGS/DVD/DVB are burned into the video server-side;
    // rendering text cues for them would double up on screen).
    if (isASSCodec(activeCodec) || isBitmapCodec(activeCodec)) {
      return;
    }
    // Need either a URL to stream from or a live cue source.
    if (!activeUrl && !activeIsLive) {
      return;
    }

    // Programmatic TextTrack. `hidden` mode still maintains activeCues
    // and fires `cuechange` synchronously with the media clock, but
    // suppresses the browser's built-in cue renderer so the appearance
    // panel stays in charge of styling.
    const track = videoEl.addTextTrack("subtitles", "Vio", activeLang || undefined);
    track.mode = "hidden";
    trackRef.current = track;
    seenCueKeysRef.current = new Set();
    processedLiveCuesRef.current = 0;
    // Fresh track has no cues, so it trivially carries the current origin.
    appliedOriginRef.current = streamOriginRef.current;

    // Restore cues and coverage carried over from the previous track when the
    // URL is unchanged — i.e. this rebuild replaces a track orphaned by a
    // stream reload, not a track switch. Cue times were snapshotted in source
    // time and are re-derived against the current origin/delay here, so an
    // origin change across the reload lands correctly. Rebuild dedup keys for
    // every restored cue, including cues newly visible after an origin change.
    const carried = carryoverRef.current;
    carryoverRef.current = null;
    const restored =
      carried &&
      carried.url === activeUrl &&
      carried.sourceGeneration === sourceGeneration &&
      carried.cueRevision === cueRevision &&
      !activeIsLive
        ? carried
        : null;
    // HLS clears every native TextTrack when it attaches a new stream. Keep
    // source cues independently so that cleanup after that clear can still
    // restore the fetched window, rather than marking an empty track covered.
    const sourceCues = new Map<string, ParsedCue>();
    for (const cue of restored?.cues ?? []) {
      sourceCues.set(`${cue.start}|${cue.end}|${cue.text}`, cue);
    }
    if (restored) {
      const origin = appliedOriginRef.current;
      const delaySec = appliedDelayMsRef.current / 1000;
      addCuesToTrack(
        track,
        restored.cues,
        origin,
        delaySec,
        seenCueKeysRef.current,
        cuePlacementsRef.current,
      );
    }
    // While retiming, the previous cues hold the screen as placeholders until
    // the first corrected window replaces them.
    let placeholders = false;
    if (retiming && carried && carried.cues.length > 0) {
      const origin = appliedOriginRef.current;
      const delaySec = appliedDelayMsRef.current / 1000;
      addCuesToTrack(
        track,
        carried.cues,
        origin,
        delaySec,
        seenCueKeysRef.current,
        cuePlacementsRef.current,
      );
      placeholders = true;
    }

    let cancelled = false;
    let hasFetched = restored?.hasFetched ?? false;

    // Sliding-window coverage state. See fetchWindow for semantics.
    let coverageStart = restored?.coverageStart ?? 0;
    let windowEnd = restored?.windowEnd ?? 0;
    let atEOF = restored?.atEOF ?? false;
    let inflight: AbortController | null = null;
    let inflightStart = 0;
    let inflightURL: string | null = null;
    // Set when the playhead moved into a different window while one was still
    // on the wire. Extraction is the expensive part of a subtitle slice, so a
    // request that was already streaming is never cancelled to chase the
    // playhead: it runs to completion, lands in the server's window cache, and
    // the planner runs again afterwards. A deliberate seek still supersedes
    // (see fetchWindow). Re-planning rather than replaying a remembered start
    // is what keeps the retry on the playhead's own window: a position that is
    // now covered issues no request at all.
    let windowRequestedWhileBusy = false;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    if (restored?.hasFetched) onLoadStateRef.current?.("ready");
    // Set on a failed (errored or stalled) window fetch; maybeFetch waits out
    // a short backoff before retrying the uncovered range.
    let lastFetchFailureAt = 0;
    let retryDelay = 0;
    // Set when a window fetch answers 409 subtitle_source_changed: the source
    // rotated under this plan and the URL is stale, so the failure branch must
    // not schedule a backoff retry of a URL that can never succeed.
    let sourceChangedSignaled = false;
    // Set when a window fetch proves the playback session itself is gone. The
    // session is not coming back under this id, so no retry is scheduled and
    // the caller is told once.
    let sessionGoneSignaled = false;
    // Consecutive window failures since the last success (seeks superseding a
    // fetch do not count). Once it reaches FETCH_MAX_CONSECUTIVE_FAILURES the
    // fetcher stops scheduling retries for this mount — same terminal-budget
    // policy as useASSSubtitles — and a later successful fetch resets it.
    let consecutiveFailures = 0;
    // Consecutive explicitly-retryable failures (502/503/504/429/408) since
    // the last success. These ride the same backoff loop on their own generous
    // budget so a transient upstream flap does not spend the terminal budget.
    let retryableFailures = 0;
    // HTTP status of the latest window attempt (0 when the fetch never got a
    // response: network error or stall). Read by the failure branch to route
    // retryable statuses away from the terminal budget.
    let lastResponseStatus = 0;
    // `Retry-After` the latest window attempt carried, in milliseconds. The
    // server raises it for a refusal it is already recovering from (a subtitle
    // page or window-budget answer), so honoring it is what lets the retry land
    // on the recovery work instead of asking again too early.
    let lastResponseRetryAfterMs = 0;

    // True once this mount has told the player that the retryable ladder has
    // stopped being a load worth waiting on. Cleared by the next committed
    // window, so one stall episode reports once.
    let stalledReported = false;

    // Clear the spent failure budget so a deliberate new window request (a
    // seek or a refresh rebuild) gets its own retries instead of inheriting
    // the previous mount's terminal state.
    function resetFailureBudget() {
      consecutiveFailures = 0;
      retryableFailures = 0;
      retryDelay = 0;
      lastFetchFailureAt = 0;
      stalledReported = false;
    }

    // Reads the track's current active cues. The browser drives this through
    // `cuechange`, but adding a cue over a paused playhead does not reliably
    // fire one, so cue-add and player-event paths refresh it by hand too.
    function refreshActiveCues() {
      const active = track.activeCues;
      if (!active || active.length === 0) {
        setActiveCues([]);
        return;
      }
      setActiveCues(
        Array.from(active).map((cue) =>
          toActiveSubtitleCue(cue as VTTCue, cuePlacementsRef.current),
        ),
      );
    }

    function handleCueChange() {
      refreshActiveCues();
    }

    track.addEventListener("cuechange", handleCueChange);
    // Paused playback adds cues without a cuechange, and a seek can change the
    // active cue before the next cuechange fires. A track rebuilt or cleared
    // while paused leaves the display empty, so resuming replays the current
    // cue list instead of waiting for the next cuechange.
    videoEl.addEventListener("pause", refreshActiveCues);
    videoEl.addEventListener("play", refreshActiveCues);
    videoEl.addEventListener("seeking", refreshActiveCues);
    videoEl.addEventListener("seeked", refreshActiveCues);
    refreshActiveCues();

    function clearCues() {
      sourceCues.clear();
      seenCueKeysRef.current.clear();
      const cues = track.cues;
      if (!cues) return;
      // Snapshot first: removeCue mutates the live list, and indexing into
      // a shifting list on each iteration is O(n²).
      for (const cue of Array.from(cues)) {
        track.removeCue(cue);
      }
    }

    function dropPlaceholders() {
      if (!placeholders) return;
      placeholders = false;
      clearCues();
    }

    function addParsedCues(newCues: ParsedCue[]) {
      if (newCues.length === 0) return;
      dropPlaceholders();
      // Cue timestamps come from ffmpeg in source-PTS. For copy-mode HLS
      // the player timeline is rebased to start at `streamOriginSeconds`,
      // so subtract it. For regular transcodes origin is 0 and the
      // subtraction is a no-op. Use the origin already baked into the
      // track (not the live one) so cues added while an origin change is
      // still propagating stay consistent with the existing cues — the
      // rebase effect shifts everything to the new origin in one pass.
      // Any active user-facing sync delay gets baked in here so new cues
      // line up with existing ones.
      for (const cue of newCues) {
        if (cue.end > cue.start) sourceCues.set(`${cue.start}|${cue.end}|${cue.text}`, cue);
      }
      const origin = appliedOriginRef.current;
      const delaySec = appliedDelayMsRef.current / 1000;
      addCuesToTrack(
        track,
        newCues,
        origin,
        delaySec,
        seenCueKeysRef.current,
        cuePlacementsRef.current,
      );
      // Adding a cue over the playhead while paused does not fire `cuechange`.
      refreshActiveCues();
    }

    /**
     * Fetch one slice. `seekStart === null` requests the whole track (sidecar
     * and downloaded text), which has no position/duration parameters and
     * covers the entire media. `supersede` is set only by a deliberate seek:
     * a position the viewer jumped to must not wait behind an extraction,
     * while a playhead that merely drifted is re-planned once the window on the
     * wire settles.
     */
    async function fetchWindow(
      seekStart: number | null,
      resetExisting: boolean,
      supersede = false,
    ) {
      if (!activeUrl) return;

      // A player event burst can ask for the same window two or three times
      // within a few milliseconds (timeupdate plus seeking/seeked). Restarting
      // it aborts a healthy extraction and spawns a second ffmpeg for the same
      // bytes, so coalesce onto the identical in-flight request.
      const url = seekStart === null ? activeUrl : appendPosition(activeUrl, seekStart);
      if (inflight && inflightURL === url) {
        windowRequestedWhileBusy = false;
        if (resetExisting) {
          // Preserve the caller's reset intent without replacing the fetch:
          // the in-flight window already covers this range.
          if (retryTimer !== null) clearTimeout(retryTimer);
          if (!placeholders) clearCues();
          coverageStart = seekStart ?? 0;
          windowEnd = seekStart ?? 0;
          atEOF = false;
          if (!stalledReported) {
            onLoadStateRef.current?.(placeholders ? "refreshing" : "loading");
          }
        }
        return;
      }

      // Only one window on the wire at a time: each fetch keeps an ffmpeg
      // process alive, and a cancelled one throws away the extraction the
      // server had already done. A playhead that moved into a different window
      // is remembered and re-planned once this one settles; only a seek
      // cancels an in-flight window.
      if (inflight && !supersede) {
        windowRequestedWhileBusy = true;
        return;
      }

      inflight?.abort();
      windowRequestedWhileBusy = false;
      const controller = new AbortController();
      inflight = controller;
      inflightStart = seekStart ?? 0;
      inflightURL = url;
      if (retryTimer !== null) clearTimeout(retryTimer);
      if (resetExisting) onLoadStateRef.current?.(placeholders ? "refreshing" : "loading");

      const requestedEnd =
        seekStart === null ? Number.POSITIVE_INFINITY : seekStart + WINDOW_DURATION;
      if (resetExisting) {
        if (!placeholders) clearCues();
        coverageStart = seekStart ?? 0;
        windowEnd = seekStart ?? 0;
        atEOF = false;
      }

      // Abort the fetch if the response stops delivering chunks. Re-armed on
      // every read so a slow-but-progressing extraction is never cut off.
      let stallTimer: ReturnType<typeof setTimeout> | null = null;
      const armStallTimer = () => {
        if (stallTimer !== null) clearTimeout(stallTimer);
        stallTimer = setTimeout(() => controller.abort(), FETCH_STALL_TIMEOUT_MS);
      };

      let succeeded = false;
      lastResponseStatus = 0;
      lastResponseRetryAfterMs = 0;
      // Cues this window actually delivered. A window that streams in whole
      // and carries none is a valid empty window, not a failure.
      let producedCues = 0;
      try {
        armStallTimer();
        const resp = await fetch(url, { signal: controller.signal });
        if (!resp.ok || !resp.body) {
          lastResponseStatus = resp.status;
          // Read before any body probe: those consume the response.
          lastResponseRetryAfterMs = subtitleRetryAfterMs(resp.headers.get("Retry-After"));
          if (await isPlaybackSessionGoneResponse(resp)) {
            // A response that lands after the track was rebuilt belongs to the
            // session this fetch was minted for, not the one on screen now.
            if (!controller.signal.aborted) {
              sessionGoneSignaled = true;
              onLoadStateRef.current?.("error");
              onSessionGoneRef.current?.();
            }
          } else if (await isSubtitleSourceChanged(resp)) {
            sourceChangedSignaled = true;
            // One signal per source generation: the refresh replan adopts a new
            // plan whose rebuilt fetcher would otherwise signal the same
            // rotation again, restarting the cycle.
            if (signaledSourceGenerationRef.current !== sourceGeneration) {
              signaledSourceGenerationRef.current = sourceGeneration;
              onSourceChangedRef.current?.();
            }
            // The stale URL cannot deliver cues, so stop claiming to be
            // loading. The refreshRevision rebuild sets "loading" again when it
            // retries the re-minted URL.
            onLoadStateRef.current?.("error");
          } else if (resp.status === 404) {
            // Terminal: the server says this track does not exist for this
            // file. Every window of the same track identity would answer the
            // same 404, so park the track instead of backing off forever (a
            // window loop with no cues used to re-ask the identical URL
            // indefinitely). Surface "unavailable" so the viewer sees the track
            // cannot load and can turn it off, without labeling it idle or ready.
            if (!controller.signal.aborted) {
              parkedTrackUrlRef.current = activeUrl;
              onLoadStateRef.current?.("unavailable");
            }
          } else {
            // Other non-ok responses (415, 5xx outside the retryable set) fall
            // through to the finally block, which schedules a bounded
            // exponential-backoff retry. A silent return here would let every
            // timeupdate re-trigger the fetch and storm the server.
            console.error(`[useSubtitleTracks] Failed to fetch ${url}: ${resp.status}`);
          }
        } else {
          const reader = resp.body.getReader();
          const decoder = new TextDecoder();
          let buf = "";

          // Split on the last complete cue boundary (blank line) and parse
          // the safe prefix, keep the rest. The WebVTT muxer emits cues
          // terminated by "\n\n".
          while (!cancelled) {
            armStallTimer();
            const { value, done } = await reader.read();
            if (cancelled || controller.signal.aborted || inflight !== controller) return;
            if (done) break;
            buf += decoder.decode(value, { stream: true }).replace(/\r\n/g, "\n");
            const split = buf.lastIndexOf("\n\n");
            if (split < 0) continue;
            const safe = buf.slice(0, split);
            buf = buf.slice(split + 2);
            const cues = parseVTT(safe);
            if (cues.length > 0) {
              producedCues += cues.length;
              addParsedCues(cues);
              onLoadStateRef.current?.("ready");
            }
          }

          // Flush any tail the muxer didn't terminate with a blank line.
          buf += decoder.decode();
          if (buf.trim()) {
            const cues = parseVTT(buf);
            if (cues.length > 0) {
              producedCues += cues.length;
              addParsedCues(cues);
              onLoadStateRef.current?.("ready");
            }
          }

          succeeded = true;
        }
      } catch (err) {
        if ((err as Error).name !== "AbortError") {
          console.error("[useSubtitleTracks] Stream error:", err);
        }
      } finally {
        if (stallTimer !== null) clearTimeout(stallTimer);
        const superseded = inflight !== controller;
        if (inflight === controller) {
          inflight = null;
          inflightURL = null;
        }
        if (succeeded && !cancelled && !superseded) {
          // A corrected window without cues still replaces the placeholders.
          dropPlaceholders();
          onLoadStateRef.current?.("ready");
          hasFetched = true;
          resetFailureBudget();
          if (seekStart === null) {
            // A whole-track fetch carries every cue: the entire media is
            // covered and there is nothing left to prefetch.
            coverageStart = 0;
            const durationSec = durationRef.current ?? 0;
            windowEnd = durationSec > 0 ? durationSec : WHOLE_TRACK_SENTINEL;
            atEOF = true;
          } else {
            // Commit coverage only after the whole window streamed in. A
            // failed or stalled fetch must leave the range uncovered, or the
            // gap would read as fetched and never be retried — subtitles
            // would silently stop for the rest of the window.
            windowEnd = Math.max(windowEnd, requestedEnd);
            // End-of-input only when the window reaches the known media
            // duration. Don't infer it from where the window's cues stop:
            // a window that ends inside a dialogue gap looks identical to
            // end-of-file by that signal, and a false positive here silently
            // stops subtitles for the rest of playback.
            const durationSec = durationRef.current ?? 0;
            if (durationSec > 0 && requestedEnd >= durationSec) {
              atEOF = true;
            }
          }
          // A window that streamed in whole with no cues is valid — a
          // dialogue gap, or a track whose first cue starts later. It must not
          // read as a failure (it does not) and must not leave the rest of the
          // slot unevaluated, so the next window is asked for now instead of
          // when the prefetch lead comes due. Only a fresh window chains:
          // an extend that is empty too leaves the normal lead in charge, so a
          // silent track cannot turn into a fetch loop.
          if (seekStart !== null && !atEOF && producedCues === 0) {
            void fetchWindow(alignWindowStart(requestedEnd - WINDOW_OVERLAP), false);
          } else if (windowRequestedWhileBusy) {
            // Something asked for a different window while this one was on the
            // wire. Plan again against where the playhead is now: if this
            // window covers it, no request is made.
            windowRequestedWhileBusy = false;
            maybeFetch();
          }
        } else if (!succeeded && !superseded && !cancelled) {
          // Genuine failure (error, stall, or non-ok response) rather than a
          // seek superseding this fetch — back off before retrying. A signaled
          // source change is not a retryable failure: the replan will re-mint
          // the URL, so no backoff is scheduled here. Nor is a parked track: a
          // 404 proved the track missing, so no retry can succeed. Guarded
          // inline rather than with a `return` — this block runs inside a
          // finally.
          // A queued window is dropped: the backoff retry re-plans from the
          // playhead, so replaying the old request would only add a request.
          windowRequestedWhileBusy = false;
          if (!sourceChangedSignaled && !sessionGoneSignaled && !isParked()) {
            lastFetchFailureAt = Date.now();
            if (isRetryableSubtitleStatus(lastResponseStatus)) {
              // Explicitly retryable (a page or budget refusal the server is
              // already recovering from, an upstream flap, a rate limit): same
              // backoff loop, but on its own generous budget, and it must not
              // move the player to its terminal state. Playback continues and
              // the track stays selected; nothing on screen is dropped, and a
              // mount that has not produced cues yet keeps claiming to load
              // instead of telling the viewer the track failed.
              retryableFailures += 1;
              if (retryableFailures >= FETCH_MAX_RETRYABLE_FAILURES) {
                // The retry budget is spent. That is the honest failure: the
                // server kept refusing and no further retry is scheduled.
                onLoadStateRef.current?.("error");
                consecutiveFailures = FETCH_MAX_CONSECUTIVE_FAILURES;
              } else if (retryableFailures >= RETRYABLE_STALL_REPORT_FAILURES && !hasFetched) {
                // The server's recovery contract did not hold: the retry that a
                // budget refusal promises is meant to be served from the
                // covering extraction, and it was refused too. Keep retrying in
                // the background, but stop presenting this as a load in progress.
                if (!stalledReported) {
                  stalledReported = true;
                  onLoadStateRef.current?.("stalled");
                }
              } else if (!hasFetched && !stalledReported) {
                onLoadStateRef.current?.(placeholders ? "refreshing" : "loading");
              }
            } else {
              retryableFailures = 0;
              consecutiveFailures += 1;
              onLoadStateRef.current?.("error");
            }
            // On the final failure the budget is spent: schedule no retry and
            // let the spent-budget gate in maybeFetch hold the mount terminal.
            // A later track rebuild (stream restart, source refresh) starts a
            // fresh budget.
            if (consecutiveFailures < FETCH_MAX_CONSECUTIVE_FAILURES) {
              retryDelay = subtitleRetryDelayMs(retryDelay, lastResponseRetryAfterMs);
              retryTimer = setTimeout(maybeFetch, retryDelay);
            }
          }
        }
      }
    }

    // Picks the right fetch action for the current player position:
    //   - no cues yet → initial fetch
    //   - current position fell behind coverageStart (backward seek
    //     outside window) → reset and fetch fresh window from here
    //   - position jumped past windowEnd (forward seek outside window) →
    //     reset and fetch fresh window from here, so the skipped-over gap
    //     is never mistaken for covered range
    //   - playback is nearing windowEnd and we haven't hit EOF → queue
    //     the next window, overlapping slightly with the previous
    // `seeked` marks the calls made from a seeking/seeked event: a seek that
    // leaves the fetched range is a new window request, not a retry of a failed
    // one, so it is allowed to reset the failure budget. It is also the only
    // trigger allowed to cancel a window that is already extracting.
    function maybeFetch(seeked = false) {
      if (sourceChangedSignaled) return; // stale URL; waiting for replan
      if (sessionGoneSignaled) return; // the session is gone; offer a restart
      if (isParked()) return; // the track is missing for this file; stay parked
      if (cancelled) return;
      const mediaTime = subtitlePlayheadSeconds(
        videoEl,
        streamOriginRef.current ?? 0,
        fetchAnchorRef.current,
      );
      // Sidecar/downloaded text is one complete artifact. Fetch it whole once
      // and hold every cue; seeks inside it need no request. A refresh or
      // stream rebuild is a fresh mount and fetches once more. A failed whole
      // fetch keeps the same backoff and terminal budget as a window fetch.
      if (activeIsWholeTrack) {
        if (hasFetched || inflight) return;
        if (lastFetchFailureAt > 0 && Date.now() - lastFetchFailureAt < retryDelay) return;
        if (consecutiveFailures >= FETCH_MAX_CONSECUTIVE_FAILURES) return;
        fetchWindow(null, true);
        return;
      }
      if (inflight) {
        if (
          mediaTime >= Math.min(coverageStart, inflightStart) - 1 &&
          mediaTime <= inflightStart + WINDOW_DURATION + 1
        )
          return;
        // A seek outside the requested range must not wait for extraction; a
        // playhead that merely drifted is queued behind it instead, because
        // cancelling would throw away the extraction already in progress.
        fetchWindow(alignWindowStart(mediaTime), true, seeked);
        return;
      }
      // Ahead of the backoff and terminal gates: a deliberate seek deserves a
      // fresh attempt. Otherwise a dead extraction parks the overlay for the
      // rest of the session even after the viewer jumps somewhere else. With
      // no window loaded yet (the first fetch failed or is still waiting out
      // its backoff), there is no coverage to compare against, so any seek is a
      // new request. The in-flight supersede branch above keeps priority, and
      // timeupdate-driven advances stay on the bounded budget.
      if (seeked) {
        const seekNeedsFreshWindow = !hasFetched
          ? true
          : mediaTime < coverageStart - 1 || mediaTime > windowEnd + 1;
        if (seekNeedsFreshWindow) {
          resetFailureBudget();
          atEOF = false;
          fetchWindow(alignWindowStart(mediaTime), true, true);
          return;
        }
      }
      if (lastFetchFailureAt > 0 && Date.now() - lastFetchFailureAt < retryDelay) return;
      if (consecutiveFailures >= FETCH_MAX_CONSECUTIVE_FAILURES) {
        // The failure budget is spent: the backoff retry and the
        // window-coverage path stop scheduling new fetches. A seek outside an
        // in-flight fetch's range still supersedes it (handled above), an
        // explicit seek into unfetched range resets the budget, and a track
        // rebuild (stream restart, source refresh) starts a fresh budget for
        // its own mount. The last failure already reported "error".
        return;
      }
      if (!hasFetched) {
        fetchWindow(alignWindowStart(mediaTime), true);
        return;
      }
      if (mediaTime < coverageStart - 1) {
        atEOF = false;
        fetchWindow(alignWindowStart(mediaTime), true);
        return;
      }
      if (mediaTime > windowEnd + 1) {
        atEOF = false;
        fetchWindow(alignWindowStart(mediaTime), true);
        return;
      }
      if (!atEOF && mediaTime > windowEnd - PREFETCH_LEAD) {
        const nextStart = alignWindowStart(Math.max(windowEnd - WINDOW_OVERLAP, mediaTime));
        fetchWindow(nextStart, false);
      }
    }

    if (refreshBumped) {
      // The refresh replan answered a source-changed 409. Clear the per-mount
      // stale-URL latch and re-arm the one-shot signal for this inventory: only
      // the outer callback's per-generation dedupe keeps a same-identity
      // rotation from looping.
      sourceChangedSignaled = false;
      signaledSourceGenerationRef.current = null;
      // Retry the current window once against the current URL even when the
      // carried-over coverage says it is already fetched: that coverage was
      // produced by the stale URL the refresh replaced.
      hasFetched = false;
      coverageStart = 0;
      windowEnd = 0;
      atEOF = false;
      resetFailureBudget();
    }

    const handleTimeUpdate = () => maybeFetch();
    const handleSeek = () => maybeFetch(true);

    // URL-backed tracks run the sliding-window fetcher; live tracks receive
    // their cues from the liveCues effect below instead.
    if (!activeIsLive) {
      // Kick off the first window before any player event fires so cues
      // are already in flight for the current position.
      maybeFetch();

      // Cue activation is driven by the browser via `cuechange`; these
      // listeners exist only to keep the sliding-window fetcher scheduled.
      videoEl.addEventListener("timeupdate", handleTimeUpdate);
      videoEl.addEventListener("seeking", handleSeek);
      videoEl.addEventListener("seeked", handleSeek);
    }

    return () => {
      cancelled = true;
      if (retryTimer !== null) clearTimeout(retryTimer);
      inflight?.abort();
      inflight = null;
      videoEl.removeEventListener("timeupdate", handleTimeUpdate);
      videoEl.removeEventListener("seeking", handleSeek);
      videoEl.removeEventListener("seeked", handleSeek);
      videoEl.removeEventListener("pause", refreshActiveCues);
      videoEl.removeEventListener("play", refreshActiveCues);
      videoEl.removeEventListener("seeking", refreshActiveCues);
      videoEl.removeEventListener("seeked", refreshActiveCues);
      track.removeEventListener("cuechange", handleCueChange);
      // Snapshot source cues and coverage independently of native tracks so
      // a rebuild against a reloaded <video> element can restore them without
      // refetching after the HLS controller has cleared the native cue list.
      {
        carryoverRef.current = {
          url: activeUrl,
          cueRevision,
          cues: Array.from(sourceCues.values()),
          coverageStart,
          windowEnd,
          atEOF,
          hasFetched,
          sourceGeneration,
        };
      }
      clearCues();
      // Tracks added via addTextTrack can't be removed from the element;
      // setting `disabled` makes it inert so a subsequent language change
      // cleanly creates a fresh track without stacking live listeners.
      track.mode = "disabled";
      if (trackRef.current === track) {
        trackRef.current = null;
      }
    };
    // `subtitleDelayMs` and `streamOriginSeconds` are intentionally excluded —
    // nudging delay or remapping the timeline must not tear down and refetch
    // the track. The update effects below shift existing cues in place instead.
    // `streamGeneration` IS included: a stream restart reloads the <video>
    // element and orphans the current track, so it must be rebuilt.
    // `sourceGeneration` is included so a genuine source change rebuilds the
    // track; a plan swap that leaves the source identity alone does not.
    // `refreshRevision` rebuilds to retry the re-minted URL after a 409, and
    // `transportRevision`/`replayRevision` rebuild when the engine transport or
    // replay swaps the element's cue list or clock.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- rebuild on source identity, not every plan swap.
  }, [
    activeUrl,
    activeCodec,
    activeLang,
    activeIsLive,
    activeIsWholeTrack,
    liveTrackKey,
    streamGeneration,
    sourceGeneration,
    cueRevision,
    refreshRevision,
    transportRevision,
    replayRevision,
    videoRef,
    elementEpoch,
  ]);

  // Re-base already-loaded cues when the media timeline remaps — e.g. a
  // copy-mode session restarting at a new position after an out-of-window
  // seek changes `streamOriginSeconds`. Cue times are player-local
  // (`source - origin + delay`), so a larger origin moves every cue earlier.
  // Without this shift, cues loaded under the old origin display offset by
  // exactly the origin delta after the restart.
  useEffect(() => {
    const deltaSec = streamOriginSeconds - appliedOriginRef.current;
    appliedOriginRef.current = streamOriginSeconds;
    if (deltaSec === 0) return;
    const cues = trackRef.current?.cues;
    if (!cues) return;
    for (const cue of Array.from(cues)) {
      const vc = cue as VTTCue;
      vc.startTime = Math.max(0, vc.startTime - deltaSec);
      vc.endTime = vc.endTime - deltaSec;
    }
  }, [streamOriginSeconds]);

  // Apply delay changes to already-loaded cues without rebuilding the track.
  // Runs after the main effect, so trackRef is current.
  useEffect(() => {
    const track = trackRef.current;
    if (!track) {
      appliedDelayMsRef.current = subtitleDelayMs;
      return;
    }
    const deltaSec = (subtitleDelayMs - appliedDelayMsRef.current) / 1000;
    appliedDelayMsRef.current = subtitleDelayMs;
    if (deltaSec === 0) return;
    const cues = track.cues;
    if (!cues) return;
    for (const cue of Array.from(cues)) {
      const vc = cue as VTTCue;
      vc.startTime = Math.max(0, vc.startTime + deltaSec);
      vc.endTime = vc.endTime + deltaSec;
    }
  }, [subtitleDelayMs]);

  // Feed websocket-pushed cues into the active live track as they arrive. Only
  // the new tail since the last push is added (liveCues is append-only within a
  // job), so ingestion stays O(batch) rather than O(total) per push. When the
  // job restarts liveCues is replaced with a shorter array, which the length
  // check below detects to start over (the track itself is rebuilt via
  // liveTrackKey, so a fresh seen-set and pointer are already in place).
  useEffect(() => {
    if (!activeIsLive) return;
    const track = trackRef.current;
    if (!track || !liveCues) return;
    if (liveCues.length < processedLiveCuesRef.current) {
      processedLiveCuesRef.current = 0;
    }
    const fresh = liveCues
      .slice(processedLiveCuesRef.current)
      .map((cue) => ({ ...cue, text: decodeVTTText(cue.text) }));
    if (fresh.length === 0) return;
    processedLiveCuesRef.current = liveCues.length;
    const origin = appliedOriginRef.current;
    const delaySec = appliedDelayMsRef.current / 1000;
    addCuesToTrack(
      track,
      fresh,
      origin,
      delaySec,
      seenCueKeysRef.current,
      cuePlacementsRef.current,
    );
    // While paused, adding a cue over the playhead doesn't reliably fire
    // `cuechange`, so refresh the on-screen text by hand. While playing the
    // browser drives `cuechange`, so skip the redundant state update.
    if (videoRef.current?.paused) {
      const active = track.activeCues;
      setActiveCues(
        active && active.length > 0
          ? Array.from(active).map((cue) =>
              toActiveSubtitleCue(cue as VTTCue, cuePlacementsRef.current),
            )
          : [],
      );
    }
  }, [
    liveCues,
    activeIsLive,
    activeSubtitleIndex,
    activeUrl,
    activeCodec,
    activeLang,
    liveTrackKey,
    streamGeneration,
    sourceGeneration,
    cueRevision,
    refreshRevision,
    transportRevision,
    replayRevision,
    videoRef,
  ]);

  return activeCues;
}
