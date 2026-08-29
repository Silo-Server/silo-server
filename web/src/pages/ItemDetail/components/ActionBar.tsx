import {
  type ComponentProps,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { useLocation, useNavigate } from "react-router";
import {
  Heart,
  Plus,
  Check,
  Captions,
  Download,
  FolderPlus,
  Info,
  Loader2,
  MoreVertical,
  Play,
  RefreshCw,
  Scissors,
  RotateCcw,
  Tags,
} from "lucide-react";
import AddToCollectionDialog from "@/components/AddToCollectionDialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
import { MediaActionIcon } from "@/components/mediaActionIcons";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { parseWatchHref } from "@/pages/watchRouteHelpers";
import VersionDropdown from "./VersionDropdown";
import AudioTracksPopover from "./AudioTracksPopover";
import SubtitlesPopover from "./SubtitlesPopover";

// Keep hover feedback on the compositor. Repainting these controls while the detail backdrop is
// animating can stall the main thread on image-heavy movie and series pages.
const responsivePrimaryActionClass =
  "transform-gpu transition-transform duration-150 motion-safe:hover:scale-[1.02] motion-safe:active:scale-[0.98]";
const responsivePlayActionClass = `${responsivePrimaryActionClass} hover:bg-primary motion-reduce:hover:bg-primary/90`;
const staticGlassActionClass = "transition-none";

function DetailOverflowMenuItem({
  className,
  closeMenu,
  onAction,
  ...props
}: Omit<ComponentProps<"button">, "onClick"> & {
  closeMenu: () => void;
  onAction?: () => void;
}) {
  return (
    <button
      {...props}
      type="button"
      role="menuitem"
      className={`focus:bg-accent focus:text-accent-foreground hover:bg-accent hover:text-accent-foreground [&_svg:not([class*='text-'])]:text-muted-foreground relative flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm outline-hidden select-none disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 ${className ?? ""}`}
      onClick={() => {
        closeMenu();
        onAction?.();
      }}
    />
  );
}

export interface ActionBarProps {
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
  onSplitItem?: () => void;
  onShowMediaInfo?: () => void;
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
  onSplitItem,
  onShowMediaInfo,
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
  const [overflowOpen, setOverflowOpen] = useState(false);
  const overflowMenuId = useId();
  const [overflowPosition, setOverflowPosition] = useState<{ left: number; top: number } | null>(
    null,
  );
  const overflowTriggerRef = useRef<HTMLButtonElement>(null);
  const overflowMenuRef = useRef<HTMLDivElement>(null);
  const scrollFrameRef = useRef<number | null>(null);
  const typeaheadRef = useRef<{ query: string; at: number }>({ query: "", at: 0 });
  const [refreshDialogOpen, setRefreshDialogOpen] = useState(false);
  const [addToCollectionOpen, setAddToCollectionOpen] = useState(false);
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
  const closeOverflowMenu = useCallback(() => setOverflowOpen(false), []);
  const toggleOverflowMenu = useCallback(() => {
    setOverflowPosition(null);
    setOverflowOpen((open) => !open);
  }, []);
  const positionOverflowMenu = useCallback(() => {
    const trigger = overflowTriggerRef.current;
    const menu = overflowMenuRef.current;
    if (!trigger || !menu) return;

    const viewportPadding = 8;
    const gap = 8;
    const triggerRect = trigger.getBoundingClientRect();
    const menuRect = menu.getBoundingClientRect();
    const spaceBelow = window.innerHeight - triggerRect.bottom - viewportPadding;
    const top =
      spaceBelow >= menuRect.height + gap
        ? triggerRect.bottom + gap
        : Math.max(viewportPadding, triggerRect.top - menuRect.height - gap);
    const left = Math.min(
      window.innerWidth - menuRect.width - viewportPadding,
      Math.max(viewportPadding, triggerRect.right - menuRect.width),
    );
    setOverflowPosition({ left, top });
  }, []);

  useLayoutEffect(() => {
    if (!overflowOpen) return;

    const triggerElement = overflowTriggerRef.current;
    positionOverflowMenu();
    overflowMenuRef.current
      ?.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')
      ?.focus({ preventScroll: true });

    window.addEventListener("resize", positionOverflowMenu);
    return () => {
      window.removeEventListener("resize", positionOverflowMenu);
      // However the menu closed — Escape, activating an item, an outside click
      // — focus must come back to the trigger instead of being dropped on
      // <body> when the portal unmounts.
      const active = document.activeElement;
      if (active && active !== document.body) return;
      triggerElement?.focus({ preventScroll: true });
    };
  }, [overflowOpen, positionOverflowMenu]);

  useEffect(() => {
    if (!overflowOpen) return;

    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (
        !overflowMenuRef.current?.contains(target) &&
        !overflowTriggerRef.current?.contains(target)
      ) {
        closeOverflowMenu();
      }
    };
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      closeOverflowMenu();
      overflowTriggerRef.current?.focus({ preventScroll: true });
    };
    const handleFocusIn = (event: FocusEvent) => {
      const target = event.target as Node;
      if (
        !overflowMenuRef.current?.contains(target) &&
        !overflowTriggerRef.current?.contains(target)
      ) {
        closeOverflowMenu();
      }
    };
    // Stay anchored to the trigger while the page scrolls rather than
    // dismissing on every wheel nudge. One measurement per frame at most.
    const handleScroll = (event: Event) => {
      const target = event.target;
      if (target instanceof Node && overflowMenuRef.current?.contains(target)) return;
      if (scrollFrameRef.current !== null) return;
      scrollFrameRef.current = requestAnimationFrame(() => {
        scrollFrameRef.current = null;
        positionOverflowMenu();
      });
    };

    document.addEventListener("pointerdown", handlePointerDown, true);
    document.addEventListener("keydown", handleKeyDown);
    document.addEventListener("focusin", handleFocusIn);
    window.addEventListener("scroll", handleScroll, true);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown, true);
      document.removeEventListener("keydown", handleKeyDown);
      document.removeEventListener("focusin", handleFocusIn);
      window.removeEventListener("scroll", handleScroll, true);
      if (scrollFrameRef.current !== null) {
        cancelAnimationFrame(scrollFrameRef.current);
        scrollFrameRef.current = null;
      }
    };
  }, [closeOverflowMenu, overflowOpen, positionOverflowMenu]);
  const handleOverflowKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const isNavigationKey = ["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key);
    // Printable single characters jump to the next item whose label starts with
    // what has been typed, the way the menu behaved before.
    const isTypeahead =
      !isNavigationKey &&
      event.key.length === 1 &&
      !event.ctrlKey &&
      !event.metaKey &&
      !event.altKey &&
      event.key !== " ";
    if (!isNavigationKey && !isTypeahead) return;

    const items = Array.from(
      event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)'),
    );
    if (items.length === 0) return;

    event.preventDefault();
    const currentIndex = items.indexOf(document.activeElement as HTMLButtonElement);

    if (isTypeahead) {
      const now = Date.now();
      const typeahead = typeaheadRef.current;
      typeahead.query = now - typeahead.at > 1000 ? event.key : typeahead.query + event.key;
      typeahead.at = now;
      const query = typeahead.query.toLowerCase();
      // A repeated single character cycles through the items starting with it.
      const startIndex = query.length === 1 ? currentIndex + 1 : Math.max(currentIndex, 0);
      const match = items
        .map((_, offset) => items[(startIndex + offset) % items.length])
        .find((item) => (item?.textContent ?? "").trim().toLowerCase().startsWith(query));
      match?.focus({ preventScroll: true });
      return;
    }

    let nextIndex: number;
    if (event.key === "Home") {
      nextIndex = 0;
    } else if (event.key === "End") {
      nextIndex = items.length - 1;
    } else if (event.key === "ArrowUp") {
      nextIndex = currentIndex <= 0 ? items.length - 1 : currentIndex - 1;
    } else {
      nextIndex = currentIndex < 0 || currentIndex === items.length - 1 ? 0 : currentIndex + 1;
    }
    items[nextIndex]?.focus({ preventScroll: true });
  };
  const hasOverflowActions = Boolean(
    restartHref || onToggleWatchlist || onDownload || onSearchSubtitles,
  );
  const hasAdminActions = Boolean(isAdmin && (contentId || onRedetectIntro));
  const hasMetadataActions = Boolean(
    (canCurateMetadata && (onRefresh || onEditMetadata || onMatchItem || onShowMediaInfo)) ||
    showMarkerEditor,
  );
  const hasOverflowMenuItems =
    hasOverflowActions || hasAdminActions || hasMetadataActions || Boolean(contentId);

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

  // The subtitle control is available even when the selected file has no
  // embedded tracks because downloaded subtitles are loaded on demand.
  const hasStreamControls = Boolean(selectedVersion);

  return (
    <div className="detail-action-bar space-y-2.5">
      {/* ── Primary actions ──────────────────────────────────── */}
      <div className="flex flex-wrap items-center gap-3">
        {/* ── Play button ────────────────────────────────────── */}
        {playHref ? (
          showPlayChoiceDialog ? (
            <Button
              onClick={openPlayChoiceDialog}
              className={`${responsivePlayActionClass} relative h-11 cursor-pointer gap-2.5 overflow-hidden rounded-full px-8 text-[15px] font-bold tracking-wide shadow-md`}
            >
              <Play className="size-[18px] fill-current" />
              {displayedPlayLabel}
              {progressOverlay}
            </Button>
          ) : selectedVersion ? (
            <Button
              onClick={() => handleSelectedVersionPlay(false)}
              className={`${responsivePlayActionClass} relative h-11 cursor-pointer gap-2.5 overflow-hidden rounded-full px-8 text-[15px] font-bold tracking-wide shadow-md`}
            >
              <Play className="size-[18px] fill-current" />
              {displayedPlayLabel}
              {progressOverlay}
            </Button>
          ) : (
            <Button
              onClick={() => startPlaybackFromHref(playHref)}
              className={`${responsivePlayActionClass} relative h-11 cursor-pointer gap-2.5 overflow-hidden rounded-full px-8 text-[15px] font-bold tracking-wide shadow-md`}
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
            className={`${responsivePrimaryActionClass} h-11 min-w-[161px] rounded-full px-5 text-[14px] font-semibold enabled:cursor-pointer`}
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
            aria-label={isFavorite ? "Remove from favorites" : "Add to favorites"}
            className={`${staticGlassActionClass} size-11 cursor-pointer rounded-full`}
          >
            <Heart
              className={`size-[18px] transition-colors ${isFavorite ? "fill-current text-red-400" : ""}`}
            />
          </Button>
        )}

        {onRatingChange && (
          <StarRating value={rating ?? null} onChange={onRatingChange} size={18} />
        )}

        {hasOverflowMenuItems && (
          <Button
            ref={overflowTriggerRef}
            variant="glass"
            size="icon-lg"
            title="More"
            aria-label="More actions"
            aria-haspopup="menu"
            aria-expanded={overflowOpen}
            aria-controls={overflowOpen ? overflowMenuId : undefined}
            className={`${staticGlassActionClass} size-11 cursor-pointer rounded-full`}
            onClick={toggleOverflowMenu}
          >
            <MoreVertical className="size-[18px]" />
          </Button>
        )}
        {hasOverflowMenuItems &&
          overflowOpen &&
          createPortal(
            <div
              id={overflowMenuId}
              ref={overflowMenuRef}
              style={{
                left: overflowPosition?.left ?? 0,
                top: overflowPosition?.top ?? 0,
                visibility: overflowPosition ? "visible" : "hidden",
              }}
              className="detail-overflow-menu border-border bg-popover text-popover-foreground fixed z-50 max-h-[calc(100vh-1rem)] w-max max-w-[calc(100vw-2rem)] min-w-0 overflow-y-auto rounded-md border p-1 shadow-md"
              role="menu"
              onKeyDown={handleOverflowKeyDown}
            >
              {restartHref && (
                <DetailOverflowMenuItem
                  closeMenu={closeOverflowMenu}
                  onAction={handleRestartPlayback}
                >
                  <RotateCcw className="size-4" />
                  Play from Beginning
                </DetailOverflowMenuItem>
              )}
              {onToggleWatchlist && (
                <DetailOverflowMenuItem closeMenu={closeOverflowMenu} onAction={onToggleWatchlist}>
                  {inWatchlist ? <Check className="size-4" /> : <Plus className="size-4" />}
                  {inWatchlist ? "Remove from Watchlist" : "Add to Watchlist"}
                </DetailOverflowMenuItem>
              )}
              {contentId && (
                <DetailOverflowMenuItem
                  closeMenu={closeOverflowMenu}
                  onAction={() => setAddToCollectionOpen(true)}
                >
                  <FolderPlus className="size-4" />
                  Add to Collection
                </DetailOverflowMenuItem>
              )}
              {onDownload && (
                <DetailOverflowMenuItem closeMenu={closeOverflowMenu} onAction={onDownload}>
                  <Download className="size-4" />
                  Download
                </DetailOverflowMenuItem>
              )}
              {onSearchSubtitles && (
                <DetailOverflowMenuItem closeMenu={closeOverflowMenu} onAction={onSearchSubtitles}>
                  <Captions className="size-4" />
                  Search Subtitles
                </DetailOverflowMenuItem>
              )}
              {(hasAdminActions || hasMetadataActions) && (
                <>
                  {hasOverflowActions && (
                    <div role="separator" className="bg-border -mx-1 my-1 h-px" />
                  )}
                  {canCurateMetadata && onShowMediaInfo && (
                    <DetailOverflowMenuItem
                      closeMenu={closeOverflowMenu}
                      onAction={onShowMediaInfo}
                    >
                      <Info className="size-4" />
                      Media Info
                    </DetailOverflowMenuItem>
                  )}
                  {isAdmin && contentId && (
                    <DetailOverflowMenuItem
                      closeMenu={closeOverflowMenu}
                      onAction={() =>
                        navigate(`/admin/history?media_item_id=${encodeURIComponent(contentId)}`)
                      }
                    >
                      <MediaActionIcon action="viewPlayHistory" />
                      View Play History
                    </DetailOverflowMenuItem>
                  )}
                  {canCurateMetadata && onRefresh && (
                    <DetailOverflowMenuItem
                      closeMenu={closeOverflowMenu}
                      disabled={isRefreshing}
                      onAction={() => {
                        setRefreshDialogOpen(true);
                      }}
                    >
                      <MediaActionIcon action="refreshMetadata" isPending={isRefreshing} />
                      Refresh Metadata
                    </DetailOverflowMenuItem>
                  )}
                  {isAdmin && onRedetectIntro && (
                    <DetailOverflowMenuItem
                      closeMenu={closeOverflowMenu}
                      disabled={isRedetectingIntro}
                      onAction={onRedetectIntro}
                    >
                      <RefreshCw className={`size-4 ${isRedetectingIntro ? "animate-spin" : ""}`} />
                      Re-detect Intro Markers
                    </DetailOverflowMenuItem>
                  )}
                  {canCurateMetadata && onEditMetadata && (
                    <DetailOverflowMenuItem closeMenu={closeOverflowMenu} onAction={onEditMetadata}>
                      <MediaActionIcon action="editMetadata" />
                      Edit Metadata
                    </DetailOverflowMenuItem>
                  )}
                  {showMarkerEditor && (
                    <DetailOverflowMenuItem
                      closeMenu={closeOverflowMenu}
                      onAction={() => setMarkerEditorOpen(true)}
                    >
                      <Tags className="size-4" />
                      Edit Markers
                    </DetailOverflowMenuItem>
                  )}
                  {canCurateMetadata && onMatchItem && (
                    <DetailOverflowMenuItem closeMenu={closeOverflowMenu} onAction={onMatchItem}>
                      <MediaActionIcon action="matchItem" />
                      Match Item
                    </DetailOverflowMenuItem>
                  )}
                  {canCurateMetadata && onSplitItem && (
                    <DetailOverflowMenuItem closeMenu={closeOverflowMenu} onAction={onSplitItem}>
                      <Scissors className="size-4" />
                      Split Versions
                    </DetailOverflowMenuItem>
                  )}
                </>
              )}
            </div>,
            document.body,
          )}
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
        {contentId && (
          <AddToCollectionDialog
            open={addToCollectionOpen}
            onOpenChange={setAddToCollectionOpen}
            mediaItemId={contentId}
          />
        )}
      </div>

      {/* ── Stream info controls (second row) ──────────────── */}
      {/* flex-wrap matters: without it this row's min-content width (two or
          three nowrap trigger buttons) inflates the auto-sized hero column
          past narrow viewports, clipping the whole info column. */}
      {hasStreamControls && (
        <div className="flex min-w-0 flex-wrap items-center gap-2">
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
