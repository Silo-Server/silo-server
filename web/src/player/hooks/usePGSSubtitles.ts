import { useEffect, useRef, type RefObject } from "react";
import type { PlayerSubtitleInfo, VideoFitMode } from "../types";
import { videoContentRect, type FitRect } from "../utils/videoFit";
import { isPGSCodec } from "../utils/subtitleCodecs";
import { toMediaTime } from "../utils/mediaTimeline";
import {
  decodePGSObject,
  mergePGSCompositions,
  pgsCompositionIndexAt,
  PGSStreamParser,
  type PGSComposition,
  type PGSPlacedObject,
} from "../utils/pgs";
import {
  nextSubtitleRetryDelay,
  planSubtitleWindow,
  SUBTITLE_FETCH_STALL_TIMEOUT_MS,
  SUBTITLE_WINDOW_SECONDS,
} from "../utils/subtitleWindows";

// A PGS composition stays on screen until the next one replaces it, and the
// server's positioned extract starts at the window, so a fresh window reaches
// back far enough to include a subtitle already showing at the playhead.
const SEEK_BACKOFF = 10;
// Larger than any real Blu-ray plane; a bigger object is damaged data.
const MAX_OBJECT_PIXELS = 4096 * 4096;
// Keeps a subtitle that would land past the player's edge (a Fill crop, or a
// plane taller than the letterboxed video) this far inside it.
const EDGE_MARGIN_RATIO = 0.02;

export type PGSLoadState = "idle" | "loading" | "ready" | "error";

export interface UsePGSSubtitlesOptions {
  videoRef: RefObject<HTMLVideoElement | null>;
  canvasRef: RefObject<HTMLCanvasElement | null>;
  subtitleUrls: PlayerSubtitleInfo[];
  activeSubtitleIndex: number | null;
  /**
   * False when the client must not draw: the player is detached, or the plan
   * burns this track into the video already.
   */
  enabled: boolean;
  streamOriginSeconds: number;
  subtitleDelayMs: number;
  /** Known media duration in source seconds (0 when unknown). */
  durationRef: RefObject<number>;
  /** Media-time position playback is heading to before media has loaded. */
  fetchAnchorRef: RefObject<number>;
  videoFit: VideoFitMode;
  /**
   * Pixels at the bottom of the player covered by the control bar. Subtitles
   * placed in the lower half rise above it, like the text overlay does.
   */
  bottomInsetPx?: number;
  onLoadState?: (state: PGSLoadState) => void;
}

/**
 * Maps the composition plane onto the screen. HD planes keep their aspect,
 * scaled to the picture's width and centered on it: a 1920x1080 plane over a
 * 1920x800 scope encode puts subtitles into the letterbox, where the disc
 * placed them. SD planes are anamorphic, so they stretch to the picture.
 */
export function pgsPlaneRect(video: FitRect, planeWidth: number, planeHeight: number): FitRect {
  if (planeWidth <= 0 || planeHeight <= 0) return video;
  if (planeHeight <= 576) return video;
  const scale = video.width / planeWidth;
  const height = planeHeight * scale;
  return {
    x: video.x,
    y: video.y + (video.height - height) / 2,
    width: video.width,
    height,
  };
}

function objectSourceRect(object: PGSPlacedObject): FitRect {
  const crop = object.crop;
  if (!crop) return { x: 0, y: 0, width: object.width, height: object.height };
  const x = Math.min(crop.x, object.width);
  const y = Math.min(crop.y, object.height);
  return {
    x,
    y,
    width: Math.max(0, Math.min(crop.width, object.width - x)),
    height: Math.max(0, Math.min(crop.height, object.height - y)),
  };
}

function appendWindow(url: string, position: number): string {
  const separator = url.includes("?") ? "&" : "?";
  return `${url}${separator}windowed=1&position=${position}&duration=${SUBTITLE_WINDOW_SECONDS}`;
}

/**
 * Renders the selected embedded PGS track on a canvas over the video.
 *
 * The server serves embedded PGS losslessly as `.sup` with absolute source
 * timestamps once the client declares `embedded_bitmap`, so nothing has to be
 * burned into the video. Display sets are fetched in positioned windows,
 * parsed incrementally, and drawn whenever the composition on screen changes.
 */
export function usePGSSubtitles({
  videoRef,
  canvasRef,
  subtitleUrls,
  activeSubtitleIndex,
  enabled,
  streamOriginSeconds,
  subtitleDelayMs,
  durationRef,
  fetchAnchorRef,
  videoFit,
  bottomInsetPx = 0,
  onLoadState,
}: UsePGSSubtitlesOptions): { isActive: boolean } {
  const onLoadStateRef = useRef(onLoadState);
  onLoadStateRef.current = onLoadState;
  const timingRef = useRef({ origin: streamOriginSeconds, delayMs: subtitleDelayMs });
  timingRef.current = { origin: streamOriginSeconds, delayMs: subtitleDelayMs };
  const videoFitRef = useRef(videoFit);
  videoFitRef.current = videoFit;
  const bottomInsetRef = useRef(bottomInsetPx);
  bottomInsetRef.current = bottomInsetPx;
  // Forces a repaint on the next frame without restarting the loader.
  const layoutRevisionRef = useRef(0);

  const activeSub =
    activeSubtitleIndex !== null
      ? (subtitleUrls.find((track) => track.index === activeSubtitleIndex) ?? null)
      : null;
  const activeUrl =
    enabled && activeSub && isPGSCodec(activeSub.codec) && activeSub.url ? activeSub.url : null;

  useEffect(() => {
    layoutRevisionRef.current += 1;
  }, [videoFit, subtitleDelayMs, streamOriginSeconds, bottomInsetPx]);

  useEffect(() => {
    const video = videoRef.current;
    const canvas = canvasRef.current;
    if (!activeUrl || !video || !canvas) {
      onLoadStateRef.current?.("idle");
      return;
    }
    const videoEl: HTMLVideoElement = video;
    const canvasEl: HTMLCanvasElement = canvas;
    const url: string = activeUrl;

    let cancelled = false;
    let timeline: PGSComposition[] = [];
    let coverageStart = 0;
    let windowEnd = 0;
    let atEOF = false;
    let hasFetched = false;
    let inflight: AbortController | null = null;
    let inflightStart = 0;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let lastFetchFailureAt = 0;
    let retryDelay = 0;
    let frameRequest = 0;
    let painted: { composition: PGSComposition | null; key: string } = {
      composition: null,
      key: "",
    };
    // Only the screen being drawn keeps decoded bitmaps; a feature-length
    // track would otherwise hold every subtitle it has shown.
    let bitmaps = new Map<PGSPlacedObject, CanvasImageSource | null>();

    function sourceTime(): number {
      const { origin, delayMs } = timingRef.current;
      const mediaTime =
        videoEl.readyState > 0
          ? toMediaTime(videoEl.currentTime, origin)
          : (fetchAnchorRef.current ?? 0);
      return mediaTime - delayMs / 1000;
    }

    function bitmapFor(object: PGSPlacedObject): CanvasImageSource | null {
      const cached = bitmaps.get(object);
      if (cached !== undefined) return cached;
      if (object.width * object.height > MAX_OBJECT_PIXELS) return null;
      const surface = document.createElement("canvas");
      surface.width = object.width;
      surface.height = object.height;
      const context = surface.getContext("2d");
      let bitmap: CanvasImageSource | null = null;
      if (context) {
        const pixels = decodePGSObject(object);
        context.putImageData(
          new ImageData(new Uint8ClampedArray(pixels.buffer), object.width, object.height),
          0,
          0,
        );
        bitmap = surface;
      }
      bitmaps.set(object, bitmap);
      return bitmap;
    }

    function paint(composition: PGSComposition | null) {
      const boxWidth = canvasEl.clientWidth;
      const boxHeight = canvasEl.clientHeight;
      const ratio = window.devicePixelRatio || 1;
      const pixelWidth = Math.max(1, Math.round(boxWidth * ratio));
      const pixelHeight = Math.max(1, Math.round(boxHeight * ratio));
      if (canvasEl.width !== pixelWidth || canvasEl.height !== pixelHeight) {
        canvasEl.width = pixelWidth;
        canvasEl.height = pixelHeight;
      }
      const context = canvasEl.getContext("2d");
      if (!context) return;
      context.setTransform(1, 0, 0, 1, 0, 0);
      context.clearRect(0, 0, pixelWidth, pixelHeight);
      const previous = bitmaps;
      bitmaps = new Map();
      if (!composition || composition.objects.length === 0) return;
      for (const object of composition.objects) {
        const kept = previous.get(object);
        if (kept !== undefined) bitmaps.set(object, kept);
      }
      context.setTransform(ratio, 0, 0, ratio, 0, 0);
      context.imageSmoothingEnabled = true;
      context.imageSmoothingQuality = "high";

      const picture = videoContentRect(
        boxWidth,
        boxHeight,
        videoEl.videoWidth || composition.width,
        videoEl.videoHeight || composition.height,
        videoFitRef.current,
      );
      const plane = pgsPlaneRect(picture, composition.width, composition.height);
      const scaleX = plane.width / composition.width;
      const scaleY = plane.height / composition.height;
      const margin = boxHeight * EDGE_MARGIN_RATIO;
      for (const object of composition.objects) {
        const source = objectSourceRect(object);
        if (source.width === 0 || source.height === 0) continue;
        const bitmap = bitmapFor(object);
        if (!bitmap) continue;
        const width = source.width * scaleX;
        const height = source.height * scaleY;
        const x = plane.x + object.x * scaleX;
        let y = plane.y + object.y * scaleY;
        const lowerHalf = y + height / 2 > boxHeight / 2;
        const floor = boxHeight - margin - (lowerHalf ? bottomInsetRef.current : 0);
        if (y + height > floor) y = floor - height;
        if (y < margin) y = Math.min(margin, boxHeight - height);
        context.drawImage(
          bitmap,
          source.x,
          source.y,
          source.width,
          source.height,
          x,
          y,
          width,
          height,
        );
      }
    }

    function renderFrame() {
      frameRequest = 0;
      if (cancelled) return;
      const time = sourceTime();
      const index = pgsCompositionIndexAt(timeline, time);
      const composition = index >= 0 ? timeline[index]! : null;
      const key = `${canvasEl.clientWidth}x${canvasEl.clientHeight}:${videoEl.videoWidth}x${videoEl.videoHeight}:${layoutRevisionRef.current}`;
      if (composition !== painted.composition || key !== painted.key) {
        painted = { composition, key };
        try {
          paint(composition);
        } catch (error) {
          // A damaged display set costs that screen, not the rest of the track.
          console.error("[usePGSSubtitles] Could not draw a subtitle:", error);
        }
      }
      frameRequest = requestAnimationFrame(renderFrame);
    }

    async function fetchWindow(seekStart: number, resetExisting: boolean) {
      inflight?.abort();
      const controller = new AbortController();
      inflight = controller;
      inflightStart = seekStart;
      if (retryTimer !== null) clearTimeout(retryTimer);
      if (resetExisting) {
        onLoadStateRef.current?.("loading");
        timeline = [];
        coverageStart = seekStart;
        windowEnd = seekStart;
        atEOF = false;
      }
      const requestedEnd = seekStart + SUBTITLE_WINDOW_SECONDS;
      let stallTimer: ReturnType<typeof setTimeout> | null = null;
      const armStallTimer = () => {
        if (stallTimer !== null) clearTimeout(stallTimer);
        stallTimer = setTimeout(() => controller.abort(), SUBTITLE_FETCH_STALL_TIMEOUT_MS);
      };
      // Each window is its own epoch history: display sets near its start may
      // reference objects defined before it, and the merge keeps the copy the
      // earlier window resolved.
      const parser = new PGSStreamParser();
      let succeeded = false;
      try {
        armStallTimer();
        const response = await fetch(appendWindow(url, seekStart), { signal: controller.signal });
        if (!response.ok || !response.body) {
          console.error(`[usePGSSubtitles] Failed to fetch ${url}: ${response.status}`);
          return;
        }
        const reader = response.body.getReader();
        while (!cancelled) {
          armStallTimer();
          const { value, done } = await reader.read();
          if (cancelled || controller.signal.aborted || inflight !== controller) return;
          if (done) break;
          const parsed = parser.push(value);
          if (parsed.length > 0) {
            timeline = mergePGSCompositions(timeline, parsed);
            onLoadStateRef.current?.("ready");
          }
        }
        succeeded = true;
      } catch (error) {
        if ((error as Error).name !== "AbortError") {
          console.error("[usePGSSubtitles] Stream error:", error);
        }
      } finally {
        if (stallTimer !== null) clearTimeout(stallTimer);
        const superseded = inflight !== controller;
        if (inflight === controller) inflight = null;
        if (succeeded && !cancelled && !superseded) {
          onLoadStateRef.current?.("ready");
          hasFetched = true;
          retryDelay = 0;
          lastFetchFailureAt = 0;
          windowEnd = Math.max(windowEnd, requestedEnd);
          const duration = durationRef.current ?? 0;
          if (duration > 0 && requestedEnd >= duration) atEOF = true;
        } else if (!succeeded && !superseded && !cancelled) {
          lastFetchFailureAt = Date.now();
          onLoadStateRef.current?.("error");
          retryDelay = nextSubtitleRetryDelay(retryDelay);
          retryTimer = setTimeout(maybeFetch, retryDelay);
        }
      }
    }

    function maybeFetch() {
      if (cancelled) return;
      const action = planSubtitleWindow(
        {
          hasFetched,
          coverageStart,
          windowEnd,
          atEOF,
          inflightStart: inflight ? inflightStart : null,
          failedAt: lastFetchFailureAt,
          retryDelayMs: retryDelay,
        },
        Math.max(0, sourceTime()),
        Date.now(),
        SEEK_BACKOFF,
      );
      if (action.kind === "fresh") void fetchWindow(action.start, true);
      else if (action.kind === "extend") void fetchWindow(action.start, false);
    }

    maybeFetch();
    videoEl.addEventListener("timeupdate", maybeFetch);
    videoEl.addEventListener("seeking", maybeFetch);
    videoEl.addEventListener("seeked", maybeFetch);
    frameRequest = requestAnimationFrame(renderFrame);

    return () => {
      cancelled = true;
      if (retryTimer !== null) clearTimeout(retryTimer);
      if (frameRequest) cancelAnimationFrame(frameRequest);
      inflight?.abort();
      inflight = null;
      videoEl.removeEventListener("timeupdate", maybeFetch);
      videoEl.removeEventListener("seeking", maybeFetch);
      videoEl.removeEventListener("seeked", maybeFetch);
      canvasEl.getContext("2d")?.clearRect(0, 0, canvasEl.width, canvasEl.height);
    };
  }, [activeUrl, canvasRef, durationRef, fetchAnchorRef, videoRef]);

  return { isActive: activeUrl !== null };
}
