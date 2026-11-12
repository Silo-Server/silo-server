import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router";
import type { ItemDetail, LeafItemUserData } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { FolderPlus, MoreVertical, Pause, Play } from "lucide-react";
import AddToCollectionDialog from "@/components/AddToCollectionDialog";
import EditMetadataDialog from "@/components/EditMetadataDialog";
import MatchItemDialog from "@/components/MatchItemDialog";
import { MediaActionIcon } from "@/components/mediaActionIcons";
import { useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { canCurateMetadata as canCurateMetadataForUser } from "@/lib/permissions";
import { ChaptersSection } from "@/pages/audiobooks/components/ChaptersSection";
import { NarratorCard } from "@/pages/audiobooks/components/NarratorCard";
import { NarratorPicker } from "@/pages/audiobooks/components/NarratorPicker";
import { RelatedRail } from "@/pages/audiobooks/components/RelatedRail";
import DetailHero from "@/pages/ItemDetail/DetailHero";
import PageBack from "@/components/PageBack";
import MetadataBadges from "@/pages/ItemDetail/components/MetadataBadges";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { buildChapterList, findChapterAt, totalAudiobookDuration } from "@/lib/audiobooks/chapters";
import { audiobookFilesFromVersions } from "@/lib/audiobooks/files";
import { useAudiobookPlaybackController } from "@/pages/audiobooks/player/audiobookPlaybackContext";
import { coldResumePosition } from "@/pages/audiobooks/player/smartRewind";
import { getAudiobookSmartRewind } from "@/pages/audiobooks/player/useAudiobookPrefs";
import { hasMachineTranslation } from "./components/machineTranslation";
import { numberText, useAppLanguage, useT, type Translate } from "@/i18n";

function formatSeconds(t: Translate, totalSeconds: number): string {
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
  const t = useT();
  const [searchParams] = useSearchParams();
  const handledPlayParamRef = useRef(false);
  const [addToCollectionOpen, setAddToCollectionOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [matchOpen, setMatchOpen] = useState(false);
  const audiobookPlayback = useAudiobookPlaybackController();
  const navigate = useNavigate();
  const { search } = useLocation();
  const { user } = useAuth();
  const { profile: currentProfile } = useCurrentProfile();
  const canCurateMetadata = canCurateMetadataForUser(user, currentProfile);
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

  const openPlayer = useCallback(
    (atSeconds: number) => {
      audiobookPlayback?.startPlayback({
        contentId: item.content_id,
        title: item.title,
        author,
        narrator,
        posterUrl: item.poster_url,
        files,
        initialPositionSeconds: atSeconds,
      });
    },
    [audiobookPlayback, author, files, item.content_id, item.poster_url, item.title, narrator],
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

  return (
    <div>
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
            onReplaced={(contentID) => {
              // Keep the page context but not a request to start playback.
              const params = new URLSearchParams(search);
              params.delete("play");
              params.delete("restart");
              const query = params.toString();
              navigate(
                { pathname: `/item/${contentID}`, search: query ? `?${query}` : "" },
                { replace: true },
              );
            }}
          />
        </>
      )}

      <DetailHero
        overviewMachineTranslated={hasMachineTranslation(item.machine_translated_fields)}
        title={item.title}
        topNav={<PageBack />}
        context={t("web.detail.hero.typeAudiobook")}
        studioLabel={item.audiobook?.publisher || item.studios?.[0] || undefined}
        posterUrl={item.poster_url}
        posterThumbhash={item.poster_thumbhash}
        posterOrientation="square"
        subtitle={
          (author || narrator) && (
            <div className="text-muted-foreground flex flex-col gap-0.5 text-sm">
              {author && (
                <span>
                  {t("web.detail.books.byAuthor", {
                    label: (chunks) => <span className="font-medium">{chunks}</span>,
                    name: (chunks) => chunks,
                    author,
                  })}
                </span>
              )}
              {narrator && (
                <span>
                  {t("web.detail.books.narratedBy", {
                    label: (chunks) => <span className="font-medium">{chunks}</span>,
                    name: (chunks) =>
                      (item.audiobook?.other_narrations ?? []).length > 0 ? (
                        <NarratorPicker
                          currentNarrator={narrator}
                          currentContentId={item.content_id}
                          others={item.audiobook?.other_narrations ?? []}
                        />
                      ) : (
                        chunks
                      ),
                    narrator,
                  })}
                </span>
              )}
            </div>
          )
        }
        metadata={
          <MetadataBadges
            year={item.year > 0 ? String(item.year) : undefined}
            duration={durationTotal > 0 ? formatSeconds(t, durationTotal) : undefined}
          />
        }
        overview={item.overview}
        genres={item.genres}
        genreHref={(genre) => genreHref(genre, libraryId)}
        actions={
          (files.length > 0 || canCurateMetadata) && (
            <div className="flex max-w-md flex-col gap-3">
              {files.length > 0 && hasProgress && durationTotal > 0 && (
                <div>
                  <div className="bg-muted h-1.5 w-full overflow-hidden rounded-full">
                    <div
                      className="bg-primary h-full rounded-full transition-all"
                      style={{ width: `${Math.min(100, (resumeSeconds / durationTotal) * 100)}%` }}
                    />
                  </div>
                  <p className="text-muted-foreground mt-1 text-xs">
                    {t("web.detail.audiobook.listenedProgress", {
                      time: formatSeconds(t, resumeSeconds),
                      percent: Math.round((resumeSeconds / durationTotal) * 100),
                    })}
                  </p>
                </div>
              )}
              <div className="flex flex-wrap items-center gap-3">
                {files.length > 0 && (
                  <>
                    <Button onClick={handlePlayResume} size="lg" className="gap-2">
                      {activePlayback?.playing ? (
                        <Pause className="h-4 w-4 fill-current" />
                      ) : (
                        <Play className="h-4 w-4 fill-current" />
                      )}
                      {primaryPlaybackLabel()}
                    </Button>
                    <Button
                      variant="outline"
                      size="lg"
                      onClick={() => setAddToCollectionOpen(true)}
                      className="gap-2"
                      title={t("web.detail.audiobook.addToCollectionHint")}
                    >
                      <FolderPlus className="h-4 w-4" />
                      {t("web.detail.actions.addToCollection")}
                    </Button>
                    {hasProgress && (
                      <Button variant="outline" size="lg" onClick={() => openPlayer(0)}>
                        {t("web.detail.audiobook.listenFromStart")}
                      </Button>
                    )}
                  </>
                )}
                {canCurateMetadata && (
                  <DropdownMenu modal={false}>
                    <DropdownMenuTrigger asChild>
                      <Button
                        variant="outline"
                        size="icon-lg"
                        title={t("web.detail.actions.more")}
                        aria-label={t("web.detail.actions.moreActions")}
                      >
                        <MoreVertical className="h-4 w-4" />
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
                    </DropdownMenuContent>
                  </DropdownMenu>
                )}
              </div>
            </div>
          )
        }
      />

      <div
        className="page-shell detail-supporting-content space-y-12 py-10 sm:space-y-14"
        style={isPlayerOpen ? { paddingBottom: "8rem" } : undefined}
      >
        {narrator && <NarratorCard narrator={narrator} />}

        {item.audiobook?.series && item.audiobook.series.entries.length > 0 && (
          <RelatedRail
            heading={
              item.audiobook.series.name
                ? t("web.detail.books.inSeries", { series: item.audiobook.series.name })
                : t("web.detail.books.inThisSeries")
            }
            items={item.audiobook.series.entries.map((entry) => ({
              content_id: entry.content_id,
              title: entry.title,
              poster_url: entry.poster_url,
              subtitle:
                typeof entry.series_index === "number"
                  ? t("web.detail.books.bookNumber", { number: entry.series_index })
                  : undefined,
              highlight: entry.content_id === item.content_id,
            }))}
          />
        )}

        {(item.audiobook?.related.also_by_author ?? []).length > 0 && (
          <RelatedRail
            heading={
              author
                ? t("web.detail.books.alsoByAuthor", { author })
                : t("web.detail.books.alsoByThisAuthor")
            }
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
            heading={t("web.detail.books.youMightAlsoLike")}
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
          currentPositionSeconds={currentPlayerSeconds ?? (resumeSeconds || null)}
          onSelect={(seconds) => openPlayer(seconds)}
        />
      </div>
    </div>
  );
}
