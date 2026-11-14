import { useCallback, useEffect, useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import type { FileVersion, ItemDetail } from "@/api/types";
import type { PlayerSubtitleTrackSignature, PrePlaySubtitleSelection } from "@/player/types";
import { useRedetectItemMarkers, useRefreshItemMetadata } from "@/hooks/queries/items";
import { useAdminMarkerCapabilities } from "@/hooks/queries/admin/markers";
import { useLibraryCapabilities } from "@/hooks/queries/admin/libraries";
import { useSimilarItems } from "@/hooks/queries/recommendations";
import { useDeleteSubtitlePreference, useSetSubtitlePreference } from "@/hooks/queries/subtitles";
import { useVersionLiveness, applyVersionAvailability } from "@/hooks/queries/versionLiveness";
import { useAuth } from "@/hooks/useAuth";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import CastCarousel from "@/components/CastCarousel";
import { buildCrewGroups } from "@/components/castCrewGroups";
import DownloadVersionPicker from "@/components/DownloadVersionPicker";
import EditMetadataDialog from "@/components/EditMetadataDialog";
import MediaLocations from "@/components/MediaLocations";
import MatchItemDialog from "@/components/MatchItemDialog";
import SplitItemDialog from "@/components/SplitItemDialog";
import PageBack from "@/components/PageBack";
import RecommendationGrid from "@/components/RecommendationGrid";
import DetailHero from "./DetailHero";
import DetailLayout, { DetailSection } from "./DetailLayout";
import { useOnViewTranslation } from "@/hooks/useOnViewTranslation";
import MetadataBadges from "./components/MetadataBadges";
import TrailersSection from "./components/TrailersSection";
import ExtrasSection from "./components/ExtrasSection";
import CollectionsSection from "./components/CollectionsSection";
import QualityBadges from "./components/QualityBadges";
import ScoreRow from "./components/ScoreRow";
import HeroCrewLine from "./components/HeroCrewLine";
import MediaUserActionBar from "./components/MediaUserActionBar";
import MediaInfoDialog from "./components/MediaInfoDialog";
import SubtitleSearchDialog from "./components/SubtitleSearchDialog";
import { sortByResolution } from "./components/VersionFlyout";
import { selectDefaultPlaybackVariantVersion } from "./components/versionRankingUtils";
import { readRequestedVersion, rememberRequestedFileId } from "@/playback/requestedVersion";
import { resolveSelectedMediaSummary } from "./components/selectedMediaSummary";
import { RecommendationGridSkeleton } from "./components/SectionSkeletons";
import { resolveLeafPrimaryAction } from "./itemDetailLayout";
import {
  canCurateMetadata as canCurateMetadataForUser,
  canEditMarkers as canEditMarkersForUser,
} from "@/lib/permissions";
import { formatRuntimeMinutes } from "@/lib/mediaFormat";
import { useQualityPreference } from "@/hooks/queries/qualityPreference";
import { useDetailWatchTogether } from "@/pages/watchtogether/DetailWatchTogether";
import { hasMachineTranslation } from "./components/machineTranslation";

export default function MovieContent({
  item,
  showAdvisoryAge,
  onStaleVersion,
  onSelectedFileIdChange,
}: {
  item: ItemDetail & { type: "movie" };
  showAdvisoryAge?: boolean;
  onStaleVersion?: (fileId: number) => void;
  /**
   * Reports the viewer's explicit version pick (the in-place choice or the
   * remembered one on this device) so the page's detail read can carry it. The
   * server-derived default is not reported: it would only duplicate the read
   * the page already made.
   */
  onSelectedFileIdChange?: (fileId: number | null) => void;
}) {
  const { translating: overviewTranslating, onTranslate: onTranslateOverview } =
    useOnViewTranslation(item);
  const navigate = useNavigate();
  const { search } = useLocation();
  // Follow the item to its new content ID, keeping the query string (such as
  // ?libraryId=) so the page keeps its library scope.
  const followReplacedItem = (contentID: string) =>
    navigate({ pathname: `/item/${contentID}`, search }, { replace: true });
  useAmbientColor(item.backdrop_thumbhash);
  const { user } = useAuth();
  const isAdmin = useIsActingAdmin();
  const { profile: currentProfile } = useCurrentProfile();
  // The resolution cap comes from the settings contract, where the quality
  // picker writes; the profile column it falls back to is only the pre-cutover
  // choice, since that picker no longer mirrors into it.
  const qualityPreference = useQualityPreference(currentProfile?.quality_preference);
  const canCurateMetadata = canCurateMetadataForUser(user, currentProfile);
  const canEditMarkers = canEditMarkersForUser(user, currentProfile);

  const refreshMetadataMutation = useRefreshItemMetadata();
  const redetectMarkersMutation = useRedetectItemMarkers();
  // Movies have no re-detect action on an API node without redetect-markers
  // or movie credits.
  const markerCapabilities = useAdminMarkerCapabilities(isAdmin);
  const capabilities = useLibraryCapabilities(isAdmin).data;
  const canManageTrickplay =
    capabilities?.trickplay === true && capabilities.trickplay_supported === true;
  const canRedetectMovieCredits =
    markerCapabilities.data?.redetect_markers === true &&
    markerCapabilities.data?.movie_credits === true;
  const deleteSubtitlePreference = useDeleteSubtitlePreference();
  const setSubtitlePreference = useSetSubtitlePreference();
  const [editOpen, setEditOpen] = useState(false);
  const [matchOpen, setMatchOpen] = useState(false);
  const [splitOpen, setSplitOpen] = useState(false);
  const [downloadOpen, setDownloadOpen] = useState(false);
  const [subtitleSearchOpen, setSubtitleSearchOpen] = useState(false);
  const [mediaInfoOpen, setMediaInfoOpen] = useState(false);
  const crewGroups = useMemo(() => buildCrewGroups(item.crew ?? [], "Director"), [item.crew]);
  const [mediaInfoFileId, setMediaInfoFileId] = useState<number | null>(null);
  // Tracks whether a version picker has been opened; the picker-open path
  // widens the liveness probe from the default version to the whole list.
  const [versionPickersOpened, setVersionPickersOpened] = useState(false);
  const handleVersionPickerOpenChange = useCallback((open: boolean) => {
    if (open) {
      setVersionPickersOpened(true);
    }
  }, []);

  // Version selection state — drives the Play button and inline stream popovers.
  const sortedVersions = useMemo(() => sortByResolution(item.versions), [item.versions]);
  const userData =
    item.user_data && "position_seconds" in item.user_data ? item.user_data : undefined;
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

  // Check the default-selected version as soon as the page mounts so it can't
  // sit as an unavailable row before any liveness result exists. Only that one
  // version is probed until a picker opens, at which point the full chunked
  // check takes over (results are cached for 5 minutes, so re-opening does not
  // re-fetch).
  const livenessVersions = useMemo(
    () =>
      versionPickersOpened ? item.versions : defaultSelectedVersion ? [defaultSelectedVersion] : [],
    [defaultSelectedVersion, item.versions, versionPickersOpened],
  );
  const versionLiveness = useVersionLiveness(livenessVersions, true);
  const versionsWithLiveness = useMemo(
    () => applyVersionAvailability(item.versions, versionLiveness),
    [item.versions, versionLiveness],
  );

  const [manualSelectedFileId, setManualSelectedFileId] = useState<number | null>(null);
  // A chosen row that the enrichment refresh no longer carries is a pick-again
  // state, not permission to silently show and play another version. The same
  // applies to the persisted last-watched row on a fresh mount. The device's
  // requested row is checked first: it is the viewer's pick, while the server's
  // last file names whatever served the bytes after a substitution.
  const persistedSelectedFileId =
    manualSelectedFileId ??
    requestedFileId ??
    (userData && "last_file_id" in userData ? (userData.last_file_id ?? null) : null);
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
  const [audioSelectionMode, setAudioSelectionMode] = useState<"auto" | "explicit">("auto");
  const [explicitAudioTrackIndex, setExplicitAudioTrackIndex] = useState<number | null>(null);
  const [subtitleSelectionMode, setSubtitleSelectionMode] = useState<"auto" | "off" | "explicit">(
    "auto",
  );
  const [explicitSubtitleSelection, setExplicitSubtitleSelection] =
    useState<PrePlaySubtitleSelection | null>(null);

  // Reset selection when navigating to a different movie (adjust state during render).
  const [prevContentId, setPrevContentId] = useState(item.content_id);
  if (prevContentId !== item.content_id) {
    setPrevContentId(item.content_id);
    setManualSelectedFileId(null);
    setAudioSelectionMode("auto");
    setExplicitAudioTrackIndex(null);
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
      setAudioSelectionMode("auto");
      setExplicitAudioTrackIndex(null);
      setSubtitleSelectionMode("auto");
      setExplicitSubtitleSelection(null);
    },
    [item.content_id, requestedProfileId],
  );

  const handleSelectAudioTrack = useCallback((trackIndex: number) => {
    setAudioSelectionMode("explicit");
    setExplicitAudioTrackIndex(trackIndex);
  }, []);

  const handleResetAudioSelection = useCallback(() => {
    setAudioSelectionMode("auto");
    setExplicitAudioTrackIndex(null);
  }, []);

  const handleSelectSubtitle = (selection: PrePlaySubtitleSelection) => {
    setSubtitleSelectionMode("explicit");
    setExplicitSubtitleSelection(selection);
    // Persist as this movie's override so the choice sticks across visits,
    // exactly like a manual in-player selection.
    setSubtitlePreference.mutate({
      prefId: item.content_id,
      selection,
    });
  };

  const handleSelectSubtitleOff = () => {
    setSubtitleSelectionMode("off");
    setExplicitSubtitleSelection(null);
    setSubtitlePreference.mutate({
      prefId: item.content_id,
      selection: null,
    });
  };

  const handleResetSubtitleSelection = () => {
    setSubtitleSelectionMode("auto");
    setExplicitSubtitleSelection(null);
    // "Auto" also clears the persisted override saved by a manual in-player
    // selection, so profile-level auto selection applies to this movie again.
    deleteSubtitlePreference.mutate(item.content_id);
  };

  const isPlayable =
    (item.versions?.length ?? 0) > 0 ||
    (item.playback_variants?.length ?? 0) > 0 ||
    Boolean(item.play_content_id);
  const primaryAction = resolveLeafPrimaryAction(item, "Play");
  const watchTogether = useDetailWatchTogether({
    item,
    target:
      item.versions.length > 0
        ? {
            content_id: item.content_id,
            title: item.title,
            subtitle: item.year ? String(item.year) : undefined,
            poster_url: item.poster_url,
            poster_thumbhash: item.poster_thumbhash,
          }
        : null,
  });
  const restartHref =
    primaryAction.label === "Resume" && isPlayable
      ? `/watch/${item.content_id}?restart=1`
      : undefined;
  const { data: similarData, isLoading: similarLoading } = useSimilarItems(item.content_id);

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

  const title = item.title ?? "";
  const year = item.year ? String(item.year) : "";
  const firstStudio = (item.studios ?? [])[0];

  return (
    <DetailLayout
      hero={
        <DetailHero
          title={title}
          topNav={<PageBack />}
          context="Movie"
          studioLabel={firstStudio}
          backdropUrl={item.backdrop_url}
          backdropThumbhash={item.backdrop_thumbhash}
          posterUrl={item.poster_url}
          posterThumbhash={item.poster_thumbhash}
          logoUrl={item.logo_url}
          tagline={item.tagline || undefined}
          metadata={
            <div className="flex flex-wrap items-center gap-2">
              <MetadataBadges
                year={year || undefined}
                contentRating={item.content_rating || undefined}
                advisoryAge={showAdvisoryAge ? (item.advisory_age ?? undefined) : undefined}
                advisorySource={item.advisory_source || undefined}
                duration={formatRuntimeMinutes(selectedMediaSummary.durationMinutes) || undefined}
              />
              <QualityBadges summary={selectedMediaSummary} />
            </div>
          }
          scoreRow={<ScoreRow ratings={item.ratings} />}
          overview={item.overview}
          overviewTranslating={overviewTranslating}
          onTranslateOverview={onTranslateOverview}
          overviewMachineTranslated={hasMachineTranslation(item.machine_translated_fields)}
          taglineMachineTranslated={hasMachineTranslation(
            item.machine_translated_fields,
            "tagline",
          )}
          crewLine={<HeroCrewLine crew={item.crew ?? []} genres={item.genres} />}
          actions={
            <MediaUserActionBar
              item={item}
              contentId={item.content_id}
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
              onRefresh={
                canCurateMetadata
                  ? (mode) =>
                      refreshMetadataMutation.mutate({
                        item,
                        mode,
                        onReplaced: followReplacedItem,
                      })
                  : undefined
              }
              isRefreshing={refreshMetadataMutation.isPending}
              onRedetectMarkers={
                isAdmin && canRedetectMovieCredits
                  ? (kind) => redetectMarkersMutation.mutate({ itemId: item.content_id, kind })
                  : undefined
              }
              redetectKind="credits"
              isRedetectingMarkers={redetectMarkersMutation.isPending}
              isAdmin={isAdmin}
              canCurateMetadata={canCurateMetadata}
              canEditMarkers={canEditMarkers}
              canManageTrickplay={canManageTrickplay}
              onEditMetadata={canCurateMetadata ? () => setEditOpen(true) : undefined}
              onMatchItem={canCurateMetadata ? () => setMatchOpen(true) : undefined}
              onSplitItem={
                canCurateMetadata && item.versions.length > 1 ? () => setSplitOpen(true) : undefined
              }
              onShowMediaInfo={
                canCurateMetadata && item.versions.length > 0 ? () => openMediaInfo() : undefined
              }
              versions={versionsWithLiveness}
              hasVirtualSource={item.has_virtual_source === true}
              playbackVariants={item.playback_variants}
              selectedVersion={selectedVersion}
              selectedVersionMissing={selectedVersionMissing}
              onSelectVersion={handleSelectVersion}
              onVersionPickerOpenChange={handleVersionPickerOpenChange}
              explicitFileSelection={explicitFileSelection}
              onDownload={
                user?.download_allowed && item.versions.length > 0
                  ? () => setDownloadOpen(true)
                  : undefined
              }
              onSearchSubtitles={
                item.versions.length > 0 ? () => setSubtitleSearchOpen(true) : undefined
              }
              qualityPreference={qualityPreference}
              audioSelectionMode={audioSelectionMode}
              explicitAudioTrackIndex={explicitAudioTrackIndex}
              onSelectAudioTrack={handleSelectAudioTrack}
              onResetAudioSelection={handleResetAudioSelection}
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
      }
      overlays={
        <>
          {canCurateMetadata && (
            <EditMetadataDialog item={item} open={editOpen} onOpenChange={setEditOpen} />
          )}
          {canCurateMetadata && (
            <MatchItemDialog
              key={item.content_id}
              item={item}
              open={matchOpen}
              onOpenChange={setMatchOpen}
              onReplaced={followReplacedItem}
            />
          )}
          {canCurateMetadata && (
            <SplitItemDialog
              key={`split-${item.content_id}`}
              item={item}
              open={splitOpen}
              onOpenChange={setSplitOpen}
            />
          )}
          <DownloadVersionPicker
            open={downloadOpen}
            onOpenChange={setDownloadOpen}
            versions={item.versions}
            title={title}
            selectedFileId={selectedVersion?.file_id}
            onStaleVersion={onStaleVersion}
            createTarget={{ contentId: item.content_id }}
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
              versions={item.versions}
              title={title}
              initialFileId={mediaInfoFileId}
            />
          )}
          {watchTogether.sheet}
        </>
      }
    >
      {canCurateMetadata && (
        <MediaLocations
          title="Media locations"
          versions={item.versions}
          onShowMediaInfo={openMediaInfo}
        />
      )}

      {/* Local extras play in Silo, so they lead; YouTube videos follow. */}
      {item.extras && item.extras.length > 0 && <ExtrasSection extras={item.extras} />}

      {item.videos && item.videos.length > 0 && <TrailersSection videos={item.videos} />}

      {((item.cast?.length ?? 0) > 0 || crewGroups.some((g) => g.members.length > 0)) && (
        <DetailSection title="Cast & Crew">
          <CastCarousel cast={item.cast ?? []} crewGroups={crewGroups} limit={12} prefetchPeople />
        </DetailSection>
      )}

      <CollectionsSection collections={item.collections} />

      {/* More Like This */}
      {similarLoading ? (
        <RecommendationGridSkeleton />
      ) : (
        similarData?.items &&
        similarData.items.length > 0 && <RecommendationGrid items={similarData.items} />
      )}
    </DetailLayout>
  );
}
