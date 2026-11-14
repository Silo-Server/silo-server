import { useCallback, useState } from "react";
import { toast } from "sonner";
import { t } from "@/i18n";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import type { WatchTogetherRoomConnectionResult } from "@/player/hooks/useWatchTogetherRoomConnection";
import type {
  SelectWatchTogetherRoomItemInput,
  WatchTogetherSelectionMode,
} from "@/lib/watchTogether";

/**
 * Toast-wrapped lobby actions. Every action captures authority before the
 * call and only reports under that same authority, so feedback never lands
 * in a replaced profile's session. Errors that mean "you were replaced" are
 * silent; everything else is a toast.
 */
export function useRoomActions(connection: WatchTogetherRoomConnectionResult) {
  const [busy, setBusy] = useState<null | "stage" | "start" | "stop" | "mode" | "promote">(null);

  const run = useCallback(
    async <T>(
      kind: NonNullable<typeof busy>,
      action: () => Promise<T>,
      success?: (value: T) => string | null,
      failure = t("web.watchTogether.actions.genericFailed"),
    ): Promise<T | null> => {
      const authority = captureProfileRequestContext();
      setBusy(kind);
      try {
        const value = await action();
        if (authority && isCapturedProfileAuthorityActive(authority)) {
          const message = success?.(value);
          if (message) toast.success(message);
        }
        return value;
      } catch (error) {
        if (error instanceof StaleApiRequestContextError) return null;
        if (authority && isCapturedProfileAuthorityActive(authority))
          toast.error(error instanceof Error ? error.message : failure);
        return null;
      } finally {
        setBusy((current) => (current === kind ? null : current));
      }
    },
    [],
  );

  const stage = useCallback(
    (input: SelectWatchTogetherRoomItemInput) =>
      run(
        "stage",
        () => connection.stageItem(input),
        () => null,
        t("web.watchTogether.actions.stageFailed"),
      ),
    [connection, run],
  );

  const start = useCallback(
    () =>
      run(
        "start",
        () => connection.startPlayback(),
        (room) => (room?.phase === "playing" ? t("web.watchTogether.actions.starting") : null),
        t("web.watchTogether.actions.startFailed"),
      ),
    [connection, run],
  );

  const stop = useCallback(
    () =>
      run(
        "stop",
        () => connection.stopPlayback(),
        (room) => (room?.phase === "lobby" ? t("web.watchTogether.actions.stopped") : null),
        t("web.watchTogether.actions.stopFailed"),
      ),
    [connection, run],
  );

  const switchMode = useCallback(
    (mode: WatchTogetherSelectionMode) =>
      run(
        "mode",
        () => connection.updateSelectionMode(mode),
        (room) =>
          room
            ? room.selection_mode === "vote"
              ? t("web.watchTogether.actions.modeVote")
              : t("web.watchTogether.actions.modeHostPick")
            : null,
        t("web.watchTogether.actions.modeFailed"),
      ),
    [connection, run],
  );

  const promote = useCallback(
    (suggestionId: string) =>
      run(
        "promote",
        () => connection.promoteSuggestion(suggestionId),
        (room) => (room ? t("web.watchTogether.actions.starting") : null),
        t("web.watchTogether.actions.promoteFailed"),
      ),
    [connection, run],
  );

  const setReady = useCallback(
    (ready: boolean) => {
      const sent = connection.setLobbyReady(ready);
      if (!sent.ok) toast.error(t("web.watchTogether.actions.notConnected"));
      return sent.ok;
    },
    [connection],
  );

  return { busy, stage, start, stop, switchMode, promote, setReady };
}
