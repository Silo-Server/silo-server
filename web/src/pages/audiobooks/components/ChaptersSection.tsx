import { useMemo, useState } from "react";
import { Check } from "lucide-react";
import { numberText, useT, type Translate } from "@/i18n";
import type { AudiobookChapter, AudiobookFile } from "@/lib/audiobooks/types";

interface ChaptersSectionProps {
  files: AudiobookFile[];
  currentPositionSeconds: number | null;
  onSelect: (absoluteSeconds: number) => void;
}

interface Row {
  chapter: AudiobookChapter;
  absoluteStart: number;
  durationSeconds: number;
  label: string;
  positionIndex: number; // 1-based
}

/** A book this short lists every chapter without a Show all toggle. */
const SHOW_ALL_UP_TO = 10;
/** Rows the collapsed list keeps before the current chapter. */
const ROWS_BEFORE_CURRENT = 3;
/** Rows the collapsed list shows in all. */
const COLLAPSED_ROWS = 8;

function buildRows(t: Translate, files: AudiobookFile[]): Row[] {
  const rows: Row[] = [];
  let offset = 0;
  let positionIndex = 1;
  for (const file of files) {
    if (file.chapters) {
      for (const ch of file.chapters) {
        rows.push({
          chapter: ch,
          absoluteStart: offset + ch.start_seconds,
          durationSeconds: Math.max(0, (ch.end_seconds ?? ch.start_seconds) - ch.start_seconds),
          label: ch.title || t("web.audiobooks.chapter.fallbackTitle", { number: ch.index + 1 }),
          positionIndex: positionIndex++,
        });
      }
    }
    offset += file.duration_seconds ?? 0;
  }
  return rows;
}

function formatChapterDuration(t: Translate, s: number): string {
  if (s <= 0) return "";
  const m = Math.floor(s / 60);
  const sec = Math.floor(s % 60);
  return t("web.audiobooks.chapters.duration", {
    minutes: m,
    seconds: numberText(String(sec).padStart(2, "0")),
  });
}

function formatChapterStart(totalSeconds: number): string {
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = Math.floor(totalSeconds % 60);
  return numberText(`${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`);
}

/**
 * The book's chapters, open by default. A long book shows a window around the
 * chapter the listener is in, with finished chapters ticked, and a toggle for
 * the full list.
 */
export function ChaptersSection({ files, currentPositionSeconds, onSelect }: ChaptersSectionProps) {
  const t = useT();
  const rows = useMemo(() => buildRows(t, files), [t, files]);
  const [expanded, setExpanded] = useState(false);

  const currentIndex = useMemo(() => {
    if (currentPositionSeconds == null) return -1;
    for (let i = rows.length - 1; i >= 0; i--) {
      const row = rows[i];
      if (row && currentPositionSeconds >= row.absoluteStart) return i;
    }
    return -1;
  }, [rows, currentPositionSeconds]);

  if (rows.length === 0) return null;

  const collapsible = rows.length > SHOW_ALL_UP_TO;
  let visible = rows;
  if (collapsible && !expanded) {
    const start = Math.max(
      0,
      Math.min(currentIndex - ROWS_BEFORE_CURRENT, rows.length - COLLAPSED_ROWS),
    );
    visible = rows.slice(start, start + COLLAPSED_ROWS);
  }

  return (
    <section>
      <div className="mb-3 flex items-baseline justify-between gap-3">
        <h2 className="text-xl font-semibold tracking-tight">
          {t("web.audiobooks.chapters.heading")}
        </h2>
        {collapsible && (
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            aria-expanded={expanded}
            className="text-muted-foreground hover:text-foreground text-sm font-semibold transition-colors"
          >
            {expanded
              ? t("web.audiobooks.chapters.showFewer")
              : t("web.audiobooks.chapters.showAll", { count: numberText(rows.length) })}
          </button>
        )}
      </div>

      <ol className="grid gap-0.5">
        {visible.map((row) => {
          const index = row.positionIndex - 1;
          const isCurrent = index === currentIndex;
          const isDone = currentIndex >= 0 && index < currentIndex;
          const leftInChapter =
            isCurrent && currentPositionSeconds != null
              ? Math.max(0, row.absoluteStart + row.durationSeconds - currentPositionSeconds)
              : 0;
          return (
            <li key={`${row.chapter.index}-${row.absoluteStart}`}>
              <button
                type="button"
                onClick={() => onSelect(row.absoluteStart)}
                data-current={isCurrent ? "true" : undefined}
                aria-current={isCurrent ? "step" : undefined}
                className={`grid w-full grid-cols-[2rem_minmax(0,1fr)_auto] items-center gap-3 rounded-lg px-3 py-2.5 text-start transition-colors sm:grid-cols-[2rem_minmax(0,1fr)_auto_auto] sm:gap-4 ${
                  isCurrent ? "bg-primary/15 hover:bg-primary/20" : "hover:bg-muted/50"
                }`}
              >
                <span
                  className={`text-end text-xs tabular-nums ${
                    isCurrent ? "text-foreground font-semibold" : "text-muted-foreground"
                  }`}
                >
                  {row.positionIndex}
                </span>
                <span className="min-w-0">
                  <span
                    className={`block truncate text-sm ${
                      isDone ? "text-muted-foreground" : "font-medium"
                    } ${isCurrent ? "font-semibold" : ""}`}
                  >
                    {row.label}
                  </span>
                  {isCurrent && leftInChapter > 0 && (
                    <span className="text-muted-foreground mt-0.5 block text-xs">
                      {t("web.audiobooks.chapters.leftInChapter", {
                        time: formatChapterDuration(t, leftInChapter),
                      })}
                    </span>
                  )}
                </span>
                <span className="text-muted-foreground hidden text-xs tabular-nums sm:inline">
                  {formatChapterStart(row.absoluteStart)}
                </span>
                <span className="text-muted-foreground flex items-center justify-end gap-1.5 text-xs tabular-nums">
                  {isDone && <Check className="text-success size-3.5" aria-hidden="true" />}
                  {formatChapterDuration(t, row.durationSeconds)}
                </span>
              </button>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
