import {
  captureSuggestionDraft,
  createRoomSuggestion,
} from "@/api/v2/watchTogetherSuggestionCreate";
import { selectRoomItem } from "@/api/v2/watchTogetherSelection";
import { useCallback, useMemo, useState } from "react";
import { toast } from "sonner";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useT } from "@/i18n";
import type { ItemDetail } from "@/api/types";
import type { ActionBarWatchTogether } from "@/pages/ItemDetail/components/ActionBar";
import { useLiveRoomPresence } from "./hooks/useLiveRoomPresence";
import { StartPartySheet, type StartPartyTarget } from "./StartPartySheet";

/**
 * Wires a detail page's item into the Watch Together menu group. Returns the
 * prop the action bar renders and the sheet to mount alongside it.
 */
export function useDetailWatchTogether({
  item,
  target,
  seriesId,
  initialSeasonNumber,
}: {
  item: ItemDetail;
  /** The playable item this page would start: the movie, the episode, or a series' next-up episode. */
  target: StartPartyTarget | null;
  seriesId?: string;
  initialSeasonNumber?: number;
}) {
  const t = useT();
  const navigate = useViewTransitionNavigate();
  const live = useLiveRoomPresence();
  const [open, setOpen] = useState(false);

  const suggestToLive = useCallback(async () => {
    if (!live || !target) return;
    const draft = captureSuggestionDraft(live.room_id, live.token, {
      content_id: target.content_id,
      content_type: item.type === "movie" ? "movie" : "episode",
      title: target.title,
      subtitle: target.subtitle ?? "",
      poster_url: target.poster_url ?? "",
    });
    try {
      await createRoomSuggestion(draft);
      if (draft.authority && isCapturedProfileAuthorityActive(draft.authority)) {
        toast.success(t("web.watchTogether.detail.suggestedTo", { code: live.code }), {
          action: {
            label: t("web.watchTogether.detail.openRoom"),
            onClick: () =>
              navigate(`/rooms/${live.room_id}?room_token=${encodeURIComponent(live.token)}`),
          },
        });
      }
    } catch (error) {
      if (error instanceof StaleApiRequestContextError) return;
      toast.error(
        error instanceof Error ? error.message : t("web.watchTogether.common.suggestFailed"),
      );
    }
  }, [item.type, live, navigate, t, target]);

  const playInLive = useCallback(async () => {
    if (!live || !target) return;
    const authority = captureProfileRequestContext();
    try {
      await selectRoomItem(
        live.room_id,
        { content_id: target.content_id, library_id: target.library_id },
        authority,
      );
      if (authority && isCapturedProfileAuthorityActive(authority)) {
        navigate(`/rooms/${live.room_id}?room_token=${encodeURIComponent(live.token)}`);
      }
    } catch (error) {
      if (error instanceof StaleApiRequestContextError) return;
      toast.error(
        error instanceof Error
          ? error.message
          : t("web.watchTogether.detail.playFailed", { code: live.code }),
      );
    }
  }, [live, navigate, t, target]);

  const menu = useMemo<ActionBarWatchTogether | undefined>(() => {
    if (!target && !seriesId) return undefined;
    return {
      onStartParty: () => setOpen(true),
      liveRoom:
        live && target
          ? {
              code: live.code,
              onSuggest: () => void suggestToLive(),
              // Direct selection is the host's move in a host-pick room.
              onPlay:
                live.role === "host" && live.selection_mode === "host_pick"
                  ? () => void playInLive()
                  : undefined,
            }
          : undefined,
    };
  }, [live, playInLive, seriesId, suggestToLive, target]);

  const sheet = (
    <StartPartySheet
      open={open}
      onOpenChange={setOpen}
      item={item}
      initialTarget={target}
      seriesId={seriesId}
      initialSeasonNumber={initialSeasonNumber}
    />
  );

  return { menu, sheet };
}
