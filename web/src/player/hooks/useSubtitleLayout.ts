import { useEffect, useMemo, useState, type CSSProperties, type RefObject } from "react";
import { computeSubtitleFontScale, computeSubtitlePositionStyle } from "@/lib/subtitleAppearance";
import type { SubtitleAppearance } from "@/lib/subtitleAppearance";

export interface SubtitleLayout {
  positionStyle: CSSProperties;
  /** Multiplier for cue font size so text scales with the rendered video. */
  fontScale: number;
}

/**
 * Tracks the player container size and the video's intrinsic aspect ratio,
 * then produces a position CSS style and a font scale. The Bottom position is
 * anchored to the player window, while Lower Third and Top are anchored to a
 * 16:9 reference frame centered on the actually-rendered video area
 * (object-fit: contain). Falls back to container-relative percentages and
 * scale 1 until measurements are available.
 */
export function useSubtitleLayout(
  containerRef: RefObject<HTMLElement | null>,
  videoRef: RefObject<HTMLVideoElement | null>,
  position: SubtitleAppearance["position"],
): SubtitleLayout {
  const [playerSize, setPlayerSize] = useState<{ w: number; h: number }>({ w: 0, h: 0 });
  const [videoAspect, setVideoAspect] = useState(0);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const update = () => setPlayerSize({ w: el.clientWidth, h: el.clientHeight });
    update();
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
  }, [containerRef]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    const update = () => {
      if (video.videoWidth > 0 && video.videoHeight > 0) {
        setVideoAspect(video.videoWidth / video.videoHeight);
      }
    };
    update();
    video.addEventListener("loadedmetadata", update);
    video.addEventListener("resize", update);
    return () => {
      video.removeEventListener("loadedmetadata", update);
      video.removeEventListener("resize", update);
    };
  }, [videoRef]);

  return useMemo(
    () => ({
      positionStyle: computeSubtitlePositionStyle(
        position,
        playerSize.w,
        playerSize.h,
        videoAspect,
      ),
      fontScale: computeSubtitleFontScale(playerSize.w, playerSize.h, videoAspect),
    }),
    [position, playerSize.w, playerSize.h, videoAspect],
  );
}
