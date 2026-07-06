import { useEffect, useRef, useState } from "react";
import type { PgsRenderer } from "libpgs";
import libpgsWorkerUrl from "libpgs/dist/libpgs.worker.js?url";
import type { PlayerSubtitleInfo } from "../types";
import { isPGSCodec } from "../utils/subtitleCodecs";
import type { SubtitleAppearance } from "../../lib/subtitleAppearance";
import {
  computePgsPlacements,
  computeVideoRect,
  detectCueRegions,
  pgsTextLineHeightPx,
} from "../utils/pgsPlacement";

// Downsampled scan width for cue-region detection. Region rects are scaled
// back up to source pixels, so the ±(source/scanWidth) rounding is invisible.
const SCAN_WIDTH = 480;

// Each windowed .sup fetch covers this many source-time seconds. ffmpeg's
// startup cost dominates the extract, so a big window is nearly free and
// keeps re-fetches rare. Matches the server's ?duration= cap.
const WINDOW_DURATION = 3600;
// Pull back this many seconds from the playback position when requesting a
// window. PGS epochs can straddle a seek point; the backoff bounds the worst
// case to one dropped cue.
const SEEK_BACKOFF = 10;
// Re-point the renderer at the next window this many seconds before the
// current one's coverage ends, so the new cues are loading before playback
// crosses the boundary.
const PREFETCH_LEAD = 60;
// A cue-gating hold shorter than this never shows the loading indicator, so
// fast extracts don't flash UI.
const HOLD_INDICATOR_GRACE_MS = 500;
// Don't strand playback on a broken or glacial extract: after this long the
// hold resumes playback and only the indicator stays up until data arrives.
const HOLD_SAFETY_TIMEOUT_MS = 20_000;
// PGS presentation timestamps are 90kHz clock ticks
// (PgsRendererHelper.getIndexFromTimestamps compares against time*1000*90).
const PGS_TICKS_PER_SECOND = 90_000;

// Undocumented libpgs internals observed for cue readiness. PgsRenderer keeps
// its PgsRendererImpl in a private `implementation` field; the impl holds the
// parsed `updateTimestamps` array (90kHz ticks, ascending — replaced wholesale
// on every worker `updateTimestamps` message) and invokes
// `onTimestampsUpdated` after each replacement. The worker posts those
// messages at most ~1/sec while parsing the streamed .sup, plus once at
// completion. If a future libpgs version reshapes these internals we detect
// that and simply never gate playback (old pre-hold behavior).
interface PgsRendererInternals {
  updateTimestamps?: unknown;
  onTimestampsUpdated?: () => void;
}

/**
 * Manages client-side PGS (Blu-ray bitmap) subtitle rendering via libpgs.
 *
 * libpgs decodes the .sup stream (fetched progressively by its worker; the
 * URL already carries auth via ?token=) and draws full display-set frames
 * onto a hidden source canvas. This hook then re-composites those frames onto
 * a visible overlay canvas, applying the user's subtitle appearance settings
 * the same way the tvOS/iOS player does: cue regions are detected from the
 * decoded frame's alpha channel and re-placed per the size scale, vertical
 * position preset, and background box (see utils/pgsPlacement.ts). Font
 * family, text color, and outline are baked into the source pixels and do
 * not apply.
 *
 * libpgs runs in `workerWithoutOffscreenCanvas` mode: decode stays in the
 * worker, but drawing happens on the main thread so the source canvas stays
 * readable for region detection.
 *
 * Fetching is windowed (mirroring useSubtitleTracks' VTT sliding window):
 * the .sup URL carries `windowed=1&position=&duration=` so the server seeks
 * near the playback position instead of demuxing the whole file from byte 0.
 * When playback seeks outside the covered range — or approaches its end —
 * the renderer is re-pointed at a fresh window via `loadFromUrl` without
 * tearing it down. Positions are in source-time seconds, the same timeline
 * libpgs looks up display sets on (`video.currentTime + timeOffset`).
 *
 * Coordination mirrors useASSSubtitles: the `isActive` return value tells the
 * player to suppress the VTT text overlay while bitmap rendering is active.
 *
 * Cue-gating hold: extraction takes seconds, so when a fetch starts with
 * nothing on screen to bridge it (initial track enable, or a seek outside the
 * covered window — NOT the prefetch, which reloads while current cues still
 * render), the hook pauses the video until the renderer's parsed data covers
 * the playhead, then auto-resumes if it was the one that paused and the user
 * hasn't taken over meanwhile. `isLoadingCues` tells the player to show a
 * "Loading subtitles…" indicator once the hold outlives a short grace period.
 */
export function usePGSSubtitles(
  videoRef: React.RefObject<HTMLVideoElement | null>,
  subtitleUrls: PlayerSubtitleInfo[],
  activeSubtitleIndex: number | null,
  isDetached: boolean,
  streamOriginSeconds: number,
  subtitleDelayMs: number,
  appearance: SubtitleAppearance,
): { isActive: boolean; isLoadingCues: boolean } {
  const rendererRef = useRef<PgsRenderer | null>(null);
  // True while a cue-gating hold has outlived its grace period (or timed out
  // waiting): the player shows a "Loading subtitles…" indicator.
  const [isLoadingCues, setIsLoadingCues] = useState(false);
  const libpgsImportRef = useRef<Promise<typeof import("libpgs")> | null>(null);
  // libpgs renders the display set whose timestamp matches
  // `video.currentTime + timeOffset`, and .sup timestamps are in source media
  // time. With HLS PTS rebasing, currentTime + streamOrigin = media time, so
  // the origin adds. A positive user delay should show subtitles *later*
  // (VTTCue semantics: cues shift forward), which here means looking up an
  // *earlier* display set at any instant — so the delay subtracts.
  const effectiveOffset = streamOriginSeconds - subtitleDelayMs / 1000;
  const offsetRef = useRef(effectiveOffset);
  offsetRef.current = effectiveOffset;
  // Latest appearance, readable from the compositor without rebuilding the
  // renderer; appearance changes just trigger a recomposite.
  const appearanceRef = useRef(appearance);
  appearanceRef.current = appearance;
  const compositeRef = useRef<(() => void) | null>(null);

  // Resolve the active subtitle track.
  const activeSub =
    activeSubtitleIndex !== null
      ? (subtitleUrls.find((s) => s.index === activeSubtitleIndex) ?? null)
      : null;

  const isPGS = activeSub !== null && isPGSCodec(activeSub.codec);
  const activeUrl = isPGS ? activeSub.url : null;

  // Main effect: create/destroy the renderer and compositor for the track.
  useEffect(() => {
    const video = videoRef.current;

    if (!activeUrl || !video || isDetached) {
      if (rendererRef.current) {
        rendererRef.current.dispose();
        rendererRef.current = null;
      }
      return;
    }

    let cancelled = false;
    let displayCanvas: HTMLCanvasElement | null = null;
    let resizeObserver: ResizeObserver | null = null;
    let followUpTimer: ReturnType<typeof setTimeout> | null = null;
    // Fingerprint of the last composited state, to skip redundant redraws on
    // the ~4Hz timeupdate cadence when nothing changed.
    let lastFingerprint = "";

    // Hidden source canvas that libpgs draws decoded frames onto. Kept
    // detached from the DOM; it only feeds the compositor.
    const sourceCanvas = document.createElement("canvas");
    // Downsampled scratch canvas for alpha-channel region detection.
    const scanCanvas = document.createElement("canvas");

    // Source-time range the current windowed fetch covers. Updated by
    // windowUrl whenever a new window is requested.
    let windowStart = 0;
    let windowEnd = 0;

    // --- Cue-gating hold state ---
    // The libpgs implementation object, when its shape matches what we know
    // how to observe; null disables holds entirely (readiness would be
    // unobservable, and a hold that can only end by timeout is worse than the
    // old play-through behavior).
    let observedImpl: { updateTimestamps: number[] } | null = null;
    // Timestamp batches received since the last loadFromUrl. The impl keeps
    // the *previous* window's array until the worker's first post for the new
    // load, so a stale watermark must not satisfy readiness.
    let updatesSinceLoad = 0;
    // A hold is pending: cue data does not yet cover the playhead.
    let holdPending = false;
    // We paused the video for this hold and owe an auto-resume. Cleared by
    // any user-initiated play/pause so we never fight the user.
    let holdWePaused = false;
    let holdGraceTimer: ReturnType<typeof setTimeout> | null = null;
    let holdSafetyTimer: ReturnType<typeof setTimeout> | null = null;
    // Our own video.pause()/play() calls fire the same events as the user's;
    // these flags let the event handlers ignore exactly those.
    let selfPause = false;
    let selfPlay = false;

    // Builds the windowed .sup URL for a window anchored just before
    // `sourcePos`, and records the coverage it will provide.
    function windowUrl(sourcePos: number): string {
      windowStart = Math.max(0, sourcePos - SEEK_BACKOFF);
      windowEnd = windowStart + WINDOW_DURATION;
      updatesSinceLoad = 0;
      const sep = activeUrl!.includes("?") ? "&" : "?";
      return `${activeUrl}${sep}windowed=1&position=${windowStart}&duration=${WINDOW_DURATION}`;
    }

    // Whether the renderer's parsed data can show a cue at source time `pos`.
    // libpgs renders the display set at time T only when some parsed
    // timestamp exceeds T*90000 (getIndexFromTimestamps returns -1
    // otherwise), so the last parsed timestamp is an exact readiness
    // watermark for the current load.
    function cueDataCovers(pos: number): boolean {
      const ts = observedImpl?.updateTimestamps;
      if (!ts || updatesSinceLoad === 0 || ts.length === 0) return false;
      return ts[ts.length - 1]! > pos * PGS_TICKS_PER_SECOND;
    }

    function resumeIfWePaused() {
      if (holdWePaused && video!.paused) {
        selfPlay = true;
        void video!.play().catch(() => {
          selfPlay = false;
        });
      }
      holdWePaused = false;
    }

    function endHold(resume: boolean) {
      holdPending = false;
      if (holdGraceTimer) clearTimeout(holdGraceTimer);
      if (holdSafetyTimer) clearTimeout(holdSafetyTimer);
      holdGraceTimer = null;
      holdSafetyTimer = null;
      setIsLoadingCues(false);
      if (resume) resumeIfWePaused();
      else holdWePaused = false;
    }

    // Starts (or restarts, after a seek during a hold) the cue-gating hold
    // for a load that has nothing on screen to bridge it.
    function beginHold() {
      if (!observedImpl || cancelled) return;
      if (holdGraceTimer) clearTimeout(holdGraceTimer);
      if (holdSafetyTimer) clearTimeout(holdSafetyTimer);
      holdPending = true;
      if (!video!.paused) {
        holdWePaused = true;
        selfPause = true;
        video!.pause();
      }
      holdGraceTimer = setTimeout(() => {
        holdGraceTimer = null;
        if (holdPending) setIsLoadingCues(true);
      }, HOLD_INDICATOR_GRACE_MS);
      holdSafetyTimer = setTimeout(() => {
        holdSafetyTimer = null;
        // Resume playback but keep holdPending (and the indicator) so the
        // hold still resolves cleanly if data eventually arrives.
        if (!holdPending) return;
        setIsLoadingCues(true);
        resumeIfWePaused();
      }, HOLD_SAFETY_TIMEOUT_MS);
    }

    // Ends the hold once parsed cue data reaches the playhead. Driven by the
    // renderer's timestamp updates (≤1/sec plus one at load completion) and,
    // when the user plays through a hold, by timeupdate.
    function checkHoldReadiness() {
      if (!holdPending || cancelled) return;
      if (!cueDataCovers(currentSourcePos())) return;
      endHold(true);
      // Show the now-available cue immediately, even while still paused.
      compositeWithFollowUp();
    }

    // User pressed play during a hold: respect it — release the resume
    // obligation and let cues pop in when data arrives (indicator stays).
    function handlePlayEvent() {
      if (selfPlay) {
        selfPlay = false;
        return;
      }
      holdWePaused = false;
    }

    // User paused deliberately (during a hold or not): never auto-resume.
    function handlePauseEvent() {
      if (selfPause) {
        selfPause = false;
        return;
      }
      holdWePaused = false;
    }

    // Current playback position in source-time seconds — the timeline both
    // the server's ?position= and libpgs display-set lookup use. offsetRef
    // is streamOrigin minus the user delay; the delay is millisecond-scale
    // and well inside SEEK_BACKOFF, so it doesn't need unpicking here.
    function currentSourcePos(): number {
      return Math.max(0, video!.currentTime + offsetRef.current);
    }

    // Re-points the renderer at a fresh window when playback falls outside
    // the covered range (a seek) or approaches its end (normal playback).
    // loadFromUrl resets libpgs' display sets in place — no renderer
    // teardown, and times outside the loaded range simply render nothing
    // until the new window's data arrives.
    function maybeReloadWindow() {
      const renderer = rendererRef.current;
      if (!renderer || cancelled) return;
      const pos = currentSourcePos();
      const inCoverage = pos >= windowStart && pos < windowEnd;
      if (inCoverage && pos < windowEnd - PREFETCH_LEAD) return;
      renderer.loadFromUrl(windowUrl(pos));
      // A prefetch (still inside coverage) keeps playing on the old window's
      // cues; only a load with nothing to show gates playback.
      if (!inCoverage) beginHold();
    }

    function composite() {
      if (cancelled || !displayCanvas || !video) return;
      const srcW = sourceCanvas.width;
      const srcH = sourceCanvas.height;

      // Match the display canvas backing store to the element size.
      const dpr = window.devicePixelRatio || 1;
      const cssW = video.clientWidth;
      const cssH = video.clientHeight;
      const pxW = Math.max(1, Math.round(cssW * dpr));
      const pxH = Math.max(1, Math.round(cssH * dpr));

      const ctx = displayCanvas.getContext("2d");
      if (!ctx) return;

      if (srcW === 0 || srcH === 0) {
        if (lastFingerprint !== "empty") {
          lastFingerprint = "empty";
          displayCanvas.width = pxW;
          displayCanvas.height = pxH;
          ctx.clearRect(0, 0, pxW, pxH);
        }
        return;
      }

      // Downsample and read the alpha channel.
      const scanW = Math.min(SCAN_WIDTH, srcW);
      const scanH = Math.max(1, Math.round((srcH / srcW) * scanW));
      if (scanCanvas.width !== scanW || scanCanvas.height !== scanH) {
        scanCanvas.width = scanW;
        scanCanvas.height = scanH;
      }
      const scanCtx = scanCanvas.getContext("2d", { willReadFrequently: true });
      if (!scanCtx) return;
      scanCtx.clearRect(0, 0, scanW, scanH);
      scanCtx.drawImage(sourceCanvas, 0, 0, scanW, scanH);
      const scanData = scanCtx.getImageData(0, 0, scanW, scanH);

      const scanRegions = detectCueRegions(scanData.data, scanW, scanH);
      const upscale = srcW / scanW;
      const regions = scanRegions.map((r) => ({
        x: r.x * upscale,
        y: r.y * upscale,
        width: r.width * upscale,
        height: r.height * upscale,
        lineHeight: (r.lineHeight ?? r.height) * upscale,
      }));

      const a = appearanceRef.current;
      const fingerprint = [
        pxW,
        pxH,
        srcW,
        srcH,
        a.fontSize,
        a.position,
        a.backgroundStyle,
        a.backgroundColor,
        a.backgroundOpacity,
        regions.map((r) => `${r.x},${r.y},${r.width},${r.height}`).join(";"),
      ].join("|");
      if (fingerprint === lastFingerprint) return;
      lastFingerprint = fingerprint;

      displayCanvas.width = pxW;
      displayCanvas.height = pxH;
      ctx.clearRect(0, 0, pxW, pxH);

      const videoAspect =
        video.videoWidth > 0 && video.videoHeight > 0
          ? video.videoWidth / video.videoHeight
          : srcW / srcH;
      const videoRect = computeVideoRect(pxW, pxH, videoAspect);
      // The canvas works in device pixels; the text overlay's font sizes are
      // CSS pixels.
      const lineHeight = pgsTextLineHeightPx(a.fontSize) * dpr;
      const placements = computePgsPlacements(regions, srcW, srcH, videoRect, a, lineHeight);

      for (const p of placements) {
        if (p.background) {
          const { rect, cornerRadius } = p.background;
          const { r, g, b } = hexToRgbSafe(a.backgroundColor);
          ctx.fillStyle = `rgba(${r}, ${g}, ${b}, ${a.backgroundOpacity / 100})`;
          ctx.beginPath();
          if (typeof ctx.roundRect === "function") {
            ctx.roundRect(rect.x, rect.y, rect.width, rect.height, cornerRadius);
          } else {
            ctx.rect(rect.x, rect.y, rect.width, rect.height);
          }
          ctx.fill();
        }
        ctx.drawImage(
          sourceCanvas,
          p.src.x,
          p.src.y,
          p.src.width,
          p.src.height,
          p.dest.x,
          p.dest.y,
          p.dest.width,
          p.dest.height,
        );
      }
    }
    compositeRef.current = composite;

    // libpgs draws after a worker round trip, so a same-tick composite can
    // catch the previous frame — the short follow-up picks up the fresh draw
    // instead of waiting for the next ~250ms timeupdate.
    function compositeWithFollowUp() {
      composite();
      if (followUpTimer) clearTimeout(followUpTimer);
      followUpTimer = setTimeout(composite, 120);
    }

    // Playback-progress handler: keeps the fetch window covering the
    // playhead, then recomposites the frame libpgs drew for the new time.
    function handleTimeProgress() {
      maybeReloadWindow();
      checkHoldReadiness();
      compositeWithFollowUp();
    }

    async function initRenderer() {
      if (!video || cancelled) return;

      // Lazy-load the libpgs module (only once).
      if (!libpgsImportRef.current) {
        libpgsImportRef.current = import("libpgs");
      }
      const { PgsRenderer: PgsRendererClass } = await libpgsImportRef.current;
      if (cancelled) return;

      type PgsOptions = ConstructorParameters<typeof PgsRendererClass>[0];
      const renderer = new PgsRendererClass({
        video,
        canvas: sourceCanvas,
        subUrl: windowUrl(currentSourcePos()),
        workerUrl: libpgsWorkerUrl,
        timeOffset: offsetRef.current,
        // Decode in the worker but draw on the main thread so the source
        // canvas stays readable for region detection (an OffscreenCanvas
        // transfer would make it opaque to getImageData/drawImage).
        mode: "workerWithoutOffscreenCanvas" as NonNullable<PgsOptions["mode"]>,
      });

      // Guard against the effect being cleaned up while the constructor ran.
      if (cancelled) {
        renderer.dispose();
        return;
      }
      rendererRef.current = renderer;

      // Observe parse progress through libpgs' private implementation. The
      // wrapper assigns its own onTimestampsUpdated (re-render on new data)
      // in its constructor; chain it rather than replace it.
      const impl = (renderer as unknown as { implementation?: PgsRendererInternals })
        .implementation;
      if (impl && Array.isArray(impl.updateTimestamps)) {
        observedImpl = impl as { updateTimestamps: number[] };
        const chained = impl.onTimestampsUpdated;
        impl.onTimestampsUpdated = () => {
          chained?.();
          updatesSinceLoad += 1;
          checkHoldReadiness();
        };
      }

      // The constructor already kicked off the initial window fetch (subUrl)
      // with nothing on screen: gate playback until its cues arrive.
      beginHold();

      // Visible overlay the compositor draws styled cues onto.
      displayCanvas = document.createElement("canvas");
      displayCanvas.style.position = "absolute";
      displayCanvas.style.inset = "0";
      displayCanvas.style.width = "100%";
      displayCanvas.style.height = "100%";
      displayCanvas.style.pointerEvents = "none";
      displayCanvas.style.zIndex = "20";
      video.parentElement?.appendChild(displayCanvas);

      // Recomposite as playback advances (libpgs redraws the source canvas
      // on the same events) and when the layout changes; the same events
      // drive the sliding fetch window.
      video.addEventListener("timeupdate", handleTimeProgress);
      video.addEventListener("seeked", handleTimeProgress);
      // Distinguish user play/pause from our own during a hold.
      video.addEventListener("play", handlePlayEvent);
      video.addEventListener("pause", handlePauseEvent);
      resizeObserver = new ResizeObserver(() => composite());
      resizeObserver.observe(video);
      composite();
    }

    void initRenderer();

    return () => {
      cancelled = true;
      compositeRef.current = null;
      // Cancel any hold: disabling subtitles (or switching tracks) must
      // resume playback immediately if we were the ones who paused it.
      endHold(true);
      if (followUpTimer) clearTimeout(followUpTimer);
      video.removeEventListener("timeupdate", handleTimeProgress);
      video.removeEventListener("seeked", handleTimeProgress);
      video.removeEventListener("play", handlePlayEvent);
      video.removeEventListener("pause", handlePauseEvent);
      resizeObserver?.disconnect();
      displayCanvas?.remove();
      // dispose() terminates the worker, which also abandons its in-flight
      // .sup fetch — no separate AbortController.
      if (rendererRef.current) {
        rendererRef.current.dispose();
        rendererRef.current = null;
      }
    };
    // videoRef is a stable ref object. effectiveOffset and appearance are
    // read from refs inside the compositor to always get the latest values.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeUrl, isDetached]);

  // Update the renderer's time offset when the media timeline remaps or the
  // user nudges subtitle sync — without recreating the renderer.
  useEffect(() => {
    const renderer = rendererRef.current;
    if (!renderer || !activeUrl) return;
    renderer.timeOffset = effectiveOffset;
  }, [effectiveOffset, activeUrl]);

  // Recomposite on appearance changes — including while paused, where no
  // timeupdate will arrive to pick up the new settings.
  useEffect(() => {
    compositeRef.current?.();
  }, [
    appearance.fontSize,
    appearance.position,
    appearance.backgroundStyle,
    appearance.backgroundColor,
    appearance.backgroundOpacity,
  ]);

  // Cleanup on unmount.
  useEffect(() => {
    return () => {
      if (rendererRef.current) {
        rendererRef.current.dispose();
        rendererRef.current = null;
      }
    };
  }, []);

  return { isActive: isPGS && !isDetached, isLoadingCues };
}

function hexToRgbSafe(hex: string): { r: number; g: number; b: number } {
  const clean = hex.replace("#", "");
  if (!/^[0-9a-fA-F]{6}$/.test(clean)) return { r: 0, g: 0, b: 0 };
  return {
    r: parseInt(clean.substring(0, 2), 16),
    g: parseInt(clean.substring(2, 4), 16),
    b: parseInt(clean.substring(4, 6), 16),
  };
}
