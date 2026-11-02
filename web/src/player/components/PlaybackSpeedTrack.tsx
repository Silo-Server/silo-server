import { useId, useRef, type PointerEvent } from "react";
import { PLAYBACK_SPEEDS, formatPlaybackSpeed } from "../utils/playbackSpeed";

const LAST_STOP = PLAYBACK_SPEEDS.length - 1;
const NORMAL_STOP = PLAYBACK_SPEEDS.indexOf(1);
const LABELLED_SPEEDS = new Set([0.5, 1, 2, 3]);

const stopPercent = (stop: number) => (stop / LAST_STOP) * 100;

// End labels sit inside the track instead of hanging past its ends.
function labelAlignment(stop: number): string {
  if (stop === 0) return "";
  if (stop === LAST_STOP) return "-translate-x-full";
  return "-translate-x-1/2";
}

interface PlaybackSpeedTrackProps {
  value: number;
  onChange: (rate: number) => void;
  /** Registers each stop with the menu's arrow-key navigation. */
  itemRef: (stop: number) => (el: HTMLButtonElement | null) => void;
}

/**
 * The speeds as stops on a track drawn like the seek bar. The fill runs from
 * 1x to the chosen speed, so the track shows how far playback is from normal.
 * Each stop is a menu item, so the menu's keyboard model still applies.
 */
export function PlaybackSpeedTrack({ value, onChange, itemRef }: PlaybackSpeedTrackProps) {
  const labelId = useId();
  const trackRef = useRef<HTMLDivElement>(null);
  const current = Math.max(
    0,
    PLAYBACK_SPEEDS.findIndex((rate) => rate === value),
  );
  const fillStart = stopPercent(Math.min(current, NORMAL_STOP));
  const fillWidth = Math.abs(stopPercent(current) - stopPercent(NORMAL_STOP));

  const select = (rate: number) => {
    if (rate !== value) onChange(rate);
  };

  // Pressing or dragging anywhere on the track picks the nearest stop.
  const pickAt = (clientX: number) => {
    const rect = trackRef.current?.getBoundingClientRect();
    if (!rect || rect.width === 0) return;
    const fraction = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
    const rate = PLAYBACK_SPEEDS[Math.round(fraction * LAST_STOP)];
    if (rate !== undefined) select(rate);
  };
  const handlePointerDown = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    // A press between stops would otherwise move focus out of the menu,
    // which closes it.
    if (!(e.target instanceof HTMLButtonElement)) e.preventDefault();
    e.currentTarget.setPointerCapture?.(e.pointerId);
    pickAt(e.clientX);
  };
  const handlePointerMove = (e: PointerEvent<HTMLDivElement>) => {
    if (e.currentTarget.hasPointerCapture?.(e.pointerId)) pickAt(e.clientX);
  };

  return (
    <div role="group" aria-labelledby={labelId} className="px-3 pt-1 pb-2">
      <div className="flex items-baseline justify-between py-1">
        <span id={labelId} className="text-xs tracking-wider text-white/40 uppercase">
          Speed
        </span>
        <span className="text-sm font-medium text-white tabular-nums" aria-hidden="true">
          {formatPlaybackSpeed(value)}
        </span>
      </div>
      <div
        ref={trackRef}
        className="group relative mx-1.5 h-8 cursor-pointer touch-none select-none"
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
      >
        <div
          aria-hidden="true"
          className="absolute inset-x-0 top-1/2 h-[3px] -translate-y-1/2 rounded-full bg-white/20"
        />
        <div
          aria-hidden="true"
          className="absolute top-1/2 h-[3px] -translate-y-1/2 rounded-full bg-white motion-safe:transition-[left,width] motion-safe:duration-150"
          style={{ left: `${fillStart}%`, width: `${fillWidth}%` }}
        />
        {PLAYBACK_SPEEDS.map((rate, stop) => (
          <span
            key={rate}
            aria-hidden="true"
            className={`absolute top-1/2 w-px -translate-x-1/2 -translate-y-1/2 ${
              stop === NORMAL_STOP ? "h-3 bg-white/70" : "h-2 bg-white/35"
            }`}
            style={{ left: `${stopPercent(stop)}%` }}
          />
        ))}
        <span
          aria-hidden="true"
          className="pointer-events-none absolute top-1/2 size-3.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-white shadow-[0_1px_4px_rgba(0,0,0,0.6)] group-active:scale-110 motion-safe:transition-[left,transform] motion-safe:duration-150"
          style={{ left: `${stopPercent(current)}%` }}
        />
        {PLAYBACK_SPEEDS.map((rate, stop) => (
          <button
            key={rate}
            ref={itemRef(stop)}
            role="menuitemradio"
            type="button"
            aria-checked={rate === value}
            aria-label={formatPlaybackSpeed(rate)}
            className="absolute top-0 h-full w-5 -translate-x-1/2 rounded-full focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
            style={{ left: `${stopPercent(stop)}%` }}
            onClick={() => select(rate)}
          />
        ))}
      </div>
      <div
        aria-hidden="true"
        className="relative mx-1.5 h-3 text-[10px] leading-3 text-white/40 tabular-nums"
      >
        {PLAYBACK_SPEEDS.map((rate, stop) =>
          LABELLED_SPEEDS.has(rate) ? (
            <span
              key={rate}
              className={`absolute top-0 ${labelAlignment(stop)} ${rate === value ? "text-white" : ""}`}
              style={{ left: `${stopPercent(stop)}%` }}
            >
              {formatPlaybackSpeed(rate)}
            </span>
          ) : null,
        )}
      </div>
    </div>
  );
}
