import "@/styles/player-hud.css";
import { useEffect, useState } from "react";
import { X } from "lucide-react";
import { buildPlaybackInfoSections, type RuntimePlaybackStats } from "../playback-info";
import type { PlanV3 } from "../protocol-v3";
import type { PlayerFileVersion } from "../types";
import { useT } from "@/i18n";

interface PlaybackInfoOverlayProps {
  videoRef: React.RefObject<HTMLVideoElement | null>;
  containerRef: React.RefObject<HTMLDivElement | null>;
  streamUrl: string;
  /** The route the server chose, and the only description of what is on the wire. */
  plan: PlanV3;
  currentSourceVersion?: PlayerFileVersion;
  requestedVersion?: PlayerFileVersion;
  /** True while the browser decode engine plays the stream. */
  browserEngine?: boolean;
  onClose: () => void;
}

export function PlaybackInfoOverlay({
  videoRef,
  containerRef,
  streamUrl,
  plan,
  currentSourceVersion,
  requestedVersion,
  browserEngine = false,
  onClose,
}: PlaybackInfoOverlayProps) {
  const t = useT();
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

  // Rebuilt on every render: the stats change every second anyway, and the
  // rows follow a language change through `t()`.
  const sections = buildPlaybackInfoSections({
    streamUrl,
    plan,
    currentSourceVersion,
    requestedVersion,
    runtimeStats,
    browserEngine,
  });

  return (
    <div
      // A labelled region, not a dialog: it is not modal, and a dialog would
      // switch off the player's shortcuts and the skip pill's focus.
      role="region"
      aria-label={t("web.player.info.heading")}
      className="hud-panel hud-panel-scroll absolute top-[max(5rem,calc(env(safe-area-inset-top)+4.5rem))] left-[max(1rem,env(safe-area-inset-left))] z-50 max-h-[max(10rem,calc(100%-14rem))] w-[min(20rem,calc(100vw-2rem))] sm:left-[max(2rem,env(safe-area-inset-left))] rtl:right-[max(1rem,env(safe-area-inset-right))] rtl:left-auto sm:rtl:right-[max(2rem,env(safe-area-inset-right))]"
    >
      <div className="hud-panel-header pe-2">
        <span>{t("web.player.info.heading")}</span>
        <button
          type="button"
          onClick={onClose}
          className="hud-icon-btn"
          aria-label={t("web.player.info.close")}
        >
          <X />
        </button>
      </div>

      <div className="px-4 pb-3">
        {sections.map((section) => (
          <section key={section.title} className="mt-3 first:mt-0">
            <h3 className="mb-1 text-[0.6875rem] font-bold tracking-[0.1em] text-[var(--hud-ash)] uppercase">
              {section.title}
            </h3>
            <dl>
              {section.rows.map((row) => (
                <div key={row.label} className="flex justify-between gap-4 py-0.5 text-[0.8125rem]">
                  <dt className="shrink-0 text-[var(--hud-ash)]">{row.label}</dt>
                  <dd className="truncate text-end tabular-nums" title={row.value}>
                    {row.value}
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </div>
    </div>
  );
}
