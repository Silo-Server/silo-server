import { useMemo, useState } from "react";
import type { ItemDetail, LeafItemUserData } from "@/api/types";
import { Button } from "@/components/ui/button";
import { FolderPlus, Play } from "lucide-react";
import AddToCollectionDialog from "@/components/AddToCollectionDialog";
import AudiobookPlayer from "@/pages/audiobooks/player/AudiobookPlayer";
import { ChaptersSection } from "@/pages/audiobooks/components/ChaptersSection";
import { NarratorCard } from "@/pages/audiobooks/components/NarratorCard";
import { NarratorPicker } from "@/pages/audiobooks/components/NarratorPicker";
import { RelatedRail } from "@/pages/audiobooks/components/RelatedRail";
import DetailHero from "@/pages/ItemDetail/DetailHero";
import MetadataBadges from "@/pages/ItemDetail/components/MetadataBadges";
import type { AudiobookChapter, AudiobookFile } from "@/lib/audiobooks/types";

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
  return files.reduce((acc, file) => acc + (file.duration_seconds ?? 0), 0);
}

function findChapterAt(
  chapters: ReturnType<typeof buildChapterList>,
  seconds: number,
): { label: string; index: number } | null {
  for (let i = chapters.length - 1; i >= 0; i--) {
    const chapter = chapters[i];
    if (chapter && seconds >= chapter.absoluteStart) {
      return { label: chapter.label, index: i + 1 };
    }
  }
  return chapters[0] ? { label: chapters[0].label, index: 1 } : null;
}

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
    for (const chapter of file.chapters ?? []) {
      result.push({
        chapter,
        absoluteStart: offset + chapter.start_seconds,
        fileId: file.id,
        label: chapter.title || `Chapter ${chapter.index + 1}`,
      });
    }
    offset += file.duration_seconds ?? 0;
  }
  return result;
}

function leafUserData(userData: ItemDetail["user_data"]): LeafItemUserData | undefined {
  return userData && "position_seconds" in userData ? userData : undefined;
}

function namesFromPeople(people: Array<{ name?: string }> | undefined): string {
  return (people ?? [])
    .map((person) => person.name?.trim())
    .filter(Boolean)
    .join(", ");
}

function namesFromCrew(item: ItemDetail, job: string): string {
  return (item.crew ?? [])
    .filter((credit) => credit.job.toLowerCase() === job)
    .map((credit) => credit.name?.trim())
    .filter(Boolean)
    .join(", ");
}

function genreHref(genre: string, libraryId?: number): string {
  const params = new URLSearchParams();
  if (libraryId) {
    params.set("tab", "library");
    params.set("genre", genre);
    return `/library/${libraryId}?${params.toString()}`;
  }
  params.set("source", "query");
  params.set("type", "audiobook");
  params.set("genre", genre);
  return `/catalog?${params.toString()}`;
}

export default function AudiobookContent({
  item,
  libraryId,
}: {
  item: ItemDetail & { type: "audiobook" };
  libraryId?: number;
}) {
  const [playerOpen, setPlayerOpen] = useState(false);
  const [startSeconds, setStartSeconds] = useState(0);
  const [addToCollectionOpen, setAddToCollectionOpen] = useState(false);
  const [playToken, setPlayToken] = useState(0);

  const files = useMemo<AudiobookFile[]>(
    () =>
      (item.versions ?? []).map((version) => ({
        id: version.file_id,
        path: version.file_path ?? version.file_name ?? "",
        duration_seconds: version.duration ?? 0,
        chapters: version.chapters ?? [],
      })),
    [item.versions],
  );

  const author =
    namesFromPeople(item.audiobook?.authors) || namesFromCrew(item, "author") || undefined;
  const narrator =
    namesFromPeople(item.audiobook?.narrators) || namesFromCrew(item, "narrator") || undefined;
  const progress = leafUserData(item.user_data);
  const resumeSeconds = progress?.position_seconds ?? 0;
  const durationTotal =
    item.audiobook?.total_duration_seconds || progress?.duration_seconds || totalDuration(files);
  const hasProgress = Boolean(
    progress &&
    resumeSeconds > 0 &&
    durationTotal > 0 &&
    (progress.is_in_progress ?? !progress.played),
  );
  const chapters = useMemo(() => buildChapterList(files), [files]);

  function openPlayer(atSeconds: number) {
    setStartSeconds(atSeconds);
    setPlayToken((token) => token + 1);
    setPlayerOpen(true);
  }

  function handlePlayResume() {
    openPlayer(hasProgress ? resumeSeconds : 0);
  }

  return (
    <div>
      <AddToCollectionDialog
        open={addToCollectionOpen}
        onOpenChange={setAddToCollectionOpen}
        mediaItemId={item.content_id}
        itemTitle={item.title}
      />

      {playerOpen && (
        <AudiobookPlayer
          key={`${item.content_id}-${startSeconds}-${playToken}`}
          contentId={item.content_id}
          title={item.title}
          author={author}
          narrator={narrator}
          posterUrl={item.poster_url}
          files={files}
          initialPositionSeconds={startSeconds}
          onClose={() => setPlayerOpen(false)}
        />
      )}

      <DetailHero
        title={item.title}
        context="Audiobook"
        studioLabel={item.audiobook?.publisher || item.studios?.[0] || undefined}
        posterUrl={item.poster_url}
        posterThumbhash={item.poster_thumbhash}
        posterOrientation="square"
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
                  <span className="font-medium">Narrated by</span>{" "}
                  {(item.audiobook?.other_narrations ?? []).length > 0 ? (
                    <NarratorPicker
                      currentNarrator={narrator}
                      currentContentId={item.content_id}
                      others={item.audiobook?.other_narrations ?? []}
                    />
                  ) : (
                    narrator
                  )}
                </span>
              )}
            </div>
          )
        }
        metadata={
          <MetadataBadges
            year={item.year > 0 ? String(item.year) : undefined}
            duration={durationTotal > 0 ? formatSeconds(durationTotal) : undefined}
          />
        }
        overview={item.overview}
        genres={item.genres}
        genreHref={(genre) => genreHref(genre, libraryId)}
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
                        const chapter = findChapterAt(chapters, resumeSeconds);
                        return chapter ? `Resume · ${chapter.label}` : "Resume";
                      })()
                    : "Play"}
                </Button>
                <Button
                  variant="outline"
                  size="lg"
                  onClick={() => setAddToCollectionOpen(true)}
                  className="gap-2"
                  title="Add to a manual collection"
                >
                  <FolderPlus className="h-4 w-4" />
                  Add to Collection
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

      <div
        className="page-shell space-y-12 py-10 sm:space-y-14"
        style={playerOpen ? { paddingBottom: "8rem" } : undefined}
      >
        {narrator && <NarratorCard narrator={narrator} />}

        {item.audiobook?.series && item.audiobook.series.entries.length > 0 && (
          <RelatedRail
            heading={
              item.audiobook.series.name ? `In ${item.audiobook.series.name}` : "In this series"
            }
            items={item.audiobook.series.entries.map((entry) => ({
              content_id: entry.content_id,
              title: entry.title,
              poster_url: entry.poster_url,
              subtitle:
                typeof entry.series_index === "number" ? `Book ${entry.series_index}` : undefined,
              highlight: entry.content_id === item.content_id,
            }))}
          />
        )}

        {(item.audiobook?.related.also_by_author ?? []).length > 0 && (
          <RelatedRail
            heading={`Also by ${author ?? "this author"}`}
            items={(item.audiobook?.related.also_by_author ?? []).map((entry) => ({
              content_id: entry.content_id,
              title: entry.title,
              poster_url: entry.poster_url,
              subtitle: entry.year ? String(entry.year) : undefined,
            }))}
          />
        )}

        {(item.audiobook?.related.similar ?? []).length > 0 && (
          <RelatedRail
            heading="You might also like"
            items={(item.audiobook?.related.similar ?? []).map((entry) => ({
              content_id: entry.content_id,
              title: entry.title,
              poster_url: entry.poster_url,
              subtitle: entry.year ? String(entry.year) : undefined,
            }))}
          />
        )}

        <ChaptersSection
          files={files}
          currentPositionSeconds={playerOpen ? startSeconds : resumeSeconds || null}
          onSelect={(seconds) => openPlayer(seconds)}
        />
      </div>
    </div>
  );
}
