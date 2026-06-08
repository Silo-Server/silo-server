import { useCallback, useEffect, useRef, useState } from "react";

interface SpeedMenuProps {
  rates: readonly number[];
  value: number;
  onChange: (rate: number) => void;
}

export function SpeedMenu({ rates, value, onChange }: SpeedMenuProps) {
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuItemsRef = useRef<(HTMLButtonElement | null)[]>([]);

  const handleBlur = useCallback((e: React.FocusEvent) => {
    if (!menuRef.current?.contains(e.relatedTarget as Node)) {
      setOpen(false);
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open]);

  const handleMenuKeyDown = useCallback((e: React.KeyboardEvent) => {
    const items = menuItemsRef.current.filter(Boolean) as HTMLButtonElement[];
    if (items.length === 0) return;
    const i = items.indexOf(document.activeElement as HTMLButtonElement);
    let next: number | null = null;
    switch (e.key) {
      case "ArrowDown":
        next = i < items.length - 1 ? i + 1 : 0;
        break;
      case "ArrowUp":
        next = i > 0 ? i - 1 : items.length - 1;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = items.length - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    items[next]?.focus();
  }, []);

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      <button
        type="button"
        className="player-utility-btn px-2 text-xs tabular-nums"
        onClick={() => setOpen((v) => !v)}
        aria-label="Playback speed"
        aria-expanded={open}
        aria-haspopup="menu"
      >
        {value}×
      </button>

      {open && (
        <div
          role="menu"
          className="absolute right-0 bottom-full mb-2 flex min-w-[100px] flex-col overflow-hidden rounded-lg bg-black/90 py-1.5 shadow-xl backdrop-blur-sm"
          onKeyDown={handleMenuKeyDown}
        >
          {rates.map((r, idx) => (
            <button
              key={r}
              ref={(el) => {
                menuItemsRef.current[idx] = el;
              }}
              role="menuitem"
              type="button"
              data-active={r === value ? "true" : undefined}
              className={`w-full px-4 py-2 text-right text-sm tabular-nums transition-colors hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
                r === value ? "bg-white/5 text-white" : "text-white/75"
              }`}
              onClick={() => {
                onChange(r);
                setOpen(false);
              }}
            >
              {r}×
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
