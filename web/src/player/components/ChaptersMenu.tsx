import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ListVideo } from "lucide-react";
import type { PlayerChapter } from "../types";
import { formatTime } from "./SeekBar";
import { PlayerMenuSurface } from "./PlayerMenuSurface";

interface ChaptersMenuProps {
  chapters: PlayerChapter[];
  currentTime: number;
  onSeek: (seconds: number) => void;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  hideTrigger?: boolean;
}

function findActiveChapterIndex(chapters: PlayerChapter[], currentTime: number): number {
  return chapters.findIndex(
    (chapter) => currentTime >= chapter.start_seconds && currentTime < chapter.end_seconds,
  );
}

export function ChaptersMenu({
  chapters,
  currentTime,
  onSeek,
  open: controlledOpen,
  onOpenChange,
  hideTrigger = false,
}: ChaptersMenuProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const open = controlledOpen ?? uncontrolledOpen;
  const setOpen = useCallback(
    (value: boolean | ((previous: boolean) => boolean)) => {
      const next = typeof value === "function" ? value(open) : value;
      if (controlledOpen === undefined) setUncontrolledOpen(next);
      onOpenChange?.(next);
    },
    [controlledOpen, onOpenChange, open],
  );
  const menuRef = useRef<HTMLDivElement>(null);
  const menuItemsRef = useRef<(HTMLButtonElement | null)[]>([]);
  const activeIndex = useMemo(
    () => findActiveChapterIndex(chapters, currentTime),
    [chapters, currentTime],
  );

  const handleBlur = useCallback((e: React.FocusEvent) => {
    if (!menuRef.current?.contains(e.relatedTarget as Node)) {
      setOpen(false);
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [open]);

  const handleMenuKeyDown = useCallback((e: React.KeyboardEvent) => {
    const items = menuItemsRef.current.filter(Boolean) as HTMLButtonElement[];
    if (items.length === 0) return;
    const currentIndex = items.indexOf(document.activeElement as HTMLButtonElement);
    let nextIndex: number | null = null;

    switch (e.key) {
      case "ArrowDown":
        nextIndex = currentIndex < items.length - 1 ? currentIndex + 1 : 0;
        break;
      case "ArrowUp":
        nextIndex = currentIndex > 0 ? currentIndex - 1 : items.length - 1;
        break;
      case "Home":
        nextIndex = 0;
        break;
      case "End":
        nextIndex = items.length - 1;
        break;
      case "Escape":
        setOpen(false);
        return;
      default:
        return;
    }

    e.preventDefault();
    items[nextIndex]?.focus();
  }, []);

  if (chapters.length === 0) return null;

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      {!hideTrigger && (
        <button
          type="button"
          className="player-utility-btn"
          onClick={() => setOpen((value) => !value)}
          aria-label="Chapters"
          aria-expanded={open}
          aria-haspopup="menu"
        >
          <ListVideo className="h-[18px] w-[18px]" />
        </button>
      )}

      {open && (
        <PlayerMenuSurface
          className="absolute right-0 bottom-full z-30 mb-2 flex max-h-[60vh] min-w-[280px] flex-col overflow-y-auto rounded-lg bg-black/90 py-1.5 shadow-xl backdrop-blur-sm"
          onClose={() => setOpen(false)}
          onKeyDown={handleMenuKeyDown}
        >
          <div className="px-3 py-1.5 text-xs font-medium tracking-wide text-white/50 uppercase">
            Chapters
          </div>
          {chapters.map((chapter, index) => (
            <button
              key={chapter.index}
              ref={(el) => {
                menuItemsRef.current[index] = el;
              }}
              role="menuitem"
              type="button"
              className={`flex w-full items-center gap-3 px-3 py-2 text-left transition-colors hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
                index === activeIndex ? "bg-white/5 text-white" : "text-white/75"
              }`}
              onClick={() => {
                onSeek(chapter.start_seconds);
                setOpen(false);
              }}
            >
              {chapter.thumbnail_url ? (
                <img
                  src={chapter.thumbnail_url}
                  alt={chapter.title}
                  className="h-12 w-20 shrink-0 rounded object-cover"
                />
              ) : (
                <div className="flex h-12 w-20 shrink-0 items-center justify-center rounded bg-white/[0.06]">
                  <svg
                    className="h-4 w-4 text-white/20"
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
              )}
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-sm">{chapter.title}</span>
                <span className="text-xs text-white/45">{formatTime(chapter.start_seconds)}</span>
              </span>
            </button>
          ))}
        </PlayerMenuSurface>
      )}
    </div>
  );
}
