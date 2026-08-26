import {
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useMemo,
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
  X,
} from "lucide-react";
import { useLocation } from "react-router";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import type { ItemDetail, MediaItemUserState } from "@/api/types";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useRefreshItemMetadata, useWatchedStateMutation } from "@/hooks/queries/items";
import { type DismissHomeItemVariables, useDismissHomeItem } from "@/hooks/queries/homeDismissals";
import { useToggleFavorite } from "@/hooks/queries/favorites";
import { useToggleWatchlist } from "@/hooks/queries/watchlist";
import { getWatchedActionLabel } from "@/pages/ItemDetail/watchedState";
import EditMetadataDialog from "@/components/EditMetadataDialog";
import MangaFilesDialog from "@/components/MangaFilesDialog";
import MatchItemDialog from "@/components/MatchItemDialog";
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
import { canCurateMetadata as canCurateMetadataForUser } from "@/lib/permissions";
import {
  mediaItemMenuIconClassName,
  mediaItemMenuTriggerClassName,
  type PosterActionDensity,
} from "@/components/mediaItemMenuTrigger";
import { useUICustomization } from "@/hooks/useUICustomization";
import { MediaActionIcon } from "@/components/mediaActionIcons";

type MediaItemType = ItemDetail["type"];

type MediaItemMenuEntry =
  | {
      kind: "action";
      key:
        | "playFromBeginning"
        | "toggleWatched"
        | "toggleFavorite"
        | "toggleWatchlist"
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
  /** When false, hides favorites and watchlist actions (e.g. for episodes). Defaults to true. */
  showCollectionActions?: boolean;
  /** Hides only the poster heart shortcut while retaining collection actions in the menu. */
  showFavoriteShortcut?: boolean;
  dismissAction?: DismissHomeItemVariables;
  hasPartialProgress?: boolean;
  /** Enables the watched shortcut on wide cards such as Continue Watching. */
  showWatchedShortcut?: boolean;
  /** Uses smaller poster controls on narrow catalog cards. */
  narrowPosterActions?: boolean;
  /** Card root whose long press opens the touch action sheet. */
  longPressRef?: RefObject<HTMLElement | null>;
  /** Heading for the touch action sheet. */
  itemTitle?: string;
}

export function buildMediaItemMenuModel({
  mediaType,
  userState,
  hasPartialProgress = false,
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

  if (userState) {
    if (showCollectionActions) {
      entries.push(
        {
          kind: "action",
          key: "toggleFavorite",
          label: userState.is_favorite ? "Remove from Favorites" : "Add to Favorites",
        },
        {
          kind: "action",
          key: "toggleWatchlist",
          label: userState.in_watchlist ? "Remove from Watchlist" : "Add to Watchlist",
        },
      );
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
    return action === "edit" ? (
      <EditMetadataDialog item={item} open onOpenChange={(open) => !open && onClose()} />
    ) : (
      <MatchItemDialog
        key={item.content_id}
        item={libraryId === undefined ? item : { ...item, library_id: libraryId }}
        open
        onOpenChange={(open) => !open && onClose()}
      />
    );
  }

  const actionLabel = action === "edit" ? "Edit Metadata" : "Match Item";
  const loading = isLoading || isFetching;

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>{actionLabel}</DialogTitle>
          <DialogDescription>
            {loading ? "Loading the latest item details…" : "The item details could not be loaded."}
          </DialogDescription>
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
            <Button type="button" variant="outline" size="sm" onClick={() => void refetch()}>
              Try Again
            </Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
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
  showWatchedShortcut = false,
  narrowPosterActions = false,
  longPressRef,
  itemTitle,
}: MediaItemMenuProps) {
  const navigate = useViewTransitionNavigate();
  const location = useLocation();
  const playbackController = useWatchPlaybackController();
  const user = useOptionalAuth()?.user;
  const { profile: currentProfile, hasSelectedProfile } = useCurrentProfile();
  const profileIsResolved = !hasSelectedProfile || Boolean(currentProfile);
  const isAdmin = useIsActingAdmin();
  const canCurateMetadata = profileIsResolved && canCurateMetadataForUser(user, currentProfile);
  const { cardPresentation } = useUICustomization();
  const [currentUserState, setCurrentUserState] = useState(userState);
  const lastSyncedUserStateRef = useRef(userState);
  const [refreshDialogOpen, setRefreshDialogOpen] = useState(false);
  const [filesDialogOpen, setFilesDialogOpen] = useState(false);
  const [metadataAction, setMetadataAction] = useState<MetadataAction | null>(null);
  const [actionSheetOpen, setActionSheetOpen] = useState(false);
  const menuTriggerRef = useRef<HTMLButtonElement>(null);
  const lastMenuInteractionRef = useRef<"keyboard" | "pointer" | null>(null);
  const pointerClosedMenuRef = useRef(false);

  useEffect(() => {
    const lastSynced = lastSyncedUserStateRef.current;
    if (
      lastSynced?.played === userState?.played &&
      lastSynced?.is_favorite === userState?.is_favorite &&
      lastSynced?.in_watchlist === userState?.in_watchlist
    ) {
      return;
    }
    lastSyncedUserStateRef.current = userState;
    setCurrentUserState(userState);
  }, [userState]);

  const watchedMutation = useWatchedStateMutation({
    content_id: contentId,
    type: mediaType,
    user_data: currentUserState ? { played: currentUserState.played } : undefined,
  });
  const favoriteMutation = useToggleFavorite(contentId);
  const watchlistMutation = useToggleWatchlist(contentId);
  const refreshMetadataMutation = useRefreshItemMetadata();
  const dismissHomeItemMutation = useDismissHomeItem();
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
  const currentHref = useMemo(
    () => `${location.pathname}${location.search}`,
    [location.pathname, location.search],
  );

  const model = buildMediaItemMenuModel({
    mediaType,
    userState: currentUserState,
    hasPartialProgress,
    isAdmin,
    canCurateMetadata,
    showCollectionActions,
    dismissLabel,
  });
  useLongPress(longPressRef, {
    onLongPress: () => setActionSheetOpen(true),
    enabled: model.length > 0,
  });
  const showPosterFavorite =
    variant === "poster" &&
    showFavoriteShortcut &&
    model.some((entry) => entry.kind === "action" && entry.key === "toggleFavorite");
  const hasWatchedAction = model.some(
    (entry) => entry.kind === "action" && entry.key === "toggleWatched",
  );
  const rootPosterSupportsWatchedShortcut =
    variant === "poster" && (mediaType === "movie" || mediaType === "series");
  const showWatchedQuickAction =
    hasWatchedAction && (rootPosterSupportsWatchedShortcut || showWatchedShortcut);
  const posterActionDensity: PosterActionDensity =
    variant === "poster" && narrowPosterActions
      ? "narrow"
      : variant === "poster" && cardPresentation.poster_size === "compact"
        ? "compact"
        : "standard";

  const isPending =
    watchedMutation.isPending ||
    favoriteMutation.isPending ||
    watchlistMutation.isPending ||
    refreshMetadataMutation.isPending ||
    dismissHomeItemMutation.isPending;

  const triggerClassName = mediaItemMenuTriggerClassName(variant, posterActionDensity);

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
        playbackController.startPlayback({
          contentId,
          restart: true,
          returnHref: currentHref,
        });
        return;
      }
      case "toggleWatched": {
        await handleWatchedToggle();
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
      case "viewDetails": {
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

  function handleRefreshConfirm(mode: "quick" | "complete") {
    setRefreshDialogOpen(false);
    refreshMetadataMutation.mutate({ item: { content_id: contentId, type: mediaType }, mode });
  }

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
        >
          {showWatchedQuickAction && (
            <WatchedQuickActionButton
              mediaType={mediaType}
              isWatched={currentUserState.played}
              isPending={watchedMutation.isPending}
              variant={variant}
              density={posterActionDensity}
              onToggle={() => {
                void handleWatchedToggle();
              }}
            />
          )}
          {showPosterFavorite && (
            <PosterCardFavoriteButton
              isFavorite={currentUserState.is_favorite}
              isPending={favoriteMutation.isPending}
              density={posterActionDensity}
              onToggle={() => {
                void handleFavoriteToggle();
              }}
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
      >
        {model.length === 0 ? (
          <button type="button" aria-label="More actions" disabled className={triggerClassName}>
            <MoreVertical className={mediaItemMenuIconClassName(variant, posterActionDensity)} />
          </button>
        ) : (
          <DropdownMenu
            modal={false}
            onOpenChange={(open) => {
              if (open) {
                pointerClosedMenuRef.current = false;
                lastMenuInteractionRef.current = null;
                return;
              }

              pointerClosedMenuRef.current = lastMenuInteractionRef.current === "pointer";
              lastMenuInteractionRef.current = null;
            }}
          >
            <DropdownMenuTrigger asChild>
              <button
                ref={menuTriggerRef}
                type="button"
                aria-label="More actions"
                className={triggerClassName}
                onPointerDown={() => {
                  lastMenuInteractionRef.current = "pointer";
                }}
                onKeyDown={() => {
                  lastMenuInteractionRef.current = "keyboard";
                }}
              >
                <MoreVertical
                  className={mediaItemMenuIconClassName(variant, posterActionDensity)}
                />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="end"
              className="w-max max-w-[calc(100vw-2rem)] min-w-0"
              onPointerDownOutside={() => {
                lastMenuInteractionRef.current = "pointer";
              }}
              onPointerDownCapture={() => {
                lastMenuInteractionRef.current = "pointer";
              }}
              onKeyDownCapture={() => {
                lastMenuInteractionRef.current = "keyboard";
              }}
              onCloseAutoFocus={(event) => {
                if (pointerClosedMenuRef.current) {
                  event.preventDefault();
                  menuTriggerRef.current?.blur();
                }
                pointerClosedMenuRef.current = false;
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
                    onSelect={() => {
                      void handleAction(entry.key);
                    }}
                  >
                    <MediaItemMenuActionIcon
                      actionKey={entry.key}
                      userState={currentUserState}
                      isRefreshing={refreshMetadataMutation.isPending}
                    />
                    {entry.label}
                  </DropdownMenuItem>
                );
              })}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      <MediaItemActionSheet
        open={actionSheetOpen}
        onOpenChange={setActionSheetOpen}
        title={itemTitle}
        entries={model}
        userState={currentUserState}
        isPending={isPending}
        isRefreshing={refreshMetadataMutation.isPending}
        onSelectAction={(actionKey) => {
          setActionSheetOpen(false);
          void handleAction(actionKey);
        }}
      />
      <RefreshMetadataDialog
        open={refreshDialogOpen}
        onOpenChange={setRefreshDialogOpen}
        onConfirm={handleRefreshConfirm}
        isPending={refreshMetadataMutation.isPending}
      />
      {metadataAction && (
        <MetadataActionDialogHost
          action={metadataAction}
          contentId={contentId}
          libraryId={libraryId}
          onClose={() => setMetadataAction(null)}
        />
      )}
      {mediaType === "manga" && (
        <MangaFilesDialog
          contentId={contentId}
          open={filesDialogOpen}
          onOpenChange={setFilesDialogOpen}
        />
      )}
    </>
  );
}
