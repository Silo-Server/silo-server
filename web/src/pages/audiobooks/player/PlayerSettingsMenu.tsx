import { useCallback, useEffect, useRef, useState } from "react";
import { SlidersHorizontal } from "lucide-react";
import { SKIP_INTERVAL_CHOICES, type AudiobookPrefs } from "./useAudiobookPrefs";

interface PlayerSettingsMenuProps {
  prefs: AudiobookPrefs;
}

/** Skip-interval and smart-rewind preferences, anchored to the player bar. */
export function PlayerSettingsMenu({ prefs }: PlayerSettingsMenuProps) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  const handleBlur = useCallback((e: React.FocusEvent) => {
    if (!containerRef.current?.contains(e.relatedTarget as Node)) {
      setOpen(false);
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open]);

  return (
    <div ref={containerRef} className="relative" onBlur={handleBlur}>
      <button
        type="button"
        className="player-utility-btn"
        onClick={() => setOpen((v) => !v)}
        aria-label="Player settings"
        aria-expanded={open}
        aria-haspopup="dialog"
        title="Player settings"
      >
        <SlidersHorizontal className="h-[18px] w-[18px]" />
      </button>

      {open && (
        <div
          role="dialog"
          aria-label="Player settings"
          className="absolute right-0 bottom-full mb-2 flex w-[260px] flex-col gap-4 rounded-lg bg-black/90 px-4 py-3.5 shadow-xl backdrop-blur-sm"
        >
          <SkipIntervalRow label="Skip back" value={prefs.skipBack} onChange={prefs.setSkipBack} />
          <SkipIntervalRow
            label="Skip forward"
            value={prefs.skipForward}
            onChange={prefs.setSkipForward}
          />

          <button
            type="button"
            role="switch"
            aria-checked={prefs.smartRewind}
            className="group flex items-start justify-between gap-3 text-left focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
            onClick={() => prefs.setSmartRewind(!prefs.smartRewind)}
          >
            <span className="flex flex-col gap-0.5">
              <span className="text-sm text-white/85">Smart rewind</span>
              <span className="text-[11px] leading-snug text-white/40">
                Backs up a little after a pause
              </span>
            </span>
            <span
              aria-hidden
              className={`mt-0.5 inline-flex h-[18px] w-8 shrink-0 items-center rounded-full px-[2px] transition-colors ${
                prefs.smartRewind ? "bg-white/80" : "bg-white/20"
              }`}
            >
              <span
                className={`h-[14px] w-[14px] rounded-full bg-black/80 transition-transform ${
                  prefs.smartRewind ? "translate-x-[14px]" : "translate-x-0"
                }`}
              />
            </span>
          </button>
        </div>
      )}
    </div>
  );
}

function SkipIntervalRow({
  label,
  value,
  onChange,
}: {
  label: string;
  value: number;
  onChange: (seconds: number) => void;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[11px] tracking-[0.14em] text-white/40 uppercase">{label}</span>
      <div className="flex flex-wrap gap-1">
        {SKIP_INTERVAL_CHOICES.map((seconds) => (
          <button
            key={seconds}
            type="button"
            data-active={seconds === value ? "true" : undefined}
            className={`rounded-full px-2 py-0.5 text-xs tabular-nums transition-colors hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
              seconds === value ? "bg-white/15 text-white" : "text-white/60"
            }`}
            onClick={() => onChange(seconds)}
          >
            {seconds}s
          </button>
        ))}
      </div>
    </div>
  );
}
