import { PreviewImage } from "./PreviewImage";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { MarkerKind, MarkerRegionView, PlayerChapter } from "../types";
import { trickplayTile, type PlayerTrickplay } from "../trickplay";
import { formatTime } from "../utils/formatTime";
import { MARKER_LABEL_KEYS } from "../hooks/useMarkerEditor";
import { useT, type Translate } from "@/i18n";

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
  /** Reports the position under a drag in progress, then null when it ends. */
  onScrubTimeChange?: (seconds: number | null) => void;
  /** Unmodified arrow keys skip by the profile's intervals; Shift+Arrow nudges 5s. */
  onSkip: { back: () => void; forward: () => void };
  /** Seek-bar previews of the file being played, when it has them. */
  trickplay?: PlayerTrickplay | null;
  /** Successful manifest refreshes retry sheets even when URLs stay unchanged. */
  trickplayUpdatedAt?: number;
  /** A preview sheet failed to load, as when its URL expired. */
  onTrickplayError?: () => void;
}

/** Half the width of the preview bubble when it shows an image (w-48). */
const WIDE_PREVIEW_HALF_WIDTH = 96;

const NO_SHEETS: ReadonlySet<string> = new Set();

interface SheetState {
  source: PlayerTrickplay | null;
  loaded: ReadonlySet<string>;
  failed: ReadonlySet<string>;
}

const NO_SHEET_STATE: SheetState = { source: null, loaded: NO_SHEETS, failed: NO_SHEETS };

/**
 * Region tint in normal playback: one quiet hatch for every kind, so the track
 * never shows a colour it does not explain. The hover preview names the region;
 * the editor keeps distinct colours while regions are being adjusted.
 */
const REGION_HATCH =
  "bg-[repeating-linear-gradient(135deg,rgb(255_255_255/0.6)_0_2px,rgb(255_255_255/0.22)_2px_5px)]";

/** Brighter tint used while editing so regions stand out against the track. */
const REGION_COLORS_EDIT: Record<MarkerKind, string> = {
  intro: "bg-sky-300/75",
  recap: "bg-violet-300/75",
  credits: "bg-rose-300/75",
  preview: "bg-emerald-300/75",
};

const REGION_DOT_COLORS: Record<MarkerKind, string> = {
  intro: "bg-sky-300",
  recap: "bg-violet-300",
  credits: "bg-rose-300",
  preview: "bg-emerald-300",
};

function markerLabel(t: Translate, kind: MarkerKind): string {
  return t(MARKER_LABEL_KEYS[kind]);
}

/** Width of the cut between chapters on the track, in CSS pixels. */
const CHAPTER_GAP_PX = 2;

/**
 * Cuts the track at each chapter start so chapters read as segments. A mask
 * keeps the cut transparent, showing the picture through it, over every layer.
 */
function chapterGapMask(chapters: PlayerChapter[], duration: number): string | undefined {
  if (duration <= 0) return undefined;
  const cuts = chapters
    .map((chapter) => (chapter.start_seconds / duration) * 100)
    .filter((percent) => percent > 0.5 && percent < 99.5)
    .sort((a, b) => a - b);
  if (cuts.length === 0) return undefined;
  const half = CHAPTER_GAP_PX / 2;
  const stops: string[] = [];
  let from = "0%";
  for (const cut of cuts) {
    stops.push(`#000 ${from} calc(${cut}% - ${half}px)`);
    stops.push(`transparent calc(${cut}% - ${half}px) calc(${cut}% + ${half}px)`);
    from = `calc(${cut}% + ${half}px)`;
  }
  stops.push(`#000 ${from} 100%`);
  return `linear-gradient(to right, ${stops.join(", ")})`;
}

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
    if (time < region.start || time >= region.end) {
      continue;
    }
    if (!match || region.end - region.start < match.end - match.start) {
      match = region;
    }
  }
  return match;
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
  onScrubTimeChange,
  onSkip,
  trickplay = null,
  trickplayUpdatedAt,
  onTrickplayError,
}: SeekBarProps) {
  const t = useT();
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
  useEffect(() => {
    onScrubTimeChange?.(dragTime);
  }, [dragTime, onScrubTimeChange]);
  // The preview follows the pointer while hovering and the scrub position
  // while dragging (mouse or touch); a marker handle drag shows its own.
  const previewTime = edgeDrag !== null ? null : (dragTime ?? hoverTime);
  const hoverChapter = useMemo(
    () => (previewTime === null ? null : findChapterAtTime(chapters, previewTime)),
    [chapters, previewTime],
  );
  const hoverRegion = useMemo(
    () => (previewTime === null ? null : findRegionAtTime(regions, previewTime)),
    [regions, previewTime],
  );
  // Sheets that loaded or failed, for the manifest they came from: a new
  // manifest (fresh URLs) starts clean.
  const [sheetState, setSheetState] = useState<SheetState>(NO_SHEET_STATE);
  const sheets = sheetState.source === trickplay ? sheetState : NO_SHEET_STATE;
  const candidate = useMemo(
    () => (trickplay && previewTime !== null ? trickplayTile(trickplay, previewTime) : null),
    [trickplay, previewTime],
  );
  // A sheet still loading, or one that failed, shows the chapter image
  // instead: a sprite without its sheet is an empty box.
  const tile =
    candidate && sheets.loaded.has(candidate.url) && !sheets.failed.has(candidate.url)
      ? candidate
      : null;

  // Load the sheet under the pointer and the one after it, so scrubbing
  // forward rarely waits.
  const candidateSheet = candidate?.sheet;
  useEffect(() => {
    if (!trickplay || candidateSheet === undefined) return;
    const mark = (kind: "loaded" | "failed", url: string) =>
      setSheetState((current) => {
        const base =
          current.source === trickplay ? current : { ...NO_SHEET_STATE, source: trickplay };
        const opposite = kind === "loaded" ? "failed" : "loaded";
        if (base[kind].has(url) && !base[opposite].has(url)) return base;
        const cleared = new Set(base[opposite]);
        cleared.delete(url);
        return { ...base, [kind]: new Set(base[kind]).add(url), [opposite]: cleared };
      });
    const images: HTMLImageElement[] = [];
    for (const url of trickplay.sheets.slice(candidateSheet, candidateSheet + 2)) {
      const image = new Image();
      image.onload = () => mark("loaded", url);
      image.onerror = () => {
        mark("failed", url);
        onTrickplayError?.();
      };
      image.src = url;
      images.push(image);
    }
    return () => {
      for (const image of images) {
        image.onload = null;
        image.onerror = null;
      }
    };
  }, [trickplay, trickplayUpdatedAt, candidateSheet, onTrickplayError]);

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

  // A touch the system takes away (a gesture, a call) ends the drag without
  // seeking, so neither the preview nor the finish time stays on it.
  const handleTouchCancel = useCallback(() => {
    if (!dragging) return;
    setDragging(false);
    setDragTime(null);
    setHoverTime(null);
  }, [dragging]);

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

  const formatAriaValueText = useCallback(
    (seconds: number): string => {
      const s = Math.floor(seconds);
      const m = Math.floor(s / 60);
      const sec = s % 60;
      if (m > 0 && sec > 0) {
        return t("web.player.seekBar.positionMinutesSeconds", {
          minutes: t("web.player.seekBar.positionMinutes", { minutes: m, minutes_text: m }),
          seconds: t("web.player.seekBar.positionSeconds", { seconds: sec }),
        });
      }
      if (m > 0) {
        return t("web.player.seekBar.positionMinutes", { minutes: m, minutes_text: m });
      }
      return t("web.player.seekBar.positionSeconds", { seconds: sec });
    },
    [t],
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      // Browser and platform shortcuts keep their modifier combinations.
      if (e.ctrlKey || e.altKey || e.metaKey) return;
      if (!e.shiftKey && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
        e.preventDefault();
        if (e.key === "ArrowLeft") onSkip.back();
        else onSkip.forward();
        return;
      }
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
    [duration, displayTime, onSeek, onSkip],
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
  // Editing grows regions past the track, which a mask would clip.
  const gapMask = editing ? undefined : chapterGapMask(chapters, duration);

  return (
    <div className="player-seekbar group/seek relative w-full px-1.5">
      {/* Hover and scrub time preview */}
      {previewTime !== null && (
        <div
          className="pointer-events-none absolute z-10 flex flex-col items-center"
          data-testid="seek-preview"
          style={{
            left: (() => {
              const half = tile || hoverChapter || hoverRegion ? WIDE_PREVIEW_HALF_WIDTH : 32;
              return `clamp(${half}px, ${(previewTime / (duration || 1)) * 100}%, calc(100% - ${half}px))`;
            })(),
            bottom: "calc(50% + 0.75rem)",
            transform: "translateX(-50%)",
          }}
        >
          <div
            className={["hud-preview", tile || hoverChapter || hoverRegion ? "w-48" : ""].join(" ")}
          >
            {/* Seek preview, else the chapter thumbnail or placeholder */}
            {tile && trickplay ? (
              <div
                role="img"
                aria-label={t("web.player.seekBar.previewAt", { time: formatTime(previewTime) })}
                data-testid="seek-preview-image"
                className="w-full bg-black bg-no-repeat"
                style={{
                  aspectRatio: `${trickplay.width} / ${trickplay.height}`,
                  backgroundImage: `url("${tile.url}")`,
                  backgroundSize: `${trickplay.columns * 100}% ${trickplay.rows * 100}%`,
                  backgroundPosition: `${trickplay.columns > 1 ? (tile.x / trickplay.width / (trickplay.columns - 1)) * 100 : 0}% ${trickplay.rows > 1 ? (tile.y / trickplay.height / (trickplay.rows - 1)) * 100 : 0}%`,
                }}
              />
            ) : (
              (hoverRegion || hoverChapter) && (
                <PreviewImage
                  src={hoverRegion ? hoverRegion.thumbnail_url : hoverChapter?.thumbnail_url}
                  alt={hoverRegion ? markerLabel(t, hoverRegion.kind) : (hoverChapter?.title ?? "")}
                  className="aspect-video w-full object-cover"
                  placeholderClassName="flex items-center justify-center bg-gradient-to-b from-white/[0.06] to-white/[0.02]"
                />
              )
            )}
            <div className="px-3 py-2">
              {hoverRegion && (
                <div className="mb-1 flex items-center gap-1.5 text-xs leading-tight font-semibold">
                  <span
                    aria-hidden="true"
                    className={["h-2 w-2 rounded-full", REGION_DOT_COLORS[hoverRegion.kind]].join(
                      " ",
                    )}
                  />
                  <span className="truncate">{markerLabel(t, hoverRegion.kind)}</span>
                  <span className="text-[var(--hud-ash)] tabular-nums">
                    {t("web.player.seekBar.markerRange", {
                      start: formatTime(hoverRegion.start),
                      end: formatTime(hoverRegion.end),
                    })}
                  </span>
                </div>
              )}
              <div className="text-sm font-semibold tabular-nums">{formatTime(previewTime)}</div>
              {hoverChapter && (
                <div
                  className="mt-0.5 truncate text-xs text-[var(--hud-ash)]"
                  title={hoverChapter.title}
                >
                  {hoverChapter.title}
                </div>
              )}
            </div>
          </div>
          {/* Caret pointing at the hovered moment on the track. */}
          <div aria-hidden="true" className="hud-preview-caret" />
        </div>
      )}

      {/* Live timecode while dragging a marker handle */}
      {editing && edgeDrag && activeRegion && duration > 0 && (
        <div
          className="pointer-events-none absolute z-20 flex flex-col items-center"
          style={{
            left: `clamp(28px, ${((edgeDrag === "start" ? activeRegion.start : activeRegion.end) / duration) * 100}%, calc(100% - 28px))`,
            bottom: "calc(50% + 0.75rem)",
            transform: "translateX(-50%)",
          }}
        >
          <div className="hud-preview px-3 py-2 text-center">
            <div className="text-xs font-medium text-[var(--hud-ash)] capitalize">
              {t("web.player.seekBar.markerEdge", { kind: activeRegion.kind, edge: edgeDrag })}
            </div>
            <div className="text-sm font-semibold tabular-nums">
              {formatTime(edgeDrag === "start" ? activeRegion.start : activeRegion.end)}
            </div>
          </div>
        </div>
      )}

      {/* Touch-friendly hit area (min 44px) with visual bar centered */}
      <div
        ref={barRef}
        role="slider"
        // Found by this attribute, not by its label, which is translated.
        data-player-seek-slider=""
        tabIndex={0}
        aria-label={t("web.player.seekBar.label")}
        aria-valuemin={0}
        aria-valuemax={duration}
        aria-valuenow={displayTime}
        aria-valuetext={formatAriaValueText(displayTime)}
        className="relative flex min-h-[2.75rem] w-full cursor-pointer touch-none items-center rounded-md"
        onMouseDown={handleMouseDown}
        onMouseMove={handleMouseMove}
        onMouseLeave={() => setHoverTime(null)}
        onTouchStart={handleTouchStart}
        onTouchMove={handleTouchMove}
        onTouchEnd={handleTouchEnd}
        onTouchCancel={handleTouchCancel}
        onKeyDown={handleKeyDown}
      >
        <div className="relative w-full">
          <div
            className="hud-track hud-track-base"
            data-active={editing || dragging ? "true" : undefined}
            style={gapMask ? { maskImage: gapMask, WebkitMaskImage: gapMask } : undefined}
          >
            {/* Buffered ranges */}
            {bufferedRanges.map((range, i) => (
              <div
                key={i}
                className="hud-track-buffered absolute inset-y-0 rounded-full"
                style={{ left: `${range.startPercent}%`, width: `${range.widthPercent}%` }}
              />
            ))}
            {/* Marker regions. While editing they brighten and grow slightly
                past the track so the area being adjusted reads clearly; the
                active segment is a touch taller and gets a ring. */}
            {duration > 0 &&
              regions.map((region) => {
                const isActive = editing && region.kind === activeEditKind;
                const isHovered = hoverRegion === region && !dragging && edgeDrag === null;
                return (
                  <div
                    aria-hidden="true"
                    key={`${region.kind}:${region.start}:${region.end}`}
                    className={[
                      "absolute top-1/2 -translate-y-1/2 rounded-full transition-[height,box-shadow] duration-150 ease-out",
                      editing ? (isActive ? "h-3" : "h-2.5") : "h-full",
                      editing ? REGION_COLORS_EDIT[region.kind] : REGION_HATCH,
                      isActive || isHovered ? "z-[2] ring-1 ring-white/80" : "z-[1]",
                    ].join(" ")}
                    style={{
                      left: `${(region.start / duration) * 100}%`,
                      width: `${((region.end - region.start) / duration) * 100}%`,
                    }}
                  />
                );
              })}
            {/* Played, under the regions: an edited region stays visible
                across the part already played. */}
            <div
              className="hud-track-played absolute inset-y-0 left-0 rounded-full"
              style={{ width: `${playedPercent}%` }}
            />
          </div>
          {/* Hover position: a hairline the preview's caret points at. */}
          {previewTime !== null && !dragging && duration > 0 ? (
            <div
              aria-hidden="true"
              className="hud-hover-line absolute top-1/2 z-[2] -translate-x-1/2 -translate-y-1/2"
              style={{ left: `${(previewTime / duration) * 100}%` }}
            />
          ) : null}
          {/* Playhead: always shown, so a paused frame says where it is. */}
          <div
            aria-hidden="true"
            className="hud-thumb absolute top-1/2 z-[3] -translate-x-1/2 -translate-y-1/2 transition-transform duration-150 group-hover/seek:scale-110"
            style={{ left: `${playedPercent}%` }}
          />
          {/* Editable marker handles for the active region */}
          {activeRegion && duration > 0 && (
            <>
              <MarkerHandle
                percent={(activeRegion.start / duration) * 100}
                label={t("web.player.seekBar.dragMarkerStart")}
                onPointerDown={handleEdgePointerDown(activeRegion.kind, "start")}
              />
              <MarkerHandle
                percent={(activeRegion.end / duration) * 100}
                label={t("web.player.seekBar.dragMarkerEnd")}
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
