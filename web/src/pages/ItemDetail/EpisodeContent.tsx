import { useCallback, useEffect, useMemo, useState } from "react";
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
import { selectDefaultPlaybackVariantVersion } from "./components/versionRankingUtils";
import { readRequestedVersion, rememberRequestedFileId } from "@/playback/requestedVersion";
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
import { formatRuntimeMinutes } from "@/lib/mediaFormat";
import { useQualityPreference } from "@/hooks/queries/qualityPreference";
import { applyVersionAvailability, useVersionLiveness } from "@/hooks/queries/versionLiveness";
import { CalendarClock } from "lucide-react";
import {
  formatCalendarDate,
  isUpcomingEpisode,
  useEpisodeReleaseCapability,
} from "@/hooks/queries/episodeRelease";
import { hasMachineTranslation } from "./components/machineTranslation";

export default function EpisodeContent({
  item,
  libraryId,
  onStaleVersion,
  onSelectedFileIdChange,
}: {
  item: ItemDetail & { type: "episode" };
  /**
   * The page's ?libraryId= scope. Series, season and episode links keep it;
   * Play does not, because playback always sees every version. The series
   * overview fallback resolves its language in it.
   */
  libraryId?: number;
  onStaleVersion?: (fileId: number) => void;
  /**
   * Reports the viewer's explicit version pick (the in-place choice or the
   * remembered one on this device) so the page's detail read can carry it. The
   * server-derived default is not reported: it would only duplicate the read
   * the page already made.
   */
  onSelectedFileIdChange?: (fileId: number | null) => void;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const { user } = useAuth();
  const isAdmin = useIsActingAdmin();
  const { profile: currentProfile } = useCurrentProfile();
  // The resolution cap comes from the settings contract, where the quality
  // picker writes; the profile column it falls back to is only the pre-cutover
  // choice, since that picker no longer mirrors into it.
  const qualityPreference = useQualityPreference(currentProfile?.quality_preference);
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
  // The viewer's own pick, remembered on this device. It outlives the server's
  // `last_file_id`, which names whichever row actually served the bytes after
  // a substitution. Read on mount, but only once the profile has resolved: the
  // store is profile-scoped, and a pre-resolution read would alias another
  // profile's pick. A pick made in place is held in state below.
  const requestedProfileId = currentProfile?.id ?? null;
  const requestedVersion = useMemo(
    () => (requestedProfileId ? readRequestedVersion(item.content_id, requestedProfileId) : null),
    [item.content_id, requestedProfileId],
  );
  const requestedFileId = requestedVersion?.fileId ?? null;
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
  const defaultSelectedVersion = useMemo(
    () =>
      selectDefaultPlaybackVariantVersion(
        sortedVersions,
        item.playback_variants,
        userData,
        qualityPreference,
        item.effective_version_edition_key,
        requestedFileId,
      ),
    [
      qualityPreference,
      item.effective_version_edition_key,
      item.playback_variants,
      requestedFileId,
      sortedVersions,
      userData,
    ],
  );
  const [manualSelectedFileId, setManualSelectedFileId] = useState<number | null>(null);
  // Tracks whether a version picker has been opened; the picker-open path
  // widens the liveness probe from the default version to the whole list.
  const [versionPickersOpened, setVersionPickersOpened] = useState(false);
  const handleVersionPickerOpenChange = useCallback((open: boolean) => {
    if (open) {
      setVersionPickersOpened(true);
    }
  }, []);

  // Check the default-selected version as soon as the page mounts so it can't
  // sit as an unavailable row before any liveness result exists. Only that one
  // version is probed until a picker opens, at which point the full chunked
  // check takes over (results are cached for 5 minutes, so re-opening does not
  // re-fetch). The stamped rows are what the ActionBar derives its
  // force-relink decision from: reading a stale `available` flag would replay
  // a provider pin the server already knows is gone.
  const livenessVersions = useMemo(
    () =>
      versionPickersOpened ? item.versions : defaultSelectedVersion ? [defaultSelectedVersion] : [],
    [defaultSelectedVersion, item.versions, versionPickersOpened],
  );
  const versionLiveness = useVersionLiveness(livenessVersions, true);
  const versionsWithLiveness = useMemo(
    () => applyVersionAvailability(sortedVersions, versionLiveness),
    [sortedVersions, versionLiveness],
  );

  // A chosen row that an enrichment refresh no longer carries is a pick-again
  // state, not permission to silently show and play another version. The
  // device's requested row is the viewer's own pick, ahead of the server's
  // last-file hint.
  const persistedSelectedFileId =
    manualSelectedFileId ?? requestedFileId ?? userData?.last_file_id ?? null;
  const selectedVersionMissing =
    persistedSelectedFileId != null &&
    !versionsWithLiveness.some((version) => version.file_id === persistedSelectedFileId);
  const selectedVersion = useMemo(() => {
    if (selectedVersionMissing) return null;
    if (manualSelectedFileId != null) {
      return (
        versionsWithLiveness.find((version) => version.file_id === manualSelectedFileId) ?? null
      );
    }
    return defaultSelectedVersion
      ? (versionsWithLiveness.find(
          (version) => version.file_id === defaultSelectedVersion.file_id,
        ) ?? defaultSelectedVersion)
      : null;
  }, [defaultSelectedVersion, manualSelectedFileId, selectedVersionMissing, versionsWithLiveness]);
  // A pick-again row (one the refresh dropped) must not keep naming itself to
  // the detail read; only a row the catalog still carries is a selection.
  const explicitSelectedFileId = selectedVersionMissing
    ? null
    : (manualSelectedFileId ?? requestedFileId ?? null);
  useEffect(() => {
    onSelectedFileIdChange?.(explicitSelectedFileId);
  }, [explicitSelectedFileId, onSelectedFileIdChange]);
  // Play must tell the server whether this row is the viewer's or the page's.
  // A pick made in place says so directly; a remembered one only while it is
  // still the row the catalog gives us to play, and only if the viewer named it
  // themselves. An Auto start's row is remembered as a hint, not a pin.
  const explicitFileSelection =
    !selectedVersionMissing &&
    (manualSelectedFileId != null ||
      (requestedVersion?.explicit === true &&
        requestedVersion.fileId === selectedVersion?.file_id));
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
  if (prevContentId !== item.content_id) {
    setPrevContentId(item.content_id);
    setManualSelectedFileId(null);
    setSubtitleSelectionMode("auto");
    setExplicitSubtitleSelection(null);
  }

  const handleSelectVersion = useCallback(
    (version: FileVersion) => {
      setManualSelectedFileId(version.file_id);
      // Remember the pick across reloads: the server records the row that
      // actually plays, which may be a substitute for this one. The standing
      // travels with it so a later Play still pins this row after a reload.
      rememberRequestedFileId(item.content_id, version.file_id, requestedProfileId, {
        explicit: true,
      });
      // Track indexes belong to a file, so another version starts from Auto.
      clearAudioSelection();
      setSubtitleSelectionMode("auto");
      setExplicitSubtitleSelection(null);
    },
    [item.content_id, requestedProfileId, clearAudioSelection],
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
  // Release timing is presentation only: it never gates playback. A
  // future-dated episode with a playable file still plays (the server's
  // release gate remains the sole enforcer), so Play eligibility is
  // unchanged; upcoming episodes only get a hero badge. The badge itself is
  // capability-gated so older servers render as before. Unknown dates fail
  // open (no badge, playable as before).
  const { data: releaseCapability } = useEpisodeReleaseCapability();
  const upcoming = isUpcomingEpisode(item, releaseCapability?.available ?? false);
  const isPlayable =
    (item.versions?.length ?? 0) > 0 ||
    (item.playback_variants?.length ?? 0) > 0 ||
    Boolean(item.play_content_id);
  const primaryAction = resolveLeafPrimaryAction(item, "Play Episode");
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
    primaryAction.label === "Resume" && isPlayable
      ? `/watch/${item.content_id}?restart=1`
      : undefined;

  const title = item.title ?? "";
  const seriesTitle = item.series_title ?? "Series";
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
  // The hero backdrop prefers this episode's own title card: the detail
  // presigns an episode's still as `poster_url`, and episode rows name the same
  // artwork `still_url`, consulted last for a series that has a poster but no
  // backdrop. The series backdrop keeps its place in between, so an episode with
  // no title card of its own renders as it did before.
  //
  // The URL picks the source; the thumbhash is that same source's, even when it
  // has none. Pairing the two independently could show one image behind another
  // image's generated thumbnail, so the placeholder would not belong to the
  // artwork it stands in for.
  const currentEpisode = siblingEpisodes.find((episode) => episode.content_id === item.content_id);
  const heroArtwork = item.poster_url
    ? { url: item.poster_url, thumbhash: item.poster_thumbhash }
    : item.backdrop_url
      ? { url: item.backdrop_url, thumbhash: item.backdrop_thumbhash }
      : currentEpisode?.still_url
        ? { url: currentEpisode.still_url, thumbhash: currentEpisode.still_thumbhash }
        : undefined;
  const heroBackdropUrl = heroArtwork?.url;
  const heroBackdropThumbhash = heroArtwork?.thumbhash;
  // The page glow follows the artwork the hero actually shows, the way the ebook
  // and manga pages key it to their poster.
  useAmbientColor(heroBackdropThumbhash);
  const seasonLabel =
    navigationState?.parentSeasonLabel ??
    (currentSeason
      ? getSeasonDisplayTitle(currentSeason)
      : seasonNum === 0
        ? "Specials"
        : seasonNum != null
          ? `Season ${seasonNum}`
          : "Season");
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
    breadcrumbSegments.push({ label: `Episode ${episodeNum}` });
  }

  return (
    <div>
      <div className="episode-detail-viewport">
        <DetailHero
          variant="episode"
          title={title}
          topNav={<PageBack />}
          context={<DetailBreadcrumb segments={breadcrumbSegments} />}
          backdropUrl={heroBackdropUrl}
          backdropThumbhash={heroBackdropThumbhash}
          hidePoster
          logoUrl={item.logo_url}
          metadata={
            <div className="flex flex-wrap items-center gap-2">
              <MetadataBadges
                duration={formatRuntimeMinutes(selectedMediaSummary.durationMinutes) || undefined}
              />
              {upcoming && (
                <span className="metadata-badge gap-1">
                  <CalendarClock className="size-3" aria-hidden="true" />
                  Upcoming{item.air_date && ` · ${formatCalendarDate(item.air_date)}`}
                </span>
              )}
              {!upcoming && item.air_date && (
                <span className="metadata-badge">{formatCalendarDate(item.air_date)}</span>
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
              hasVirtualSource={item.has_virtual_source === true}
              canAddToCollection={false}
              watchTogether={watchTogether.menu}
              playHref={isPlayable ? `/watch/${item.content_id}` : undefined}
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
              onShowMediaInfo={
                canCurateMetadata && (item.versions?.length ?? 0) > 0
                  ? () => openMediaInfo()
                  : undefined
              }
              versions={versionsWithLiveness}
              playbackVariants={item.playback_variants}
              selectedVersion={selectedVersion}
              selectedVersionMissing={selectedVersionMissing}
              onVersionPickerOpenChange={handleVersionPickerOpenChange}
              explicitFileSelection={explicitFileSelection}
              onSelectVersion={handleSelectVersion}
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
          <section className="page-shell episode-detail-navigation" aria-label="More episodes">
            {/* More Episodes carousel — most useful, so show first */}
            {siblingsLoading ? (
              <EpisodeCarouselSkeleton />
            ) : (
              <div>
                <h2 className="mb-5 text-xl font-semibold tracking-tight">More Episodes</h2>
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
            title="Media locations"
            versions={item.versions}
            onShowMediaInfo={openMediaInfo}
          />
        )}

        {item.cast && item.cast.length > 0 && (
          <div>
            <h2 className="mb-5 text-xl font-semibold tracking-tight">Cast</h2>
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
        versions={item.versions ?? []}
        title={title}
        selectedFileId={selectedVersion?.file_id}
        onStaleVersion={onStaleVersion}
        createTarget={{
          contentId: item.series_id ?? item.content_id,
          episodeId: item.content_id,
        }}
      />
      <SubtitleSearchDialog
        open={subtitleSearchOpen}
        onOpenChange={setSubtitleSearchOpen}
        version={selectedVersion}
        title={title}
      />
      {canCurateMetadata && (
        <MediaInfoDialog
          open={mediaInfoOpen}
          onOpenChange={setMediaInfoOpen}
          versions={item.versions ?? []}
          title={title}
          initialFileId={mediaInfoFileId}
        />
      )}
      {watchTogether.sheet}
    </div>
  );
}
