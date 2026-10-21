import { useEffect, useRef, type RefObject } from "react";
import type { PlayerSubtitleInfo, VideoFitMode } from "../types";
import { videoContentRect, type FitRect } from "../utils/videoFit";
import { isPGSCodec } from "../utils/subtitleCodecs";
import {
  decodePGSObject,
  mergePGSCompositions,
  pgsCompositionAt,
  PGSStreamParser,
  prunePGSTimeline,
  type PGSComposition,
  type PGSPlacedObject,
} from "../utils/pgs";
import {
  createSubtitleWindowFetcher,
  SUBTITLE_WINDOW_SECONDS,
  subtitlePlayheadSeconds,
  subtitleWindowUrl,
} from "../utils/subtitleWindows";

// A PGS composition stays on screen until the next one replaces it, and the
// server's positioned extract starts at the window, so a fresh window reaches
// back far enough to include a subtitle already showing at the playhead.
const SEEK_BACKOFF = 10;
// Decoding is the costly step, and a crafted stream could redefine a huge
// object on every display set. Real streams stay well under this, even a
// full-plane fade at 24 frames a second (about 50 million pixels).
const MAX_DECODED_PIXELS_PER_SECOND = 4 * 4096 * 4096;
// Keeps a subtitle that would land past the player's edge (a Fill crop, or a
// plane larger than the cropped video) this far inside it.
const EDGE_MARGIN_RATIO = 0.02;

interface DecodedBitmap {
  rle: Uint8Array;
  palette: Uint32Array;
  bitmap: CanvasImageSource | null;
}

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
   * Pixels at the bottom of the player that subtitles placed in the lower
   * half must stay above: the control bar and its gap while the bar is up.
   */
  bottomInsetPx?: number;
  onLoadState?: (state: PGSLoadState) => void;
}

/**
 * Maps the composition plane onto the screen. An HD plane is the disc's full
 * frame and the picture is that frame, maybe cropped, so the plane keeps its
 * aspect, matches the picture along the uncropped side, and is centered on
 * it: a 1920x1080 plane over a 1920x800 scope encode reaches into the
 * letterbox, and over a 1440x1080 pillarbox crop into the side bars, where
 * the disc placed the subtitles. SD planes are anamorphic, so they stretch to
 * the picture.
 */
export function pgsPlaneRect(video: FitRect, planeWidth: number, planeHeight: number): FitRect {
  if (planeWidth <= 0 || planeHeight <= 0) return video;
  if (planeHeight <= 576) return video;
  const scale = Math.max(video.width / planeWidth, video.height / planeHeight);
  const width = planeWidth * scale;
  const height = planeHeight * scale;
  return {
    x: video.x + (video.width - width) / 2,
    y: video.y + (video.height - height) / 2,
    width,
    height,
  };
}

/** Moves a span inside [min, max], centered there when it is too long to fit. */
function clampSpan(start: number, size: number, min: number, max: number): number {
  if (size > max - min) return min + (max - min - size) / 2;
  return Math.min(Math.max(start, min), max - size);
}

/**
 * Where a composition's objects land in the player box. Each object keeps its
 * place on the plane. Objects in the same half of the box then move and shrink
 * as one block, so the two lines of a subtitle stay apart and at one scale:
 * the block stays inside the box with a small margin, shrinking when it does
 * not fit (a Fill crop on a narrow screen, or a tall subtitle over a short
 * player), and a lower-half block also stays above `bottomInsetPx`.
 */
export function placePGSObjects({
  objects,
  plane,
  planeWidth,
  planeHeight,
  boxWidth,
  boxHeight,
  bottomInsetPx,
}: {
  objects: { object: { x: number; y: number }; source: FitRect }[];
  plane: FitRect;
  planeWidth: number;
  planeHeight: number;
  boxWidth: number;
  boxHeight: number;
  bottomInsetPx: number;
}): FitRect[] {
  const scaleX = plane.width / planeWidth;
  const scaleY = plane.height / planeHeight;
  const marginX = boxWidth * EDGE_MARGIN_RATIO;
  const marginY = boxHeight * EDGE_MARGIN_RATIO;
  const onPlane = objects.map(({ object, source }) => ({
    x: plane.x + object.x * scaleX,
    y: plane.y + object.y * scaleY,
    width: source.width * scaleX,
    height: source.height * scaleY,
  }));
  const placed = [...onPlane];
  for (const lowerHalf of [false, true]) {
    const group = onPlane
      .map((rect, index) => ({ rect, index }))
      .filter(({ rect }) => rect.y + rect.height / 2 > boxHeight / 2 === lowerHalf);
    if (group.length === 0) continue;
    const left = Math.min(...group.map(({ rect }) => rect.x));
    const top = Math.min(...group.map(({ rect }) => rect.y));
    const width = Math.max(...group.map(({ rect }) => rect.x + rect.width)) - left;
    const height = Math.max(...group.map(({ rect }) => rect.y + rect.height)) - top;
    const bottom = boxHeight - Math.max(marginY, lowerHalf ? bottomInsetPx : 0);
    const roomX = boxWidth - 2 * marginX;
    const roomY = bottom - marginY;
    const shrink = Math.max(0, Math.min(1, roomX / width, roomY / height));
    const blockWidth = width * shrink;
    const blockHeight = height * shrink;
    const blockX = clampSpan(
      left + (width - blockWidth) / 2,
      blockWidth,
      marginX,
      boxWidth - marginX,
    );
    const blockY = clampSpan(top + (height - blockHeight) / 2, blockHeight, marginY, bottom);
    for (const { rect, index } of group) {
      placed[index] = {
        x: blockX + (rect.x - left) * shrink,
        y: blockY + (rect.y - top) * shrink,
        width: rect.width * shrink,
        height: rect.height * shrink,
      };
    }
  }
  return placed;
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
    // Carried across contiguous windows so an epoch that began in one window
    // still resolves in the next; a fresh window starts a new one.
    let parser: PGSStreamParser | null = null;
    let frameRequest = 0;
    let painted: { composition: PGSComposition | null; key: string } = {
      composition: null,
      key: "",
    };
    // Only the last screen drawn keeps decoded bitmaps; a feature-length
    // track would otherwise hold every subtitle it has shown. They are keyed by
    // the object's pixel data and palette, which display sets share by
    // reference while the definitions are unchanged, so a screen that is
    // placed again or repeated at an acquisition point is not decoded again.
    let bitmaps: DecodedBitmap[] = [];
    let decodeBudget = { since: 0, pixels: 0 };

    function sourceTime(): number {
      const { origin, delayMs } = timingRef.current;
      return subtitlePlayheadSeconds(videoEl, origin, fetchAnchorRef.current) - delayMs / 1000;
    }

    function bitmapFor(
      object: PGSPlacedObject,
      previous: DecodedBitmap[],
    ): CanvasImageSource | null {
      const same = (entry: DecodedBitmap) =>
        entry.rle === object.rle && entry.palette === object.palette;
      const reused = bitmaps.find(same) ?? previous.find(same);
      if (reused) {
        if (!bitmaps.includes(reused)) bitmaps.push(reused);
        return reused.bitmap;
      }
      const now = performance.now();
      if (now - decodeBudget.since > 1000) decodeBudget = { since: now, pixels: 0 };
      decodeBudget.pixels += object.width * object.height;
      if (decodeBudget.pixels > MAX_DECODED_PIXELS_PER_SECOND) return null;
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
      bitmaps.push({ rle: object.rle, palette: object.palette, bitmap });
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
      // A clear keeps the last screen's bitmaps, so a screen that comes back
      // after it is not decoded again.
      if (!composition || composition.objects.length === 0) return;
      const previous = bitmaps;
      bitmaps = [];
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
      const drawable = composition.objects.flatMap((object) => {
        const source = objectSourceRect(object);
        if (source.width === 0 || source.height === 0) return [];
        const bitmap = bitmapFor(object, previous);
        return bitmap ? [{ object, source, bitmap }] : [];
      });
      const targets = placePGSObjects({
        objects: drawable,
        plane,
        planeWidth: composition.width,
        planeHeight: composition.height,
        boxWidth,
        boxHeight,
        bottomInsetPx: bottomInsetRef.current,
      });
      drawable.forEach(({ source, bitmap }, index) => {
        const target = targets[index];
        if (!target) return;
        context.drawImage(
          bitmap,
          source.x,
          source.y,
          source.width,
          source.height,
          target.x,
          target.y,
          target.width,
          target.height,
        );
      });
    }

    function renderFrame() {
      frameRequest = 0;
      if (cancelled) return;
      // The clear for the last screen may not have arrived yet, so the
      // timeline only reaches the last finished window or, past it, the
      // latest segment the parser has read.
      const { windowEnd } = fetcher.coverage();
      const loadedUntil = Math.max(windowEnd, parser?.receivedUntil ?? -Infinity) + 1;
      const composition = pgsCompositionAt(timeline, sourceTime(), loadedUntil);
      const key = `${canvasEl.clientWidth}x${canvasEl.clientHeight}@${window.devicePixelRatio}:${videoEl.videoWidth}x${videoEl.videoHeight}:${layoutRevisionRef.current}`;
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

    const fetcher = createSubtitleWindowFetcher({
      windowUrl: (start) => subtitleWindowUrl(url, start, "windowed=1"),
      playhead: () => Math.max(0, sourceTime()),
      duration: () => durationRef.current ?? 0,
      seekBackoffSeconds: SEEK_BACKOFF,
      logTag: "usePGSSubtitles",
      onWindowStart: (fresh) => {
        if (fresh) {
          onLoadStateRef.current?.("loading");
          timeline = [];
        }
        // A fresh window starts a new epoch history. An extend continues the
        // last one, so display sets past the join can still place objects the
        // previous window defined; the repeats in the overlap keep the copy the
        // earlier window resolved.
        if (fresh || !parser) parser = new PGSStreamParser();
        else parser.resetBuffer();
      },
      onChunk: (chunk) => {
        const parsed = parser?.push(chunk) ?? [];
        if (parsed.length === 0) return;
        timeline = mergePGSCompositions(timeline, parsed);
        onLoadStateRef.current?.("ready");
      },
      onWindowCommitted: (fresh) => {
        onLoadStateRef.current?.("ready");
        // Keep about one window behind the playhead, so a straight run
        // through a long film holds a bounded number of screens.
        const keepFrom = sourceTime() - SUBTITLE_WINDOW_SECONDS;
        if (!fresh && keepFrom > fetcher.coverage().coverageStart) {
          timeline = prunePGSTimeline(timeline, keepFrom);
          fetcher.releaseBefore(keepFrom);
        }
      },
      onWindowFailed: () => onLoadStateRef.current?.("error"),
    });
    const maybeFetch = fetcher.maybeFetch;

    maybeFetch();
    videoEl.addEventListener("timeupdate", maybeFetch);
    videoEl.addEventListener("seeking", maybeFetch);
    videoEl.addEventListener("seeked", maybeFetch);
    frameRequest = requestAnimationFrame(renderFrame);

    return () => {
      cancelled = true;
      fetcher.dispose();
      if (frameRequest) cancelAnimationFrame(frameRequest);
      videoEl.removeEventListener("timeupdate", maybeFetch);
      videoEl.removeEventListener("seeking", maybeFetch);
      videoEl.removeEventListener("seeked", maybeFetch);
      canvasEl.getContext("2d")?.clearRect(0, 0, canvasEl.width, canvasEl.height);
    };
  }, [activeUrl, canvasRef, durationRef, fetchAnchorRef, videoRef]);

  return { isActive: activeUrl !== null };
}
