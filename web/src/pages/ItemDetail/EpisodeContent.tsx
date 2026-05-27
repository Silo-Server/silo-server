import { useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import type { FileVersion, ItemDetail } from "@/api/types";
import type { PlayerSubtitleTrackSignature, PrePlaySubtitleSelection } from "@/player/types";
import { useSeasonDetail, useSeasonEpisodes } from "@/hooks/queries/episodes";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import {
  useRedetectEpisodeIntro,
  useRefreshItemMetadata,
  useWatchedStateMutation,
} from "@/hooks/queries/items";
import CastCarousel from "@/components/CastCarousel";
import CrewList from "@/components/CrewList";
import DownloadVersionPicker from "@/components/DownloadVersionPicker";
import EditMetadataDialog from "@/components/EditMetadataDialog";
import MediaLocations from "@/components/MediaLocations";
import EpisodeCarousel from "./components/EpisodeCarousel";
import DetailHero from "./DetailHero";
import MetadataBadges from "./components/MetadataBadges";
import QualityBadges from "./components/QualityBadges";
import ScoreRow from "./components/ScoreRow";
import HeroCrewLine from "./components/HeroCrewLine";
import ActionBar from "./components/ActionBar";
import DetailBreadcrumb from "./components/DetailBreadcrumb";
import SubtitleSearchDialog from "./components/SubtitleSearchDialog";
import { sortByResolution } from "./components/VersionFlyout";
import { selectDefaultPlaybackVariantVersion } from "./components/versionRankingUtils";
import { EpisodeCarouselSkeleton } from "./components/SectionSkeletons";
import {
  getSeasonDisplayTitle,
  resolveLeafPrimaryAction,
  resolveEpisodeSiblingSeason,
  type EpisodeNavigationState,
} from "./itemDetailLayout";
import { getWatchedActionLabel } from "./watchedState";
import { canCurateMetadata as canCurateMetadataForUser } from "@/lib/permissions";

function formatDuration(minutes: number): string {
  if (minutes <= 0) return "";
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

export default function EpisodeContent({ item }: { item: ItemDetail & { type: "episode" } }) {
  const navigate = useNavigate();
  const location = useLocation();
  useAmbientColor(item.backdrop_thumbhash);
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const canCurateMetadata = canCurateMetadataForUser(user);
  const { profile: currentProfile } = useCurrentProfile();
  const [editOpen, setEditOpen] = useState(false);
  const [downloadOpen, setDownloadOpen] = useState(false);
  const [subtitleSearchOpen, setSubtitleSearchOpen] = useState(false);
  const refreshMetadataMutation = useRefreshItemMetadata();
  const redetectIntroMutation = useRedetectEpisodeIntro();

  // Version selection state — drives the Play button and inline stream popovers.
  const sortedVersions = useMemo(() => sortByResolution(item.versions ?? []), [item.versions]);
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

  // Reset selection when navigating to a different episode (adjust state during render).
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

  const handleResetSubtitleSelection = () => {
    setSubtitleSelectionMode("auto");
    setExplicitSubtitleSelection(null);
  };

  const handleSelectSubtitleOff = () => {
    setSubtitleSelectionMode("off");
    setExplicitSubtitleSelection(null);
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
  const watchedMutation = useWatchedStateMutation(item);
  const navigationState = location.state as EpisodeNavigationState | null;
  const primaryAction = resolveLeafPrimaryAction(item, "Play Episode");
  const restartHref =
    primaryAction.label === "Resume" && (item.versions?.length ?? 0) > 0
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
  );

  const ratingImdb = item.rating_imdb;
  const ratingTmdb = item.rating_tmdb;
  const effectiveRating = ratingImdb ?? ratingTmdb;

  // Sibling episodes now come from the season collection, not the current episode ID.
  const { data: episodesData, isLoading: siblingsLoading } = useSeasonEpisodes(
    siblingSeason?.seriesId,
    siblingSeason?.seasonNumber ?? -1,
  );
  const siblingEpisodes = episodesData?.episodes ?? [];
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
    (currentSeason ? `/item/${currentSeason.content_id}` : undefined);
  const episodeLinkState =
    seasonHref || seasonLabel
      ? {
          parentSeasonHref: seasonHref,
          parentSeasonLabel: seasonLabel,
        }
      : undefined;

  // Build breadcrumb segments
  const breadcrumbSegments = [
    { label: seriesTitle, href: seriesId ? `/item/${seriesId}` : "/" },
  ] as Array<{ label: string; href?: string }>;
  if (seasonNum != null) {
    breadcrumbSegments.push({ label: seasonLabel, href: seasonHref });
  }
  if (episodeNum != null) {
    breadcrumbSegments.push({ label: `Episode ${episodeNum}` });
  }

  const contextLabel =
    seasonNum != null && episodeNum != null ? `S${seasonNum} \u00B7 E${episodeNum}` : undefined;

  return (
    <div>
      <DetailHero
        title={title}
        context={
          <div className="space-y-3">
            <DetailBreadcrumb segments={breadcrumbSegments} />
            {contextLabel && (
              <div className="text-muted-foreground text-xs font-medium">{contextLabel}</div>
            )}
          </div>
        }
        backdropUrl={item.backdrop_url}
        backdropThumbhash={item.backdrop_thumbhash}
        hidePoster
        logoUrl={item.logo_url}
        metadata={
          <div className="flex flex-wrap items-center gap-2">
            <MetadataBadges duration={formatDuration(item.runtime ?? 0) || undefined} />
            {item.air_date && (
              <span className="metadata-badge">
                {(() => {
                  const d = new Date(item.air_date);
                  return Number.isNaN(d.getTime())
                    ? item.air_date
                    : new Intl.DateTimeFormat(undefined, {
                        year: "numeric",
                        month: "short",
                        day: "numeric",
                      }).format(d);
                })()}
              </span>
            )}
            <QualityBadges versions={item.versions ?? []} />
          </div>
        }
        scoreRow={
          <ScoreRow
            ratingImdb={effectiveRating}
            ratingRtCritic={item.rating_rt_critic}
            ratingRtAudience={item.rating_rt_audience}
          />
        }
        overview={item.overview}
        crewLine={<HeroCrewLine crew={item.crew ?? []} />}
        actions={
          <ActionBar
            contentId={item.content_id}
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
            watchedLabel={getWatchedActionLabel(item)}
            onToggleWatched={() => watchedMutation.mutate(!(item.user_data?.played ?? false))}
            isUpdatingWatched={watchedMutation.isPending}
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
            onRedetectIntro={
              isAdmin ? () => redetectIntroMutation.mutate(item.content_id) : undefined
            }
            isRedetectingIntro={redetectIntroMutation.isPending}
            isAdmin={isAdmin}
            canCurateMetadata={canCurateMetadata}
            onEditMetadata={canCurateMetadata ? () => setEditOpen(true) : undefined}
            versions={item.versions ?? []}
            playbackVariants={item.playback_variants}
            selectedVersion={selectedVersion}
            onSelectVersion={handleSelectVersion}
            onDownload={
              user?.download_allowed && (item.versions ?? []).length > 0
                ? () => setDownloadOpen(true)
                : undefined
            }
            onSearchSubtitles={
              (item.versions?.length ?? 0) > 0 ? () => setSubtitleSearchOpen(true) : undefined
            }
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

        {/* More Episodes carousel — most useful, so show first */}
        {siblingsLoading ? (
          <EpisodeCarouselSkeleton />
        ) : (
          siblingEpisodes.length > 1 && (
            <div>
              <h2 className="mb-5 text-xl font-semibold tracking-tight">More Episodes</h2>
              <EpisodeCarousel
                episodes={siblingEpisodes}
                currentEpisodeNumber={episodeNum ?? -1}
                episodeLinkState={episodeLinkState}
              />
            </div>
          )
        )}

        {item.cast && item.cast.length > 0 && (
          <div>
            <h2 className="mb-5 text-xl font-semibold tracking-tight">Cast</h2>
            <CastCarousel cast={item.cast} />
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
