import { useEffect, useRef, useState } from "react";
import type JASSUB from "jassub";
import type { PlayerSubtitleInfo, VideoFitMode } from "../types";
import { isASSCodec } from "../utils/subtitleCodecs";
import { isSubtitleSourceChanged } from "../utils/subtitleSourceChanged";
import { subtitleRetryDelayMs, subtitleRetryAfterMs } from "../utils/subtitleWindows";
import {
  applyASSMarginInset,
  coverZoom,
  NO_ASS_MARGIN_INSET,
  NO_COVER_CROP,
  resolveASSMarginInset,
  sameASSMarginInset,
  type ASSMarginInset,
  type CoverCrop,
} from "../utils/assFillMargins";
import {
  fallbackFontForSubtitle,
  forceASSFontFamily,
  loadSubtitleFontBundleResult,
  loadSubtitleFallbackFontData,
  type SubtitleFontBundleResult,
} from "../utils/subtitleFonts";
// Liberation Sans (SIL OFL 1.1; license colocated as liberation-sans.LICENSE),
// the font JASSUB uses as its built-in Latin default, taken verbatim from
// jassub@2.4.2's dist/default.woff2. Vendored because jassub >= 2.5.4 still
// references that file but no longer ships it in the npm package, which would
// leave libass with no usable default font (queryFonts is disabled) and
// silently render nothing.
import liberationSansUrl from "../assets/liberation-sans.woff2?url";

// A font bundle extraction (up to 32MiB, server-side, over network-backed or
// virtual storage) is allowed this much time before JASSUB is constructed
// without it. Subtitles render with the fallback/default fonts meanwhile; the
// losing fetch keeps running in the background and lands in the shared
// fontBundleCache, so a later re-select or the prefetch path picks up the real
// fonts. The subtitle TEXT is never gated by this budget.
const FONT_BUNDLE_BUDGET_MS = 3000;

// Explicitly bound each ASS fetch to this many source-time seconds. JASSUB
// cannot render anything until the whole script response arrives, so a cold
// remote 600 s slice keeps the viewer waiting ~18-24 s before the first line
// appears. A two-minute slice reads proportionally less of the container and
// starts in a few seconds; the prefetch below keeps the next slice ready.
const ASS_WINDOW_DURATION_SECONDS = 120;
// Start a window this many seconds before the playhead so the cue currently
// on screen is inside the requested interval rather than cut off at its start.
const ASS_WINDOW_LEAD_SECONDS = 20;
// Fetch the next window once playback comes this close to the current one's
// end. The replacement window restarts `ASS_WINDOW_LEAD_SECONDS` behind the
// playhead, so it overlaps the outgoing window and the swap drops no cues.
const ASS_WINDOW_PREFETCH_LEAD_SECONDS = 60;
// The prefetch lead is source-time seconds, so at 2x/4x the fixed 60 s above is
// only 30 s/15 s of wall clock and a cold extraction can miss the window
// boundary. Scale it with the playback rate, but cap it below the window
// duration: at 4x the raw scaled lead (240 s) exceeds a whole 120 s window, so
// the next page would be requested the instant the current one commits. 90 s
// keeps at most one prefetch per window while still leaving 22.5 s of wall
// clock at 4x.
const ASS_WINDOW_MAX_PREFETCH_LEAD_SECONDS = 90;
// A forward seek whose computed window start is within this many seconds of
// the attempt already in flight is covered by that request and must not
// restart it. The paired seeking/seeked events (and a playhead that advances
// during the same seek) can otherwise shift the start by a second and abort a
// multi-second extraction that already covers the new position. A backward
// shift is deliberately excluded: the in-flight window starts after it, so the
// cues just behind the new playhead would be missing.
const ASS_WINDOW_REANCHOR_HYSTERESIS_SECONDS = 2;
// A windowed extraction is seconds of work, not a whole-track 100s: allow this
// long without a chunk before abandoning the attempt. Covers a cold ffmpeg
// start plus a slow-but-progressing extraction.
const ASS_STALL_TIMEOUT_MS = 60_000;
// Consecutive windowed failures tolerated before falling back to the
// param-less whole-track URL once.
const ASS_WINDOW_MAX_ATTEMPTS = 3;
// Retry backoff after a failed windowed attempt is the shared 5 s floor from
// subtitleWindows (subtitleRetryDelayMs), raised by the server's Retry-After when
// it asks for longer and capped so a long delay cannot park the retry.
// Hard ceiling on consecutive subtitle TEXT fetch failures for one mount,
// counting the windowed attempts and the whole-track fallback together. The
// per-position windowed budget resets on a seek so a new position gets a fair
// try, which lets a release that errors every request be re-fetched forever as
// playback re-anchors; this counter does not reset on a seek. Once it is spent
// the pipeline latches terminal.
const ASS_TEXT_MAX_FAILURES = 4;
// Generous ceiling for explicitly-retryable TEXT failures (502/503/504/429/
// 408) on the same backoff loop. A transient upstream flap must not spend the
// 4-deep terminal budget above, but a source that never recovers still
// terminates instead of polling forever.
const ASS_RETRYABLE_TEXT_MAX_FAILURES = 12;
// Two consecutive retryable refusals with no committed script: the first is a
// budget refusal whose retry the server promises to serve from the recovery
// extraction, so a second one means that promise did not hold. Report the track
// as stalled and keep retrying in the background.
const ASS_RETRYABLE_TEXT_STALL_REPORT_FAILURES = 2;
// The server's implicit window for a param-less subtitle request against a
// cold large/unknown remote source: 600 s from just behind the session
// position, flagged with the windowed headers. A param-less fallback that
// still reports a window is therefore not a whole track and must keep the
// refresh loop armed, unlike a complete (header-less) body.
const ASS_FALLBACK_WINDOW_SECONDS = 600;
// Artifact-aware upgrade: once a window is on screen, a param-less probe can
// discover that the server has since committed the full-track artifact (the
// warm it starts behind a windowed serve). The first probe waits this long so
// first paint and the first prefetch are never contended.
const ASS_UPGRADE_PROBE_FIRST_DELAY_MS = 30_000;
// At most one probe per this interval afterwards: each one is a param-less
// extraction the server may have to serve, so it is not free.
const ASS_UPGRADE_PROBE_INTERVAL_MS = 60_000;
// Ceiling for the probe's backoff when the server keeps answering 5xx or an
// explicitly-retryable status. The working window pipeline is untouched.
const ASS_UPGRADE_PROBE_MAX_INTERVAL_MS = 5 * 60_000;

/**
 * Whether an ASS response holds the complete track. Every bounded window
 * carries `X-Subtitle-Windowed: true`; a whole-track body (local or small
 * source, committed full-track artifact, or a codec that cannot be sliced)
 * omits it. The server exposes both markers cross-origin for this check.
 */
function isWholeTrackResponse(response: Response): boolean {
  return !response.headers.get("X-Subtitle-Windowed");
}

/**
 * Source-time range advertised by `X-Subtitle-Coverage` (`<start>-<end>`, or
 * `<start>-*` for an open end), or null when the header is absent or
 * unparseable. An open end has no finite end, so it is not a range a caller can
 * treat as a loaded window.
 */
function subtitleCoverageRange(response: Response): { start: number; end: number } | null {
  const coverage = response.headers.get("X-Subtitle-Coverage");
  if (!coverage) return null;
  const [startText, endText] = coverage.split("-");
  const start = Number.parseFloat(startText ?? "");
  const end = Number.parseFloat(endText ?? "");
  if (!Number.isFinite(start) || start < 0) return null;
  if (!Number.isFinite(end) || end <= start) return null;
  return { start, end };
}

/** True for statuses the server marks retry-after-backoff rather than failed. */
function isRetryableSubtitleStatus(status: number): boolean {
  return status === 408 || status === 429 || status === 502 || status === 503 || status === 504;
}

/** A non-OK subtitle response, carrying the status so the retry policy can
 * distinguish a definitive 4xx from a possibly-transient failure. */
class SubtitleFetchError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** `Retry-After` the response carried, in milliseconds (0 when absent). */
    readonly retryAfterMs = 0,
  ) {
    super(message);
    this.name = "SubtitleFetchError";
  }
}

/** True when a status cannot be satisfied by retrying the same URL. */
function isDefinitiveSubtitleStatus(status: number): boolean {
  // 409 is resolved by a replan, not a retry, and is handled before this.
  return status >= 400 && status < 500 && status !== 409;
}
// Bounded background refresh of a font bundle the server reported as pending
// (empty bundle, extraction still running). It stops as soon as fonts arrive
// or a definitive font-less bundle is reported, so it can never poll forever.
//
// The budget must outlive the server's dump: the dump is payload-scaled from a
// 30 s floor up to a 4 m ceiling at a conservative relay rate, so a client that
// stopped after the old three attempts (~50 s) abandoned exactly the slow packs
// the server had been given room to extract. Twelve capped attempts span about
// 5.3 minutes; a committed window additionally re-arms one poll (see
// rearmFontRefreshAfterWindowCommit) so a bundle landing even later is adopted.
const ASS_FONT_REFRESH_MAX_ATTEMPTS = 12;
// Backoff between pending-font refresh attempts. The first retry stays fast
// (the extraction usually lands within seconds); each consecutive pending
// answer multiplies it so a slow relay is not hammered while the server-side
// warm is still streaming the same bundle.
const ASS_FONT_REFRESH_BACKOFF_MS = 5_000;
const ASS_FONT_REFRESH_BACKOFF_FACTOR = 3;
const ASS_FONT_REFRESH_MAX_BACKOFF_MS = 30_000;
// Bounded window-commit re-arms after the base poll budget is spent. The
// server's dump ceiling is four minutes, so one window advance is enough to
// adopt a bundle that landed just after the budget. Never re-arm forever: a
// source whose extraction keeps timing out would otherwise poll for the life
// of the track.
const ASS_FONT_REFRESH_MAX_WINDOW_REARMS = 1;

/** `url` with the bounded window query the VTT path also uses. */
function appendSubtitleWindow(url: string, position: number): string {
  const sep = url.includes("?") ? "&" : "?";
  return `${url}${sep}position=${Math.max(0, Math.floor(position))}&duration=${ASS_WINDOW_DURATION_SECONDS}`;
}

/**
 * Fetches an ASS font bundle but abandons it at a time budget. On a budget miss
 * the in-flight fetch continues in the background (warming the shared cache)
 * and this resolves with no attached fonts so subtitle appearance is never
 * delayed. Errors degrade to [] exactly like the old parallel Promise.all.
 */
async function loadSubtitleFontBundleWithinBudget(
  url: string,
  signal: AbortSignal,
  onSourceChanged?: () => void,
): Promise<SubtitleFontBundleResult> {
  const fontPromise = loadSubtitleFontBundleResult(url, signal, onSourceChanged);
  let budgetTimer: ReturnType<typeof setTimeout> | null = null;
  const budgetMiss = new Promise<SubtitleFontBundleResult>((resolve) => {
    // A budget miss means the bytes have not arrived yet, not that the track
    // has none: mark it pending so a bounded background refresh adopts them
    // into the live renderer when the extraction completes.
    budgetTimer = setTimeout(() => resolve({ fonts: [], pending: true }), FONT_BUNDLE_BUDGET_MS);
  });
  try {
    const result = await Promise.race([fontPromise, budgetMiss]);
    // The race is settled on the font result; never leave the budget timer
    // dangling to fire into a settled pipeline.
    if (budgetTimer !== null) clearTimeout(budgetTimer);
    return result;
  } catch (err) {
    if (budgetTimer !== null) clearTimeout(budgetTimer);
    if ((err as Error).name === "AbortError") {
      return { fonts: [], pending: false };
    }
    console.error(`[useASSSubtitles] Failed to load subtitle font bundle ${url}:`, err);
    // A transient failure is retryable, independently of whether the server
    // marked the bundle pending.
    return { fonts: [], pending: true };
  }
}
// Window drags and fullscreen transitions resize the player many times in a
// row; reload the track once the crop settles rather than on every frame.
const FILL_MARGIN_DEBOUNCE_MS = 150;

interface ASSFillState {
  instance: JASSUB;
  /** Track content before any Fill margin inset. */
  baseContent: string;
  /** The fallback font family forced onto the script, if one was. */
  fallbackFamily?: string;
  /** Inset currently loaded into the renderer. */
  inset: ASSMarginInset;
  /** JASSUB's own render-resolution settings, restored in Fit. */
  prescaleFactor: number;
  prescaleHeightLimit: number;
}

function fillInset(content: string, videoFit: VideoFitMode, coverCrop: CoverCrop) {
  return videoFit === "cover" ? resolveASSMarginInset(content, coverCrop) : NO_ASS_MARGIN_INSET;
}

/**
 * Whether the renderer's video has a frame size to resize against. JASSUB
 * sizes its canvas from the video's intrinsic size, which is 0×0 while a
 * stream reload (seek reanchor, quality or audio switch) has emptied the
 * element. Resizing then leaves libass drawing into a 0×0 frame, and JASSUB
 * resizes again only when the frame size changes, which a reloaded stream at
 * the same resolution never does.
 */
function hasFrameSize(instance: JASSUB): boolean {
  const video = instance._video;
  return !!video && video.videoWidth > 0 && video.videoHeight > 0;
}

/** Resizes and repaints the renderer once it is ready and has a frame size. */
function repaintASS(instance: JASSUB, isCurrent: () => boolean): void {
  void instance.ready
    .then(() => {
      if (isCurrent() && hasFrameSize(instance)) return instance.resize(true);
    })
    .catch((err) => {
      if (isCurrent()) console.error("[useASSSubtitles] Unable to repaint subtitles:", err);
    });
}

/**
 * Puts the canvas on the video's Fit/Fill crop, reloads the track with margins
 * that keep regular events inside the visible area, and repaints. JASSUB sizes
 * its canvas as if the video always used object-fit: contain, so Fill relies on
 * the `player-ass-fill` override to crop the canvas exactly like the video and
 * raises the render resolution by the zoom so the enlarged bitmap stays sharp.
 */
async function syncASSFill(
  fill: ASSFillState,
  videoFit: VideoFitMode,
  coverCrop: CoverCrop,
  isCurrent: () => boolean,
): Promise<void> {
  const { instance } = fill;
  instance._canvas.classList.toggle("player-ass-fill", videoFit === "cover");
  if (videoFit === "cover") {
    instance.prescaleFactor = coverZoom(coverCrop);
    instance.prescaleHeightLimit = Number.POSITIVE_INFINITY;
  } else {
    instance.prescaleFactor = fill.prescaleFactor;
    instance.prescaleHeightLimit = fill.prescaleHeightLimit;
  }
  const inset = fillInset(fill.baseContent, videoFit, coverCrop);
  if (!sameASSMarginInset(inset, fill.inset)) {
    fill.inset = inset;
    await instance.renderer.setTrack(applyASSMarginInset(fill.baseContent, inset));
  }
  if (isCurrent() && hasFrameSize(instance)) await instance.resize(true);
}

/**
 * Manages client-side ASS/SSA subtitle rendering via JASSUB (libass WASM).
 *
 * When an ASS-codec subtitle track is active, this hook lazy-loads JASSUB,
 * creates an instance attached to the video element, and renders styled
 * subtitles onto a canvas overlay. When a non-ASS track is selected (or
 * subtitles are turned off), the JASSUB instance is destroyed.
 *
 * The existing VTT subtitle pipeline (useSubtitleTracks) handles SRT/VTT;
 * this hook handles ASS/SSA. The two are coordinated by the `isActive`
 * return value — when true, the VTT overlay should be suppressed.
 */
export function useASSSubtitles(
  videoRef: React.RefObject<HTMLVideoElement | null>,
  subtitleUrls: PlayerSubtitleInfo[],
  activeSubtitleIndex: number | null,
  isDetached: boolean,
  streamOriginSeconds: number,
  subtitleDelayMs: number,
  // "refreshing" is a reload of the track on screen for new timing: its
  // current events stay up meanwhile, so it is not announced as loading.
  onLoadState?: (state: "idle" | "loading" | "refreshing" | "ready" | "stalled" | "error") => void,
  // Fired when the server answers the subtitle fetch with
  // `subtitle_source_changed` (409): a virtual release rotated under this plan
  // and every URL for the active track is stale. The caller must refresh the
  // plan's subtitle inventory; retrying the same URL can never succeed.
  onSourceChanged?: () => void,
  // The effective subtitle source generation. It survives plan swaps and only
  // changes when the underlying source identity changes, so a subtitle-only
  // replan does not tear down JASSUB and immediately refetch a possibly-still-
  // stale URL. A genuine source change still rebuilds.
  sourceGeneration = 0,
  videoFit: VideoFitMode = "contain",
  coverCrop: CoverCrop = NO_COVER_CROP,
  // Bumped when the server retimed the active track behind an unchanged URL
  // (subtitle sync or a timing reset); changing it swaps the track's script
  // in the running renderer.
  cueRevision = 0,
  // Bumped when the browser decode engine's transport starts or stops. JASSUB
  // re-arms requestVideoFrameCallback only from inside its own callback, and
  // callbacks registered on an engine end with it, so the renderer rebuilds.
  transportRevision = 0,
  // Bumped when a new playback session starts at the beginning. Unlike a
  // transport rebuild, a replay can reuse the same URL and source generation,
  // so it needs an explicit signal to discard the previous playhead's window.
  replayRevision = 0,
): { isActive: boolean } {
  const onLoadStateRef = useRef(onLoadState);
  onLoadStateRef.current = onLoadState;
  const onSourceChangedRef = useRef(onSourceChanged);
  onSourceChangedRef.current = onSourceChanged;
  const videoFitRef = useRef(videoFit);
  videoFitRef.current = videoFit;
  const coverCropRef = useRef(coverCrop);
  coverCropRef.current = coverCrop;

  const jassubRef = useRef<JASSUB | null>(null);
  const fillRef = useRef<ASSFillState | null>(null);
  // The window a run committed, with the track identity it was loaded for, so
  // a renderer rebuild can keep it. The video element can briefly report
  // currentTime 0 while a replacement stream attaches; without this, the
  // rebuild starts a window at 0 and then races the playhead at the real
  // position. Written only on success and consumed by the next effect run,
  // which clears it first: a run that merely starts, or that fails before
  // committing, is not eligible to donate a window to a later rebuild.
  const committedWindowRef = useRef<{
    url: string;
    sourceGeneration: number;
    replayRevision: number;
    start: number;
  } | null>(null);
  const syncedFitRef = useRef(videoFit);
  const jassubImportRef = useRef<Promise<typeof JASSUB> | null>(null);
  // The track and cue revision the running renderer was built from, and
  // whether it finished loading: a retime swaps the script only then.
  const cueRevisionRef = useRef(cueRevision);
  cueRevisionRef.current = cueRevision;
  const builtRef = useRef<{ url: string; revision: number; ready: boolean } | null>(null);
  // Bumped to rebuild the renderer when a script swap cannot be made.
  const [rebuild, setRebuild] = useState(0);
  // Effective JASSUB time offset. JASSUB renders the ASS event matching
  // `video.currentTime + timeOffset`, so an event at source time S appears
  // at video time S - timeOffset. `streamOriginSeconds` accounts for HLS
  // PTS rebasing; the user-facing delay (ms → s) must be SUBTRACTED so that
  // positive delay = subtitles shown later, matching the VTT path's
  // `start - origin + delay` cue shift.
  const effectiveOffset = streamOriginSeconds - subtitleDelayMs / 1000;
  const streamOriginRef = useRef(effectiveOffset);
  streamOriginRef.current = effectiveOffset;
  // Raw stream origin (without the user sync delay). The player clock plus
  // this is the source time a window request must be anchored to; the delay
  // only shifts rendering and must not shift the requested source interval.
  const sourceOriginRef = useRef(streamOriginSeconds);
  sourceOriginRef.current = streamOriginSeconds;

  // Resolve the active subtitle track.
  const activeSub =
    activeSubtitleIndex !== null
      ? (subtitleUrls.find((s) => s.index === activeSubtitleIndex) ?? null)
      : null;

  const isASS = activeSub !== null && isASSCodec(activeSub.codec);
  const activeUrl = isASS ? activeSub.url : null;
  const activeLanguage = isASS ? activeSub.language : "";
  const activeFontBundleUrl = isASS ? activeSub.font_bundle_url : undefined;

  // Main effect: create/destroy JASSUB based on active track.
  useEffect(() => {
    const video = videoRef.current;
    onLoadStateRef.current?.("idle");

    // A rebuild of the same track (plan revision, transport rebuild, retime)
    // may keep the window the previous run committed. A deactivated track, a
    // track switch, or a changed subtitle source identity starts over from the
    // playhead rather than reviving a window from the previous position.
    // Take the committed window, then invalidate it: this run must commit its
    // own window before a later run can reuse one. That keeps a run that only
    // started, or failed before its first commit, from certifying the previous
    // generation's window as a continuation.
    const previousCommitted = committedWindowRef.current;
    committedWindowRef.current = null;
    const rememberedWindow =
      previousCommitted !== null &&
      activeUrl !== null &&
      previousCommitted.url === activeUrl &&
      previousCommitted.sourceGeneration === sourceGeneration &&
      previousCommitted.replayRevision === replayRevision
        ? previousCommitted
        : null;

    // Destroy JASSUB if the active track is not ASS, or player is detached,
    // or no video element is available.
    if (!activeUrl || !video || isDetached) {
      if (jassubRef.current) {
        jassubRef.current.destroy();
        jassubRef.current = null;
      }
      // Nothing was committed for this deactivated run, so there is no
      // continuation left to preserve (off, seek to 0, on again).
      return;
    }

    let cancelled = false;
    // Exactly one attempt (initial load or boundary/seek refresh) runs at a
    // time; its AbortController is the cancel handle for seek supersession and
    // teardown.
    let activeController: AbortController | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    // Set when the subtitle fetch answers 409 subtitle_source_changed: the
    // source rotated under this plan, so the retry must not re-run the whole
    // pipeline against the same stale URL.
    let sourceChangedSignaled = false;
    // Set when a seek cancels the in-flight attempt: the aborted request must
    // not count against the attempt budget, and the replacement restarts it.
    let supersededBySeek = false;
    // Resizes skipped while a stream reload emptied the video are replayed
    // here, once the next stream reports a frame size.
    let stopResizeToStream: (() => void) | null = null;

    // Sliding-window state. The first window is anchored just behind the
    // playhead so the cue already on screen is included. `windowEnd` is
    // Infinity once the whole-track fallback is in use, so boundary refresh
    // never schedules.
    let usingWholeTrack = false;
    const playheadStart = Math.max(
      0,
      (video.readyState > 0 ? video.currentTime : 0) +
        sourceOriginRef.current -
        ASS_WINDOW_LEAD_SECONDS,
    );
    // The element reports 0 both for a real seek to the start and for a
    // replacement stream that has not been seeked yet. Only the latter may
    // reuse the committed window; in that case the position is unknown until
    // the transport reports one, so the commit backstop below must not read it.
    const resumedAtUnknownPlayhead =
      rememberedWindow !== null &&
      (video.readyState === 0 || video.currentTime <= 0) &&
      rememberedWindow.start > playheadStart;
    let windowStart =
      resumedAtUnknownPlayhead && rememberedWindow !== null
        ? rememberedWindow.start
        : playheadStart;
    let windowEnd = windowStart + ASS_WINDOW_DURATION_SECONDS;
    // Consecutive windowed failures since the last successful window, shared by
    // the initial load and boundary refreshes so the 3→1→terminal budget is one
    // finite policy. `terminal` latches after the whole-track fallback fails:
    // no further attempts, ever.
    let windowFailures = 0;
    let terminal = false;
    // Consecutive TEXT fetch failures for this mount, reset only on a
    // successful load — not on a seek. Unlike `windowFailures` (which a seek
    // resets so a new position gets a fair try), this is the backstop that
    // stops a release erroring every request from being re-fetched forever.
    let textFailures = 0;
    // Consecutive explicitly-retryable TEXT failures (502/503/504/429/408)
    // since the last successful load. Same backoff loop on a separate generous
    // budget so a transient upstream flap does not spend the terminal budget
    // and wedge the mount on "Retrying…".
    let retryableTextFailures = 0;
    // One report per stall episode: two consecutive retryable refusals with no
    // committed script mean the server's recovery did not serve the retry, so
    // the overlay stops claiming a load is in progress (the ladder keeps
    // retrying in the background). Cleared by the next successful load.
    let stalledReported = false;
    let busy = false;
    // One rate-limited param-less probe at a time, only while the loaded
    // script is a bounded window. A complete answer upgrades the script in
    // place; a still-windowed one keeps the 120 s pipeline and probes later.
    let upgradeProbeTimer: ReturnType<typeof setTimeout> | null = null;
    let upgradeProbeController: AbortController | null = null;
    let upgradeProbeInFlight = false;
    let upgradeProbeStopped = false;
    let upgradeProbeDelayMs = ASS_UPGRADE_PROBE_FIRST_DELAY_MS;
    // Start position of the attempt currently in flight, so a paired
    // seeking/seeked event for the same position does not abort and restart it.
    let attemptStart: number | null = null;
    // Newest seek target queued while an attempt is in flight.
    let pendingStart: number | null = null;

    // Fonts belong to the track, not the window: resolve them once (after the
    // first window's text lands) and reuse them for every later window. A
    // pending bundle leaves `attached` empty but is NOT final: a bounded
    // background refresh adopts the completed bytes into the live instance.
    let fontState: {
      attached: Uint8Array[];
      fallbackFont: ReturnType<typeof fallbackFontForSubtitle>;
      fallbackFontData: Uint8Array[] | null;
    } | null = null;
    let fontBundlePending = false;
    let fontRefreshAttempts = 0;
    let fontRefreshWindowRearms = 0;
    let fontRefreshBackoffMs = ASS_FONT_REFRESH_BACKOFF_MS;
    let fontRefreshTimer: ReturnType<typeof setTimeout> | null = null;
    let fontRefreshController: AbortController | null = null;

    /** Whether the element can report a playhead at all. */
    function playheadKnown(): boolean {
      return !!video && video.readyState > 0;
    }

    /**
     * Whether a resumed transport's ambiguous zero has become a real playhead.
     * A resumed window deliberately skips completion reconciliation while the
     * element still reports 0, but once a position arrives the normal backstop
     * must not stay suppressed for the rest of that attempt.
     */
    function playheadLeftTransportZero(): boolean {
      return !!video && video.readyState > 0 && video.currentTime > 0;
    }

    /** Source-time position of the playhead (player clock + stream origin). */
    function sourcePosition(): number {
      return (video!.readyState > 0 ? video!.currentTime : 0) + sourceOriginRef.current;
    }

    /** The element's playback rate, coerced to a usable positive value. */
    function playbackRate(): number {
      const rate = video!.playbackRate;
      return Number.isFinite(rate) && rate > 0 ? rate : 1;
    }

    /**
     * Source-time lead before the current window's end at which the next page
     * is fetched. JASSUB cannot render a page until its whole script arrives,
     * so the lead has to cover extraction in wall-clock terms; scaling by the
     * playback rate keeps that margin from collapsing at 2x/4x. A negative or
     * unreadable rate counts as 1x, and a rate back to 1x restores the fixed
     * lead. The cap keeps a fast rate from requesting the next page before the
     * current one has played any of its window.
     */
    function prefetchLeadSeconds(): number {
      return Math.min(
        ASS_WINDOW_PREFETCH_LEAD_SECONDS * Math.max(1, playbackRate()),
        ASS_WINDOW_MAX_PREFETCH_LEAD_SECONDS,
      );
    }

    /**
     * Attaches a completed font bundle to the live renderer without reloading
     * the subtitle script or the track: libass picks the newly registered fonts
     * up on the next repaint.
     */
    function adoptFontsIntoLiveInstance(fonts: Uint8Array[]) {
      const instance = jassubRef.current;
      if (!instance || fonts.length === 0) return;
      const renderer = (
        instance as unknown as {
          renderer?: { addFonts?: (values: Uint8Array[]) => Promise<unknown> };
        }
      ).renderer;
      if (!renderer?.addFonts) return;
      void Promise.resolve(renderer.addFonts(fonts))
        .then(() => instance.ready)
        .then(() => {
          if (jassubRef.current === instance) return instance.resize(true);
        })
        .catch((err) => {
          console.error("[useASSSubtitles] Failed to attach refreshed fonts:", err);
        });
    }

    /**
     * Re-fetches a font bundle the server reported as pending and hot-swaps it
     * into the live renderer when it completes. Finite: it stops on fonts, on a
     * definitive font-less bundle, or after ASS_FONT_REFRESH_MAX_ATTEMPTS.
     */
    async function refreshPendingFonts() {
      fontRefreshTimer = null;
      if (cancelled || !fontBundlePending || !activeFontBundleUrl) return;
      if (fontRefreshAttempts >= ASS_FONT_REFRESH_MAX_ATTEMPTS) return;
      fontRefreshAttempts += 1;
      const controller = new AbortController();
      fontRefreshController = controller;
      try {
        const result = await loadSubtitleFontBundleResult(
          activeFontBundleUrl,
          controller.signal,
          onSourceChangedRef.current ?? undefined,
        );
        if (cancelled) return;
        if (!result.pending) {
          // Definitive answer (fonts or a genuinely font-less file): stop.
          fontBundlePending = false;
          if (result.fonts.length > 0) {
            if (fontState) fontState.attached = result.fonts;
            adoptFontsIntoLiveInstance(result.fonts);
          }
          return;
        }
        // Still extracting: back off so the next poll does not compete with
        // the server's in-flight warm for relay bandwidth.
        fontRefreshBackoffMs = Math.min(
          fontRefreshBackoffMs * ASS_FONT_REFRESH_BACKOFF_FACTOR,
          ASS_FONT_REFRESH_MAX_BACKOFF_MS,
        );
      } catch (err) {
        if ((err as Error).name !== "AbortError") {
          console.error(
            `[useASSSubtitles] Failed to refresh subtitle font bundle ${activeFontBundleUrl}:`,
            err,
          );
        }
      } finally {
        if (fontRefreshController === controller) fontRefreshController = null;
      }
      if (fontBundlePending && fontRefreshAttempts < ASS_FONT_REFRESH_MAX_ATTEMPTS) {
        fontRefreshTimer = setTimeout(() => void refreshPendingFonts(), fontRefreshBackoffMs);
      }
    }

    function scheduleFontRefresh() {
      if (cancelled || !fontBundlePending || !activeFontBundleUrl) return;
      if (fontRefreshTimer !== null) return;
      if (fontRefreshAttempts >= ASS_FONT_REFRESH_MAX_ATTEMPTS) return;
      fontRefreshTimer = setTimeout(() => void refreshPendingFonts(), fontRefreshBackoffMs);
    }

    /**
     * Re-arms the pending-font refresh after a committed window, a bounded
     * number of times. The server may still be streaming the bundle when the
     * refresh budget runs out (its dump ceiling is four minutes), and a window
     * advance is a natural re-check point: one extra poll at the capped
     * interval keeps a landed bundle adoptable. After that the bundle is
     * treated as unavailable for the rest of the mount instead of polling
     * forever. Fonts remain track-level, so the newly committed window reuses
     * whatever the adopted state holds.
     */
    function rearmFontRefreshAfterWindowCommit() {
      if (cancelled || !fontBundlePending || !activeFontBundleUrl) return;
      // Not yet through the base budget: this window is just a normal
      // scheduling point, not one of the bounded re-arms.
      if (fontRefreshAttempts < ASS_FONT_REFRESH_MAX_ATTEMPTS) {
        scheduleFontRefresh();
        return;
      }
      if (fontRefreshWindowRearms >= ASS_FONT_REFRESH_MAX_WINDOW_REARMS) {
        // The base budget and the bounded window re-check both elapsed while
        // the server still reported an in-flight bundle. Stop treating it as
        // pending so every later window does not mint another poll against a
        // source that is not producing fonts; the track keeps its fallback
        // fonts, and a new mount resolves from scratch.
        fontBundlePending = false;
        return;
      }
      fontRefreshWindowRearms += 1;
      // Budget already spent: allow exactly one more poll for this window.
      fontRefreshAttempts = ASS_FONT_REFRESH_MAX_ATTEMPTS - 1;
      fontRefreshBackoffMs = ASS_FONT_REFRESH_MAX_BACKOFF_MS;
      scheduleFontRefresh();
    }

    /** Resolve the per-track font state once, reusing it across windows. */
    async function resolveFontState(signal: AbortSignal, subContent: string) {
      if (fontState) return fontState;
      let attachedFontData: Uint8Array[] = [];
      // The subtitle TEXT drives the pipeline (and the stall watchdog); the
      // font bundle is raced against a budget so a slow extraction never
      // delays subtitle appearance. JASSUB renders with fallback fonts
      // meanwhile, and the budget-losing fetch continues in the background.
      if (activeFontBundleUrl) {
        const result = await loadSubtitleFontBundleWithinBudget(
          activeFontBundleUrl,
          signal,
          onSourceChangedRef.current ?? undefined,
        );
        attachedFontData = result.fonts;
        fontBundlePending = result.pending;
      }
      // libass renders missing glyphs with its *default* font — it does not
      // search other loaded fonts for coverage. JASSUB's built-in default
      // (Liberation Sans) lacks many non-Latin glyphs, so for those scripts we
      // point `defaultFont` at a font that covers them, chosen by track metadata
      // first and subtitle text as a fallback.
      const fallbackFont = fallbackFontForSubtitle(activeLanguage, subContent);
      let fallbackFontData: Uint8Array[] | null = null;
      if (fallbackFont) {
        try {
          fallbackFontData = await loadSubtitleFallbackFontData(fallbackFont);
        } catch (err) {
          if (!cancelled) {
            console.error(
              `[useASSSubtitles] Failed to load fallback font ${fallbackFont.family}:`,
              err,
            );
          }
        }
      }
      fontState = { attached: attachedFontData, fallbackFont, fallbackFontData };
      // A pending bundle must not persist as "this track has no fonts": fetch
      // the completed bytes in the background and hot-swap them in.
      if (fontBundlePending) scheduleFontRefresh();
      return fontState;
    }

    async function initJASSUB(
      signal: AbortSignal,
      progress: () => void,
      start: number,
      wholeTrack: boolean,
    ): Promise<boolean> {
      if (!video || cancelled) return false;
      // A retry of an already-stalled track keeps the stalled card up until
      // this attempt actually commits.
      if (!stalledReported) onLoadStateRef.current?.("loading");

      // Lazy-load JASSUB module (only once).
      if (!jassubImportRef.current) {
        jassubImportRef.current = import("jassub")
          .then((m) => m.default)
          .catch((err) => {
            jassubImportRef.current = null;
            throw err;
          });
      }

      const classPromise = jassubImportRef.current;
      void classPromise.catch(() => {});

      const url = wholeTrack ? activeUrl! : appendSubtitleWindow(activeUrl!, start);

      let subContent: string;
      let fonts: Uint8Array[];
      let renderedSubContent: string;
      let fallbackFamily: string | undefined;
      // What the response body actually is, which is not implied by the
      // request: a param-less fallback against a cold large/remote source is
      // answered with a bounded window, and a committed artifact answers a
      // positioned request with the whole track. Read the markers before the
      // body streams (a box, so the closure's assignment is visible here).
      const received: { wholeTrack: boolean; coverage: { start: number; end: number } | null } = {
        wholeTrack: false,
        coverage: null,
      };
      try {
        subContent = await fetch(url, { signal }).then(async (response) => {
          if (!response.ok) {
            if (await isSubtitleSourceChanged(response)) {
              sourceChangedSignaled = true;
              onSourceChangedRef.current?.();
              throw new DOMException("Subtitle source changed", "AbortError"); // bypass the retry
            }
            throw new SubtitleFetchError(
              response.status,
              `HTTP ${response.status}`,
              subtitleRetryAfterMs(response.headers.get("Retry-After")),
            );
          }
          received.wholeTrack = isWholeTrackResponse(response);
          received.coverage = subtitleCoverageRange(response);
          progress();
          if (!response.body) return response.text();
          const reader = response.body.getReader();
          const decoder = new TextDecoder();
          let text = "";
          while (!signal.aborted && !cancelled) {
            const { value, done } = await reader.read();
            if (done) return text + decoder.decode();
            progress();
            text += decoder.decode(value, { stream: true });
          }
          throw new DOMException("Subtitle loading cancelled", "AbortError");
        });

        const resolvedFonts = await resolveFontState(signal, subContent);
        fallbackFamily = resolvedFonts.fallbackFont?.family;
        renderedSubContent =
          resolvedFonts.fallbackFont && resolvedFonts.fallbackFontData
            ? forceASSFontFamily(subContent, resolvedFonts.fallbackFont.family)
            : subContent;
        fonts = [...resolvedFonts.attached, ...(resolvedFonts.fallbackFontData ?? [])];
      } catch (err) {
        if (!cancelled && (err as Error).name !== "AbortError") {
          console.error(`[useASSSubtitles] Failed to fetch ${url}:`, err);
        }
        throw err;
      }

      if (cancelled || signal.aborted) return false;

      const JASSUBClass = await classPromise;
      if (cancelled || signal.aborted) return false;
      const initialInset = fillInset(renderedSubContent, videoFitRef.current, coverCropRef.current);
      const instance = new JASSUBClass({
        video,
        subContent: applyASSMarginInset(renderedSubContent, initialInset),
        timeOffset: streamOriginRef.current,
        // The browser Local Font Access API is inconsistent and permissioned.
        // Letting JASSUB probe it produces noisy console warnings for common ASS
        // style fonts without making playback reliable across clients.
        queryFonts: false,
        availableFonts: { "liberation sans": liberationSansUrl },
        ...(fonts.length > 0
          ? {
              fonts,
              ...(fontState?.fallbackFont && { defaultFont: fontState.fallbackFont.family }),
            }
          : {}),
      });

      // Guard against the effect being cleaned up while the constructor ran.
      if (cancelled || signal.aborted) {
        instance.destroy();
        return false;
      }

      const previous = jassubRef.current;
      jassubRef.current = instance;
      // Resizes skipped while a stream reload emptied the video happen here,
      // once the next stream has a frame size. Some browsers report that size
      // after loadedmetadata, with a resize event. Replacements share the same
      // <video>, so drop the previous renderer's listeners before attaching the
      // new pair rather than leaving them behind until the effect tears down.
      stopResizeToStream?.();
      stopResizeToStream = null;
      const resizeToStream = () => repaintASS(instance, () => jassubRef.current === instance);
      video.addEventListener("loadedmetadata", resizeToStream);
      video.addEventListener("resize", resizeToStream);
      stopResizeToStream = () => {
        video.removeEventListener("loadedmetadata", resizeToStream);
        video.removeEventListener("resize", resizeToStream);
      };
      const fill: ASSFillState = {
        instance,
        baseContent: renderedSubContent,
        fallbackFamily,
        inset: initialInset,
        prescaleFactor: instance.prescaleFactor,
        prescaleHeightLimit: instance.prescaleHeightLimit,
      };
      fillRef.current = fill;
      instance._canvas.classList.toggle("player-ass-fill", videoFitRef.current === "cover");
      await instance.ready;
      if (cancelled || signal.aborted || jassubRef.current !== instance) return false;
      if (previous && previous !== instance) previous.destroy();

      // Fit and the player size can change while the subtitle source, fonts,
      // or renderer are still loading. Re-read both after readiness so the
      // first rendered frame cannot inherit what this effect started with.
      await syncASSFill(
        fill,
        videoFitRef.current,
        coverCropRef.current,
        () => jassubRef.current === instance,
      );
      if (!cancelled && !signal.aborted && jassubRef.current === instance) {
        // Advance to the window this instance renders so the next
        // timeupdate check measures against the loaded track, then clear
        // the failure budgets so a later unrelated error starts fresh.
        // Completeness comes from the response markers, never from the
        // request: a param-less fallback answered with windowed headers is the
        // server's implicit 600 s window, not the whole track. Treating it as
        // whole would set windowEnd to Infinity and silently stop refreshing
        // while every cue past that window is missing.
        usingWholeTrack = wholeTrack && received.wholeTrack;
        const committedStart = wholeTrack ? (received.coverage?.start ?? start) : start;
        windowStart = committedStart;
        windowEnd = usingWholeTrack
          ? Infinity
          : committedStart +
            (wholeTrack ? ASS_FALLBACK_WINDOW_SECONDS : ASS_WINDOW_DURATION_SECONDS);
        // This run's commit is the only window a later rebuild of this same
        // track identity may reuse.
        committedWindowRef.current = {
          url: activeUrl!,
          sourceGeneration,
          replayRevision,
          start: committedStart,
        };
        windowFailures = 0;
        textFailures = 0;
        retryableTextFailures = 0;
        stalledReported = false;
        builtRef.current = {
          url: activeUrl!,
          revision: cueRevisionRef.current,
          ready: true,
        };
        onLoadStateRef.current?.("ready");
        // A bounded body still needs the artifact-aware upgrade probe; a
        // complete body already is the whole track. A fallback that landed on
        // a window arms the loop here too, so the misclassification costs no
        // refresh.
        if (!usingWholeTrack) scheduleUpgradeProbe();
        // The window is committed: if the track's font bundle was still
        // pending, give it another chance to land even when the refresh
        // budget was already spent before this window started.
        if (fontBundlePending) rearmFontRefreshAfterWindowCommit();
        return true;
      }
      return false;
    }

    /**
     * One bounded attempt at `start`. Arming the watchdog before any network
     * work means a pre-header hang times out and counts as a failed attempt
     * instead of blocking the pipeline forever. Success resets the shared
     * budget inside `initJASSUB`; failure applies the one finite policy:
     * 3 windowed attempts, then 1 whole-track fallback, then terminal.
     *
     * Two things end the loop early rather than retrying: a definitive 4xx
     * (the URL can never satisfy this request, so the whole-track fallback is
     * pointless too), and the shared ASS_TEXT_MAX_FAILURES ceiling, which stops
     * a release that errors every request from being re-fetched forever as the
     * window re-anchors on seeks.
     */
    async function attempt(start: number, wholeTrack: boolean, recheckAfterCommit = true) {
      if (cancelled || terminal || busy) return;
      busy = true;
      // A window attempt owns the pipeline: a probe that landed mid-attempt
      // could swap the script out from under the commit that is about to
      // define windowStart/windowEnd. Clear the timer too, so the next probe is
      // armed from the attempt's own commit instead of firing against it.
      cancelUpgradeProbe();
      attemptStart = start;
      supersededBySeek = false;
      const attemptController = new AbortController();
      activeController = attemptController;
      let timeout: ReturnType<typeof setTimeout> | null = null;
      const progress = () => {
        if (cancelled || attemptController.signal.aborted) return;
        if (timeout !== null) clearTimeout(timeout);
        timeout = setTimeout(() => attemptController.abort(), ASS_STALL_TIMEOUT_MS);
      };
      // Arm the watchdog before the fetch, not on first progress.
      progress();
      let committed = false;
      try {
        committed = await initJASSUB(attemptController.signal, progress, start, wholeTrack);
      } catch (err) {
        if (cancelled) return;
        // A signaled source change must not re-run against the same stale URL;
        // the rebuilt JASSUB comes from the main effect re-running when the
        // refresh adopts a new plan and activeUrl changes.
        if (sourceChangedSignaled) return;
        if (supersededBySeek) {
          // A seek cancelled this request to replace it: it is not a failure.
          return;
        }
        console.error("[useASSSubtitles] Unable to load subtitles:", err);
        // Count every genuine failure (a 4xx, 5xx, stall, or network error)
        // against the shared ceiling before deciding the next step — except
        // explicitly-retryable statuses, which ride the same backoff loop on
        // a separate generous budget so a transient upstream flap does not
        // spend the terminal budget and wedge the mount on "Retrying…".
        const retryableFailure =
          err instanceof SubtitleFetchError && isRetryableSubtitleStatus(err.status);
        if (retryableFailure) {
          // A retryable answer (503 + Retry-After from a page or window-budget
          // refusal the server is already recovering from) must not move the
          // player to its terminal state: playback continues, the track stays
          // selected, and the retry below happens in the background while the
          // script already on screen keeps rendering.
          retryableTextFailures += 1;
          if (retryableTextFailures >= ASS_RETRYABLE_TEXT_MAX_FAILURES) {
            // The retry budget is spent: the final failure stays honest.
            onLoadStateRef.current?.("error");
            terminal = true;
            return;
          }
          if (
            retryableTextFailures >= ASS_RETRYABLE_TEXT_STALL_REPORT_FAILURES &&
            !stalledReported
          ) {
            stalledReported = true;
            onLoadStateRef.current?.("stalled");
          }
        } else {
          retryableTextFailures = 0;
          textFailures += 1;
          onLoadStateRef.current?.("error");
        }
        if (wholeTrack && !retryableFailure) {
          // The whole-track fallback failed too: terminal, no retry loop.
          // A retryable whole-track failure falls through to the backoff
          // retry below so a flap recovers instead of wedging the mount.
          terminal = true;
          return;
        }
        // A definitive 4xx cannot be fixed by the whole-track fallback or
        // another windowed attempt: stop now instead of burning the budget on
        // requests the server has already rejected.
        if (err instanceof SubtitleFetchError && isDefinitiveSubtitleStatus(err.status)) {
          terminal = true;
          return;
        }
        windowFailures += 1;
        if (windowFailures >= ASS_WINDOW_MAX_ATTEMPTS) {
          // Bounded windowed retries exhausted: request the param-less
          // whole-track URL once instead of hammering the failing window.
          usingWholeTrack = true;
        }
        if (textFailures >= ASS_TEXT_MAX_FAILURES) {
          // The shared failure ceiling is spent: no further attempt, even the
          // whole-track fallback.
          terminal = true;
          return;
        }
        // Re-anchor from the playhead unless it is unknown (an emptied
        // element): then the window this attempt was for is still the best
        // position and must not be replaced by a window at 0.
        const retryStart = usingWholeTrack
          ? windowStart
          : playheadKnown()
            ? Math.max(0, sourcePosition() - ASS_WINDOW_LEAD_SECONDS)
            : start;
        // The fixed attempt backoff, raised by a Retry-After the server sent for
        // a refusal it is already recovering from and capped so a long delay
        // cannot park the retry.
        const retryAfterMs = err instanceof SubtitleFetchError ? err.retryAfterMs : 0;
        retryTimer = setTimeout(
          () => {
            retryTimer = null;
            void attempt(retryStart, usingWholeTrack, recheckAfterCommit);
          },
          subtitleRetryDelayMs(0, retryAfterMs),
        );
      } finally {
        if (timeout !== null) clearTimeout(timeout);
        if (activeController === attemptController) activeController = null;
        busy = false;
        attemptStart = null;
        const next = pendingStart;
        pendingStart = null;
        if (next !== null && !cancelled && !terminal) {
          void attempt(next, false);
        } else if (
          committed &&
          (recheckAfterCommit || playheadLeftTransportZero()) &&
          !cancelled &&
          !terminal &&
          playheadKnown()
        ) {
          // Backstop for a playhead that left the committed window while the
          // extraction ran and then stopped emitting events (paused). If the
          // position is inside, this is a no-op: there is no refresh loop. An
          // empty element has no playhead to reconcile, and a window resumed
          // across a transport rebuild must not be undone by the same
          // ambiguous 0 that made it necessary. Once that resumed attempt sees
          // a real non-zero playhead, reconcile it.
          maybeRefreshWindow(false);
        }
      }
    }

    function maybeRefreshWindow(seek: boolean) {
      if (cancelled || terminal || usingWholeTrack || !jassubRef.current) return;
      const source = sourcePosition();
      const outside = source < windowStart - 1 || source > windowEnd + 1;
      const nearEnd = !outside && source > windowEnd - prefetchLeadSeconds();
      if (!outside && !nearEnd) return;
      // Restart a full lead behind the playhead so the replacement window
      // overlaps the cue currently on screen regardless of why it fired.
      const target = Math.max(0, source - ASS_WINDOW_LEAD_SECONDS);
      if (busy) {
        // Only a real seek supersedes an in-flight attempt. A timeupdate can
        // report a position outside the old window while the replacement is
        // still extracting; aborting on every tick would cancel that request
        // indefinitely. Let the current attempt finish, then the next
        // timeupdate starts the correct window.
        if (outside && seek) {
          // `seeking` and `seeked` both fire for one seek. When the
          // replacement for this position is already pending or already
          // extracting, one event is enough: aborting and restarting the same
          // target twice would throw away a request that is about to land. The
          // playhead can move a fraction of a second between the paired events,
          // so compare inside a small hysteresis rather than for equality.
          const currentTarget = pendingStart ?? attemptStart;
          if (
            currentTarget !== null &&
            target >= currentTarget &&
            target - currentTarget <= ASS_WINDOW_REANCHOR_HYSTERESIS_SECONDS
          ) {
            return;
          }
          supersededBySeek = true;
          windowFailures = 0;
          usingWholeTrack = false;
          if (retryTimer !== null) {
            clearTimeout(retryTimer);
            retryTimer = null;
          }
          // A seek-driven replacement owns the next pipeline; never leave a
          // whole-track probe scheduled to race it for the same source.
          cancelUpgradeProbe();
          activeController?.abort();
          pendingStart = target;
        }
        return;
      }
      // A scheduled retry owns the next attempt; event-driven refreshes wait.
      if (retryTimer !== null) return;
      void attempt(target, false);
    }

    /**
     * Arms the next artifact-aware probe. Only a bounded window needs one: a
     * complete body already is the whole track. The first probe is delayed past
     * first paint and the first prefetch; later ones are rate-limited.
     */
    function scheduleUpgradeProbe() {
      if (cancelled || terminal || usingWholeTrack || upgradeProbeStopped) return;
      if (upgradeProbeTimer !== null || upgradeProbeInFlight) return;
      upgradeProbeTimer = setTimeout(() => {
        upgradeProbeTimer = null;
        void probeWholeTrackUpgrade();
      }, upgradeProbeDelayMs);
    }

    /**
     * Drops a scheduled or in-flight whole-track probe. Called whenever an
     * attempt takes over the pipeline so a probe GET never runs concurrently
     * with a window (or seek-driven) fetch against the same source.
     */
    function cancelUpgradeProbe() {
      if (upgradeProbeTimer !== null) {
        clearTimeout(upgradeProbeTimer);
        upgradeProbeTimer = null;
      }
      upgradeProbeController?.abort();
    }

    /**
     * Asks the param-less URL whether the server can now serve the whole track
     * (the warm behind a windowed serve commits a full-track artifact). A
     * complete 200 swaps the script into the running renderer through the same
     * `setTrack` path a retime uses, so seeking needs no further network; a
     * still-windowed 200 keeps the 120 s pipeline. Nothing here affects the
     * window durations, leads, stall and failure budgets of that pipeline: the
     * probe is skipped whenever an attempt owns it, and any probe failure just
     * backs the probe off.
     */
    async function probeWholeTrackUpgrade() {
      if (cancelled || terminal || usingWholeTrack || upgradeProbeStopped) return;
      if (!activeUrl) return;
      // Never race a windowed (or seek-driven) attempt: it owns the commit
      // that defines windowStart/windowEnd for the loaded script.
      if (busy || pendingStart !== null || retryTimer !== null) {
        scheduleUpgradeProbe();
        return;
      }
      const fill = fillRef.current;
      const instance = jassubRef.current;
      if (!fill || !instance || fill.instance !== instance) return;

      const controller = new AbortController();
      upgradeProbeController = controller;
      upgradeProbeInFlight = true;
      let timeout: ReturnType<typeof setTimeout> | null = null;
      const armWatchdog = () => {
        if (timeout !== null) clearTimeout(timeout);
        timeout = setTimeout(() => controller.abort(), ASS_STALL_TIMEOUT_MS);
      };
      try {
        armWatchdog();
        const response = await fetch(activeUrl, { signal: controller.signal });
        if (cancelled || controller.signal.aborted) return;
        if (!response.ok) {
          if (await isSubtitleSourceChanged(response)) {
            // The plan's URLs are stale; a later probe against this one could
            // only fail the same way.
            sourceChangedSignaled = true;
            upgradeProbeStopped = true;
            onSourceChangedRef.current?.();
            return;
          }
          if (isDefinitiveSubtitleStatus(response.status)) {
            // Same terminal policy as a windowed fetch: the server rejected
            // this URL definitively, so stop rather than keep probing it.
            terminal = true;
            return;
          }
          // 5xx or an explicitly-retryable status: the loaded window keeps
          // rendering; keep windowing and probe again on a growing backoff.
          upgradeProbeDelayMs = Math.min(
            Math.max(upgradeProbeDelayMs * 2, ASS_UPGRADE_PROBE_INTERVAL_MS),
            ASS_UPGRADE_PROBE_MAX_INTERVAL_MS,
          );
          return;
        }
        if (!isWholeTrackResponse(response)) {
          // Still a bounded window: no committed full-track artifact yet. When
          // its advertised coverage abuts (or overlaps) the loaded window and
          // reaches past it, adopt it as the next window instead of throwing
          // the body away; otherwise drop it and keep the 120 s pipeline.
          const coverage = subtitleCoverageRange(response);
          if (
            coverage === null ||
            coverage.start > windowEnd + 1 ||
            coverage.end <= windowEnd + 1
          ) {
            void response.body?.cancel().catch(() => {});
            upgradeProbeDelayMs = ASS_UPGRADE_PROBE_INTERVAL_MS;
            return;
          }
          const text = await response.text();
          if (cancelled || controller.signal.aborted || jassubRef.current !== instance) return;
          // Same swap path as the retime: the loaded coverage grows, the
          // renderer and the video element are untouched.
          const content = fill.fallbackFamily
            ? forceASSFontFamily(text, fill.fallbackFamily)
            : text;
          fill.baseContent = content;
          fill.inset = fillInset(content, videoFitRef.current, coverCropRef.current);
          await instance.renderer.setTrack(applyASSMarginInset(content, fill.inset));
          if (cancelled || controller.signal.aborted || jassubRef.current !== instance) return;
          windowStart = coverage.start;
          windowEnd = coverage.end;
          windowFailures = 0;
          textFailures = 0;
          retryableTextFailures = 0;
          committedWindowRef.current = {
            url: activeUrl,
            sourceGeneration,
            replayRevision,
            start: coverage.start,
          };
          upgradeProbeDelayMs = ASS_UPGRADE_PROBE_INTERVAL_MS;
          return;
        }
        const text = await response.text();
        if (cancelled || controller.signal.aborted || jassubRef.current !== instance) return;
        // The retime swap, not a rebuild: the running renderer's script is
        // replaced in place, so the current frame and the video element are
        // untouched.
        const content = fill.fallbackFamily ? forceASSFontFamily(text, fill.fallbackFamily) : text;
        fill.baseContent = content;
        fill.inset = fillInset(content, videoFitRef.current, coverCropRef.current);
        await instance.renderer.setTrack(applyASSMarginInset(content, fill.inset));
        if (cancelled || controller.signal.aborted || jassubRef.current !== instance) return;
        usingWholeTrack = true;
        windowEnd = Infinity;
        committedWindowRef.current = {
          url: activeUrl,
          sourceGeneration,
          replayRevision,
          start: windowStart,
        };
        // Nothing left to upgrade.
        upgradeProbeStopped = true;
      } catch (err) {
        if (cancelled) return;
        if ((err as Error).name !== "AbortError") {
          console.error(
            `[useASSSubtitles] Whole-track subtitle probe failed for ${activeUrl}:`,
            err,
          );
        }
        // A watchdog abort, a network failure, or a failed setTrack leaves the
        // loaded window rendering, so stay windowed and back the probe off.
        upgradeProbeDelayMs = Math.min(
          Math.max(upgradeProbeDelayMs * 2, ASS_UPGRADE_PROBE_INTERVAL_MS),
          ASS_UPGRADE_PROBE_MAX_INTERVAL_MS,
        );
      } finally {
        if (timeout !== null) clearTimeout(timeout);
        if (upgradeProbeController === controller) upgradeProbeController = null;
        upgradeProbeInFlight = false;
        if (!cancelled && !terminal && !usingWholeTrack && !upgradeProbeStopped) {
          scheduleUpgradeProbe();
        }
      }
    }

    const handleTimeUpdate = () => maybeRefreshWindow(false);
    const handleSeek = () => maybeRefreshWindow(true);
    // A rate change moves the wall-clock point at which the current window
    // needs its successor: recompute the trigger at once instead of waiting
    // for the next timeupdate. This is a no-op unless the playhead is already
    // inside the (possibly new) prefetch lead, and the in-flight guard in
    // maybeRefreshWindow keeps repeated rate events from double-fetching.
    const handleRateChange = () => maybeRefreshWindow(false);
    void attempt(windowStart, false, !resumedAtUnknownPlayhead);
    video.addEventListener("timeupdate", handleTimeUpdate);
    video.addEventListener("seeking", handleSeek);
    video.addEventListener("seeked", handleSeek);
    video.addEventListener("ratechange", handleRateChange);

    return () => {
      cancelled = true;
      if (retryTimer !== null) clearTimeout(retryTimer);
      if (fontRefreshTimer !== null) clearTimeout(fontRefreshTimer);
      if (upgradeProbeTimer !== null) clearTimeout(upgradeProbeTimer);
      fontRefreshController?.abort();
      upgradeProbeController?.abort();
      activeController?.abort();
      stopResizeToStream?.();
      video.removeEventListener("timeupdate", handleTimeUpdate);
      video.removeEventListener("seeking", handleSeek);
      video.removeEventListener("seeked", handleSeek);
      video.removeEventListener("ratechange", handleRateChange);
      // Destroy the current instance if the effect is being torn down
      // (e.g. track switch or unmount). This covers the common case where
      // initJASSUB has already completed and stored the instance.
      if (jassubRef.current) {
        jassubRef.current.destroy();
        jassubRef.current = null;
      }
    };
    // videoRef is a stable ref object. streamOriginSeconds is read from
    // sourceOriginRef inside the async function to always get the latest value.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- videoRef and streamOriginRef are refs
  }, [
    activeUrl,
    activeLanguage,
    activeFontBundleUrl,
    isDetached,
    sourceGeneration,
    rebuild,
    transportRevision,
    replayRevision,
  ]);

  // A retime of the track on screen (subtitle sync, a timing reset) loads the
  // corrected script into the running renderer: the current events stay up
  // until it is in, and nothing is announced. A renderer still loading, or a
  // swap that fails, rebuilds instead.
  useEffect(() => {
    const built = builtRef.current;
    if (!activeUrl || !built || built.url !== activeUrl || built.revision === cueRevision) return;
    const fill = fillRef.current;
    if (!built.ready || !fill || jassubRef.current !== fill.instance) {
      setRebuild((n) => n + 1);
      return;
    }
    built.revision = cueRevision;
    const { instance } = fill;
    const controller = new AbortController();
    onLoadStateRef.current?.("refreshing");
    void (async () => {
      try {
        const response = await fetch(activeUrl, { signal: controller.signal });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        if (!isWholeTrackResponse(response)) {
          // The param-less URL answered with a bounded window (the server has
          // no committed full-track artifact yet). Swapping it into the
          // running renderer would truncate the retimed script to that window,
          // so rebuild through the windowed pipeline instead.
          setRebuild((n) => n + 1);
          return;
        }
        const text = await response.text();
        if (controller.signal.aborted || jassubRef.current !== instance) return;
        const content = fill.fallbackFamily ? forceASSFontFamily(text, fill.fallbackFamily) : text;
        fill.baseContent = content;
        fill.inset = fillInset(content, videoFitRef.current, coverCropRef.current);
        await instance.renderer.setTrack(applyASSMarginInset(content, fill.inset));
        if (!controller.signal.aborted && jassubRef.current === instance) {
          onLoadStateRef.current?.("ready");
        }
      } catch (err) {
        if (controller.signal.aborted) return;
        console.error("[useASSSubtitles] Unable to load retimed subtitles:", err);
        setRebuild((n) => n + 1);
      }
    })();
    return () => controller.abort();
  }, [activeUrl, cueRevision]);

  // Update JASSUB's time offset when either the media timeline remaps or
  // the user nudges subtitle sync. Avoids destroying and recreating the
  // instance for offset-only changes.
  useEffect(() => {
    const instance = jassubRef.current;
    if (!instance || !activeUrl) return;

    instance.timeOffset = effectiveOffset;
    repaintASS(instance, () => jassubRef.current === instance);
  }, [effectiveOffset, activeUrl]);

  // Keep the canvas crop and the Fill margins in step with the video. A fit
  // toggle applies immediately; crop changes from resizing are debounced.
  const cropX = coverCrop.x;
  const cropY = coverCrop.y;
  useEffect(() => {
    const fitChanged = syncedFitRef.current !== videoFit;
    syncedFitRef.current = videoFit;
    const fill = fillRef.current;
    const instance = jassubRef.current;
    if (!fill || !instance || fill.instance !== instance || !activeUrl) return;

    instance._canvas.classList.toggle("player-ass-fill", videoFit === "cover");
    const isCurrent = () => jassubRef.current === instance;
    const timer = setTimeout(
      () => {
        void instance.ready
          .then(() => {
            if (isCurrent()) {
              return syncASSFill(fill, videoFit, { x: cropX, y: cropY }, isCurrent);
            }
          })
          .catch((err) => {
            if (isCurrent()) {
              console.error("[useASSSubtitles] Unable to apply video fit to subtitles:", err);
            }
          });
      },
      fitChanged ? 0 : FILL_MARGIN_DEBOUNCE_MS,
    );
    return () => clearTimeout(timer);
  }, [activeUrl, videoFit, cropX, cropY]);

  // Cleanup on unmount.
  useEffect(() => {
    return () => {
      if (jassubRef.current) {
        jassubRef.current.destroy();
        jassubRef.current = null;
      }
    };
  }, []);

  return { isActive: isASS && !isDetached };
}
