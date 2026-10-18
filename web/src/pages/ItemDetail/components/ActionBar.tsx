import {
  type ComponentProps,
  Fragment,
  type FocusEvent as ReactFocusEvent,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
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
  type LucideIcon,
  Heart,
  Plus,
  Check,
  ChevronRight,
  Download,
  ExternalLink,
  FolderPlus,
  GalleryHorizontal,
  Inbox,
  Info,
  Loader2,
  MoreVertical,
  Play,
  RefreshCw,
  Scissors,
  RotateCcw,
  Shuffle,
  Tags,
  TimerReset,
  ThumbsDown,
  UsersRound,
  Hand,
  Wrench,
  Zap,
} from "lucide-react";
import { cn } from "@/lib/utils";
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
import type { RedetectMarkersKind, RefreshItemMetadataMode } from "@/hooks/queries/items";
import type {
  PlayerSubtitleTrackSignature,
  PrePlaySubtitleSelection,
  SubtitleMode,
} from "@/player/types";
import RefreshMetadataDialog from "@/components/RefreshMetadataDialog";
import { MarkerEditor } from "@/components/markers/MarkerEditor";
import RedetectMarkersDialog from "@/components/markers/RedetectMarkersDialog";
import { TrickplayStatusDialog } from "@/components/admin/trickplay/TrickplayStatusDialog";
import StarRating from "@/components/StarRating";
import { MediaActionIcon } from "@/components/mediaActionIcons";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { parseWatchHref } from "@/pages/watchRouteHelpers";
import type { TitleLink } from "@/lib/titleLinks";
import VersionDropdown from "./VersionDropdown";
import AudioTracksPopover from "./AudioTracksPopover";
import SubtitlesPopover from "./SubtitlesPopover";

// Keep hover feedback on the compositor. Repainting these controls while the detail backdrop is
// animating can stall the main thread on image-heavy movie and series pages.
const responsivePrimaryActionClass =
  "transform-gpu transition-transform duration-150 motion-safe:hover:scale-[1.02] motion-safe:active:scale-[0.98]";
const responsivePlayActionClass = `${responsivePrimaryActionClass} hover:bg-primary motion-reduce:hover:bg-primary/90`;
const staticGlassActionClass = "transition-none";
const overflowMenuClass =
  "detail-overflow-menu border-border bg-popover text-popover-foreground fixed z-50 max-h-[calc(100vh-1rem)] w-max max-w-[calc(100vw-2rem)] min-w-0 overflow-y-auto rounded-md border p-1 shadow-md";

function visibleOverflowItems(menu: HTMLElement | null): HTMLButtonElement[] {
  if (!menu) return [];
  return Array.from(
    menu.querySelectorAll<HTMLButtonElement>(
      '[role="menuitem"]:not(:disabled), [role="radio"][tabindex="0"]:not(:disabled)',
    ),
  ).filter(
    (item) => item.getClientRects().length > 0 && getComputedStyle(item).visibility === "visible",
  );
}

function DetailOverflowSeparator() {
  return <div role="separator" className="bg-border -mx-1 my-1 h-px" />;
}

/** Renders the non-empty groups with a separator between each. */
function DetailOverflowGroups({ groups }: { groups: ReactNode[][] }) {
  return groups
    .map((group) => group.filter(Boolean))
    .filter((group) => group.length > 0)
    .map((group, index) => (
      <Fragment key={index}>
        {index > 0 && <DetailOverflowSeparator />}
        {group}
      </Fragment>
    ));
}

function DetailOverflowMenuItem({
  className,
  closeMenu,
  onAction,
  ...props
}: Omit<ComponentProps<"button">, "onClick"> & {
  /** Omitted by items that keep the menu open, such as Manage. */
  closeMenu?: () => void;
  onAction?: () => void;
}) {
  return (
    <button
      {...props}
      type="button"
      role="menuitem"
      className={`focus:bg-accent focus:text-accent-foreground hover:bg-accent hover:text-accent-foreground [&_svg:not([class*='text-'])]:text-muted-foreground relative flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm outline-hidden select-none disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 ${className ?? ""}`}
      onClick={() => {
        closeMenu?.();
        onAction?.();
      }}
    />
  );
}

/** Drawn like the Play pill in each of its states: enabled, disabled, or in flight. */
function PrimaryActionButton({ action }: { action: ActionBarPrimaryAction }) {
  const inert = Boolean(action.disabled || action.pending);
  const variant = action.variant ?? "default";
  const Icon = action.pending ? Loader2 : action.icon;
  const interactiveClass = inert
    ? ""
    : `${variant === "default" ? responsivePlayActionClass : responsivePrimaryActionClass} cursor-pointer shadow-md`;

  return (
    <Button
      variant={variant}
      onClick={action.onClick}
      disabled={inert}
      aria-busy={action.pending || undefined}
      className={`${interactiveClass} relative h-11 gap-2.5 overflow-hidden rounded-full px-8 text-[0.9375rem] font-bold tracking-wide`}
    >
      {Icon && (
        <Icon
          aria-hidden="true"
          className={`size-[18px] ${action.pending ? "animate-spin" : ""}`}
        />
      )}
      {action.label}
    </Button>
  );
}

/** The Watch Together group in the overflow menu. */
export interface ActionBarWatchTogether {
  onStartParty: () => void;
  /** Present when this browser is in a live room. */
  liveRoom?: { code: string; onSuggest: () => void; onPlay?: () => void };
}

/**
 * Takes the Play pill's place on a title that cannot be played here, such as
 * one not in the library yet. A state the viewer cannot act on ("Requested",
 * "In the library") is a disabled primary action rather than a badge.
 */
export interface ActionBarPrimaryAction {
  label: string;
  icon?: LucideIcon;
  onClick?: () => void;
  /** Shows a spinner and blocks the action while it is in flight. */
  pending?: boolean;
  disabled?: boolean;
  /** Defaults to the Play pill's styling. */
  variant?: "default" | "glass";
}

/** A labelled glass pill after the primary action. */
export interface ActionBarSecondaryAction {
  /** Stable key, since a toggle's label changes with its state. */
  id: string;
  label: string;
  icon?: LucideIcon;
  onClick: () => void;
  pending?: boolean;
  /** Set on a toggle, such as following a title. */
  pressed?: boolean;
}

export interface ActionBarProps {
  compactMobile?: boolean;
  contentId?: string;
  canAddToCollection?: boolean;
  /** The item's title, named by Add to collection. */
  itemTitle?: string;
  watchTogether?: ActionBarWatchTogether;
  /** Replaces the Play action. */
  primaryAction?: ActionBarPrimaryAction;
  secondaryActions?: ActionBarSecondaryAction[];
  /** The title's pages elsewhere, such as IMDb or TMDB. */
  links?: TitleLink[];
  playHref?: string;
  playLabel?: string;
  playProgress?: number;
  restartHref?: string;
  /** Clears the resume position without marking the item watched. */
  onResetProgress?: () => void;
  resumePositionSeconds?: number;
  resumeDurationSeconds?: number;
  resumeResolution?: string;
  resumeHdr?: boolean;
  effectiveVersionResolution?: string;
  effectiveVersionHdr?: boolean;
  watchedLabel?: string;
  isWatched?: boolean;
  onToggleWatched?: () => void;
  isUpdatingWatched?: boolean;
  onToggleFavorite?: () => void;
  isFavorite?: boolean;
  onToggleWatchlist?: () => void;
  inWatchlist?: boolean;
  /** Marks or clears Not interested; movies and series only. */
  onToggleNotInterested?: () => void;
  notInterested?: boolean;
  onRefresh?: (mode: RefreshItemMetadataMode) => void;
  isRefreshing?: boolean;
  /** Re-detects local markers; without redetectKind the admin picks intro, credits, or both. */
  onRedetectMarkers?: (kind: RedetectMarkersKind) => void;
  /**
   * Re-detects this one kind directly, without the picker: credits for a movie,
   * which has no intro, or intro for an episode on a server without redetect-markers.
   */
  redetectKind?: "intro" | "credits";
  isRedetectingMarkers?: boolean;
  onEditMetadata?: () => void;
  onMatchItem?: () => void;
  onSplitItem?: () => void;
  onShowMediaInfo?: () => void;
  isAdmin?: boolean;
  canCurateMetadata?: boolean;
  /** Enables the "Edit Markers" action (playable items only: movies/episodes). */
  canEditMarkers?: boolean;
  /** Enables the admin "Seek Previews" action; the server must offer seek previews. */
  canManageTrickplay?: boolean;
  versions?: FileVersion[];
  playbackVariants?: PlaybackVariant[];
  selectedVersion?: FileVersion | null;
  onSelectVersion?: (version: FileVersion) => void;
  onDownload?: () => void;
  onSearchSubtitles?: () => void;
  /** Opens the season picker to request seasons the library is missing. */
  onRequestSeasons?: () => void;
  /** Shuffles this series' or season's episodes. */
  onShuffle?: () => void;
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
  compactMobile = false,
  contentId,
  canAddToCollection = true,
  itemTitle,
  watchTogether,
  primaryAction,
  secondaryActions,
  links,
  playHref,
  playLabel = "Play",
  playProgress,
  restartHref,
  onResetProgress,
  resumePositionSeconds,
  resumeDurationSeconds,
  watchedLabel,
  isWatched,
  onToggleWatched,
  isUpdatingWatched = false,
  onToggleFavorite,
  isFavorite = false,
  onToggleWatchlist,
  inWatchlist = false,
  onToggleNotInterested,
  notInterested = false,
  onRefresh,
  isRefreshing = false,
  onRedetectMarkers,
  redetectKind,
  isRedetectingMarkers = false,
  onEditMetadata,
  onMatchItem,
  onSplitItem,
  onShowMediaInfo,
  isAdmin = false,
  canCurateMetadata = false,
  canEditMarkers = false,
  canManageTrickplay = false,
  versions,
  playbackVariants,
  selectedVersion,
  onSelectVersion,
  onDownload,
  onSearchSubtitles,
  onRequestSeasons,
  onShuffle,
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
  // The admin and curation actions open in a second menu beside this one.
  const [manageOpen, setManageOpen] = useState(false);
  const [managePosition, setManagePosition] = useState<{ left: number; top: number } | null>(null);
  const manageMenuId = useId();
  const manageTriggerRef = useRef<HTMLButtonElement>(null);
  const manageMenuRef = useRef<HTMLDivElement>(null);
  const focusManageOnOpenRef = useRef(false);
  const overflowMenuId = useId();
  const [overflowPosition, setOverflowPosition] = useState<{ left: number; top: number } | null>(
    null,
  );
  const overflowTriggerRef = useRef<HTMLButtonElement>(null);
  const overflowMenuRef = useRef<HTMLDivElement>(null);
  const scrollFrameRef = useRef<number | null>(null);
  const typeaheadRef = useRef<{ query: string; at: number }>({ query: "", at: 0 });
  const [refreshDialogOpen, setRefreshDialogOpen] = useState(false);
  const [redetectDialogOpen, setRedetectDialogOpen] = useState(false);
  const [addToCollectionOpen, setAddToCollectionOpen] = useState(false);
  const [markerEditorOpen, setMarkerEditorOpen] = useState(false);
  const [trickplayOpen, setTrickplayOpen] = useState(false);
  const showTrickplay = isAdmin && canManageTrickplay && !!contentId;
  const showMarkerEditor = canEditMarkers && !!contentId;
  const hasMultipleVersions = (playbackVariants?.length ?? 0) > 1 || (versions?.length ?? 0) > 1;
  const showPlayChoiceDialog =
    !hasMultipleVersions && playLabel === "Resume" && !!playHref && !!restartHref;
  const displayedPlayLabel = showPlayChoiceDialog ? "Play" : playLabel;
  const playText = compactMobile ? (
    <>
      <span className="detail-full-label">{displayedPlayLabel}</span>
      <span className="detail-short-label">
        {displayedPlayLabel === "Resume" ? "Resume" : "Play"}
      </span>
    </>
  ) : (
    displayedPlayLabel
  );

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
        "viewer",
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
  const handleRedetectConfirm = (kind: RedetectMarkersKind) => {
    setRedetectDialogOpen(false);
    onRedetectMarkers?.(kind);
  };
  const closeOverflowMenu = useCallback(() => setOverflowOpen(false), []);
  const toggleOverflowMenu = useCallback(() => {
    setOverflowPosition(null);
    setManageOpen(false);
    setOverflowOpen((open) => !open);
  }, []);
  /** Pointer hover opens Manage in place; the keyboard and clicks also move focus into it. */
  const openManageMenu = useCallback(
    (focusFirst: boolean) => {
      if (manageOpen) {
        if (focusFirst) {
          visibleOverflowItems(manageMenuRef.current)[0]?.focus({ preventScroll: true });
        }
        return;
      }
      focusManageOnOpenRef.current = focusFirst;
      setManagePosition(null);
      setManageOpen(true);
    },
    [manageOpen],
  );
  const closeManageMenu = useCallback((returnFocus: boolean) => {
    setManageOpen(false);
    if (returnFocus) manageTriggerRef.current?.focus({ preventScroll: true });
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
  const positionManageMenu = useCallback(() => {
    const main = overflowMenuRef.current;
    const trigger = manageTriggerRef.current;
    const menu = manageMenuRef.current;
    if (!main || !trigger || !menu) return;

    const viewportPadding = 8;
    const gap = 4;
    const mainRect = main.getBoundingClientRect();
    const triggerRect = trigger.getBoundingClientRect();
    const menuRect = menu.getBoundingClientRect();
    const clampTop = (top: number) =>
      Math.min(
        window.innerHeight - menuRect.height - viewportPadding,
        Math.max(viewportPadding, top),
      );
    // Line the first item up with Manage: the menu's border and padding sit above it.
    const besideTop = clampTop(triggerRect.top - 5);
    if (mainRect.right + gap + menuRect.width <= window.innerWidth - viewportPadding) {
      setManagePosition({ left: mainRect.right + gap, top: besideTop });
    } else if (mainRect.left - gap - menuRect.width >= viewportPadding) {
      setManagePosition({ left: mainRect.left - gap - menuRect.width, top: besideTop });
    } else {
      // No room beside it on a narrow screen: cascade over the main menu, under Manage.
      setManagePosition({
        left: Math.min(
          window.innerWidth - menuRect.width - viewportPadding,
          Math.max(viewportPadding, mainRect.left + 24),
        ),
        top: clampTop(triggerRect.bottom),
      });
    }
  }, []);

  useLayoutEffect(() => {
    if (!overflowOpen) return;

    const triggerElement = overflowTriggerRef.current;
    positionOverflowMenu();

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

  // The portal starts hidden until its position is measured. Focus only once
  // it is painted, and skip actions hidden by the responsive layout.
  const overflowPositioned = overflowPosition !== null;
  useLayoutEffect(() => {
    if (!overflowOpen || !overflowPositioned) return;
    visibleOverflowItems(overflowMenuRef.current)[0]?.focus({ preventScroll: true });
  }, [overflowOpen, overflowPositioned]);

  // Manage follows the main menu as it is placed, resized, or scrolled.
  useLayoutEffect(() => {
    if (overflowOpen && manageOpen) positionManageMenu();
  }, [manageOpen, overflowOpen, overflowPosition, positionManageMenu]);
  const managePositioned = managePosition !== null;
  useLayoutEffect(() => {
    if (!manageOpen || !managePositioned || !focusManageOnOpenRef.current) return;
    focusManageOnOpenRef.current = false;
    visibleOverflowItems(manageMenuRef.current)[0]?.focus({ preventScroll: true });
  }, [manageOpen, managePositioned]);

  useEffect(() => {
    if (!overflowOpen) return;

    const isInsideMenus = (target: Node) =>
      Boolean(
        overflowMenuRef.current?.contains(target) ||
        manageMenuRef.current?.contains(target) ||
        overflowTriggerRef.current?.contains(target),
      );
    const handlePointerDown = (event: PointerEvent) => {
      if (!isInsideMenus(event.target as Node)) closeOverflowMenu();
    };
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      if (manageOpen) {
        closeManageMenu(true);
        return;
      }
      closeOverflowMenu();
      overflowTriggerRef.current?.focus({ preventScroll: true });
    };
    const handleFocusIn = (event: FocusEvent) => {
      if (!isInsideMenus(event.target as Node)) closeOverflowMenu();
    };
    // Stay anchored to the trigger while the page scrolls rather than
    // dismissing on every wheel nudge. One measurement per frame at most.
    const handleScroll = (event: Event) => {
      const target = event.target;
      if (target instanceof Node && isInsideMenus(target)) {
        // Scrolling the main menu moves Manage's trigger. Placing the main menu
        // again places Manage after it.
        const movesManageTrigger = manageOpen && overflowMenuRef.current?.contains(target);
        if (!movesManageTrigger) return;
      }
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
  }, [closeManageMenu, closeOverflowMenu, manageOpen, overflowOpen, positionOverflowMenu]);
  const handleOverflowKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "ArrowRight" && event.target === manageTriggerRef.current) {
      event.preventDefault();
      openManageMenu(true);
      return;
    }
    if (event.key === "ArrowLeft" && event.currentTarget === manageMenuRef.current) {
      event.preventDefault();
      closeManageMenu(true);
      return;
    }
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

    const items = visibleOverflowItems(event.currentTarget);
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
  // With one version, Play already asks whether to resume or start over.
  const showRestartInMenu = Boolean(restartHref) && !showPlayChoiceDialog;
  const playbackItems = [
    onShuffle && (
      <DetailOverflowMenuItem key="shuffle" closeMenu={closeOverflowMenu} onAction={onShuffle}>
        <Shuffle className="size-4" />
        Shuffle
      </DetailOverflowMenuItem>
    ),
    showRestartInMenu && (
      <DetailOverflowMenuItem
        key="restart"
        closeMenu={closeOverflowMenu}
        onAction={handleRestartPlayback}
      >
        <RotateCcw className="size-4" />
        Play from Beginning
      </DetailOverflowMenuItem>
    ),
    onResetProgress && (
      <DetailOverflowMenuItem key="reset" closeMenu={closeOverflowMenu} onAction={onResetProgress}>
        <TimerReset className="size-4" />
        Reset Progress
      </DetailOverflowMenuItem>
    ),
  ];
  const libraryItems = [
    onToggleWatchlist && (
      <DetailOverflowMenuItem
        key="watchlist"
        closeMenu={closeOverflowMenu}
        onAction={onToggleWatchlist}
      >
        {inWatchlist ? <Check className="size-4" /> : <Plus className="size-4" />}
        {inWatchlist ? "Remove from Watchlist" : "Add to Watchlist"}
      </DetailOverflowMenuItem>
    ),
    contentId && canAddToCollection && (
      <DetailOverflowMenuItem
        key="collection"
        closeMenu={closeOverflowMenu}
        onAction={() => setAddToCollectionOpen(true)}
      >
        <FolderPlus className="size-4" />
        Add to Collection
      </DetailOverflowMenuItem>
    ),
    onDownload && (
      <DetailOverflowMenuItem key="download" closeMenu={closeOverflowMenu} onAction={onDownload}>
        <Download className="size-4" />
        Download
      </DetailOverflowMenuItem>
    ),
    onRequestSeasons && (
      <DetailOverflowMenuItem
        key="request-seasons"
        closeMenu={closeOverflowMenu}
        onAction={onRequestSeasons}
      >
        <Inbox className="size-4" />
        Request Seasons
      </DetailOverflowMenuItem>
    ),
    onToggleNotInterested && (
      <DetailOverflowMenuItem
        key="not-interested"
        closeMenu={closeOverflowMenu}
        onAction={onToggleNotInterested}
      >
        <ThumbsDown className={cn("size-4", notInterested && "fill-current")} />
        {notInterested ? "Show in Recommendations" : "Not Interested"}
      </DetailOverflowMenuItem>
    ),
  ];
  const partyItems = watchTogether
    ? [
        <DetailOverflowMenuItem
          key="start-party"
          closeMenu={closeOverflowMenu}
          onAction={watchTogether.onStartParty}
        >
          <UsersRound className="size-4" />
          Start a Watch Party
        </DetailOverflowMenuItem>,
        watchTogether.liveRoom && (
          <DetailOverflowMenuItem
            key="suggest-party"
            closeMenu={closeOverflowMenu}
            onAction={watchTogether.liveRoom.onSuggest}
          >
            <Hand className="size-4" />
            Suggest to {watchTogether.liveRoom.code}
          </DetailOverflowMenuItem>
        ),
        watchTogether.liveRoom?.onPlay && (
          <DetailOverflowMenuItem
            key="play-party"
            closeMenu={closeOverflowMenu}
            onAction={watchTogether.liveRoom.onPlay}
          >
            <Zap className="size-4" />
            Play in {watchTogether.liveRoom.code}
          </DetailOverflowMenuItem>
        ),
      ]
    : [];
  const manageGroups = [
    [
      canCurateMetadata && onEditMetadata && (
        <DetailOverflowMenuItem
          key="edit-metadata"
          closeMenu={closeOverflowMenu}
          onAction={onEditMetadata}
        >
          <MediaActionIcon action="editMetadata" />
          Edit Metadata
        </DetailOverflowMenuItem>
      ),
      canCurateMetadata && onMatchItem && (
        <DetailOverflowMenuItem key="match" closeMenu={closeOverflowMenu} onAction={onMatchItem}>
          <MediaActionIcon action="matchItem" />
          Match Item
        </DetailOverflowMenuItem>
      ),
      canCurateMetadata && onRefresh && (
        <DetailOverflowMenuItem
          key="refresh"
          closeMenu={closeOverflowMenu}
          disabled={isRefreshing}
          onAction={() => setRefreshDialogOpen(true)}
        >
          <MediaActionIcon action="refreshMetadata" isPending={isRefreshing} />
          Refresh Metadata
        </DetailOverflowMenuItem>
      ),
    ],
    [
      showMarkerEditor && (
        <DetailOverflowMenuItem
          key="edit-markers"
          closeMenu={closeOverflowMenu}
          onAction={() => setMarkerEditorOpen(true)}
        >
          <Tags className="size-4" />
          Edit Markers
        </DetailOverflowMenuItem>
      ),
      isAdmin && onRedetectMarkers && (
        <DetailOverflowMenuItem
          key="redetect"
          closeMenu={closeOverflowMenu}
          disabled={isRedetectingMarkers}
          onAction={() =>
            redetectKind ? onRedetectMarkers(redetectKind) : setRedetectDialogOpen(true)
          }
        >
          <RefreshCw className={`size-4 ${isRedetectingMarkers ? "animate-spin" : ""}`} />
          {redetectKind === "credits"
            ? "Re-detect Credits"
            : redetectKind === "intro"
              ? "Re-detect Intro Markers"
              : "Re-detect Markers"}
        </DetailOverflowMenuItem>
      ),
      showTrickplay && (
        <DetailOverflowMenuItem
          key="trickplay"
          closeMenu={closeOverflowMenu}
          onAction={() => setTrickplayOpen(true)}
        >
          <GalleryHorizontal className="size-4" />
          Seek Previews
        </DetailOverflowMenuItem>
      ),
      canCurateMetadata && onSplitItem && (
        <DetailOverflowMenuItem key="split" closeMenu={closeOverflowMenu} onAction={onSplitItem}>
          <Scissors className="size-4" />
          Split Versions
        </DetailOverflowMenuItem>
      ),
    ],
    [
      canCurateMetadata && onShowMediaInfo && (
        <DetailOverflowMenuItem
          key="media-info"
          closeMenu={closeOverflowMenu}
          onAction={onShowMediaInfo}
        >
          <Info className="size-4" />
          Media Info
        </DetailOverflowMenuItem>
      ),
      isAdmin && contentId && (
        <DetailOverflowMenuItem
          key="play-history"
          closeMenu={closeOverflowMenu}
          onAction={() => navigate(`/admin/history?media_item_id=${encodeURIComponent(contentId)}`)}
        >
          <MediaActionIcon action="viewPlayHistory" />
          View Play History
        </DetailOverflowMenuItem>
      ),
    ],
  ];
  const manageItems = manageGroups.flat().filter(Boolean);
  // A single management action sits at the end of the menu instead of behind Manage.
  const hasManagePanel = manageItems.length > 1;
  const mainGroups = [
    playbackItems,
    libraryItems,
    partyItems,
    hasManagePanel
      ? [
          <DetailOverflowMenuItem
            key="manage"
            ref={manageTriggerRef}
            aria-haspopup="menu"
            aria-expanded={manageOpen}
            aria-controls={manageOpen ? manageMenuId : undefined}
            className="aria-expanded:bg-accent aria-expanded:text-accent-foreground"
            onAction={() => openManageMenu(true)}
          >
            <Wrench className="size-4" />
            Manage
            <ChevronRight className="ml-auto size-4" />
          </DetailOverflowMenuItem>,
        ]
      : manageItems,
  ];
  const hasMainMenuItems = mainGroups.some((group) => group.some(Boolean));
  const hasOverflowMenuItems =
    hasMainMenuItems || (compactMobile && Boolean(onToggleFavorite || onRatingChange));
  // Hovering another item, or moving focus to one, closes Manage again.
  const handleMainMenuPointerOver = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.pointerType !== "mouse" || !hasManagePanel) return;
    const item = (event.target as Element).closest('[role="menuitem"]');
    if (item === manageTriggerRef.current) openManageMenu(false);
    else if (item && manageOpen) closeManageMenu(false);
  };
  const handleMainMenuFocus = (event: ReactFocusEvent<HTMLDivElement>) => {
    if (manageOpen && (event.target as Element) !== manageTriggerRef.current) {
      closeManageMenu(false);
    }
  };

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
        "viewer",
      );
    },
    [buildPrePlayStartInput, contentId, currentHref, playbackController, selectedVersion],
  );

  // The subtitle control is available even when the selected file has no
  // embedded tracks because downloaded subtitles are loaded on demand.
  const hasStreamControls = Boolean(selectedVersion);

  return (
    <div className="detail-action-bar space-y-2.5" data-compact-mobile={compactMobile || undefined}>
      {/* ── Primary actions ──────────────────────────────────── */}
      <div className="detail-primary-actions flex flex-wrap items-center gap-3">
        {/* ── Play button ────────────────────────────────────── */}
        {primaryAction ? (
          <PrimaryActionButton action={primaryAction} />
        ) : playHref ? (
          showPlayChoiceDialog ? (
            <Button
              onClick={openPlayChoiceDialog}
              className={`${responsivePlayActionClass} relative h-11 cursor-pointer gap-2.5 overflow-hidden rounded-full px-8 text-[0.9375rem] font-bold tracking-wide shadow-md`}
            >
              <Play className="size-[18px] fill-current" />
              {playText}
              {progressOverlay}
            </Button>
          ) : selectedVersion ? (
            <Button
              onClick={() => handleSelectedVersionPlay(false)}
              className={`${responsivePlayActionClass} relative h-11 cursor-pointer gap-2.5 overflow-hidden rounded-full px-8 text-[0.9375rem] font-bold tracking-wide shadow-md`}
            >
              <Play className="size-[18px] fill-current" />
              {playText}
              {progressOverlay}
            </Button>
          ) : (
            <Button
              onClick={() => startPlaybackFromHref(playHref)}
              className={`${responsivePlayActionClass} relative h-11 cursor-pointer gap-2.5 overflow-hidden rounded-full px-8 text-[0.9375rem] font-bold tracking-wide shadow-md`}
            >
              <Play className="size-[18px] fill-current" />
              {playText}
              {progressOverlay}
            </Button>
          )
        ) : (
          <Button
            disabled
            className="h-11 gap-2.5 rounded-full px-8 text-[0.9375rem] font-bold tracking-wide"
          >
            <Play className="size-[18px] fill-current" />
            {playLabel}
          </Button>
        )}

        {secondaryActions?.map((action) => {
          const Icon = action.pending ? Loader2 : action.icon;
          return (
            <Button
              key={action.id}
              variant="glass"
              onClick={action.onClick}
              disabled={action.pending}
              aria-busy={action.pending || undefined}
              aria-pressed={action.pressed}
              className={`${responsivePrimaryActionClass} h-11 rounded-full px-5 text-[0.875rem] font-semibold enabled:cursor-pointer`}
            >
              {Icon && (
                <Icon
                  aria-hidden="true"
                  className={`size-[18px] ${action.pending ? "animate-spin" : ""}`}
                />
              )}
              {action.label}
            </Button>
          );
        })}

        {/* ── Watched toggle ─────────────────────────────────── */}
        {watchedLabel && onToggleWatched && (
          <Button
            variant="glass"
            onClick={onToggleWatched}
            disabled={isUpdatingWatched}
            className={`${responsivePrimaryActionClass} h-11 min-w-[161px] rounded-full px-5 text-[0.875rem] font-semibold enabled:cursor-pointer`}
          >
            <Check className="size-[18px]" />
            {compactMobile ? (
              <>
                <span className="detail-full-label">{watchedLabel}</span>
                <span className="detail-short-label">
                  {isWatched ? "Mark Unwatched" : "Mark Watched"}
                </span>
              </>
            ) : (
              watchedLabel
            )}
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
            className={`${staticGlassActionClass} detail-secondary-action size-11 cursor-pointer rounded-full`}
          >
            <Heart
              className={`size-[18px] transition-colors ${isFavorite ? "fill-current text-red-400" : ""}`}
            />
          </Button>
        )}

        {onRatingChange && (
          <div className="detail-secondary-action">
            <StarRating value={rating ?? null} onChange={onRatingChange} size={18} />
          </div>
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
              className={overflowMenuClass}
              role="menu"
              onKeyDown={handleOverflowKeyDown}
              onPointerOver={handleMainMenuPointerOver}
              onFocus={handleMainMenuFocus}
            >
              {compactMobile && (
                <div className="detail-mobile-menu-actions">
                  {onToggleFavorite && (
                    <DetailOverflowMenuItem
                      closeMenu={closeOverflowMenu}
                      onAction={onToggleFavorite}
                    >
                      <Heart className="size-4" />
                      {isFavorite ? "Remove from favorites" : "Add to favorites"}
                    </DetailOverflowMenuItem>
                  )}
                  {onRatingChange && (
                    <div className="px-2 py-2" role="group" aria-label="Your rating">
                      <span className="mb-2 block text-sm">Your rating</span>
                      {/* ArrowUp/ArrowDown keep moving through the menu; only
                              ArrowLeft/ArrowRight change the rating here. */}
                      <StarRating
                        value={rating ?? null}
                        onChange={onRatingChange}
                        size={18}
                        verticalArrows={false}
                      />
                    </div>
                  )}
                  {hasMainMenuItems && <DetailOverflowSeparator />}
                </div>
              )}
              <DetailOverflowGroups groups={mainGroups} />
            </div>,
            document.body,
          )}
        {hasOverflowMenuItems &&
          overflowOpen &&
          hasManagePanel &&
          manageOpen &&
          createPortal(
            <div
              id={manageMenuId}
              ref={manageMenuRef}
              style={{
                left: managePosition?.left ?? 0,
                top: managePosition?.top ?? 0,
                visibility: managePosition ? "visible" : "hidden",
              }}
              className={overflowMenuClass}
              role="menu"
              aria-label="Manage"
              onKeyDown={handleOverflowKeyDown}
            >
              <DetailOverflowGroups groups={manageGroups} />
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
        {showTrickplay && contentId && (
          <TrickplayStatusDialog
            open={trickplayOpen}
            onOpenChange={setTrickplayOpen}
            itemId={contentId}
            versions={versions}
          />
        )}
        {isAdmin && onRedetectMarkers && !redetectKind && (
          <RedetectMarkersDialog
            open={redetectDialogOpen}
            onOpenChange={setRedetectDialogOpen}
            onConfirm={handleRedetectConfirm}
            isPending={isRedetectingMarkers}
          />
        )}
        {contentId && canAddToCollection && (
          <AddToCollectionDialog
            open={addToCollectionOpen}
            onOpenChange={setAddToCollectionOpen}
            mediaItemId={contentId}
            itemTitle={itemTitle}
          />
        )}
      </div>

      {/* ── Stream info controls (second row) ──────────────── */}
      {/* flex-wrap matters: without it this row's min-content width (two or
          three nowrap trigger buttons) inflates the auto-sized hero column
          past narrow viewports, clipping the whole info column. */}
      {hasStreamControls && (
        <div className="detail-stream-actions flex min-w-0 flex-wrap items-center gap-2">
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
              onSearchSubtitles={onSearchSubtitles}
            />
          )}
        </div>
      )}

      {/* ── The title on IMDb, TMDB and TVDB ─────────────────── */}
      {/* A row of its own, so the links never push the library actions
          (watched, favorite, rating, more) onto another line. */}
      {links && links.length > 0 && (
        <ul aria-label="External links" className="detail-external-links flex flex-wrap gap-2">
          {links.map((link) => (
            <li key={link.href}>
              <Button
                asChild
                variant="glass"
                size="sm"
                className={`${staticGlassActionClass} cursor-pointer rounded-full text-xs font-semibold tracking-wide`}
              >
                <a href={link.href} target="_blank" rel="noopener noreferrer">
                  {link.label} <span className="sr-only">(opens in a new tab)</span>
                  <ExternalLink aria-hidden="true" className="size-3 opacity-60" />
                </a>
              </Button>
            </li>
          ))}
        </ul>
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
