import { useEffect, useMemo, useState } from "react";
import { X } from "lucide-react";
import { buildPlaybackInfoSections, type RuntimePlaybackStats } from "../playback-info";
import type { PlanV3 } from "../protocol-v3";
import type { PlayerFileVersion } from "../types";

interface PlaybackInfoOverlayProps {
  videoRef: React.RefObject<HTMLVideoElement | null>;
  containerRef: React.RefObject<HTMLDivElement | null>;
  streamUrl: string;
  /** The route the server chose, and the only description of what is on the wire. */
  plan: PlanV3;
  currentSourceVersion?: PlayerFileVersion;
  requestedVersion?: PlayerFileVersion;
  onClose: () => void;
}

export function PlaybackInfoOverlay({
  videoRef,
  containerRef,
  streamUrl,
  plan,
  currentSourceVersion,
  requestedVersion,
  onClose,
}: PlaybackInfoOverlayProps) {
  const [runtimeStats, setRuntimeStats] = useState<RuntimePlaybackStats>({});

  // Poll runtime stats every second.
  useEffect(() => {
    function collect() {
      const video = videoRef.current;
      const container = containerRef.current;
      if (!video) return;

      const quality = (
        video as HTMLVideoElement & { getVideoPlaybackQuality?: () => VideoPlaybackQuality }
      ).getVideoPlaybackQuality?.();

      setRuntimeStats({
        playerWidth: container?.clientWidth,
        playerHeight: container?.clientHeight,
        videoWidth: video.videoWidth || undefined,
        videoHeight: video.videoHeight || undefined,
        droppedFrames: quality?.droppedVideoFrames ?? null,
        corruptedFrames: quality?.corruptedVideoFrames ?? null,
      });
    }

    collect();
    const id = setInterval(collect, 1000);
    return () => clearInterval(id);
  }, [videoRef, containerRef]);

  const sections = useMemo(
    () =>
      buildPlaybackInfoSections({
        streamUrl,
        plan,
        currentSourceVersion,
        requestedVersion,
        runtimeStats,
      }),
    [streamUrl, plan, currentSourceVersion, requestedVersion, runtimeStats],
  );

  return (
    <div className="absolute top-12 left-4 z-50 max-h-[calc(100%-6rem)] w-80 overflow-y-auto rounded-lg bg-black/85 text-sm text-white shadow-lg backdrop-blur-sm">
      <div className="flex items-center justify-between px-4 pt-3 pb-2">
        <span className="font-medium text-white/90">Playback Info</span>
        <button
          type="button"
          onClick={onClose}
          className="flex h-6 w-6 items-center justify-center rounded hover:bg-white/10"
          aria-label="Close playback info"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      <div className="px-4 pb-3">
        {sections.map((section) => (
          <div key={section.title} className="mt-3 first:mt-0">
            <div className="mb-1 text-xs font-semibold tracking-wider text-white/50 uppercase">
              {section.title}
            </div>
            {section.rows.map((row) => (
              <div key={row.label} className="flex justify-between gap-4 py-0.5">
                <span className="shrink-0 text-white/60">{row.label}</span>
                <span className="truncate text-right text-white/90">{row.value}</span>
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}
