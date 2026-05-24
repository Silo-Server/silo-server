import { useState } from "react";
import { useParams } from "react-router";
import { useAudiobook } from "@/hooks/audiobooks/useAudiobook";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { Play } from "lucide-react";
import AudiobookPlayer from "./player/AudiobookPlayer";
import { ChaptersSection } from "./components/ChaptersSection";
import { NarratorCard } from "./components/NarratorCard";
import { RelatedRail } from "./components/RelatedRail";
import DetailHero from "@/pages/ItemDetail/DetailHero";
import MetadataBadges from "@/pages/ItemDetail/components/MetadataBadges";
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

function findChapterAt(
  chapters: ReturnType<typeof buildChapterList>,
  seconds: number,
): { label: string; index: number } | null {
  for (let i = chapters.length - 1; i >= 0; i--) {
    const ch = chapters[i];
    if (ch && seconds >= ch.absoluteStart) {
      return { label: ch.label, index: i + 1 };
    }
  }
  return chapters[0] ? { label: chapters[0].label, index: 1 } : null;
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
      {/* The player owns its own positioning (mini bar at the bottom or
          Now Listening full-screen overlay). Key forces a remount when the
          user jumps to a different position so initialPositionSeconds
          takes effect even if the player was already open. */}
      {playerOpen && (
        <AudiobookPlayer
          key={`${contentId}-${startSeconds}`}
          contentId={contentId ?? ""}
          title={audiobook.title}
          author={author}
          narrator={narrator}
          posterUrl={audiobook.poster_url}
          files={files}
          initialPositionSeconds={startSeconds}
          onClose={() => setPlayerOpen(false)}
        />
      )}

      <DetailHero
        title={audiobook.title}
        context="Audiobook"
        studioLabel={data.audiobook.publisher || undefined}
        posterUrl={audiobook.poster_url}
        posterOrientation="portrait"
        subtitle={
          (author || narrator) && (
            <div className="text-muted-foreground flex flex-col gap-0.5 text-sm">
              {author && (
                <span>
                  <span className="font-medium">By</span> {author}
                </span>
              )}
              {narrator && (
                <span>
                  <span className="font-medium">Narrated by</span> {narrator}
                </span>
              )}
            </div>
          )
        }
        metadata={
          <MetadataBadges
            year={audiobook.year > 0 ? String(audiobook.year) : undefined}
            duration={durationTotal > 0 ? formatSeconds(durationTotal) : undefined}
          />
        }
        overview={audiobook.overview}
        genres={data.audiobook.genres}
        actions={
          files.length > 0 && (
            <div className="flex max-w-md flex-col gap-3">
              {hasProgress && durationTotal > 0 && (
                <div>
                  <div className="bg-muted h-1.5 w-full overflow-hidden rounded-full">
                    <div
                      className="bg-primary h-full rounded-full transition-all"
                      style={{ width: `${Math.min(100, (resumeSeconds / durationTotal) * 100)}%` }}
                    />
                  </div>
                  <p className="text-muted-foreground mt-1 text-xs">
                    {formatSeconds(resumeSeconds)} listened ·{" "}
                    {Math.round((resumeSeconds / durationTotal) * 100)}%
                  </p>
                </div>
              )}
              <div className="flex flex-wrap items-center gap-3">
                <Button onClick={handlePlayResume} size="lg" className="gap-2">
                  <Play className="h-4 w-4 fill-current" />
                  {hasProgress
                    ? (() => {
                        const ch = findChapterAt(buildChapterList(files), resumeSeconds);
                        return ch ? `Resume · ${ch.label}` : "Resume";
                      })()
                    : "Play"}
                </Button>
                {hasProgress && (
                  <Button variant="outline" size="lg" onClick={() => openPlayer(0)}>
                    Play from Start
                  </Button>
                )}
              </div>
            </div>
          )
        }
      />

      <div className={`page-shell pb-12 ${playerOpen ? "pb-32" : ""}`}>
        <ChaptersSection
          files={files}
          currentPositionSeconds={playerOpen ? startSeconds : resumeSeconds || null}
          onSelect={(s) => openPlayer(s)}
        />
        {narrator && <NarratorCard narrator={narrator} />}

        {data.similar_audiobooks && data.similar_audiobooks.length > 0 && (
          <RelatedRail
            heading="Similar audiobooks"
            subtitle="Based on listening patterns"
            items={data.similar_audiobooks.map((it) => ({
              content_id: it.content_id,
              title: it.title,
              poster_url: it.poster_url,
              subtitle: it.year ? String(it.year) : undefined,
            }))}
          />
        )}

        {data.also_by_author && data.also_by_author.length > 0 && (
          <RelatedRail
            heading={`Also by ${author ?? "this author"}`}
            items={data.also_by_author.map((it) => ({
              content_id: it.content_id,
              title: it.title,
              poster_url: it.poster_url,
              subtitle: it.year ? String(it.year) : undefined,
            }))}
          />
        )}

        {data.in_series && data.in_series.entries.length > 0 && (
          <RelatedRail
            heading={data.in_series.name ? `In ${data.in_series.name}` : "In this series"}
            items={data.in_series.entries.map((it) => ({
              content_id: it.content_id,
              title: it.title,
              poster_url: it.poster_url,
              subtitle: typeof it.series_index === "number" ? `Book ${it.series_index}` : undefined,
              highlight: it.content_id === contentId,
            }))}
          />
        )}
      </div>
    </div>
  );
}
