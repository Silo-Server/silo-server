import { useCallback, useMemo, useRef, useState } from "react";
import type { MarkerKind, MarkerRegionView, PlayerChapter } from "../types";

interface SeekBarProps {
  currentTime: number;
  duration: number;
  buffered: TimeRanges | null;
  chapters?: PlayerChapter[];
  /** Marker regions to highlight on the track (intro/recap/credits/preview). */
  regions?: MarkerRegionView[];
  /** When true, the active region shows draggable start/end handles. */
  editing?: boolean;
  /** Which region's handles are draggable while editing. */
  activeEditKind?: MarkerKind | null;
  /** Fires continuously while a handle is dragged. */
  onRegionEdgeChange?: (kind: MarkerKind, edge: "start" | "end", seconds: number) => void;
  onSeek: (seconds: number) => void;
}

/** Region tint per marker kind (normal playback). */
const REGION_COLORS: Record<MarkerKind, string> = {
  intro: "bg-sky-300/40",
  recap: "bg-violet-300/40",
  credits: "bg-amber-300/45",
  preview: "bg-emerald-300/40",
};

/** Brighter tint used while editing so regions stand out against the track. */
const REGION_COLORS_EDIT: Record<MarkerKind, string> = {
  intro: "bg-sky-300/70",
  recap: "bg-violet-300/70",
  credits: "bg-amber-300/75",
  preview: "bg-emerald-300/70",
};

const REGION_DOT_COLORS: Record<MarkerKind, string> = {
  intro: "bg-sky-300",
  recap: "bg-violet-300",
  credits: "bg-amber-300",
  preview: "bg-emerald-300",
};

const MARKER_LABELS: Record<MarkerKind, string> = {
  intro: "Intro",
  recap: "Recap",
  credits: "Credits / Outro",
  preview: "Preview",
};

function findChapterAtTime(chapters: PlayerChapter[], time: number): PlayerChapter | null {
  for (const chapter of chapters) {
    if (time >= chapter.start_seconds && time < chapter.end_seconds) {
      return chapter;
    }
  }
  return chapters.length > 0 ? (chapters[chapters.length - 1] ?? null) : null;
}

function findRegionAtTime(regions: MarkerRegionView[], time: number): MarkerRegionView | null {
  let match: MarkerRegionView | null = null;
  for (const region of regions) {
    if (time < region.start || time > region.end) {
      continue;
    }
    if (!match || region.end - region.start < match.end - match.start) {
      match = region;
    }
  }
  return match;
}

function formatTime(seconds: number): string {
  const s = Math.floor(seconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) {
    return `${h}:${m.toString().padStart(2, "0")}:${sec.toString().padStart(2, "0")}`;
  }
  return `${m}:${sec.toString().padStart(2, "0")}`;
}

export function SeekBar({
  currentTime,
  duration,
  buffered,
  chapters = [],
  regions = [],
  editing = false,
  activeEditKind = null,
  onRegionEdgeChange,
  onSeek,
}: SeekBarProps) {
  const barRef = useRef<HTMLDivElement>(null);
  const [hoverTime, setHoverTime] = useState<number | null>(null);
  const [dragging, setDragging] = useState(false);
  const [edgeDrag, setEdgeDrag] = useState<"start" | "end" | null>(null);

  const getTimeFromClientX = useCallback(
    (clientX: number) => {
      const bar = barRef.current;
      if (!bar || duration <= 0) return 0;
      const rect = bar.getBoundingClientRect();
      const fraction = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
      return fraction * duration;
    },
    [duration],
  );

  const getTimeFromEvent = useCallback(
    (e: React.MouseEvent | MouseEvent) => getTimeFromClientX(e.clientX),
    [getTimeFromClientX],
  );

  const getTimeFromTouch = useCallback(
    (e: React.TouchEvent<HTMLDivElement>) => {
      const touch = e.touches[0] ?? e.changedTouches[0];
      if (!touch) return 0;
      return getTimeFromClientX(touch.clientX);
    },
    [getTimeFromClientX],
  );

  const [dragTime, setDragTime] = useState<number | null>(null);
  const hoverChapter = useMemo(
    () => (hoverTime === null ? null : findChapterAtTime(chapters, hoverTime)),
    [chapters, hoverTime],
  );
  const hoverRegion = useMemo(
    () => (hoverTime === null ? null : findRegionAtTime(regions, hoverTime)),
    [regions, hoverTime],
  );

  const handleMouseDown = useCallback(
    (e: React.MouseEvent) => {
      setDragging(true);
      const time = getTimeFromEvent(e);
      setDragTime(time);

      const handleMouseMove = (ev: MouseEvent) => {
        setDragTime(getTimeFromEvent(ev));
      };
      const handleMouseUp = (ev: MouseEvent) => {
        const finalTime = getTimeFromEvent(ev);
        onSeek(finalTime);
        setDragging(false);
        setDragTime(null);
        document.removeEventListener("mousemove", handleMouseMove);
        document.removeEventListener("mouseup", handleMouseUp);
      };
      document.addEventListener("mousemove", handleMouseMove);
      document.addEventListener("mouseup", handleMouseUp);
    },
    [getTimeFromEvent, onSeek],
  );

  const handleMouseMove = useCallback(
    (e: React.MouseEvent) => {
      if (!dragging) {
        setHoverTime(getTimeFromEvent(e));
      }
    },
    [dragging, getTimeFromEvent],
  );

  const handleTouchStart = useCallback(
    (e: React.TouchEvent<HTMLDivElement>) => {
      setDragging(true);
      const time = getTimeFromTouch(e);
      setDragTime(time);
    },
    [getTimeFromTouch],
  );

  const handleTouchMove = useCallback(
    (e: React.TouchEvent<HTMLDivElement>) => {
      if (!dragging) return;
      e.preventDefault();
      const time = getTimeFromTouch(e);
      setDragTime(time);
    },
    [dragging, getTimeFromTouch],
  );

  const handleTouchEnd = useCallback(
    (e: React.TouchEvent<HTMLDivElement>) => {
      if (!dragging) return;
      setDragging(false);
      const time = getTimeFromTouch(e);
      onSeek(time);
      setDragTime(null);
      setHoverTime(null);
    },
    [dragging, getTimeFromTouch, onSeek],
  );

  // Dragging a marker edge: its own pointer loop, isolated from the seek click.
  const handleEdgePointerDown = useCallback(
    (kind: MarkerKind, edge: "start" | "end") => (e: React.PointerEvent) => {
      if (!onRegionEdgeChange) return;
      e.preventDefault();
      e.stopPropagation();
      setEdgeDrag(edge);
      const move = (ev: PointerEvent) => {
        onRegionEdgeChange(kind, edge, getTimeFromClientX(ev.clientX));
      };
      const up = () => {
        setEdgeDrag(null);
        document.removeEventListener("pointermove", move);
        document.removeEventListener("pointerup", up);
      };
      document.addEventListener("pointermove", move);
      document.addEventListener("pointerup", up);
    },
    [getTimeFromClientX, onRegionEdgeChange],
  );

  const displayTime = dragTime ?? currentTime;
  const playedPercent = duration > 0 ? (displayTime / duration) * 100 : 0;

  const formatAriaValueText = useCallback((seconds: number): string => {
    const s = Math.floor(seconds);
    const m = Math.floor(s / 60);
    const sec = s % 60;
    if (m > 0 && sec > 0) return `${m} minutes ${sec} seconds`;
    if (m > 0) return `${m} minutes`;
    return `${sec} seconds`;
  }, []);

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      let newTime: number | null = null;
      switch (e.key) {
        case "ArrowRight":
          newTime = Math.min(duration, displayTime + 5);
          break;
        case "ArrowLeft":
          newTime = Math.max(0, displayTime - 5);
          break;
        case "Home":
          newTime = 0;
          break;
        case "End":
          newTime = duration;
          break;
        default:
          return;
      }
      e.preventDefault();
      onSeek(newTime);
    },
    [duration, displayTime, onSeek],
  );

  // Calculate all buffered ranges as percentages.
  const bufferedRanges: Array<{ startPercent: number; widthPercent: number }> = [];
  if (buffered && duration > 0) {
    for (let i = 0; i < buffered.length; i++) {
      const startPercent = (buffered.start(i) / duration) * 100;
      const endPercent = (buffered.end(i) / duration) * 100;
      bufferedRanges.push({ startPercent, widthPercent: endPercent - startPercent });
    }
  }

  const activeRegion =
    editing && activeEditKind ? (regions.find((r) => r.kind === activeEditKind) ?? null) : null;

  return (
    <div className="player-seekbar group/seek relative w-full px-2">
      {/* Hover time preview */}
      {hoverTime !== null && !dragging && edgeDrag === null && (
        <div
          className="pointer-events-none absolute z-10 flex flex-col items-center"
          style={{
            left: `clamp(24px, ${(hoverTime / (duration || 1)) * 100}%, calc(100% - 24px))`,
            bottom: "calc(100% + 8px)",
            transform: "translateX(-50%)",
          }}
        >
          <div
            className={[
              "overflow-hidden rounded-lg border border-white/[0.08] bg-neutral-900/95 text-white shadow-2xl backdrop-blur-md",
              hoverChapter || hoverRegion ? "w-44" : "",
            ].join(" ")}
          >
            {/* Thumbnail or chapter placeholder */}
            {hoverChapter &&
              (hoverChapter.thumbnail_url ? (
                <img
                  src={hoverChapter.thumbnail_url}
                  alt={hoverChapter.title}
                  className="aspect-video w-full object-cover"
                />
              ) : (
                <div className="flex aspect-video w-full items-center justify-center bg-gradient-to-b from-white/[0.06] to-white/[0.02]">
                  <svg
                    className="h-5 w-5 text-white/[0.12]"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.5"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  >
                    <rect x="2" y="2" width="20" height="20" rx="2.18" ry="2.18" />
                    <path d="m7 2 0 20M17 2v20M2 12h20M2 7h5M2 17h5M17 17h5M17 7h5" />
                  </svg>
                </div>
              ))}
            <div className="px-2.5 py-1.5">
              {hoverRegion && (
                <div className="mb-1 flex items-center gap-1.5 text-[11px] leading-tight font-semibold text-white">
                  <span
                    aria-hidden="true"
                    className={[
                      "h-1.5 w-1.5 rounded-full",
                      REGION_DOT_COLORS[hoverRegion.kind],
                    ].join(" ")}
                  />
                  <span className="truncate">{MARKER_LABELS[hoverRegion.kind]}</span>
                  <span className="text-white/45 tabular-nums">
                    {formatTime(hoverRegion.start)}-{formatTime(hoverRegion.end)}
                  </span>
                </div>
              )}
              <div className="text-xs font-semibold text-white tabular-nums">
                {formatTime(hoverTime)}
              </div>
              {hoverChapter && (
                <div className="mt-0.5 truncate text-[11px] leading-tight text-white/50">
                  {hoverChapter.title}
                </div>
              )}
            </div>
          </div>
          {/* Caret */}
          <div className="relative -mt-px h-2 w-4 overflow-hidden">
            <div className="absolute top-0 left-1/2 h-2.5 w-2.5 -translate-x-1/2 rotate-45 border-r border-b border-white/[0.08] bg-neutral-900/95" />
          </div>
        </div>
      )}

      {/* Live timecode while dragging a marker handle */}
      {editing && edgeDrag && activeRegion && duration > 0 && (
        <div
          className="pointer-events-none absolute z-20 flex flex-col items-center"
          style={{
            left: `clamp(28px, ${((edgeDrag === "start" ? activeRegion.start : activeRegion.end) / duration) * 100}%, calc(100% - 28px))`,
            bottom: "calc(100% + 8px)",
            transform: "translateX(-50%)",
          }}
        >
          <div className="rounded-lg border border-white/[0.08] bg-neutral-900/95 px-2.5 py-1.5 text-center shadow-2xl backdrop-blur-md">
            <div className="text-[10px] font-medium tracking-wide text-white/45 capitalize">
              {activeRegion.kind} {edgeDrag}
            </div>
            <div className="text-xs font-semibold text-white tabular-nums">
              {formatTime(edgeDrag === "start" ? activeRegion.start : activeRegion.end)}
            </div>
          </div>
          {/* Caret */}
          <div className="relative -mt-px h-2 w-4 overflow-hidden">
            <div className="absolute top-0 left-1/2 h-2.5 w-2.5 -translate-x-1/2 rotate-45 border-r border-b border-white/[0.08] bg-neutral-900/95" />
          </div>
        </div>
      )}

      {/* Touch-friendly hit area (min 44px) with visual bar centered */}
      <div
        ref={barRef}
        role="slider"
        tabIndex={0}
        aria-label="Seek"
        aria-valuemin={0}
        aria-valuemax={duration}
        aria-valuenow={displayTime}
        aria-valuetext={formatAriaValueText(displayTime)}
        className="relative flex min-h-[44px] w-full cursor-pointer touch-none items-center rounded focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
        onMouseDown={handleMouseDown}
        onMouseMove={handleMouseMove}
        onMouseLeave={() => setHoverTime(null)}
        onTouchStart={handleTouchStart}
        onTouchMove={handleTouchMove}
        onTouchEnd={handleTouchEnd}
        onKeyDown={handleKeyDown}
      >
        <div
          className={[
            "relative h-[3px] w-full rounded-full bg-white/15 transition-[height] duration-200 ease-out group-hover/seek:h-[5px]",
            editing ? "h-[5px]" : "",
          ].join(" ")}
        >
          {duration > 0 &&
            chapters
              .filter((chapter) => chapter.start_seconds > 0 && chapter.start_seconds < duration)
              .map((chapter) => (
                <div
                  key={chapter.index}
                  className="absolute top-1/2 z-[1] h-[10px] w-px -translate-x-1/2 -translate-y-1/2 bg-white/55"
                  style={{ left: `${(chapter.start_seconds / duration) * 100}%` }}
                />
              ))}
          {/* Buffered ranges */}
          {bufferedRanges.map((range, i) => (
            <div
              key={i}
              className="absolute inset-y-0 rounded-full bg-white/30"
              style={{ left: `${range.startPercent}%`, width: `${range.widthPercent}%` }}
            />
          ))}
          {/* Marker regions. While editing they brighten and grow slightly
              past the thin track so the area being adjusted reads clearly; the
              active segment is a touch taller and gets a ring. */}
          {duration > 0 &&
            regions.map((region) => {
              const isActive = editing && region.kind === activeEditKind;
              const isHovered = hoverRegion?.kind === region.kind && !dragging && edgeDrag === null;
              return (
                <div
                  aria-hidden="true"
                  key={region.kind}
                  className={[
                    "absolute top-1/2 -translate-y-1/2 rounded-full transition-[height,box-shadow] duration-150 ease-out",
                    editing ? (isActive ? "h-2.5" : "h-2") : isHovered ? "h-2" : "h-full",
                    editing ? REGION_COLORS_EDIT[region.kind] : REGION_COLORS[region.kind],
                    isActive || isHovered ? "z-[2] ring-1 ring-white/80" : "z-[1]",
                  ].join(" ")}
                  style={{
                    left: `${(region.start / duration) * 100}%`,
                    width: `${((region.end - region.start) / duration) * 100}%`,
                  }}
                />
              );
            })}
          {/* Played — warm white with a subtle amber kiss at the leading edge */}
          <div
            className="absolute inset-y-0 left-0 rounded-full bg-white shadow-[0_0_10px_-1px_rgb(255_255_255/0.35)]"
            style={{ width: `${playedPercent}%` }}
          />
          {/* Thumb */}
          <div
            className="absolute top-1/2 z-[3] h-3.5 w-3.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-white opacity-0 shadow-[0_4px_14px_rgb(0_0_0/0.45)] ring-1 ring-black/10 transition-all duration-200 group-hover/seek:opacity-100"
            style={{ left: `${playedPercent}%` }}
          />
          {/* Editable marker handles for the active region */}
          {activeRegion && duration > 0 && (
            <>
              <MarkerHandle
                percent={(activeRegion.start / duration) * 100}
                label="Drag marker start"
                onPointerDown={handleEdgePointerDown(activeRegion.kind, "start")}
              />
              <MarkerHandle
                percent={(activeRegion.end / duration) * 100}
                label="Drag marker end"
                onPointerDown={handleEdgePointerDown(activeRegion.kind, "end")}
              />
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function MarkerHandle({
  percent,
  label,
  onPointerDown,
}: {
  percent: number;
  label: string;
  onPointerDown: (e: React.PointerEvent) => void;
}) {
  return (
    <div
      aria-label={label}
      onPointerDown={onPointerDown}
      onMouseDown={(e) => e.stopPropagation()}
      className="absolute top-1/2 z-[2] flex h-5 w-5 -translate-x-1/2 -translate-y-1/2 cursor-ew-resize touch-none items-center justify-center"
      style={{ left: `${percent}%` }}
    >
      <span className="h-4 w-1.5 rounded-full bg-white shadow-[0_2px_8px_rgb(0_0_0/0.55)] ring-1 ring-black/20" />
    </div>
  );
}

export { formatTime };
