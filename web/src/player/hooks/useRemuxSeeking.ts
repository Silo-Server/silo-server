import { useCallback } from "react";

/**
 * Provides a seek function for progressive streams.
 *
 * HLS routes seek against the plan's timeline through hls.js, so this hook only
 * covers the progressive and direct-play cases, where the element's own
 * `currentTime` is the whole story.
 */
export function useRemuxSeeking(videoRef: React.RefObject<HTMLVideoElement | null>): {
  handleSeek: (seconds: number) => void;
} {
  const handleSeek = useCallback(
    (seconds: number) => {
      const video = videoRef.current;
      if (!video) return;
      video.currentTime = seconds;
    },
    [videoRef],
  );

  return { handleSeek };
}
