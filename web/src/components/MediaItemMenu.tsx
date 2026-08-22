import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Check,
  FileText,
  Heart,
  History,
  LoaderCircle,
  MoreVertical,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  X,
} from "lucide-react";
import { useLocation } from "react-router";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import type { ItemDetail, MediaItemUserState } from "@/api/types";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
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
import { cn } from "@/lib/utils";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { buildMediaPlayHref } from "@/lib/mediaNavigation";
import {
  canCurateMetadata as canCurateMetadataForUser,
  isActingAdmin as isActingAdminForUser,
} from "@/lib/permissions";
import { mediaItemMenuTriggerClassName } from "@/components/mediaItemMenuTrigger";

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
  dismissAction?: DismissHomeItemVariables;
  hasPartialProgress?: boolean;
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
      return <Check aria-hidden="true" className="size-4" />;
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
      return <History aria-hidden="true" className="size-4" />;
    case "refreshMetadata":
      return (
        <RefreshCw aria-hidden="true" className={cn("size-4", isRefreshing && "animate-spin")} />
      );
    case "editMetadata":
      return <Pencil aria-hidden="true" className="size-4" />;
    case "matchItem":
      return <Search aria-hidden="true" className="size-4" />;
  }
}

export function PosterCardFavoriteButton({
  isFavorite,
  isPending,
  onToggle,
}: {
  isFavorite: boolean;
  isPending: boolean;
  onToggle: () => void;
}) {
  const [isAnimating, setIsAnimating] = useState(false);
  const pointerStartRef = useRef<{
    pointerId: number;
    clientX: number;
    clientY: number;
    maxMovement: number;
  } | null>(null);
  const label = isFavorite ? "Remove from favorites" : "Add to favorites";

  const activateFavorite = useCallback(() => {
    if (isPending) return;
    if (!isFavorite) setIsAnimating(true);
    onToggle();
  }, [isFavorite, isPending, onToggle]);

  useEffect(() => {
    if (!isAnimating) return;
    const timeout = window.setTimeout(() => setIsAnimating(false), 420);
    return () => window.clearTimeout(timeout);
  }, [isAnimating]);

  return (
    <div
      className="absolute bottom-2.5 left-2.5 z-20"
      onClick={stopMenuEvent}
      onPointerDown={stopMenuEvent}
    >
      <button
        type="button"
        aria-label={label}
        aria-pressed={isFavorite}
        title={label}
        disabled={isPending}
        className={cn(
          mediaItemMenuTriggerClassName("poster"),
          "relative cursor-pointer overflow-visible disabled:opacity-70",
          isFavorite && "text-red-500 hover:text-red-400",
        )}
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

          const movement = Math.max(
            pointerStart.maxMovement,
            Math.abs(event.clientX - pointerStart.clientX),
            Math.abs(event.clientY - pointerStart.clientY),
          );
          if (movement > 10) return;

          event.preventDefault();
          event.stopPropagation();
          activateFavorite();
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
          // Embla consumes the click following a carousel drag. Pointer taps are
          // handled on pointerup above; detail=0 preserves keyboard/AT activation.
          if (event.detail === 0) activateFavorite();
        }}
      >
        {isAnimating && (
          <span
            aria-hidden="true"
            data-testid="favorite-burst"
            className="absolute top-1/2 left-1/2 size-7 -translate-x-1/2 -translate-y-1/2 animate-ping rounded-full bg-red-500/30 motion-reduce:hidden"
          />
        )}
        <Heart
          className={cn(
            "relative size-4 transition-[transform,color,fill] duration-300 ease-out motion-reduce:transition-none",
            isFavorite && "scale-110 fill-red-500 text-red-500",
            isAnimating && "scale-125",
          )}
        />
      </button>
    </div>
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
  dismissAction,
  hasPartialProgress = false,
}: MediaItemMenuProps) {
  const navigate = useViewTransitionNavigate();
  const location = useLocation();
  const playbackController = useWatchPlaybackController();
  const user = useOptionalAuth()?.user;
  const { profile: currentProfile, hasSelectedProfile } = useCurrentProfile();
  const profileIsResolved = !hasSelectedProfile || Boolean(currentProfile);
  const isAdmin = profileIsResolved && isActingAdminForUser(user, currentProfile);
  const canCurateMetadata = profileIsResolved && canCurateMetadataForUser(user, currentProfile);
  const [currentUserState, setCurrentUserState] = useState(userState);
  const [refreshDialogOpen, setRefreshDialogOpen] = useState(false);
  const [filesDialogOpen, setFilesDialogOpen] = useState(false);
  const [metadataAction, setMetadataAction] = useState<MetadataAction | null>(null);
  const menuTriggerRef = useRef<HTMLButtonElement>(null);
  const lastMenuInteractionRef = useRef<"keyboard" | "pointer" | null>(null);
  const pointerClosedMenuRef = useRef(false);

  useEffect(() => {
    setCurrentUserState(userState);
  }, [userState?.played, userState?.is_favorite, userState?.in_watchlist]);

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
  const showPosterFavorite =
    variant === "poster" &&
    model.some((entry) => entry.kind === "action" && entry.key === "toggleFavorite");

  const isPending =
    watchedMutation.isPending ||
    favoriteMutation.isPending ||
    watchlistMutation.isPending ||
    refreshMetadataMutation.isPending ||
    dismissHomeItemMutation.isPending;

  const triggerClassName = mediaItemMenuTriggerClassName(variant);

  async function handleFavoriteToggle() {
    if (!currentUserState || favoriteMutation.isPending) return;
    const wasFavorite = currentUserState.is_favorite;
    setCurrentUserState((previous) =>
      previous ? { ...previous, is_favorite: !wasFavorite } : previous,
    );
    try {
      await favoriteMutation.mutateAsync(wasFavorite);
    } catch {
      setCurrentUserState((previous) =>
        previous ? { ...previous, is_favorite: wasFavorite } : previous,
      );
    }
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
        if (!currentUserState) return;
        const nextPlayed = !currentUserState.played;
        await watchedMutation.mutateAsync(nextPlayed);
        setCurrentUserState((prev) => (prev ? { ...prev, played: nextPlayed } : prev));
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
      {showPosterFavorite && currentUserState && (
        <PosterCardFavoriteButton
          isFavorite={currentUserState.is_favorite}
          isPending={favoriteMutation.isPending}
          onToggle={() => {
            void handleFavoriteToggle();
          }}
        />
      )}
      <div
        className={cn(
          "absolute z-20",
          variant === "wide" ? "right-3 bottom-3" : "right-2.5 bottom-2.5",
        )}
        onClick={stopMenuEvent}
        onPointerDown={stopMenuEvent}
      >
        {model.length === 0 ? (
          <button type="button" aria-label="More actions" disabled className={triggerClassName}>
            <MoreVertical className={variant === "wide" ? "size-5" : "size-4"} />
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
                <MoreVertical className={variant === "wide" ? "size-5" : "size-4"} />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="end"
              className="w-max min-w-0"
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
