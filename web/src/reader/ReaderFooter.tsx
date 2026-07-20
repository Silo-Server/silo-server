import { Keyboard } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { ChapterExtent } from "./readerNavigation";

export type ReaderFooterProps = {
  fraction: number;
  extent: ChapterExtent | null;
  chapterLabel: string | null;
  onScrub: (fraction: number) => void;
  onShowShortcuts: () => void;
};

export default function ReaderFooter({
  fraction,
  extent,
  chapterLabel,
  onScrub,
  onShowShortcuts,
}: ReaderFooterProps) {
  const percent = Math.round(Math.min(1, Math.max(0, fraction)) * 100);
  return (
    <footer className="border-border/70 bg-background/95 sticky bottom-0 z-20 border-t backdrop-blur">
      <div className="flex flex-col gap-1 px-4 py-2">
        <div className="relative h-2">
          {extent && (
            <div
              data-chapter-band
              title={chapterLabel ?? undefined}
              className="bg-primary/25 pointer-events-none absolute top-0 h-2 rounded"
              style={{
                left: `${extent.start * 100}%`,
                width: `${(extent.end - extent.start) * 100}%`,
              }}
            />
          )}
          <input
            aria-label="Reading progress"
            type="range"
            min="0"
            max="100"
            step="1"
            value={percent}
            onInput={(event) => onScrub(Number((event.target as HTMLInputElement).value) / 100)}
            className="accent-primary absolute inset-0 h-2 w-full min-w-0"
          />
        </div>
        <div className="text-muted-foreground flex items-center justify-between text-xs">
          <span className="min-w-0 truncate">{extent ? (chapterLabel ?? "") : ""}</span>
          <span className="tabular-nums">{percent}%</span>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Keyboard shortcuts"
            title="Keyboard shortcuts"
            onClick={onShowShortcuts}
          >
            <Keyboard className="size-4" />
          </Button>
        </div>
      </div>
    </footer>
  );
}
