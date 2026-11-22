import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { BookOpen, Check, ChevronRight, Download, RotateCcw } from "lucide-react";
import type { AudiobookRelatedItem, FileVersion, ItemDetail } from "@/api/types";
import DownloadVersionPicker from "@/components/DownloadVersionPicker";
import MediaCardArtwork from "@/components/MediaCardArtwork";
import MediaLocations from "@/components/MediaLocations";
import PageBack from "@/components/PageBack";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Button } from "@/components/ui/button";
import { useEbookReaderProgress } from "@/hooks/queries/ebookReaderProgress";
import { useWatchedStateMutation } from "@/hooks/queries/items";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useAuth } from "@/hooks/useAuth";
import { numberText, useT, type Translate } from "@/i18n";
import { joinList } from "@/lib/appLocale";
import { formatFileSize } from "@/lib/mediaFormat";
import { RelatedRail } from "@/pages/audiobooks/components/RelatedRail";
import { isReaderSupportedFile, readerFileFormat } from "@/reader/FoliateBookReader";
import DetailOverview from "./components/DetailOverview";
import { hasMachineTranslation } from "./components/machineTranslation";
import MetadataBadges from "./components/MetadataBadges";
import ScoreRow from "./components/ScoreRow";
import { formatPageCount, metadataLine } from "./components/versionFormatUtils";
import { getWatchedActionLabel } from "./watchedState";

import "@/pages/audiobooks/audiobooks.css";

function authorNames(item: ItemDetail): string[] {
  const extensionAuthors = (item.ebook?.authors ?? [])
    .map((author) => author.name.trim())
    .filter((name) => name !== "");
  if (extensionAuthors.length > 0) {
    return extensionAuthors;
  }
  return (item.crew ?? [])
    .filter((credit) => credit.job.toLowerCase() === "author")
    .map((credit) => credit.name.trim())
    .filter((name) => name !== "");
}

function ebookVersionSummary(version: FileVersion): string {
  return metadataLine([
    version.container ? version.container.toUpperCase() : undefined,
    formatFileSize(version.file_size),
    formatPageCount(version.duration),
  ]);
}

function preferredReadVersion(versions: FileVersion[]): FileVersion | undefined {
  return (
    versions.find((version) => readerFileFormat(version) === "epub") ??
    versions.find((version) => isReaderSupportedFile(version))
  );
}

function progressReadVersion(
  versions: FileVersion[],
  fileID: number | undefined,
): FileVersion | undefined {
  if (typeof fileID !== "number") return undefined;
  return versions.find((version) => version.file_id === fileID && isReaderSupportedFile(version));
}

function uniqueFormats(versions: FileVersion[]): string {
  const seen = new Set<string>();
  const formats: string[] = [];
  for (const version of versions) {
    const raw = (readerFileFormat(version) || version.container || "").trim().toUpperCase();
    if (!raw || seen.has(raw)) continue;
    seen.add(raw);
    formats.push(raw);
  }
  return formats.length > 0 ? joinList(formats) : "";
}

function clampInProgressPercent(ratio: number): number {
  return Math.min(99, Math.max(1, Math.round(ratio * 100)));
}

function libraryFilterHref(field: "genre" | "series", value: string, libraryId: number): string {
  const params = new URLSearchParams();
  params.set("tab", "library");
  if (field === "genre") {
    params.set("genre", value);
  } else {
    params.set("groups[0][rules][0][field]", "series");
    params.set("groups[0][rules][0][op]", "is");
    params.set("groups[0][rules][0][value]", value);
  }
  return `/library/${libraryId}?${params.toString()}`;
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-foreground/65 text-xs">{label}</dt>
      <dd className="truncate text-[0.9375rem] font-semibold">{children}</dd>
    </div>
  );
}

/**
 * A related book's line under its title: finished, reading percentage while
 * partway through, else its publication year.
 */
function relatedStatus(t: Translate, entry: AudiobookRelatedItem): string | undefined {
  if (entry.played) return t("web.detail.manga.readStatus");
  if (entry.progress_ratio != null && entry.progress_ratio > 0) {
    return t("web.home.continueWatching.percentRead", {
      percent: clampInProgressPercent(entry.progress_ratio),
    });
  }
  return entry.year ? numberText(entry.year) : undefined;
}

function relatedRailItem(t: Translate, entry: AudiobookRelatedItem) {
  return {
    content_id: entry.content_id,
    title: entry.title,
    poster_url: entry.poster_url,
    poster_thumbhash: entry.poster_thumbhash,
    subtitle: relatedStatus(t, entry),
    progress: entry.played ? undefined : entry.progress_ratio,
  };
}

export default function EbookContent({
  item,
  libraryId,
  showAdvisoryAge,
}: {
  item: ItemDetail & { type: "ebook" };
  libraryId?: number;
  showAdvisoryAge?: boolean;
}) {
  const t = useT();
  useAmbientColor(item.poster_thumbhash);
  const { user } = useAuth();
  const { data: readerProgress } = useEbookReaderProgress(item.content_id);
  const watchedMutation = useWatchedStateMutation(item);
  const isRead = item.user_data?.played === true;
  const [downloadOpen, setDownloadOpen] = useState(false);

  const authors = authorNames(item);
  const author = authors.length > 0 ? joinList(authors) : undefined;
  const series = item.ebook?.series;
  const seriesEntries = series?.entries ?? [];
  const thisSeriesEntry = seriesEntries.find((entry) => entry.content_id === item.content_id);
  const seriesIndex = thisSeriesEntry?.series_index;
  const publisher = item.ebook?.publisher || item.studios?.[0] || undefined;
  const alsoByAuthor = item.ebook?.related.also_by_author ?? [];
  const similar = item.ebook?.related.similar ?? [];

  const matchedProgressVersion = progressReadVersion(item.versions, readerProgress?.file_id);
  const rawProgress = readerProgress?.progress;
  // Continue and the progress bar require a visible, reader-supported file
  // with an unfinished saved position, so a finished book or a scoped library
  // missing the saved edition never advertises a resume that starts at zero.
  const hasInProgress = Boolean(
    !isRead &&
    matchedProgressVersion &&
    typeof rawProgress === "number" &&
    Number.isFinite(rawProgress) &&
    rawProgress > 0 &&
    rawProgress < 1,
  );
  const progressPercent =
    hasInProgress && rawProgress != null ? clampInProgressPercent(rawProgress) : 0;

  const readVersion = matchedProgressVersion ?? preferredReadVersion(item.versions);
  const primaryFile = readVersion ?? item.versions[0];
  const canRead = Boolean(readVersion);
  const canDownload = Boolean(user?.download_allowed && item.versions.length > 0);

  const readerParams = new URLSearchParams();
  if (readVersion) {
    readerParams.set("file_id", String(readVersion.file_id));
  }
  if (libraryId !== undefined) {
    readerParams.set("libraryId", String(libraryId));
  }
  const readerQuery = readerParams.toString();
  const readerHref = `/reader/ebook/${encodeURIComponent(item.content_id)}${
    readerQuery ? `?${readerQuery}` : ""
  }`;

  const formats = uniqueFormats(item.versions);
  const fileSize = primaryFile?.file_size ? formatFileSize(primaryFile.file_size) : "";
  const hasFacts = Boolean(formats || fileSize || item.year > 0 || publisher);
  const hasContentBadges =
    Boolean(item.content_rating) || Boolean(showAdvisoryAge && (item.advisory_age ?? 0) > 0);
  const hasRatings = Boolean(item.ratings && item.ratings.length > 0);
  const seriesLabel = series?.name
    ? typeof seriesIndex === "number"
      ? t("web.audiobooks.detail.seriesBook", { series: series.name, number: seriesIndex })
      : series.name
    : undefined;

  return (
    <div className="relative isolate">
      {item.poster_thumbhash && <div aria-hidden="true" className="audiobook-tint" />}
      <PageBack />

      <div className="page-shell-wide audiobook-detail-grid audiobook-detail-grid--ebook pt-16 pb-14 sm:pt-20 lg:pt-24">
        <div className="audiobook-detail-side">
          <div className="audiobook-detail-cover overflow-hidden rounded-xl shadow-[0_24px_48px_-20px_rgb(0_0_0/0.85)] lg:rounded-2xl">
            <MediaCardArtwork
              src={item.poster_url}
              alt={item.title}
              mediaType="ebook"
              thumbhash={item.poster_thumbhash}
              scrim="none"
              priority
            />
          </div>

          <div className="audiobook-detail-actions grid grid-cols-[minmax(0,1fr)] gap-3">
            {canRead ? (
              <Button asChild size="lg" className="w-full gap-2 rounded-full">
                <ViewTransitionLink to={readerHref}>
                  <BookOpen className="h-4 w-4 shrink-0" />
                  <span className="truncate">
                    {hasInProgress
                      ? t("web.detail.books.continueReading")
                      : t("web.detail.books.read")}
                  </span>
                </ViewTransitionLink>
              </Button>
            ) : (
              canDownload && (
                <Button
                  type="button"
                  size="lg"
                  className="w-full gap-2 rounded-full"
                  onClick={() => setDownloadOpen(true)}
                >
                  <Download className="h-4 w-4 shrink-0" />
                  <span className="truncate">{t("web.detail.actions.download")}</span>
                </Button>
              )
            )}

            {(hasInProgress || isRead) && (
              <div>
                <div className="bg-foreground/15 h-1.5 w-full overflow-hidden rounded-full">
                  <div
                    className={`${isRead ? "bg-success" : "bg-primary"} h-full rounded-full transition-[width]`}
                    style={{ width: `${isRead ? 100 : progressPercent}%` }}
                  />
                </div>
                <div className="mt-1.5 flex items-center justify-between gap-3 text-xs">
                  {isRead ? (
                    <span className="text-foreground inline-flex min-w-0 items-center gap-1.5 font-medium">
                      <Check className="text-success size-3.5 shrink-0" aria-hidden="true" />
                      <span className="truncate">{t("web.detail.manga.readStatus")}</span>
                    </span>
                  ) : (
                    <span className="text-foreground/70 truncate">
                      {t("web.reader.toolbar.progress")}
                    </span>
                  )}
                  <span className="text-foreground shrink-0 font-semibold tabular-nums">
                    {t("web.detail.manga.percent", { percent: isRead ? 100 : progressPercent })}
                  </span>
                </div>
              </div>
            )}

            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant="secondary"
                className="h-10 max-w-full flex-[1_0_auto] gap-2 rounded-full px-3.5 md:h-9"
                onClick={() => watchedMutation.mutate(!isRead)}
                disabled={watchedMutation.isPending}
              >
                {isRead ? (
                  <RotateCcw className="h-4 w-4 shrink-0" />
                ) : (
                  <Check className="h-4 w-4 shrink-0" />
                )}
                <span className="truncate">{getWatchedActionLabel(item)}</span>
              </Button>
              {canRead && canDownload && (
                <Button
                  type="button"
                  variant="secondary"
                  className="h-10 max-w-full flex-[1_0_auto] gap-2 rounded-full px-3.5 md:h-9"
                  onClick={() => setDownloadOpen(true)}
                >
                  <Download className="h-4 w-4 shrink-0" />
                  <span className="truncate">{t("web.detail.actions.download")}</span>
                </Button>
              )}
            </div>
          </div>
        </div>

        <header className="audiobook-detail-head grid grid-cols-[minmax(0,1fr)] gap-1.5 md:pt-1 lg:gap-2 lg:pt-3">
          {seriesLabel &&
            (libraryId !== undefined && series?.name ? (
              <Link
                to={libraryFilterHref("series", series.name, libraryId)}
                className="group text-foreground/80 hover:text-foreground inline-flex w-fit items-center gap-0.5 text-sm font-semibold underline-offset-4 transition-colors hover:underline"
              >
                <span>{seriesLabel}</span>
                <ChevronRight
                  className="text-muted-foreground size-3.5 shrink-0 transition-transform group-hover:translate-x-0.5 rtl:group-hover:-translate-x-0.5"
                  aria-hidden="true"
                />
              </Link>
            ) : (
              <p className="text-foreground/80 w-fit text-sm font-semibold">{seriesLabel}</p>
            ))}
          <h1 className="bidi-text font-display text-2xl leading-[1.08] font-extrabold tracking-[-0.03em] text-balance sm:text-4xl lg:mb-1 lg:text-5xl">
            {item.title}
          </h1>
          {author && (
            <p className="text-foreground/80 text-sm sm:text-base">
              {t("web.detail.books.byAuthor", {
                label: (chunks) => <span>{chunks}</span>,
                name: (chunks) => <span className="text-foreground font-semibold">{chunks}</span>,
                author,
              })}
            </p>
          )}
        </header>

        <div className="audiobook-detail-body grid grid-cols-[minmax(0,1fr)] gap-10 lg:gap-12">
          <div className="grid grid-cols-[minmax(0,1fr)] gap-6">
            {hasFacts && (
              <dl className="border-border/60 grid grid-cols-2 gap-x-6 gap-y-3 border-y py-4 sm:flex sm:flex-wrap sm:gap-x-9">
                {formats && <Fact label={t("web.detail.mediaInfo.row.format")}>{formats}</Fact>}
                {fileSize && <Fact label={t("web.detail.mediaInfo.row.fileSize")}>{fileSize}</Fact>}
                {item.year > 0 && (
                  <Fact label={t("web.audiobooks.detail.released")}>{numberText(item.year)}</Fact>
                )}
                {publisher && <Fact label={t("web.audiobooks.detail.publisher")}>{publisher}</Fact>}
              </dl>
            )}

            {(hasContentBadges || hasRatings) && (
              <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
                {hasContentBadges && (
                  <MetadataBadges
                    contentRating={item.content_rating || undefined}
                    advisoryAge={showAdvisoryAge ? (item.advisory_age ?? undefined) : undefined}
                    advisorySource={item.advisory_source || undefined}
                  />
                )}
                {hasRatings && <ScoreRow ratings={item.ratings} />}
              </div>
            )}

            {item.overview && (
              <div className="max-w-[68ch]">
                <DetailOverview
                  key={item.content_id}
                  overview={item.overview}
                  clamp
                  clampLines={4}
                  machineTranslated={hasMachineTranslation(item.machine_translated_fields)}
                />
              </div>
            )}

            {item.genres && item.genres.length > 0 && (
              <div className="flex flex-wrap gap-2">
                {item.genres.map((genre) =>
                  libraryId !== undefined ? (
                    <Link
                      key={genre}
                      to={libraryFilterHref("genre", genre, libraryId)}
                      className="metadata-badge hover:bg-foreground/10 transition-colors"
                    >
                      {genre}
                    </Link>
                  ) : (
                    <span key={genre} className="metadata-badge">
                      {genre}
                    </span>
                  ),
                )}
              </div>
            )}
          </div>

          {series && seriesEntries.length > 0 && (
            <RelatedRail
              key={`series:${series.name || item.content_id}`}
              heading={
                series.name
                  ? t("web.detail.books.inSeries", { series: series.name })
                  : t("web.detail.books.inThisSeries")
              }
              coverAspect="poster"
              items={seriesEntries.map((entry) => ({
                ...relatedRailItem(t, entry),
                kicker:
                  typeof entry.series_index === "number"
                    ? t("web.detail.books.bookNumber", { number: entry.series_index })
                    : undefined,
                highlight: entry.content_id === item.content_id,
              }))}
            />
          )}

          {alsoByAuthor.length > 0 && (
            <RelatedRail
              key={`author:${item.content_id}`}
              heading={
                authors[0]
                  ? t("web.detail.books.alsoByAuthor", { author: authors[0] })
                  : t("web.detail.books.alsoByThisAuthor")
              }
              coverAspect="poster"
              items={alsoByAuthor.map((entry) => relatedRailItem(t, entry))}
            />
          )}

          {similar.length > 0 && (
            <RelatedRail
              key={`similar:${item.content_id}`}
              heading={t("web.detail.books.youMightAlsoLike")}
              coverAspect="poster"
              items={similar.map((entry) => relatedRailItem(t, entry))}
            />
          )}

          <MediaLocations
            title={t("web.detail.sections.files")}
            versions={item.versions}
            emptyMessage={item.versions.length === 0 ? t("web.detail.ebook.noFiles") : undefined}
            summaryBuilder={ebookVersionSummary}
          />
        </div>
      </div>

      <DownloadVersionPicker
        open={downloadOpen}
        onOpenChange={setDownloadOpen}
        versions={item.versions}
        title={item.title}
        summaryBuilder={ebookVersionSummary}
      />
    </div>
  );
}
