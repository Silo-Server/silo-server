import { useEffect, useRef } from "react";
import type { PgsRenderer } from "libpgs";
import libpgsWorkerUrl from "libpgs/dist/libpgs.worker.js?url";
import type { PlayerSubtitleInfo } from "../types";
import { isPGSCodec } from "../utils/subtitleCodecs";
import type { SubtitleAppearance } from "../../lib/subtitleAppearance";
import { computePgsPlacements, computeVideoRect, detectCueRegions } from "../utils/pgsPlacement";

// Downsampled scan width for cue-region detection. Region rects are scaled
// back up to source pixels, so the ±(source/scanWidth) rounding is invisible.
const SCAN_WIDTH = 480;

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
 * Coordination mirrors useASSSubtitles: the `isActive` return value tells the
 * player to suppress the VTT text overlay while bitmap rendering is active.
 */
export function usePGSSubtitles(
  videoRef: React.RefObject<HTMLVideoElement | null>,
  subtitleUrls: PlayerSubtitleInfo[],
  activeSubtitleIndex: number | null,
  isDetached: boolean,
  streamOriginSeconds: number,
  subtitleDelayMs: number,
  appearance: SubtitleAppearance,
): { isActive: boolean } {
  const rendererRef = useRef<PgsRenderer | null>(null);
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
      const placements = computePgsPlacements(regions, srcW, srcH, videoRect, a);

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
        subUrl: activeUrl!,
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
      // on the same events) and when the layout changes.
      video.addEventListener("timeupdate", compositeWithFollowUp);
      video.addEventListener("seeked", compositeWithFollowUp);
      resizeObserver = new ResizeObserver(() => composite());
      resizeObserver.observe(video);
      composite();
    }

    void initRenderer();

    return () => {
      cancelled = true;
      compositeRef.current = null;
      if (followUpTimer) clearTimeout(followUpTimer);
      video.removeEventListener("timeupdate", compositeWithFollowUp);
      video.removeEventListener("seeked", compositeWithFollowUp);
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

  return { isActive: isPGS && !isDetached };
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
