import { useCallback, useEffect, useRef, useState } from "react";
import { Moon } from "lucide-react";
import { useT } from "@/i18n";

export type SleepSetting =
  | { kind: "off" }
  | { kind: "duration"; seconds: number }
  | { kind: "end-of-chapter" };

interface SleepTimerMenuProps {
  setting: SleepSetting;
  remainingMs: number | null;
  onChange: (next: SleepSetting) => void;
}

const PRESET_SECONDS = [300, 900, 1800, 2700, 3600];

function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

export function SleepTimerMenu({ setting, remainingMs, onChange }: SleepTimerMenuProps) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const itemsRef = useRef<(HTMLButtonElement | null)[]>([]);
  const armed = setting.kind !== "off";

  const handleBlur = useCallback((e: React.FocusEvent) => {
    if (!menuRef.current?.contains(e.relatedTarget as Node)) setOpen(false);
  }, []);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open]);

  const label =
    armed && remainingMs != null
      ? t("web.player.sleepTimer.countdown", { time: formatCountdown(remainingMs) })
      : t("web.player.sleepTimer.short");

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      <button
        type="button"
        className="player-utility-btn flex w-auto items-center gap-1.5 px-2 text-xs"
        onClick={() => setOpen((v) => !v)}
        aria-label={t("web.player.sleepTimer.trigger")}
        aria-expanded={open}
        aria-haspopup="menu"
      >
        <Moon className="h-3.5 w-3.5" />
        <span className="tabular-nums">{label}</span>
      </button>

      {open && (
        <div
          role="menu"
          className="absolute end-0 bottom-full z-30 mb-2 flex min-w-[160px] flex-col overflow-hidden rounded-lg bg-black/90 py-1.5 shadow-xl backdrop-blur-sm"
        >
          {armed && (
            <button
              ref={(el) => {
                itemsRef.current[0] = el;
              }}
              role="menuitem"
              type="button"
              className="w-full px-4 py-2 text-start text-sm text-white/85 hover:bg-white/10"
              onClick={() => {
                onChange({ kind: "off" });
                setOpen(false);
              }}
            >
              {t("web.player.sleepTimer.turnOff")}
            </button>
          )}
          {PRESET_SECONDS.map((seconds, i) => (
            <button
              key={seconds}
              ref={(el) => {
                itemsRef.current[(armed ? 1 : 0) + i] = el;
              }}
              role="menuitem"
              type="button"
              className="w-full px-4 py-2 text-start text-sm text-white/85 hover:bg-white/10"
              onClick={() => {
                onChange({ kind: "duration", seconds });
                setOpen(false);
              }}
            >
              {t("web.player.sleepTimer.minutes", { minutes: String(seconds / 60) })}
            </button>
          ))}
          <button
            role="menuitem"
            type="button"
            className="w-full px-4 py-2 text-start text-sm text-white/85 hover:bg-white/10"
            onClick={() => {
              onChange({ kind: "end-of-chapter" });
              setOpen(false);
            }}
          >
            {t("web.player.sleepTimer.endOfChapter")}
          </button>
        </div>
      )}
    </div>
  );
}
