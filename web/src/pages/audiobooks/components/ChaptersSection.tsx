import { useMemo, useState } from "react";
import { ArrowUpDown, Play } from "lucide-react";
import type { AudiobookChapter, AudiobookFile } from "@/lib/audiobooks/types";

interface ChaptersSectionProps {
  files: AudiobookFile[];
  currentPositionSeconds: number | null;
  onSelect: (absoluteSeconds: number) => void;
}

type SortMode = "position" | "longest-first";

interface Row {
  chapter: AudiobookChapter;
  absoluteStart: number;
  durationSeconds: number;
  label: string;
  positionIndex: number; // 1-based
}

function buildRows(files: AudiobookFile[]): Row[] {
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
          label: ch.title || `Chapter ${ch.index + 1}`,
          positionIndex: positionIndex++,
        });
      }
    }
    offset += file.duration_seconds ?? 0;
  }
  return rows;
}

function formatChapterDuration(s: number): string {
  if (s <= 0) return "";
  const m = Math.floor(s / 60);
  const sec = Math.floor(s % 60);
  return `${m}m ${String(sec).padStart(2, "0")}s`;
}

function formatChapterStart(totalSeconds: number): string {
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = Math.floor(totalSeconds % 60);
  return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

export function ChaptersSection({ files, currentPositionSeconds, onSelect }: ChaptersSectionProps) {
  const rows = useMemo(() => buildRows(files), [files]);
  const [sort, setSort] = useState<SortMode>("position");
  const [sortOpen, setSortOpen] = useState(false);

  const sorted = useMemo(() => {
    if (sort === "longest-first") {
      return [...rows].sort((a, b) => b.durationSeconds - a.durationSeconds);
    }
    return rows;
  }, [rows, sort]);

  const currentIndex = useMemo(() => {
    if (currentPositionSeconds == null) return -1;
    for (let i = rows.length - 1; i >= 0; i--) {
      const row = rows[i];
      if (row && currentPositionSeconds >= row.absoluteStart) return i;
    }
    return -1;
  }, [rows, currentPositionSeconds]);

  if (rows.length === 0) return null;

  return (
    <section className="mt-10">
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-xl font-semibold tracking-tight">
          Chapters
          <span className="text-muted-foreground ml-2 text-sm font-normal">({rows.length})</span>
        </h2>
        <div className="relative">
          <button
            type="button"
            aria-label="Sort chapters"
            aria-haspopup="menu"
            aria-expanded={sortOpen}
            className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-xs"
            onClick={() => setSortOpen((v) => !v)}
          >
            <ArrowUpDown className="h-3.5 w-3.5" />
            Sort
          </button>
          {sortOpen && (
            <div
              role="menu"
              className="bg-popover absolute right-0 mt-2 w-44 overflow-hidden rounded-md border shadow-lg"
            >
              {(["position", "longest-first"] as SortMode[]).map((mode) => (
                <button
                  key={mode}
                  role="menuitem"
                  type="button"
                  data-active={mode === sort ? "true" : undefined}
                  className="hover:bg-muted/60 w-full px-3 py-2 text-left text-sm"
                  onClick={() => {
                    setSort(mode);
                    setSortOpen(false);
                  }}
                >
                  {mode === "position" ? "By position" : "Longest first"}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      <ol className="divide-border divide-y rounded-xl border">
        {sorted.map((row) => {
          const isCurrent = rows[currentIndex]?.chapter === row.chapter;
          return (
            <li key={`${row.chapter.index}-${row.absoluteStart}`}>
              <button
                type="button"
                onClick={() => onSelect(row.absoluteStart)}
                data-current={isCurrent ? "true" : undefined}
                className={`hover:bg-muted/50 flex w-full items-center gap-3 px-4 py-3 text-left transition-colors ${
                  isCurrent ? "bg-muted/50 border-primary border-l-2" : ""
                }`}
              >
                <span className="text-muted-foreground w-8 shrink-0 text-right text-xs tabular-nums">
                  {row.positionIndex}
                </span>
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{row.label}</span>
                <span className="text-muted-foreground hidden shrink-0 font-mono text-xs sm:inline">
                  {formatChapterStart(row.absoluteStart)}
                </span>
                <span className="text-muted-foreground shrink-0 font-mono text-xs">
                  {formatChapterDuration(row.durationSeconds)}
                </span>
                {isCurrent && (
                  <span className="text-primary flex shrink-0 items-center gap-1 text-xs">
                    <Play className="h-3 w-3 fill-current" /> playing
                  </span>
                )}
              </button>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
