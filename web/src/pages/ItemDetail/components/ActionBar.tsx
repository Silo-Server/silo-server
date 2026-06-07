import { useCallback, useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import {
  Heart,
  Plus,
  Check,
  Captions,
  Download,
  Loader2,
  MoreVertical,
  Play,
  RefreshCw,
  Pencil,
  Search,
  RotateCcw,
  Tags,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { FileVersion, PlaybackVariant } from "@/api/types";
import type { RefreshItemMetadataMode } from "@/hooks/queries/items";
import type {
  PlayerSubtitleTrackSignature,
  PrePlaySubtitleSelection,
  SubtitleMode,
} from "@/player/types";
import RefreshMetadataDialog from "@/components/RefreshMetadataDialog";
import { MarkerEditor } from "@/components/markers/MarkerEditor";
import StarRating from "@/components/StarRating";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { parseWatchHref } from "@/pages/watchRouteHelpers";
import VersionDropdown from "./VersionDropdown";
import AudioTracksPopover from "./AudioTracksPopover";
import SubtitlesPopover from "./SubtitlesPopover";

interface ActionBarProps {
  contentId?: string;
  playHref?: string;
  playLabel?: string;
  playLoading?: boolean;
  playProgress?: number;
  restartHref?: string;
  resumePositionSeconds?: number;
  resumeDurationSeconds?: number;
  resumeResolution?: string;
  resumeHdr?: boolean;
  effectiveVersionResolution?: string;
  effectiveVersionHdr?: boolean;
  watchedLabel?: string;
  onToggleWatched?: () => void;
  isUpdatingWatched?: boolean;
  onToggleFavorite?: () => void;
  isFavorite?: boolean;
  onToggleWatchlist?: () => void;
  inWatchlist?: boolean;
  onRefresh?: (mode: RefreshItemMetadataMode) => void;
  isRefreshing?: boolean;
  onRedetectIntro?: () => void;
  isRedetectingIntro?: boolean;
  onEditMetadata?: () => void;
  onMatchItem?: () => void;
  isAdmin?: boolean;
  canCurateMetadata?: boolean;
  /** Enables the "Edit Markers" action (playable items only: movies/episodes). */
  canEditMarkers?: boolean;
  versions?: FileVersion[];
  playbackVariants?: PlaybackVariant[];
  selectedVersion?: FileVersion | null;
  onSelectVersion?: (version: FileVersion) => void;
  onDownload?: () => void;
  onSearchSubtitles?: () => void;
  rating?: number | null;
  onRatingChange?: (rating: number | null) => void;
  qualityPreference?: string | null;
  audioSelectionMode?: "auto" | "explicit";
  explicitAudioTrackIndex?: number | null;
  onSelectAudioTrack?: (trackIndex: number) => void;
  onResetAudioSelection?: () => void;
  prePlaySubtitleMode?: "auto" | "off" | "explicit";
  explicitSubtitleSelection?: PrePlaySubtitleSelection | null;
  onSelectSubtitle?: (selection: PrePlaySubtitleSelection) => void;
  onSelectSubtitleOff?: () => void;
  onResetSubtitleSelection?: () => void;
  preferredSubtitleLanguage?: string | null;
  preferredSubtitleTrackSignature?: PlayerSubtitleTrackSignature | null;
  subtitleMode?: SubtitleMode;
  showForcedSubtitles?: boolean;
  profileLanguage?: string | null;
}

export default function ActionBar({
  contentId,
  playHref,
  playLabel = "Play",
  playLoading = false,
  playProgress,
  restartHref,
  resumePositionSeconds,
  resumeDurationSeconds,
  watchedLabel,
  onToggleWatched,
  isUpdatingWatched = false,
  onToggleFavorite,
  isFavorite = false,
  onToggleWatchlist,
  inWatchlist = false,
  onRefresh,
  isRefreshing = false,
  onRedetectIntro,
  isRedetectingIntro = false,
  onEditMetadata,
  onMatchItem,
  isAdmin = false,
  canCurateMetadata = false,
  canEditMarkers = false,
  versions,
  playbackVariants,
  selectedVersion,
  onSelectVersion,
  onDownload,
  onSearchSubtitles,
  rating,
  onRatingChange,
  audioSelectionMode = "auto",
  explicitAudioTrackIndex = null,
  onSelectAudioTrack,
  onResetAudioSelection,
  prePlaySubtitleMode = "auto",
  explicitSubtitleSelection = null,
  onSelectSubtitle,
  onSelectSubtitleOff,
  onResetSubtitleSelection,
  preferredSubtitleLanguage,
  preferredSubtitleTrackSignature,
  subtitleMode,
  showForcedSubtitles,
  profileLanguage,
}: ActionBarProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const playbackController = useWatchPlaybackController();
  const [playChoiceOpen, setPlayChoiceOpen] = useState(false);
  const [refreshDialogOpen, setRefreshDialogOpen] = useState(false);
  const [markerEditorOpen, setMarkerEditorOpen] = useState(false);
  const showMarkerEditor = canEditMarkers && !!contentId;
  const hasMultipleVersions = (playbackVariants?.length ?? 0) > 1 || (versions?.length ?? 0) > 1;
  const showPlayChoiceDialog =
    !hasMultipleVersions && playLabel === "Resume" && !!playHref && !!restartHref;
  const displayedPlayLabel = showPlayChoiceDialog ? "Play" : playLabel;

  const progressOverlay =
    playProgress != null && playProgress > 0 && playProgress < 100 ? (
      <span
        className="border-primary-foreground/40 bg-primary-foreground/20 pointer-events-none absolute inset-y-0 left-0 border-r-2"
        style={{ width: `${playProgress}%` }}
      />
    ) : null;

  const openPlayChoiceDialog = () => setPlayChoiceOpen(true);
  const currentHref = useMemo(
    () => `${location.pathname}${location.search}`,
    [location.pathname, location.search],
  );
  const buildPrePlayStartInput = useCallback(
    (base: {
      contentId: string;
      fileId?: number;
      libraryId?: number;
      restart?: boolean;
      returnHref?: string;
    }) => ({
      ...base,
      audioTrackIndex:
        audioSelectionMode === "explicit" &&
        explicitAudioTrackIndex != null &&
        explicitAudioTrackIndex >= 0
          ? explicitAudioTrackIndex
          : undefined,
      prePlaySubtitleMode,
      prePlaySubtitleSelection:
        prePlaySubtitleMode === "explicit" ? explicitSubtitleSelection : null,
    }),
    [audioSelectionMode, explicitAudioTrackIndex, explicitSubtitleSelection, prePlaySubtitleMode],
  );
  const startPlaybackFromHref = useCallback(
    (href: string, restartOverride?: boolean) => {
      const parsed = parseWatchHref(href);
      if (!parsed) {
        navigate(href);
        return;
      }

      playbackController.startPlayback(
        buildPrePlayStartInput({
          contentId: parsed.contentId,
          fileId: selectedVersion?.file_id ?? parsed.fileId,
          libraryId: parsed.libraryId,
          restart: restartOverride ?? parsed.restart,
          returnHref: currentHref,
        }),
      );
    },
    [buildPrePlayStartInput, currentHref, navigate, playbackController, selectedVersion?.file_id],
  );
  const handleResumePlayback = () => {
    if (!playHref) return;
    setPlayChoiceOpen(false);
    startPlaybackFromHref(playHref, false);
  };
  const handleRestartPlayback = () => {
    if (!restartHref) return;
    setPlayChoiceOpen(false);
    startPlaybackFromHref(restartHref, true);
  };
  const handleRefreshConfirm = (mode: RefreshItemMetadataMode) => {
    setRefreshDialogOpen(false);
    onRefresh?.(mode);
  };
  const hasOverflowActions = Boolean(
    restartHref || onToggleWatchlist || onDownload || onSearchSubtitles,
  );
  const hasAdminActions = Boolean(isAdmin && (contentId || onRedetectIntro));
  const hasMetadataActions = Boolean(
    (canCurateMetadata && (onRefresh || onEditMetadata || onMatchItem)) || showMarkerEditor,
  );

  const formattedResumeTime = formatPlaybackTime(resumePositionSeconds ?? 0);
  const percentComplete =
    playProgress != null && Number.isFinite(playProgress)
      ? Math.round(playProgress)
      : resumeDurationSeconds != null && resumeDurationSeconds > 0 && resumePositionSeconds != null
        ? Math.round((resumePositionSeconds / resumeDurationSeconds) * 100)
        : null;
  const dialogDescription = showPlayChoiceDialog
    ? buildPlayChoiceDescription(formattedResumeTime, percentComplete)
    : "";

  // When a version is explicitly selected (multi-version), play it directly.
  const handleSelectedVersionPlay = useCallback(
    (restart: boolean) => {
      if (!selectedVersion || !contentId) return;
      playbackController.startPlayback(
        buildPrePlayStartInput({
          contentId,
          fileId: selectedVersion.file_id,
          restart,
          returnHref: currentHref,
        }),
      );
    },
    [buildPrePlayStartInput, contentId, currentHref, playbackController, selectedVersion],
  );

  const hasStreamControls =
    selectedVersion &&
    ((versions && hasMultipleVersions) || (selectedVersion.audio_tracks?.length ?? 0) > 0 || true); // subs popover always shows when version is selected

  return (
    <div className="space-y-2.5">
      {/* ── Primary actions ──────────────────────────────────── */}
      <div className="flex flex-wrap items-center gap-3">
        {/* ── Play button ────────────────────────────────────── */}
        {playHref ? (
          showPlayChoiceDialog ? (
            <Button
              onClick={openPlayChoiceDialog}
              className="relative h-11 gap-2.5 overflow-hidden rounded-full px-8 text-[15px] font-bold tracking-wide shadow-md"
            >
              <Play className="size-[18px] fill-current" />
              {displayedPlayLabel}
              {progressOverlay}
            </Button>
          ) : selectedVersion ? (
            <Button
              onClick={() => handleSelectedVersionPlay(false)}
              className="relative h-11 gap-2.5 overflow-hidden rounded-full px-8 text-[15px] font-bold tracking-wide shadow-md"
            >
              <Play className="size-[18px] fill-current" />
              {displayedPlayLabel}
              {progressOverlay}
            </Button>
          ) : (
            <Button
              onClick={() => startPlaybackFromHref(playHref)}
              className="relative h-11 gap-2.5 overflow-hidden rounded-full px-8 text-[15px] font-bold tracking-wide shadow-md"
            >
              <Play className="size-[18px] fill-current" />
              {displayedPlayLabel}
              {progressOverlay}
            </Button>
          )
        ) : (
          <Button
            disabled
            className="h-11 gap-2.5 rounded-full px-8 text-[15px] font-bold tracking-wide"
          >
            {playLoading ? (
              <Loader2 className="size-[18px] animate-spin" />
            ) : (
              <Play className="size-[18px] fill-current" />
            )}
            {playLabel}
          </Button>
        )}

        {/* ── Watched toggle ─────────────────────────────────── */}
        {watchedLabel && onToggleWatched && (
          <Button
            variant="glass"
            onClick={onToggleWatched}
            disabled={isUpdatingWatched}
            className="h-11 rounded-full px-5 text-[14px] font-semibold"
          >
            <Check className="size-[18px]" />
            {watchedLabel}
          </Button>
        )}

        {/* ── Icon action buttons ────────────────────────────── */}
        {onToggleFavorite && (
          <Button
            variant="glass"
            size="icon-lg"
            onClick={onToggleFavorite}
            title={isFavorite ? "Unfavorite" : "Favorite"}
            className="size-11 rounded-full"
          >
            <Heart
              className={`size-[18px] transition-colors ${isFavorite ? "fill-current text-red-400" : ""}`}
            />
          </Button>
        )}

        {onRatingChange && (
          <StarRating value={rating ?? null} onChange={onRatingChange} size={18} />
        )}

        <DropdownMenu modal={false}>
          <DropdownMenuTrigger asChild>
            <Button variant="glass" size="icon-lg" title="More" className="size-11 rounded-full">
              <MoreVertical className="size-[18px]" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-56">
            {restartHref && (
              <DropdownMenuItem
                onSelect={() => {
                  handleRestartPlayback();
                }}
              >
                <RotateCcw className="size-4" />
                Play from Beginning
              </DropdownMenuItem>
            )}
            {onToggleWatchlist && (
              <DropdownMenuItem onSelect={onToggleWatchlist}>
                {inWatchlist ? <Check className="size-4" /> : <Plus className="size-4" />}
                {inWatchlist ? "Remove from Watchlist" : "Add to Watchlist"}
              </DropdownMenuItem>
            )}
            {onDownload && (
              <DropdownMenuItem onSelect={onDownload}>
                <Download className="size-4" />
                Download
              </DropdownMenuItem>
            )}
            {onSearchSubtitles && (
              <DropdownMenuItem onSelect={onSearchSubtitles}>
                <Captions className="size-4" />
                Search Subtitles
              </DropdownMenuItem>
            )}
            {(hasAdminActions || hasMetadataActions) && (
              <>
                {hasOverflowActions && <DropdownMenuSeparator />}
                {isAdmin && contentId && (
                  <DropdownMenuItem
                    onSelect={() =>
                      navigate(`/admin/history?media_item_id=${encodeURIComponent(contentId)}`)
                    }
                  >
                    View Play History
                  </DropdownMenuItem>
                )}
                {canCurateMetadata && onRefresh && (
                  <DropdownMenuItem
                    disabled={isRefreshing}
                    onSelect={() => {
                      setRefreshDialogOpen(true);
                    }}
                  >
                    {isRefreshing && <RefreshCw className="size-4 animate-spin" />}
                    Refresh Metadata
                  </DropdownMenuItem>
                )}
                {isAdmin && onRedetectIntro && (
                  <DropdownMenuItem disabled={isRedetectingIntro} onSelect={onRedetectIntro}>
                    <RefreshCw className={`size-4 ${isRedetectingIntro ? "animate-spin" : ""}`} />
                    Re-detect Intro Markers
                  </DropdownMenuItem>
                )}
                {canCurateMetadata && onEditMetadata && (
                  <DropdownMenuItem onSelect={onEditMetadata}>
                    <Pencil className="size-4" />
                    Edit Metadata
                  </DropdownMenuItem>
                )}
                {showMarkerEditor && (
                  <DropdownMenuItem onSelect={() => setMarkerEditorOpen(true)}>
                    <Tags className="size-4" />
                    Edit Markers
                  </DropdownMenuItem>
                )}
                {canCurateMetadata && onMatchItem && (
                  <DropdownMenuItem onSelect={onMatchItem}>
                    <Search className="size-4" />
                    Match Item
                  </DropdownMenuItem>
                )}
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
        {showPlayChoiceDialog && (
          <Dialog open={playChoiceOpen} onOpenChange={setPlayChoiceOpen}>
            <DialogContent className="max-w-xs gap-3 p-5">
              <DialogHeader className="gap-1.5">
                <DialogTitle className="text-base">Resume Playback?</DialogTitle>
                <DialogDescription className="text-xs">{dialogDescription}</DialogDescription>
              </DialogHeader>
              <div className="grid gap-2">
                <Button
                  onClick={handleResumePlayback}
                  className="h-9 justify-start gap-2.5 px-3 text-sm"
                >
                  <Play className="size-3.5 fill-current" />
                  Resume at {formattedResumeTime}
                </Button>
                <Button
                  variant="outline"
                  onClick={handleRestartPlayback}
                  className="h-9 justify-start gap-2.5 px-3 text-sm"
                >
                  <RotateCcw className="size-3.5" />
                  Play from Beginning
                </Button>
              </div>
            </DialogContent>
          </Dialog>
        )}
        {showMarkerEditor && contentId && (
          <MarkerEditor
            itemId={contentId}
            open={markerEditorOpen}
            onOpenChange={setMarkerEditorOpen}
          />
        )}
        <RefreshMetadataDialog
          open={refreshDialogOpen}
          onOpenChange={setRefreshDialogOpen}
          onConfirm={handleRefreshConfirm}
          isPending={isRefreshing}
        />
      </div>

      {/* ── Stream info controls (second row) ──────────────── */}
      {hasStreamControls && (
        <div className="flex min-w-0 items-center gap-2">
          {versions && hasMultipleVersions && selectedVersion && onSelectVersion && (
            <VersionDropdown
              versions={versions}
              playbackVariants={playbackVariants}
              selectedVersion={selectedVersion}
              onSelectVersion={onSelectVersion}
            />
          )}
          {selectedVersion && (selectedVersion.audio_tracks?.length ?? 0) > 0 && (
            <AudioTracksPopover
              version={selectedVersion}
              selectionMode={audioSelectionMode}
              explicitTrackIndex={explicitAudioTrackIndex}
              onSelectTrack={onSelectAudioTrack}
              onResetSelection={onResetAudioSelection}
            />
          )}
          {selectedVersion && (
            <SubtitlesPopover
              version={selectedVersion}
              selectionMode={prePlaySubtitleMode}
              explicitSelection={explicitSubtitleSelection}
              preferredSubtitleLanguage={preferredSubtitleLanguage}
              preferredSubtitleTrackSignature={preferredSubtitleTrackSignature}
              subtitleMode={subtitleMode}
              showForcedSubtitles={showForcedSubtitles}
              profileLanguage={profileLanguage}
              activeAudioTrackIndex={
                audioSelectionMode === "explicit" ? explicitAudioTrackIndex : null
              }
              onSelectSubtitle={onSelectSubtitle}
              onSelectSubtitleOff={onSelectSubtitleOff}
              onResetSelection={onResetSubtitleSelection}
            />
          )}
        </div>
      )}
    </div>
  );
}

function formatPlaybackTime(totalSeconds: number): string {
  const seconds = Math.max(0, Math.floor(totalSeconds));
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainingSeconds = seconds % 60;

  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, "0")}:${String(remainingSeconds).padStart(2, "0")}`;
  }

  return `${minutes}:${String(remainingSeconds).padStart(2, "0")}`;
}

function buildPlayChoiceDescription(
  formattedResumeTime: string,
  percentComplete: number | null,
): string {
  if (percentComplete != null && percentComplete > 0) {
    return `You're ${formattedResumeTime} in, about ${percentComplete}% through.`;
  }

  return `You're ${formattedResumeTime} in. Resume where you left off or start over.`;
}
