import { useEffect, useRef } from "react";
import type { PgsRenderer } from "libpgs";
import libpgsWorkerUrl from "libpgs/dist/libpgs.worker.js?url";
import type { PlayerSubtitleInfo } from "../types";
import { isPGSCodec } from "../utils/subtitleCodecs";

/**
 * Manages client-side PGS (Blu-ray bitmap) subtitle rendering via libpgs.
 *
 * When a PGS-codec subtitle track is active, this hook lazy-loads libpgs and
 * creates a PgsRenderer attached to the video element. The renderer's worker
 * fetches the .sup stream itself (the URL already carries auth via ?token=),
 * decodes display sets progressively as bytes arrive, and draws them onto a
 * canvas it positions over the video. When a non-PGS track is selected (or
 * subtitles are turned off), the renderer is disposed.
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

  // Resolve the active subtitle track.
  const activeSub =
    activeSubtitleIndex !== null
      ? (subtitleUrls.find((s) => s.index === activeSubtitleIndex) ?? null)
      : null;

  const isPGS = activeSub !== null && isPGSCodec(activeSub.codec);
  const activeUrl = isPGS ? activeSub.url : null;

  // Main effect: create/destroy the renderer based on the active track.
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

    async function initRenderer() {
      if (!video || cancelled) return;

      // Lazy-load the libpgs module (only once).
      if (!libpgsImportRef.current) {
        libpgsImportRef.current = import("libpgs");
      }
      const { PgsRenderer: PgsRendererClass } = await libpgsImportRef.current;
      if (cancelled) return;

      const renderer = new PgsRendererClass({
        video,
        subUrl: activeUrl!,
        workerUrl: libpgsWorkerUrl,
        timeOffset: offsetRef.current,
        // Must match the video element's object-fit so subtitle positions
        // stay anchored to the video content box, not the element box.
        aspectRatio: "contain",
      });

      // Guard against the effect being cleaned up while the constructor ran.
      if (cancelled) {
        renderer.dispose();
        return;
      }

      rendererRef.current = renderer;
    }

    void initRenderer();

    return () => {
      cancelled = true;
      // dispose() removes the canvas and terminates the worker, which also
      // abandons its in-flight .sup fetch — no separate AbortController.
      if (rendererRef.current) {
        rendererRef.current.dispose();
        rendererRef.current = null;
      }
    };
    // videoRef is a stable ref object. effectiveOffset is read from
    // offsetRef inside the async function to always get the latest value.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeUrl, isDetached]);

  // Update the renderer's time offset when the media timeline remaps or the
  // user nudges subtitle sync — without recreating the renderer.
  useEffect(() => {
    const renderer = rendererRef.current;
    if (!renderer || !activeUrl) return;
    renderer.timeOffset = effectiveOffset;
  }, [effectiveOffset, activeUrl]);

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
