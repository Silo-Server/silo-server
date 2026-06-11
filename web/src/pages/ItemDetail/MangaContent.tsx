import { useMemo, useState } from "react";
import { BookOpen, Check, Download, Loader2 } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";
import type { FileVersion, ItemDetail, MangaChapter } from "@/api/types";
import DownloadVersionPicker from "@/components/DownloadVersionPicker";
import PageBack from "@/components/PageBack";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/hooks/useAuth";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { fetchCatalogItemVersions } from "@/hooks/queries/catalogRead";
import { useWatchedStateMutation } from "@/hooks/queries/items";
import { buildItemHref, buildMediaPlayHref } from "@/lib/mediaNavigation";
import { buildMangaList } from "@/lib/mangaChapters";
import DetailHero from "./DetailHero";
import MetadataBadges from "./components/MetadataBadges";
import ScoreRow from "./components/ScoreRow";
import { formatFileSize, formatPageCount, metadataLine } from "./components/versionFormatUtils";

function genreHref(genre: string, libraryId?: number): string {
  const params = new URLSearchParams();
  if (libraryId) {
    params.set("tab", "library");
    params.set("genre", genre);
    return `/library/${libraryId}?${params.toString()}`;
  }
  params.set("source", "query");
  params.set("type", "manga");
  params.set("genre", genre);
  return `/catalog?${params.toString()}`;
}

// chapterLabel prefers a "Chapter <n>" form derived from the index, falling
// back to the chapter's own title when no index is available.
function chapterLabel(chapter: MangaChapter): string {
  if (typeof chapter.chapter_index === "number") {
    return `Chapter ${chapter.chapter_index}`;
  }
  return chapter.title || "Chapter";
}

function chapterVersionSummary(version: FileVersion): string {
  return metadataLine([
    version.container ? version.container.toUpperCase() : undefined,
    formatFileSize(version.file_size),
    formatPageCount(version.duration),
  ]);
}

// MangaRow is a single reader row used for volume units, loose chapters, and
// chapters nested inside a volume section. Each row offers Read (the reader
// link), Mark-read, and Download. Because the manga detail payload carries only
// the chapter's content_id (no file versions), Download lazily fetches the
// chapter's versions on demand and hands them to the shared picker.
function MangaRow({
  chapter,
  label,
  seriesContentId,
  libraryId,
}: {
  chapter: MangaChapter;
  label: string;
  seriesContentId: string;
  libraryId?: number;
}) {
  const { user } = useAuth();
  // The series detail is where "back" should land from the reader; passing it
  // explicitly avoids the chapter→reader→chapter loop. Only manga rows set this.
  const backTo = buildItemHref({ contentId: seriesContentId, libraryId });
  const readerHref = `${buildMediaPlayHref({
    contentId: chapter.content_id,
    type: "ebook",
    libraryId,
  })}&backTo=${encodeURIComponent(backTo)}`;

  // The row payload has no read state, so we track the user's mark-read action
  // locally and fire the shared watched mutation against this chapter's id.
  const watchedMutation = useWatchedStateMutation({
    content_id: chapter.content_id,
    type: "ebook",
  });
  const [markedRead, setMarkedRead] = useState(false);

  const [downloadOpen, setDownloadOpen] = useState(false);
  const [downloadVersions, setDownloadVersions] = useState<FileVersion[] | null>(null);
  const [loadingVersions, setLoadingVersions] = useState(false);
  const canDownload = Boolean(user?.download_allowed);

  const handleDownload = async () => {
    if (loadingVersions) return;
    if (downloadVersions && downloadVersions.length > 0) {
      setDownloadOpen(true);
      return;
    }
    setLoadingVersions(true);
    try {
      const versions = await fetchCatalogItemVersions(chapter.content_id);
      if (versions.length === 0) {
        toast.error("No downloadable files for this chapter");
        return;
      }
      setDownloadVersions(versions);
      setDownloadOpen(true);
    } catch {
      toast.error("Couldn't load chapter files. Try again later");
    } finally {
      setLoadingVersions(false);
    }
  };

  return (
    <div className="hover:bg-muted/40 flex items-center gap-3 px-4 py-3 transition-colors">
      <Link to={readerHref} className="flex min-w-0 flex-1 items-center gap-3">
        <BookOpen className="text-muted-foreground size-[18px] flex-shrink-0" />
        <span className="text-foreground/90 truncate text-[15px] font-medium">{label}</span>
      </Link>
      <div className="flex flex-shrink-0 items-center gap-1">
        <Button
          type="button"
          variant={markedRead ? "secondary" : "ghost"}
          size="icon-sm"
          aria-label={markedRead ? "Mark chapter unread" : "Mark chapter read"}
          aria-pressed={markedRead}
          title={markedRead ? "Mark unread" : "Mark read"}
          disabled={watchedMutation.isPending}
          onClick={() => {
            const next = !markedRead;
            setMarkedRead(next);
            watchedMutation.mutate(next, {
              onError: () => setMarkedRead(!next),
            });
          }}
        >
          <Check className="size-4" />
        </Button>
        {canDownload && (
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label="Download chapter"
            title="Download"
            disabled={loadingVersions}
            onClick={() => void handleDownload()}
          >
            {loadingVersions ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <Download className="size-4" />
            )}
          </Button>
        )}
      </div>
      {canDownload && downloadVersions && (
        <DownloadVersionPicker
          open={downloadOpen}
          onOpenChange={setDownloadOpen}
          versions={downloadVersions}
          title={label}
          summaryBuilder={chapterVersionSummary}
        />
      )}
    </div>
  );
}

export default function MangaContent({
  item,
  libraryId,
}: {
  item: ItemDetail & { type: "manga" };
  libraryId?: number;
}) {
  useAmbientColor(item.poster_thumbhash);
  const entries = useMemo(() => buildMangaList(item.manga?.chapters ?? []), [item.manga?.chapters]);
  const year = item.year ? String(item.year) : "";
  const publisher = item.studios?.[0];

  return (
    <div>
      <DetailHero
        title={item.title}
        topNav={<PageBack />}
        context="Manga"
        studioLabel={publisher}
        backdropUrl={item.backdrop_url}
        backdropThumbhash={item.backdrop_thumbhash}
        posterUrl={item.poster_url}
        posterThumbhash={item.poster_thumbhash}
        metadata={
          <MetadataBadges
            year={year || undefined}
            contentRating={item.content_rating || undefined}
          />
        }
        scoreRow={
          <ScoreRow
            ratingImdb={item.rating_imdb}
            ratingRtCritic={item.rating_rt_critic}
            ratingRtAudience={item.rating_rt_audience}
          />
        }
        overview={item.overview}
        genres={item.genres}
        genreHref={(genre) => genreHref(genre, libraryId)}
      />

      <div className="page-shell space-y-6 py-10">
        {entries.length === 0 ? (
          <p className="text-muted-foreground text-sm">No chapters found.</p>
        ) : (
          <ul className="divide-border/40 border-border/40 divide-y overflow-hidden rounded-lg border">
            {entries.map((entry) =>
              entry.kind === "section" ? (
                <li key={`section-${entry.label}`}>
                  <h2 className="text-muted-foreground bg-muted/30 px-4 py-2 text-sm font-bold tracking-tight uppercase">
                    {entry.label}
                  </h2>
                  <ul className="divide-border/40 divide-y">
                    {entry.chapters.map((chapter) => (
                      <li key={chapter.content_id} className="pl-4">
                        <MangaRow
                          chapter={chapter}
                          label={chapterLabel(chapter)}
                          seriesContentId={item.content_id}
                          libraryId={libraryId}
                        />
                      </li>
                    ))}
                  </ul>
                </li>
              ) : (
                <li key={entry.chapter.content_id}>
                  <MangaRow
                    chapter={entry.chapter}
                    label={entry.label}
                    seriesContentId={item.content_id}
                    libraryId={libraryId}
                  />
                </li>
              ),
            )}
          </ul>
        )}
      </div>
    </div>
  );
}
