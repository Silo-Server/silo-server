import { useCallback, useMemo, useRef, useState } from "react";
import { ListVideo } from "lucide-react";
import type { PlayerChapter } from "../types";
import { findActiveChapterIndex } from "../utils/chapters";
import { formatTime } from "../utils/formatTime";
import { PreviewImage } from "./PreviewImage";
import { PlayerMenuSurface } from "./PlayerMenuSurface";
import { numberText, useT } from "@/i18n";

interface ChaptersMenuProps {
  chapters: PlayerChapter[];
  currentTime: number;
  onSeek: (seconds: number) => void;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  hideTrigger?: boolean;
}

export function ChaptersMenu({
  chapters,
  currentTime,
  onSeek,
  open: controlledOpen,
  onOpenChange,
  hideTrigger = false,
}: ChaptersMenuProps) {
  const t = useT();
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
  const activeIndex = useMemo(
    () => findActiveChapterIndex(chapters, currentTime),
    [chapters, currentTime],
  );
  // Thumbnails only when the file has them: a column of empty frames reads as
  // broken, so without any the list leads with chapter numbers instead.
  const hasThumbnails = chapters.some((chapter) => Boolean(chapter.thumbnail_url));

  const handleBlur = useCallback(
    (e: React.FocusEvent) => {
      if (!menuRef.current?.contains(e.relatedTarget as Node)) {
        setOpen(false);
      }
    },
    [setOpen],
  );

  if (chapters.length === 0) return null;

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      {!hideTrigger && (
        <button
          type="button"
          className="hud-icon-btn hud-rail-btn"
          onClick={() => setOpen((value) => !value)}
          aria-label={t("web.player.chaptersMenu.trigger")}
          title={t("web.player.chaptersMenu.trigger")}
          aria-expanded={open}
          aria-haspopup="menu"
        >
          <ListVideo />
          <span className="hud-rail-label">{t("web.player.chaptersMenu.trigger")}</span>
        </button>
      )}

      {open && (
        <PlayerMenuSurface
          title={t("web.player.chaptersMenu.heading")}
          anchorRef={menuRef}
          className="hud-popover w-[22rem] max-w-[calc(100vw-2rem)]"
          onClose={() => setOpen(false)}
        >
          <div className="hud-scroll-list hud-chapter-list">
            {chapters.map((chapter, index) => {
              const current = index === activeIndex;
              return (
                <button
                  key={chapter.index}
                  role="menuitem"
                  type="button"
                  aria-current={current ? "true" : undefined}
                  className="hud-item hud-chapter"
                  onClick={() => {
                    onSeek(chapter.start_seconds);
                    setOpen(false);
                  }}
                >
                  {hasThumbnails ? (
                    <PreviewImage
                      src={chapter.thumbnail_url}
                      alt=""
                      className="hud-chapter-thumb"
                      placeholderClassName="flex items-center justify-center bg-white/[0.06]"
                      iconClassName="h-4 w-4 text-white/25"
                    />
                  ) : (
                    <span aria-hidden="true" className="hud-chapter-number">
                      {numberText(index + 1)}
                    </span>
                  )}
                  <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                    <span className="truncate" title={chapter.title}>
                      {chapter.title}
                    </span>
                    <span className="hud-item-secondary tabular-nums">
                      {current
                        ? t("web.player.chaptersMenu.nowPlaying", {
                            time: formatTime(chapter.start_seconds),
                            now: (chunks) => <span className="hud-now">{chunks}</span>,
                          })
                        : formatTime(chapter.start_seconds)}
                    </span>
                  </span>
                </button>
              );
            })}
          </div>
        </PlayerMenuSurface>
      )}
    </div>
  );
}
