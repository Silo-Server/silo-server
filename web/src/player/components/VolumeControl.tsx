import { useCallback, useRef } from "react";
import { Volume2, VolumeX } from "lucide-react";
import { storage } from "@/utils/storage";

/** Read persisted volume from localStorage. */
export function getPersistedVolume(): { volume: number; muted: boolean } {
  const vol = storage.get(storage.KEYS.VOLUME);
  const muted = storage.get(storage.KEYS.MUTED);
  return {
    volume: vol != null ? Number(vol) : 1,
    muted: muted === "true",
  };
}

/** Persist volume to localStorage. */
export function persistVolume(volume: number, muted: boolean): void {
  storage.set(storage.KEYS.VOLUME, String(volume));
  storage.set(storage.KEYS.MUTED, String(muted));
}

interface VolumeControlProps {
  volume: number;
  muted: boolean;
  onVolumeChange: (volume: number) => void;
  onMutedChange: (muted: boolean) => void;
  /**
   * "overlay" (default) is the white-on-dark video HUD styling; "surface"
   * uses theme tokens for placement on regular app surfaces like the
   * audiobook mini bar.
   */
  tone?: "overlay" | "surface";
}

export function VolumeControl({
  volume,
  muted,
  onVolumeChange,
  onMutedChange,
  tone = "overlay",
}: VolumeControlProps) {
  const sliderRef = useRef<HTMLDivElement>(null);

  const getVolumeFromEvent = useCallback(
    (e: React.MouseEvent | MouseEvent) => {
      const el = sliderRef.current;
      if (!el) return volume;
      const rect = el.getBoundingClientRect();
      return Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    },
    [volume],
  );

  const handleMouseDown = useCallback(
    (e: React.MouseEvent) => {
      const v = getVolumeFromEvent(e);
      onVolumeChange(v);

      const handleMouseMove = (ev: MouseEvent) => onVolumeChange(getVolumeFromEvent(ev));
      const handleMouseUp = () => {
        document.removeEventListener("mousemove", handleMouseMove);
        document.removeEventListener("mouseup", handleMouseUp);
      };
      document.addEventListener("mousemove", handleMouseMove);
      document.addEventListener("mouseup", handleMouseUp);
    },
    [getVolumeFromEvent, onVolumeChange],
  );

  const getVolumeFromClientX = useCallback(
    (clientX: number) => {
      const el = sliderRef.current;
      if (!el) return volume;
      const rect = el.getBoundingClientRect();
      return Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
    },
    [volume],
  );

  const handleTouchStart = useCallback(
    (e: React.TouchEvent<HTMLDivElement>) => {
      e.preventDefault();
      const touch = e.touches[0];
      if (!touch) return;
      const v = getVolumeFromClientX(touch.clientX);
      onVolumeChange(v);
      if (muted) onMutedChange(false);

      const handleTouchMove = (ev: TouchEvent) => {
        ev.preventDefault();
        const t = ev.touches[0];
        if (!t) return;
        onVolumeChange(getVolumeFromClientX(t.clientX));
      };
      const handleTouchEnd = () => {
        document.removeEventListener("touchmove", handleTouchMove);
        document.removeEventListener("touchend", handleTouchEnd);
      };
      document.addEventListener("touchmove", handleTouchMove, { passive: false });
      document.addEventListener("touchend", handleTouchEnd);
    },
    [getVolumeFromClientX, onVolumeChange, muted, onMutedChange],
  );

  const handleWheel = useCallback(
    (e: React.WheelEvent) => {
      e.preventDefault();
      const delta = e.deltaY < 0 ? 0.05 : -0.05;
      onVolumeChange(Math.max(0, Math.min(1, volume + delta)));
    },
    [volume, onVolumeChange],
  );

  const displayVolume = muted ? 0 : volume;

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      let newVolume: number | null = null;
      switch (e.key) {
        case "ArrowUp":
        case "ArrowRight":
          newVolume = Math.min(1, volume + 0.05);
          break;
        case "ArrowDown":
        case "ArrowLeft":
          newVolume = Math.max(0, volume - 0.05);
          break;
        case "Home":
          newVolume = 0;
          break;
        case "End":
          newVolume = 1;
          break;
        default:
          return;
      }
      e.preventDefault();
      if (muted) onMutedChange(false);
      onVolumeChange(newVolume);
    },
    [volume, muted, onVolumeChange, onMutedChange],
  );

  const surface = tone === "surface";
  const buttonClass = surface
    ? "text-muted-foreground hover:text-foreground hover:bg-muted inline-flex h-9 w-9 items-center justify-center rounded-full transition-colors"
    : "player-utility-btn";
  const sliderFocusClass = surface ? "focus-visible:ring-ring" : "focus-visible:ring-white/70";
  const trackClass = surface ? "bg-muted" : "bg-white/15";
  const fillClass = surface
    ? "bg-foreground"
    : "bg-white shadow-[0_0_8px_-1px_rgb(255_255_255/0.35)]";
  const thumbClass = surface ? "bg-foreground ring-border" : "bg-white ring-black/10";

  return (
    // Scroll-wheel still adjusts volume from anywhere in the group, but the
    // mute button and slider are now fully independent items — nothing shifts
    // on hover, and the slider is always visible.
    <div className="group/vol flex items-center gap-2" onWheel={handleWheel}>
      <button
        type="button"
        className={buttonClass}
        onClick={() => onMutedChange(!muted)}
        aria-label={muted ? "Unmute" : "Mute"}
        title={muted ? "Unmute (M)" : "Mute (M)"}
        data-active={muted || volume === 0 ? "false" : undefined}
      >
        {muted || volume === 0 ? (
          <VolumeX className="h-[18px] w-[18px]" />
        ) : (
          <Volume2 className="h-[18px] w-[18px]" />
        )}
      </button>

      {/* Always-visible slider. Uses the same thin resting height as the seek
          bar, grows subtly on hover for richer feedback, and reveals a small
          thumb dot at the playhead when hovered. */}
      <div
        ref={sliderRef}
        role="slider"
        tabIndex={0}
        aria-label="Volume"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(displayVolume * 100)}
        aria-valuetext={`Volume ${Math.round(displayVolume * 100)}%`}
        className={`group/vol-slider relative flex h-6 w-24 cursor-pointer touch-none items-center rounded-full focus-visible:ring-2 focus-visible:outline-none ${sliderFocusClass}`}
        onMouseDown={handleMouseDown}
        onTouchStart={handleTouchStart}
        onKeyDown={handleKeyDown}
      >
        <div
          className={`relative h-[3px] w-full rounded-full transition-[height] duration-150 ease-out group-hover/vol-slider:h-[5px] ${trackClass}`}
        >
          <div
            className={`absolute inset-y-0 left-0 rounded-full ${fillClass}`}
            style={{ width: `${displayVolume * 100}%` }}
          />
          <div
            className={`absolute top-1/2 h-3 w-3 -translate-x-1/2 -translate-y-1/2 rounded-full opacity-0 shadow-[0_2px_8px_rgb(0_0_0/0.5)] ring-1 transition-opacity duration-150 group-hover/vol-slider:opacity-100 ${thumbClass}`}
            style={{ left: `${displayVolume * 100}%` }}
          />
        </div>
      </div>
    </div>
  );
}
