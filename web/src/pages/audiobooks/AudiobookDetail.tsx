import { useState } from "react";
import { useParams } from "react-router";
import { useAudiobook } from "@/hooks/audiobooks/useAudiobook";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { ChevronDown, ChevronRight, Play, BookHeadphones } from "lucide-react";
import AudiobookPlayer from "./AudiobookPlayer";
import type { AudiobookChapter, AudiobookFile } from "@/lib/audiobooks/types";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatSeconds(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds < 0) return "";
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = Math.floor(totalSeconds % 60);
  if (h > 0) {
    return `${h}h ${String(m).padStart(2, "0")}m`;
  }
  if (m > 0) {
    return `${m}m ${String(s).padStart(2, "0")}s`;
  }
  return `${s}s`;
}

function totalDuration(files: AudiobookFile[]): number {
  return files.reduce((acc, f) => acc + (f.duration_seconds ?? 0), 0);
}

/** Gather all chapters across files, adjusting start/end by each file's cumulative offset. */
function buildChapterList(files: AudiobookFile[]): Array<{
  chapter: AudiobookChapter;
  absoluteStart: number;
  fileId: number;
  label: string;
}> {
  const result: Array<{
    chapter: AudiobookChapter;
    absoluteStart: number;
    fileId: number;
    label: string;
  }> = [];
  let offset = 0;
  for (const file of files) {
    if (file.chapters && file.chapters.length > 0) {
      for (const ch of file.chapters) {
        result.push({
          chapter: ch,
          absoluteStart: offset + ch.start_seconds,
          fileId: file.id,
          label: ch.title || `Chapter ${ch.index + 1}`,
        });
      }
    }
    offset += file.duration_seconds ?? 0;
  }
  return result;
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

interface ChapterListProps {
  files: AudiobookFile[];
  onSelect: (absoluteSeconds: number) => void;
}

function ChapterList({ files, onSelect }: ChapterListProps) {
  const [expanded, setExpanded] = useState(true);
  const chapters = buildChapterList(files);

  if (chapters.length === 0) return null;

  return (
    <div className="mt-10">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="mb-4 flex items-center gap-1.5 text-xl font-semibold tracking-tight"
      >
        {expanded ? (
          <ChevronDown className="h-5 w-5 opacity-60" />
        ) : (
          <ChevronRight className="h-5 w-5 opacity-60" />
        )}
        Chapters
        <span className="text-muted-foreground ml-1.5 text-sm font-normal">
          ({chapters.length})
        </span>
      </button>

      {expanded && (
        <ol className="divide-border divide-y rounded-xl border">
          {chapters.map(({ chapter, absoluteStart, label }, i) => (
            <li key={`${chapter.index}-${i}`}>
              <button
                type="button"
                onClick={() => onSelect(absoluteStart)}
                className="hover:bg-muted/50 flex w-full items-center gap-3 px-4 py-3 text-left transition-colors"
              >
                <span className="text-muted-foreground w-6 shrink-0 text-right text-xs tabular-nums">
                  {i + 1}
                </span>
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{label}</span>
                <span className="text-muted-foreground shrink-0 font-mono text-xs">
                  {formatSeconds(absoluteStart)}
                </span>
                <Play className="text-muted-foreground h-3.5 w-3.5 shrink-0 opacity-0 transition-opacity group-hover:opacity-100" />
              </button>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Loading skeleton
// ---------------------------------------------------------------------------

function AudiobookDetailSkeleton() {
  return (
    <div className="page-shell py-8">
      <div className="flex flex-col gap-8 sm:flex-row">
        <Skeleton className="aspect-[2/3] w-full rounded-xl sm:w-[200px] sm:shrink-0 md:w-[260px]" />
        <div className="flex flex-1 flex-col gap-3">
          <Skeleton className="h-8 w-3/4" />
          <Skeleton className="h-4 w-1/3" />
          <Skeleton className="h-4 w-1/4" />
          <Skeleton className="mt-4 h-24 w-full" />
          <Skeleton className="mt-6 h-10 w-32" />
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

export default function AudiobookDetail() {
  const { contentId } = useParams<{ contentId: string }>();
  const { data, isLoading, error } = useAudiobook(contentId);

  const [playerOpen, setPlayerOpen] = useState(false);
  const [startSeconds, setStartSeconds] = useState(0);

  if (isLoading && !data) {
    return <AudiobookDetailSkeleton />;
  }

  if (error || !data) {
    return (
      <div className="page-shell py-8">
        <div className="text-destructive py-16 text-center text-sm">
          {error instanceof Error ? error.message : "Failed to load audiobook."}
        </div>
      </div>
    );
  }

  const { audiobook, author, narrator, files, progress } = data;

  const hasProgress = Boolean(progress && progress.position_seconds > 0 && !progress.completed);
  const resumeSeconds = progress?.position_seconds ?? 0;
  const durationTotal = totalDuration(files);

  function openPlayer(atSeconds: number) {
    setStartSeconds(atSeconds);
    setPlayerOpen(true);
  }

  function handlePlayResume() {
    openPlayer(hasProgress ? resumeSeconds : 0);
  }

  return (
    <div>
      {/* Sticky inline player */}
      {playerOpen && (
        <div className="bg-background sticky top-0 z-30 border-b">
          <AudiobookPlayer
            contentId={contentId ?? ""}
            files={files}
            initialPositionSeconds={startSeconds}
            onClose={() => setPlayerOpen(false)}
          />
        </div>
      )}

      <div className="page-shell py-8">
        {/* Hero row */}
        <div className="flex flex-col gap-8 sm:flex-row">
          {/* Cover */}
          <div className="w-full shrink-0 sm:w-[200px] md:w-[260px]">
            <div className="aspect-[2/3] overflow-hidden rounded-xl">
              {audiobook.poster_url ? (
                <img
                  src={audiobook.poster_url}
                  alt={audiobook.title}
                  className="h-full w-full object-cover"
                />
              ) : (
                <div className="bg-muted text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-3 p-4 text-center">
                  <BookHeadphones className="h-16 w-16 opacity-30" />
                  <span className="line-clamp-3 text-sm font-medium">{audiobook.title}</span>
                </div>
              )}
            </div>
          </div>

          {/* Info */}
          <div className="flex flex-1 flex-col">
            {/* Eyebrow */}
            <p className="text-muted-foreground mb-2 text-xs font-semibold tracking-[0.2em] uppercase">
              Audiobook
              {audiobook.year > 0 && <> &middot; {audiobook.year}</>}
              {durationTotal > 0 && <> &middot; {formatSeconds(durationTotal)}</>}
            </p>

            {/* Title */}
            <h1 className="mb-1 text-3xl leading-tight font-bold tracking-tight">
              {audiobook.title}
            </h1>

            {/* Author / narrator */}
            {author && (
              <p className="text-muted-foreground mt-1 text-sm">
                <span className="font-medium">By</span> {author}
              </p>
            )}
            {narrator && (
              <p className="text-muted-foreground mt-0.5 text-sm">
                <span className="font-medium">Narrated by</span> {narrator}
              </p>
            )}

            {/* Overview */}
            {audiobook.overview && (
              <p className="mt-5 max-w-prose text-sm leading-relaxed">{audiobook.overview}</p>
            )}

            {/* Actions */}
            {files.length > 0 && (
              <div className="mt-6 flex items-center gap-3">
                <Button onClick={handlePlayResume} className="gap-2">
                  <Play className="h-4 w-4" />
                  {hasProgress ? "Resume" : "Play"}
                </Button>
                {hasProgress && (
                  <Button variant="outline" onClick={() => openPlayer(0)}>
                    Play from Start
                  </Button>
                )}
              </div>
            )}

            {/* Progress bar */}
            {hasProgress && durationTotal > 0 && (
              <div className="mt-4">
                <div className="bg-muted h-1.5 w-full max-w-xs overflow-hidden rounded-full">
                  <div
                    className="bg-primary h-full rounded-full transition-all"
                    style={{ width: `${Math.min(100, (resumeSeconds / durationTotal) * 100)}%` }}
                  />
                </div>
                <p className="text-muted-foreground mt-1 text-xs">
                  {formatSeconds(resumeSeconds)} listened
                </p>
              </div>
            )}
          </div>
        </div>

        {/* Chapter list */}
        {files.length > 0 && <ChapterList files={files} onSelect={(s) => openPlayer(s)} />}
      </div>
    </div>
  );
}
