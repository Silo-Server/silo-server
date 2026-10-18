import {
  type Dispatch,
  lazy,
  type ReactNode,
  type RefObject,
  type SetStateAction,
  startTransition,
  Suspense,
  useCallback,
  useEffect,
  useId,
  useImperativeHandle,
  useRef,
  useState,
} from "react";
import {
  Check,
  Eye,
  EyeOff,
  FileText,
  Heart,
  LoaderCircle,
  MoreVertical,
  Plus,
  RotateCcw,
  TimerReset,
  ThumbsDown,
  X,
} from "lucide-react";
import { useLocation } from "react-router";
import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useFinePointer } from "@/hooks/useFinePointer";
import type { ItemDetail, MediaItemUserState } from "@/api/types";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useRefreshItemMetadata, useWatchedStateMutation } from "@/hooks/queries/items";
import { type DismissHomeItemVariables, useDismissHomeItem } from "@/hooks/queries/homeDismissals";
import { useToggleFavorite } from "@/hooks/queries/favorites";
import { useToggleWatchlist } from "@/hooks/queries/watchlist";
import { useClearResumePosition } from "@/hooks/queries/progress";
import { canMarkNotInterested, useToggleNotInterested } from "@/hooks/queries/notInterested";
import { getWatchedActionLabel } from "@/pages/ItemDetail/watchedState";
import { LocalErrorBoundary } from "@/components/LocalErrorBoundary";
import MangaFilesDialog from "@/components/MangaFilesDialog";
import RefreshMetadataDialog from "@/components/RefreshMetadataDialog";
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { useLongPress } from "@/hooks/useLongPress";
import { cn } from "@/lib/utils";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { buildMediaPlayHref } from "@/lib/mediaNavigation";
import { canCurateMetadata as canCurateMetadataForUser, isActingAdmin } from "@/lib/permissions";
import {
  mediaItemMenuIconClassName,
  mediaItemMenuTriggerClassName,
  type PosterActionDensity,
} from "@/components/mediaItemMenuTrigger";
import { useUICustomization } from "@/hooks/useUICustomization";
import { MediaActionIcon } from "@/components/mediaActionIcons";
import {
  showsFavoriteQuickAction,
  showsWatchedQuickAction,
  type CardQuickActionMode,
} from "@/lib/cardQuickActions";

// Edit Metadata and Match Item are curator tools, yet every media card carries
// this menu, so the dialogs load on demand instead of with the launch bundle.
// Opening a menu that offers them starts the download.
const importEditMetadataDialog = () => import("@/components/EditMetadataDialog");
const importMatchItemDialog = () => import("@/components/MatchItemDialog");
const EditMetadataDialog = lazy(importEditMetadataDialog);
const MatchItemDialog = lazy(importMatchItemDialog);

function prefetchMetadataDialogs() {
  // Nothing to report here: the dialog host imports them again when it
  // renders, and its error boundary shows a failure.
  importEditMetadataDialog().catch(() => undefined);
  importMatchItemDialog().catch(() => undefined);
}

type MediaItemType = ItemDetail["type"];

// Favorites and the watchlist are kept at the title level. The server rejects
// season writes, and the lists' default views and Home rows leave episodes
// out, so neither offers to be added.
function canAddToPersonalLists(mediaType: MediaItemType) {
  return mediaType !== "season" && mediaType !== "episode";
}

function useHasFinePointer() {
  // Reads the observed pointer, not the media query: Chromium on some Windows
  // machines with a touchscreen reports no fine pointer while a mouse is in
  // use, which withheld these shortcuts from the very devices this gate is
  // meant to serve. The `true` fallback preserves the established quick
  // actions in SSR, tests, and browsers where init has not run.
  return useFinePointer(true);
}

type MediaItemMenuEntry =
  | {
      kind: "action";
      key:
        | "playFromBeginning"
        | "toggleWatched"
        | "resetProgress"
        | "toggleFavorite"
        | "toggleWatchlist"
        | "toggleNotInterested"
        | "dismissFromHome"
        | "viewDetails"
        | "viewPlayHistory"
        | "refreshMetadata"
        | "editMetadata"
        | "matchItem";
      label: string;
    }
  | { kind: "separator" };

type MediaItemMenuActionKey = Extract<MediaItemMenuEntry, { kind: "action" }>["key"];

interface BuildMediaItemMenuModelOptions {
  mediaType: MediaItemType;
  userState?: MediaItemUserState;
  hasPartialProgress?: boolean;
  /**
   * Whether the item has a resume position to clear, rewatches included.
   * Defaults to hasPartialProgress.
   */
  hasResumePosition?: boolean;
  isAdmin: boolean;
  canCurateMetadata?: boolean;
  showCollectionActions?: boolean;
  dismissLabel?: string;
}

interface MediaItemMenuProps {
  contentId: string;
  mediaType: MediaItemType;
  libraryId?: number;
  userState?: MediaItemUserState;
  variant?: "poster" | "wide";
  /**
   * When false, hides favorites and watchlist actions. Defaults to true. Seasons and episodes
   * never offer adding to either list; see canAddToPersonalLists.
   */
  showCollectionActions?: boolean;
  /** Hides only the poster heart shortcut while retaining collection actions in the menu. */
  showFavoriteShortcut?: boolean;
  dismissAction?: DismissHomeItemVariables;
  hasPartialProgress?: boolean;
  /**
   * Whether the item has a resume position to clear, rewatches included, for
   * lists that hide a watched item's progress. Defaults to hasPartialProgress.
   */
  hasResumePosition?: boolean;
  /** Enables the watched shortcut on wide cards such as Continue Watching. */
  showWatchedShortcut?: boolean;
  /** Uses smaller poster controls on narrow catalog cards. */
  narrowPosterActions?: boolean;
  /** Which watched/favorite shortcuts appear outside the overflow menu. */
  quickActionMode?: CardQuickActionMode;
  /** Card root whose long press opens the touch action sheet. */
  longPressRef?: RefObject<HTMLElement | null>;
  /** Heading for the touch action sheet. */
  itemTitle?: string;
}

export function buildMediaItemMenuModel({
  mediaType,
  userState,
  hasPartialProgress = false,
  hasResumePosition = hasPartialProgress,
  isAdmin,
  canCurateMetadata = isAdmin,
  showCollectionActions = true,
  dismissLabel,
}: BuildMediaItemMenuModelOptions): MediaItemMenuEntry[] {
  const entries: MediaItemMenuEntry[] = [];
  const isAudiobook = mediaType === "audiobook";
  const isLeaf = mediaType === "movie" || mediaType === "episode" || isAudiobook;

  if (isLeaf && (hasPartialProgress || userState?.played === true)) {
    entries.push({
      kind: "action",
      key: "playFromBeginning",
      label: isAudiobook ? "Listen from Beginning" : "Play from Beginning",
    });
  }

  if (userState) {
    entries.push({
      kind: "action",
      key: "toggleWatched",
      label: getWatchedActionLabel({ type: mediaType, user_data: { played: userState.played } }),
    });
  }

  // Clears the resume point without passing through watched (issue #2134).
  if ((mediaType === "movie" || mediaType === "episode") && hasResumePosition) {
    entries.push({ kind: "action", key: "resetProgress", label: "Reset Progress" });
  }

  if (userState && showCollectionActions) {
    // A season or episode can already be on a list (saved by another client,
    // imported from another server, or saved by an older web build), so it
    // keeps the Remove entry.
    const canAdd = canAddToPersonalLists(mediaType);
    if (canAdd || userState.is_favorite) {
      entries.push({
        kind: "action",
        key: "toggleFavorite",
        label: userState.is_favorite ? "Remove from Favorites" : "Add to Favorites",
      });
    }
    if (canAdd || userState.in_watchlist) {
      entries.push({
        kind: "action",
        key: "toggleWatchlist",
        label: userState.in_watchlist ? "Remove from Watchlist" : "Add to Watchlist",
      });
    }
    if (canMarkNotInterested(mediaType)) {
      entries.push({
        kind: "action",
        key: "toggleNotInterested",
        label: userState.not_interested ? "Show in Recommendations" : "Not Interested",
      });
    }
  }

  // Manga series get a local file inspector (folder path, per-volume files).
  if (mediaType === "manga") {
    entries.push({ kind: "action", key: "viewDetails", label: "View Details" });
  }

  if (isAdmin || canCurateMetadata) {
    if (entries.length > 0) {
      entries.push({ kind: "separator" });
    }

    if (isAdmin) {
      entries.push({
        kind: "action",
        key: "viewPlayHistory",
        label: "View Play History",
      });
    }

    if (canCurateMetadata) {
      entries.push({
        kind: "action",
        key: "refreshMetadata",
        label: "Refresh Metadata",
      });

      if (mediaType === "movie" || mediaType === "series") {
        entries.push(
          {
            kind: "action",
            key: "editMetadata",
            label: "Edit Metadata",
          },
          {
            kind: "action",
            key: "matchItem",
            label: "Match Item",
          },
        );
      }
    }
  }

  if (dismissLabel) {
    if (entries.length > 0) {
      entries.push({ kind: "separator" });
    }
    entries.push({
      kind: "action",
      key: "dismissFromHome",
      label: dismissLabel,
    });
  }

  return entries;
}

function stopMenuEvent(event: Pick<Event, "preventDefault" | "stopPropagation">) {
  event.preventDefault();
  event.stopPropagation();
}

function MediaItemMenuActionIcon({
  actionKey,
  userState,
  isRefreshing,
}: {
  actionKey: MediaItemMenuActionKey;
  userState?: MediaItemUserState;
  isRefreshing: boolean;
}) {
  switch (actionKey) {
    case "playFromBeginning":
      return <RotateCcw aria-hidden="true" className="size-4" />;
    case "toggleWatched":
      return userState?.played ? (
        <Eye aria-hidden="true" className="size-4 text-emerald-400" />
      ) : (
        <EyeOff aria-hidden="true" className="size-4" />
      );
    case "resetProgress":
      return <TimerReset aria-hidden="true" className="size-4" />;
    case "toggleFavorite":
      return (
        <Heart
          aria-hidden="true"
          className={cn("size-4", userState?.is_favorite && "fill-current text-red-400")}
        />
      );
    case "toggleWatchlist":
      return userState?.in_watchlist ? (
        <Check aria-hidden="true" className="size-4" />
      ) : (
        <Plus aria-hidden="true" className="size-4" />
      );
    case "toggleNotInterested":
      return (
        <ThumbsDown
          aria-hidden="true"
          className={cn("size-4", userState?.not_interested && "fill-current")}
        />
      );
    case "dismissFromHome":
      return <X aria-hidden="true" className="size-4" />;
    case "viewDetails":
      return <FileText aria-hidden="true" className="size-4" />;
    case "viewPlayHistory":
      return <MediaActionIcon action="viewPlayHistory" />;
    case "refreshMetadata":
      return <MediaActionIcon action="refreshMetadata" isPending={isRefreshing} />;
    case "editMetadata":
      return <MediaActionIcon action="editMetadata" />;
    case "matchItem":
      return <MediaActionIcon action="matchItem" />;
  }
}

function MediaItemActionSheetRow({
  disabled,
  onSelect,
  children,
}: {
  disabled?: boolean;
  onSelect: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onSelect}
      className="hover:bg-accent/60 active:bg-accent flex min-h-11 w-full items-center gap-3 px-5 py-3 text-left text-sm font-medium transition-colors disabled:opacity-50"
    >
      {children}
    </button>
  );
}

/**
 * Touch equivalent of the three-dot dropdown: the same action model presented
 * as a bottom sheet, opened by long-pressing the card.
 */
function MediaItemActionSheet({
  open,
  onOpenChange,
  title,
  entries,
  userState,
  isPending,
  isRefreshing,
  onSelectAction,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title?: string;
  entries: MediaItemMenuEntry[];
  userState?: MediaItemUserState;
  isPending: boolean;
  isRefreshing: boolean;
  onSelectAction: (actionKey: MediaItemMenuActionKey) => void;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="bottom"
        showCloseButton={false}
        className="max-h-[80svh] gap-0 overflow-y-auto rounded-t-2xl p-0 pb-[env(safe-area-inset-bottom)]"
      >
        <SheetHeader className="px-5 pt-4 pb-2">
          <SheetTitle className="truncate text-left text-base">{title ?? "Actions"}</SheetTitle>
          <SheetDescription className="sr-only">Choose an action for this item.</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col pb-3">
          {entries.map((entry, index) =>
            entry.kind === "separator" ? (
              <div
                key={`separator-${index}`}
                aria-hidden="true"
                className="bg-border/60 my-1.5 h-px"
              />
            ) : (
              <MediaItemActionSheetRow
                key={entry.key}
                disabled={isPending}
                onSelect={() => onSelectAction(entry.key)}
              >
                <MediaItemMenuActionIcon
                  actionKey={entry.key}
                  userState={userState}
                  isRefreshing={isRefreshing}
                />
                {entry.label}
              </MediaItemActionSheetRow>
            ),
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function CardQuickActionButton({
  pressed,
  isPending,
  label,
  className,
  burstClassName,
  burstTestId,
  onActivate,
  children,
}: {
  pressed: boolean;
  isPending: boolean;
  label: string;
  className?: string;
  burstClassName: string;
  burstTestId: string;
  onActivate: () => void;
  children: (isAnimating: boolean) => ReactNode;
}) {
  const [isAnimating, setIsAnimating] = useState(false);
  const pointerStartRef = useRef<{
    pointerId: number;
    clientX: number;
    clientY: number;
    maxMovement: number;
  } | null>(null);
  const suppressPointerClickRef = useRef(false);

  const activate = useCallback(() => {
    if (isPending) return;
    if (!pressed) setIsAnimating(true);
    onActivate();
  }, [isPending, onActivate, pressed]);

  useEffect(() => {
    if (!isAnimating) return;
    const timeout = window.setTimeout(() => setIsAnimating(false), 420);
    return () => window.clearTimeout(timeout);
  }, [isAnimating]);

  return (
    <button
      type="button"
      aria-label={label}
      aria-pressed={pressed}
      title={label}
      disabled={isPending}
      className={cn("relative cursor-pointer overflow-visible disabled:opacity-70", className)}
      onPointerDown={(event) => {
        if (event.button !== 0) {
          pointerStartRef.current = null;
          return;
        }
        pointerStartRef.current = {
          pointerId: event.pointerId,
          clientX: event.clientX,
          clientY: event.clientY,
          maxMovement: 0,
        };
        event.currentTarget.setPointerCapture?.(event.pointerId);
      }}
      onPointerMove={(event) => {
        const pointerStart = pointerStartRef.current;
        if (!pointerStart || pointerStart.pointerId !== event.pointerId) return;

        pointerStart.maxMovement = Math.max(
          pointerStart.maxMovement,
          Math.abs(event.clientX - pointerStart.clientX),
          Math.abs(event.clientY - pointerStart.clientY),
        );
      }}
      onPointerUp={(event) => {
        const pointerStart = pointerStartRef.current;
        pointerStartRef.current = null;
        if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
          event.currentTarget.releasePointerCapture(event.pointerId);
        }
        if (!pointerStart || pointerStart.pointerId !== event.pointerId) return;

        // A normal browser click follows pointerup. Handle the pointer release
        // here so carousel swipes can be rejected, then suppress only that
        // follow-up click. The timeout leaves an unpaired mouse/synthetic click
        // available as a cross-browser fallback.
        suppressPointerClickRef.current = true;
        window.setTimeout(() => {
          suppressPointerClickRef.current = false;
        }, 0);

        const movement = Math.max(
          pointerStart.maxMovement,
          Math.abs(event.clientX - pointerStart.clientX),
          Math.abs(event.clientY - pointerStart.clientY),
        );
        if (movement > 10) return;

        const bounds = event.currentTarget.getBoundingClientRect();
        const releasedOutside =
          bounds.width > 0 &&
          bounds.height > 0 &&
          (event.clientX < bounds.left ||
            event.clientX > bounds.right ||
            event.clientY < bounds.top ||
            event.clientY > bounds.bottom);
        if (releasedOutside) return;

        event.preventDefault();
        event.stopPropagation();
        activate();
      }}
      onPointerCancel={(event) => {
        pointerStartRef.current = null;
        if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
          event.currentTarget.releasePointerCapture(event.pointerId);
        }
      }}
      onLostPointerCapture={() => {
        pointerStartRef.current = null;
      }}
      onClick={(event) => {
        stopMenuEvent(event);
        if (suppressPointerClickRef.current) {
          suppressPointerClickRef.current = false;
          return;
        }
        activate();
      }}
    >
      {isAnimating && (
        <span
          aria-hidden="true"
          data-testid={burstTestId}
          className={cn(
            "absolute top-1/2 left-1/2 size-7 -translate-x-1/2 -translate-y-1/2 animate-ping rounded-full motion-reduce:hidden",
            burstClassName,
          )}
        />
      )}
      {children(isAnimating)}
    </button>
  );
}

export function PosterCardFavoriteButton({
  isFavorite,
  isPending,
  density = "standard",
  onToggle,
}: {
  isFavorite: boolean;
  isPending: boolean;
  density?: PosterActionDensity;
  onToggle: () => void;
}) {
  const label = isFavorite ? "Remove from favorites" : "Add to favorites";

  return (
    <CardQuickActionButton
      pressed={isFavorite}
      isPending={isPending}
      label={label}
      className={cn(
        mediaItemMenuTriggerClassName("poster", density),
        isFavorite && "text-red-500 hover:text-red-400",
      )}
      burstClassName="bg-red-500/30"
      burstTestId="favorite-burst"
      onActivate={onToggle}
    >
      {(isAnimating) => (
        <Heart
          className={cn(
            "relative transition-[transform,color,fill] duration-300 ease-out motion-reduce:transition-none",
            mediaItemMenuIconClassName("poster", density),
            isFavorite && "scale-110 fill-red-500 text-red-500",
            isAnimating && "scale-125",
          )}
        />
      )}
    </CardQuickActionButton>
  );
}

function WatchedQuickActionButton({
  mediaType,
  isWatched,
  isPending,
  variant,
  density,
  onToggle,
}: {
  mediaType: MediaItemType;
  isWatched: boolean;
  isPending: boolean;
  variant: "poster" | "wide";
  density: PosterActionDensity;
  onToggle: () => void;
}) {
  const label = getWatchedActionLabel({ type: mediaType, user_data: { played: isWatched } });
  const Icon = isWatched ? Eye : EyeOff;

  return (
    <CardQuickActionButton
      pressed={isWatched}
      isPending={isPending}
      label={label}
      className={cn(
        mediaItemMenuTriggerClassName(variant, density),
        isWatched && "text-emerald-400 hover:text-emerald-300",
      )}
      burstClassName="bg-emerald-400/30"
      burstTestId="watched-burst"
      onActivate={onToggle}
    >
      {(isAnimating) => (
        <Icon
          className={cn(
            "relative transition-[transform,color] duration-300 ease-out motion-reduce:transition-none",
            mediaItemMenuIconClassName(variant, density),
            isWatched && "scale-110",
            isAnimating && "scale-125",
          )}
        />
      )}
    </CardQuickActionButton>
  );
}

type MetadataAction = "edit" | "match";

/** What the status dialog stands in for: the item fetch or the dialog's own chunk. */
type MetadataActionPending = "details" | "dialog";

const PENDING_TEXT: Record<MetadataActionPending, { loading: string; failed: string }> = {
  details: {
    loading: "Loading the latest item details…",
    failed: "The item details could not be loaded.",
  },
  dialog: {
    loading: "Loading the dialog…",
    failed: "The dialog could not be loaded.",
  },
};

function MetadataActionStatusDialog({
  action,
  pending,
  loading,
  error,
  retryLabel = "Try Again",
  onRetry,
  onClose,
}: {
  action: MetadataAction;
  pending: MetadataActionPending;
  loading: boolean;
  error?: unknown;
  retryLabel?: string;
  onRetry?: () => void;
  onClose: () => void;
}) {
  const actionLabel = action === "edit" ? "Edit Metadata" : "Match Item";
  const text = PENDING_TEXT[pending];

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>{actionLabel}</DialogTitle>
          <DialogDescription>{loading ? text.loading : text.failed}</DialogDescription>
        </DialogHeader>
        {loading ? (
          <div className="text-muted-foreground flex items-center gap-2 text-sm">
            <LoaderCircle className="size-4 animate-spin" />
            Loading…
          </div>
        ) : (
          <div className="flex items-center justify-between gap-3">
            <p className="text-muted-foreground text-sm">
              {error instanceof Error ? error.message : "Please try again."}
            </p>
            <Button type="button" variant="outline" size="sm" onClick={onRetry}>
              {retryLabel}
            </Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function MetadataActionDialogHost({
  action,
  contentId,
  libraryId,
  onClose,
}: {
  action: MetadataAction;
  contentId: string;
  libraryId?: number;
  onClose: () => void;
}) {
  const {
    data: item,
    error,
    isFetching,
    isLoading,
    refetch,
  } = useCatalogItemDetail(contentId, libraryId);

  if (item) {
    return (
      <LocalErrorBoundary
        fallback={(dialogError) => (
          // Browsers remember a failed module import for the life of the page,
          // so importing the chunk again cannot succeed; a reload can.
          <MetadataActionStatusDialog
            action={action}
            pending="dialog"
            loading={false}
            error={dialogError}
            retryLabel="Reload Page"
            onRetry={() => window.location.reload()}
            onClose={onClose}
          />
        )}
      >
        <Suspense
          fallback={
            <MetadataActionStatusDialog
              action={action}
              pending="dialog"
              loading
              onClose={onClose}
            />
          }
        >
          {action === "edit" ? (
            <EditMetadataDialog item={item} open onOpenChange={(open) => !open && onClose()} />
          ) : (
            <MatchItemDialog
              key={item.content_id}
              item={libraryId === undefined ? item : { ...item, library_id: libraryId }}
              open
              onOpenChange={(open) => !open && onClose()}
            />
          )}
        </Suspense>
      </LocalErrorBoundary>
    );
  }

  return (
    <MetadataActionStatusDialog
      action={action}
      pending="details"
      loading={isLoading || isFetching}
      error={error}
      onRetry={() => void refetch()}
      onClose={onClose}
    />
  );
}

type MenuEngineRequest =
  | { kind: "action"; key: MediaItemMenuActionKey }
  | { kind: "toggleWatched" }
  | { kind: "toggleFavorite" };

interface MenuEngineHandle {
  run: (request: MenuEngineRequest) => void;
}

interface MenuPendingState {
  watched: boolean;
  favorite: boolean;
  refresh: boolean;
  any: boolean;
}

const IDLE_MENU_PENDING: MenuPendingState = {
  watched: false,
  favorite: false,
  refresh: false,
  any: false,
};

type MenuInteraction = "keyboard" | "pointer" | null;

/**
 * The Radix menu behind the three-dot trigger. It mounts the first time the
 * trigger is focused or opened, so idle cards carry no menu root (each root
 * installs document-level keyboard listeners). The visible trigger button
 * stays owned by MediaItemMenu; Radix anchors to an inert span laid over it,
 * so the button keeps its DOM identity, and with it focus, when this mounts.
 */
function MediaItemMenuDropdown({
  open,
  onOpenChange,
  menuId,
  triggerId,
  model,
  userState,
  isPending,
  isRefreshing,
  onSelectAction,
  triggerRef,
  lastMenuInteractionRef,
  pointerClosedMenuRef,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  menuId: string;
  triggerId: string;
  model: MediaItemMenuEntry[];
  userState?: MediaItemUserState;
  isPending: boolean;
  isRefreshing: boolean;
  onSelectAction: (actionKey: MediaItemMenuActionKey) => void;
  triggerRef: RefObject<HTMLButtonElement | null>;
  lastMenuInteractionRef: RefObject<MenuInteraction>;
  pointerClosedMenuRef: RefObject<boolean>;
}) {
  const interactedOutsideRef = useRef(false);

  return (
    <DropdownMenu modal={false} open={open} onOpenChange={onOpenChange}>
      <DropdownMenuTrigger asChild>
        <span aria-hidden="true" className="pointer-events-none absolute inset-0" />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        id={menuId}
        aria-labelledby={triggerId}
        align="end"
        className="w-max max-w-[calc(100vw-2rem)] min-w-0"
        onPointerDownOutside={() => {
          lastMenuInteractionRef.current = "pointer";
        }}
        onInteractOutside={() => {
          interactedOutsideRef.current = true;
        }}
        onPointerDownCapture={() => {
          lastMenuInteractionRef.current = "pointer";
        }}
        onKeyDownCapture={() => {
          lastMenuInteractionRef.current = "keyboard";
        }}
        onEscapeKeyDown={() => {
          // Radix handles Escape from a document listener, ahead of the
          // capture handler above, and this controlled menu closes right away.
          lastMenuInteractionRef.current = "keyboard";
        }}
        onCloseAutoFocus={(event) => {
          // Radix would return focus to its own (inert) trigger; the visible
          // button is ours, so mirror Radix's rule here: a pointer close drops
          // focus so hover exit can hide the trigger, a close after an outside
          // interaction leaves focus where the user put it, and anything else
          // (Escape, selecting an item) returns focus to the trigger.
          event.preventDefault();
          if (pointerClosedMenuRef.current) {
            triggerRef.current?.blur();
          } else if (!interactedOutsideRef.current) {
            triggerRef.current?.focus();
          }
          pointerClosedMenuRef.current = false;
          interactedOutsideRef.current = false;
        }}
      >
        {model.map((entry, index) => {
          if (entry.kind === "separator") {
            return <DropdownMenuSeparator key={`separator-${index}`} />;
          }

          return (
            <DropdownMenuItem
              key={entry.key}
              disabled={isPending}
              onSelect={() => onSelectAction(entry.key)}
            >
              <MediaItemMenuActionIcon
                actionKey={entry.key}
                userState={userState}
                isRefreshing={isRefreshing}
              />
              {entry.label}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * The card's action machinery: mutations, navigation, playback, the touch
 * action sheet, and the dialogs. A home page holds hundreds of cards, so this
 * mounts on a card's first hover, focus, context menu, long press, or action;
 * requests made before it mounted are queued and run on mount.
 */
function MediaItemMenuEngine({
  handleRef,
  contentId,
  mediaType,
  libraryId,
  dismissAction,
  model,
  currentUserState,
  setCurrentUserState,
  onPendingChange,
  takeQueuedRequests,
  actionSheetMounted,
  actionSheetOpen,
  onActionSheetOpenChange,
  itemTitle,
}: {
  handleRef: RefObject<MenuEngineHandle | null>;
  contentId: string;
  mediaType: MediaItemType;
  libraryId?: number;
  dismissAction?: DismissHomeItemVariables;
  model: MediaItemMenuEntry[];
  currentUserState?: MediaItemUserState;
  setCurrentUserState: Dispatch<SetStateAction<MediaItemUserState | undefined>>;
  onPendingChange: Dispatch<SetStateAction<MenuPendingState>>;
  takeQueuedRequests: () => MenuEngineRequest[];
  actionSheetMounted: boolean;
  actionSheetOpen: boolean;
  onActionSheetOpenChange: (open: boolean) => void;
  itemTitle?: string;
}) {
  const navigate = useViewTransitionNavigate();
  const location = useLocation();
  const playbackController = useWatchPlaybackController();
  const [refreshDialogOpen, setRefreshDialogOpen] = useState(false);
  const [filesDialogOpen, setFilesDialogOpen] = useState(false);
  const [metadataAction, setMetadataAction] = useState<MetadataAction | null>(null);
  // Once opened, an overlay stays mounted so closing runs its exit animation
  // and restores focus instead of vanishing.
  const [refreshDialogMounted, setRefreshDialogMounted] = useState(false);
  const [filesDialogMounted, setFilesDialogMounted] = useState(false);

  const watchedMutation = useWatchedStateMutation({
    content_id: contentId,
    type: mediaType,
    user_data: currentUserState ? { played: currentUserState.played } : undefined,
  });
  const favoriteMutation = useToggleFavorite(contentId);
  const watchlistMutation = useToggleWatchlist(contentId);
  const notInterestedMutation = useToggleNotInterested(contentId);
  const refreshMetadataMutation = useRefreshItemMetadata();
  const dismissHomeItemMutation = useDismissHomeItem();
  const clearResumePositionMutation = useClearResumePosition();
  const currentHref = `${location.pathname}${location.search}`;

  const isPending =
    watchedMutation.isPending ||
    favoriteMutation.isPending ||
    watchlistMutation.isPending ||
    notInterestedMutation.isPending ||
    refreshMetadataMutation.isPending ||
    dismissHomeItemMutation.isPending ||
    clearResumePositionMutation.isPending;
  const watchedPending = watchedMutation.isPending;
  const favoritePending = favoriteMutation.isPending;
  const refreshPending = refreshMetadataMutation.isPending;

  useEffect(() => {
    onPendingChange((previous) =>
      previous.watched === watchedPending &&
      previous.favorite === favoritePending &&
      previous.refresh === refreshPending &&
      previous.any === isPending
        ? previous
        : {
            watched: watchedPending,
            favorite: favoritePending,
            refresh: refreshPending,
            any: isPending,
          },
    );
  }, [favoritePending, isPending, onPendingChange, refreshPending, watchedPending]);

  async function runOptimisticToggle(
    field: "played" | "is_favorite",
    pending: boolean,
    mutate: (nextValue: boolean, previousValue: boolean) => Promise<unknown>,
  ) {
    if (!currentUserState || pending) return;
    const previousValue = currentUserState[field];
    const nextValue = !previousValue;
    setCurrentUserState((previous) => (previous ? { ...previous, [field]: nextValue } : previous));
    try {
      await mutate(nextValue, previousValue);
    } catch {
      setCurrentUserState((previous) =>
        previous ? { ...previous, [field]: previousValue } : previous,
      );
    }
  }

  async function handleWatchedToggle() {
    await runOptimisticToggle("played", watchedMutation.isPending, (nextValue) =>
      watchedMutation.mutateAsync(nextValue),
    );
  }

  async function handleFavoriteToggle() {
    await runOptimisticToggle("is_favorite", favoriteMutation.isPending, (_, previousValue) =>
      favoriteMutation.mutateAsync(previousValue),
    );
  }

  async function handleAction(actionKey: MediaItemMenuActionKey) {
    switch (actionKey) {
      case "playFromBeginning": {
        if (mediaType === "audiobook") {
          navigate(buildMediaPlayHref({ contentId, type: mediaType, libraryId, restart: true }));
          return;
        }
        playbackController.startPlayback(
          {
            contentId,
            restart: true,
            returnHref: currentHref,
          },
          "viewer",
        );
        return;
      }
      case "toggleWatched": {
        await handleWatchedToggle();
        return;
      }
      case "resetProgress": {
        clearResumePositionMutation.mutate({ contentId });
        return;
      }
      case "toggleFavorite": {
        await handleFavoriteToggle();
        return;
      }
      case "toggleWatchlist": {
        if (!currentUserState) return;
        await watchlistMutation.mutateAsync(currentUserState.in_watchlist);
        setCurrentUserState((prev) =>
          prev ? { ...prev, in_watchlist: !prev.in_watchlist } : prev,
        );
        return;
      }
      case "toggleNotInterested": {
        if (!currentUserState) return;
        const marked = currentUserState.not_interested ?? false;
        const authority = captureProfileRequestContext();
        setCurrentUserState((prev) => (prev ? { ...prev, not_interested: !marked } : prev));
        try {
          await notInterestedMutation.mutateAsync(marked);
        } catch {
          // After a profile switch the card shows the new profile's state.
          if (!authority || isCapturedProfileAuthorityActive(authority)) {
            setCurrentUserState((prev) => (prev ? { ...prev, not_interested: marked } : prev));
          }
        }
        return;
      }
      case "viewDetails": {
        setFilesDialogMounted(true);
        setFilesDialogOpen(true);
        return;
      }
      case "viewPlayHistory": {
        navigate(`/admin/history?media_item_id=${encodeURIComponent(contentId)}`);
        return;
      }
      case "dismissFromHome": {
        if (!dismissAction) return;
        await dismissHomeItemMutation.mutateAsync(dismissAction);
        return;
      }
      case "refreshMetadata": {
        setRefreshDialogMounted(true);
        setRefreshDialogOpen(true);
        return;
      }
      case "editMetadata": {
        setMetadataAction("edit");
        return;
      }
      case "matchItem": {
        setMetadataAction("match");
        return;
      }
    }
  }

  function run(request: MenuEngineRequest) {
    switch (request.kind) {
      case "toggleWatched":
        void handleWatchedToggle();
        return;
      case "toggleFavorite":
        void handleFavoriteToggle();
        return;
      case "action":
        void handleAction(request.key);
        return;
    }
  }

  useImperativeHandle(handleRef, () => ({ run }));

  // The handle is attached during the layout phase, before this runs.
  useEffect(() => {
    const queued = takeQueuedRequests();
    queued.forEach((request) => handleRef.current?.run(request));
  }, [handleRef, takeQueuedRequests]);

  function handleRefreshConfirm(mode: "quick" | "complete") {
    setRefreshDialogOpen(false);
    refreshMetadataMutation.mutate({ item: { content_id: contentId, type: mediaType }, mode });
  }

  return (
    <>
      {actionSheetMounted && (
        <MediaItemActionSheet
          open={actionSheetOpen}
          onOpenChange={onActionSheetOpenChange}
          title={itemTitle}
          entries={model}
          userState={currentUserState}
          isPending={isPending}
          isRefreshing={refreshMetadataMutation.isPending}
          onSelectAction={(actionKey) => {
            onActionSheetOpenChange(false);
            void handleAction(actionKey);
          }}
        />
      )}
      {refreshDialogMounted && (
        <RefreshMetadataDialog
          open={refreshDialogOpen}
          onOpenChange={setRefreshDialogOpen}
          onConfirm={handleRefreshConfirm}
          isPending={refreshMetadataMutation.isPending}
        />
      )}
      {metadataAction && (
        <MetadataActionDialogHost
          action={metadataAction}
          contentId={contentId}
          libraryId={libraryId}
          onClose={() => setMetadataAction(null)}
        />
      )}
      {mediaType === "manga" && filesDialogMounted && (
        <MangaFilesDialog
          contentId={contentId}
          open={filesDialogOpen}
          onOpenChange={setFilesDialogOpen}
        />
      )}
    </>
  );
}

export default function MediaItemMenu({
  contentId,
  mediaType,
  libraryId,
  userState,
  variant = "poster",
  showCollectionActions = true,
  showFavoriteShortcut = true,
  dismissAction,
  hasPartialProgress = false,
  hasResumePosition = hasPartialProgress,
  showWatchedShortcut = false,
  narrowPosterActions = false,
  quickActionMode = "none",
  longPressRef,
  itemTitle,
}: MediaItemMenuProps) {
  const user = useOptionalAuth()?.user;
  const { profile: currentProfile, hasSelectedProfile } = useCurrentProfile();
  const profileIsResolved = !hasSelectedProfile || Boolean(currentProfile);
  // useIsActingAdmin's policy, read from this card's single profile lookup:
  // fail closed while a selected profile is still resolving.
  const isAdmin = profileIsResolved && isActingAdmin(user, currentProfile);
  const canCurateMetadata = profileIsResolved && canCurateMetadataForUser(user, currentProfile);
  const { cardPresentation } = useUICustomization();
  const hasFinePointer = useHasFinePointer();
  const [currentUserState, setCurrentUserState] = useState(userState);
  const lastSyncedUserStateRef = useRef(userState);
  const activeProfileId = currentProfile?.id;
  const lastSyncedProfileRef = useRef(activeProfileId);
  // A card that has never been hovered, focused, or used mounts none of the
  // action machinery — a home page holds hundreds of cards.
  const [engineMounted, setEngineMounted] = useState(false);
  const [dropdownMounted, setDropdownMounted] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [actionSheetMounted, setActionSheetMounted] = useState(false);
  const [actionSheetOpen, setActionSheetOpen] = useState(false);
  const [pending, setPending] = useState(IDLE_MENU_PENDING);
  const engineRef = useRef<MenuEngineHandle | null>(null);
  const queuedRequestsRef = useRef<MenuEngineRequest[]>([]);
  const menuTriggerRef = useRef<HTMLButtonElement>(null);
  const menuOpenRef = useRef(false);
  const lastMenuInteractionRef = useRef<MenuInteraction>(null);
  const pointerClosedMenuRef = useRef(false);
  const pointerPressRef = useRef(false);
  const menuBaseId = useId();
  const triggerId = `${menuBaseId}-trigger`;
  const menuId = `${menuBaseId}-menu`;

  useEffect(() => {
    const lastSynced = lastSyncedUserStateRef.current;
    // A profile switch always resyncs: the local state may still hold the
    // earlier profile's optimistic change while the props look unchanged.
    const sameProfile = lastSyncedProfileRef.current === activeProfileId;
    if (
      sameProfile &&
      lastSynced?.played === userState?.played &&
      lastSynced?.is_favorite === userState?.is_favorite &&
      lastSynced?.in_watchlist === userState?.in_watchlist &&
      lastSynced?.not_interested === userState?.not_interested
    ) {
      return;
    }
    lastSyncedUserStateRef.current = userState;
    lastSyncedProfileRef.current = activeProfileId;
    setCurrentUserState(userState);
  }, [userState, activeProfileId]);

  const dismissLabel =
    dismissAction?.surface === "continue_watching"
      ? mediaType === "audiobook"
        ? "Remove from Continue Listening"
        : mediaType === "ebook"
          ? "Remove from Continue Reading"
          : "Remove from Continue Watching"
      : dismissAction?.surface === "next_up"
        ? "Remove from Next Up"
        : undefined;

  const model = buildMediaItemMenuModel({
    mediaType,
    userState: currentUserState,
    hasPartialProgress,
    hasResumePosition,
    isAdmin,
    canCurateMetadata,
    showCollectionActions,
    dismissLabel,
  });
  const offersMetadataDialogs = model.some(
    (entry) =>
      entry.kind === "action" && (entry.key === "editMetadata" || entry.key === "matchItem"),
  );

  const takeQueuedRequests = useCallback(() => {
    const queued = queuedRequestsRef.current;
    queuedRequestsRef.current = [];
    return queued;
  }, []);

  function requestEngine(request: MenuEngineRequest) {
    const engine = engineRef.current;
    if (engine) {
      engine.run(request);
      return;
    }
    queuedRequestsRef.current.push(request);
    setEngineMounted(true);
  }

  function warmUp({ dropdown = false, deferred = false } = {}) {
    if (engineMounted && (!dropdown || dropdownMounted)) return;
    const mount = () => {
      setEngineMounted(true);
      if (dropdown && model.length > 0) setDropdownMounted(true);
    };
    if (deferred) {
      startTransition(mount);
    } else {
      mount();
    }
  }

  function changeMenuOpen(open: boolean) {
    if (menuOpenRef.current === open) return;
    menuOpenRef.current = open;
    if (open) {
      if (offersMetadataDialogs) prefetchMetadataDialogs();
      pointerClosedMenuRef.current = false;
      lastMenuInteractionRef.current = null;
      setEngineMounted(true);
      setDropdownMounted(true);
    } else {
      pointerClosedMenuRef.current = lastMenuInteractionRef.current === "pointer";
      lastMenuInteractionRef.current = null;
    }
    setMenuOpen(open);
  }

  useLongPress(longPressRef, {
    onLongPress: () => {
      if (offersMetadataDialogs) prefetchMetadataDialogs();
      setEngineMounted(true);
      setActionSheetMounted(true);
      setActionSheetOpen(true);
    },
    enabled: model.length > 0,
  });
  // The model can still hold Remove for a season or episode already on the
  // list; that stays in the menu rather than a heart that vanishes on click.
  const showPosterFavorite =
    hasFinePointer &&
    variant === "poster" &&
    canAddToPersonalLists(mediaType) &&
    showFavoriteShortcut &&
    showsFavoriteQuickAction(quickActionMode) &&
    model.some((entry) => entry.kind === "action" && entry.key === "toggleFavorite");
  const hasWatchedAction = model.some(
    (entry) => entry.kind === "action" && entry.key === "toggleWatched",
  );
  const rootPosterSupportsWatchedShortcut =
    variant === "poster" && (mediaType === "movie" || mediaType === "series");
  const showWatchedQuickAction =
    hasFinePointer &&
    showsWatchedQuickAction(quickActionMode) &&
    hasWatchedAction &&
    (rootPosterSupportsWatchedShortcut || showWatchedShortcut);
  const posterActionDensity: PosterActionDensity =
    variant === "poster" && narrowPosterActions
      ? "narrow"
      : variant === "poster" && cardPresentation.poster_size === "compact"
        ? "compact"
        : "standard";

  const triggerClassName = mediaItemMenuTriggerClassName(variant, posterActionDensity);
  // Hovering or focusing the controls mounts the machinery ahead of the first
  // press. A press that beats it still works: requests queue until it mounts.
  const warmUpHandlers = (dropdown: boolean) => ({
    onPointerEnter: (event: React.PointerEvent) => {
      if (event.pointerType === "mouse") warmUp({ dropdown, deferred: true });
    },
    onFocus: () => warmUp({ dropdown }),
    onContextMenu: () => warmUp({ dropdown }),
  });

  return (
    <>
      {(showWatchedQuickAction || showPosterFavorite) && currentUserState && (
        <div
          className={cn(
            "absolute z-20 flex items-center",
            variant === "wide"
              ? "bottom-3 left-3 gap-1.5"
              : posterActionDensity === "narrow"
                ? "bottom-1.5 left-1.5 gap-0.5"
                : posterActionDensity === "compact"
                  ? "bottom-1.5 left-1.5 gap-0.5 sm:bottom-2 sm:left-2 sm:gap-1"
                  : "bottom-1.5 left-1.5 gap-0.5 sm:bottom-2.5 sm:left-2.5 sm:gap-1.5",
          )}
          onClick={stopMenuEvent}
          onPointerDown={stopMenuEvent}
          {...warmUpHandlers(false)}
        >
          {showWatchedQuickAction && (
            <WatchedQuickActionButton
              mediaType={mediaType}
              isWatched={currentUserState.played}
              isPending={pending.watched}
              variant={variant}
              density={posterActionDensity}
              onToggle={() => requestEngine({ kind: "toggleWatched" })}
            />
          )}
          {showPosterFavorite && (
            <PosterCardFavoriteButton
              isFavorite={currentUserState.is_favorite}
              isPending={pending.favorite}
              density={posterActionDensity}
              onToggle={() => requestEngine({ kind: "toggleFavorite" })}
            />
          )}
        </div>
      )}
      <div
        className={cn(
          "absolute z-20",
          variant === "wide"
            ? "right-3 bottom-3"
            : posterActionDensity === "narrow"
              ? "right-1.5 bottom-1.5"
              : posterActionDensity === "compact"
                ? "right-1.5 bottom-1.5 sm:right-2 sm:bottom-2"
                : "right-1.5 bottom-1.5 sm:right-2.5 sm:bottom-2.5",
        )}
        onClick={stopMenuEvent}
        onPointerDown={stopMenuEvent}
        {...warmUpHandlers(true)}
      >
        {model.length === 0 ? (
          <button type="button" aria-label="More actions" disabled className={triggerClassName}>
            <MoreVertical className={mediaItemMenuIconClassName(variant, posterActionDensity)} />
          </button>
        ) : (
          <button
            ref={menuTriggerRef}
            id={triggerId}
            type="button"
            aria-label="More actions"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-controls={menuOpen ? menuId : undefined}
            data-state={menuOpen ? "open" : "closed"}
            className={triggerClassName}
            onPointerDown={(event) => {
              lastMenuInteractionRef.current = "pointer";
              // Radix trigger semantics: only a plain primary press toggles.
              if (event.button !== 0 || event.ctrlKey) return;
              pointerPressRef.current = true;
              changeMenuOpen(!menuOpenRef.current);
            }}
            onPointerCancel={() => {
              pointerPressRef.current = false;
            }}
            onClick={(event) => {
              // Screen readers and element.click() send a lone click. A click
              // that follows a pointer press was already handled on
              // pointerdown, and Enter/Space clicks are cancelled by the key
              // handlers below.
              const handledByPointer = pointerPressRef.current || event.detail > 0;
              pointerPressRef.current = false;
              if (handledByPointer) return;
              lastMenuInteractionRef.current = "keyboard";
              changeMenuOpen(!menuOpenRef.current);
            }}
            onKeyUp={(event) => {
              // Space activates a button on keyup; keydown already toggled.
              if (event.key === " ") event.preventDefault();
            }}
            onKeyDown={(event) => {
              lastMenuInteractionRef.current = "keyboard";
              if (event.key === "Enter" || event.key === " ") {
                changeMenuOpen(!menuOpenRef.current);
              } else if (event.key === "ArrowDown") {
                changeMenuOpen(true);
              } else {
                return;
              }
              event.preventDefault();
            }}
          >
            <MoreVertical className={mediaItemMenuIconClassName(variant, posterActionDensity)} />
          </button>
        )}
        {dropdownMounted && model.length > 0 && (
          <MediaItemMenuDropdown
            open={menuOpen}
            onOpenChange={changeMenuOpen}
            menuId={menuId}
            triggerId={triggerId}
            model={model}
            userState={currentUserState}
            isPending={pending.any}
            isRefreshing={pending.refresh}
            onSelectAction={(actionKey) => requestEngine({ kind: "action", key: actionKey })}
            triggerRef={menuTriggerRef}
            lastMenuInteractionRef={lastMenuInteractionRef}
            pointerClosedMenuRef={pointerClosedMenuRef}
          />
        )}
      </div>
      {engineMounted && (
        <MediaItemMenuEngine
          handleRef={engineRef}
          contentId={contentId}
          mediaType={mediaType}
          libraryId={libraryId}
          dismissAction={dismissAction}
          model={model}
          currentUserState={currentUserState}
          setCurrentUserState={setCurrentUserState}
          onPendingChange={setPending}
          takeQueuedRequests={takeQueuedRequests}
          actionSheetMounted={actionSheetMounted}
          actionSheetOpen={actionSheetOpen}
          onActionSheetOpenChange={setActionSheetOpen}
          itemTitle={itemTitle}
        />
      )}
    </>
  );
}
