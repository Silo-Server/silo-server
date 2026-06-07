import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import type { FileVersion, ItemDetail } from "@/api/types";
import type { PlayerSubtitleTrackSignature, PrePlaySubtitleSelection } from "@/player/types";
import { useToggleFavorite } from "@/hooks/queries/favorites";
import { useToggleWatchlist } from "@/hooks/queries/watchlist";
import { useRefreshItemMetadata, useWatchedStateMutation } from "@/hooks/queries/items";
import { useSetRating, useDeleteRating } from "@/hooks/queries/ratings";
import { useSimilarItems } from "@/hooks/queries/recommendations";
import { useAuth } from "@/hooks/useAuth";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import CastCarousel from "@/components/CastCarousel";
import CrewList from "@/components/CrewList";
import DownloadVersionPicker from "@/components/DownloadVersionPicker";
import EditMetadataDialog from "@/components/EditMetadataDialog";
import MediaLocations from "@/components/MediaLocations";
import MatchItemDialog from "@/components/MatchItemDialog";
import PageBack from "@/components/PageBack";
import RecommendationGrid from "@/components/RecommendationGrid";
import DetailHero from "./DetailHero";
import MetadataBadges from "./components/MetadataBadges";
import QualityBadges from "./components/QualityBadges";
import ScoreRow from "./components/ScoreRow";
import HeroCrewLine from "./components/HeroCrewLine";
import ActionBar from "./components/ActionBar";
import SubtitleSearchDialog from "./components/SubtitleSearchDialog";
import { sortByResolution } from "./components/VersionFlyout";
import { selectDefaultPlaybackVariantVersion } from "./components/versionRankingUtils";
import { RecommendationGridSkeleton } from "./components/SectionSkeletons";
import { resolveLeafPrimaryAction } from "./itemDetailLayout";
import { getWatchedActionLabel } from "./watchedState";
import {
  canCurateMetadata as canCurateMetadataForUser,
  canEditMarkers as canEditMarkersForUser,
} from "@/lib/permissions";

function formatDuration(minutes: number): string {
  if (minutes <= 0) return "";
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

export default function MovieContent({ item }: { item: ItemDetail & { type: "movie" } }) {
  const navigate = useNavigate();
  useAmbientColor(item.backdrop_thumbhash);
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const canCurateMetadata = canCurateMetadataForUser(user);
  const canEditMarkers = canEditMarkersForUser(user);
  const { profile: currentProfile } = useCurrentProfile();

  const isFavorite = item.user_state?.is_favorite ?? false;
  const inWatchlist = item.user_state?.in_watchlist ?? false;
  const toggleFavoriteMutation = useToggleFavorite(item.content_id);
  const toggleWatchlistMutation = useToggleWatchlist(item.content_id);
  const refreshMetadataMutation = useRefreshItemMetadata();
  const watchedMutation = useWatchedStateMutation(item);
  const setRatingMutation = useSetRating(item.content_id);
  const deleteRatingMutation = useDeleteRating(item.content_id);
  const [editOpen, setEditOpen] = useState(false);
  const [matchOpen, setMatchOpen] = useState(false);
  const [downloadOpen, setDownloadOpen] = useState(false);
  const [subtitleSearchOpen, setSubtitleSearchOpen] = useState(false);

  // Version selection state — drives the Play button and inline stream popovers.
  const sortedVersions = useMemo(() => sortByResolution(item.versions), [item.versions]);
  const userData =
    item.user_data && "position_seconds" in item.user_data ? item.user_data : undefined;
  const defaultSelectedVersion = useMemo(
    () =>
      selectDefaultPlaybackVariantVersion(
        sortedVersions,
        item.playback_variants,
        userData,
        currentProfile?.quality_preference,
        item.effective_version_edition_key,
      ),
    [
      currentProfile?.quality_preference,
      item.effective_version_edition_key,
      item.playback_variants,
      sortedVersions,
      userData,
    ],
  );
  const [manualSelectedFileId, setManualSelectedFileId] = useState<number | null>(null);
  const selectedVersion = useMemo(
    () =>
      (manualSelectedFileId != null
        ? (sortedVersions.find((version) => version.file_id === manualSelectedFileId) ?? null)
        : null) ?? defaultSelectedVersion,
    [defaultSelectedVersion, manualSelectedFileId, sortedVersions],
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
  const handleSelectVersion = (version: FileVersion) => {
    setManualSelectedFileId(version.file_id);
    setAudioSelectionMode("auto");
    setExplicitAudioTrackIndex(null);
    setSubtitleSelectionMode("auto");
    setExplicitSubtitleSelection(null);
  };

  const handleSelectAudioTrack = (trackIndex: number) => {
    setAudioSelectionMode("explicit");
    setExplicitAudioTrackIndex(trackIndex);
  };

  const handleResetAudioSelection = () => {
    setAudioSelectionMode("auto");
    setExplicitAudioTrackIndex(null);
  };

  const handleSelectSubtitle = (selection: PrePlaySubtitleSelection) => {
    setSubtitleSelectionMode("explicit");
    setExplicitSubtitleSelection(selection);
  };

  const handleSelectSubtitleOff = () => {
    setSubtitleSelectionMode("off");
    setExplicitSubtitleSelection(null);
  };

  const handleResetSubtitleSelection = () => {
    setSubtitleSelectionMode("auto");
    setExplicitSubtitleSelection(null);
  };

  const primaryAction = resolveLeafPrimaryAction(item, "Play");
  const restartHref =
    primaryAction.label === "Resume" && item.versions.length > 0
      ? `/watch/${item.content_id}?restart=1`
      : undefined;
  const { data: similarData, isLoading: similarLoading } = useSimilarItems(item.content_id);

  const handleRatingChange = (rating: number | null) => {
    if (rating === null) {
      deleteRatingMutation.mutate();
    } else {
      setRatingMutation.mutate(rating);
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

  const title = item.title ?? "";
  const year = item.year ? String(item.year) : "";
  const firstStudio = (item.studios ?? [])[0];

  return (
    <div>
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
              duration={formatDuration(item.runtime ?? 0) || undefined}
            />
            <QualityBadges versions={item.versions} />
          </div>
        }
        scoreRow={
          <ScoreRow
            ratingImdb={item.rating_imdb}
            ratingRtCritic={item.rating_rt_critic}
            ratingRtAudience={item.rating_rt_audience}
          />
        }
        overview={item.overview}
        crewLine={<HeroCrewLine crew={item.crew ?? []} genres={item.genres} />}
        actions={
          <ActionBar
            contentId={item.content_id}
            playHref={item.versions.length > 0 ? `/watch/${item.content_id}` : undefined}
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
            watchedLabel={getWatchedActionLabel(item)}
            onToggleWatched={() => watchedMutation.mutate(!(item.user_data?.played ?? false))}
            isUpdatingWatched={watchedMutation.isPending}
            onToggleFavorite={() => toggleFavoriteMutation.mutate(isFavorite)}
            isFavorite={isFavorite}
            onToggleWatchlist={() => toggleWatchlistMutation.mutate(inWatchlist)}
            inWatchlist={inWatchlist}
            onRefresh={
              canCurateMetadata
                ? (mode) =>
                    refreshMetadataMutation.mutate({
                      item,
                      mode,
                      onReplaced: (contentID) => navigate(`/item/${contentID}`, { replace: true }),
                    })
                : undefined
            }
            isRefreshing={refreshMetadataMutation.isPending}
            isAdmin={isAdmin}
            canCurateMetadata={canCurateMetadata}
            canEditMarkers={canEditMarkers}
            onEditMetadata={canCurateMetadata ? () => setEditOpen(true) : undefined}
            onMatchItem={canCurateMetadata ? () => setMatchOpen(true) : undefined}
            versions={item.versions}
            playbackVariants={item.playback_variants}
            selectedVersion={selectedVersion}
            onSelectVersion={handleSelectVersion}
            onDownload={
              user?.download_allowed && item.versions.length > 0
                ? () => setDownloadOpen(true)
                : undefined
            }
            onSearchSubtitles={
              item.versions.length > 0 ? () => setSubtitleSearchOpen(true) : undefined
            }
            rating={item.user_rating ?? null}
            onRatingChange={handleRatingChange}
            qualityPreference={currentProfile?.quality_preference}
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
            profileLanguage={currentProfile?.language}
          />
        }
      />

      <div className="page-shell space-y-12 py-10 sm:space-y-14">
        {canCurateMetadata && <MediaLocations title="Media locations" versions={item.versions} />}

        {item.cast && item.cast.length > 0 && (
          <div>
            <h2 className="mb-5 text-xl font-semibold tracking-tight">Cast</h2>
            <CastCarousel cast={item.cast} />
          </div>
        )}

        {item.crew && item.crew.length > 0 && <CrewList crew={item.crew} />}

        {/* More Like This */}
        {similarLoading ? (
          <RecommendationGridSkeleton />
        ) : (
          similarData?.items &&
          similarData.items.length > 0 && (
            <div>
              <h2 className="mb-5 text-xl font-semibold tracking-tight">More Like This</h2>
              <RecommendationGrid items={similarData.items} />
            </div>
          )
        )}
      </div>
      {canCurateMetadata && (
        <EditMetadataDialog item={item} open={editOpen} onOpenChange={setEditOpen} />
      )}
      {canCurateMetadata && (
        <MatchItemDialog
          key={item.content_id}
          item={item}
          open={matchOpen}
          onOpenChange={setMatchOpen}
        />
      )}
      <DownloadVersionPicker
        open={downloadOpen}
        onOpenChange={setDownloadOpen}
        versions={item.versions}
        title={title}
      />
      <SubtitleSearchDialog
        open={subtitleSearchOpen}
        onOpenChange={setSubtitleSearchOpen}
        version={selectedVersion}
        title={title}
      />
    </div>
  );
}
