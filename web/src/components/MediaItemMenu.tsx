import { useEffect, useMemo, useState } from "react";
import { MoreVertical, RefreshCw } from "lucide-react";
import { useLocation } from "react-router";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import type { ItemDetail, MediaItemUserState } from "@/api/types";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useRefreshItemMetadata, useWatchedStateMutation } from "@/hooks/queries/items";
import { type DismissHomeItemVariables, useDismissHomeItem } from "@/hooks/queries/homeDismissals";
import { useToggleFavorite } from "@/hooks/queries/favorites";
import { useToggleWatchlist } from "@/hooks/queries/watchlist";
import RefreshMetadataDialog from "@/components/RefreshMetadataDialog";
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
        | "viewPlayHistory"
        | "refreshMetadata";
      label: string;
    }
  | { kind: "separator" };

interface BuildMediaItemMenuModelOptions {
  mediaType: MediaItemType;
  userState?: MediaItemUserState;
  hasPartialProgress?: boolean;
  isAdmin: boolean;
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
      label: isAudiobook
        ? userState.played
          ? "Mark Unlistened"
          : "Mark Listened"
        : userState.played
          ? "Mark Unwatched"
          : "Mark Watched",
    });

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

  if (isAdmin) {
    if (entries.length > 0) {
      entries.push({ kind: "separator" });
    }
    entries.push(
      {
        kind: "action",
        key: "viewPlayHistory",
        label: "View Play History",
      },
      {
        kind: "action",
        key: "refreshMetadata",
        label: "Refresh Metadata",
      },
    );
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
  const auth = useOptionalAuth();
  const isAdmin = auth?.user?.role === "admin";
  const [currentUserState, setCurrentUserState] = useState(userState);
  const [refreshDialogOpen, setRefreshDialogOpen] = useState(false);

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
    showCollectionActions,
    dismissLabel,
  });

  const isPending =
    watchedMutation.isPending ||
    favoriteMutation.isPending ||
    watchlistMutation.isPending ||
    refreshMetadataMutation.isPending ||
    dismissHomeItemMutation.isPending;

  const triggerClassName = cn(
    "inline-flex items-center justify-center rounded-md border border-border/20 bg-background/60 text-foreground shadow-sm backdrop-blur-sm transition-[opacity,background-color] duration-150 hover:bg-background/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/70",
    variant === "wide" ? "size-9" : "size-8",
    "opacity-100 md:opacity-0 md:group-hover/card:opacity-100 md:group-focus-within/card:opacity-100",
  );

  async function handleAction(actionKey: Extract<MediaItemMenuEntry, { kind: "action" }>["key"]) {
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
        if (!currentUserState) return;
        await favoriteMutation.mutateAsync(currentUserState.is_favorite);
        setCurrentUserState((prev) => (prev ? { ...prev, is_favorite: !prev.is_favorite } : prev));
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
    }
  }

  function handleRefreshConfirm(mode: "quick" | "complete") {
    setRefreshDialogOpen(false);
    refreshMetadataMutation.mutate({ item: { content_id: contentId, type: mediaType }, mode });
  }

  return (
    <>
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
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger asChild>
              <button type="button" aria-label="More actions" className={triggerClassName}>
                <MoreVertical className={variant === "wide" ? "size-5" : "size-4"} />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
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
                    {entry.key === "refreshMetadata" && refreshMetadataMutation.isPending ? (
                      <RefreshCw className="size-4 animate-spin" />
                    ) : null}
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
    </>
  );
}
