import { useEffect, useMemo, useState } from "react";
import {
  BookOpen,
  Check,
  ChevronDown,
  CornerDownRight,
  Download,
  FileText,
  Loader2,
  MoreVertical,
  RefreshCw,
} from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";
import type { FileVersion, ItemDetail, MangaChapter } from "@/api/types";
import DownloadVersionPicker from "@/components/DownloadVersionPicker";
import MangaFilesDialog from "@/components/MangaFilesDialog";
import PageBack from "@/components/PageBack";
import RefreshMetadataDialog from "@/components/RefreshMetadataDialog";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useAuth } from "@/hooks/useAuth";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { fetchCatalogItemVersions } from "@/hooks/queries/catalogRead";
import { useRefreshItemMetadata, useWatchedStateMutation } from "@/hooks/queries/items";
import { buildItemHref, buildMediaPlayHref } from "@/lib/mediaNavigation";
import {
  buildMangaList,
  chapterLabel,
  firstUnreadChapter,
  flattenMangaList,
  type MangaListEntry,
} from "@/lib/mangaChapters";
import { cn } from "@/lib/utils";
import DetailHero from "./DetailHero";
import HeroCrewLine from "./components/HeroCrewLine";
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

function chapterVersionSummary(version: FileVersion): string {
  return metadataLine([
    version.container ? version.container.toUpperCase() : undefined,
    formatFileSize(version.file_size),
    formatPageCount(version.duration),
  ]);
}

// chapterReaderHref builds the reader link for a chapter with the series page
// as the explicit back target (avoids the chapter→reader→chapter loop).
function chapterReaderHref(
  chapterContentId: string,
  seriesContentId: string,
  libraryId?: number,
): string {
  const backTo = buildItemHref({ contentId: seriesContentId, libraryId });
  return buildMediaPlayHref({
    contentId: chapterContentId,
    type: "ebook",
    libraryId,
    backTo,
  });
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
  const readerHref = chapterReaderHref(chapter.content_id, seriesContentId, libraryId);

  // The mutation carries series_id so the series detail (this page's payload,
  // including every chapter's read flag) is invalidated and refetched after a
  // toggle. The local override only bridges the optimistic gap until the
  // refreshed chapter.read arrives.
  const watchedMutation = useWatchedStateMutation({
    content_id: chapter.content_id,
    type: "ebook",
    series_id: seriesContentId,
  });
  const [readOverride, setReadOverride] = useState<boolean | null>(null);
  useEffect(() => {
    setReadOverride(null);
  }, [chapter.read]);
  const markedRead = readOverride ?? chapter.read ?? false;

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

  const progressPct =
    !markedRead && typeof chapter.progress === "number" && chapter.progress > 0
      ? Math.max(1, Math.min(99, Math.round(chapter.progress * 100)))
      : null;

  return (
    <div
      id={`manga-chapter-${chapter.content_id}`}
      className="hover:bg-muted/40 flex items-center gap-3 px-4 py-2 transition-colors"
    >
      <Link to={readerHref} className="flex min-w-0 flex-1 items-center gap-3">
        {chapter.poster_url ? (
          <img
            src={chapter.poster_url}
            alt=""
            loading="lazy"
            className="h-12 w-8 flex-shrink-0 rounded object-cover"
          />
        ) : (
          <BookOpen className="text-muted-foreground size-[18px] flex-shrink-0" />
        )}
        <span
          className={cn(
            "truncate text-[15px] font-medium",
            markedRead ? "text-muted-foreground" : "text-foreground/90",
          )}
        >
          {label}
        </span>
        {markedRead && (
          <span className="text-success flex-shrink-0" title="Read">
            <Check className="size-4" />
            <span className="sr-only">Read</span>
          </span>
        )}
        {progressPct != null && (
          <span className="flex flex-shrink-0 items-center gap-1.5" title={`${progressPct}% read`}>
            <span className="bg-muted block h-1 w-16 overflow-hidden rounded-full">
              <span
                className="bg-primary block h-full rounded-full"
                style={{ width: `${progressPct}%` }}
              />
            </span>
            <span className="text-muted-foreground text-[11px] tabular-nums">{progressPct}%</span>
          </span>
        )}
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
            setReadOverride(next);
            watchedMutation.mutate(next, {
              onError: () => setReadOverride(!next),
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
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const entries = useMemo(() => buildMangaList(item.manga?.chapters ?? []), [item.manga?.chapters]);
  const year = item.year ? String(item.year) : "";
  const publisher = item.studios?.[0];
  const chapterRows = item.manga?.chapters ?? [];
  // Derive the badge counts from the rendered list so they always match the
  // rows on screen: a volume/section entry is one volume (buildMangaList
  // already canonicalizes v01 ≡ 1), a loose chapter entry is one chapter.
  const volumeCount = useMemo(
    () => entries.filter((e) => e.kind === "volume" || e.kind === "section").length,
    [entries],
  );
  const looseChapterCount = useMemo(
    () => entries.filter((e) => e.kind === "chapter").length,
    [entries],
  );

  // The resume target is the first unfinished chapter in reading order. Any
  // finished chapter before it means the viewer is mid-series ("Continue");
  // a fully read series restarts from the beginning.
  const anyRead = chapterRows.some((chapter) => chapter.read === true);
  const resume = useMemo(() => firstUnreadChapter(entries), [entries]);
  const fallbackStart = entries.length > 0 ? flattenFirst(entries) : null;
  const cta = resume
    ? { ...resume, verb: anyRead ? "Continue" : "Start Reading" }
    : fallbackStart
      ? { ...fallbackStart, verb: "Read Again" }
      : null;

  const [filesOpen, setFilesOpen] = useState(false);
  const [refreshOpen, setRefreshOpen] = useState(false);
  const refreshMetadataMutation = useRefreshItemMetadata();

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
            volumeCount={volumeCount}
            chapterCount={looseChapterCount}
            status={item.show_status || undefined}
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
        crewLine={<HeroCrewLine crew={item.crew ?? []} />}
        genres={item.genres}
        genreHref={(genre) => genreHref(genre, libraryId)}
        actions={
          <div className="flex flex-wrap items-center gap-3">
            {cta && (
              <Button
                asChild
                className="h-11 gap-2.5 rounded-full px-6 text-[15px] font-bold tracking-wide shadow-md"
              >
                <Link to={chapterReaderHref(cta.chapter.content_id, item.content_id, libraryId)}>
                  <BookOpen className="size-[18px]" />
                  {cta.verb}
                  <span className="text-primary-foreground/75 text-xs font-semibold">
                    {cta.label}
                  </span>
                </Link>
              </Button>
            )}
            <DropdownMenu modal={false}>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="glass"
                  size="icon-lg"
                  title="More"
                  aria-label="More actions"
                  className="size-11 rounded-full"
                >
                  <MoreVertical className="size-[18px]" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-56">
                <DropdownMenuItem onSelect={() => setFilesOpen(true)}>
                  <FileText className="size-4" />
                  View Details
                </DropdownMenuItem>
                {isAdmin && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      disabled={refreshMetadataMutation.isPending}
                      onSelect={() => setRefreshOpen(true)}
                    >
                      {refreshMetadataMutation.isPending && (
                        <RefreshCw className="size-4 animate-spin" />
                      )}
                      Refresh Metadata
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        }
      />

      <div className="page-shell space-y-4 py-10">
        {resume && flattenMangaList(entries).length > 10 && (
          <div className="flex justify-end">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="text-muted-foreground gap-1.5"
              onClick={() => {
                document
                  .getElementById(`manga-chapter-${resume.chapter.content_id}`)
                  ?.scrollIntoView({ behavior: "smooth", block: "center" });
              }}
            >
              <CornerDownRight className="size-4" />
              Jump to {resume.label}
            </Button>
          </div>
        )}
        {entries.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            No chapters found. Chapters appear here once the library scan completes.
          </p>
        ) : (
          <ul className="divide-border/40 border-border/40 divide-y overflow-hidden rounded-lg border">
            {entries.map((entry) =>
              entry.kind === "section" ? (
                <MangaSection
                  key={`section-${entry.label}`}
                  entry={entry}
                  seriesContentId={item.content_id}
                  libraryId={libraryId}
                />
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

      <MangaFilesDialog
        contentId={item.content_id}
        title={item.title}
        open={filesOpen}
        onOpenChange={setFilesOpen}
      />
      <RefreshMetadataDialog
        open={refreshOpen}
        onOpenChange={setRefreshOpen}
        onConfirm={(mode) => {
          setRefreshOpen(false);
          refreshMetadataMutation.mutate({ item, mode });
        }}
        isPending={refreshMetadataMutation.isPending}
      />
    </div>
  );
}

// MangaSection renders a multi-chapter volume as a collapsible block with a
// sticky header. Fully read sections start collapsed so long series open at
// the unread frontier.
function MangaSection({
  entry,
  seriesContentId,
  libraryId,
}: {
  entry: Extract<MangaListEntry, { kind: "section" }>;
  seriesContentId: string;
  libraryId?: number;
}) {
  const allRead = entry.chapters.every((chapter) => chapter.read === true);
  const [open, setOpen] = useState(!allRead);

  return (
    <li>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        className="bg-background/95 hover:bg-muted/40 sticky top-0 z-10 flex w-full items-center justify-between px-4 py-2 backdrop-blur transition-colors"
      >
        <span className="text-muted-foreground text-sm font-bold tracking-tight uppercase">
          {entry.label}
        </span>
        <span className="text-muted-foreground flex items-center gap-2 text-xs">
          {allRead && (
            <span className="text-success flex items-center" title="All chapters read">
              <Check className="size-3.5" />
              <span className="sr-only">All chapters read</span>
            </span>
          )}
          {entry.chapters.length} {entry.chapters.length === 1 ? "chapter" : "chapters"}
          <ChevronDown className={cn("size-4 transition-transform", !open && "-rotate-90")} />
        </span>
      </button>
      {open && (
        <ul className="divide-border/40 divide-y">
          {entry.chapters.map((chapter) => (
            <li key={chapter.content_id} className="pl-4">
              <MangaRow
                chapter={chapter}
                label={chapterLabel(chapter)}
                seriesContentId={seriesContentId}
                libraryId={libraryId}
              />
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

// flattenFirst returns the first readable unit of the series (used as the
// re-read target once everything is read).
function flattenFirst(entries: ReturnType<typeof buildMangaList>) {
  const [first] = entries;
  if (!first) return null;
  if (first.kind === "section") {
    const [chapter] = first.chapters;
    return chapter ? { chapter, label: `${first.label} · ${chapterLabel(chapter)}` } : null;
  }
  return { chapter: first.chapter, label: first.label };
}
