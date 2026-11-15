import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { joinList } from "@/lib/appLocale";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router";
import type { AudiobookRelatedItem, ItemDetail, LeafItemUserData } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { FolderPlus, MoreHorizontal, Pause, Play, RotateCcw } from "lucide-react";
import AddToCollectionDialog from "@/components/AddToCollectionDialog";
import EditMetadataDialog from "@/components/EditMetadataDialog";
import MatchItemDialog from "@/components/MatchItemDialog";
import MediaCardArtwork from "@/components/MediaCardArtwork";
import RefreshMetadataDialog from "@/components/RefreshMetadataDialog";
import { MediaActionIcon } from "@/components/mediaActionIcons";
import PageBack from "@/components/PageBack";
import { useRefreshItemMetadata } from "@/hooks/queries/items";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { canCurateMetadata as canCurateMetadataForUser } from "@/lib/permissions";
import { ChaptersSection } from "@/pages/audiobooks/components/ChaptersSection";
import { NarratorPicker } from "@/pages/audiobooks/components/NarratorPicker";
import { RelatedRail } from "@/pages/audiobooks/components/RelatedRail";
import DetailOverview from "@/pages/ItemDetail/components/DetailOverview";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { buildChapterList, findChapterAt, totalAudiobookDuration } from "@/lib/audiobooks/chapters";
import { formatHoursMinutes, formatListeningTimeLeft } from "@/lib/audiobooks/duration";
import { audiobookFilesFromVersions } from "@/lib/audiobooks/files";
import { useAudiobookPlaybackController } from "@/pages/audiobooks/player/audiobookPlaybackContext";
import { coldResumePosition } from "@/pages/audiobooks/player/smartRewind";
import { getAudiobookSmartRewind } from "@/pages/audiobooks/player/useAudiobookPrefs";
import { hasMachineTranslation } from "./components/machineTranslation";
import { numberText, useAppLanguage, useT, type Translate } from "@/i18n";

import "@/pages/audiobooks/audiobooks.css";

function formatListened(t: Translate, totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds < 0) return "";
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = Math.floor(totalSeconds % 60);
  if (h > 0) {
    return t("web.detail.duration.hoursMinutes", {
      hours: h,
      minutes: numberText(String(m).padStart(2, "0")),
    });
  }
  if (m > 0) {
    return t("web.detail.mediaInfo.durationMs", {
      minutes: m,
      seconds: numberText(String(s).padStart(2, "0")),
    });
  }
  return t("web.detail.mediaInfo.durationS", { seconds: s });
}

function leafUserData(userData: ItemDetail["user_data"]): LeafItemUserData | undefined {
  return userData && "position_seconds" in userData ? userData : undefined;
}

function namesFromPeople(people: Array<{ name?: string }> | undefined): string {
  return joinList(
    (people ?? []).map((person) => person.name?.trim()).filter((name): name is string => !!name),
  );
}

function namesFromCrew(item: ItemDetail, job: string): string {
  return joinList(
    (item.crew ?? [])
      .filter((credit) => credit.job.toLowerCase() === job)
      .map((credit) => credit.name?.trim())
      .filter((name): name is string => !!name),
  );
}

/** A Library-tab browse filtered to one rule, else the same query in the catalog. */
function filteredBrowseHref(field: string, op: string, value: string, libraryId?: number): string {
  const params = new URLSearchParams();
  if (libraryId) {
    params.set("tab", "library");
  } else {
    params.set("source", "query");
    params.set("type", "audiobook");
  }
  if (field === "genre") {
    params.set("genre", value);
  } else {
    params.set("groups[0][rules][0][field]", field);
    params.set("groups[0][rules][0][op]", op);
    params.set("groups[0][rules][0][value]", value);
  }
  return libraryId ? `/library/${libraryId}?${params.toString()}` : `/catalog?${params.toString()}`;
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="truncate text-[0.9375rem] font-semibold">{children}</dd>
    </div>
  );
}

/**
 * A related book's line under its title: finished, the time left while the
 * profile is partway through, else its length, else its year.
 */
function relatedStatus(t: Translate, entry: AudiobookRelatedItem): string | undefined {
  if (entry.played) return t("web.audiobooks.detail.finished");
  const duration = entry.duration_seconds ?? 0;
  if (entry.progress_ratio != null && duration > 0) {
    return formatListeningTimeLeft(entry.progress_ratio * duration, duration) ?? undefined;
  }
  if (duration > 0) return formatHoursMinutes(duration);
  return entry.year ? numberText(entry.year) : undefined;
}

/** The rail card for a related book. */
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

export default function AudiobookContent({
  item,
  libraryId,
}: {
  item: ItemDetail & { type: "audiobook" };
  libraryId?: number;
}) {
  const t = useT();
  const [searchParams] = useSearchParams();
  const handledPlayParamRef = useRef(false);
  const [addToCollectionOpen, setAddToCollectionOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [matchOpen, setMatchOpen] = useState(false);
  const [refreshOpen, setRefreshOpen] = useState(false);
  const refreshMetadataMutation = useRefreshItemMetadata();
  const audiobookPlayback = useAudiobookPlaybackController();
  const navigate = useNavigate();
  const { search } = useLocation();
  useAmbientColor(item.poster_thumbhash);
  // Follow a match or refresh that gives the book a new content ID. Keep the
  // page context but not a request to start playback.
  const followReplacedItem = (contentID: string) => {
    const params = new URLSearchParams(search);
    params.delete("play");
    params.delete("restart");
    const query = params.toString();
    navigate(
      { pathname: `/item/${contentID}`, search: query ? `?${query}` : "" },
      { replace: true },
    );
  };
  const { user } = useAuth();
  const { profile: currentProfile, hasSelectedProfile } = useCurrentProfile();
  // Until a stored profile resolves, the null profile would read as "no
  // profile selected" and grant an admin's curation rights to any profile.
  const profileIsResolved = !hasSelectedProfile || Boolean(currentProfile);
  const canCurateMetadata = profileIsResolved && canCurateMetadataForUser(user, currentProfile);
  const scopedItem = useMemo(
    () => (libraryId === undefined ? item : { ...item, library_id: libraryId }),
    [item, libraryId],
  );

  const files = useMemo<AudiobookFile[]>(
    () => audiobookFilesFromVersions(item.versions),
    [item.versions],
  );

  const author =
    namesFromPeople(item.audiobook?.authors) || namesFromCrew(item, "author") || undefined;
  const narrator =
    namesFromPeople(item.audiobook?.narrators) || namesFromCrew(item, "narrator") || undefined;
  const progress = leafUserData(item.user_data);
  const resumeSeconds = progress?.position_seconds ?? 0;
  const durationTotal =
    item.audiobook?.total_duration_seconds ||
    progress?.duration_seconds ||
    totalAudiobookDuration(files);
  const hasProgress = Boolean(
    progress &&
    resumeSeconds > 0 &&
    durationTotal > 0 &&
    (progress.is_in_progress ?? !progress.played),
  );
  const language = useAppLanguage();
  const chapters = useMemo(
    () => buildChapterList(files),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- untitled chapters get a translated fallback title
    [files, language],
  );
  const isPlayerOpen = audiobookPlayback?.activeRequest?.contentId === item.content_id;
  const activePlayback = isPlayerOpen ? audiobookPlayback?.active : null;
  const currentPlayerSeconds =
    activePlayback?.currentTime != null ? activePlayback.currentTime : null;
  // The live position while this book plays, else the saved one.
  const shownPosition = currentPlayerSeconds ?? (hasProgress ? resumeSeconds : 0);
  const shownPercent =
    durationTotal > 0 ? Math.min(100, Math.round((shownPosition / durationTotal) * 100)) : 0;

  const openPlayer = useCallback(
    (atSeconds: number) => {
      audiobookPlayback?.startPlayback({
        contentId: item.content_id,
        title: item.title,
        author,
        narrator,
        posterUrl: item.poster_url,
        posterThumbhash: item.poster_thumbhash,
        files,
        initialPositionSeconds: atSeconds,
      });
    },
    [
      audiobookPlayback,
      author,
      files,
      item.content_id,
      item.poster_thumbhash,
      item.poster_url,
      item.title,
      narrator,
    ],
  );

  function handlePlayResume() {
    if (activePlayback) {
      audiobookPlayback?.toggleActivePlayback();
      return;
    }
    openPlayer(hasProgress ? coldResumePosition(resumeSeconds, getAudiobookSmartRewind()) : 0);
  }

  function primaryPlaybackLabel() {
    if (activePlayback?.playing) {
      return t("web.detail.audiobook.pause");
    }
    if (activePlayback) {
      return t("web.detail.actions.resume");
    }
    if (hasProgress) {
      const chapter = findChapterAt(chapters, resumeSeconds);
      return chapter
        ? t("web.detail.audiobook.resumeChapter", { chapter: chapter.label })
        : t("web.detail.actions.resume");
    }
    return t("web.detail.audiobook.listen");
  }

  useEffect(() => {
    if (handledPlayParamRef.current || searchParams.get("play") !== "1" || files.length === 0) {
      return;
    }
    handledPlayParamRef.current = true;
    openPlayer(
      searchParams.get("restart") === "1"
        ? 0
        : hasProgress
          ? coldResumePosition(resumeSeconds, getAudiobookSmartRewind())
          : 0,
    );
  }, [files.length, hasProgress, openPlayer, resumeSeconds, searchParams]);

  const series = item.audiobook?.series;
  const seriesEntries = series?.entries ?? [];
  const thisSeriesEntry = seriesEntries.find((entry) => entry.content_id === item.content_id);
  const seriesIndex = thisSeriesEntry?.series_index;
  const publisher = item.audiobook?.publisher || item.studios?.[0] || undefined;
  const otherNarrations = item.audiobook?.other_narrations ?? [];
  const alsoByAuthor = item.audiobook?.related.also_by_author ?? [];
  const similar = item.audiobook?.related.similar ?? [];

  return (
    <div className="relative isolate">
      <AddToCollectionDialog
        open={addToCollectionOpen}
        onOpenChange={setAddToCollectionOpen}
        mediaItemId={item.content_id}
        itemTitle={item.title}
      />
      {canCurateMetadata && (
        <>
          {/* Mounted per open: the form takes its values from the item when it
              mounts, so a dialog kept open across a match would save stale fields. */}
          {editOpen && <EditMetadataDialog item={item} open onOpenChange={setEditOpen} />}
          <MatchItemDialog
            key={item.content_id}
            item={scopedItem}
            open={matchOpen}
            onOpenChange={setMatchOpen}
            onReplaced={followReplacedItem}
          />
          <RefreshMetadataDialog
            open={refreshOpen}
            onOpenChange={setRefreshOpen}
            onConfirm={(mode) => {
              setRefreshOpen(false);
              refreshMetadataMutation.mutate({ item, mode, onReplaced: followReplacedItem });
            }}
            isPending={refreshMetadataMutation.isPending}
          />
        </>
      )}

      <div aria-hidden="true" className="audiobook-tint" />
      <PageBack />

      <div
        className="page-shell-wide audiobook-detail-grid pt-16 pb-14 sm:pt-20 lg:pt-24"
        style={isPlayerOpen ? { paddingBottom: "8rem" } : undefined}
      >
        <div className="audiobook-detail-side">
          <div className="audiobook-detail-cover overflow-hidden rounded-xl shadow-[0_24px_48px_-20px_rgb(0_0_0/0.85)] lg:rounded-2xl">
            <MediaCardArtwork
              src={item.poster_url}
              alt={item.title}
              mediaType="audiobook"
              thumbhash={item.poster_thumbhash}
              square
              scrim="none"
              priority
            />
          </div>

          {(files.length > 0 || canCurateMetadata) && (
            <div className="audiobook-detail-actions grid grid-cols-[minmax(0,1fr)] gap-3">
              {files.length > 0 && (
                <Button onClick={handlePlayResume} size="lg" className="w-full gap-2 rounded-full">
                  {activePlayback?.playing ? (
                    <Pause className="h-4 w-4 shrink-0 fill-current" />
                  ) : (
                    <Play className="h-4 w-4 shrink-0 fill-current" />
                  )}
                  <span className="truncate">{primaryPlaybackLabel()}</span>
                </Button>
              )}
              {files.length > 0 && shownPosition > 0 && durationTotal > 0 && (
                <div>
                  <div className="bg-foreground/15 h-1.5 w-full overflow-hidden rounded-full">
                    <div
                      className="bg-primary h-full rounded-full transition-[width]"
                      style={{ width: `${shownPercent}%` }}
                    />
                  </div>
                  <div className="mt-1.5 flex items-baseline justify-between gap-3 text-xs">
                    <span className="text-muted-foreground truncate">
                      {t("web.detail.audiobook.listenedProgress", {
                        time: formatListened(t, shownPosition),
                        percent: shownPercent,
                      })}
                    </span>
                    <span className="text-foreground shrink-0 font-semibold">
                      {formatListeningTimeLeft(shownPosition, durationTotal)}
                    </span>
                  </div>
                </div>
              )}
              <div className="flex items-center gap-2">
                {files.length > 0 && hasProgress && (
                  <Button
                    variant="secondary"
                    onClick={() => openPlayer(0)}
                    className="min-w-0 flex-1 gap-2 rounded-full"
                  >
                    <RotateCcw className="h-4 w-4 shrink-0" />
                    <span className="truncate">{t("web.detail.audiobook.listenFromStart")}</span>
                  </Button>
                )}
                {files.length > 0 && (
                  <Button
                    variant="secondary"
                    size="icon"
                    className="shrink-0 rounded-full"
                    onClick={() => setAddToCollectionOpen(true)}
                    title={t("web.detail.audiobook.addToCollectionHint")}
                    aria-label={t("web.detail.actions.addToCollection")}
                  >
                    <FolderPlus className="h-4 w-4" />
                  </Button>
                )}
                {canCurateMetadata && (
                  <DropdownMenu modal={false}>
                    <DropdownMenuTrigger asChild>
                      <Button
                        variant="secondary"
                        size="icon"
                        className="shrink-0 rounded-full"
                        title={t("web.detail.actions.more")}
                        aria-label={t("web.detail.actions.moreActions")}
                      >
                        <MoreHorizontal className="h-4 w-4" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="start" className="w-56">
                      <DropdownMenuItem onSelect={() => setEditOpen(true)}>
                        <MediaActionIcon action="editMetadata" />
                        {t("web.detail.actions.editMetadata")}
                      </DropdownMenuItem>
                      <DropdownMenuItem onSelect={() => setMatchOpen(true)}>
                        <MediaActionIcon action="matchItem" />
                        {t("web.detail.actions.matchItem")}
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        disabled={refreshMetadataMutation.isPending}
                        onSelect={() => setRefreshOpen(true)}
                      >
                        <MediaActionIcon
                          action="refreshMetadata"
                          isPending={refreshMetadataMutation.isPending}
                        />
                        {t("web.detail.actions.refreshMetadata")}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                )}
              </div>
            </div>
          )}
        </div>

        <header className="audiobook-detail-head grid grid-cols-[minmax(0,1fr)] gap-1.5 lg:gap-2 lg:pt-3">
          {series?.name && (
            <Link
              to={filteredBrowseHref("series", "is", series.name, libraryId)}
              className="text-foreground/80 hover:text-foreground w-fit text-sm font-semibold transition-colors"
            >
              {typeof seriesIndex === "number"
                ? t("web.audiobooks.detail.seriesBook", {
                    series: series.name,
                    number: seriesIndex,
                  })
                : series.name}
            </Link>
          )}
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
          {narrator && (
            <p className="text-foreground/80 text-sm sm:text-base">
              {t("web.detail.books.narratedBy", {
                label: (chunks) => <span>{chunks}</span>,
                name: (chunks) =>
                  otherNarrations.length > 0 ? (
                    <NarratorPicker
                      currentNarrator={narrator}
                      currentContentId={item.content_id}
                      others={otherNarrations}
                    />
                  ) : (
                    <span className="text-foreground font-semibold">{chunks}</span>
                  ),
                narrator,
              })}
            </p>
          )}
        </header>

        <div className="audiobook-detail-body grid grid-cols-[minmax(0,1fr)] gap-10 lg:gap-12">
          <div className="grid grid-cols-[minmax(0,1fr)] gap-6">
            <dl className="border-border/60 flex flex-wrap gap-x-9 gap-y-3 border-y py-4">
              {durationTotal > 0 && (
                <Fact label={t("web.audiobooks.detail.length")}>
                  {formatHoursMinutes(durationTotal)}
                </Fact>
              )}
              {chapters.length > 0 && (
                <Fact label={t("web.audiobooks.chapters.heading")}>
                  {numberText(chapters.length)}
                </Fact>
              )}
              {item.year > 0 && (
                <Fact label={t("web.audiobooks.detail.released")}>{numberText(item.year)}</Fact>
              )}
              {publisher && <Fact label={t("web.audiobooks.detail.publisher")}>{publisher}</Fact>}
            </dl>

            {item.overview && (
              <div className="max-w-[68ch]">
                <DetailOverview
                  overview={item.overview}
                  machineTranslated={hasMachineTranslation(item.machine_translated_fields)}
                />
              </div>
            )}

            {item.genres && item.genres.length > 0 && (
              <div className="flex flex-wrap gap-2">
                {item.genres.map((genre) => (
                  <Link
                    key={genre}
                    to={filteredBrowseHref("genre", "contains", genre, libraryId)}
                    className="metadata-badge hover:bg-foreground/10 transition-colors"
                  >
                    {genre}
                  </Link>
                ))}
              </div>
            )}
          </div>

          <ChaptersSection
            files={files}
            currentPositionSeconds={currentPlayerSeconds ?? (hasProgress ? resumeSeconds : null)}
            onSelect={(seconds) => openPlayer(seconds)}
          />

          {series && seriesEntries.length > 0 && (
            <RelatedRail
              heading={
                series.name
                  ? t("web.detail.books.inSeries", { series: series.name })
                  : t("web.detail.books.inThisSeries")
              }
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
              heading={
                author
                  ? t("web.detail.books.alsoByAuthor", { author })
                  : t("web.detail.books.alsoByThisAuthor")
              }
              items={alsoByAuthor.map((entry) => relatedRailItem(t, entry))}
            />
          )}

          {similar.length > 0 && (
            <RelatedRail
              heading={t("web.detail.books.youMightAlsoLike")}
              items={similar.map((entry) => relatedRailItem(t, entry))}
            />
          )}
        </div>
      </div>
    </div>
  );
}
