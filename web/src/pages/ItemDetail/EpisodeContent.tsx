import { useCallback, useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import { leafUserData, type FileVersion, type ItemDetail } from "@/api/types";
import type { PlayerSubtitleTrackSignature, PrePlaySubtitleSelection } from "@/player/types";
import { useSeasonDetail, useSeasonEpisodes } from "@/hooks/queries/episodes";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useDeleteSubtitlePreference, useSetSubtitlePreference } from "@/hooks/queries/subtitles";
import { usePrePlayAudioSelection } from "./usePrePlayAudioSelection";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useEpisodeSpoilerPrefs } from "@/hooks/useEpisodeSpoilerPrefs";
import { useAuth } from "@/hooks/useAuth";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useOnViewTranslation } from "@/hooks/useOnViewTranslation";
import {
  useRedetectEpisodeIntro,
  useRedetectItemMarkers,
  useRefreshItemMetadata,
} from "@/hooks/queries/items";
import { useAdminMarkerCapabilities } from "@/hooks/queries/admin/markers";
import { useLibraryCapabilities } from "@/hooks/queries/admin/libraries";
import CastCarousel from "@/components/CastCarousel";
import CrewList from "@/components/CrewList";
import DownloadVersionPicker from "@/components/DownloadVersionPicker";
import EditMetadataDialog from "@/components/EditMetadataDialog";
import MediaLocations from "@/components/MediaLocations";
import PageBack from "@/components/PageBack";
import EpisodeCarousel from "./components/EpisodeCarousel";
import DetailHero from "./DetailHero";
import { useDetailWatchTogether } from "@/pages/watchtogether/DetailWatchTogether";
import MetadataBadges from "./components/MetadataBadges";
import QualityBadges from "./components/QualityBadges";
import ScoreRow from "./components/ScoreRow";
import HeroCrewLine from "./components/HeroCrewLine";
import WatchedActionBar from "./components/WatchedActionBar";
import DetailBreadcrumb from "./components/DetailBreadcrumb";
import MediaInfoDialog from "./components/MediaInfoDialog";
import SubtitleSearchDialog from "./components/SubtitleSearchDialog";
import { sortByResolution } from "./components/VersionFlyout";
import {
  selectDefaultPlaybackVariantVersion,
  selectedDownloadVersions,
} from "./components/versionRankingUtils";
import { resolveSelectedMediaSummary } from "./components/selectedMediaSummary";
import { EpisodeCarouselSkeleton } from "./components/SectionSkeletons";
import {
  getSeasonDisplayTitle,
  resolveLeafPrimaryAction,
  resolveEpisodeSiblingSeason,
  type EpisodeNavigationState,
} from "./itemDetailLayout";
import {
  canCurateMetadata as canCurateMetadataForUser,
  canEditMarkers as canEditMarkersForUser,
} from "@/lib/permissions";
import { isEpisodeUnwatched } from "@/lib/episodeSpoilers";
import { buildItemHref } from "@/lib/mediaNavigation";
import { formatDate } from "@/lib/datetime";
import { formatRuntimeMinutes } from "@/lib/mediaFormat";
import { useQualityPreferenceState } from "@/hooks/queries/qualityPreference";
import { hasMachineTranslation } from "./components/machineTranslation";
import { useT } from "@/i18n";

export default function EpisodeContent({
  item,
  libraryId,
}: {
  item: ItemDetail & { type: "episode" };
  /**
   * The page's ?libraryId= scope. Series, season and episode links keep it;
   * Play does not, because playback always sees every version. The series
   * overview fallback resolves its language in it.
   */
  libraryId?: number;
}) {
  const t = useT();
  const navigate = useNavigate();
  const location = useLocation();
  useAmbientColor(item.backdrop_thumbhash);
  const { user } = useAuth();
  const isAdmin = useIsActingAdmin();
  const { profile: currentProfile } = useCurrentProfile();
  // The resolution cap comes from the settings contract, where the quality
  // picker writes; the profile column it falls back to is only the pre-cutover
  // choice, since that picker no longer mirrors into it.
  const { preference: qualityPreference, settled: qualitySettled } = useQualityPreferenceState(
    currentProfile?.quality_preference,
  );
  const canCurateMetadata = canCurateMetadataForUser(user, currentProfile);
  const canEditMarkers = canEditMarkersForUser(user, currentProfile);
  const [editOpen, setEditOpen] = useState(false);
  const [downloadOpen, setDownloadOpen] = useState(false);
  const [subtitleSearchOpen, setSubtitleSearchOpen] = useState(false);
  const [mediaInfoOpen, setMediaInfoOpen] = useState(false);
  const [mediaInfoFileId, setMediaInfoFileId] = useState<number | null>(null);
  const refreshMetadataMutation = useRefreshItemMetadata();
  const redetectMarkersMutation = useRedetectItemMarkers({ introFallback: true });
  const redetectIntroMutation = useRedetectEpisodeIntro();
  // An API node without redetect-markers (rolling deploy, rollback) keeps the
  // older intro-only re-detection.
  const markerCapabilities = useAdminMarkerCapabilities(isAdmin);
  const capabilities = useLibraryCapabilities(isAdmin).data;
  const canManageTrickplay =
    capabilities?.trickplay === true && capabilities.trickplay_supported === true;
  const canRedetectMarkers = markerCapabilities.data?.redetect_markers === true;
  const deleteSubtitlePreference = useDeleteSubtitlePreference();
  const setSubtitlePreference = useSetSubtitlePreference();

  // Version selection state — drives the Play button and inline stream popovers.
  const sortedVersions = useMemo(() => sortByResolution(item.versions ?? []), [item.versions]);
  const userData = leafUserData(item.user_data);
  const { hideOverviews } = useEpisodeSpoilerPrefs();
  // Keyed by episode so a reveal survives an on-view translation of the same
  // episode but not navigation to the next one.
  const [revealedOverviewId, setRevealedOverviewId] = useState<string | null>(null);
  // An episode without a description shows its series' overview, in the
  // language the series page shows in this library, and on-view translation
  // follows it. It reveals nothing about the episode, so spoiler protection leaves it
  // visible.
  const hasOwnOverview = !!item.overview?.trim();
  const { data: series } = useCatalogItemDetail(
    hasOwnOverview ? undefined : item.series_id,
    libraryId,
  );
  const overviewSource = hasOwnOverview ? item : series;
  const { translating: overviewTranslating, onTranslate: onTranslateOverview } =
    useOnViewTranslation(overviewSource);
  const overviewHidden =
    hasOwnOverview &&
    hideOverviews &&
    isEpisodeUnwatched(item.user_data) &&
    revealedOverviewId !== item.content_id;
  const [selectedEditionKey, setSelectedEditionKey] = useState<string | undefined>(undefined);
  const defaultSelectedVersion = useMemo(
    () =>
      selectDefaultPlaybackVariantVersion(
        sortedVersions,
        item.playback_variants,
        userData,
        qualityPreference,
        selectedEditionKey,
      ),
    [qualityPreference, selectedEditionKey, item.playback_variants, sortedVersions, userData],
  );
  const [manualSelectedFileId, setManualSelectedFileId] = useState<number | null>(null);
  const pickedVersion = useMemo(
    () =>
      manualSelectedFileId != null
        ? (sortedVersions.find((version) => version.file_id === manualSelectedFileId) ?? null)
        : null,
    [manualSelectedFileId, sortedVersions],
  );
  const selectedVersion = pickedVersion ?? defaultSelectedVersion;
  const selectedMediaSummary = useMemo(
    () => resolveSelectedMediaSummary(selectedVersion, item.playback_variants, item.runtime ?? 0),
    [item.playback_variants, item.runtime, selectedVersion],
  );
  const openMediaInfo = useCallback(
    (fileId?: number) => {
      setMediaInfoFileId(fileId ?? selectedVersion?.file_id ?? null);
      setMediaInfoOpen(true);
    },
    [selectedVersion?.file_id],
  );
  const audioSelection = usePrePlayAudioSelection(item.content_id, item.series_id, selectedVersion);
  const clearAudioSelection = audioSelection.clear;
  const [subtitleSelectionMode, setSubtitleSelectionMode] = useState<"auto" | "off" | "explicit">(
    "auto",
  );
  const [explicitSubtitleSelection, setExplicitSubtitleSelection] =
    useState<PrePlaySubtitleSelection | null>(null);

  // Reset selection when navigating to a different episode (adjust state during render).
  const [prevContentId, setPrevContentId] = useState(item.content_id);
  const [prevSelectedFileId, setPrevSelectedFileId] = useState(selectedVersion?.file_id);
  if (prevContentId !== item.content_id) {
    setPrevContentId(item.content_id);
    setManualSelectedFileId(null);
    setSelectedEditionKey(undefined);
    setSubtitleSelectionMode("auto");
    setExplicitSubtitleSelection(null);
  } else if (selectedEditionKey === undefined && qualitySettled && defaultSelectedVersion) {
    setSelectedEditionKey(defaultSelectedVersion.edition_key ?? "");
  }
  if (prevSelectedFileId !== selectedVersion?.file_id) {
    setPrevSelectedFileId(selectedVersion?.file_id);
    setSubtitleSelectionMode("auto");
    setExplicitSubtitleSelection(null);
  }

  const handleSelectVersion = useCallback(
    (version: FileVersion | null) => {
      setManualSelectedFileId(version?.file_id ?? null);
      if (version) setSelectedEditionKey(version.edition_key ?? "");
      // Track indexes belong to a file, so another version starts from Auto.
      clearAudioSelection();
      setSubtitleSelectionMode("auto");
      setExplicitSubtitleSelection(null);
    },
    [clearAudioSelection],
  );

  const handleSelectEdition = useCallback(
    (key: string) => {
      setSelectedEditionKey(key);
      handleSelectVersion(null);
    },
    [handleSelectVersion],
  );
  const downloadVersions = useMemo(
    () => selectedDownloadVersions(selectedVersion, item.playback_variants, qualityPreference),
    [selectedVersion, item.playback_variants, qualityPreference],
  );

  const handleSelectSubtitle = (selection: PrePlaySubtitleSelection) => {
    setSubtitleSelectionMode("explicit");
    setExplicitSubtitleSelection(selection);
    // Persist as the series' override so the choice sticks across visits,
    // exactly like a manual in-player selection (preferences are
    // series-scoped).
    if (item.series_id) {
      setSubtitlePreference.mutate({
        prefId: item.series_id,
        selection,
      });
    }
  };

  const handleResetSubtitleSelection = () => {
    setSubtitleSelectionMode("auto");
    setExplicitSubtitleSelection(null);
    // "Auto" also clears the persisted override saved by a manual in-player
    // selection. Subtitle preferences are series-scoped, so this restores
    // profile-level auto selection for the whole series.
    if (item.series_id) {
      deleteSubtitlePreference.mutate(item.series_id);
    }
  };

  const handleSelectSubtitleOff = () => {
    setSubtitleSelectionMode("off");
    setExplicitSubtitleSelection(null);
    if (item.series_id) {
      setSubtitlePreference.mutate({
        prefId: item.series_id,
        selection: null,
      });
    }
  };
  const preferredSubtitleTrackSignature: PlayerSubtitleTrackSignature | null =
    item.effective_subtitle_track_signature
      ? {
          source:
            item.effective_subtitle_track_signature.source === "downloaded"
              ? "downloaded"
              : item.effective_subtitle_track_signature.source === "external"
                ? "external"
                : item.effective_subtitle_track_signature.source === "embedded"
                  ? "embedded"
                  : undefined,
          language: item.effective_subtitle_track_signature.language,
          codec: item.effective_subtitle_track_signature.codec,
          label: item.effective_subtitle_track_signature.label,
          forced: item.effective_subtitle_track_signature.forced,
          hearing_impaired: item.effective_subtitle_track_signature.hearing_impaired,
        }
      : null;
  const navigationState = location.state as EpisodeNavigationState | null;
  const primaryAction = resolveLeafPrimaryAction(item, t("web.detail.actions.playEpisode"));
  const watchTogether = useDetailWatchTogether({
    item,
    target:
      item.versions && item.versions.length > 0
        ? {
            content_id: item.content_id,
            title: item.title,
            subtitle:
              item.season_number != null && item.episode_number != null
                ? `S${item.season_number} E${item.episode_number}`
                : undefined,
            poster_url: item.poster_url,
            poster_thumbhash: item.poster_thumbhash,
          }
        : null,
    seriesId: item.series_id,
    initialSeasonNumber: item.season_number ?? undefined,
  });
  const restartHref =
    primaryAction.resuming && (item.versions?.length ?? 0) > 0
      ? `/watch/${item.content_id}?restart=1`
      : undefined;

  const title = item.title ?? "";
  const seriesTitle = item.series_title ?? t("web.detail.hero.typeSeries");
  const seriesId = item.series_id;
  const seasonNum = item.season_number;
  const episodeNum = item.episode_number;
  const siblingSeason = resolveEpisodeSiblingSeason(item);
  const { data: currentSeason } = useSeasonDetail(
    siblingSeason?.seriesId,
    siblingSeason?.seasonNumber ?? -1,
    libraryId,
  );

  // Sibling episodes now come from the season collection, not the current episode ID.
  const { data: episodesData, isLoading: siblingsLoading } = useSeasonEpisodes(
    siblingSeason?.seriesId,
    siblingSeason?.seasonNumber ?? -1,
    libraryId,
  );
  const siblingEpisodes = episodesData?.episodes ?? [];
  const seasonLabel =
    navigationState?.parentSeasonLabel ??
    (currentSeason
      ? getSeasonDisplayTitle(currentSeason)
      : seasonNum === 0
        ? t("web.detail.seasons.specials")
        : seasonNum != null
          ? t("web.detail.seasons.numbered", { season: seasonNum })
          : t("web.detail.seasons.seasonFallback"));
  const seasonHref =
    navigationState?.parentSeasonHref ??
    (currentSeason ? buildItemHref({ contentId: currentSeason.content_id, libraryId }) : undefined);
  const episodeLinkState =
    seasonHref || seasonLabel
      ? {
          parentSeasonHref: seasonHref,
          parentSeasonLabel: seasonLabel,
        }
      : undefined;

  // Build breadcrumb segments
  const breadcrumbSegments = [
    {
      label: seriesTitle,
      href: seriesId ? buildItemHref({ contentId: seriesId, libraryId }) : "/",
    },
  ] as Array<{ label: string; href?: string }>;
  if (seasonNum != null) {
    breadcrumbSegments.push({ label: seasonLabel, href: seasonHref });
  }
  if (episodeNum != null) {
    breadcrumbSegments.push({
      label: t("web.detail.episodes.numbered", { episode: episodeNum }),
    });
  }

  return (
    <div>
      <div className="episode-detail-viewport">
        <DetailHero
          variant="episode"
          title={title}
          topNav={<PageBack />}
          context={<DetailBreadcrumb segments={breadcrumbSegments} />}
          backdropUrl={item.backdrop_url}
          backdropThumbhash={item.backdrop_thumbhash}
          hidePoster
          logoUrl={item.logo_url}
          metadata={
            <div className="flex flex-wrap items-center gap-2">
              <MetadataBadges
                duration={formatRuntimeMinutes(selectedMediaSummary.durationMinutes) || undefined}
              />
              {item.air_date && (
                <span className="metadata-badge">
                  {formatDate(item.air_date, "medium") || item.air_date}
                </span>
              )}
              <QualityBadges summary={selectedMediaSummary} />
            </div>
          }
          scoreRow={<ScoreRow ratings={item.ratings} />}
          overview={overviewSource?.overview}
          overviewTranslating={overviewTranslating}
          onTranslateOverview={onTranslateOverview}
          overviewMachineTranslated={hasMachineTranslation(
            overviewSource?.machine_translated_fields,
          )}
          overviewHidden={overviewHidden}
          onRevealOverview={() => setRevealedOverviewId(item.content_id)}
          crewLine={<HeroCrewLine crew={item.crew ?? []} />}
          actions={
            <WatchedActionBar
              compactMobile
              item={item}
              contentId={item.content_id}
              canAddToCollection={false}
              watchTogether={watchTogether.menu}
              playHref={
                item.versions && item.versions.length > 0 ? `/watch/${item.content_id}` : undefined
              }
              playLabel={primaryAction.label}
              playProgress={primaryAction.progress}
              restartHref={restartHref}
              resumePositionSeconds={
                item.user_data && "position_seconds" in item.user_data
                  ? item.user_data.position_seconds
                  : undefined
              }
              resumeDurationSeconds={
                item.user_data && "duration_seconds" in item.user_data
                  ? item.user_data.duration_seconds
                  : undefined
              }
              resumeResolution={
                item.user_data && "last_resolution" in item.user_data
                  ? item.user_data.last_resolution
                  : undefined
              }
              resumeHdr={
                item.user_data && "last_hdr" in item.user_data ? item.user_data.last_hdr : undefined
              }
              effectiveVersionResolution={item.effective_version_resolution}
              effectiveVersionHdr={item.effective_version_hdr}
              onRefresh={
                canCurateMetadata
                  ? (mode) =>
                      refreshMetadataMutation.mutate({
                        item,
                        mode,
                        onReplaced: (contentID) =>
                          navigate(buildItemHref({ contentId: contentID, libraryId }), {
                            replace: true,
                          }),
                      })
                  : undefined
              }
              isRefreshing={refreshMetadataMutation.isPending}
              onRedetectMarkers={
                !isAdmin
                  ? undefined
                  : canRedetectMarkers
                    ? (kind) => redetectMarkersMutation.mutate({ itemId: item.content_id, kind })
                    : () => redetectIntroMutation.mutate(item.content_id)
              }
              redetectKind={canRedetectMarkers ? undefined : "intro"}
              isRedetectingMarkers={
                redetectMarkersMutation.isPending || redetectIntroMutation.isPending
              }
              isAdmin={isAdmin}
              canCurateMetadata={canCurateMetadata}
              canEditMarkers={canEditMarkers}
              canManageTrickplay={canManageTrickplay}
              onEditMetadata={canCurateMetadata ? () => setEditOpen(true) : undefined}
              onShowMediaInfo={(item.versions?.length ?? 0) > 0 ? () => openMediaInfo() : undefined}
              versions={item.versions ?? []}
              playbackVariants={item.playback_variants}
              selectedVersion={selectedVersion}
              versionPicked={pickedVersion != null}
              onSelectVersion={handleSelectVersion}
              onSelectEdition={handleSelectEdition}
              onDownload={
                user?.download_allowed && (item.versions ?? []).length > 0
                  ? () => setDownloadOpen(true)
                  : undefined
              }
              onSearchSubtitles={
                (item.versions?.length ?? 0) > 0 ? () => setSubtitleSearchOpen(true) : undefined
              }
              qualityPreference={qualityPreference}
              audioSelectionMode={audioSelection.mode}
              explicitAudioTrackIndex={audioSelection.explicitIndex}
              onSelectAudioTrack={audioSelection.select}
              onResetAudioSelection={audioSelection.reset}
              prePlaySubtitleMode={subtitleSelectionMode}
              explicitSubtitleSelection={explicitSubtitleSelection}
              onSelectSubtitle={handleSelectSubtitle}
              onSelectSubtitleOff={handleSelectSubtitleOff}
              onResetSubtitleSelection={handleResetSubtitleSelection}
              preferredSubtitleLanguage={item.effective_subtitle_language}
              preferredSubtitleTrackSignature={preferredSubtitleTrackSignature}
              subtitleMode={item.effective_subtitle_mode as "off" | "auto" | "always" | undefined}
              showForcedSubtitles={item.effective_show_forced_subtitles}
            />
          }
        />

        {(siblingsLoading || siblingEpisodes.length > 1) && (
          <section
            className="page-shell episode-detail-navigation"
            aria-label={t("web.detail.episodes.moreEpisodesRegion")}
          >
            {/* More Episodes carousel — most useful, so show first */}
            {siblingsLoading ? (
              <EpisodeCarouselSkeleton />
            ) : (
              <div>
                <h2 className="mb-5 text-xl font-semibold tracking-tight">
                  {t("web.detail.episodes.moreEpisodes")}
                </h2>
                <EpisodeCarousel
                  episodes={siblingEpisodes}
                  currentEpisodeNumber={episodeNum ?? -1}
                  episodeLinkState={episodeLinkState}
                  libraryId={libraryId}
                />
              </div>
            )}
          </section>
        )}
      </div>

      <div className="page-shell detail-supporting-content space-y-12 py-10 sm:space-y-14">
        {canCurateMetadata && (
          <MediaLocations
            title={t("web.detail.sections.mediaLocations")}
            versions={item.versions}
            onShowMediaInfo={openMediaInfo}
          />
        )}

        {item.cast && item.cast.length > 0 && (
          <div>
            <h2 className="mb-5 text-xl font-semibold tracking-tight">
              {t("web.detail.sections.cast")}
            </h2>
            <CastCarousel cast={item.cast} prefetchPeople />
          </div>
        )}

        {item.crew && item.crew.length > 0 && <CrewList crew={item.crew} />}
      </div>
      {canCurateMetadata && (
        <EditMetadataDialog item={item} open={editOpen} onOpenChange={setEditOpen} />
      )}
      <DownloadVersionPicker
        open={downloadOpen}
        onOpenChange={setDownloadOpen}
        versions={downloadVersions}
        title={title}
      />
      <SubtitleSearchDialog
        open={subtitleSearchOpen}
        onOpenChange={setSubtitleSearchOpen}
        version={selectedVersion}
        title={title}
      />
      <MediaInfoDialog
        open={mediaInfoOpen}
        onOpenChange={setMediaInfoOpen}
        versions={item.versions ?? []}
        title={title}
        initialFileId={mediaInfoFileId}
      />
      {watchTogether.sheet}
    </div>
  );
}
